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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReportCollectsFindings(t *testing.T) {
	r := &Report{}
	require.False(t, r.HasFindings(), "a fresh report holds no findings")

	first := Finding{
		Test:    "TestOne",
		Feature: "annotation",
		Surface: "OCI spec",
		Err:     errors.New("first boom"),
	}
	second := Finding{
		Test:    "TestTwo",
		Feature: "cpuset",
		Surface: "/proc/self/status",
		Err:     errors.New("second boom"),
	}

	r.AddFinding(first)
	r.AddFinding(second)

	require.True(t, r.HasFindings(), "a report with findings says so")

	got := r.Findings()
	require.Len(t, got, 2)
	require.Equal(t, first, got[0], "findings come back in the order they arrived")
	require.Equal(t, second, got[1], "findings come back in the order they arrived")
}

func TestReportFindingsCannotBeMutatedThroughTheSlice(t *testing.T) {
	// Findings returns a clone: a caller that scribbles on what it got must not
	// be able to rewrite the record the summary is printed from.
	r := &Report{}
	r.AddFinding(Finding{Test: "TestOne", Err: errors.New("boom")})

	got := r.Findings()
	got[0].Test = "overwritten"

	require.Equal(t, "TestOne", r.Findings()[0].Test,
		"Findings must hand out a copy, not the report's own slice")
}

func TestReportPrintNamesEverything(t *testing.T) {
	r := &Report{}
	r.AddFinding(Finding{
		Test:    "TestAdjustAnnotation",
		Feature: "annotation",
		Surface: "OCI spec",
		Err:     errors.New("annotation nri-test.io/one not found"),
	})
	r.AddFinding(Finding{
		Test:    "TestAdjustCpuset",
		Feature: "cpuset",
		Surface: "/proc/self/status",
		Err:     errors.New("Cpus_allowed_list is 0-43, want 1\nparsed as 0..43"),
	})

	var b strings.Builder
	r.Print(&b, Header{})
	out := b.String()

	for _, want := range []string{
		"TestAdjustAnnotation",
		"annotation",
		"OCI spec",
		"annotation nri-test.io/one not found",
		"TestAdjustCpuset",
		"cpuset",
		"/proc/self/status",
		"Cpus_allowed_list is 0-43, want 1",
	} {
		require.Contains(t, out, want, "the summary must name every part of a finding")
	}

	// The count's own text, not just the digit: the enumeration already prints a
	// "2." of its own, so asserting on "2" alone would pass with the count line
	// deleted outright.
	require.Contains(t, out, "2 expectation failure(s) and 0 failed operation(s) collected",
		"the summary must count what it collected")

	// A multi-line expectation error stays visibly part of its finding rather
	// than running back to column zero, where it reads as a new section.
	require.Contains(t, out, "\n              parsed as 0..43\n",
		"an error's continuation lines must be indented under it")
}

func TestReportPrintSeparatesOperationsFromExpectations(t *testing.T) {
	// The two kinds are different news about a run and must not be mixed. An
	// unmet expectation is a statement about the runtime: the test ran, read a
	// surface, and the answer was wrong. A failed operation is a statement about
	// the run: the test never got far enough to read anything, so nothing about
	// the runtime's NRI behaviour was measured by it at all.
	r := &Report{}
	r.AddFinding(Finding{
		Kind:    KindExpectation,
		Test:    "TestAdjustAnnotation",
		Feature: "annotation",
		Surface: "OCI spec",
		Err:     errors.New("annotation nri-test.io/one not found"),
	})
	r.AddFinding(Finding{
		Kind:    KindOperation,
		Test:    "TestCreateContainer",
		Feature: "CreateContainer",
		Err:     errors.New("failed to create container ctr0: user specified image not specified"),
	})

	var b strings.Builder
	r.Print(&b, Header{})
	out := b.String()

	for _, want := range []string{
		"TestAdjustAnnotation",
		"annotation nri-test.io/one not found",
		"TestCreateContainer",
		"CreateContainer",
		"failed to create container ctr0: user specified image not specified",
	} {
		require.Contains(t, out, want, "the summary must name both kinds of failure")
	}

	// Distinguishable headings, and each finding under its own. Presence alone
	// would pass with both kinds listed in one undifferentiated section, which is
	// precisely what must not happen.
	expectations := strings.Index(out, "Expectation failures:")
	operations := strings.Index(out, "Failed operations")

	require.GreaterOrEqual(t, expectations, 0, "the summary must head its expectation failures")
	require.GreaterOrEqual(t, operations, 0, "the summary must head its failed operations")
	require.Less(t, expectations, operations, "the two sections must be separate")
	require.Less(t, strings.Index(out, "TestAdjustAnnotation"), operations,
		"an unmet expectation belongs under the expectation heading")
	require.Greater(t, strings.Index(out, "TestCreateContainer"), operations,
		"a failed driver call belongs under the operation heading")

	require.Contains(t, out, "1 expectation failure(s) and 1 failed operation(s) collected.",
		"the count line must name both counts")
}

func TestReportPrintWithOnlyOperationFailures(t *testing.T) {
	// This is the summary the first CRI-O sweep printed. 34 tests had just failed
	// on a driver call CRI-O refused; every one of those failures was fatal and
	// none of them was recorded anywhere, so the summary signed off with "No
	// expectation failures were collected" -- the run itself exited 1, since each
	// aborted test failed on its own. A suite that reports success while
	// asserting nothing is this project's primary anti-goal, and a summary that
	// says it is worse than no summary at all, because a reader believes it.
	//
	// Two lines of error, because a multi-line one is the common shape here
	// rather than an edge case: a refused CRI call arrives as a gRPC error
	// wrapping the runtime's own message, and nothing trims it to one line. The
	// rendering below is representative rather than a transcript of the sweep's.
	r := &Report{}
	r.AddFinding(Finding{
		Kind:    KindOperation,
		Test:    "TestCreateContainer",
		Feature: "CreateContainer",
		Err: errors.New("failed to create container ctr0: rpc error: code = Unknown\n" +
			"desc = user specified image not specified, cannot verify image signature"),
	})

	var b strings.Builder
	r.Print(&b, Header{})
	out := b.String()

	// The zero-case sentence, in the form every other test in this file pins it
	// by: it fires only when neither kind was recorded.
	require.NotContains(t, strings.ToLower(out), "no expectation failures",
		"a run whose driver calls failed must not sign off as having collected nothing")

	require.Contains(t, out, "TestCreateContainer",
		"the summary must name the test whose operation failed")

	// The operation block's own indent, measured rather than assumed: its error
	// label is two columns wider than a finding's, so the two indents are
	// different numbers and a continuation line indented to the finding's width
	// would run back under the label. Pinned exactly as the finding equivalent is.
	require.Contains(t,
		out,
		"\n                desc = user specified image not specified, cannot verify image signature\n",
		"a failed operation's error must indent its continuation lines under it")

	require.Contains(t, out, "0 expectation failure(s) and 1 failed operation(s) collected.",
		"the count line must say that one operation failed and that no expectation "+
			"was evaluated to failure, which are different facts")
}

func TestReportHasFindingsCountsOperations(t *testing.T) {
	// The summary and the exit code have to tell one story. Such a run already
	// exits non-zero, because each aborted test failed in its own right -- but
	// TestMain consults HasFindings, so a report that denied holding anything
	// would make the two disagree about the same run.
	r := &Report{}
	r.AddFinding(Finding{
		Kind:    KindOperation,
		Test:    "TestCreatePod",
		Feature: "CreatePod",
		Err:     errors.New("failed to create pod pod0: sandbox name is reserved"),
	})

	require.True(t, r.HasFindings(), "a failed driver call is something collected")
}

func TestReportPrintWithNothingCollected(t *testing.T) {
	var b strings.Builder
	(&Report{}).Print(&b, Header{})
	out := b.String()

	require.Contains(t, strings.ToLower(out), "no expectation failures",
		"an empty summary must say that nothing was collected")

	// Both kinds named, because the sentence now speaks for both. Pinned
	// separately from the clause above, and deliberately so: the old wording said
	// nothing about operations and still satisfies that assertion, so without
	// this one the honest half of the sentence would have no test behind it.
	require.Contains(t, strings.ToLower(out), "no failed operations",
		"an empty summary must say that no driver call failed either")

	// An empty summary says what this run checked and nothing more. The suite
	// exercises a fraction of NRI, so "nothing failed" is not a verdict on the
	// runtime, and the summary must not be quotable as one.
	require.NotContains(t, strings.ToLower(out), "conformant",
		"an empty summary must not read as a conformance claim")
}

// blindSentence is the clause the summary prints when the run failed and it
// cannot say why. Named once, so the two directions below cannot drift apart and
// pin different strings.
const blindSentence = "The run FAILED and yet nothing was collected"

func TestReportPrintAnnouncesWhenItCannotSeeTheFailure(t *testing.T) {
	// Nothing collected means two different things, and the summary is the only
	// thing a reader of a long run is guaranteed to see. Most of the suite asserts
	// through bare require and t.Errorf calls, which record nothing here, so a run
	// that failed only through those arrives with an empty report -- and an empty
	// report printed as a clean run is the 34-failure sign-off again, in a place no
	// single test of one assertion path could see.
	//
	// Both directions, because a one-sided test passes with the branch deleted:
	// without the clean case the sentence could be printed unconditionally, and
	// without the failing case it could never be printed at all.
	t.Run("a failing run with nothing collected says the summary is blind", func(t *testing.T) {
		var b strings.Builder
		(&Report{}).Print(&b, Header{Failed: true})
		out := b.String()

		require.Contains(t, out, blindSentence,
			"a failing run that collected nothing must say that it failed somewhere "+
				"the summary cannot see")

		// The reader is sent to the only account there is. A sentence that said the
		// summary was blind without saying where to look would leave them with the
		// fact and nothing to do about it.
		require.Contains(t, out, "test output above",
			"it must name the test output as where the failure is")

		// And not also the clean sentence: the two states must not both print, or
		// the summary contradicts itself about one run.
		require.NotContains(t, strings.ToLower(out), "no expectation failures",
			"a failing run must not also sign off as having collected nothing")
	})

	t.Run("a clean run does not say it", func(t *testing.T) {
		var b strings.Builder
		(&Report{}).Print(&b, Header{Failed: false})
		out := b.String()

		require.NotContains(t, out, blindSentence,
			"a run go test passed must not be reported as a failure the summary missed")
		require.Contains(t, strings.ToLower(out), "no expectation failures",
			"a clean run still gets the sentence saying nothing was collected")
	})

	t.Run("a failing run with findings counts them instead", func(t *testing.T) {
		// The blind sentence is for the case where the summary has nothing to show.
		// With a finding in hand it has, and printing both would tell the reader the
		// summary cannot see what it just printed.
		r := &Report{}
		r.AddFinding(Finding{Test: "TestFailed", Err: errors.New("boom")})

		var b strings.Builder
		r.Print(&b, Header{Failed: true})
		out := b.String()

		require.NotContains(t, out, blindSentence,
			"a summary that collected something is not blind to the failure")
		require.Contains(t, out, "1 expectation failure(s) and 0 failed operation(s) collected.")
	})
}

func TestReportPrintHeader(t *testing.T) {
	hdr := Header{
		Runtime:        "containerd",
		RuntimeVersion: "2.3.3",
		NRIVersion:     "0.9.0",
		RuntimeHandler: "runc",
		RunID:          "0123456789abcdef",
		Collecting:     true,
	}

	var b strings.Builder
	(&Report{}).Print(&b, hdr)
	out := b.String()

	// Label and value together, never each on its own: five independent
	// Contains assertions would all pass with the runtime version printed on the
	// NRI version's line and vice versa, which is the one mistake in a
	// column-aligned block that a reader cannot catch either.
	for _, want := range []string{
		"runtime:         containerd 2.3.3",
		"NRI version:     0.9.0",
		"runtime handler: runc",
		"run id:          0123456789abcdef",
	} {
		require.Contains(t, out, want, "the header must name what produced the result")
	}
}

func TestReportPrintHeaderNamesTheMode(t *testing.T) {
	// Which mode a run used decides what the absence of a finding means: in the
	// default mode the first failure ends its test, so later expectations in that
	// test were never evaluated. A summary that did not say so would be read as
	// if every expectation had run.
	modeLine := func(collecting bool) string {
		var b strings.Builder
		(&Report{}).Print(&b, Header{Collecting: collecting})

		for line := range strings.Lines(b.String()) {
			if strings.HasPrefix(line, "mode:") {
				return line
			}
		}

		return ""
	}

	collecting, failing := modeLine(true), modeLine(false)

	require.NotEmpty(t, collecting, "the header must carry a mode line")
	require.NotEmpty(t, failing, "the header must carry a mode line")
	require.NotEqual(t, collecting, failing, "the two modes must not read alike")
	require.Contains(t, collecting, "collect")
	require.Contains(t, failing, "first")
}

func TestReportPrintHeaderNamesADefaultedRuntimeHandler(t *testing.T) {
	// An empty handler field would leave the reader unable to tell a result
	// produced by the runtime's default handler from one produced by a handler
	// the header forgot to record.
	var b strings.Builder
	(&Report{}).Print(&b, Header{Runtime: "containerd"})

	require.Contains(t, b.String(), "default",
		"an unselected runtime handler must print as the runtime's default")
}

func TestReportIsConcurrencySafe(t *testing.T) {
	const goroutines = 50

	r := &Report{}

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)

		go func() {
			defer wg.Done()

			r.AddFinding(Finding{
				Test: fmt.Sprintf("TestConcurrent/%d", i),
				Err:  errors.New("boom"),
			})
		}()
	}

	wg.Wait()

	require.Len(t, r.Findings(), goroutines)
}

// fakeSkipT is the part of *testing.T that Skipped uses. It records the Skipf it
// receives, so a test can see that the skip happened as well as what the summary
// was told about it.
type fakeSkipT struct {
	name   string
	skipfs []string
}

func (f *fakeSkipT) Helper()      {}
func (f *fakeSkipT) Name() string { return f.name }

func (f *fakeSkipT) Skipf(format string, args ...any) {
	f.skipfs = append(f.skipfs, fmt.Sprintf(format, args...))
}

var _ skipT = &fakeSkipT{}

func TestSkippedRecordsAndSkips(t *testing.T) {
	r := useReport(t)

	ft := &fakeSkipT{name: "TestNeedsTwoCPUs"}
	Skipped(ft, "the node has %d CPU, this test needs %d", 1, 2)

	require.Equal(t, []Skip{{
		Test:   "TestNeedsTwoCPUs",
		Reason: "the node has 1 CPU, this test needs 2",
	}}, r.Skips(), "a skip must be recorded against the test that skipped")
	require.Equal(t, []string{"the node has 1 CPU, this test needs 2"}, ft.skipfs,
		"the test must still be skipped, once, with the same reason")
	require.False(t, r.HasFindings(), "a skip is not a finding")
}

func TestReportPrintNamesSkips(t *testing.T) {
	r := &Report{}
	r.AddFinding(Finding{
		Test:    "TestFailed",
		Feature: "cpuset",
		Surface: "/proc/self/status",
		Err:     errors.New("Cpus_allowed_list is 0-43, want 1"),
	})
	r.AddSkip(Skip{
		Test:   "TestUnmeasured",
		Reason: "the node has 1 CPU; run it on 2 or more",
	})

	var b strings.Builder
	r.Print(&b, Header{})
	out := b.String()

	require.Contains(t, out, "TestUnmeasured", "the summary must name the skipped test")
	require.Contains(t, out, "the node has 1 CPU; run it on 2 or more",
		"the summary must say why the test was skipped")

	// A heading of its own, pinned exactly as the operation heading is, and for the
	// same reason. The order below holds with the heading deleted outright -- and
	// then a reader sees the skips appended to the expectation-failures list in the
	// identical block shape, which defeats the whole point of recording them apart
	// from failures. The words matter too: a reader who takes a skip for a pass has
	// misread the summary, so the heading has to say that a skip is not one.
	heading := strings.Index(out, "Skipped (unverified in this run; a skip is not a pass):")
	require.GreaterOrEqual(t, heading, 0,
		"the summary must head its skips, and say that a skip is not a pass")

	// The order is the contract: findings, then skips, then the count.
	finding := strings.Index(out, "TestFailed")
	skip := strings.Index(out, "TestUnmeasured")
	count := strings.Index(out, "1 expectation failure(s) and 0 failed operation(s) collected.")

	require.GreaterOrEqual(t, finding, 0)
	require.GreaterOrEqual(t, skip, 0)
	require.GreaterOrEqual(t, count, 0)
	require.Less(t, finding, heading, "the skip heading must come after the findings")
	require.Less(t, heading, skip, "every skip must be under the skip heading")
	require.Less(t, skip, count, "the skip section must come before the count")
}

func TestReportPrintWithOnlySkips(t *testing.T) {
	// A run whose gates all skipped is the ordinary outcome on a host that cannot
	// measure something. It must print its skips, must still say that no failures
	// were collected, and must not be a failing run.
	r := &Report{}
	r.AddSkip(Skip{
		Test:   "TestUnmeasured",
		Reason: "the node has 1 CPU; run it on 2 or more",
	})

	require.False(t, r.HasFindings(),
		"a run that only skipped must not exit non-zero: skips are not findings")

	var b strings.Builder
	r.Print(&b, Header{})
	out := b.String()

	require.Contains(t, out, "TestUnmeasured")
	require.Contains(t, out, "the node has 1 CPU; run it on 2 or more")
	require.Contains(t, strings.ToLower(out), "no expectation failures",
		"a summary with skips and no findings must still say nothing was collected")
}
