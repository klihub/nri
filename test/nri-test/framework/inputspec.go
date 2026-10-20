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

// The NRI input surface: what a plugin is told about a pod and a container, as
// opposed to what it may ask the runtime to change.
//
// This file is one table, and the table is the specification. Every field of
// the four input messages is either in InputFields, meaning a test verifies it,
// or in inputFieldsOutOfScope with a reason, meaning we have decided not to.
// A field in neither fails the coverage tests, so the protocol cannot grow a
// field that silently goes unlooked-at.
//
// It is also what the run summary's coverage section is computed from, which is
// why it is data rather than a set of test names: a hand-maintained count of
// what the suite covers went stale three times before this existed.

// InputTier says how a field is checked. There is no valid zero value: a row
// that did not say would generate a test that does not know what to compare.
type InputTier int

const (
	// TierFidelity is a field the test sets over CRI and reads back off the
	// hook request. It can only pass for the right reason, provided the value
	// is one an unadjusted container would not have.
	TierFidelity InputTier = iota + 1
	// TierAgreement is a field the test cannot set, checked against what the
	// runtime realized -- the OCI spec, or CRI's own status. Subset, plus a
	// non-vacuity guard: see ExpectPopulated for why the guard is not optional.
	TierAgreement
	// TierState is Container.state alone, which is populated at every hook with
	// a different correct value at each, and so is a ladder rather than a
	// presence check.
	TierState
)

// InputField is one field of the input surface, and how and when to check it.
type InputField struct {
	// Message is the input message the field belongs to: PodSandbox,
	// LinuxPodSandbox, Container or LinuxContainer.
	Message string
	// Name is the field's name as the protocol declares it, so that the
	// coverage tests can compare this table against the descriptor rather than
	// against a transliteration of it. Hence CDI_devices and not CdiDevices.
	Name string
	// Tier says how the field is checked.
	Tier InputTier
	// RequiredAt is the earliest hook by which the runtime must have populated
	// the field. Before it, absence is not a finding; at or after it, absence
	// is.
	//
	// One declaration per field, in one place, because the strictness is a
	// first guess: most fields are expected already at CreateContainer, and
	// whether a runtime that fills one in only by PostStartContainer should
	// fail is a question the first runs against both runtimes will answer.
	// Moving a row must cost one line.
	RequiredAt EventType
}

// InputFields is every field a test verifies. See the file comment.
var InputFields = []InputField{
	// PodSandbox. The pod hooks all carry the same message; RunPodSandbox is
	// the first, and fires at the very end of the runtime's own
	// RunPodSandbox -- after network setup -- so the addresses are expected.
	{"PodSandbox", "name", TierFidelity, RunPodSandbox},
	{"PodSandbox", "uid", TierFidelity, RunPodSandbox},
	{"PodSandbox", "labels", TierFidelity, RunPodSandbox},
	{"PodSandbox", "annotations", TierFidelity, RunPodSandbox},
	{"PodSandbox", "runtime_handler", TierFidelity, RunPodSandbox},
	{"PodSandbox", "pid", TierAgreement, RunPodSandbox},
	{"PodSandbox", "ips", TierAgreement, RunPodSandbox},

	// LinuxPodSandbox. CRI carries overhead and resources on the sandbox
	// config, so both are the test's to choose.
	{"LinuxPodSandbox", "cgroup_parent", TierFidelity, RunPodSandbox},
	{"LinuxPodSandbox", "pod_overhead", TierFidelity, RunPodSandbox},
	{"LinuxPodSandbox", "pod_resources", TierFidelity, RunPodSandbox},
	{"LinuxPodSandbox", "cgroups_path", TierAgreement, RunPodSandbox},
	{"LinuxPodSandbox", "namespaces", TierAgreement, RunPodSandbox},
	{"LinuxPodSandbox", "resources", TierAgreement, RunPodSandbox},

	// Container.
	{"Container", "pod_sandbox_id", TierFidelity, CreateContainer},
	{"Container", "name", TierFidelity, CreateContainer},
	{"Container", "labels", TierFidelity, CreateContainer},
	{"Container", "annotations", TierFidelity, CreateContainer},
	{"Container", "args", TierFidelity, CreateContainer},
	{"Container", "env", TierFidelity, CreateContainer},
	{"Container", "mounts", TierFidelity, CreateContainer},
	{"Container", "image", TierFidelity, CreateContainer},
	{"Container", "CDI_devices", TierFidelity, CreateContainer},
	{"Container", "user", TierFidelity, CreateContainer},
	// Rlimits have no CRI expression at all, so they can only be compared
	// against what the runtime wrote into the spec.
	{"Container", "rlimits", TierAgreement, CreateContainer},
	// OCI hooks are the runtime's to add; CRI cannot ask for one.
	{"Container", "hooks", TierAgreement, CreateContainer},
	{"Container", "state", TierState, CreateContainer},
	// The timestamps are checked for being populated, not against CRI's own
	// reading of the same clock: the two are taken at different moments through
	// different calls, so an equality check would compare one reading against
	// another. A non-zero but wildly wrong timestamp therefore passes.
	{"Container", "created_at", TierAgreement, PostCreateContainer},
	{"Container", "started_at", TierAgreement, PostStartContainer},
	{"Container", "pid", TierAgreement, PostStartContainer},
	// The exit fields are required at RemoveContainer, the last hook and the
	// only one that necessarily follows the exit. StopContainer is a request
	// hook: it fires before the container is stopped.
	{"Container", "finished_at", TierAgreement, RemoveContainer},
	{"Container", "exit_code", TierAgreement, RemoveContainer},
	{"Container", "status_reason", TierAgreement, RemoveContainer},
	{"Container", "status_message", TierAgreement, RemoveContainer},

	// LinuxContainer.
	{"LinuxContainer", "sysctl", TierFidelity, CreateContainer},
	{"LinuxContainer", "seccomp_profile", TierFidelity, CreateContainer},
	{"LinuxContainer", "oom_score_adj", TierFidelity, CreateContainer},
	{"LinuxContainer", "namespaces", TierAgreement, CreateContainer},
	{"LinuxContainer", "devices", TierAgreement, CreateContainer},
	{"LinuxContainer", "resources", TierAgreement, CreateContainer},
	{"LinuxContainer", "cgroups_path", TierAgreement, CreateContainer},
	{"LinuxContainer", "seccomp_policy", TierAgreement, CreateContainer},
}

// inputFieldsOutOfScope is every input field no test verifies, and why.
//
// The reason is data rather than a comment on purpose. This map is where the
// suite admits what it does not check, and a reason in a comment can be
// deleted without the compiler or a test noticing -- which is how an
// unexamined exclusion becomes an unexamined claim. The coverage tests require
// every entry to carry one.
var inputFieldsOutOfScope = map[string]map[string]string{
	"PodSandbox": {
		"id": "the key every capture is looked up by, so a runtime reporting it " +
			"wrongly presents as a hook that never fired rather than as a wrong " +
			"id -- the circularity sync_test.go documents for the scope filter",
		"namespace": "the scope filter matches on it, so a wrong value means the " +
			"pod is filtered out and reported as one the runtime never mentioned",
		"linux": "a message, verified through its own fields rather than as a whole",
	},
	"LinuxPodSandbox": {},
	"Container": {
		"id": "the key every capture is looked up by; see PodSandbox.id",
		"linux": "a message, verified through its own fields rather than as a " +
			"whole",
	},
	"LinuxContainer": {
		"io_priority": "the node-dependent tier, deferred: needs a kernel and a " +
			"runtime that apply it, so an absent value is not yet " +
			"distinguishable from an unsupported one",
		"scheduler":   "the node-dependent tier, deferred; see io_priority",
		"net_devices": "the node-dependent tier, deferred; see io_priority",
		"rdt":         "the node-dependent tier, deferred: needs hardware and a resctrl mount",
	},
}

// InputFieldsAt returns the fields whose required hook is exactly hook.
//
// Not the cumulative set: a caller asking what must be populated by
// PostStartContainer gets that hook's own rows, not those of every hook before
// it. The coverage report and the tests both want the per-hook grouping.
func InputFieldsAt(hook EventType) []InputField {
	var at []InputField

	for _, f := range InputFields {
		if f.RequiredAt == hook {
			at = append(at, f)
		}
	}

	return at
}
