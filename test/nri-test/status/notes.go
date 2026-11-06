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
	_ "embed"
	"fmt"
	"strings"

	"github.com/containerd/nri/test/nri-test/framework"
)

//go:embed notes.md
var defaultNotes string

// noteHeading introduces a note and names the field it is about.
const noteHeading = "## "

// Notes returns the note for each input field that has one, read from the
// committed notes.md.
func Notes() (map[string]string, error) {
	return parseNotes(defaultNotes)
}

// parseNotes returns the note for each field named by a heading in src.
//
// A heading naming a field outside framework.InputFields is an error, and so
// is a second heading for a field that already has a note.
func parseNotes(src string) (map[string]string, error) {
	known := map[string]bool{}
	for _, f := range framework.InputFields {
		known[fieldName(f)] = true
	}

	notes := map[string]string{}

	field := ""
	var body []string

	flush := func() error {
		if field == "" {
			return nil
		}

		// A footnote's continuation lines are indented, so that a note of
		// several lines renders as one footnote rather than ending at its
		// first line.
		note := strings.TrimSpace(strings.Join(body, "\n    "))
		if note == "" {
			return fmt.Errorf("notes.md: %q has a heading and no note", field)
		}

		notes[field] = note

		return nil
	}

	for _, line := range strings.Split(src, "\n") {
		name, ok := strings.CutPrefix(line, noteHeading)
		if !ok {
			body = append(body, line)

			continue
		}

		if err := flush(); err != nil {
			return nil, err
		}

		field = strings.TrimSpace(name)
		body = nil

		if !known[field] {
			return nil, fmt.Errorf("notes.md: %q is not a field in InputFields", field)
		}
		if _, seen := notes[field]; seen {
			return nil, fmt.Errorf("notes.md: %q has more than one note", field)
		}

		notes[field] = ""
	}

	if err := flush(); err != nil {
		return nil, err
	}

	return notes, nil
}
