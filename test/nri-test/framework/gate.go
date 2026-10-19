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
	"fmt"
	"maps"
	"slices"
	"testing"

	"golang.org/x/mod/semver"
)

// skipT is the part of *testing.T that Skipped uses.
type skipT interface {
	Helper()
	Name() string
	Skipf(string, ...any)
}

// gateT is the part of *testing.T the gates below use: everything Skipped needs,
// since every gate can skip, plus Fatal for the ones that cannot evaluate their
// own condition.
//
// Stated by embedding rather than by repeating skipT's three members, so that
// the relationship is the declaration: a gate can do whatever a skip can, and
// one more thing.
//
// It is an interface only so that their failure paths can be unit tested:
// Fatal does not return on a real *testing.T, so a test cannot survive calling
// it, and a fake is the only way to observe that it was called.
type gateT interface {
	skipT

	Fatal(args ...any)
}

// Skipped skips the test and records the skip with the summary.
//
// Every skip in the suite goes through here. The summary cannot enumerate skips
// after the fact, since testing offers no way to ask which tests skipped, so a
// skip that does not come through here is invisible to it. The reason is recorded
// exactly as formatted, and is reported through Skipf unchanged.
func Skipped(t skipT, format string, args ...any) {
	t.Helper()

	reason := fmt.Sprintf(format, args...)
	TestReport.AddSkip(Skip{Test: t.Name(), Reason: reason})

	// The reason goes through %s, not as the format itself: a reason may contain
	// a percent sign, and it must reach the test output exactly as it was written.
	t.Skipf("%s", reason)
}

// RequireCaps skips the test unless the active driver has all the given
// capabilities. Pass the test's *testing.T; gateT exists for the framework's own
// unit tests.
func RequireCaps(t gateT, caps Capability) {
	t.Helper()

	// Same guard as NewSuite: without it a run that never called Setup panics
	// inside a gate, which reads as a framework bug rather than as the missing
	// TestMain wiring it is.
	if driver == nil {
		t.Fatal("framework.Setup has not been called")

		// Unreachable with a real *testing.T, whose Fatal does not return. It is
		// here so that a fake T cannot fall through into the nil dereference this
		// guard exists to prevent.
		return
	}

	have := driver.Capabilities()
	if !have.Has(caps) {
		Skipped(t, "driver %q lacks required capabilities: need %s, have %s",
			driver.Name(), caps, have)
	}
}

// RequireNRIVersion skips the test unless the runtime's NRI version is at least
// the given one. Pass a semver string such as "v0.10.0".
func RequireNRIVersion(t *testing.T, min string) {
	t.Helper()

	have := runtimeNRIVersion
	if have == "" {
		Skipped(t, "runtime NRI version unknown; %s or later required", min)
	}
	if !semver.IsValid(have) {
		Skipped(t, "runtime NRI version %q is not valid semver; %s or later required",
			have, min)
	}
	if semver.Compare(have, min) < 0 {
		Skipped(t, "runtime NRI version is %s; %s or later required", have, min)
	}
}

// SkipUnlessConfigSuite skips the test unless the named configuration-dependent
// suite was requested with -nri-test.config.
func SkipUnlessConfigSuite(t *testing.T, name string) {
	t.Helper()

	if TestConfig.ConfigSuite != name {
		Skipped(t, "requires -nri-test.config=%s", name)
	}
}

// SkipUnlessRuntimeHandler skips the test unless the node has the named
// low-level runtime installed.
//
// It is the gate a test specific to one low-level runtime needs. Without it, such
// a test run on a node that does not have the runtime fails at CreatePod -- the
// runtime refuses a handler it does not know -- and reads as a conformance
// failure of whatever the test was about. What is true is weaker and has to be
// said as such: the property went unverified here.
//
// It gates on what discovery found, not on what -nri-test.runtime-handler
// selected: the question is whether this node can run the test at all. A node
// that advertises no handlers skips too, since nothing there can confirm the
// runtime is present.
//
// The contract is two-step, and a caller that reads it as one step will measure
// the wrong thing. Passing this gate means only that the node HAS name. It does
// not make this test's workloads run under name: Suite.PodSpec stamps the handler
// the run selected with -nri-test.runtime-handler, so a test that must exercise
// name has to set PodSpec.RuntimeHandler = name on each spec it creates as well.
// Gate, then stamp. A test that gates and does not stamp runs under whatever the
// run selected while reading as though it had pinned the handler.
func SkipUnlessRuntimeHandler(t gateT, name string) {
	t.Helper()

	// Through the accessor, so the gate and anything else reading the discovered
	// set cannot come to disagree about the node.
	if slices.Contains(RuntimeHandlers(), name) {
		return
	}

	// Through Skipped, never t.Skipf: a skip the run's summary does not name is a
	// skip its reader takes for a pass.
	Skipped(t, "the runtime handler %q is not installed on this node, which reports "+
		"%s; what this test would verify under %s is unmeasured here. Run it on a node "+
		"that has %s installed to measure it",
		name, installedHandlers(), name, name)
}

// gateStatusReader is the part of *Suite the allowed-list gates below use.
//
// It is an interface for the same reason gateT is one: it is the seam that lets
// the counting below be unit tested without a runtime, a driver or a container.
// *Suite satisfies it, so a test passes its Suite and never names this type.
type gateStatusReader interface {
	ProcSelfStatus(ctr *Container) map[string]string
}

// What an allowed list absent from a /proc/self/status that was otherwise read
// correctly means, which differs between the two lines and is not a matter of
// taste. Both are printed from fs/proc/array.c's proc_pid_status:
//
//   - Cpus_allowed_list is printed for every task unconditionally, so its absence
//     cannot be a property of the host. Something ate the line, and a gate that
//     skipped on it would hide a broken read behind a message about the node's
//     hardware.
//   - Mems_allowed_list comes from cpuset_task_status_allowed, which is a no-op
//     without CONFIG_CPUSETS. Its absence is a genuine environment fact -- a
//     kernel without cpusets cannot confine a container to a NUMA node, and
//     nothing about NRI is being measured either way -- so it is a skip, and the
//     message says what went unmeasured.
const (
	absentIsSkip    = true
	absentIsFailure = false
)

// SkipUnlessCPUs skips the test unless the kernel inside ctr allows its processes
// at least min CPUs.
//
// This is the guard against a cpuset expectation that cannot fail. A container
// with no cpuset adjustment is allowed every online CPU, so on a one-CPU node its
// Cpus_allowed_list is already exactly "0" -- and an ExpectCpuset(ctr, "0") there
// passes whether or not the adjustment took effect, whether or not the plugin was
// invoked, and whether or not the runtime implements NRI at all. A single-CPU CI
// VM is an entirely ordinary place to run this suite, so that is not a corner.
//
// Pass a container created BEFORE this case's plugins were started: what is being
// counted is the node's unadjusted view of a container, and an adjusted container
// reports the adjustment rather than what was available to adjust.
func SkipUnlessCPUs(t gateT, s gateStatusReader, ctr *Container, min int) {
	t.Helper()

	skipUnlessAllowedListSize(t, s, ctr, "Cpus_allowed_list", "CPUs", min, absentIsFailure)
}

// SkipUnlessNUMANodes skips the test unless the kernel inside ctr allows its
// processes to allocate from at least min NUMA nodes.
//
// The sibling of SkipUnlessCPUs over Mems_allowed_list, and needed for the same
// reason: on a single-node host Mems_allowed_list reads "0" no matter what was
// requested, so ExpectMems(ctr, "0") there cannot fail. Single-node is the
// ordinary configuration rather than the exception, which is precisely why the
// gate matters -- without it a NUMA test would pass on most hosts while measuring
// nothing, and be read as proof that NUMA adjustment works.
//
// The count comes from inside the container, from the same /proc/self/status line
// the expectation reads, for the reason ExpectCpuset documents: the host's view is
// not the container's, and a gate that consulted the host would admit a test the
// container cannot satisfy -- a node with eight NUMA nodes can still hand a
// container a single-node cpuset by default.
//
// This is deliberately NOT inside ExpectMems. It would break the pure-error
// contract that makes the expectations unit-testable without a runtime, and it
// would silently turn a genuine single-node *failure* on a multi-node host into a
// skip -- a container wrongly confined to one node is a real conformance bug, and
// the one ExpectMems exists to catch. A skip is the test's decision, made before
// it asserts, never the assertion's.
//
// Pass a container created before this case's plugins were started; see
// SkipUnlessCPUs.
func SkipUnlessNUMANodes(t gateT, s gateStatusReader, ctr *Container, min int) {
	t.Helper()

	skipUnlessAllowedListSize(t, s, ctr, "Mems_allowed_list", "NUMA nodes", min, absentIsSkip)
}

// skipUnlessAllowedListSize skips the test unless one of the container's
// /proc/self/status allowed lists names at least min members.
//
// SkipUnlessCPUs and SkipUnlessNUMANodes are wrappers over this and share its
// body deliberately, exactly as ExpectCpuset and ExpectMems share
// expectAllowedList: two copies of this counting could drift apart, and one of
// them would then admit a vacuous test that no test of the other could see. The
// list itself is parsed with expect.go's parseAllowedList, for the same reason --
// a second parser of the kernel's syntax is a second thing to drift.
//
// Four outcomes, and which is which is load-bearing:
//
//   - min below 2 is a failure, not a skip. A running container is allowed at
//     least one CPU and one node, so such a gate can never skip anything, and a
//     gate that cannot skip is indistinguishable from the missing gate this
//     exists to be.
//   - a status that was not read at all, or a list that cannot be parsed, is a
//     failure. Skipping there would be the worst outcome available: a broken exec
//     channel or an unparsable line would silently skip every gated test, and a
//     run of green skips would be read as a run that measured something.
//   - a key absent from a status that WAS read is whichever of the two the caller
//     says, because the two lines differ in whether the kernel prints them
//     conditionally. See absentIsSkip above; this is the only outcome that is not
//     the same for both gates.
//   - a list that was read and counted, and is shorter than min, is the skip this
//     function exists for. Its message says what went unmeasured, because that is
//     what a reader needs: not "skipped" but "NUMA placement is unverified on this
//     host, here is the list that decided it, and here is the node it would take
//     to measure it".
func skipUnlessAllowedListSize(t gateT, s gateStatusReader, ctr *Container,
	key, subject string, min int, absentMeansSkip bool) {
	t.Helper()

	if min < 2 {
		t.Fatal(fmt.Sprintf("a gate requiring fewer than two %s cannot skip anything, "+
			"since a running container is allowed at least one, and a vacuity gate "+
			"that never fires is the same as no gate at all; the minimum given was %d",
			subject, min))

		// Unreachable with a real *testing.T, whose Fatal does not return, and
		// present for the reason RequireCaps's is: a fake T must not fall through
		// into the check this guard replaces.
		return
	}

	status := s.ProcSelfStatus(ctr)

	// A positive control on the read, asserted before anything is counted out of
	// it. Without it an empty map -- an exec that returned nothing, a parser that
	// dropped every line -- presents as a container allowed no CPUs at all, and
	// every gated test skips with a message blaming the host's hardware for a
	// broken observation channel.
	if _, ok := status["Name"]; !ok {
		t.Fatal(fmt.Sprintf("the container's /proc/self/status carries no Name field, "+
			"so it was not read; %s cannot be counted from %v", subject, status))

		return
	}

	raw, ok := status[key]
	if !ok {
		if !absentMeansSkip {
			t.Fatal(fmt.Sprintf("the container's /proc/self/status was read but reports "+
				"no %s, which the kernel prints unconditionally; its %s cannot be "+
				"counted, and this is a broken read rather than anything about the node. "+
				"The status carries %v", key, subject, slices.Sorted(maps.Keys(status))))

			return
		}

		Skipped(t, "the container's kernel reports no %s, so its %s cannot be counted; "+
			"this test needs at least %d of them to be able to fail, and what it "+
			"would have verified is unmeasured here", key, subject, min)

		return
	}

	set, err := parseAllowedList(raw)
	if err != nil {
		t.Fatal(fmt.Sprintf("cannot read %s %q from the container's /proc/self/status, "+
			"so its %s cannot be counted: %v", key, raw, subject, err))

		return
	}

	if len(set) < min {
		Skipped(t, "the container's %s is %q, fewer than the %d %s this test needs to be "+
			"able to fail: with fewer, the value it asks for is already what an "+
			"unadjusted container gets, so the test would pass without verifying "+
			"anything. What it would have verified is unmeasured on this host; run it "+
			"on a node with %d or more %s to measure it.",
			key, raw, min, subject, min, subject)
	}
}
