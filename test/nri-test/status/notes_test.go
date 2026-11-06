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
	"regexp"
	"strings"
	"testing"
)

// TestTheCommittedNotesParse pins that notes.md names only input fields.
func TestTheCommittedNotesParse(t *testing.T) {
	notes, err := Notes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 {
		t.Error("notes.md yields no note")
	}
}

// TestANoteForAnUnknownFieldFails pins the refusal of a heading outside
// framework.InputFields.
func TestANoteForAnUnknownFieldFails(t *testing.T) {
	_, err := parseNotes("## Container.image\nfine\n\n## Container.nonesuch\nx\n")
	if err == nil {
		t.Fatal("a note for a field the suite does not have was accepted")
	}
	if !strings.Contains(err.Error(), "Container.nonesuch") {
		t.Errorf("error %q does not name the field", err)
	}
}

// TestADuplicateNoteFails pins the refusal of a second note for one field.
func TestADuplicateNoteFails(t *testing.T) {
	_, err := parseNotes("## Container.image\none\n\n## Container.state\ntwo\n" +
		"\n## Container.image\nthree\n")
	if err == nil {
		t.Fatal("two notes for one field were accepted")
	}
	if !strings.Contains(err.Error(), "Container.image") {
		t.Errorf("error %q does not name the field", err)
	}
}

// TestAHeadingWithNoNoteFails pins the refusal of a field named and left
// unexplained.
func TestAHeadingWithNoNoteFails(t *testing.T) {
	if _, err := parseNotes("## Container.image\n\n## Container.state\nx\n"); err == nil {
		t.Error("a heading with no note was accepted")
	}
}

// TestAMultiLineNoteIsIndentedAndTheTextBeforeTheFirstHeadingIsDropped pins
// parseNotes' shape: continuation lines indented, the preamble not a note.
func TestAMultiLineNoteIsIndentedAndTheTextBeforeTheFirstHeadingIsDropped(t *testing.T) {
	notes, err := parseNotes("preamble\n\n## Container.image\none\ntwo\n\n" +
		"## Container.state\nthree\n")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := notes["Container.image"], "one\n    two"; got != want {
		t.Errorf("image note %q, want %q", got, want)
	}
	if got, want := notes["Container.state"], "three"; got != want {
		t.Errorf("state note %q, want %q", got, want)
	}
	if len(notes) != 2 {
		t.Errorf("got %d notes, want 2", len(notes))
	}
}

// frozenCounts returns the per-run counts written into prose, ignoring
// anything inside a template action, which a run fills in.
func frozenCounts(src string) []string {
	actions := regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	counts := regexp.MustCompile(`[0-9]+ (passed|failed|skipped|of [0-9]+)`)

	return counts.FindAllString(actions.ReplaceAllString(src, ""), -1)
}

// TestNoFrozenCountsInWrittenProse pins that no hand-written text carries a
// per-run count: the template and the notes.
func TestNoFrozenCountsInWrittenProse(t *testing.T) {
	for name, src := range map[string]string{
		"runtime-status.md.tmpl": defaultTemplate,
		"notes.md":               defaultNotes,
	} {
		if got := frozenCounts(src); len(got) != 0 {
			t.Errorf("%s carries the frozen counts %q", name, got)
		}
	}
}

// TestTheFrozenCountCheckSeparatesProseFromRenderedCounts pins the check
// itself: written counts are found and a count a run fills in is not.
func TestTheFrozenCountCheckSeparatesProseFromRenderedCounts(t *testing.T) {
	for _, prose := range []string{"28 of 41 fields", "3 passed", "2 failed", "1 skipped"} {
		if len(frozenCounts(prose)) == 0 {
			t.Errorf("the check misses %q", prose)
		}
	}

	for _, rendered := range []string{
		"{{.Measured}} of {{.TotalFields}}", "| Input fields measured, of {{.TotalFields}} |",
	} {
		if got := frozenCounts(rendered); len(got) != 0 {
			t.Errorf("the check flags %q as %q", rendered, got)
		}
	}
}
