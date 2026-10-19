# Library defects the mock arm found

What the `-nri-test.driver=mock` arm settles about the in-tree libraries,
rather than about any runtime. `RUNTIME-STATUS.md` records runtime status;
this file records defects in `pkg/adaptation` and
`pkg/runtime-tools/generate` that the mock arm is the only thing able to
reach.

Written and maintained by hand. The mock is not a runtime, and the status
report's loader refuses its fragments, so nothing here is generated.

`-nri-test.driver=mock` runs the conformance suite against
`pkg/adaptation` with no runtime, no node and no root. It is a control
arm, not a third runtime: what it measures is the NRI side of the
protocol -- the stub, the proto round trip, the merge across plugins, and
the in-tree adjustment-to-spec mapping -- and nothing below it. A clean
run here says that chain held. It says nothing about CRI-O or containerd,
and the summary says so twice.

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

## What the arm settles about the in-tree spec generator

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
forwards.

- **FIXED.** **An environment variable or a device the container already
  has could not be removed by a plugin.** `adjustEnv` and
  `adjustDevices` in `pkg/adaptation/result.go` resolved a removal
  marker themselves -- stripping the variable from the creation request
  and from adjustments collected from earlier plugins -- and never put
  the marker into the adjustment the runtime applies. `adjustMounts` did
  forward one, in a step titled "apply deletions with no corresponding
  additions", and `adjustAnnotations`, `adjustSysctl` and
  `adjustNamespaces` forwarded theirs too. So `RemoveEnv` and
  `RemoveDevice` worked plugin-to-plugin but not against the container's
  own environment or devices.

  Both now forward a deletion with no corresponding addition, as
  `adjustMounts` does, on the branch `fix/adjust-env-device-removal`.
  `pkg/adaptation` has a case per removal beside the existing mount one,
  and `generate`'s removal branch for each -- reachable through NRI for
  the first time -- has one too.
- **RESOLVED BY THAT FIX.** **`AdjustEnv`'s modify-existing block had no
  reachable effect.** `AddProcessEnv` already replaces a key in place,
  and the block's other job is removal, which nothing could reach. The
  removal job is reachable now, and a test fails if the block stops
  honouring the marker.
- **OPEN.** **Two adjustments of one environment key are refused, not
  merged.** `pkg/adaptation` reports `plugins "X" and "X" both tried to
  set Env K` -- naming the one plugin twice -- so `generate`'s own
  last-one-wins never runs.

And one inconsistency inside `generate` itself, **being improved in its
own PR**: **`AdjustRlimits` is the only `Adjust*` entry point that
neither deduplicates nor honours a removal marker.** It appends
unconditionally, so adjusting a limit the container already has, such as
the `RLIMIT_NOFILE` every container here is created with, leaves two
entries for one type. The test asserts only that the requested limit is
present, so whoever changes this does not have to rewrite it.

## What the arm settles

The runtime columns are the versions they name and are not refreshed by
any run. What the runtimes do now is in `RUNTIME-STATUS.md`, which a run
does refresh; where the two disagree, that file is the measurement. The
`mock` column and the conclusion are what this table is for.

| Field | mock | CRI-O 1.37.2 | containerd 2.3.3 | What that means |
|---|---|---|---|---|
| `Container.image` | **passes** | not reported | not reported | NRI carries it; the gap is **below NRI** -- the CRI layer, or how both runtimes populate the NRI message from it |
| `LinuxContainer.sysctl` | **passes** | empty | empty | same: the transport is fine, the gap is below it |
| `Container.status_reason` | withheld | not reported | not reported | unclassified: this driver decides how a container ends, so it cannot answer |

That is the arm's purpose, and it is discharged. Two of the three
fields that fail identically on both runtimes are now located below NRI
rather than merely "pointing at the protocol or the CRI layer". What it
cannot do is go further: with no CRI beneath it, the mock cannot say
whether CRI hands the runtime an image name at all.

## What it cannot cover

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

## What "agreement" means on this arm

Nine fields count as measured on this arm but measure something weaker
than they do against a runtime: `Container.rlimits`,
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

## What it found about the suite and the mock, not about NRI

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

