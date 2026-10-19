/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package framework

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

// The expectations below turn "the plugin asked for X" into "X happened". Each
// one names the surface that is authoritative for its subject and reads only
// that surface, or both where both exist and agreement is itself the assertion.
//
// The governing rule is: prefer the kernel's answer where one exists, and use
// the runtime's recorded spec where it does not. The spec says what the runtime
// wrote down; only the kernel says what the container got.
//
// Two subjects are read on two surfaces, and the two cases are not equally
// strong. A mount is genuine mount-namespace state, so a container's
// /proc/self/mounts is an independent witness and disagreement with the spec is a
// real finding. An environment variable is not: both containerd and CRI-O build
// an exec'd process's environment from the container's stored OCI spec, so
// reading /proc/self/environ through an exec re-reads the document the inspector
// already returned. Both reads are still required -- see ExpectEnv for exactly
// what the second one buys and what it cannot -- but only the mount case earns
// the phrase "second surface".
//
// Every function here is pure: it takes the evidence and returns an error rather
// than taking a *testing.T. That keeps this layer unit-testable without a
// runtime, and it is why the Suite.Expect* wrappers -- which gather the evidence
// and turn an error into a failure -- are thin enough to read at a glance.

// maxAllowedListID bounds the cpu and NUMA node numbers an allowed list may
// name, so that a typo in a range bound is reported as one instead of expanding
// into a set large enough to matter.
//
// The bound has to be small enough for that claim to be true, which is a
// measured question rather than a matter of taste. The widest range this bound
// admits, "0-8192", parses in 1.3ms and allocates 0.6MiB. An earlier version of
// this file set the bound at 1<<20, which admitted "0-1048576": measured on this
// host at 210ms and 72MiB, so the protection this comment claims did not hold at
// that bound at all, and "0-999999" sailed through it.
//
// 8192 is Linux's CONFIG_NR_CPUS ceiling on x86_64 (arch/x86/Kconfig, the
// NR_CPUS_RANGE_END for MAXSMP), comfortably above MAX_NUMNODES' 1024
// (include/linux/numa.h, 1 << CONFIG_NODES_SHIFT). Check it against those rather
// than trusting this comment. Nothing legitimate is excluded: both numbering
// spaces are dense from 0, so a machine cannot have a cpu 8193 without having
// 8194 cpus, which no kernel that caps NR_CPUS at 8192 will boot.
const maxAllowedListID = 1 << 13

// ExpectAnnotation checks that a container's OCI spec carries an annotation.
//
// The spec is authoritative here, and it is the only surface: an annotation has
// no in-container expression at all, so there is nothing for the kernel to
// confirm.
//
// Note carefully what this does and does not verify. CRI pod and container
// annotations do NOT reach a container's OCI spec by default -- containerd
// gates that on its PodAnnotations/ContainerAnnotations allowlists, which are
// empty unless a node's configuration opts in -- while an NRI adjustment writes
// the annotation into the spec directly. So this verifies an NRI adjustment.
// Pointing it at an annotation set through CRI asserts something that is false
// on a stock node, and a test that does so is testing the node's configuration
// rather than NRI. The two must not be confused; an earlier test in this suite
// confused them and had to be replaced.
func ExpectAnnotation(spec *rspec.Spec, key, want string) error {
	if spec == nil {
		return fmt.Errorf("annotation %q: there is no OCI spec to read it from (wanted %q)",
			key, want)
	}

	got, ok := spec.Annotations[key]
	if !ok {
		return fmt.Errorf("annotation %q: not set in the container's OCI spec, wanted %q; "+
			"the spec carries %v", key, want, slices.Sorted(maps.Keys(spec.Annotations)))
	}

	if got != want {
		return fmt.Errorf("annotation %q: the container's OCI spec has %q, wanted %q",
			key, got, want)
	}

	return nil
}

// ExpectEnv checks that a container has an environment variable, requiring both
// reads to agree.
//
// Be precise about what the second read is worth, because an earlier version of
// this comment -- and of the design doc -- claimed more. The OCI spec is what the
// runtime recorded. procEnviron is the NUL-separated contents of
// /proc/self/environ as read by a command run in the container, and both
// containerd and CRI-O compose an exec'd process's environment from the
// container's *stored OCI spec*: it is therefore the same document the inspector
// returns, reached by a second path, and NOT an independent witness the way a
// mount table or an allowed list is.
//
// So what the second read buys is real but narrow: it proves the recorded value
// is actually used to build the environment of a process the runtime starts in
// this container, rather than merely stored, and it catches a runtime whose exec
// path and inspect path disagree, or which mangles or drops the entry on the way.
// What it cannot catch is the failure a genuine second surface exists for -- a
// runtime that recorded the adjustment and did not apply it to the container's
// own init process. That would pass here. The message still names whichever read
// dissented, since the two are reached differently even though they share a
// source.
//
// Observing the container's init process is deferred, not rejected: under CRI's
// default pod PID namespace the container's init is not PID 1 -- PID 1 is the
// sandbox's pause process -- so it needs the init pid, which the framework does
// not yet plumb through. See ProcSelfStatus for the measurement behind that.
//
// The comparison is over whole "key=value" entries, never a substring search: a
// substring would let KK=V satisfy an expectation of K=V, and the assertion
// would pass with the adjustment unapplied.
func ExpectEnv(spec *rspec.Spec, procEnviron, key, want string) error {
	entry := key + "=" + want

	if spec == nil || spec.Process == nil {
		return fmt.Errorf("environment variable %q: the container's OCI spec records no "+
			"process, so there is nothing to compare (wanted %q)", key, want)
	}

	if !slices.Contains(spec.Process.Env, entry) {
		return fmt.Errorf("environment variable %q: the container's OCI spec does not "+
			"record %q; it records %v", key, entry, spec.Process.Env)
	}

	inside := splitEnviron(procEnviron)
	if !slices.Contains(inside, entry) {
		return fmt.Errorf("environment variable %q: the container's process environment "+
			"does not have %q, though the runtime recorded it; the process has %v",
			key, entry, inside)
	}

	return nil
}

// splitEnviron splits the contents of a /proc/<pid>/environ into entries.
//
// The file is NUL-separated and NUL-terminated, so the split leaves a trailing
// empty element that is dropped. Only empty elements are dropped -- an entry
// with an empty value is still "KEY=", which is a legitimate result of an
// adjustment that cleared a variable, and must survive.
func splitEnviron(environ string) []string {
	entries := []string{}

	for _, entry := range strings.Split(environ, "\x00") {
		if entry == "" {
			continue
		}

		entries = append(entries, entry)
	}

	return entries
}

// ExpectMount checks that a container has a mount at destination, requiring both
// surfaces to agree.
//
// Both, for the reason ExpectEnv gives: the OCI spec records the mount the
// runtime was asked to make, and procMounts -- the container's
// /proc/self/mounts -- proves the kernel actually made it. A mount recorded in
// the spec but missing from the mount table is precisely the failure this exists
// to catch, so neither surface alone would do.
//
// The mount table is read by whitespace field 2, the mount point. Field 1 is the
// device or source, and reading it would accept a filesystem mounted FROM the
// path under test -- the opposite of what is being asserted. Paths containing
// whitespace appear octal-escaped in the mount table and are not decoded here,
// since nothing this suite mounts uses them.
//
// wantOpts, when given, names options the mount table must record for the
// destination: each must be present as a whole element of field 4, so that "ro"
// is not satisfied by "errors=remount-ro". Only the mount table is consulted for
// them, never spec.Mounts[].Options, and the reason is a vocabulary difference
// rather than a preference. The spec records what was requested, which includes
// "bind" and "rprivate". Neither ever appears in /proc/self/mounts: "bind" is not
// a kernel mount flag at all, and propagation such as "rprivate" lives in
// /proc/self/mountinfo's optional fields, a file this suite does not read. So
// wantOpts names kernel mount flags -- "ro", "nosuid", "noexec", "relatime" --
// for which the kernel is the authority on whether the request took effect.
// Asking for "bind" or "rprivate" here would fail against a runtime that did
// everything right.
func ExpectMount(spec *rspec.Spec, procMounts, destination string, wantOpts ...string) error {
	if spec == nil {
		return fmt.Errorf("mount at %q: there is no OCI spec to read it from", destination)
	}

	recorded := make([]string, 0, len(spec.Mounts))
	for _, m := range spec.Mounts {
		recorded = append(recorded, m.Destination)
	}

	if !slices.Contains(recorded, destination) {
		return fmt.Errorf("mount at %q: the container's OCI spec records no mount with "+
			"that destination; it records %v", destination, recorded)
	}

	points := mountPoints(procMounts)
	if !slices.Contains(points, destination) {
		return fmt.Errorf("mount at %q: the container's mount table has no such mount "+
			"point, though the runtime recorded one; the container has mounted %v",
			destination, points)
	}

	if len(wantOpts) == 0 {
		return nil
	}

	opts, err := mountOptions(procMounts, destination)
	if err != nil {
		return err
	}

	var missing []string

	for _, want := range wantOpts {
		if !slices.Contains(opts, want) {
			missing = append(missing, want)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("mount at %q: the container's mount table records options %q, "+
			"which lack %q; the kernel did not apply them", destination,
			strings.Join(opts, ","), missing)
	}

	return nil
}

// mountOptions returns the options the container's mount table records for a
// destination.
//
// A /proc/<pid>/mounts line names the mount point in whitespace field 2 and the
// comma-separated options in field 4, counting from one as mount(5) does. The
// options come back split rather than as the raw field because the caller looks
// for "ro", which is a substring of plenty of legitimate option and source
// spellings -- "errors=remount-ro", "/var/lib/ro-data" -- and a substring search
// would report a read-only mount that is nothing of the kind.
//
// A line for the destination with fewer than four fields is an error naming the
// options field, not a skip and not a panic: the destination was found, so a
// truncated line is evidence the table could not be read to the end.
func mountOptions(procMounts, destination string) ([]string, error) {
	for _, line := range strings.Split(procMounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != destination {
			continue
		}

		if len(fields) < 4 {
			return nil, fmt.Errorf("mount at %q: its line in the container's mount table "+
				"has %d field(s) and no options field, so the options cannot be read: %q",
				destination, len(fields), line)
		}

		return strings.Split(fields[3], ","), nil
	}

	return nil, fmt.Errorf("mount at %q: the container's mount table has no line for it "+
		"to read the options from", destination)
}

// mountPoints returns the mount points named by a /proc/<pid>/mounts.
//
// A line with fewer than two fields is skipped rather than indexed into: output
// read back through an exec can be truncated, and a short last line must not
// panic the assertion that was reading it.
func mountPoints(procMounts string) []string {
	points := []string{}

	for _, line := range strings.Split(procMounts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		points = append(points, fields[1])
	}

	return points
}

// ExpectCpuset checks the set of CPUs a container's processes may run on, given
// a parsed /proc/self/status.
//
// The kernel alone is authoritative. spec.linux.resources.cpu.cpus records the
// request; Cpus_allowed_list is what the process actually got, and only the
// latter answers "did it take effect" -- a runtime that wrote the request into
// the spec and failed to apply it looks correct on the spec and wrong here,
// which is the whole point of reading it.
//
// /proc/self, never /proc/1: under CRI's default pod PID namespace, PID 1 is the
// sandbox's pause process, which is not subject to this container's cpuset.
// Measured on this host, /proc/1/status reported 0-43 -- every CPU -- while
// /proc/self/status reported 0-1, matching the requested CpusetCpus of "0-1".
// Reading PID 1 would have made the assertion silently meaningless. Suite's
// ProcSelfStatus is the correct way to obtain the argument.
func ExpectCpuset(status map[string]string, want string) error {
	return expectAllowedList(status, "Cpus_allowed_list", "cpuset", want)
}

// ExpectMems checks the set of NUMA memory nodes a container's processes may
// allocate from, given a parsed /proc/self/status.
//
// The kernel alone is authoritative, and /proc/self rather than /proc/1, for
// exactly the reasons ExpectCpuset documents; this is its sibling over
// Mems_allowed_list. NUMA placement is one of the main things an NRI plugin
// adjusts, so the two belong together.
func ExpectMems(status map[string]string, want string) error {
	return expectAllowedList(status, "Mems_allowed_list", "NUMA nodes", want)
}

// expectAllowedList compares one of /proc/self/status's allowed lists against
// what was asked for.
//
// ExpectCpuset and ExpectMems are wrappers over this and share its body
// deliberately: two copies could drift apart in behaviour, and one of them would
// then be wrong in a way no test of the other could see.
//
// Sets are compared, not strings. The kernel normalizes an allowed list into
// ascending ranges, so a runtime asked for CpusetCpus "1,0" reports 0-1: a
// literal comparison would fail and the suite would blame a runtime that did
// exactly what it was asked. "0,1" is the spelling a test author is most likely
// to write, so this is the common case rather than a corner.
//
// Parsing fails closed on both sides. A parser that returned an empty set for
// text it could not read would let an empty expectation match garbage, and the
// resulting test would pass because both sides failed to parse rather than
// because anything was verified.
//
// The message reports the kernel's raw text as well as the parsed set: when this
// fails, the raw text is what a reader compares against the runtime's own logs,
// and the parsed form is what the comparison actually used.
func expectAllowedList(status map[string]string, key, subject, want string) error {
	raw, ok := status[key]
	if !ok {
		return fmt.Errorf("%s: the container's /proc/self/status reports no %s, so what "+
			"the kernel allowed cannot be read (wanted %q)", subject, key, want)
	}

	got, err := parseAllowedList(raw)
	if err != nil {
		return fmt.Errorf("%s: cannot read %s %q from the container's /proc/self/status: %w",
			subject, key, raw, err)
	}

	wanted, err := parseAllowedList(want)
	if err != nil {
		return fmt.Errorf("%s: cannot read the wanted %s %q: %w", subject, key, want, err)
	}

	if !maps.Equal(got, wanted) {
		return fmt.Errorf("%s: the container's kernel reports %s %q, which is %s; "+
			"wanted %q, which is %s",
			subject, key, raw, allowedListString(got), want, allowedListString(wanted))
	}

	return nil
}

// parseAllowedList parses a cpu or NUMA node list into the set it names.
//
// Both syntaxes are accepted on both sides -- comma-separated members, lo-hi
// ranges, and any mixture of the two, such as "0-2,5" -- because the kernel
// prints ranges and a test author writes members.
//
// Every rejection is an error, never an empty set: see expectAllowedList for why
// silently returning nothing here would produce tests that pass without
// verifying anything.
func parseAllowedList(list string) (map[int]struct{}, error) {
	if strings.TrimSpace(list) == "" {
		return nil, errors.New("the list is empty, which names no cpu or node")
	}

	set := map[int]struct{}{}

	for _, member := range strings.Split(list, ",") {
		member = strings.TrimSpace(member)

		lo, hi, isRange := strings.Cut(member, "-")
		if !isRange {
			id, err := parseAllowedListID(member)
			if err != nil {
				return nil, err
			}

			set[id] = struct{}{}

			continue
		}

		first, err := parseAllowedListID(lo)
		if err != nil {
			return nil, fmt.Errorf("range %q: %w", member, err)
		}

		last, err := parseAllowedListID(hi)
		if err != nil {
			return nil, fmt.Errorf("range %q: %w", member, err)
		}

		if last < first {
			return nil, fmt.Errorf("range %q descends; the kernel prints ascending ranges",
				member)
		}

		for id := first; id <= last; id++ {
			set[id] = struct{}{}
		}
	}

	return set, nil
}

// parseAllowedListID parses one member of an allowed list.
//
// Only decimal digits, so that the spellings which look numeric but are not --
// "+1", "-1", "0x1", "1.0" -- are rejected rather than quietly reinterpreted.
func parseAllowedListID(s string) (int, error) {
	if s == "" {
		return 0, errors.New("a list member is empty")
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not a decimal number", s)
		}
	}

	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number: %w", s, err)
	}

	if id > maxAllowedListID {
		return 0, fmt.Errorf("%q is beyond any plausible cpu or node number (%d)",
			s, maxAllowedListID)
	}

	return id, nil
}

// allowedListString renders a set the way the kernel would: ascending members,
// with consecutive runs coalesced into ranges.
//
// It exists for the error messages, so that a reader sees the comparison the
// assertion actually made alongside the raw text it started from.
func allowedListString(set map[int]struct{}) string {
	ids := slices.Sorted(maps.Keys(set))

	var out strings.Builder

	for i := 0; i < len(ids); {
		last := i
		for last+1 < len(ids) && ids[last+1] == ids[last]+1 {
			last++
		}

		if out.Len() > 0 {
			out.WriteString(",")
		}

		out.WriteString(strconv.Itoa(ids[i]))

		if last != i {
			out.WriteString("-")
			out.WriteString(strconv.Itoa(ids[last]))
		}

		i = last + 1
	}

	return out.String()
}
