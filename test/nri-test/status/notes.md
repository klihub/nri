Notes about individual input fields, written by hand.

A note is keyed by the field's qualified name and is rendered beside that
field's row, so a note cannot outlive the row it annotates and a change in
a field's status appears next to the note that interprets it. A heading
naming a field the suite does not have fails the render.

A note says what the field is and what a finding on it would mean. It does
not say what any runtime currently does: the table says that, a run
refreshes the table, and nothing refreshes a note. A note that names a
runtime's result will contradict the row beside it the first time that
result changes.

## Container.CDI_devices

A runtime can apply the CRI field and not report it: the device node is
present inside the container while the plugin is told nothing about it. A
plugin therefore cannot tell "this container has no CDI devices" from
"this runtime does not report them", and will under-adjust rather than
fail. This asymmetry between applying and reporting is what the input
layer was built to detect.

## Container.state

The protocol populates this at every hook, so the field table requires a
specific value at each one rather than merely a non-empty one. A runtime
can be right at one hook and wrong at another, which makes a finding here
a question about what the protocol intends a hook to imply rather than a
simple omission.

## Container.image

Covers `name`, `digest` and `config_digest` together, and a runtime may
report none of them. CRI carries the image reference the container was
created from, so an absent report is a gap between CRI and NRI rather
than information the runtime does not have.

## Container.status_reason

CRI's own container status carries a reason for a container that exited
non-zero. When this field is unreported the information exists one layer
down and does not reach the plugin, so a plugin cannot distinguish a
clean exit from a failed one.

## LinuxContainer.seccomp_profile

A version number does not predict this field. A runtime version that
advertises the security-profile input capability can still misreport the
profile, and the version tables that map a runtime version onto NRI
capabilities have been wrong about it in both directions. Read the row,
not a version table.

## Container.hooks

Cannot be exercised on an ordinary node: CRI offers no way to ask for an
OCI hook, and a runtime that adds none of its own leaves nothing to
compare a report against. The subtest skips rather than counting a
comparison that could not fail, so this field reads as unmeasured on any
node without a runtime-added hook.

## Container.status_message

Needs a runtime that fills in a CRI message for a container that exits
non-zero. Without one there is nothing to compare a report against, so
the field reads as unmeasured rather than as agreement.
