# nri-test

A conformance test suite for the NRI implementation of an installed container
runtime, in the spirit of `critest`. It acts as one or more NRI plugins and as
a client of the runtime, drives pod and container lifecycle, and verifies that
what plugins asked for over NRI actually happened to the resulting containers.

The `pkg/adaptation` unit tests cover NRI's own logic thoroughly, but only
in-process: they assert on the `CreateContainerResponse` the `Adaptation`
object returns. They cannot tell whether a runtime embedding NRI honours that
response. This closes that gap.

Two operating modes, selected by `-nri-test.driver`. **CRI mode** creates pods
and containers directly over CRI. **Kubernetes mode** creates them through a
cluster with `client-go`, so kubelet makes them the way it does a user's. Both
reuse the entire assertion layer unchanged, which is what the architecture
below is shaped for: a Kubernetes driver pairs with the *unchanged* CRI
inspector, because the suite runs on the node it observes.

## Contents

- [Why a new tool](#why-a-new-tool)
- [Requirements](#requirements)
- [Non-goals](#non-goals)
- [Building and testing](#building-and-testing)
- [Running against a node](#running-against-a-node)
- [Layout](#layout)
- [Architecture](#architecture)
- [Verification](#verification)
- [Reporting](#reporting)
- [The low-level runtime](#the-low-level-runtime)
- [Isolation](#isolation)
- [Gating](#gating)
- [Error handling](#error-handling)
- [Configuration](#configuration)
- [Recorded results](#recorded-results)
- [Invariants a change must not break](#invariants-a-change-must-not-break)
- [Measured facts](#measured-facts)
- [Input conformance](#input-conformance)
- [Coverage](#coverage)
- [Known gaps and remaining work](#known-gaps-and-remaining-work)
- [Open questions](#open-questions)

## Why a new tool

`cri-tools` already ships an NRI suite, `pkg/validate/nri_linux.go` and
`pkg/validate/nri_util_linux.go`, gated on a `-nri-socket` flag. It uses
`pkg/stub` in the same way this does, and covers lifecycle event ordering and
hook contracts: pod and container event ordering with metadata, sandbox and
container visibility while a hook is in progress, hook-error cleanup and retry,
and plugin synchronization including the create-during-`Synchronize` race.

It covers none of the adjustment or update surface: no mounts, environment,
arguments, hooks, devices, CDI, resources, seccomp, RDT, conflict detection or
validating plugins. That is the majority of what NRI does and the majority of
what `pkg/adaptation` tests.

So this is a standalone tool in this repository with full coverage, including
the lifecycle ground `critest` already covers. NRI should own the definition of
NRI conformance; tests belong next to the API they test so they evolve with it;
the suite can gate NRI pull requests; and Kubernetes mode has no home upstream
in `cri-tools`. The cost is duplication with `cri-tools`, which may later be
resolved by trimming the `critest` NRI specs.

## Non-goals

- Testing CRI itself. That is `critest`'s job.
- Deploying or configuring a runtime. The suite runs against an installed one.
- Benchmarking.
- Replacing the `pkg/adaptation` unit tests. Several test in-process API
  contracts with no external expression; those stay unit tests.

The low-level runtime is not the *subject*, but it is part of the chain and is
selectable, so a failure can be located rather than merely observed. See
[The low-level runtime](#the-low-level-runtime).

## Requirements

- Runs against an **already-installed** runtime; it does not deploy or
  configure one. The only exception is the configuration-dependent opt-in
  suites, which a wrapper script configures and restores around the run.
- The default suite assumes only that NRI is enabled and that external plugin
  connections are allowed. Anything needing more specific runtime
  configuration is opt-in.
- **cgroup v2 only.** `Setup` refuses to run on a host without the unified
  hierarchy (no `cgroup.controllers` at the cgroup root). cgroup v1 is obsolete
  and was never measured.
- **Never disturbs workloads it did not create.** The suite may legitimately
  be pointed at a node running other things.
- **Every skip states its reason**, and the reason reaches the end-of-run
  summary.
- **No ginkgo, no gomega.** Standard library `testing` plus
  `github.com/stretchr/testify/require`, matching `pkg/adaptation`.

## Building and testing

```
make nri-test          # from the repository root, writes build/bin/nri-test
```

One binary, packaged like `critest`: `-test.run` selects tests, `-test.v`
controls verbosity, and the suite's own flags are registered from `init()`.

A separate Go module is required, not merely tidy: `k8s.io/cri-api` today and
`k8s.io/client-go` later must not enter the root `go.mod`, which containerd and
CRI-O vendor. The repository already uses per-directory modules for `plugins/*`
and `examples/`, so this follows an established pattern.

Unit tests cover the framework's pure parts and the CRI driver's conversions:

```
cd test/nri-test
go test -race ./framework/... ./driver/...
go vet ./framework/... ./driver/...
```

The `suite` package is not unit-testable: its `TestMain` calls
`framework.Setup`, which needs root and a live runtime, so `go test ./...`
fails there on any ordinary workstation. That failure is expected and is not a
regression. Listing is the exception and works anywhere:

```
./build/bin/nri-test -test.list '.*'
./build/bin/nri-test -nri-test.list-images
```

`-nri-test.list-images` prints the images the suite uses, one per line, and
likewise needs no runtime. A test that creates containers from an image other
than `-nri-test.image` must declare it with `framework.UseImage` in an `init`
function, so that it is listed and pre-pulled. `-nri-test.pull-images` pulls
all of them into the node's image store through the selected driver and exits
without running a test, for warming a node before an air-gapped or timed run.

## Running against a node

```
bash hack/run-nri-test.sh runc          # one handler per invocation
bash hack/run-nri-test.sh crun
bash hack/run-nri-test.sh '' -test.run=TestSmoke
```

The script builds with `CGO_ENABLED=0`, copies the binary to the node, stops
kubelet, runs the suite, and restores kubelet on every exit path. Node, ssh
config, endpoints and timeout come from the environment; read its header before
using it.

Three things about it are load-bearing:

- **One run at a time per node.** The interrupt path identifies the remote
  suite by its binary path, because nothing on the remote command line
  distinguishes one invocation from another. A second run, or a copy started by
  hand, is killed as root.
- **Kubelet must be down for a CRI-mode run.** Kubelet garbage-collects
  sandboxes it does not recognise, and the suite creates exactly those. The
  suite detects a running kubelet and refuses rather than stopping it itself;
  `-nri-test.allow-kubelet` overrides, and accepts that sandboxes may vanish
  mid-run.
- **An unconfirmed stop withholds the restore** and exits 2. A node left with
  kubelet down is loud and one command to fix. A kubelet collecting a live
  sweep's sandboxes is neither.

To sweep both low-level runtimes, invoke once per handler. The flag is
single-valued on purpose: one that accepted a list and ran one entry of it
would misreport what produced the result.

## Layout

```
test/nri-test/
|-- go.mod                    module github.com/containerd/nri/test/nri-test
|                             replace github.com/containerd/nri => ../..
|-- framework/                mode-agnostic library
|   |-- config.go             flags -> Config
|   |-- suite.go              Suite: run-level Setup/Teardown, per-case lifecycle
|   |-- plugin.go             TestPlugin -- ported mockPlugin, scope-filtered
|   |-- eventq.go             Event/EventQ -- ported from pkg/adaptation/suite_test.go
|   |-- driver.go             Driver interface + Capability bits
|   |-- inspector.go          Inspector + Executor interfaces, Observed
|   |-- spec.go               PodSpec / ContainerSpec (mode-neutral)
|   |-- scope.go              Scope: the run's isolation guard
|   |-- stale.go              stale-workload notice
|   |-- expect.go             adjustment -> spec and kernel assertions
|   |-- proc.go               /proc/<pid>/status parser
|   |-- gate.go               capability / version / environment / CPU / NUMA / handler gates
|   |-- report.go             Finding, Skip, Report, the end-of-run summary
|   |-- kubelet.go            kubelet detection and the CRI-mode refusal
|   |-- listing.go            -test.list detection, so listing needs no runtime
|   `-- images.go             the images the suite uses: UseImage, Images, PullImages
|-- driver/cri/               Driver + Inspector over CRI
|   |-- client.go             cri-api dial for runtime and image services
|   |-- driver.go             Driver implementation
|   |-- convert.go            PodSpec/ContainerSpec -> CRI configs
|   `-- inspector.go          ContainerStatus(verbose) -> runtimeSpec decode
|-- driver/k8s/               Driver over the API server, observing over CRI
|   |-- client.go             kubeconfig -> clientset and rest config
|   |-- driver.go             pods through the API server, wrapping driver/cri
|   `-- exec.go               exec through pods/exec, and the kubelet policy check
`-- suite/                    package suite -- all *_test.go, one test binary
    |-- main_test.go          TestMain, flag registration, exit code
    |-- smoke_test.go         one end-to-end lifecycle + OCI-spec check
    |-- connection_test.go    registration, configuration, version exchange
    |-- sync_test.go          synchronization + Container-proto fidelity
    |-- lifecycle_test.go     runLifecycle helper + subscription-mask table
    |-- ordering_test.go      multi-plugin invocation order by index
    `-- adjust_test.go        five creation adjustments, spec and kernel
```

## Architecture

### Driver and Inspector

The single architectural seam is the split between mutating workloads and
observing them. `Driver` creates and mutates; `Inspector` observes.

`Inspector.Inspect` is keyed by a **bare container id**, not a driver-specific
handle. That is deliberate: the id is available in every mode, from the NRI
events our own plugins receive and, in Kubernetes mode, from
`pod.Status.ContainerStatuses[].ContainerID`. So `k8s.Driver` pairs
with the *unchanged* `cri.Inspector`, and the assertion layer -- where nearly
all the code and all the maintenance lives -- never learns which mode it is in.

The alternative, one `Workload` interface per mode that both mutates and
observes, was rejected: a Kubernetes implementation would have to re-derive
observation, and the path of least resistance there is `client-go` (pod status,
exec), which cannot see an OCI spec at all. That would quietly weaken every
adjustment test in the new mode. Two separate suites sharing only the plugin
library was also rejected, as it duplicates every adjustment assertion.

`ListRunPods` and `ListRunContainers` are required methods rather than optional
type assertions, because a driver that did not implement one would silently
disable the mechanism whose whole job is noticing a missed cleanup.

**Optional driver capabilities are discovered by type assertion on an
unexported interface**, never by widening `Driver`. The instances are
`versioner` (runtime name and version), `imagePuller`, `executorOf` (exec),
`kubeletConflicter` (is this mode kubelet-sensitive) and
`runtimeHandlerLister`. This keeps `Driver` from growing a method per
observation surface, and keeps each genuinely optional. The cost is that a
rename on the driver side stops matching silently, so the driver half of each
is pinned by a test.

### Capability matrix

| Capability | CRI mode | Kubernetes mode |
|---|---|---|
| `CapContainerLifecycle` | yes | no -- pods are created as a unit |
| `CapObserveOpError` | yes | no (pod status and events could be read) |
| `CapUpdateContainerResources` | yes | no (in-place resize could be added) |
| `CapUpdatePodResources` | never advertised | never advertised |
| `CapExec` | yes | yes, through the API server |
| `CapRawResources` | yes | no -- a pod cannot state a cpuset; the exec smoke test expects the node's full online sets instead |
| `CapContainerMetadata` | yes | no -- a container has its pod's metadata |

A test declares what it needs through `RequireCaps`; the framework skips it,
with a reason, when the active driver lacks them. The CRI driver has every
capability, so the gates do nothing there; the Kubernetes driver advertises
`CapExec` alone, which is what exercises them.

`CapContainerLifecycle` means containers can be made in an existing pod and
started, stopped and removed apart from it. No test needs that: they create
a pod with its container through `Suite.CreatePodWithContainer`, which every
driver can do, and `StopContainer` and `RemoveContainer` reach a plugin when
the pod is removed (`TestSmoke`, pod teardown). The capability stays for a
future test whose subject is the gap between those steps.

### Kubernetes mode

`-nri-test.driver=k8s` creates the workloads as pods through the API
server, so kubelet makes them the way it does a user's. Kubelet must be
running, which is the reverse of CRI mode; `hack/run-nri-test.sh` leaves it
alone when `DRIVER=k8s`. The suite still runs on the node and observes
through that node's runtime, so the cluster's kubelet must drive the runtime
under test.

Pods are placed with `spec.nodeName` (`-nri-test.k8s-node`, default this
host's name), `restartPolicy: Never` and no grace period. A handler becomes
a `RuntimeClass` the run creates and deletes. A spec with no container gets
a `sleep` keep-alive, since a pod cannot be empty. The driver writes the API
server's pod UID and the DNS-safe pod name back into the spec. The test
namespace is created on first use and deleted by `Cleanup` only if the
driver created it. Tests whose subject is an update event or a container's
own annotations skip, with the capability named.

Exec goes through the API server (pods/exec), as kubectl exec does;
inspection of the realized OCI spec and the leak sweeps stay on the node's
CRI runtime. At start the driver reads the kubelet's configz and refuses
to run unless cpuManagerPolicy and memoryManagerPolicy are none (the
default). Containers then start with the node's full cpuset and memory
nodes, which is what the exec smoke test expects; its memory-node check
runs only on a node with two or more NUMA nodes.

### Mode-neutral workload description

`PodSpec` carries name, namespace, uid, labels, annotations, cgroup parent,
runtime handler and an optional container list. `ContainerSpec` carries name,
image, command, args, env, mounts, resources, labels and annotations. The CRI
driver maps these onto `runtimeapi.PodSandboxConfig` and
`runtimeapi.ContainerConfig`; the Kubernetes driver maps them onto
`corev1.Pod`. `PodSpec.Containers` is the path tests use, and `Pod.Containers`
lists what the driver created, so the leak guard attributes it. A driver whose
containers start on their own makes `StartContainer` wait for running.

`framework.Resources` carries only the fields the suite currently sets. The
update phases will need the rest.

## Verification

Several strategies, not one. Each answers a different question, and where two
disagree the more authoritative one for that subject wins.

**Prefer the kernel's own answer where one exists; fall back to the runtime's
record where it does not.** Reading the spec back through
`ContainerStatus(verbose)` says what the runtime *recorded*; reading
`/proc/self/status` inside the container says what the kernel actually
*applied*. For "did this adjustment take effect", the second is stronger. For
things with no in-container expression -- an annotation, a hook, `cgroupsPath`,
an RDT class name -- the spec is the only witness there is.

Each subject names which surface is authoritative for it, in a comment at the
assertion. Getting that wrong is not hypothetical: an early smoke test asserted
a pod annotation would appear in the container's OCI spec, which it never does,
while the same annotation *does* reach the NRI `Container` proto. Same data,
two surfaces, different answers.

### 1. The realized OCI spec

`ContainerStatus(verbose=true)` returns `Info["info"]` as a JSON document with
a `runtimeSpec` field holding the container's full OCI spec. Both runtimes
provide it: containerd in `internal/cri/server/container_status.go`, CRI-O in
`server/container_status.go`, each a `RuntimeSpec` field tagged `runtimeSpec`.
It is a uniform, runtime-agnostic channel, and it is exactly what the
`pkg/adaptation` tests assert against `CreateContainerResponse`, except
observable from outside the runtime.

`framework/expect.go` holds the per-subject mapping from NRI adjustment to OCI
spec location, written by hand as explicit rows so that it is an oracle
independent of NRI's own code:

| Adjustment | OCI spec location |
|---|---|
| `Annotations` | `spec.annotations` |
| `Mounts` (add) | `spec.mounts` contains the mount |
| `Mounts` (`MarkForRemoval`) | `spec.mounts` does not contain the destination |
| `Env` | `spec.process.env` |
| `Args` | `spec.process.args` |
| `Hooks` | `spec.hooks` |
| `Rlimits` | `spec.process.rlimits` |
| `Linux.Devices` | `spec.linux.devices` and `spec.linux.resources.devices` |
| `Linux.Namespaces` | `spec.linux.namespaces` |
| `Linux.Sysctl` | `spec.linux.sysctl` |
| `Linux.Seccomp` | `spec.linux.seccomp` |
| `Linux.CgroupsPath` | `spec.linux.cgroupsPath` |
| `Linux.OomScoreAdj` | `spec.process.oomScoreAdj` |
| `Linux.IOPriority` | `spec.process.ioPriority` |
| `Linux.Scheduler` | `spec.process.scheduler` |
| `Linux.MemoryPolicy` | `spec.linux.memoryPolicy` |
| `Linux.NetDevices` | `spec.linux.netDevices` |
| `Linux.RDT` | `spec.linux.intelRdt` |
| `Linux.Resources.Cpu` | `spec.linux.resources.cpu` |
| `Linux.Resources.Memory` | `spec.linux.resources.memory` |
| `Linux.Resources.HugepageLimits` | `spec.linux.resources.hugepageLimits` |
| `Linux.Resources.Unified` | `spec.linux.resources.unified` |
| `Linux.Resources.BlockioClass` | `spec.linux.resources.blockIO`, resolved by the runtime |
| `Linux.Resources.RdtClass` | `spec.linux.intelRdt.closID`, resolved by the runtime |
| `CDIDevices` | resolved: the injected devices, environment and mounts the CDI spec declares |

Spec-only assertions need a *created* container, not a started one, since
`ContainerStatus(verbose)` works on a created container, so those tests skip
`StartContainer` and avoid a class of flakes. Exec-verified and update
assertions need a running one. One busybox image with a keep-alive command
serves both, so the suite needs only one image.

### 2. In-container observation, via `ExecSync`

`ExecSync` runs a command inside a running container, and because exec joins
the container's cgroups and namespaces, what that process sees is what the
container got. This is the authoritative surface for anything the kernel
exposes about itself:

| Subject | Read |
|---|---|
| CPU affinity | `Cpus_allowed_list` in `/proc/self/status` |
| NUMA node affinity | `Mems_allowed_list` in `/proc/self/status` |
| seccomp mode | `Seccomp`, `Seccomp_filters` in `/proc/self/status` |
| CPU quota, period, weight | `/sys/fs/cgroup/cpu.max`, `cpu.weight` |
| cpuset | `/sys/fs/cgroup/cpuset.cpus.effective` |
| memory limits | `/sys/fs/cgroup/memory.max`, `memory.high` |
| OOM score adjustment | `/proc/1/oom_score_adj`, only with a PID namespace per container |
| environment an exec'd process is given | `/proc/self/environ`, with the caveat below |
| mounts actually present | `/proc/self/mounts` |
| sysctls actually applied | the relevant `/proc/sys/...` path |

**Environment is the one row that is not an independent second surface.** Both
runtimes build an exec'd process's environment from the container's *stored OCI
spec*, so reading `/proc/self/environ` re-reads the document the inspector
already returned. It proves the recorded value is used to start processes in
the container, and it catches a runtime whose exec and inspect paths disagree,
but it cannot catch one that recorded the adjustment without applying it to the
container's own init process -- which is the case a second surface exists for.
For every other row the kernel is a genuinely separate witness.

**Mount options are matched against the mount table only**, never against
`spec.Mounts[].Options`. The two are different vocabularies: the spec records
what NRI asked for, including `bind` and propagation such as `rprivate`, and
neither ever appears in `/proc/self/mounts` -- propagation lives in
`/proc/self/mountinfo`'s optional fields, which this suite does not read. So
`ExpectMount`'s `wantOpts` names kernel mount flags (`ro`, `nosuid`, `noexec`,
`relatime`), and asking for `bind` or `rprivate` would fail a correct runtime.
Options are compared as whole comma-separated elements, never by substring,
because `ro` is a substring of spellings such as `errors=remount-ro`.

Two consequences for test shape:

- **The test image must have a shell.** `pause` cannot be exec'd into at all,
  so the default is busybox, `registry.k8s.io/e2e-test-images/busybox:1.36.1-1`,
  2.1 MiB. Its default command exits immediately without a TTY, so containers
  get a keep-alive command.
- **Exec-verified tests must start their container**, where spec-only tests
  need only create it. That costs some flake surface and is the price of the
  kernel's answer.

### 3. Deliberately absent: a host cgroupfs reader

An earlier design had the suite read the host's cgroup hierarchy under
`spec.linux.cgroupsPath`. Dropped: reading `/sys/fs/cgroup` from *inside* the
container answers the same question better -- it is the container's own view, it
needs no root, and it cannot disagree with what the workload experiences. So
there is no `framework/cgroup.go` and no `CapHostCgroups`.

What this gives up: a `cgroupsPath` adjustment can be verified only against the
runtime's recorded spec, not by confirming the kernel placed the container
there. An accepted limit, recorded so it is a choice rather than an oversight.

### 4. Differential check against `runtime-tools/generate`, not built

`pkg/runtime-tools/generate.Generator.Adjust` *is* the adjustment-to-spec
mapping the runtimes use. So: create a control container from an identical
`ContainerSpec` with no plugin connected, observe its spec, apply the
adjustment locally with `Generator.Adjust`, and diff against the adjusted
container's observed spec. Near-total field coverage for very little code.

It is not an independent oracle -- it compares the runtime's vendored copy of
this code against the in-tree copy -- so it belongs behind a flag, off by
default, with strategy 1 remaining authoritative. Its value is detecting
version skew and adjustments the runtime drops on the floor. It needs a
normalizer plus an allowlist for legitimate divergences (mount ordering,
runtime-added defaults, per-container identifiers), and one new exported
constructor, `generate.NewSpecGenerator`, in the main module.

The flag is **not** registered until the check exists. It was briefly
registered ahead of the feature, which meant setting it did nothing and said
nothing about it.

## Reporting

Every `Suite.Expect*` wrapper used to end in `require.NoError`, which calls
`FailNow`, so one failure per test was all a run reported and enumerating what
a runtime does differently took as many runs as there were failures.

**Expectations collect. Driver calls stay fatal.** `CreatePod`,
`CreateContainer`, `StartContainer`, `Exec` and `Inspect` still end their test,
because a collected creation failure leaves the body running with a nil
`*Container` and fills the summary with derived noise that buries the one real
finding. A failed operation ends its test; a failed comparison does not.

**A failure is recorded in both modes.** The mode decides whether the test
aborts, not whether the failure is reported. A fatal driver call is recorded
too, under its own kind: leaving it unrecorded is how a 34-failure run once
printed `No expectation failures were collected`, which is worse than printing
nothing at all, because a reader believes it.

**The default is failing.** `-nri-test.collect-failures` selects collecting
mode. A collecting default would make a CI report green on real failures unless
CI remembered to pass a flag. In collecting mode the run still exits non-zero
if anything was collected: collecting changes *when* the suite stops, not
whether it reports.

`Teardown` prints a summary: a header naming the runtime, its version, the NRI
version, the runtime handler and the run id; then expectation failures with the
test, the feature and the surface read; then failed operations; then every skip
with its reason; then a count of both failure kinds.

**Three things the summary does not see**, which matters when you read one:

1. The gates' own inability-to-evaluate aborts. Deliberate: a suite bug,
   not a runtime result.
2. Every bare `require.*` and `t.Errorf` in the `suite` package. Only
   `adjust_test.go` calls `Suite.Expect*` at all, so most tests still fail at
   their first assertion in both modes. Unconverted, not deliberate.
3. The per-case cleanup and leak guards, including the pod and container leak
   reports. Unconverted.

Because of 2 and 3 a run can fail entirely through paths the summary cannot
see. It says so when that happens: a failing run with nothing collected prints
that it failed through assertions the summary does not see, and that the test
output is the only account of what. Converting the leak and cleanup guards to
record is the real remedy and is still owed.

**A skip is not a failure.** Skips are recorded so the summary can name what
went unverified, but `HasFindings` counts findings only, so a run whose gates
all skipped exits zero. `testing` offers no way to enumerate skips after the
fact and `m.Run` returns only an exit code, so every skip site records as it
skips, through `framework.Skipped`. A direct `t.Skip` in a new test is
invisible to the summary.

## The low-level runtime

The chain under test is

```
plugin -> NRI -> runtime -> OCI spec -> low-level runtime (runc/crun) -> container/kernel
```

Every link can drop an adjustment. A feature that fails under crun and passes
under runc is a crun defect rather than an NRI one, and that is a distinction
the suite could not previously produce at all: it took whatever handler the
runtime defaulted to and never said which.

`PodSpec.RuntimeHandler` is mode-neutral: CRI mode maps it onto
`RunPodSandboxRequest.RuntimeHandler`, and the Kubernetes driver maps it onto
a `RuntimeClass` it owns, named in `pod.spec.runtimeClassName`. `Suite.PodSpec` stamps the configured handler on
every spec it builds, so no test has to remember to.

Discovery reads `StatusResponse.RuntimeHandlers` through the optional
`runtimeHandlerLister`. **An entry with an empty name is the runtime's default
and is never offered as selectable**; on the CRI-O 1.37.2 test node the three
reported entries are `crun`, `runc`, and one carrying no name at all.

A handler the node has not installed fails `Setup` once, naming what was asked
for and what is installed, rather than failing every test or silently falling
back to the default. A runtime advertising no handlers therefore refuses the
flag: CRI refuses an unknown handler rather than ignoring it, so the
alternative is one loud `CreatePod` error per test naming none of the context.
A discovery failure is fatal only when a handler was requested.

`SkipUnlessRuntimeHandler` skips a test whose handler is not installed. Its
contract is two-step: passing the gate does not make the test's workloads run
under that handler -- the caller must also set `PodSpec.RuntimeHandler`. It has
no caller yet; the feature matrix is where it acquires one, and where the
question of whether installed-ness or "this run selected it" is the gate worth
having gets settled against a real need.

## Isolation

The plugins are node-global: a plugin connected to a runtime sees every pod on
the host, including workloads unrelated to the test. Getting this wrong means
corrupting or failing a production workload, so it is a first-class requirement.

- **Every test workload lives in a dedicated pod namespace**, `nri-test` by
  default. This is the outer guard and the one that carries the safety
  property, because `PodSandbox.Namespace` is pod identity metadata: unlike
  labels and annotations it does not depend on the runtime round-tripping
  anything.
- Every test pod also carries `nri-test.containerd.io/run-id=<uuid>` and a
  per-case identifier, in both labels and annotations. Within the namespace the
  run id separates concurrent runs on one node.
- `TestPlugin` applies a **scope filter before every callback body**. Out of
  scope means: return a nil adjustment, no updates, record no event, and never
  return an error. In scope requires the pod's namespace to be one of ours;
  within it, a *different* run id is out of scope and *no* run id is in scope,
  because a dedicated namespace makes it ours by construction and the
  alternative is going blind when a runtime drops labels. An empty namespace
  set fails closed. Containers need no namespace of their own, since every
  container callback also receives its pod.

**The filter guards inputs and outputs, at two layers.** On the way in, every
callback is filtered, `Synchronize` included: the handshake receives the
runtime's whole world, so `TestPlugin` filters the lists itself and passes only
in-scope ones to the test's callback. That makes the mistake impossible rather
than merely caught. `SyncedPods` and `SyncedCtrs` still report the *unfiltered*
ids, because what the runtime actually told us about is itself a conformance
question.

**Filtering the handshake creates a circularity, so fidelity tests read around
it.** The pod fields a fidelity test measures -- namespace, labels, annotations
-- are the fields the filter keys on. Ask a filtered view whether the runtime
reported our namespace correctly and a runtime that reported it *wrongly* is
one whose pods the filter already excluded: the test fails with "we were never
told about this pod" instead of "this pod's namespace was wrong", and for a
mangled label it does not fail at all. So `RawSyncedPods` and `RawSyncedCtrs`
return the protos as delivered, cloned, before any filtering. They exist for
fidelity measurement and nothing else: a test that *acts* on a workload must go
through the filtered view, because the raw view carries no safety property.

On the way out, the returned update list is filtered too, because input
filtering does not subsume it: `ContainerUpdate.ContainerId` is a free-form
string the runtime applies to *that* container, and `TestPlugin.UpdateContainers`
takes an arbitrary list with no callback involved. So `TestPlugin` records the
ids it has seen through in-scope callbacks and drops any update naming another,
in all five exits.

**Every drop records an event, and a recorded drop fails the case.** Recording
alone is not enough: nothing else reads that event type, so the entry would be
a record no assertion looks at and a case whose request was silently emptied
would still pass. The guard takes no opt-out; a per-case latch was written and
removed, because it muted every later drop in the case across all its plugins.

- The suite is **strictly serial**. No `t.Parallel()` anywhere: plugin
  registration is global state, and plugins from concurrent tests would see
  each other's workloads.
- Every case registers a `t.Cleanup` that removes its pods and stops its stubs,
  on the failure path too. A leak guard then asserts nothing of ours survives.
  CRI's `PodSandboxFilter` has no namespace field, so listing filters
  client-side on `pod.Metadata.Namespace`.

**The leak guard is pod-level and container-level.** Container removal cascades
from `RemovePodSandbox`, so a pod-level guard covers the ordinary case, but it
cannot express "this rejected creation leaked no container" -- the container's
pod is one the case created on purpose, so the pod guard stays silent either
way.

**Each stage gets its own timeout budget and reports its own error.** Not
stylistic: an earlier version shared one budget across
StopPod/RemovePod/ListRunPods/Cleanup and hid failures behind an `err == nil &&`
condition, which disabled the guard outright while still looking like one. The
removals that leaked the pods also exhausted the budget, so the listing ran
with an expired context and its error was discarded. For a leak guard, missing
a leak is the fatal direction; over-reporting is a noisy failure someone
investigates.

Deciding whether a *container* is this run's uses a **union**: its
`PodSandboxId` is a pod we already know is ours, **or** its own labels or
annotations carry this run's id. The first arm catches a rejected creation's
leftover, which may carry no labels at all; the second catches a container
whose pod the listing missed. This arm is *stricter* than `Scope.Matches`,
which treats an absent run id as in-scope -- safe only behind a namespace check,
and that would fail open here. Assuming every listed container sits in our
namespace was rejected: every unlabelled container on the node would become our
leak, so every run on a busy node would fail its own guard.

**The stale-workload sweep is the one destructive thing the suite does**, so
its predicate is narrower than the filter's -- three tiers, not two:

1. A pod carrying **no** run id is never removed, only reported, even though
   the input filter and the leak guard both treat it as in scope.
2. A pod carrying **another run's** id is never removed either, and is reported
   as a stale peer. A label cannot distinguish a crashed run's leftover from a
   live concurrent run's workload, so the sweep declines to guess.
3. A pod carrying **our** run id is removed. Only what this run created.

With no run id configured the sweep removes nothing: it fails closed.

**The rule that came out of getting this wrong twice: a destructive predicate
is derived from scratch, never borrowed from a filter, however alike the two
look.** An earlier version shared one predicate across all three and would
have removed any unlabelled pod in the configured namespace, before printing
its banner and before running a single test. The first fix was itself too
broad: it removed a pod carrying *any* run id, which would have destroyed a
live concurrent run's sandboxes. Both mistakes had one shape -- reasoning about
which pods a predicate *matches* without re-deriving what then *happens* to
them.

`-nri-test.keep-artifacts` skips cleanup for debugging.

## Gating

Six independent gates, each producing a skip with a stated reason.

1. **Capability** -- the driver cannot express the operation.
2. **NRI version** -- `stub.RuntimeNRIVersion()` gives the runtime's version;
   for runtimes too old to advertise it, `api.InferVersionFromRuntime` infers
   from name and version. Comparison uses `semver.Compare`. Newer adjustment
   fields -- memory policy, Linux net devices, seccomp policy -- are gated this
   way rather than failing against an older runtime.
3. **Environment** -- resctrl mounted for RDT, a writable CDI directory for CDI
   devices, `NET_ADMIN` and a dummy link for net devices.
4. **CPU count** -- `SkipUnlessCPUs`. Below two CPUs a cpuset assertion cannot
   fail; see the vacuity rule below.
5. **NUMA node count** -- `SkipUnlessNUMANodes`, the same argument for
   `Mems_allowed_list`.
6. **Runtime handler** -- `SkipUnlessRuntimeHandler`.

Gates 4 and 5 count from **inside** a container, not from the host, for the
same reason `ExpectCpuset` reads `/proc/self/status`: the host's view is not
the container's, and a gate consulting the host would admit a test the
container cannot satisfy. They read a *probe* container created before the
case's plugins start -- the container under test is already adjusted, so
measuring it would make the gate skip everywhere.

The gates deliberately live beside `RequireCaps` rather than inside the
expectations. An expectation that skipped on its own would break the
pure-`error` contract that makes it unit-testable, and worse, it would mask a
genuine single-node *failure* on a multi-node host, which is exactly the
conformance bug the expectation exists to catch. **A skip is the test's
decision, made before it asserts, never the assertion's.**

### Configuration-dependent suites, not built

Behaviours that depend on how the runtime configured NRI belong in opt-in
suites selected by `-nri-test.config=<name>`, each probing first and skipping
if the runtime is clearly not configured for it:

| Suite | Requires |
|---|---|
| `default-validator` | default validator enabled |
| `no-hook-injection` | default validator, OCI hook injection disallowed |
| `no-namespace-adjustment` | default validator, namespace adjustment disallowed |
| `seccomp-policy` | default validator, specific seccomp adjustment policy |
| `required-plugins` | default validator with required plugins configured |
| `no-external-connections` | external plugin connections disabled |

Each needs a runtime configuration drop-in and a restart, so they cannot be
selected from inside one invocation. A wrapper script applies a configuration,
restarts the runtime, runs the matching suite, and restores the previous
configuration. The restore path matters more than the apply path: a node left
with `no-external-connections` set would make every later run fail its
preflight with a misleading cause.

## Error handling

- **Preflight.** `Setup` connects a probe plugin before running anything. On
  failure it fails fast with a message naming the likely cause -- NRI disabled,
  or external connections disallowed -- rather than producing a wall of
  confusing failures.
- **Stuck stub startup.** `stub.Start()` can wedge on its internal `cfgErrC`,
  which does not observe context cancellation; a real defect `cri-tools` hit
  and worked around. This suite keeps that workaround: install a custom dialer
  via `stub.WithDialer`, retain the `net.Conn`, and force-close it to break the
  ttrpc multiplex if startup does not complete within the timeout.
- **Timeouts everywhere.** Every wait for an NRI event or a container state
  transition is bounded and reports what it was waiting for on expiry. Every
  driver call a test makes goes through a wrapper that bounds it: an unbounded
  call on a wedged runtime hangs until `go test`'s own timeout, which panics
  the process, so no `t.Cleanup` runs, no leak guard fires, and the workloads
  genuinely leak.
- **Negative tests assert absence of damage.** When a conflict or a validating
  plugin is expected to fail `CreateContainer`, the test asserts both that the
  call returned an error and that no container was leaked.

## Configuration

Flags registered from `init()`, all prefixed `-nri-test.` except the endpoint
flags, which follow `critest` naming:

| Flag | Default | Meaning |
|---|---|---|
| `-runtime-endpoint` | first of the containerd and CRI-O default socket paths that answers, as `crictl` does | CRI runtime service endpoint |
| `-image-endpoint` | same as the runtime endpoint | CRI image service endpoint |
| `-nri-socket` | `api.DefaultSocketPath` (`/var/run/nri/nri.sock`) | NRI socket for external plugins |
| `-nri-test.namespace` | `nri-test` | pod namespace for test workloads; anything outside it is ignored |
| `-nri-test.image` | `registry.k8s.io/e2e-test-images/busybox:1.36.1-1` | image for created containers; must have a shell |
| `-nri-test.config` | empty | opt-in configuration-dependent suite |
| `-nri-test.proc-view` | self | `self` reads `/proc/self/status` of the exec'd process; `init` reads `/proc/1/status`, valid only with a per-container PID namespace |
| `-nri-test.stop-grace` | 2s | grace for stopping test containers and pods (CRI stop timeout, pod termination grace and deletion grace); 0 kills at once |
| `-nri-test.list-images` | false | print the images the suite uses and exit; needs no runtime |
| `-nri-test.pull-images` | false | pull the images the suite uses and exit, running no test |
| `-nri-test.no-pull` | false | skip the image pull in `Setup`, for an air-gapped node |
| `-nri-test.run-id` | generated | this run's id, stamped into every workload; override only to reproduce a run |
| `-nri-test.keep-artifacts` | false | skip cleanup for debugging |
| `-nri-test.timeout` | 30s | per-operation timeout |
| `-nri-test.collect-failures` | false | collect expectation failures into an end-of-run summary instead of failing at the first; the run still exits non-zero if anything was collected |
| `-nri-test.driver` | cri | what creates the tests' workloads: `cri` (needs kubelet stopped) or `k8s` (pods through the API server, needs kubelet running) |
| `-nri-test.cri-namespaces` | k8s | namespace options the `cri` driver creates pods with: `k8s` sends what kubelet sends (network and IPC per pod, a PID namespace per container), `cri` sends none, so cri-api's zero values share one PID namespace per pod. The `k8s` driver is not affected |
| `-nri-test.k8s-node` | this host | node the k8s driver places pods on |
| `-nri-test.kubeconfig` | client-go rules | kubeconfig for the k8s driver or a scalability run |
| `-nri-test.allow-kubelet` | false | run even though a kubelet process is present; its sandbox garbage collection may remove this run's pods mid-test |
| `-nri-test.runtime-handler` | empty | CRI runtime handler, and so the low-level runtime, to create pods with; empty means the runtime's own default. One handler per run |
| `-nri-test.scale-replicas` | 0 | replica count a scalability burst scales to; 0 skips the scalability test entirely, which is what an ordinary conformance run wants |
| `-nri-test.scale-iterations` | 1 | how many bursts to measure |
| `-nri-test.scale-settle` | 2m | how long to wait for one direction of a burst before reporting what arrived; a window that closes early reports a lower bound |
| `-nri-test.scale-terminal` | `PostStartContainer` | stage a burst is measured to: `StartContainer`, the runtime asking before it starts a container, or `PostStartContainer`, being told it has started. They answer different questions; anything else is refused |
| `-nri-test.scale-report` | none | write the raw per-pod timing table here as JSON, so a distribution can be plotted without re-running the burst; iterations after the first get the iteration number appended |
| `-nri-test.scale-driver` | cri | what creates a scalability burst: `cri` drives the runtime directly, so every operation has client-side timing too and kubelet must be stopped; `k8s` scales a ReplicaSet, which needs kubelet running. The two have opposite kubelet requirements and a typo is refused rather than defaulted |
| `-nri-test.scale-plugins` | 1 | plugins kept registered during a scalability burst, the recorder (index 00) included. Plugins 01 and up simulate real plugins: they subscribe and take time to answer, and NRI calls them in index order |
| `-nri-test.scale-events` | every hook but `ValidateContainerAdjustment` | events the simulated plugins subscribe to, as `[INDEX=]EVENT,EVENT...`; repeatable. Without an index it applies to every simulated plugin. The recorder's events are fixed |
| `-nri-test.scale-delay` | none | simulated processing time per event, as `DURATION` (every event) or `[INDEX/]EVENT=DURATION`, comma separated and repeatable; later entries win and `all` names every event. E.g. `50ms`, or `CreateContainer=200ms,StopContainer=200ms`. Index 0 delays the recorder, after it has recorded the event |
| `-nri-test.scale-trace` | empty | measure from the runtime's OpenTelemetry spans instead of a recorder plugin: the `ADDR` an OTLP receiver listens on, e.g. `127.0.0.1:4317`. Every plugin is then simulated, so `-nri-test.scale-plugins` must be at least 1 |
| `-nri-test.scale-trace-protocol` | http | `http` (OTLP/HTTP protobuf, `/v1/traces`) or `grpc` |
| `-nri-test.scale-trace-dump` | none | write every span received to this file, one JSON span per line |

### Measuring from the runtime's traces

With `-nri-test.scale-trace` the scalability test has no recorder plugin.
It starts an OTLP receiver, simulates all the plugins, and takes the
timing of every NRI hook from the spans the runtime exports. The runtime
must be configured to export to the receiver's address, which the test
does not do. The suite runs on the node, so the address is local.

containerd takes `OTEL_*` environment variables (a systemd drop-in is
enough) and `-nri-test.scale-trace-protocol=http`:

    OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
    OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
    OTEL_SERVICE_NAME=containerd
    OTEL_TRACES_SAMPLER=traceidratio
    OTEL_TRACES_SAMPLER_ARG=1.0

CRI-O takes a drop-in in `/etc/crio/crio.conf.d/` and
`-nri-test.scale-trace=127.0.0.1:4317 -nri-test.scale-trace-protocol=grpc`:

    [crio.tracing]
    enable_tracing = true
    tracing_endpoint = "127.0.0.1:4317"
    tracing_sampling_rate_per_million = 1000000

Limits to know about:

- Runtimes batch spans, so the test waits up to 30 s for the last ones.
- Full sampling slows the runtime. These are traced timings, and the
  report says so.
- Hook spans do not say which plugin they were for. Plugins are told apart
  by start order, which relies on NRI calling them in index order.
- A hook is attributed to a pod by the ids on its ancestor spans, or, for
  CRI-O whose spans carry none, by the trace context the test sets on each
  CRI call. containerd's `StopContainer` hook span is a root span with
  neither, so it shows up as unattributed and the teardown stage count for
  it is 0.
- With containerd and 3 plugins at 50 ms on `CreateContainer` and
  `StopContainer`, the traced per-pod CreateContainer to
  PostCreateContainer times matched the recorder-based ones to within a
  few milliseconds.

### What `Setup` does before any test runs

In order: dial CRI and probe capabilities; refuse to run if a kubelet process
is present, unless `-nri-test.allow-kubelet`; discover the runtime's installed
runtime handlers and refuse if the one `-nri-test.runtime-handler` names is not
among them; check that an advertised `CapExec` matches an actual `Executor`;
connect a preflight plugin to prove NRI answers; pull every test image unless
`-nri-test.no-pull`, because a suite that fails every case on
`ImagePullBackOff` is reporting the wrong defect; remove stale workloads left
by a previous run; report other runs' leftovers. Every phase has its own
timeout and any failure exits non-zero rather than proceeding.

Two details of that order were decided rather than fallen into. **The kubelet
and runtime-handler phases sit before the preflight probe:** refusing an
unusable environment and validating a flag value are input validation and
belong ahead of any network work, and there is a practical consequence --
`preflight` needs a live NRI socket, so a validation phase after it could never
be reached by a unit test and the refusal would be unverifiable. And **the
kubelet phase asks the driver** whether it is kubelet-sensitive, through an
optional interface, rather than assuming: the CRI driver says yes, while a
Kubernetes driver says nothing, because there a running kubelet is the
precondition rather than the hazard.

Kubelet detection scans `/proc/<pid>/comm` for `kubelet`, which needs no
privileges. Any process whose `comm` is `kubelet` trips it, and
`-nri-test.allow-kubelet` exists for that case. A listening-port probe was
rejected because kubelet's ports are configurable, and looking for
Kubernetes-managed sandboxes over CRI was rejected because those pods keep
running after kubelet stops -- so the suite would refuse on precisely the node
that had been prepared correctly.

For a fresh run `Setup`'s sweep is close to a no-op, because the run has
created nothing yet. The real work happens in `Teardown` and per-case cleanup,
driven by the ids the driver tracked. The sweep still is not dead code: it
catches a pod an earlier phase of this run lost track of, and it is what cleans
up when `-nri-test.run-id` is deliberately re-used.

## Recorded results

The baseline a change must not regress, both measured from a clean tree with a
byte-identical binary:

| Runtime | Leaf tests | Pass | Fail | Skip | runc vs crun |
|---|---|---|---|---|---|
| CRI-O 1.37.2 | 45 | 43 | 0 | 2 | identical |
| containerd v2.3.3 | 45 | 44 | 0 | 1 | identical |

The one test that differs between runtimes is
`TestNRIVersionExchange/should_agree_with_the_version_inferred_from_the_runtime_version`:
it skips on CRI-O, because `api.InferVersionFromRuntime`'s table has no entry
for 1.37.x, and passes on containerd. It logs a stale-table note and passes
either way; it does not fail.

## Invariants a change must not break

These are the things reviews of this suite keep catching. Check a change
against them before anything else.

- **A test must be able to fail.** The characteristic failure of a conformance
  suite is asserting nothing while reporting success, and every defect of
  substance found in this tool has been of that kind: fixtures whose `Name`
  equalled their `Id`, so confusing the two passed everything; a cpuset
  comparison pinned only up to set *size*, so "the same CPUs" and "the same
  number of CPUs" were indistinguishable -- precisely the bug a plugin pinning
  a container to the wrong socket produces; a filter whose dropped-update event
  nothing read; a 34-failure run whose summary said no failures were collected.
  None would have been found by reading. For any new test, ask what mutation it
  would catch, then **make that mutation and confirm a *named* test fails**.
  When the faithful mutation is awkward to express, say that you substituted
  one: a stronger stand-in can fail for a different reason than the property
  you meant to check, which reads as success and is not.
- **A kernel-surface assertion must be able to fail on the host it runs on.** A
  container with no cpuset adjustment is allowed every online CPU, so on a
  one-CPU node `Cpus_allowed_list` is already exactly `0`, and a test asking
  for `0` passes whether or not the adjustment took effect, whether or not the
  plugin was invoked, and whether or not the runtime implements NRI at all. The
  same holds for `Mems_allowed_list` on a single-NUMA-node host. The failure
  mode is invisible on a large machine: this was found on a 44-CPU host. So
  such a test must either target a value the host's default cannot already
  satisfy, or skip with a message saying what could not be measured. The
  companion guard is asserting the hook fired -- every adjustment test requires
  the plugin to have received `CreateContainer` for the container it inspects.
  The two cover different halves and neither subsumes the other.
- **Never disturb a workload the suite did not create.**
- **A destructive predicate is derived from scratch, never borrowed from a
  filter.**
- **Expectations are pure functions returning `error`.** They take evidence,
  never a `*testing.T`.
- **A skip is the test's decision, made before it asserts.**
- **Skips go through `framework.Skipped`.**
- **Do not add API without a live caller.** Four unreachable affordances have
  been removed from this tool; each was a route around a guard or a flag that
  silently did nothing.
- **A claim in a comment, a commit message or a document that the evidence does
  not support is a defect, not prose.** Several have been withdrawn after
  measurement.

## Measured facts

So they are not re-derived.

- **Read `/proc/self/status`, never `/proc/1/status`.** With a pod-wide PID
  namespace -- cri-api's default, `-nri-test.cri-namespaces=cri`, or a pod
  with `shareProcessNamespace` -- PID 1 is the sandbox's pause process, which
  is not in the container's cgroup. Measured on containerd v2.3.3 with
  `CpusetCpus: "0-1"`: `/proc/self` reported `0-1` while `/proc/1` reported
  `0-43`. With a PID namespace per container, which is kubelet's default and
  the suite's, PID 1 is the container's own init and agrees with `/proc/self`
  in every case measured. `/proc/self` is correct in both, so it is the only
  form the suite uses. `oom_score_adj` is the exception: read it from PID 1
  only in a per-container PID namespace, where that is the container's init.
- **`/proc/self` and the cgroup agree in every combination measured.** On the
  test node (cgroup v2, 16 CPUs, 4 NUMA nodes), with CRI-O 1.37.2 and containerd
  2.3.3, each with runc and crun, a pod PID namespace and a container one, and
  the cpuset set at creation (CRI config or an NRI adjustment) or by an update
  after start: the exec'd `cat /proc/self/status` matched the container's
  `cpuset.cpus.effective` and `cpuset.mems.effective` every time, and the same
  held for pods made through kubelet. `/proc/1` matched only in a per-container
  PID namespace; in a pod-wide one it reported every CPU and NUMA node. No
  difference between runc and crun was seen. cgroup v1 is refused by `Setup`.
- **The k8s driver passes on containerd too.** With the node's kubelet pointed
  at containerd 2.3.3 (containerd's runc and crun handlers set to
  `SystemdCgroup = true` to match the kubelet's systemd cgroup driver), the
  whole suite passed with runc and with crun: 54 passed, 8 skipped, none
  failed, each.
- **A pod's termination grace and its deletion grace must agree.** The k8s
  driver once created pods with a 2 s `terminationGracePeriodSeconds` but
  deleted them with grace 0, and then removed the sandbox over CRI itself; about
  half the runs lost a race with kubelet's own teardown (CRI-O: `no such id`)
  and leaked a pod. One `-nri-test.stop-grace` now feeds both, and 12 of 12
  runs passed. The removal retry alone also cured the old mismatch (3 of 6 runs
  retried, all passed), so it stays as a logged safety net.
- **`drop_infra_ctr = true` drops the infra container for every pod, but the
  infra process only where the pod's PID namespace mode permits.** The suite
  sets no `NamespaceOptions` under `-nri-test.cri-namespaces=cri`, and
  cri-api's `NamespaceMode` has `POD = 0`, so those pods request a shared pod
  PID namespace and something must hold it. By default the suite sends what
  kubelet does, `pid: CONTAINER`, as the `kube-system` pods on the test node
  do; those have no sandbox pid at all.
- **A created-but-not-started container has no cgroup yet**, so any assertion
  about what the kernel applied needs a running container.
- **CRI pod and container annotations do not reach a container's OCI spec**
  unless the runtime's passthrough allowlist names them, and those are empty by
  default -- containerd's `PodAnnotations` and `ContainerAnnotations`. A created
  container's `spec.annotations` holds only the runtime's own keys. So a test
  must never use an annotation it set through CRI as evidence that it is
  looking at the right spec; container *environment* is the right evidence,
  because it is data the test chooses and both runtimes must carry CRI
  container env into `spec.process.env`.
- **A runtime's own spec annotation keys are not portable.** CRI-O uses
  `io.container.manager`, `io.kubernetes.cri-o.*` and
  `io.kubernetes.container.*`; containerd uses `io.kubernetes.cri.*`.
- **CRI-O requires `ContainerConfig.Image.UserSpecifiedImage`.** With it empty
  it refuses every container creation with "user specified image not specified,
  cannot verify image signature". containerd does not require it, which is why
  the omission went unnoticed until the first CRI-O run.
- **An entry with an empty name in `StatusResponse.RuntimeHandlers` is the
  runtime's default** and is never selectable. CRI-O 1.37.2 reports `crun`,
  `runc`, and one entry with no name at all.
- **The suite's pods get cgroup path `system.slice:crio:<id>`**, not a
  `kubepods-...slice` path, because the suite sets no `CgroupParent`. The
  systemd triple form breaks no expectation.
- **Both runtimes report the container's init pid** in
  `ContainerStatus(verbose)`'s `Info["info"]`, and `/proc/<pid>/environ` is
  readable. `ParseVerboseInfo` decodes only `runtimeSpec`, so the suite does
  not use it yet; it is the genuine second surface `ExpectEnv` lacks.
- **Both runtimes report container environment and annotations during
  `Synchronize`**, so the two fidelity hatches in `sync_test.go` have never
  fired. Two runtimes are not grounds for removing them.
- **Closing the local `ssh` does not stop a remote suite.** Measured: a SIGTERM
  during `TestSmoke` exited the script 130 and restarted kubelet while the
  suite ran on for nine more leaf tests, eight of them pod-creating. The local
  `ssh` outlived the signal, so the pty never closed and no SIGHUP reached the
  node. `sudo`'s `use_pty`, suite-side signal handlers and the trap shape were
  each ruled out. The script now kills the remote process explicitly and
  confirms it is gone before touching kubelet.
- **A non-interactive shell's asynchronous list inherits SIGINT ignored**, and
  a signal ignored on entry cannot be trapped. So SIGINT cannot stop a
  backgrounded run at all; send such a run SIGTERM. In the foreground, INT,
  TERM and HUP all trap.

## Input conformance

What a runtime tells a plugin about a pod and a container, as opposed to what it
does with what a plugin returns. This is the layer the adjustment tests rest on:
those inject constants, so they would pass whether or not the `Container` a
plugin was handed bore any resemblance to the container being created, while a
real plugin computes its adjustment *from* the input.

`framework/inputspec.go` is the specification. Every field of `PodSandbox`,
`LinuxPodSandbox`, `Container` and `LinuxContainer` is either in `InputFields`,
meaning a test verifies it, or in `inputFieldsOutOfScope` with a stated reason.
A test walks the protocol's own descriptors and fails naming any field in
neither, so `pkg/api` cannot grow a field that silently goes unlooked-at. The
same table is what the summary's coverage section is computed from, which is why
it is data rather than a set of test names.

Each field carries the earliest hook by which the runtime must have populated
it. Before that hook, absence is not a finding; at or after it, absence is. The
strictness is a first guess held deliberately -- most fields are expected
already at `CreateContainer`, and whether a runtime that fills one in only by
`PostStartContainer` should fail is a question runs against more runtimes will
answer. `RequiredAt` is one declaration per field so that moving a row costs one
line.

Two assertion forms, and the difference matters to what a failure means.

**Fidelity** sets the value over CRI and reads it back off the hook request, so
a failure is a runtime that did not report what it was given. Every value is one
a stock container would not have: an `oom_score_adj` of zero is CRI's own
default and would pass without the runtime reading the field at all.

**Agreement** compares the plugin's view against what the runtime realized --
the OCI spec it wrote, or CRI's own status -- so a failure is a report and a
container that disagree. The comparison is subset, not equality: NRI projects
the OCI spec rather than copying it, and a runtime adds defaults of its own, so
equality would fail every runtime on things that are not defects. Subset alone
cannot fail for a runtime that reports *nothing*, since the empty set is a
subset of anything, so every agreement check pairs it with a non-vacuity guard.
Omission is the defect class this layer exists for. Where a field may
legitimately be empty the subtest omits the guard and says why; the omission is
a claim that has to be argued, not a default.

`Container.state` is neither form. It is populated at every hook with a
different correct value at each, so it gets a ladder -- `CONTAINER_CREATED` at
`PostCreateContainer`, `CONTAINER_RUNNING` at `PostStartContainer`,
`CONTAINER_STOPPED` at `RemoveContainer`. `CONTAINER_UNKNOWN` is the enum's zero
value, so the ladder is also the check that the field was set at all.

### The CDI spec, and the one piece of node state

A CDI device named on a CRI call has to resolve or the runtime refuses the
creation, so the run writes a CDI spec and removes it again. It is the only node
state outside a workload that this suite creates, and three conditions make that
acceptable: the file goes in `/var/run/cdi`, which is transient, never
`/etc/cdi`; the vendor is `nri-test.containerd.io`, so the devices are
recognisable as ours on a shared node; and the devices are `/dev/null` and
`/dev/zero` exposed as `/dev/test-null` and `/dev/test-zero`, so a fixture that
outlived its run could not hand a container anything that matters.

The file is named for the run and removed by the run that wrote it -- one path,
never a glob and never a sweep, because `/var/run/cdi` is where anything on the
node may declare a device. The directory is removed too, but only when the run
created it and only when it is empty.

### Measured, 2026-10-20, CRI-O 1.37.2 / NRI v0.12.2 / runc

33 of 41 fields measured, 17 subtests passing, eight findings. *Measured*, not
*verified*: a field whose test ran and failed was still measured, so the count
is what the run looked at rather than what it approved. The node was
byte-identical before and after every run: the same 14 sandboxes, and no
`/var/run/cdi` left behind.

| Field | What the runtime does |
|---|---|
| `Container.CDI_devices` | **not reported.** The device *is* injected -- `/dev/test-null` is a character device inside the container -- so CRI-O applies the CRI field and does not tell the plugin. Applied but not reported |
| `Container.image` | not reported at all: `name`, `digest` and `config_digest` are all unset |
| `LinuxContainer.sysctl` | empty for a pod created with `net.ipv4.ip_forward=1` |
| `LinuxContainer.seccomp_profile` | reports `RUNTIME_DEFAULT` when `UNCONFINED` was asked for. The realized OCI spec carries no seccomp section, so here too the profile is applied and misreported |
| `LinuxContainer.seccomp_policy` | not reported, while the realized spec carries a seccomp section. The policy too is applied and not reported |
| `Container.started_at` | not populated at `PostStartContainer` |
| `PodSandbox.pid` | not reported, while the sandbox has one. `Container.pid` *is* reported, so the two differ |
| `Container.state` | `CONTAINER_CREATED` at `PostStartContainer`, not `CONTAINER_RUNNING`. The other two rungs pass |

Two of those rows exist only because a first version of their test could not
have found them. The `seccomp_policy` check originally asked for an unconfined
profile, which means the realized spec carries no seccomp section at all, so
the only comparison available was "neither side has one" -- omission, the
defect class the whole tier is for, was undetectable by construction. And both
`pid` checks compared the reported value against an oracle that is left at zero
when the runtime reports none, so `0 == 0` passed. A subset check needs its
non-vacuity guard, and an agreement test must not arrange the state that hides
what it is looking for.

The two CDI assertions are independent on purpose, and that is what makes the
first row attributable: a runtime can report a device it did not inject, or
inject one it did not report, and only running both says which happened.

The seccomp row contradicts `capbilityVersionMap` on the
`devel/version-exchange-and-capabilities` branch, which claims
`INPUT_LINUX_SECURITY_PROFILE` from cri-o v1.34.0. That is the first measured
instance of the gap between the version-based capability inference and what a
runtime actually does, and it is the evidence that work needs.

Eight rows in the table have no test yet and the coverage section names them
every run: `LinuxPodSandbox.{cgroups_path,namespaces,resources}`,
`Container.{hooks,status_reason,status_message}` and
`LinuxContainer.{devices,resources}`.

## Coverage

### Ported from `pkg/adaptation`

| Test | How it ports |
|---|---|
| `TestPluginConnection` -- invalid name rejected | connect stubs named `abcd-0123+EFGH_4567.ijkl` (must succeed) and `foo,bar` (must fail) |
| `TestPluginConnection` -- plugin configured | assert `Configure` arrived with a non-empty runtime name and version |
| `TestPluginConnection` -- synchronize after configuration | create pods and containers first, then connect; assert the `Synchronize` request carries them, with field-level checks on the protos |
| `TestPodAndContainerRequestsAndEvents` -- no plugins | a full lifecycle with no plugin connected must succeed |
| -- honour event subscriptions | one subtest per event mask plus the four combination masks; subscribed events arrive and unsubscribed ones do not |
| -- multi-plugin invocation order | three plugins at index 00, 20, 99 with a shared ordered recorder |
| `TestAdjust*` | **built:** annotation, environment, mount, cpuset and NUMA nodes. Annotation is create-only against the realized spec; the other four start the container, and cpuset and NUMA read the kernel. Five of roughly twenty-five subjects. The input layer's coverage is no longer hand-counted: the run prints it, see [Input conformance](#input-conformance) |
| `TestPluginConfigurationRequest` | runtime name and version non-empty; advertised timeouts sane |
| `TestNRIVersionExchange` | advertised NRI version is valid semver, cross-checked against `InferVersionFromRuntime` |

### Not built

- **Conflicts and validating plugins.** `CreateContainer` must fail and leak no
  container. A validator subscribing to `VALIDATE_CONTAINER_ADJUSTMENT`
  asserts the request's `adjust`, `owners` and `plugins` contents, then
  exercises the reject path. A validator's `owners` map is the only way a
  plugin can observe the ownership record at all, which makes validator support
  a prerequisite for the multi-plugin variant rather than an independent piece.
- **Updates:** during creation, solicited through CRI
  `UpdateContainerResources`, and unsolicited through `stub.UpdateContainers()`.
- **The configuration-dependent suites** above.

### Skipped with a TODO

`UPDATE_POD_SANDBOX` and `POST_UPDATE_POD_SANDBOX` subscriptions, and
`Driver.UpdatePodResources`. Pod-level resource updates are likely not fully
implemented in either runtime and there is no side-effect-free way to find out,
since asking means issuing a real `UpdatePodSandboxResources` call. So
`CapUpdatePodResources` is **never advertised** -- the CRI driver deliberately
does not probe it -- and the tests skip unconditionally with a stated reason.
The capability gate is left in place after the skip so it becomes live the
moment a runtime is confirmed to implement the path.

### Not portable

These have no external expression and remain unit tests: `TestAdaptation`
(nil `SyncFn`/`UpdateFn` rejection, an in-process API contract);
`TestConfiguration` (startup with and without options, except the
external-connections case, which becomes an opt-in suite);
`TestPluginConnection`'s close-on-failed-synchronization, which needs a failure
injected into the runtime's own sync function; and all 15 tests in
`metrics_test.go`, which is runtime-internal metric plumbing.

### New, only possible against a real runtime

- A plugin hook returning an error fails the corresponding CRI operation and
  leaks no pod or container.
- A plugin disconnecting mid-lifecycle leaves the runtime working; reconnecting
  re-synchronizes it with current state.

## Known gaps and remaining work

- Roughly twenty creation-adjustment subjects beyond the five built.
- `CapObserveOpError` wants splitting in two: conflict detection, validator
  rejection and the hook-error tests all assert that an operation failed, and
  some want to assert *what* the error said. In Kubernetes mode the failure is
  observable but the text arrives asynchronously and reworded, so one bit would
  force the entire conflict half of the matrix to skip there.
- The leak and cleanup guards do not record into the summary.
- `ExpectMount` reads the first mount table line matching a destination; with
  mounts stacked at one point the last is in effect. Nothing stacks mounts
  today. `mountPoints` shares the indifference.
- The default (non-collecting) mode has unit tests behind it and has never been
  run against a node.
- CI wiring. `hack/run-nri-test.sh` is the committed manual-invocation path.
- **Spec coverage reporting**: enumerate the NRI surface from `pkg/api` -- the
  event mask's hooks, `ContainerAdjustment`'s fields, `ContainerUpdate`'s
  fields -- record which a run exercised and verified, and report it at the end.
  This is the coverage metric that fits the tool, for a structural reason: Go
  statement coverage cannot reach what is being tested, because the suite binary
  links only the *plugin* side of NRI while `pkg/adaptation`, whose conformance
  is in question, is compiled into the runtime -- a separate binary we neither
  build nor instrument. Instrumenting our own binary measures the harness, not
  the subject. `go build -cover` with `GOCOVERDIR` would yield real coverage
  from a runtime *you* built, but it attributes to that runtime's vendored copy
  of this code, which under version skew is not the same code, and it cannot
  apply to the installed runtimes this suite exists to test. Spec coverage has
  neither problem, and it replaces hand-counted claims -- the "five of roughly
  twenty-five subjects" figure above is hand-counted, and hand-maintained
  claims in this document's predecessor went stale three separate times.

## Open questions

- **CRI-to-NRI hook mapping for pod resource updates** is unconfirmed in both
  runtimes. Handled by probing rather than assumption; see the skip above.
- **`Container` proto fidelity beyond the fields now measured.** Answered for
  the 33 fields the input layer verifies; see
  [Input conformance](#input-conformance). What is still open is the eight table
  rows with no test, and whether the single required hook per field is the right
  strictness -- `Container.started_at` and `Container.state` both fail against
  CRI-O at the hook the table names, and whether that is the runtime's defect or
  the table's expectation is not yet settled.
- **Kubernetes mode observation.** `k8s.Driver` observes through the unchanged
  `cri.Inspector`, which requires the suite to run on the node whose runtime
  kubelet drives. If that turns out to be unacceptable, the fallback is a
  node-local inspector deployed as a DaemonSet.
