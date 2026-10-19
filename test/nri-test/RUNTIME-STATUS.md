# Runtime status

What the runtimes the suite has been run against actually do with NRI, as
measured. `README.md` describes the suite; this file records what the suite
found, per runtime and per version, so a reader can tell a runtime's gap from
a suite gap.

Every row here is a measurement on a real node, not an inference from a
version number. Nothing here is a conformance score: the suite exercises a
fraction of NRI, so a clean column means "nothing this suite looked at
failed", which is weaker than "conformant" and must not be quoted as it.

## Runtime behaviour the suite had to measure to work at all

Facts about the runtimes themselves, found while building the suite and
recorded so they are not re-derived. They shape how a test reads a
result rather than being findings about conformance.

- **The k8s driver passes on containerd too.** With the node's kubelet pointed
  at containerd 2.3.3 (containerd's runc and crun handlers set to
  `SystemdCgroup = true` to match the kubelet's systemd cgroup driver), the
  whole suite passed with runc and with crun: 54 passed, 8 skipped, none
  failed, each.
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
- **Both runtimes report the container's init pid** in
  `ContainerStatus(verbose)`'s `Info["info"]`, and `/proc/<pid>/environ` is
  readable. `ParseVerboseInfo` decodes only `runtimeSpec`, so the suite does
  not use it yet; it is the genuine second surface `ExpectEnv` lacks.
- **Both runtimes report container environment and annotations during
  `Synchronize`**, so the two fidelity hatches in `sync_test.go` have never
  fired. Two runtimes are not grounds for removing them.

## Measured, 2026-10-20: CRI-O 1.37.2 and containerd 2.3.3, CRI driver, runc

Both runs used the CRI driver against the runtime's own CRI and NRI sockets,
with `runc` as the handler, `-nri-test.collect-failures` so a test reports
every expectation rather than stopping at its first, and the node's seven
live `kube-system` pods left alone. The node was unchanged after each run:
the same 14 CRI-O sandboxes and no containerd sandboxes before and after,
and neither `/etc/cdi` nor `/var/run/cdi` left behind. Every failing test
maps to a finding in the summary, so nothing failed outside it.

| | CRI-O 1.37.2 | containerd 2.3.3 |
|---|---|---|
| NRI version negotiated | v0.12.2 | v0.12.0 |
| Input fields measured | 39 of 41 | 39 of 41 |
| Input fields with a finding | 9 | 4 |
| Expectation failures | 11 | 5 |
| Failed operations | 0 | 0 |

A field can yield more than one expectation failure, which is why the two
counts differ: `Container.image` fails both as an absent message and as a
name that does not match, and `seccomp_policy` fails both its presence and
its syscall set.

Every failure on both runtimes is in the input layer. The behavioral
conformance tests -- hook delivery, subscription, creation adjustments,
mounts, the event ladder, and the leak and cleanup guards -- pass on both,
and no test failed outside the summary. The two runtimes differ only in
which NRI input fields they populate.

Two tests skipped rather than ran, and a skip is not a pass. Pod resource
updates skipped on both, because they are not verified against any runtime
yet. `TestNRIVersionExchange/should_agree_with_the_version_inferred_from_the_runtime_version`
skipped on CRI-O alone: `api.InferVersionFromRuntime` has no entry for
1.37.x, while it does infer containerd v2.3.3, where the test ran and
passed.

### The input surface, side by side

| Field | CRI-O 1.37.2 | containerd 2.3.3 |
|---|---|---|
| `Container.image` | not reported: `name`, `digest`, `config_digest` all unset | same: not reported |
| `LinuxContainer.sysctl` | empty for a pod created with `net.ipv4.ip_forward=1` | same: empty |
| `Container.state` | `CONTAINER_CREATED` at `PostStartContainer`, not `CONTAINER_RUNNING`; the `PostCreateContainer` and `RemoveContainer` rungs pass | `CONTAINER_UNKNOWN` at `RemoveContainer`, not `CONTAINER_STOPPED`; the `PostCreateContainer` and `PostStartContainer` rungs pass |
| `Container.CDI_devices` | **applied but not reported.** `/dev/test-null` *is* a character device in the container, so the CRI field is applied and the plugin is not told | **reported** |
| `LinuxContainer.seccomp_profile` | reports `RUNTIME_DEFAULT` when `UNCONFINED` was asked for: applied and misreported | reported correctly |
| `LinuxContainer.seccomp_policy` | not reported, while the realized spec carries a seccomp section | reported |
| `Container.started_at` | not populated at `PostStartContainer` | populated |
| `PodSandbox.pid` | not reported. `Container.pid` *is* reported, so the two differ. CRI-O also exposes no sandbox pid over CRI, so there is no oracle to compare against here: the finding rests on the field table requiring one at `RunPodSandbox`, not on a reading of the sandbox | reported, and agrees with CRI's own |
| `Container.status_reason` | not reported, while CRI's status says `Error` for a container that exited 3 | same: not reported |

Fields not in this table were measured and agreed on both runtimes:
`rlimits`, `created_at`, `finished_at`, `exit_code`, pod `ips`,
`Container.pid`, the container identity, metadata, args, env, mounts, user
and oom score, and -- newly exercised on 2026-10-20 --
`LinuxPodSandbox.{cgroups_path,namespaces,resources}` against the sandbox's
own OCI spec, plus `LinuxContainer.{namespaces,cgroups_path,devices,resources}`
against the container's.

### What the comparison adds

- **The CDI premise holds.** `Container.CDI_devices` is reported by
  containerd and not by CRI-O, which is what the input layer was built to
  find out. It is now measured on both sides rather than expected on one.
- **`Container.state` is wrong on both runtimes, at different rungs, and
  each passes the other's.** No single reading of the field table explains
  both, which weakens the "the table is too strict" interpretation of the
  CRI-O row and makes this a question about what the protocol intends a hook
  to imply.
- **`Container.image`, `LinuxContainer.sysctl` and
  `Container.status_reason` fail identically on both.** Three independent
  fields where two independent runtimes behave the same way, which points at
  the protocol or the CRI layer rather than at either runtime, and is a
  different kind of signal from a one-sided gap. `status_reason` is the
  clearest of the three: CRI's own status carries `Error` for the container
  that exited 3, and the plugin is shown an empty string.
- **Version-based capability inference is wrong in both directions.**
  `capbilityVersionMap` on the fork branch
  `devel/version-exchange-and-capabilities` claims
  `INPUT_LINUX_SECURITY_PROFILE` from cri-o v1.34.0, yet CRI-O 1.37.2
  misreports `seccomp_profile` while containerd 2.3.3 gets it right. The
  version table cannot infer an NRI version for cri-o 1.37.x at all, as the
  skip above shows, so it is both unable to answer for one runtime and wrong
  about the other. Two runtimes, two different gaps, and no version number
  predicts either.

### Not covered by these runs

- Two of the 41 input fields were not exercised, and the coverage section
  names them every run. `Container.hooks`: CRI cannot ask for an OCI hook and
  neither runtime adds one on this node, so a report of none cannot be told
  from an omission -- the subtest skips rather than counting a comparison that
  could not fail. `Container.status_message`: neither runtime fills in a CRI
  message for a container that exits non-zero, so there is nothing to compare
  a report against. Both are reachable with node configuration or a different
  failure mode, and neither is counted as measured.
- `crun` was not re-run on 2026-10-20. On 2026-10-18 the handler made no
  difference to the conformance result on either runtime.
- The k8s driver was not used. It withholds `CapCDIDevices`, `CapSysctls`
  and `CapSecurityContext` by design, because a Kubernetes pod cannot name a
  CDI device and can set only the sysctls kubelet deems safe, so three of
  the rows above are unreachable through it.
- Pod resource updates are skipped on both: they are not verified against
  any runtime yet.

## Measured, 2026-10-20: the mock arm, no runtime under test

`-nri-test.driver=mock` runs the conformance suite against
`pkg/adaptation` with no runtime, no node and no root. It is a control
arm, not a third runtime: what it measures is the NRI side of the
protocol -- the stub, the proto round trip, the merge across plugins, and
the in-tree adjustment-to-spec mapping -- and nothing below it. A clean
run here says that chain held. It says nothing about CRI-O or containerd,
and the summary says so twice.

**103 passed, 0 failed, 19 skipped. 28 of 41 input fields measured.**

Fourteen skips are capability gates: seven need `ObservedRuntimeState`,
which this driver withholds because the pids, timestamps, lifecycle
states, addresses and termination status it would report are its own
invention; five need `Exec`, which needs a running container; one needs
`CDIDevices`; and one needs `ContainerLifecycle`. Of the rest, the
runtime-handler, pod-resource and scalability gates skip on any driver,
one is `Container.hooks`, which no runtime adds here, and **one is
inherent to the arm**: the second half of `TestNRIVersionExchange`
cross-checks the advertised NRI version against the one inferred from
the runtime version, and there is no runtime here to infer from. The arm
advertises the NRI version it was built from, so `RequireNRIVersion` and
every other version gate run here -- but only in a build that generated
`pkg/version/info/git`, which `make nri-test` does and a bare
`go test` in a fresh checkout does not.

### What the arm settles about the in-tree spec generator

The mock applies every creation adjustment through
`pkg/runtime-tools/generate`, with no runtime in between, so on this arm
an assertion about the adjusted spec is a test of that library. The
tests in `suite/generate_test.go` gate on `CapInTreeAdjustments`, which
only this driver advertises, and aim at the merge rather than the write:
replacing a value the spec already carries, and honouring a leading
`-` on a key as a removal marker. Each one was mutation checked, and
`AdjustEnv`, `AdjustArgs`, `AdjustAnnotations`, `AdjustSysctl`,
`AdjustMounts`, `AdjustNamespaces`, `AdjustResources` and
`AdjustRlimits` all have a named test that fails when they stop doing
their job.

Three limits on what a plugin can reach came out of writing them. None
is a defect in `generate`; all three are in what `pkg/adaptation`
forwards, and all three are untested elsewhere:

- **An environment variable or a device the container already has
  cannot be removed by a plugin.** `adjustEnv` and `adjustDevices` in
  `pkg/adaptation/result.go` resolve a removal marker themselves --
  stripping the variable from the creation request and from adjustments
  collected from earlier plugins -- and never put the marker into the
  adjustment the runtime applies. `adjustMounts` does forward one, in a
  step titled "apply deletions with no corresponding additions", and
  `adjustAnnotations`, `adjustSysctl` and `adjustNamespaces` forward
  theirs too. So `RemoveEnv` and `RemoveDevice` work plugin-to-plugin
  but not against the container's own environment or devices, and
  `generate`'s removal branch for both is unreachable through NRI.
- **`AdjustEnv`'s modify-existing block therefore has no reachable
  effect.** `AddProcessEnv` already replaces a key in place, and the
  block's other job is removal, so skipping it changes nothing a test
  can observe. That mutation is the one the suite deliberately does not
  catch.
- **Two adjustments of one environment key are refused, not merged.**
  `pkg/adaptation` reports `plugins "X" and "X" both tried to set Env K`
  -- naming the one plugin twice -- so `generate`'s own last-one-wins
  never runs.

And one inconsistency inside `generate` itself: **`AdjustRlimits` is the
only `Adjust*` entry point that neither deduplicates nor honours a
removal marker.** It appends unconditionally, so adjusting a limit the
container already has, such as the `RLIMIT_NOFILE` every container here
is created with, leaves two entries for one type. The test asserts only
that the requested limit is present, so whoever changes this does not
have to rewrite it.

### What the arm settles

| Field | mock | CRI-O 1.37.2 | containerd 2.3.3 | What that means |
|---|---|---|---|---|
| `Container.image` | **passes** | not reported | not reported | NRI carries it; the gap is **below NRI** -- the CRI layer, or how both runtimes populate the NRI message from it |
| `LinuxContainer.sysctl` | **passes** | empty | empty | same: the transport is fine, the gap is below it |
| `Container.status_reason` | withheld | not reported | not reported | unclassified: this driver decides how a container ends, so it cannot answer |

That is the arm's purpose, discharged on its first run. Two of the three
fields that fail identically on both runtimes are now located below NRI
rather than merely "pointing at the protocol or the CRI layer". What it
cannot do is go further: with no CRI beneath it, the mock cannot say
whether CRI hands the runtime an image name at all.

### What it cannot cover

- Everything needing a kernel: in-container observation and the cgroup
  readback. This costs more than it sounds: four of the five adjustment
  subtests assert the spec half and the kernel half in one case and gate
  on `Exec`, so they skip **whole**. The end-to-end exercise of
  `generate.Adjust` under this arm is annotations alone. The spec's third
  payoff -- putting `pkg/runtime-tools/generate` under test across its
  ~25 entry points, including the RDT, io-priority and net-device
  dimensions no node here can reach -- is therefore **not** delivered.
  Splitting those four subtests into a spec half and a kernel half is the
  cheap follow-up that would deliver it.
- `Container.CDI_devices`: the mock runs no CDI registry, so it could
  only report a device name it never injected, and the CDI test's two
  assertions exist precisely to tell those apart.
- The fields it authors, listed above. They print as not exercised, and
  a test fails if one of them is ever counted as measured here.

### What "agreement" means on this arm

Nine fields are counted in the 28 but measure something weaker than they
do against a runtime: `Container.rlimits`,
`LinuxContainer.{namespaces,cgroups_path,devices,resources,seccomp_policy}`
and `LinuxPodSandbox.{cgroups_path,namespaces,resources}`. `Inspect`
returns the same `*rspec.Spec` that the hook message was projected from,
and the suite's input plugin returns no adjustment, so both sides of
those comparisons come from this driver's own model.

They are not vacuous -- the shown side makes the full
stub-to-proto-to-Adaptation round trip, so a transport that dropped
namespaces or mangled a cgroups path would still be caught -- but what
they measure here is **transport, not agreement**. Read those nine rows
as "NRI carried it", not as "the runtime realized what it reported". The
agreement tier means what it says only against a real runtime, and the
design's rule is the reason this paragraph exists rather than nine gates:
gating them would discard a real transport check, and leaving them
undisclosed would overstate the arm.

### What it found about the suite and the mock, not about NRI

- A runtime must hold `Adaptation.BlockPluginSync()` around each hook it
  dispatches. A plugin becomes eligible for events only after its
  `Synchronize` call returns, so a plugin that considers itself ready can
  still miss a hook dispatched in that window. Real runtimes hold the
  block; a runtime fast enough to create a pod inside the window has to.
  This driver takes it per `Adaptation` call rather than across a whole
  driver operation, which matches NRI's own per-call granularity and
  narrows the window rather than closing it between calls.
- Four gaps in the mock itself, all fixed and all pinned by tests: a
  cgroups path built from names where a runtime builds it from ids; a pod
  removal that did not stop its containers first; a security context it
  advertised and did not apply, which left the suite's `seccomp_policy`
  check comparing two empties and passing; and a sysctl map shared by
  every container in a pod, where one container's adjustment would have
  rewritten its siblings' realized specs and produced a false finding
  against NRI.
- It will not take a node's NRI socket. Adaptation's listener unlinks
  whatever sits at the path before binding, so a mock run pointed at
  `/var/run/nri/nri.sock` as root would have stolen a live runtime's
  socket and removed it on exit. The driver now listens on a private
  temporary socket unless a path is given explicitly, refuses one
  something is already answering on, and publishes the path it chose so
  the suite's plugins find it.

### Reproducing it

```sh
export NODE=vagrant@node TIMEOUT=60s

# CRI-O
export ENDPOINT=unix:///var/run/crio/crio.sock NRI_SOCKET=/run/nri/nri.sock
bash hack/run-nri-test.sh runc

# containerd
export ENDPOINT=unix:///run/containerd/containerd.sock
export NRI_SOCKET=/run/nri-containerd/nri.sock
bash hack/run-nri-test.sh runc
```

`ENDPOINT` must be passed explicitly: an empty value autodetects the
containerd socket even when `NRI_SOCKET` is CRI-O's, and the run then hangs.
