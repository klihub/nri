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
	"io"
	"slices"
	"strings"
	"sync"
)

// Kind says what sort of failure a finding records, because the two are
// different news about a run and a reader must not have to guess which one is
// in front of them.
//
// An unmet expectation is a statement about the runtime: the test ran, read a
// surface, and the answer was not what NRI promises. A failed operation is a
// statement about the run: a driver call the test needed was refused, the test
// was ended there, and nothing about the runtime's NRI behaviour was measured
// by it at all. Counting the second as the first would overstate what the suite
// found; counting it as nothing -- which is what happened, see fatalf -- lets a
// run full of them be summarized as clean. The run itself failed, there and in
// every case like it: each aborted test fails on its own, and TestMain only
// ever raises the exit code. It was the summary that signed off, which is the
// thing a reader quotes.
type Kind int

const (
	// KindExpectation is an expectation that was not met. It is the zero value
	// deliberately: a finding was only ever an unmet expectation before
	// operations were recorded too, so a Finding built without a Kind is one.
	KindExpectation Kind = iota
	// KindOperation is a driver call that failed, which ended its test.
	KindOperation
)

// String names a Kind, so that a mismatch in a test's own output reads as
// "expectation" and "operation" rather than as two integers.
func (k Kind) String() string {
	switch k {
	case KindExpectation:
		return "expectation"
	case KindOperation:
		return "operation"
	}

	return fmt.Sprintf("Kind(%d)", int(k))
}

// Finding is one failure a run collected: an expectation that was not met, or a
// driver call that failed.
//
// Test, Feature and Surface are what make a finding readable on its own, which
// is the whole point of collecting it: the summary is read long after the test
// output it came from scrolled past. Test says which case asked, Feature says
// what was asked for, and Surface says where the answer was read -- the OCI
// spec, the container's own /proc, or both -- because "the mount is missing" and
// "the mount is in the spec but not in the container's mount table" are
// different findings about a runtime, and only Surface separates them.
type Finding struct {
	// Kind is which sort of failure this is. The zero value is KindExpectation.
	Kind Kind
	// Test is the name of the test case that recorded the finding.
	Test string
	// Feature is what was being verified, e.g. "annotation" or "cpuset". For a
	// KindOperation finding it is the operation that failed, e.g. "CreatePod":
	// there is no feature, since the test never got to verify one.
	Feature string
	// Surface is where the verification read its answer from. Empty for a
	// KindOperation finding, which read nothing.
	Surface string
	// Err is the expectation's own error, verbatim.
	Err error
}

// Skip is one test that skipped, and why.
//
// A skip is not a finding and does not fail a run. It says that a property was
// not verified here, which is the opposite of a pass: a reader of the summary has
// to be able to tell the two apart, so every skip is recorded with the reason the
// test gave, which says what went unmeasured and what it would take to measure it.
type Skip struct {
	// Test is the name of the test that skipped.
	Test string
	// Reason is the formatted message the test skipped with.
	Reason string
}

// Coverage is one input field a run verified.
//
// Recorded by the test that checked it, so the summary reports what a run
// actually did rather than what the suite is capable of. The hand-counted
// coverage claims this replaces went stale three separate times.
type Coverage struct {
	// Field is the field's qualified name, as InputFields names it:
	// "Container.CDI_devices".
	Field string
	// Hook is the callback the field was read from.
	Hook EventType
}

// Header describes the run a summary belongs to.
//
// A summary that did not name these is not quotable: the same finding means
// different things against a different runtime, a different NRI version or a
// different runtime handler, and a run id is what ties it back to the output it
// was collected from.
type Header struct {
	// Runtime is the runtime's name, as reported over CRI.
	Runtime string
	// RuntimeVersion is the runtime's version, as reported over CRI.
	RuntimeVersion string
	// NRIVersion is the runtime's NRI version, advertised or inferred.
	NRIVersion string
	// RuntimeHandler is the runtime handler the run's workloads were created
	// with, or "" when none was selected and the runtime's default applied.
	RuntimeHandler string
	// RunID scopes everything the run created.
	RunID string
	// Collecting says whether expectation failures were collected or whether
	// the run stopped each test at its first one.
	Collecting bool
	// NoRuntime says there was no runtime under test: the run measured the
	// NRI side of the protocol against a driver that plays the runtime
	// itself. A summary of such a run says so twice, because a reader who
	// quotes it as a runtime result has been misled by it.
	NoRuntime bool
	// Failed says whether the run failed, as go test's own verdict reported it.
	//
	// A Report cannot know this, and that is the point. Most of the suite's
	// assertions are bare require and t.Errorf calls that record nothing here, so
	// a run that failed only through those leaves the report empty -- and an empty
	// report prints as a clean run, which is the one thing this summary must never
	// say about a failing one. With this, the count section can tell a clean run
	// from a run it is blind to.
	Failed bool
}

// Report is what a run collected. It is safe for concurrent use.
//
// The mutex is not there because the suite runs tests in parallel -- it does
// not -- but because nothing in a Report's contract says a finding may only be
// recorded from the goroutine running the test, and an expectation verified
// from a plugin callback would be recorded from the plugin's. A mutex costs
// nothing on a path that runs a few hundred times per run; the race it rules
// out would be reported as a flake in whatever test happened to be running.
type Report struct {
	mu       sync.Mutex
	findings []Finding
	skips    []Skip
	coverage []Coverage
}

// TestReport is what the suite collects into, and what TestMain's exit code
// consults. A run with findings must not exit 0.
var TestReport = &Report{}

// AddFinding records a failure: an expectation that was not met, or a driver
// call that failed.
//
// One slice holds both kinds rather than one slice each, and Kind separates
// them only where they are printed. HasFindings is why: a run with either kind
// must exit non-zero, and two slices would let a kind be added without being
// counted -- which is the whole of the defect this Kind exists because of, in a
// new place.
func (r *Report) AddFinding(f Finding) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.findings = append(r.findings, f)
}

// AddSkip records a test that skipped, and why.
func (r *Report) AddSkip(s Skip) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.skips = append(r.skips, s)
}

// Skips returns what was recorded, in the order it arrived. Like Findings, it
// returns a clone.
func (r *Report) Skips() []Skip {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.skips)
}

// AddCoverage records that a run verified an input field.
//
// Called by the test that read the field, after its assertions, so that what is
// recorded is a field this run measured. A field recorded before its
// assertions would be counted even where the test went on to be skipped.
func (r *Report) AddCoverage(c Coverage) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.coverage = append(r.coverage, c)
}

// CoverageOf returns the input fields the run verified, in arrival order. A
// clone, for the reason Findings returns one.
func (r *Report) CoverageOf() []Coverage {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.coverage)
}

// Findings returns what was collected, in the order it arrived.
//
// A clone, so that a caller cannot rewrite the record the summary is printed
// from -- including by holding on to the slice and appending to it, which would
// otherwise share the report's backing array.
func (r *Report) Findings() []Finding {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.findings)
}

// HasFindings reports whether anything was collected, of either kind. TestMain
// turns a true here into a non-zero exit code.
//
// Both kinds, because a run whose driver calls were refused is a failing run:
// its aborted tests already fail it, and a report that denied holding anything
// would make the summary and the exit code disagree about one run.
//
// Skips are deliberately not considered. A run whose gates all skipped is the
// ordinary outcome on a host that cannot measure something, and it must not be
// read as a runtime failure.
func (r *Report) HasFindings() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.findings) > 0
}

// Print writes the run's summary: the header, then every unmet expectation,
// then every failed operation, then every skip, then a one-line verdict on what
// was collected.
//
// It is the only thing a run's reader is guaranteed to see, since the output of
// a long run scrolls past, so everything needed to act on a finding is repeated
// here rather than referred to.
func (r *Report) Print(w io.Writer, hdr Header) {
	findings := r.Findings()
	expectations := findingsOfKind(findings, KindExpectation)
	operations := findingsOfKind(findings, KindOperation)

	var b strings.Builder

	writeSummaryHeader(&b, hdr)
	writeFindings(&b, expectations)
	writeOperationFailures(&b, operations)
	writeSkips(&b, r.Skips())
	writeCoverage(&b, r.CoverageOf(), InputFields)
	writeSummaryCount(&b, len(expectations), len(operations), hdr.Failed, hdr.NoRuntime)

	fmt.Fprint(w, b.String())
}

// findingsOfKind returns the findings of one kind, in the order they arrived.
func findingsOfKind(findings []Finding, kind Kind) []Finding {
	var of []Finding

	for _, f := range findings {
		if f.Kind == kind {
			of = append(of, f)
		}
	}

	return of
}

// writeSummaryHeader writes what produced the result and how.
func writeSummaryHeader(b *strings.Builder, hdr Header) {
	b.WriteString("\n=== nri-test summary ===\n")

	runtime := strings.TrimSpace(hdr.Runtime + " " + hdr.RuntimeVersion)
	if runtime == "" {
		runtime = "<unknown>"
	}

	// A handler that was never selected prints as the runtime's default rather
	// than as an empty field: a reader must not have to guess whether the run
	// used some other handler and the header failed to record it.
	handler := hdr.RuntimeHandler
	if handler == "" {
		handler = "<runtime default>"
	}

	mode := "stop at the first expectation failure (default)"
	if hdr.Collecting {
		mode = "collect expectation failures (-nri-test.collect-failures)"
	}

	if hdr.NoRuntime {
		runtime += " -- no runtime under test"
	}

	fmt.Fprintf(b, "runtime:         %s\n", runtime)
	fmt.Fprintf(b, "NRI version:     %s\n", orUnknown(hdr.NRIVersion))
	fmt.Fprintf(b, "runtime handler: %s\n", handler)
	fmt.Fprintf(b, "run id:          %s\n", orUnknown(hdr.RunID))
	fmt.Fprintf(b, "mode:            %s\n", mode)
}

// writeFindings writes one block per finding, and nothing at all when there
// are none: the count line below says that case better than an empty section.
func writeFindings(b *strings.Builder, findings []Finding) {
	if len(findings) == 0 {
		return
	}

	b.WriteString("\nExpectation failures:\n")

	for i, f := range findings {
		fmt.Fprintf(b, "\n  %d. %s\n", i+1, orUnknown(f.Test))
		fmt.Fprintf(b, "     feature: %s\n", orUnknown(f.Feature))
		fmt.Fprintf(b, "     surface: %s\n", orUnknown(f.Surface))

		// Indent the error's continuation lines to the same column, so a
		// multi-line expectation error stays visibly part of its finding.
		fmt.Fprintf(b, "     error:   %s\n", indentContinuation(errText(f.Err), "              "))
	}
}

// writeOperationFailures writes one block per failed driver call, and nothing
// at all when there are none.
//
// A section of its own rather than more entries among the expectation failures,
// because what it says is different: the test was ended at this call, so its
// expectations were never evaluated and the runtime's behaviour on them is
// unknown rather than correct. The heading says so, since a reader who takes
// these for unmet expectations would conclude that the suite measured something.
//
// No surface line: a call that was refused read no surface. The operation's own
// name takes the feature line's place, for the reason Finding.Feature documents.
func writeOperationFailures(b *strings.Builder, findings []Finding) {
	if len(findings) == 0 {
		return
	}

	b.WriteString("\nFailed operations (each ended its test, leaving its expectations unevaluated):\n")

	for i, f := range findings {
		fmt.Fprintf(b, "\n  %d. %s\n", i+1, orUnknown(f.Test))
		fmt.Fprintf(b, "     operation: %s\n", orUnknown(f.Feature))
		fmt.Fprintf(b, "     error:     %s\n",
			indentContinuation(errText(f.Err), "                "))
	}
}

// writeSkips writes one block per skip, and nothing at all when there are none.
//
// The heading says what a skip is, because a reader who takes a skip for a pass
// has misread the summary: the test was not run to completion, and what it would
// have checked is unknown here.
func writeSkips(b *strings.Builder, skips []Skip) {
	if len(skips) == 0 {
		return
	}

	b.WriteString("\nSkipped (unverified in this run; a skip is not a pass):\n")

	for i, s := range skips {
		fmt.Fprintf(b, "\n  %d. %s\n", i+1, orUnknown(s.Test))

		// The reason's continuation lines are indented under it, as a finding's
		// error is, so a multi-line reason stays part of its skip.
		fmt.Fprintf(b, "     reason: %s\n", indentContinuation(s.Reason, "             "))
	}
}

// writeSummaryCount writes the run's one-line verdict, which is the last thing
// the summary says.
//
// Both counts, always, and each named: a line saying only "1 failed
// operation(s)" would leave a reader to guess whether expectations were
// evaluated at all, and a line saying only "0 expectation failure(s)" is the
// reassurance this whole section exists to stop the summary from giving.
//
// Nothing collected is two different states, and they must not read alike. A
// clean run collected nothing because nothing failed. A failing run that
// collected nothing failed somewhere this summary cannot see -- which is most of
// the suite, since only Suite.Expect* and the recorded driver calls report
// through a Report at all -- and saying "nothing was collected" and no more
// would hand a reader of a failing run the same sentence a clean one gets. That
// is the shape of the 34-failure sign-off, in a place no single test could see,
// so the failing case says plainly that it is blind.
func writeSummaryCount(b *strings.Builder, expectations, operations int, failed, noRuntime bool) {
	// Only when neither kind was recorded. This condition is the defect: with it
	// reading `expectations == 0` alone, a run whose every test was ended by a
	// refused driver call -- 34 of them, on the first CRI-O sweep -- signed off
	// as having collected nothing, which a reader believes.
	if expectations == 0 && operations == 0 {
		if failed {
			b.WriteString("\nThe run FAILED and yet nothing was collected, so it failed " +
				"through assertions\nthis summary does not see: bare require and t.Errorf " +
				"calls in the tests, the\ngates' own inability-to-evaluate failures, and the " +
				"per-case cleanup and leak\nguards. What failed is in the test output above, " +
				"and only there. Do not read\nthis run as clean.\n")

			return
		}

		// Deliberately not a statement about the runtime. The suite exercises a
		// fraction of NRI, and in the default mode a test stops at its first
		// failure, so expectations after it were never evaluated. "Nothing was
		// collected" is all this can honestly say, and it must not be quotable
		// as anything more.
		b.WriteString("\nNo expectation failures and no failed operations were collected. " +
			"That says\nonly that the expectations this run reached were met; it is not a " +
			"verdict on\nanything it did not check.\n")

		if noRuntime {
			b.WriteString("\nThere was no runtime under test: this run measured the NRI side " +
				"of the\nprotocol against a driver that plays the runtime itself, so it says " +
				"nothing\nabout any runtime.\n")
		}

		return
	}

	fmt.Fprintf(b, "\n%d expectation failure(s) and %d failed operation(s) collected.\n",
		expectations, operations)
}

// errText renders a finding's error, including the nil case: a Finding with no
// error should not reach a report, but printing "%!s(<nil>)" in a summary whose
// job is legibility would be worse than saying so.
func errText(err error) string {
	if err == nil {
		return "<no error recorded>"
	}

	return err.Error()
}

// indentContinuation indents every line of s but the first.
func indentContinuation(s, indent string) string {
	return strings.ReplaceAll(s, "\n", "\n"+indent)
}

// orUnknown keeps an empty field from printing as nothing at all, which in a
// column-aligned block reads as a formatting bug rather than as missing data.
func orUnknown(s string) string {
	if s == "" {
		return "<unknown>"
	}

	return s
}

// writeCoverage writes what the run measured of the input surface, against the
// table of everything it could have.
//
// The section is absent from a run that measured no input fields at all: a run
// of the adjustment tests alone would otherwise be told it covered 0 of 41,
// which describes a run that asked and failed rather than one that never asked.
//
// A field counts as covered when its test ran and recorded it. A field whose
// test skipped is NOT covered -- a skip says a property went unverified here,
// which is the opposite of a pass, and the one way this reporting can go wrong
// is by letting a run of skips read as a run that measured something. A field
// whose test ran and failed IS covered: the run looked, and the finding above
// says what it found.
func writeCoverage(b *strings.Builder, covered []Coverage, table []InputField) {
	if len(covered) == 0 {
		return
	}

	seen := map[string]bool{}
	for _, c := range covered {
		seen[c.Field] = true
	}

	var missing []string
	for _, f := range table {
		name := f.Message + "." + f.Name
		if !seen[name] {
			missing = append(missing, name)
		}
	}

	fmt.Fprintf(b, "\nInput field coverage: %d of %d measured in this run\n",
		len(seen), len(table))

	for _, c := range covered {
		fmt.Fprintf(b, "\n  %s at %s\n", c.Field, c.Hook)
	}

	if len(missing) > 0 {
		b.WriteString("\n  not exercised by this run:\n")

		for _, name := range missing {
			fmt.Fprintf(b, "    %s\n", name)
		}
	}
}
