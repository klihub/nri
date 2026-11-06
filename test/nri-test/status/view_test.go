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

package status

import (
	"slices"
	"strings"
	"testing"

	"github.com/containerd/nri/test/nri-test/framework"
)

func build(t *testing.T, frags ...framework.Fragment) *View {
	t.Helper()

	v, err := Build(frags)
	if err != nil {
		t.Fatal(err)
	}

	return v
}

func covered(field string, hooks ...framework.EventType) []framework.Coverage {
	var cov []framework.Coverage
	for _, hook := range hooks {
		cov = append(cov, framework.Coverage{Field: field, Hook: hook})
	}

	return cov
}

func inputFinding(field string, hook framework.EventType, text string) framework.FragmentFinding {
	return framework.FragmentFinding{
		Kind:    framework.KindExpectation.String(),
		Test:    "TestInput",
		Feature: field,
		Surface: "OCI spec",
		Hook:    string(hook),
		Error:   field + ": " + text,
	}
}

// values returns the values of a column row, each named by its handler where
// the handlers did not report the same one.
func values(row []HandlerValue) []string {
	var got []string
	for _, v := range row {
		if v.Handler == "" {
			got = append(got, v.Value)

			continue
		}

		got = append(got, v.Handler+": "+v.Value)
	}

	return got
}

func rowFor(t *testing.T, v *View, field string) Row {
	t.Helper()

	for _, r := range v.Rows {
		if r.Field == field {
			return r
		}
	}

	t.Fatalf("no row for %s", field)

	return Row{}
}

func columnFor(t *testing.T, v *View, column int) Column {
	t.Helper()

	if column >= len(v.Columns) {
		t.Fatalf("got %d columns, want column %d", len(v.Columns), column)
	}

	return v.Columns[column]
}

func cellFor(t *testing.T, v *View, field string, column int) Cell {
	t.Helper()

	row := rowFor(t, v, field)
	if column >= len(row.Cells) {
		t.Fatalf("row %s has %d cells, want column %d", field, len(row.Cells), column)
	}

	return row.Cells[column]
}

func TestHandlersThatAgreeBecomeOneCell(t *testing.T) {
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Coverage = covered("Container.image", framework.CreateContainer)
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")
	crun.Coverage = covered("Container.image", framework.CreateContainer)

	v := build(t, runc, crun)

	if len(v.Columns) != 1 {
		t.Fatalf("got %d columns, want 1", len(v.Columns))
	}
	if got := v.Columns[0].Handlers; !slices.Equal(got, []string{"crun", "runc"}) {
		t.Errorf("handlers %v, want [crun runc]", got)
	}
	if len(v.Rows) != len(framework.InputFields) {
		t.Errorf("got %d rows, want %d", len(v.Rows), len(framework.InputFields))
	}

	cell := cellFor(t, v, "Container.image", 0)
	if cell.State != StateAgreed {
		t.Errorf("state %s, want agreed", cell.State)
	}
	if len(cell.PerHandler) != 0 {
		t.Errorf("got %d per-handler cells, want none", len(cell.PerHandler))
	}
}

func TestHandlersThatDifferSplitTheCell(t *testing.T) {
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Coverage = covered("Container.image", framework.CreateContainer)
	runc.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "the plugin was shown busybox"),
	}
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")
	crun.Coverage = covered("Container.image", framework.CreateContainer)
	crun.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "the plugin was shown nothing"),
	}

	cell := cellFor(t, build(t, runc, crun), "Container.image", 0)

	if cell.State != StateFinding {
		t.Errorf("state %s, want finding", cell.State)
	}
	if cell.Text != "" {
		t.Errorf("text %q, want none", cell.Text)
	}
	if len(cell.PerHandler) != 2 {
		t.Fatalf("got %d per-handler cells, want 2", len(cell.PerHandler))
	}

	want := map[string]string{"crun": "shown nothing", "runc": "shown busybox"}
	for _, hc := range cell.PerHandler {
		text, ok := want[hc.Handler]
		if !ok {
			t.Errorf("unexpected handler %q", hc.Handler)

			continue
		}
		if hc.State != StateFinding {
			t.Errorf("handler %s: state %s, want finding", hc.Handler, hc.State)
		}
		if !strings.Contains(hc.Text, text) {
			t.Errorf("handler %s: text %q does not report %q", hc.Handler, hc.Text, text)
		}
		delete(want, hc.Handler)
	}
	if len(want) != 0 {
		t.Errorf("handlers %v are unnamed", want)
	}
}

func TestHandlersThatDifferByHookSplitTheCell(t *testing.T) {
	// One error text, two hooks, so the hook is the only thing the two
	// results differ by.
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Coverage = covered("Container.image", framework.CreateContainer)
	runc.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "absent"),
	}
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")
	crun.Coverage = covered("Container.image", framework.PostCreateContainer)
	crun.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.PostCreateContainer, "absent"),
	}

	cell := cellFor(t, build(t, runc, crun), "Container.image", 0)

	if len(cell.PerHandler) != 2 {
		t.Fatalf("got %d per-handler cells, want 2", len(cell.PerHandler))
	}

	want := map[string]framework.EventType{
		"crun": framework.PostCreateContainer,
		"runc": framework.CreateContainer,
	}
	for _, hc := range cell.PerHandler {
		hook, ok := want[hc.Handler]
		if !ok {
			t.Errorf("unexpected handler %q", hc.Handler)

			continue
		}
		if !strings.HasPrefix(hc.Text, string(hook)+": ") {
			t.Errorf("handler %s: text %q does not lead with the hook %s",
				hc.Handler, hc.Text, hook)
		}
		delete(want, hc.Handler)
	}
	if len(want) != 0 {
		t.Errorf("handlers %v are unnamed", want)
	}
}

func TestUnmeasuredIsNeverMergedIntoAgreement(t *testing.T) {
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Coverage = covered("Container.image", framework.CreateContainer)
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")

	cell := cellFor(t, build(t, runc, crun), "Container.image", 0)

	if cell.State == StateAgreed {
		t.Errorf("state %s reads as agreement", cell.State)
	}
	if len(cell.PerHandler) != 2 {
		t.Fatalf("got %d per-handler cells, want 2", len(cell.PerHandler))
	}

	want := map[string]CellState{"crun": StateUnmeasured, "runc": StateAgreed}
	for _, hc := range cell.PerHandler {
		state, ok := want[hc.Handler]
		if !ok {
			t.Errorf("unexpected handler %q", hc.Handler)

			continue
		}
		if hc.State != state {
			t.Errorf("handler %s: state %s, want %s", hc.Handler, hc.State, state)
		}
		delete(want, hc.Handler)
	}
	if len(want) != 0 {
		t.Errorf("handlers %v are unnamed", want)
	}
}

func TestRuntimesAreNeverMerged(t *testing.T) {
	// One version string for both, so that the runtime is the only thing
	// keeping the two results apart.
	containerd := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	containerd.Coverage = covered("Container.image", framework.CreateContainer)
	crio := fragment(framework.DriverCRI, "cri-o", "2.0.0", "runc")
	crio.Coverage = covered("Container.image", framework.CreateContainer)

	v := build(t, containerd, crio)

	if len(v.Columns) != 2 {
		t.Fatalf("got %d columns, want 2", len(v.Columns))
	}
	for i, want := range []string{"containerd", "cri-o"} {
		if v.Columns[i].Runtime != want {
			t.Errorf("column %d is %q, want %q", i, v.Columns[i].Runtime, want)
		}
	}

	row := rowFor(t, v, "Container.image")
	if len(row.Cells) != 2 {
		t.Fatalf("got %d cells, want 2", len(row.Cells))
	}
	for i, cell := range row.Cells {
		if cell.State != StateAgreed || len(cell.PerHandler) != 0 {
			t.Errorf("cell %d: state %s with %d per-handler cells, want agreed with none",
				i, cell.State, len(cell.PerHandler))
		}
	}
}

func TestUnknownFeatureFailsTheBuild(t *testing.T) {
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Findings = []framework.FragmentFinding{
		inputFinding("Container.nonesuch", framework.CreateContainer, "absent"),
	}

	_, err := Build([]framework.Fragment{f})
	if err == nil {
		t.Fatal("Build succeeded")
	}
	if !strings.Contains(err.Error(), "Container.nonesuch") {
		t.Errorf("error %q does not name the feature", err)
	}
}

func TestBehaviouralFindingsGoToOthers(t *testing.T) {
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Findings = []framework.FragmentFinding{
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestCpuset",
			Feature: "cpuset",
			Surface: "/proc/self/status",
			Error:   "cpuset is 0-7, want 2",
		},
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestHooks",
			Feature: string(framework.RunPodSandbox),
			Surface: "hook request",
			Error:   "the plugin was never invoked",
		},
		{
			Kind:    framework.KindOperation.String(),
			Test:    "TestPod",
			Feature: "CreatePod",
			Error:   "cannot create the pod: refused",
		},
	}

	v := build(t, f)

	if len(v.Others) != 3 {
		t.Fatalf("got %d others, want 3", len(v.Others))
	}

	want := map[string]string{
		"cpuset":        "0-7",
		"RunPodSandbox": "never invoked",
		"CreatePod":     "refused",
	}
	for _, o := range v.Others {
		text, ok := want[o.Feature]
		if !ok {
			t.Errorf("unexpected feature %q", o.Feature)

			continue
		}
		if !strings.Contains(o.Text, text) {
			t.Errorf("feature %s: text %q does not report %q", o.Feature, o.Text, text)
		}
		if !strings.Contains(o.Column, "containerd") || !strings.Contains(o.Column, "runc") {
			t.Errorf("feature %s: column %q names neither the runtime nor the handler",
				o.Feature, o.Column)
		}
		delete(want, o.Feature)
	}
	if len(want) != 0 {
		t.Errorf("findings %v were dropped", want)
	}

	// The two kinds are selected apart, since a refused call measured nothing
	// about the runtime and an unmet expectation did.
	var behavioural, operations []string
	for _, o := range v.OtherExpectations() {
		behavioural = append(behavioural, o.Feature)
	}
	for _, o := range v.OtherOperations() {
		operations = append(operations, o.Feature)
	}
	if !slices.Equal(behavioural, []string{"cpuset", "RunPodSandbox"}) {
		t.Errorf("behavioural findings %v, want [cpuset RunPodSandbox]", behavioural)
	}
	if !slices.Equal(operations, []string{"CreatePod"}) {
		t.Errorf("failed operations %v, want [CreatePod]", operations)
	}

	c := columnFor(t, v, 0)
	if got := values(c.Expectations); !slices.Equal(got, []string{"2"}) {
		t.Errorf("expectations %v, want [2]", got)
	}
	if got := values(c.Operations); !slices.Equal(got, []string{"1"}) {
		t.Errorf("operations %v, want [1]", got)
	}
}

func TestFailedWithNoFindingsIsFlagged(t *testing.T) {
	blind := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	blind.Header.Failed = true

	if c := columnFor(t, build(t, blind), 0); !c.FailedUnseen {
		t.Error("a failed run with no findings is not flagged")
	}

	seen := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	seen.Header.Failed = true
	seen.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "absent"),
	}

	if c := columnFor(t, build(t, seen), 0); c.FailedUnseen {
		t.Error("a failed run that reported a finding is flagged as unseen")
	}
}

func TestCountsComeFromTheFragment(t *testing.T) {
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Coverage = append(
		covered("Container.image", framework.CreateContainer, framework.PostCreateContainer),
		covered("Container.env", framework.CreateContainer)...,
	)
	f.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "absent"),
		inputFinding("Container.image", framework.PostCreateContainer, "absent"),
	}

	v := build(t, f)
	c := columnFor(t, v, 0)

	for name, got := range map[string][]string{
		"measured":            values(c.Measured),
		"fields with finding": values(c.FieldsWithFinding),
		"expectations":        values(c.Expectations),
		"operations":          values(c.Operations),
	} {
		want := map[string][]string{
			"measured":            {"2"},
			"fields with finding": {"1"},
			"expectations":        {"2"},
			"operations":          {"0"},
		}[name]
		if !slices.Equal(got, want) {
			t.Errorf("%s %v, want %v", name, got, want)
		}
	}

	if v.TotalFields != len(framework.InputFields) {
		t.Errorf("total %d, want %d", v.TotalFields, len(framework.InputFields))
	}
}

func TestCountsAreNeverAddedUpAcrossHandlers(t *testing.T) {
	// Two handlers finding the same thing is two runs each finding it, not one
	// column finding it twice. A sum here would grow with the number of runs
	// rather than with what the runtime does.
	one := func(handler string) framework.Fragment {
		f := fragment(framework.DriverCRI, "containerd", "2.0.0", handler)
		f.Coverage = covered("Container.image", framework.CreateContainer)
		f.Findings = []framework.FragmentFinding{
			inputFinding("Container.image", framework.CreateContainer, "absent"),
			{
				Kind:    framework.KindOperation.String(),
				Test:    "TestPod",
				Feature: "CreatePod",
				Error:   "refused",
			},
		}

		return f
	}

	c := columnFor(t, build(t, one("runc"), one("crun")), 0)

	for name, got := range map[string][]string{
		"measured":            values(c.Measured),
		"fields with finding": values(c.FieldsWithFinding),
		"expectations":        values(c.Expectations),
		"operations":          values(c.Operations),
	} {
		if !slices.Equal(got, []string{"1"}) {
			t.Errorf("%s %v, want [1]", name, got)
		}
	}
}

func TestCountsThatDifferAreNamedPerHandler(t *testing.T) {
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Coverage = append(
		covered("Container.image", framework.CreateContainer),
		covered("Container.pid", framework.CreateContainer)...,
	)
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")
	crun.Coverage = covered("Container.image", framework.CreateContainer)

	got := values(columnFor(t, build(t, runc, crun), 0).Measured)
	if want := []string{"crun: 1", "runc: 2"}; !slices.Equal(got, want) {
		t.Errorf("measured %v, want %v", got, want)
	}
}

func TestCoverageOfAnUnknownFieldFailsTheBuild(t *testing.T) {
	// Goal 5 is that the document cannot name a field the suite does not know
	// or omit one it does, and an unknown finding feature already fails the
	// build. Coverage of a field outside the table would otherwise be dropped
	// silently, so a fragment could measure fields nothing could see.
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Coverage = covered("Container.nonesuch", framework.CreateContainer)

	_, err := Build([]framework.Fragment{f})
	if err == nil {
		t.Fatal("Build succeeded")
	}
	for _, want := range []string{"Container.nonesuch", "containerd", "runc"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestSkipsAreCollectedOnceAndNameTheirRuns(t *testing.T) {
	// A skip in one run alone is a gap in that runtime's results and the same
	// skip everywhere is a gap in the suite. Merged without their runs the two
	// read alike, which is the one distinction this document exists to make.
	same := framework.Skip{Test: "TestNUMA", Reason: "one NUMA node"}
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Skips = []framework.Skip{same}
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")
	crun.Skips = []framework.Skip{same, {Test: "TestCDI", Reason: "no CDI registry"}}
	crio := fragment(framework.DriverCRI, "cri-o", "1.37.2", "runc")
	crio.Skips = []framework.Skip{same}

	got := build(t, runc, crun, crio).Skips

	want := []Skip{
		{
			Test:   "TestNUMA",
			Reason: "one NUMA node",
			Columns: []string{
				"containerd 2.0.0 (crun)",
				"containerd 2.0.0 (runc)",
				"cri-o 1.37.2 (runc)",
			},
		},
		{
			Test:    "TestCDI",
			Reason:  "no CDI registry",
			Columns: []string{"containerd 2.0.0 (crun)"},
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d skips, want %d: %v", len(got), len(want), got)
	}
	for i, s := range got {
		if s.Test != want[i].Test || s.Reason != want[i].Reason ||
			!slices.Equal(s.Columns, want[i].Columns) {
			t.Errorf("skip %d is %+v, want %+v", i, s, want[i])
		}
	}
}

func TestAFilteredRunIsReported(t *testing.T) {
	// The write guard covers input-field coverage alone, so a filtered run
	// that covers every input field replaces a full fragment without being
	// refused -- and the document would then say the behavioural tests found
	// nothing, about tests that never ran.
	filtered := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	filtered.Selection = "TestInput"
	whole := fragment(framework.DriverCRI, "cri-o", "1.37.2", "runc")

	got := build(t, filtered, whole).Selections

	want := []Selection{{Column: "containerd 2.0.0 (runc)", Tests: "TestInput"}}
	if !slices.Equal(got, want) {
		t.Errorf("selections %v, want %v", got, want)
	}

	if got := build(t, whole).Selections; len(got) != 0 {
		t.Errorf("an unfiltered run reports the selection %v", got)
	}
}

func TestAnUnmeasuredRowNamesBothSides(t *testing.T) {
	// A field one column measured and agreed on, while another never looked at
	// it. Listed bare, it reads exactly like a field nobody measured, and the
	// agreement vanishes from the document.
	measured := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	measured.Coverage = covered("Container.image", framework.CreateContainer)
	silent := fragment(framework.DriverCRI, "cri-o", "1.37.2", "runc")

	row := rowFor(t, build(t, measured, silent), "Container.image")

	if got, want := row.MeasuredIn, []string{"containerd 2.0.0"}; !slices.Equal(got, want) {
		t.Errorf("measured in %v, want %v", got, want)
	}
	if got, want := row.UnmeasuredIn, []string{"cri-o 1.37.2"}; !slices.Equal(got, want) {
		t.Errorf("unmeasured in %v, want %v", got, want)
	}

	// A field no column measured names no column as having measured it, so the
	// list of those stays bare.
	nobody := rowFor(t, build(t, measured, silent), "Container.hooks")
	if len(nobody.MeasuredIn) != 0 {
		t.Errorf("a field nobody measured names %v as measuring it", nobody.MeasuredIn)
	}
	if got, want := nobody.UnmeasuredIn,
		[]string{"containerd 2.0.0", "cri-o 1.37.2"}; !slices.Equal(got, want) {
		t.Errorf("unmeasured in %v, want %v", got, want)
	}
}

func TestCellTextPrefersTheReason(t *testing.T) {
	// The row is already labelled with the field, so a cell that led with
	// InputMismatch.Error would name it twice.
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Coverage = append(
		covered("Container.image", framework.CreateContainer),
		covered("Container.pid", framework.CreateContainer)...,
	)
	withReason := inputFinding("Container.image", framework.CreateContainer, "not reported")
	withReason.Reason = "not reported"
	f.Findings = []framework.FragmentFinding{
		withReason,
		inputFinding("Container.pid", framework.CreateContainer, "zero"),
	}

	v := build(t, f)

	if got, want := cellFor(t, v, "Container.image", 0).Text,
		"CreateContainer: not reported"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
	if got, want := cellFor(t, v, "Container.pid", 0).Text,
		"CreateContainer: Container.pid: zero"; got != want {
		t.Errorf("text %q, want %q", got, want)
	}
}

func TestRowStateIsTheMostSevereCell(t *testing.T) {
	// One column measures and fails one field, measures and agrees on a
	// second, and never looks at the rest. A second column measures only the
	// field the first agreed on, so that row carries agreement and silence.
	first := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	first.Coverage = append(
		covered("Container.image", framework.CreateContainer),
		covered("Container.pid", framework.CreateContainer)...,
	)
	first.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "absent"),
	}
	second := fragment(framework.DriverCRI, "cri-o", "1.37.2", "runc")
	second.Coverage = covered("Container.image", framework.CreateContainer)

	v := build(t, first, second)

	for field, want := range map[string]CellState{
		"Container.image": StateFinding,
		"Container.pid":   StateUnmeasured,
		"Container.name":  StateUnmeasured,
	} {
		if got := rowFor(t, v, field).State; got != want {
			t.Errorf("%s: state %s, want %s", field, got, want)
		}
	}

	// Agreement is reported only when every cell agrees, so it needs the
	// column that is silent on Container.pid removed.
	only := build(t, first)
	if got := rowFor(t, only, "Container.pid").State; got != StateAgreed {
		t.Errorf("Container.pid alone: state %s, want agreed", got)
	}
}

func TestRowStateOfAViewWithNoFragments(t *testing.T) {
	for _, row := range build(t).Rows {
		if row.State != StateUnmeasured {
			t.Fatalf("%s: state %s, want unmeasured", row.Field, row.State)
		}
	}
}

func TestTheRowSelectorsSelectInFieldOrder(t *testing.T) {
	// One field fails late in framework.InputFields and one early, so the
	// finding order cannot come from the findings' own order.
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Coverage = append(
		covered("PodSandbox.name", framework.RunPodSandbox),
		covered("Container.image", framework.CreateContainer)...,
	)
	f.Coverage = append(f.Coverage,
		covered("Container.pid", framework.CreateContainer)...)
	f.Findings = []framework.FragmentFinding{
		inputFinding("Container.image", framework.CreateContainer, "absent"),
		inputFinding("PodSandbox.name", framework.RunPodSandbox, "absent"),
	}

	v := build(t, f)

	var findings []string
	for _, row := range v.RowsWithFinding() {
		findings = append(findings, row.Field)
	}
	if !slices.Equal(findings, []string{"PodSandbox.name", "Container.image"}) {
		t.Errorf("finding rows %v, want them in framework.InputFields order", findings)
	}

	if got, want := len(v.RowsUnmeasured()), len(framework.InputFields)-3; got != want {
		t.Errorf("got %d unmeasured rows, want %d", got, want)
	}
}

func TestColumnNamesTheFailureMode(t *testing.T) {
	// The expectation and operation counts mean different things in the two
	// modes, so a column that does not say which it ran in cannot be read.
	collecting := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	collecting.Header.Collecting = true
	stopping := fragment(framework.DriverCRI, "cri-o", "1.37.2", "runc")

	v := build(t, collecting, stopping)

	if got, want := values(columnFor(t, v, 0).Collecting),
		[]string{"every expectation failure"}; !slices.Equal(got, want) {
		t.Errorf("collecting column: %v, want %v", got, want)
	}
	if got, want := values(columnFor(t, v, 1).Collecting),
		[]string{"the first failure in each test"}; !slices.Equal(got, want) {
		t.Errorf("stopping column: %v, want %v", got, want)
	}
}

func TestOneColumnWhoseHandlersDisagreeOnTheFailureMode(t *testing.T) {
	// Two handlers of one runtime run in different modes, so the column's
	// counts are not comparable within itself. Naming one mode would hide it.
	runc := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	runc.Header.Collecting = true
	crun := fragment(framework.DriverCRI, "containerd", "2.0.0", "crun")

	got := values(columnFor(t, build(t, runc, crun), 0).Collecting)
	want := []string{"crun: the first failure in each test", "runc: every expectation failure"}
	if !slices.Equal(got, want) {
		t.Errorf("collecting %v, want %v", got, want)
	}
}

func TestTableBreakingTextIsEscaped(t *testing.T) {
	// A finding's error, a finding's surface and a skip's reason are written
	// by whoever wrote the test, so a pipe or a newline in one would break
	// the row it renders into.
	f := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	f.Coverage = covered("Container.image", framework.CreateContainer)
	f.Findings = []framework.FragmentFinding{
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestInput",
			Feature: "Container.image",
			Hook:    string(framework.CreateContainer),
			Error:   "shown a|b,\nwanted c",
		},
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestMounts",
			Feature: "mount",
			Surface: "a|b",
			Error:   "x|y",
		},
	}
	f.Skips = []framework.Skip{{Test: "TestA|B", Reason: "needs a|b\nand more"}}

	v := build(t, f)

	for name, got := range map[string]string{
		"cell text":      cellFor(t, v, "Container.image", 0).Text,
		"other text":     v.Others[0].Text,
		"other surface":  v.Others[0].Surface,
		"skip test":      v.Skips[0].Test,
		"skip reason":    v.Skips[0].Reason,
		"column handler": columnFor(t, v, 0).Handlers[0],
	} {
		if strings.ContainsAny(got, "\n\r\t") {
			t.Errorf("%s %q carries a line break", name, got)
		}
		if strings.Contains(strings.ReplaceAll(got, "\\|", ""), "|") {
			t.Errorf("%s %q carries an unescaped pipe", name, got)
		}
	}

	if got, want := v.Skips[0].Reason, `needs a\|b and more`; got != want {
		t.Errorf("skip reason %q, want %q", got, want)
	}
}

// TestSkipsGroupByReason pins that skips sharing a reason collapse into one
// group whose columns are the union of theirs, since a report where one
// reason repeats verbatim per test buries the reasons that differ.
func TestSkipsGroupByReason(t *testing.T) {
	a := fragment(framework.DriverCRI, "containerd", "2.0.0", "runc")
	a.Skips = []framework.Skip{
		{Test: "TestOne", Reason: "lacks a capability"},
		{Test: "TestTwo", Reason: "lacks a capability"},
		{Test: "TestThree", Reason: "no OCI hook on this node"},
	}
	b := fragment(framework.DriverCRI, "cri-o", "1.37.3", "runc")
	b.Skips = []framework.Skip{
		{Test: "TestOne", Reason: "lacks a capability"},
	}

	groups := build(t, a, b).SkipGroups()

	if len(groups) != 2 {
		t.Fatalf("got %d skip groups, want 2", len(groups))
	}
	if got := groups[0].Reason; got != "lacks a capability" {
		t.Errorf("first group's reason is %q", got)
	}
	if got := len(groups[0].Tests); got != 2 {
		t.Errorf("first group holds %d tests, want 2", got)
	}
	if got := len(groups[0].Columns); got != 2 {
		t.Errorf("first group names %d columns, want both", got)
	}
	if got := len(groups[1].Tests); got != 1 {
		t.Errorf("second group holds %d tests, want 1", got)
	}
}
