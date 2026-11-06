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
	_ "embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed runtime-status.md.tmpl
var defaultTemplate string

// Render writes v to w, laid out by tmpl.
func Render(w io.Writer, tmpl *template.Template, v *View) error {
	return tmpl.Execute(w, v)
}

// DefaultTemplate returns the template this package carries.
func DefaultTemplate() (*template.Template, error) {
	return template.New("runtime-status.md.tmpl").Parse(defaultTemplate)
}

// ParseTemplate returns the template in the file at path. It fails on a file
// that is unreadable or does not parse, naming it.
func ParseTemplate(path string) (*template.Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New(filepath.Base(path)).Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return tmpl, nil
}

// WriteDocument writes doc to path, through a temporary file in the same
// directory and a rename, so a failed write leaves the document that is there
// intact. Unless force is set, it refuses to write a render of v that holds no
// measurement at all over a file that is already there.
//
// The refusal is what a mistyped fragment directory meets. Losing the document
// to a typo is the worst outcome this command has, and a render with no column
// in it is a state only a fresh tree is legitimately in.
func WriteDocument(path string, doc []byte, v *View, force bool) error {
	if len(v.Columns) == 0 && !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s is there and this render records no measurement, "+
				"since no fragment was loaded; use -force to replace it", path)
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-status-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// Check returns nil if the file at path holds exactly want, and otherwise an
// error naming the first differing line and the command that regenerates it.
func Check(path string, want []byte) error {
	have, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.Equal(have, want) {
		return nil
	}

	haveLines := strings.Split(string(have), "\n")
	wantLines := strings.Split(string(want), "\n")

	n := 0
	for n < len(haveLines) && n < len(wantLines) && haveLines[n] == wantLines[n] {
		n++
	}

	return fmt.Errorf("%s differs from the render at line %d: has %s, render has %s; run make nri-test-status",
		path, n+1, lineAt(haveLines, n), lineAt(wantLines, n))
}

// lineAt returns line i of lines quoted, or "no line" past the end.
func lineAt(lines []string, i int) string {
	if i >= len(lines) {
		return "no line"
	}
	return fmt.Sprintf("%q", lines[i])
}
