#!/usr/bin/env bash
#
# Build the nri-test suite on this host, run it on a test node, and put the
# node back the way it was found.
#
# The node needs no Go toolchain: the binary is built here with CGO_ENABLED=0,
# which makes it independent of the node's glibc, and copied over.
#
# A CRI-mode run cannot share a node with a running kubelet, which garbage-
# collects sandboxes it did not create. So kubelet is stopped for the run and
# restarted however the script leaves -- a failing suite, a closed terminal, an
# interrupt -- because a node left with kubelet down silently breaks every later
# Kubernetes-mode run, and that restore path is the reason this script exists at
# all. A restore that fails decides the exit code: see restore_kubelet.
#
# The restore is ordered after the remote suite's death, and that order is the
# delicate part. Killing the local ssh does not stop the far side: measured, a
# SIGTERM to this script left `sudo nri-test` running on the node for nine more
# tests -- eight of them pod-creating -- while the script exited and restarted
# kubelet. That is the hazard the stop exists to avoid, inflicted by the very
# cleanup meant to avoid it. So the suite is killed explicitly over a second ssh
# connection and confirmed gone before kubelet comes back; if it cannot be
# confirmed gone, kubelet is deliberately left down and the script says what to
# do by hand. See stop_suite.
#
# That kill is by the binary's path, because nothing on the remote command line
# distinguishes one invocation from another. What is unconditional is the
# confirmation, not the kill: the probe runs on every exit -- including a clean
# one, since an ssh round trip is cheap -- and a kill goes out only on the
# rounds where the probe did not report the suite gone. A clean run therefore
# issues one pgrep and no pkill at all.
# The consequence is the one assumption this script makes of its operator: ONE
# RUN AT A TIME PER NODE. A second run of the same binary on the same node, or
# a copy of it started by hand, is indistinguishable from this one's and will be
# killed as root, after which this script reports the suite gone and restores
# kubelet none the wiser. A node is not shareable during a run anyway -- the run
# stops its kubelet -- so this costs nothing that was not already lost.
#
# Usage: hack/run-nri-test.sh [runtime-handler] [extra nri-test flags...]
#
# The handler is the low-level runtime -- runc, crun -- or empty for the
# runtime's default. One handler per run, because the suite takes one; sweep
# by invoking this twice.
#
# It is positional and it comes first, so passing flags with the runtime's
# default handler means spelling the empty handler out:
#
#   hack/run-nri-test.sh '' -test.run=TestSmoke
#
# A first argument beginning with "-" is refused rather than taken as a handler
# name, since that mistake is what the usage line above invites.
#
# Exit codes: the suite's own, except 1 for a failure of this script's own
# steps (build, copy, validating REMOTE_BIN, deciding kubelet's state) and 2 for
# a node left needing a hand: kubelet's restart failed, or the remote suite
# could not be confirmed stopped -- in which case the restore is withheld too,
# if there was one to withhold. That is the one outcome that must never look
# green.
#
# The binary is left behind at $REMOTE_BIN on the node, deliberately: the next
# run's scp overwrites it, and a copy that survives the run is what lets a
# failure be re-run by hand on the node. Remove it by hand when a stale build
# must not linger -- in particular after running a locally patched build, which
# nothing here can tell apart from a clean one.
#
# Re-run it by hand only while no scripted run is in flight. The stop above
# matches that path, so a hand-run started during one gets TERM and then KILL
# as root, and this script will report it as the suite it meant to stop.
#
# Environment:
#   NODE          ssh destination of the test node (default vagrant@node)
#   SSH_CONFIG    ssh -F config file, if the node needs one
#   REMOTE_BIN    where to copy the binary (default /tmp/nri-test)
#   ENDPOINT      CRI endpoint; empty lets the suite autodetect
#   NRI_SOCKET    NRI socket (default /run/nri/nri.sock)
#   TIMEOUT       -nri-test.timeout value (default 60s)
#   DRIVER        -nri-test.driver value: cri (default) or k8s. The k8s driver
#                 needs kubelet running, so this script leaves it alone then.
#                 A ReplicaSet scalability burst needs it too and is selected
#                 by flags, not by this variable: pass
#                 -nri-test.scale-driver=k8s with -nri-test.scale-replicas
#                 and the script leaves kubelet alone without DRIVER=k8s
#                 switching every conformance test to the k8s driver.
#   K8S_KUBECONFIG  kubeconfig on the node for the k8s driver (default
#                 /etc/kubernetes/admin.conf)

set -o errexit -o nounset -o pipefail

NODE="${NODE:-vagrant@node}"
SSH_CONFIG="${SSH_CONFIG:-}"
REMOTE_BIN="${REMOTE_BIN:-/tmp/nri-test}"
ENDPOINT="${ENDPOINT:-}"
NRI_SOCKET="${NRI_SOCKET:-/run/nri/nri.sock}"
TIMEOUT="${TIMEOUT:-60s}"
DRIVER="${DRIVER:-cri}"
K8S_KUBECONFIG="${K8S_KUBECONFIG:-/etc/kubernetes/admin.conf}"

# The handler is positional and first, so a flag in that slot is taken as a
# handler name: `run-nri-test.sh -test.run=TestSmoke` becomes
# `-nri-test.runtime-handler -test.run=TestSmoke`. That fails loudly in Setup and
# the restore still runs, so it costs an operator a round trip rather than
# leaving a node broken -- but the usage line invites it, so refuse it here and
# name the form that does what was meant.
case "${1-}" in
-*)
    echo "ERROR: the first argument is the runtime handler (runc, crun, or ''" \
        "for the runtime's default), not a flag. To pass flags and use the" \
        "runtime's default handler, name the empty handler first:" \
        "$0 '' $*" >&2
    exit 1
    ;;
esac

handler="${1-}"
[ $# -gt 0 ] && shift

# REMOTE_BIN ends up inside a remote `sudo pkill -f` pattern, quoted into a
# command this script hands to the node's shell as root. So it is restricted to
# an absolute path of ordinary path characters, which is what makes it safe to
# interpolate: nothing in that class is special to a shell, through one ssh and
# two shells, so the value needs no quoting of its own.
#
# What the class does NOT do is bound what the pattern matches. /bin/sh is a
# legal value and would make the kill below a node-wide one. Nothing here can
# tell a sensible path from a reckless one; this check is about quoting, and the
# blast radius is the caller's to get right.
#
# Checked here, before anything is built, copied or stopped, so a bad value
# costs nothing.
case "$REMOTE_BIN" in
"" | [!/]* | *[!A-Za-z0-9/._-]*)
    echo "ERROR: REMOTE_BIN ($REMOTE_BIN) must be an absolute path of letters," \
        "digits, '/', '.', '_' and '-': it is interpolated into a 'sudo pkill'" \
        "pattern on the node" >&2
    exit 1
    ;;
esac

# The pattern that identifies the suite's processes on the node. Matched against
# whole command lines (pkill -f), anchored, and against $REMOTE_BIN's path
# rather than a bare name: a loose pattern run through sudo on a shared node
# could match something this script has no business killing. The optional "sudo"
# covers both processes of the pair -- `sudo $REMOTE_BIN ...` and the suite it
# spawns -- and the trailing space both keeps a longer path from matching and,
# because the anchor then cannot match "sudo pkill ..." or "sudo pgrep ...",
# keeps the kill and the check from finding themselves.
#
# The dots are escaped because this is an extended regular expression, where an
# unescaped "." matches any character: /tmp/nri-test.v2 would otherwise also
# match /tmp/nri-testXv2. The default path has no dot, so this has never been
# live, which is exactly why it is easy to leave broken.
#
# What the pattern cannot do is tell one run from another. Nothing on the remote
# command line distinguishes this invocation, so the kill is by binary, not by
# run: see the header on running one at a time per node.
suite_pattern="^(sudo )?${REMOTE_BIN//./\\.} "

top_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

ssh_opts=()
[ -n "$SSH_CONFIG" ] && ssh_opts=(-F "$SSH_CONFIG")

on_node() {
    ssh "${ssh_opts[@]}" "$NODE" "$@"
}

# on_node_tty runs a command on the node through a pty. See the suite
# invocation below for why that matters there and nowhere else. A separate
# function rather than an option passed to on_node, because ssh stops reading
# options at the destination: `ssh host -tt cmd` sends "-tt" to the remote
# shell as part of the command.
on_node_tty() {
    ssh "${ssh_opts[@]}" -tt "$NODE" "$@"
}

kubelet_was_active=""
kubelet_restored=""
restore_failed=""
stop_unconfirmed=""
suite_launched=""
# Why this run needs kubelet left running, or empty. Declared here rather than
# only where it is assigned, because it is read again when the suite's
# arguments are built and a node whose kubelet was already down never reaches
# the assignment -- which under `set -o nounset` would abort the run.
kubelet_reason=""
suite_pid=""

# kill_remote_suite signals the suite's processes on the node, over an ssh
# connection of its own. Its result is deliberately ignored: pkill exits 1 when
# it matched nothing, which is the outcome being aimed at, and whether the kill
# landed is not pkill's word to take anyway -- remote_suite_state is.
kill_remote_suite() {
    on_node "sudo pkill -$1 -f '$suite_pattern'" > /dev/null 2>&1 || true
}

# remote_suite_state echoes gone, alive or unknown.
#
# It refuses to read a bare exit status, for the same reason the kubelet check
# below does. pgrep exits 1 for "no match" -- and so does sudo when it declines,
# while ssh exits 255 for its own failures, and reading either of those as "no
# match" would report a running suite gone and let kubelet back on top of it.
#
# So pgrep's own status comes back in a sentinel, and the sentinel is printed by
# the command sudo ran rather than after it: a sudo that never ran anything
# prints nothing, which is exactly the point. Anything that does not come back
# as the sentinel is unknown rather than gone.
remote_suite_state() {
    local out=""
    local probe="pgrep -f \"$suite_pattern\" > /dev/null 2>&1
        echo NRI_PGREP:\$?"

    out="$(on_node "sudo sh -c '$probe'" 2>/dev/null)" || true

    case "$out" in
    *NRI_PGREP:0*) echo alive ;;
    *NRI_PGREP:1*) echo gone ;;
    *) echo unknown ;;
    esac
}

# stop_suite makes sure the suite is not running on the node any more, and is
# what the restore is ordered after.
#
# That order is the whole point: starting kubelet while the suite is still
# creating pod sandboxes inflicts exactly the hazard the stop exists to avoid --
# kubelet garbage-collecting sandboxes it did not create -- only this time
# inflicted by the cleanup. So the kill goes out before the restore, and the
# restore waits on a positive confirmation that the suite is gone.
#
# Killing the local ssh is not that confirmation and was measured not to be a
# kill at all: with the pty in place, a SIGTERM to this script left the remote
# `sudo nri-test` running through nine further tests. The pty is the wrong layer
# -- sudo's use_pty, suite-side handlers and the trap shape were each tested and
# ruled out -- so the kill is issued over a second connection instead, by the
# shipped binary's path, and nothing here depends on pty semantics.
#
# The check runs on every path that launched the suite, not only on an
# interrupt: a run whose ssh died of its own accord (255) says nothing about
# what is still running on the node, and "confirmed gone" is cheap enough to be
# unconditional.
#
# An interrupted suite leaves its own pods behind -- it has no signal handlers,
# so its cleanup does not run. Those are for kubelet to collect once it is back,
# which is harmless; it is only the live suite that must not meet it.
stop_suite() {
    [ -n "$suite_launched" ] || return 0

    echo "--- making sure the remote suite is gone before restoring kubelet"

    # Kill the far side first, then reap the local ssh. The other order hangs:
    # the local `wait` would sit there until the suite finished on its own,
    # which is the bug this replaced.
    if [ -n "$suite_pid" ]; then
        kill_remote_suite TERM
        kill -TERM "$suite_pid" 2>/dev/null || true
        wait "$suite_pid" 2>/dev/null || true
        suite_pid=""
    fi

    # Up to ten rounds, roughly ten seconds. A kill on the first round that
    # still finds the suite, because this is also the path for a run nobody
    # interrupted; SIGKILL on the fourth, since a suite that ignored a TERM it
    # has no handler for is not going to answer a second one. An unknown state
    # is retried too: a single failed ssh is not grounds for leaving a node
    # down.
    local state=unknown attempt=1
    while [ "$attempt" -le 10 ]; do
        state="$(remote_suite_state)"
        if [ "$state" = gone ]; then
            break
        fi
        if [ "$attempt" = 1 ]; then
            kill_remote_suite TERM
        elif [ "$attempt" = 4 ]; then
            kill_remote_suite KILL
        fi
        sleep 1
        attempt=$((attempt + 1))
    done

    if [ "$state" = gone ]; then
        echo "--- the remote suite is gone"
        return 0
    fi

    # The one outcome that must be impossible is kubelet coming back over a live
    # suite, so an unconfirmed stop withholds the restore rather than risking
    # it. A node left down is loud, exits 2, and is one command to fix; a node
    # whose kubelet garbage-collects a running sweep's sandboxes is neither.
    stop_unconfirmed=yes
    echo "ERROR: the remote suite on $NODE could not be confirmed gone (it is" \
        "$state). On the node, run \"sudo pkill -KILL -f '$suite_pattern'\"" \
        "and check with \"sudo pgrep -af '$suite_pattern'\"" >&2

    # Only this script's own doing is this script's to undo. Told to start a
    # kubelet the run found stopped, an operator following these words would
    # change the node rather than restore it.
    [ -n "$kubelet_was_active" ] || return 0

    echo "ERROR: kubelet on $NODE is deliberately NOT being restarted while" \
        "that suite might still be running: starting it under a suite that is" \
        "still creating pod sandboxes is the hazard stopping it exists to" \
        "avoid. Once the suite is gone, run \"sudo systemctl start kubelet\"" >&2
}

# restore_kubelet starts kubelet again, if this script was the thing that
# stopped it.
#
# Idempotent on purpose. A signal trap exits, which runs the EXIT trap, which
# restores; the guard is what keeps a second entry from reporting a restore
# twice and from asking the node to start kubelet again.
#
# A failure here is not a warning. The script's whole job is the restore, so a
# node left with kubelet down must not be reported by a zero exit code; on_exit
# turns it into exit 2.
restore_kubelet() {
    [ -n "$kubelet_was_active" ] || return 0
    [ -z "$kubelet_restored" ] || return 0
    kubelet_restored=yes

    # No path through this function starts kubelet while the suite might still
    # be running: that is the whole reason stop_suite goes first. It has
    # already said what the operator must do by hand, so say nothing here.
    if [ -n "$stop_unconfirmed" ]; then
        return 0
    fi

    echo "--- restarting kubelet"
    if ! on_node sudo systemctl start kubelet; then
        echo "ERROR: failed to restart kubelet on $NODE. The node is left with" \
            "kubelet down, which will break every later Kubernetes-mode run;" \
            "start it by hand" >&2
        restore_failed=yes
    fi
}

on_exit() {
    local rc=$?

    stop_suite
    restore_kubelet

    # Either way the node is left with kubelet down, and that outranks whatever
    # the suite had to say.
    if [ -n "$restore_failed" ] || [ -n "$stop_unconfirmed" ]; then
        exit 2
    fi

    exit "$rc"
}

# One restoring trap, on EXIT, and the signal traps only exit into it. So the
# restore happens exactly once however the script leaves, and it happens for
# HUP too -- a closed terminal during a long sweep is the ordinary way this
# script gets interrupted, and EXIT alone does not run for it.
#
# An interrupted sweep must never report success. Two independent mechanisms
# see to that, and the harness exercises both: these traps, which exit 130
# before the verdict below is ever reached, and the 128+signal arm of that
# verdict, for a run whose remote end died of a signal while this script was
# not itself signalled.
#
# One thing about signals here is worth knowing before changing any of it, since
# it is easy to measure wrongly. Run normally -- foreground, terminal or not --
# all three signals are trappable and this script exits 130 -- or 2, which
# overrides it, when the remote suite could not be confirmed stopped, since a
# node needing a hand outranks the reason the run ended. Launched
# asynchronously by a non-interactive shell (`script.sh &`, which is how CI and
# an agent's background job launch it), POSIX requires SIGINT and SIGQUIT to be
# inherited *ignored*, and a signal ignored on entry cannot be trapped or reset:
# a Ctrl-C-equivalent then does nothing at all, the suite runs to completion,
# and the honest exit code is the suite's own. Measuring this script under a
# background launch therefore shows "an interrupt that reports success", and the
# right reading of that is not a defect here but that SIGINT cannot stop a
# backgrounded run: send it SIGTERM, which is never ignored.
#
# SIGKILL is the one signal nothing can cover, and a node it leaves behind needs
# `systemctl start kubelet` by hand.
trap on_exit EXIT
trap 'exit 130' HUP INT TERM

echo "--- building nri-test"
CGO_ENABLED=0 make -C "$top_dir" nri-test || {
    echo "ERROR: the build failed; nothing has been done on $NODE" >&2
    exit 1
}

echo "--- copying nri-test to $NODE:$REMOTE_BIN"
scp "${ssh_opts[@]}" "$top_dir/build/bin/nri-test" "$NODE:$REMOTE_BIN" || {
    echo "ERROR: copying the binary to $NODE:$REMOTE_BIN failed" >&2
    exit 1
}

# Copies every -nri-test.scale-delay-file to the node and rewrites its path
# into suite_args, which the launch below passes instead of "$@": the suite
# runs on the node and cannot see a local path. Remote names derive from
# $REMOTE_BIN, so they stay within the characters the unquoted remote command
# line allows, and are left behind like the binary.
shipped_delay_file=""
delay_files_sent=0

# ship_delay_file copies one delay-profile file to the node and sets
# shipped_delay_file to its path there.
ship_delay_file() {
    local src="$1"

    if [ ! -f "$src" ]; then
        # This exit is why the path is assigned and not printed: inside $(...)
        # it would end only the subshell and the run would go on.
        echo "ERROR: -nri-test.scale-delay-file $src does not exist or is not a file" >&2
        exit 1
    fi

    delay_files_sent=$((delay_files_sent + 1))
    shipped_delay_file="$REMOTE_BIN.delays.$delay_files_sent.yaml"

    echo "--- copying $src to $NODE:$shipped_delay_file"
    scp "${ssh_opts[@]}" "$src" "$NODE:$shipped_delay_file" || {
        echo "ERROR: copying $src to $NODE:$shipped_delay_file failed" >&2
        exit 1
    }
}

delay_file_flag="-nri-test.scale-delay-file"
suite_args=()
want_delay_file=""

for arg in "$@"; do
    if [ -n "$want_delay_file" ]; then
        want_delay_file=""

        ship_delay_file "$arg"
        suite_args+=("$shipped_delay_file")

        continue
    fi

    case "$arg" in
    "$delay_file_flag="*)
        ship_delay_file "${arg#"$delay_file_flag"=}"
        suite_args+=("$delay_file_flag=$shipped_delay_file")
        ;;
    "$delay_file_flag")
        want_delay_file=yes
        suite_args+=("$arg")
        ;;
    *)
        suite_args+=("$arg")
        ;;
    esac
done

if [ -n "$want_delay_file" ]; then
    echo "ERROR: $delay_file_flag was given with no path after it" >&2
    exit 1
fi

# flag_value NAME ARGS... prints the value of -nri-test.NAME among ARGS, in
# either the -flag=value or the -flag value spelling, or nothing if it is
# absent. The suite's own flags are read here rather than mirrored into another
# environment variable, so a flag passed straight through cannot disagree with
# the script's idea of what the run is doing.
flag_value() {
    local name="-nri-test.$1"
    shift

    local arg want_next=""

    for arg in "$@"; do
        if [ -n "$want_next" ]; then
            printf '%s' "$arg"

            return 0
        fi

        case "$arg" in
        "$name="*)
            printf '%s' "${arg#"$name"=}"

            return 0
            ;;
        "$name")
            want_next=yes
            ;;
        esac
    done
}

# needs_kubelet_reason prints why this run must have kubelet left running, or
# nothing when kubelet is this run's hazard rather than its precondition.
#
# There are two ways to need it and only one of them is an environment
# variable, which is the whole point. Deciding from DRIVER alone stopped
# kubelet for a ReplicaSet scalability burst -- a burst only kubelet can bring
# up -- so the suite then refused with "no kubelet process found", and the only
# workaround was DRIVER=k8s, which also switches every conformance test to the
# k8s driver and so measures something nobody asked for.
#
# The condition mirrors framework.Config.ScaleViaK8s: a scale driver of k8s AND
# a replica count above zero. A scale driver named without replicas is not a
# scalability run, and must not keep kubelet up over a CRI-mode sweep whose
# sandboxes it would collect.
needs_kubelet_reason() {
    if [ "$DRIVER" = k8s ]; then
        printf 'DRIVER=%s, whose workloads kubelet creates' "$DRIVER"

        return 0
    fi

    local replicas scale_driver
    replicas="$(flag_value scale-replicas "$@")"
    scale_driver="$(flag_value scale-driver "$@")"

    if [ "${scale_driver:-cri}" = k8s ] && [ "${replicas:-0}" -gt 0 ] 2>/dev/null; then
        printf -- '-nri-test.scale-driver=k8s with %s replicas, a ReplicaSet burst only kubelet can bring up' \
            "$replicas"

        return 0
    fi

    return 1
}

# Deciding kubelet's state, and refusing to guess. A non-zero `systemctl
# is-active` means inactive only when systemctl is what answered: ssh's own
# failures (255) and sudo's (1) are non-zero too, and reading either as
# "kubelet is not running" would start the run with kubelet still up, where the
# suite's own refusal is the only thing left to catch it -- or, worse, would
# skip the stop and so skip the restore.
# Asked once, before kubelet's own state is known, because it is a question
# about this run and not about the node: a ReplicaSet burst needs a cluster
# whether or not kubelet happens to be up, and the suite's arguments are built
# from the same answer further down.
kubelet_reason="$(needs_kubelet_reason "$@" || true)"

echo "--- checking kubelet on $NODE"
kubelet_rc=0
on_node sudo systemctl is-active --quiet kubelet || kubelet_rc=$?

case $kubelet_rc in
0)
    if [ -n "$kubelet_reason" ]; then
        # This run's workloads are kubelet's: stopping it would stop the run.
        echo "--- leaving kubelet running: $kubelet_reason"
    else
        kubelet_was_active=yes
        echo "--- stopping kubelet for the run"
        on_node sudo systemctl stop kubelet || {
            echo "ERROR: failed to stop kubelet on $NODE" >&2
            exit 1
        }
    fi
    ;;
3)
    # systemd's documented "inactive" status. Nothing to stop, so nothing to
    # restore either.
    echo "--- kubelet is not active on $NODE; leaving it alone"
    ;;
255)
    echo "ERROR: ssh to $NODE failed (rc 255), so kubelet's state is unknown;" \
        "refusing to run" >&2
    exit 1
    ;;
*)
    echo "ERROR: could not determine kubelet's state on $NODE (rc $kubelet_rc," \
        "which is neither active nor systemd's inactive); refusing to run" >&2
    exit 1
    ;;
esac

args=(
    -test.v
    -nri-test.collect-failures
    -nri-test.timeout "$TIMEOUT"
    -nri-socket "$NRI_SOCKET"
)
[ -n "$ENDPOINT" ] && args+=(-runtime-endpoint "$ENDPOINT")
[ -n "$handler" ] && args+=(-nri-test.runtime-handler "$handler")
if [ "$DRIVER" != cri ]; then
    args+=(-nri-test.driver "$DRIVER")
fi

# The kubeconfig goes with every run that talks to the cluster, which is the
# same set as the runs that need kubelet left running: the k8s conformance
# driver, and a ReplicaSet scalability burst. It used to be tied to DRIVER
# alone, so a ReplicaSet burst was handed no kubeconfig and reached the
# cluster only where client-go's own rules happened to find one -- a node with
# /root/.kube/config worked, and a node without it failed with an error about
# the cluster rather than about the missing flag.
#
# Read from kubelet_reason rather than asking again, so the kubeconfig and the
# kubelet decision cannot come to disagree about whether this run uses a
# cluster.
if [ -n "$kubelet_reason" ] || [ "$DRIVER" != cri ]; then
    args+=(-nri-test.kubeconfig "$K8S_KUBECONFIG")
fi

# Two details of this invocation are load-bearing, and neither is about ssh's
# options as such.
#
# A background job reaped with `wait`, rather than a foreground command, so that
# a signal can be acted on while the suite is running. A foreground child defers
# every trap until it returns, so a TERM during a two-minute sweep did nothing
# until the sweep ended -- and, worse, there was no pid to wait for, so the
# restore could not be ordered after the suite's death. `wait` also yields
# 128+signal for a job that died from one, which is a second, independent way
# for the verdict below to know the run did not finish.
#
# -tt, a pty. What it is NOT is how an interrupt reaches the suite: that was
# the claim here, and it is false. Measured, with the pty in place, a SIGTERM to
# this script ended the script and left the remote suite running to completion;
# the local ssh outlived the signal, so the pty never closed and no SIGHUP ever
# reached the node. stop_suite kills the far side by hand over a second
# connection instead, and that is what the ordering rests on. The pty is kept
# because it is what this has been run through -- the suite's output streams
# back line by line, and a node whose sudoers wants a tty still works -- not
# because anything depends on its semantics. Its cost is real: output comes
# back through a terminal, so lines end CRLF. Pipe a captured log through
# `tr -d '\r'` if it has to be byte-clean.
echo "--- running nri-test (handler: ${handler:-<runtime default>})"
rc=0
suite_launched=yes
on_node_tty sudo "$REMOTE_BIN" "${args[@]}" "${suite_args[@]}" &
suite_pid=$!
wait "$suite_pid" || rc=$?
suite_pid=""

# Name the step, because rc alone cannot. 255 is ssh's own and says nothing
# about the suite; a status above 128 is a signal, not a verdict; anything else
# came back from the node.
case $rc in
0)
    echo "--- nri-test passed"
    ;;
255)
    echo "--- ssh to $NODE failed (rc 255) while running nri-test; that is" \
        "ssh's own error and says nothing about the suite" >&2
    ;;
12[89] | 1[3-8][0-9] | 19[0-2])
    echo "--- the run was interrupted by signal $((rc - 128)) and did NOT" \
        "finish; whatever its output above says, this sweep is incomplete and" \
        "must not be quoted" >&2
    ;;
*)
    echo "--- nri-test exited $rc: a test failure, or sudo or the binary" \
        "failing to start. Its output above says which"
    ;;
esac

exit $rc
