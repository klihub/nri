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
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/nri/test/nri-test/framework"
)

// render returns the default template's output for v.
func render(t *testing.T, v *View) string {
	t.Helper()

	tmpl, err := DefaultTemplate()
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Render(&buf, tmpl, v); err != nil {
		t.Fatal(err)
	}

	return buf.String()
}

// flatten collapses every run of whitespace in s to one space.
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// line returns the first line of out that starts with prefix.
func line(t *testing.T, out, prefix string) string {
	t.Helper()

	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}

	t.Fatalf("output has no line starting with %q", prefix)

	return ""
}

// section returns the part of out under the heading line heading, up to the
// next heading of the same level or higher. A "## " heading therefore carries
// its own subsections.
func section(t *testing.T, out, heading string) string {
	t.Helper()

	level := len(heading) - len(strings.TrimLeft(heading, "#"))
	if level == 0 {
		t.Fatalf("%q is not a heading", heading)
	}

	lines := strings.Split(out, "\n")

	start := -1
	for i, l := range lines {
		if l == heading {
			start = i + 1

			break
		}
	}
	if start < 0 {
		t.Fatalf("output has no heading %q", heading)
	}

	for i := start; i < len(lines); i++ {
		next := len(lines[i]) - len(strings.TrimLeft(lines[i], "#"))
		if next > 0 && next <= level {
			return strings.Join(lines[start:i], "\n")
		}
	}

	return strings.Join(lines[start:], "\n")
}

// tableAfter returns the body rows of the table whose header row starts with
// header.
func tableAfter(t *testing.T, out, header string) []string {
	t.Helper()

	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, header) {
			continue
		}

		var rows []string
		for _, r := range lines[i+2:] {
			if !strings.HasPrefix(r, "|") {
				break
			}
			rows = append(rows, r)
		}

		return rows
	}

	t.Fatalf("output has no table headed %q", header)

	return nil
}

// cellsOf returns the trimmed cells of the table row r, without its first.
func cellsOf(r string) []string {
	parts := strings.Split(strings.Trim(r, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	return parts[1:]
}

// fieldTable returns the rows of the input surface table.
func fieldTable(t *testing.T, out string) []string {
	t.Helper()

	return tableAfter(t, out, "| Field |")
}

// inputMismatch returns an input finding carrying the structured reason a run
// against a real runtime records.
func inputMismatch(field string, hook framework.EventType, reason string) framework.FragmentFinding {
	fd := inputFinding(field, hook, reason)
	fd.Reason = reason

	return fd
}

// statusFragment returns a CRI fragment of the runtime, version and handler,
// collecting failures and covering two input fields.
func statusFragment(runtime, version, handler string) framework.Fragment {
	f := fragment(framework.DriverCRI, runtime, version, handler)
	f.Header.NRIVersion = "v0.12.0"
	f.Header.Collecting = true
	f.Environment.KernelRelease = "6.17.0-200.fc44.x86_64"
	f.Environment.Arch = "amd64"
	f.Coverage = append(
		covered("Container.image", framework.CreateContainer),
		covered("Container.rlimits", framework.CreateContainer)...,
	)

	return f
}

// twoColumns returns a view of one runtime measured with two handlers that
// disagree and one measured with a single handler.
func twoColumns(t *testing.T) *View {
	t.Helper()

	crio := statusFragment("cri-o", "1.37.2", "runc")
	crio.Header.NRIVersion = "v0.12.2"
	crio.Findings = []framework.FragmentFinding{
		inputMismatch("Container.image", framework.CreateContainer,
			"not reported: name, digest and config_digest all unset"),
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestMounts",
			Feature: "mount",
			Surface: "OCI spec",
			Hook:    string(framework.CreateContainer),
			Error:   "the mount /tmp/nri is missing",
		},
	}
	crio.Skips = []framework.Skip{{
		Test:   "TestNRIVersionExchange/should_agree_with_the_inferred_version",
		Reason: "InferVersionFromRuntime has no entry for 1.37.x",
	}}

	runc := statusFragment("containerd", "2.3.3", "runc")
	runc.Coverage = append(runc.Coverage,
		covered("Container.state", framework.RemoveContainer)...)
	runc.Findings = []framework.FragmentFinding{
		inputMismatch("Container.state", framework.RemoveContainer,
			"CONTAINER_UNKNOWN, not CONTAINER_STOPPED"),
	}

	crun := statusFragment("containerd", "2.3.3", "crun")
	crun.Coverage = append(crun.Coverage,
		covered("Container.state", framework.PostStartContainer)...)
	crun.Findings = []framework.FragmentFinding{
		inputMismatch("Container.state", framework.PostStartContainer,
			"CONTAINER_CREATED, not CONTAINER_RUNNING"),
	}

	return build(t, crio, runc, crun)
}

func TestRenderIsDeterministic(t *testing.T) {
	v := twoColumns(t)

	first, second := render(t, v), render(t, v)
	if first != second {
		t.Error("two renderings of one view differ")
	}
}

func TestRenderedOutputIsASCII(t *testing.T) {
	for name, v := range map[string]*View{
		"two columns": twoColumns(t),
		"no fragment": build(t),
	} {
		for i, b := range []byte(render(t, v)) {
			if b < 0x09 || b > 0x7e {
				t.Fatalf("%s: byte %d is %#x, outside 0x09..0x7e", name, i, b)
			}
		}
	}
}

func TestDefaultTemplateNamesEveryColumn(t *testing.T) {
	out := render(t, twoColumns(t))

	for _, want := range []string{
		"containerd 2.3.3", "cri-o 1.37.2", "v0.12.0", "v0.12.2",
		"6.17.0-200.fc44.x86_64", "amd64",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not name %q", want)
		}
	}

	handlers := line(t, out, "| Runtime handlers |")
	if want := "| crun, runc | runc |"; !strings.HasSuffix(handlers, want) {
		t.Errorf("handler row %q does not end with %q", handlers, want)
	}
}

// TestReportIsNotAScoreAndSaysItIsGenerated pins the caveat and the
// provenance line, both in a report with columns and in one without.
func TestReportIsNotAScoreAndSaysItIsGenerated(t *testing.T) {
	for name, v := range map[string]*View{
		"two columns": twoColumns(t),
		"no fragment": build(t),
	} {
		flat := flatten(render(t, v))

		for _, want := range []string{
			"Nothing here is a conformance score",
			"`agreed` means \"nothing this suite looked at failed\", " +
				"which is weaker than conformant",
			"Columns are not totalled, ranked or graded",
			"This file is generated",
			"`test/nri-test/status/runtime-status.md.tmpl`",
			"`test/nri-test/status/fragments`",
			"A hand edit here is lost at the next render",
		} {
			if !strings.Contains(flat, want) {
				t.Errorf("%s: output does not carry %q", name, want)
			}
		}
	}
}

func TestParseTemplateRejectsABrokenTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.md.tmpl")
	if err := os.WriteFile(path, []byte("# Status\n{{ .Columns\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseTemplate(path)
	if err == nil {
		t.Fatal("ParseTemplate succeeded")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %q", err, path)
	}
}

func TestParseTemplateRendersAnExternalLayout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terse.md.tmpl")
	body := "{{range .Columns}}{{.Runtime}} {{.Version}}:" +
		"{{range .Measured}} {{.Value}}{{end}}\n{{end}}"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tmpl, err := ParseTemplate(path)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Render(&buf, tmpl, twoColumns(t)); err != nil {
		t.Fatal(err)
	}

	want := "containerd 2.3.3: 3\ncri-o 1.37.2: 2\n"
	if buf.String() != want {
		t.Errorf("rendered %q, want %q", buf.String(), want)
	}
}

func TestRenderFailsOnATemplateThatNamesNothing(t *testing.T) {
	// A layout that misnames a selector must fail loudly. The alternative is
	// a section of the document that silently renders empty, which is a
	// document that looks complete and is not.
	path := filepath.Join(t.TempDir(), "typo.md.tmpl")
	body := "{{range .RowsWithFindings}}- {{.Field}}\n{{end}}"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tmpl, err := ParseTemplate(path)
	if err != nil {
		t.Fatal(err)
	}

	err = Render(io.Discard, tmpl, twoColumns(t))
	if err == nil {
		t.Fatal("Render succeeded on a template naming no such selector")
	}
	if !strings.Contains(err.Error(), "RowsWithFindings") {
		t.Errorf("error %q does not name the selector", err)
	}
}

// TestAnEmptyViewRendersTheNoFragmentsMessage pins the report of a tree with
// no fragment: the message, the preamble, and no measurement table.
func TestAnEmptyViewRendersTheNoFragmentsMessage(t *testing.T) {
	v := build(t)
	if len(v.Rows) != len(framework.InputFields) {
		t.Fatalf("got %d rows, want %d", len(v.Rows), len(framework.InputFields))
	}

	out := render(t, v)

	if flat := flatten(out); !strings.Contains(flat,
		"There are no fragments, so this report records no measurement") {
		t.Error("output does not say it records no measurement")
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "|") {
			t.Errorf("output renders the table row %q for a view with no columns", l)
		}
	}
	for _, unwanted := range []string{
		"## The input surface", "## What was not measured",
		"## Failures outside the input surface",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output renders %q for a view with no columns", unwanted)
		}
	}
}

// TestEveryFieldHasOneRowInInputFieldsOrder pins the field table to one row
// per input field, in the order the framework lists them.
func TestEveryFieldHasOneRowInInputFieldsOrder(t *testing.T) {
	for name, v := range map[string]*View{
		"two columns": twoColumns(t),
		"one column":  build(t, statusFragment("cri-o", "1.37.2", "runc")),
	} {
		rows := fieldTable(t, render(t, v))

		if len(rows) != len(framework.InputFields) {
			t.Errorf("%s: %d rows, want %d", name, len(rows), len(framework.InputFields))

			continue
		}

		for i, f := range framework.InputFields {
			if want := "| `" + fieldName(f) + "`"; !strings.HasPrefix(rows[i], want) {
				t.Errorf("%s: row %d is %q, want it to start with %q", name, i, rows[i], want)
			}
		}
	}
}

// TestACellRendersOneTokenPerColumn pins the three tokens a cell may carry.
func TestACellRendersOneTokenPerColumn(t *testing.T) {
	rows := fieldTable(t, render(t, twoColumns(t)))
	byField := map[string][]string{}
	for _, r := range rows {
		name := strings.TrimPrefix(strings.Fields(r)[1], "`")
		byField[strings.TrimSuffix(name, "`")] = cellsOf(r)
	}

	// The columns are containerd 2.3.3, then cri-o 1.37.2.
	for field, want := range map[string]string{
		"Container.rlimits": "agreed|agreed",
		"Container.image":   "agreed|finding",
		"PodSandbox.uid":    "not measured|not measured",
	} {
		if got := strings.Join(byField[field], "|"); got != want {
			t.Errorf("%s: cells %q, want %q", field, got, want)
		}
	}
}

// TestASplitCellNamesEachHandlerAndItsToken pins a column whose handlers
// disagreed: each handler is named beside its own token.
func TestASplitCellNamesEachHandlerAndItsToken(t *testing.T) {
	runc := statusFragment("containerd", "2.3.3", "runc")
	runc.Coverage = append(runc.Coverage,
		covered("Container.state", framework.RemoveContainer)...)
	crun := statusFragment("containerd", "2.3.3", "crun")
	crun.Coverage = append(crun.Coverage,
		covered("Container.state", framework.PostStartContainer)...)
	crun.Findings = []framework.FragmentFinding{
		inputMismatch("Container.state", framework.PostStartContainer, "wrong"),
	}
	silent := statusFragment("cri-o", "1.37.2", "runc")

	rows := fieldTable(t, render(t, build(t, runc, crun, silent)))
	row := ""
	for _, r := range rows {
		if strings.HasPrefix(r, "| `Container.state`") {
			row = r
		}
	}

	if got, want := cellsOf(row), []string{
		"crun: finding; runc: agreed", "not measured",
	}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("row %q has cells %q, want %q", row, got, want)
	}
}

// TestAFindingIsDetailedBelowTheTableAndNotInIt pins where a finding's text
// goes: under "Findings in detail" naming its column, and out of every cell.
func TestAFindingIsDetailedBelowTheTableAndNotInIt(t *testing.T) {
	out := render(t, twoColumns(t))

	for _, r := range fieldTable(t, out) {
		for _, unwanted := range []string{"not reported", "CONTAINER_", "CreateContainer"} {
			if strings.Contains(r, unwanted) {
				t.Errorf("row %q carries finding text %q", r, unwanted)
			}
		}
	}

	detail := section(t, out, "### Findings in detail")
	for _, want := range []string{
		"- **`Container.image`**\n" +
			"  - cri-o 1.37.2: CreateContainer: not reported: name, " +
			"digest and config_digest all unset\n",
		"- **`Container.state`**\n" +
			"  - containerd 2.3.3: crun: PostStartContainer: " +
			"CONTAINER_CREATED, not CONTAINER_RUNNING; " +
			"runc: RemoveContainer: CONTAINER_UNKNOWN, not CONTAINER_STOPPED\n",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("the detail %q does not carry %q", detail, want)
		}
	}
	if strings.Contains(detail, "Container.rlimits") {
		t.Error("the detail names a field every column agreed on")
	}
}

// TestAPipeInAFindingDoesNotBreakTheTables pins that a pipe in finding text
// leaves every table row with its cells.
func TestAPipeInAFindingDoesNotBreakTheTables(t *testing.T) {
	f := statusFragment("containerd", "2.3.3", "runc")
	f.Findings = []framework.FragmentFinding{
		inputMismatch("Container.image", framework.CreateContainer, "shown a|b, wanted c"),
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestMounts",
			Feature: "mount",
			Surface: "OCI spec",
			Error:   "shown d|e",
		},
	}

	out := render(t, build(t, f))

	for _, r := range fieldTable(t, out) {
		if got, want := len(cellsOf(r)), 1; got != want {
			t.Errorf("row %q has %d cells, want %d", r, got, want)
		}
	}

	row := tableAfter(t, out, "| Column | Test | Feature |")[0]
	if cells := strings.Count(row, "|") - strings.Count(row, `\|`); cells != 6 {
		t.Errorf("row %q has %d cell borders, want 6", row, cells)
	}
}

// TestTheScoreboardNamesTheHandlerOfEachNumber pins that a column of two
// handlers shows each handler's count rather than their sum.
func TestTheScoreboardNamesTheHandlerOfEachNumber(t *testing.T) {
	runc := statusFragment("containerd", "2.3.3", "runc")
	runc.Findings = []framework.FragmentFinding{
		inputMismatch("Container.image", framework.CreateContainer, "not reported"),
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestMounts",
			Feature: "mount",
			Surface: "OCI spec",
			Error:   "the mount is missing",
		},
		{
			Kind:    framework.KindOperation.String(),
			Test:    "TestCreateContainer",
			Feature: "CreateContainer",
			Error:   "refused",
		},
	}
	crun := statusFragment("containerd", "2.3.3", "crun")
	crun.Header.NRIVersion = "v0.11.0"

	out := render(t, build(t, runc, crun))

	for prefix, want := range map[string]string{
		"| Input fields with a finding |": "| crun: 0; runc: 1 |",
		"| NRI version negotiated |":      "| crun: v0.11.0; runc: v0.12.0 |",

		// What both runs reported alike is reported once.
		"| Input fields measured, of ": "| 2 |",
		"| Kernel |":                   "| 6.17.0-200.fc44.x86_64 |",
	} {
		if got := line(t, out, prefix); !strings.HasSuffix(got, want) {
			t.Errorf("row %q does not end with %q", got, want)
		}
	}

	// Suite events are itemised under the failures section, not counted here.
	for _, unwanted := range []string{"| Expectation failures |", "| Failed operations |"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the scoreboard carries the row %q", unwanted)
		}
	}
}

func TestTheFailureModeIsNamedPerColumn(t *testing.T) {
	// The expectation count means a floor in one mode and a total in the
	// other, so a column that does not name its mode cannot be read.
	out := render(t, twoColumns(t))

	mode := line(t, out, "| Failures reported per test |")
	if want := "| every expectation failure | every expectation failure |"; !strings.HasSuffix(mode, want) {
		t.Errorf("mode row %q does not end with %q", mode, want)
	}

	if !strings.Contains(flatten(out), "-nri-test.collect-failures") {
		t.Error("output does not name the flag that chooses the mode")
	}
}

// TestANotedFieldCarriesAMarkerAndADefinition pins that a row's footnote
// marker and its definition exist together, and only for a field with a note.
func TestANotedFieldCarriesAMarkerAndADefinition(t *testing.T) {
	v := twoColumns(t)
	for i := range v.Rows {
		switch v.Rows[i].Field {
		case "Container.rlimits":
			v.Rows[i].Note = ""
		case "Container.image":
			v.Rows[i].Note = "first line\n    second line"
		}
	}

	out := render(t, v)

	for _, r := range fieldTable(t, out) {
		field := strings.Trim(strings.Fields(r)[1], "`")
		marker := "[^" + field + "]"
		noted := strings.Contains(r, marker)

		var note string
		for _, row := range v.Rows {
			if row.Field == field {
				note = row.Note
			}
		}

		if noted != (note != "") {
			t.Errorf("row %q: marker present is %v, note present is %v", r, noted, note != "")
		}
		if def := "\n" + marker + ": "; strings.Contains(out, def) != (note != "") {
			t.Errorf("%s: definition present is %v, note present is %v", field,
				strings.Contains(out, def), note != "")
		}
	}

	if !strings.Contains(out, "\n[^Container.image]: first line\n    second line\n") {
		t.Errorf("the image footnote is not rendered whole in %q",
			section(t, out, "### Notes on individual fields"))
	}
}

// TestNoNotesSectionWithoutNotes pins that a view whose rows carry no note
// renders neither a marker nor the notes section.
func TestNoNotesSectionWithoutNotes(t *testing.T) {
	v := twoColumns(t)
	for i := range v.Rows {
		v.Rows[i].Note = ""
	}

	out := render(t, v)
	for _, unwanted := range []string{"[^", "Notes on individual fields"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a view with no note renders %q", unwanted)
		}
	}
}

// TestAMultiLineNoteRendersAsOneFootnote pins that a note of several lines in
// notes.md reaches the report with every continuation line indented.
func TestAMultiLineNoteRendersAsOneFootnote(t *testing.T) {
	notes, err := Notes()
	if err != nil {
		t.Fatal(err)
	}

	out := render(t, twoColumns(t))
	lines := strings.Split(out, "\n")

	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "[^Container.state]: ") {
			start = i
		}
	}
	if start < 0 {
		t.Fatal("output has no footnote for Container.state")
	}

	want := strings.Split(notes["Container.state"], "\n")
	if len(want) < 2 {
		t.Fatalf("the Container.state note has %d lines, want a multi-line note", len(want))
	}

	for i, w := range want {
		got := lines[start+i]
		if i == 0 {
			got = strings.TrimPrefix(got, "[^Container.state]: ")
		} else if !strings.HasPrefix(got, "    ") {
			t.Errorf("continuation line %q is not indented", got)
		}
		if strings.TrimSpace(got) != strings.TrimSpace(w) {
			t.Errorf("footnote line %d is %q, want %q", i, got, w)
		}
	}
	if next := lines[start+len(want)]; next != "" {
		t.Errorf("the footnote runs on into %q", next)
	}
}

func TestFindingsOutsideTheInputSurfaceAreRendered(t *testing.T) {
	out := render(t, twoColumns(t))

	row := line(t, out, "| cri-o 1.37.2 (runc) |")
	for _, want := range []string{"TestMounts", "mount", "the mount /tmp/nri is missing"} {
		if !strings.Contains(row, want) {
			t.Errorf("row %q does not carry %q", row, want)
		}
	}
}

// TestARefusedDriverCallHasItsOwnTable pins that a refused call is kept out
// of the behavioural findings table and is not called a finding.
func TestARefusedDriverCallHasItsOwnTable(t *testing.T) {
	f := statusFragment("containerd", "2.3.3", "runc")
	f.Findings = []framework.FragmentFinding{
		{
			Kind:    framework.KindExpectation.String(),
			Test:    "TestMounts",
			Feature: "mount",
			Surface: "OCI spec",
			Error:   "the mount /tmp/nri is missing",
		},
		{
			Kind:    framework.KindOperation.String(),
			Test:    "TestCreateContainer",
			Feature: "CreateContainer",
			Error:   "user specified image not specified",
		},
	}

	out := render(t, build(t, f))

	behavioural := strings.Join(tableAfter(t, out,
		"| Column | Test | Feature | Surface | Finding |"), "\n")
	if !strings.Contains(behavioural, "TestMounts") {
		t.Errorf("the behavioural table %q does not carry the mount finding", behavioural)
	}
	if strings.Contains(behavioural, "TestCreateContainer") {
		t.Error("a refused driver call is rendered among the behavioural findings")
	}

	calls := strings.Join(tableAfter(t, out, "| Column | Test | Operation | Error |"), "\n")
	for _, want := range []string{"TestCreateContainer", "user specified image not specified"} {
		if !strings.Contains(calls, want) {
			t.Errorf("the refused-call table %q does not carry %q", calls, want)
		}
	}
	if strings.Contains(calls, "TestMounts") {
		t.Error("a behavioural finding is rendered among the refused driver calls")
	}

	// The prose above the table says what a refusal is: not a finding.
	flat := flatten(section(t, out, "## Failures outside the input surface"))
	for _, want := range []string{
		"These driver calls were refused",
		"unevaluated",
		"none is a finding about NRI behaviour",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("the failures section does not carry %q", want)
		}
	}
}

// TestNoRefusedDriverCallRendersNoRefusedCallTable pins that the refused-call
// table appears only when a call was refused.
func TestNoRefusedDriverCallRendersNoRefusedCallTable(t *testing.T) {
	out := render(t, twoColumns(t))

	for _, unwanted := range []string{"| Operation |", "These driver calls were refused"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a run with no refused call renders %q", unwanted)
		}
	}
}

// TestAFilteredRunQualifiesEveryNegative pins that a run with a selection
// qualifies what the report does not find, and an unfiltered run does not.
func TestAFilteredRunQualifiesEveryNegative(t *testing.T) {
	f := statusFragment("containerd", "2.3.3", "runc")
	f.Selection = "TestInput"

	out := render(t, build(t, f))
	flat := flatten(out)

	for _, want := range []string{
		"**Not every run covered the whole suite.**",
		"| containerd 2.3.3 (runc) | `TestInput` |",
		"recorded no finding among the tests the selected runs ran.",
		"No test skipped among the tests the selected runs ran.",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output does not carry %q", want)
		}
	}

	whole := flatten(render(t, build(t, statusFragment("containerd", "2.3.3", "runc"))))
	for _, unwanted := range []string{
		"Not every run covered the whole suite", "among the tests", "Tests selected",
	} {
		if strings.Contains(whole, unwanted) {
			t.Errorf("an unfiltered run's output carries %q", unwanted)
		}
	}
	for _, want := range []string{
		"recorded no finding.", "No test skipped.",
	} {
		if !strings.Contains(whole, want) {
			t.Errorf("an unfiltered run does not state %q flat", want)
		}
	}
}

// TestASkipNamesTheRunsItWasRecordedIn pins that a skip is attributed to the
// runs that recorded it, and to no other.
func TestASkipNamesTheRunsItWasRecordedIn(t *testing.T) {
	const reason = "InferVersionFromRuntime has no entry for 1.37.x"

	out := render(t, twoColumns(t))

	rows := tableAfter(t, out, "| Tests | Skipped in | Reason |")
	if len(rows) != 1 {
		t.Fatalf("got %d skip rows, want 1: %q", len(rows), rows)
	}
	if want := "| `TestNRIVersionExchange/should_agree_with_the_inferred_version` | " +
		"cri-o 1.37.2 (runc) | " + reason + " |"; rows[0] != want {
		t.Errorf("skip row %q, want %q", rows[0], want)
	}

	skip := framework.Skip{Test: "TestX", Reason: reason}
	runc := statusFragment("containerd", "2.3.3", "runc")
	runc.Skips = []framework.Skip{skip}
	crun := statusFragment("containerd", "2.3.3", "crun")
	crun.Skips = []framework.Skip{skip}

	rows = tableAfter(t, render(t, build(t, runc, crun)), "| Tests | Skipped in | Reason |")
	if len(rows) != 1 || !strings.Contains(rows[0],
		"| containerd 2.3.3 (crun), containerd 2.3.3 (runc) |") {
		t.Errorf("a skip in two runs renders %q, want both runs named", rows)
	}
}

// TestAnUnmeasuredFieldNamesTheColumnsOnBothSides pins that a field one
// column measured and another did not is listed with both, and that a field
// nobody measured is not listed.
func TestAnUnmeasuredFieldNamesTheColumnsOnBothSides(t *testing.T) {
	measured := statusFragment("containerd", "2.3.3", "runc")
	measured.Coverage = append(measured.Coverage,
		covered("Container.env", framework.CreateContainer)...)
	silent := statusFragment("cri-o", "1.37.2", "runc")

	unmeasured := section(t, render(t, build(t, measured, silent)),
		"## What was not measured")

	if want := "- `Container.env` -- not measured by cri-o 1.37.2; " +
		"measured with no finding by containerd 2.3.3"; !strings.Contains(unmeasured, want) {
		t.Errorf("the unmeasured list %q does not carry %q", unmeasured, want)
	}
	for _, unwanted := range []string{"`Container.hooks`", "`Container.rlimits`"} {
		if strings.Contains(unmeasured, unwanted) {
			t.Errorf("the unmeasured list names %s, which no column measured alone", unwanted)
		}
	}
}

func TestAFailedRunWithNoFindingIsRendered(t *testing.T) {
	f := statusFragment("containerd", "2.3.3", "runc")
	f.Header.Failed = true

	flat := flatten(render(t, build(t, f)))
	for _, want := range []string{
		// Named per column, but not attributed to a handler: FailedUnseen is
		// set for the column when any one of its runs failed unseen.
		"**A run in this column failed without recording a finding.**",
		"Some test in containerd 2.3.3 failed outside the findings below",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("output does not carry %q", want)
		}
	}
}

func TestWriteDocumentRefusesToEmptyTheDocument(t *testing.T) {
	// What a mistyped fragment directory would otherwise meet: a view of no
	// columns, a render saying there are no fragments, and every measured row
	// in the committed document gone.
	path := filepath.Join(t.TempDir(), "doc.md")
	const measured = "# Runtime status\n\n| Field | cri-o 1.37.2 |\n"
	if err := os.WriteFile(path, []byte(measured), 0o644); err != nil {
		t.Fatal(err)
	}

	empty := build(t)
	err := WriteDocument(path, []byte("nothing was measured\n"), empty, false)
	if err == nil {
		t.Fatal("WriteDocument replaced the document with a render of no measurement")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "-force") {
		t.Errorf("error %q does not name the file and the way past it", err)
	}

	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(have) != measured {
		t.Errorf("the document is now %q, want it untouched", have)
	}

	if err := WriteDocument(path, []byte("nothing was measured\n"), empty, true); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if have, err := os.ReadFile(path); err != nil || string(have) != "nothing was measured\n" {
		t.Errorf("forced: document is %q (%v)", have, err)
	}
}

func TestWriteDocumentWritesAFreshTree(t *testing.T) {
	// A tree with no fragment and no document yet is the one state a render
	// of no measurement is legitimately in.
	path := filepath.Join(t.TempDir(), "doc.md")

	if err := WriteDocument(path, []byte("x\n"), build(t), false); err != nil {
		t.Fatal(err)
	}
	if have, err := os.ReadFile(path); err != nil || string(have) != "x\n" {
		t.Errorf("document is %q (%v)", have, err)
	}
}

func TestWriteDocumentReplacesByRename(t *testing.T) {
	// Through a temporary file and a rename, as WriteFragment is, so an
	// interrupted write cannot leave a half-truncated document where the
	// whole one was.
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := WriteDocument(path, []byte("new\n"), twoColumns(t), false); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the document was truncated in place rather than renamed over")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "doc.md" {
		t.Errorf("the directory holds %v, want the document alone", entries)
	}
}

func TestCheckFailsOnADivergentDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Check(path, []byte("one\ntwo\nthree\n")); err != nil {
		t.Errorf("identical bytes: %v", err)
	}

	err := Check(path, []byte("one\nTWO\nthree\n"))
	if err == nil {
		t.Fatal("divergent bytes reported no error")
	}
	for _, want := range []string{"line 2", `"two"`, `"TWO"`, "make nri-test-status"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "three") {
		t.Errorf("error %q reaches past the first differing line", err)
	}
}

func TestCheckReportsALengthDifference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Check(path, []byte("one\ntwo\nthree\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Errorf("got %v, want an error at line 3", err)
	}
}

func TestCheckFailsOnAMissingDocument(t *testing.T) {
	if err := Check(filepath.Join(t.TempDir(), "absent.md"), []byte("x\n")); err == nil {
		t.Error("a missing document reported no error")
	}
}
