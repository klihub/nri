# Runtime status

What each runtime the suite has been run against does with the NRI input
surface, as measured on a real node. A plugin author reads this to find out
what a runtime can be relied on to report; a runtime maintainer reads their
own column.

Nothing here is a conformance score. The suite exercises a fraction of NRI,
so `agreed` means "nothing this suite looked at failed", which is weaker
than conformant. Columns are not totalled, ranked or graded.

This file is generated: the prose lives in
`test/nri-test/status/runtime-status.md.tmpl`, the measurements in the JSON
fragments under `test/nri-test/status/fragments`, and `make nri-test-status`
renders one from the other. A hand edit here is lost at the next render.
`README.md` describes the suite itself, and `LIBRARY-DEFECTS.md` records
what the mock arm found about the in-tree libraries.

## Runs behind this report

| | containerd v2.4.2 | cri-o 1.37.3 |
|---|---|---|
| Runtime handlers | runc | runc |
| NRI version negotiated | v0.12.3 | v0.12.2 |
| Kernel | 6.17.1-300.fc43.x86_64 | 6.17.1-300.fc43.x86_64 |
| Architecture | amd64 | amd64 |
| Failures reported per test | every expectation failure | every expectation failure |
| Input fields measured, of 41 | 39 | 39 |
| Input fields with a finding | 3 | 9 |

Every column is one runtime and version, over every handler its fragments
were measured with, and every run used the CRI driver against the runtime's
own sockets. A cell naming a handler is one whose handlers' runs disagreed.

## The input surface

Every field a plugin is shown, and what each runtime did with it. `finding`
means the runtime and the suite disagreed; the detail is below the table.
`not measured` is not a pass.

| Field | containerd v2.4.2 | cri-o 1.37.3 |
|---|---|---|
| `PodSandbox.name` | agreed | agreed |
| `PodSandbox.uid` | agreed | agreed |
| `PodSandbox.labels` | agreed | agreed |
| `PodSandbox.annotations` | agreed | agreed |
| `PodSandbox.runtime_handler` | agreed | agreed |
| `PodSandbox.pid` | agreed | finding |
| `PodSandbox.ips` | agreed | agreed |
| `LinuxPodSandbox.cgroup_parent` | agreed | agreed |
| `LinuxPodSandbox.pod_overhead` | agreed | agreed |
| `LinuxPodSandbox.pod_resources` | agreed | agreed |
| `LinuxPodSandbox.cgroups_path` | agreed | agreed |
| `LinuxPodSandbox.namespaces` | agreed | agreed |
| `LinuxPodSandbox.resources` | agreed | agreed |
| `Container.pod_sandbox_id` | agreed | agreed |
| `Container.name` | agreed | agreed |
| `Container.labels` | agreed | agreed |
| `Container.annotations` | agreed | agreed |
| `Container.args` | agreed | agreed |
| `Container.env` | agreed | agreed |
| `Container.mounts` | agreed | agreed |
| `Container.image` [^Container.image] | agreed | finding |
| `Container.CDI_devices` [^Container.CDI_devices] | agreed | finding |
| `Container.user` | agreed | agreed |
| `Container.rlimits` | agreed | agreed |
| `Container.hooks` [^Container.hooks] | not measured | not measured |
| `Container.state` [^Container.state] | finding | finding |
| `Container.created_at` | agreed | agreed |
| `Container.started_at` | agreed | finding |
| `Container.pid` | agreed | agreed |
| `Container.finished_at` | agreed | agreed |
| `Container.exit_code` | agreed | agreed |
| `Container.status_reason` [^Container.status_reason] | finding | finding |
| `Container.status_message` [^Container.status_message] | not measured | not measured |
| `LinuxContainer.sysctl` | finding | finding |
| `LinuxContainer.seccomp_profile` [^LinuxContainer.seccomp_profile] | agreed | finding |
| `LinuxContainer.oom_score_adj` | agreed | agreed |
| `LinuxContainer.namespaces` | agreed | agreed |
| `LinuxContainer.devices` | agreed | agreed |
| `LinuxContainer.resources` | agreed | agreed |
| `LinuxContainer.cgroups_path` | agreed | agreed |
| `LinuxContainer.seccomp_policy` | agreed | finding |

### Findings in detail

Each reads as the hook it was taken at, then the reason the expectation
gave. A reason names both the value the plugin was shown and the value it
was compared against, except where there was no oracle to compare with.

- **`PodSandbox.pid`**
  - cri-o 1.37.3: RunPodSandbox: the plugin was shown nothing, and the protocol defines this field as populated by this hook
- **`Container.image`**
  - cri-o 1.37.3: CreateContainer: the plugin was shown nothing, but the container has 1; a field the runtime realized and did not report is an input the protocol defines and this runtime does not deliver; CreateContainer: the test set registry.k8s.io/e2e-test-images/busybox:1.36.1-1, the plugin was shown 
- **`Container.CDI_devices`**
  - cri-o 1.37.3: CreateContainer: the test set [nri-test.containerd.io/device=null], which the plugin was not shown; the plugin was shown []
- **`Container.state`**
  - containerd v2.4.2: RemoveContainer: the plugin was shown CONTAINER_UNKNOWN, wanted CONTAINER_STOPPED
  - cri-o 1.37.3: PostStartContainer: the plugin was shown CONTAINER_CREATED, wanted CONTAINER_RUNNING
- **`Container.started_at`**
  - cri-o 1.37.3: PostStartContainer: the plugin was shown nothing, and the protocol defines this field as populated by this hook
- **`Container.status_reason`**
  - containerd v2.4.2: RemoveContainer: the plugin was shown , the container has Error
  - cri-o 1.37.3: RemoveContainer: the plugin was shown , the container has Error
- **`LinuxContainer.sysctl`**
  - containerd v2.4.2: CreateContainer: the test set [net.ipv4.ip_forward="1" is absent]; the plugin was shown []
  - cri-o 1.37.3: CreateContainer: the test set [net.ipv4.ip_forward="1" is absent]; the plugin was shown []
- **`LinuxContainer.seccomp_profile`**
  - cri-o 1.37.3: CreateContainer: the test set UNCONFINED, the plugin was shown RUNTIME_DEFAULT
- **`LinuxContainer.seccomp_policy`**
  - cri-o 1.37.3: CreateContainer: the plugin was shown false, the container has true; CreateContainer: the plugin was shown nothing, but the container has 1; a field the runtime realized and did not report is an input the protocol defines and this runtime does not deliver

### Notes on individual fields

[^Container.image]: Covers `name`, `digest` and `config_digest` together, and a runtime may
    report none of them. CRI carries the image reference the container was
    created from, so an absent report is a gap between CRI and NRI rather
    than information the runtime does not have.

[^Container.CDI_devices]: A runtime can apply the CRI field and not report it: the device node is
    present inside the container while the plugin is told nothing about it. A
    plugin therefore cannot tell "this container has no CDI devices" from
    "this runtime does not report them", and will under-adjust rather than
    fail. This asymmetry between applying and reporting is what the input
    layer was built to detect.

[^Container.hooks]: Cannot be exercised on an ordinary node: CRI offers no way to ask for an
    OCI hook, and a runtime that adds none of its own leaves nothing to
    compare a report against. The subtest skips rather than counting a
    comparison that could not fail, so this field reads as unmeasured on any
    node without a runtime-added hook.

[^Container.state]: The protocol populates this at every hook, so the field table requires a
    specific value at each one rather than merely a non-empty one. A runtime
    can be right at one hook and wrong at another, which makes a finding here
    a question about what the protocol intends a hook to imply rather than a
    simple omission.

[^Container.status_reason]: CRI's own container status carries a reason for a container that exited
    non-zero. When this field is unreported the information exists one layer
    down and does not reach the plugin, so a plugin cannot distinguish a
    clean exit from a failed one.

[^Container.status_message]: Needs a runtime that fills in a CRI message for a container that exits
    non-zero. Without one there is nothing to compare a report against, so
    the field reads as unmeasured rather than as agreement.

[^LinuxContainer.seccomp_profile]: A version number does not predict this field. A runtime version that
    advertises the security-profile input capability can still misreport the
    profile, and the version tables that map a runtime version onto NRI
    capabilities have been wrong about it in both directions. Read the row,
    not a version table.

## What was not measured

A report that hides its gaps overstates itself.

These tests skipped, grouped by why. A skip in one run alone is a gap in
that runtime's results; the same skip in every run is a gap in the suite.

| Tests | Skipped in | Reason |
|---|---|---|
| 14 tests | containerd v2.4.2 (runc), cri-o 1.37.3 (runc) | driver "cri" lacks required capabilities: need InTreeAdjustments, have ContainerLifecycle,ObserveOpError,UpdateContainerResources,Exec,RawResources,ContainerMetadata,CDIDevices,Sysctls,SecurityContext,Devices,ObservedRuntimeState |
| `TestInputAgreement/should_report_the_OCI_hooks_matching_the_realized_spec` | containerd v2.4.2 (runc), cri-o 1.37.3 (runc) | this node's runtime adds no OCI hooks to a container, so a report of none cannot be distinguished from an omission |
| `TestSelfAuthoredFieldsAreNotMeasured` | containerd v2.4.2 (runc), cri-o 1.37.3 (runc) | only the mock driver authors these values; a real runtime produces them and they are measured against it |
| `TestPodResourceUpdateEvents/should_deliver_UpdatePodSandbox_and_PostUpdatePodSandbox` | containerd v2.4.2 (runc), cri-o 1.37.3 (runc) | pod resource updates are not verified in any runtime yet; see TODO in TestPodResourceUpdateEvents |
| `TestNRIVersionExchange/should_agree_with_the_version_inferred_from_the_runtime_version` | cri-o 1.37.3 (runc) | cannot infer an NRI version for cri-o 1.37.3: failed to find latest version for "1.37.3" |

A group of several tests names no test: they skipped for one reason, and
the reason is what a reader of this report needs.

The runtime handler's own version is not recorded, because CRI does not
report it. Two columns naming the same handler may have run different
builds of it. Neither is the build of the suite that measured a column,
so two columns may have been measured by different builds of it as well:
a column is re-measured on its own, and a difference between two columns
is evidence about the runtimes only as far as that.

## Failures outside the input surface

The behavioural conformance tests recorded no finding.

## Reading the counts

"Input fields measured" counts the fields a run's tests recorded coverage
for. It is not everything the run looked at, and it does not bound the row
below it: a finding can be recorded against a field no coverage call named.
Neither row is summed across a column's handlers, because a sum would grow
with the number of runs rather than with what the runtime does.

A column's failure-reporting mode decides how much a run could have found.
With `-nri-test.collect-failures` a test reports every expectation it
checked; without it a test stops at its first failure, so a column in that
mode may have more findings than it recorded.
