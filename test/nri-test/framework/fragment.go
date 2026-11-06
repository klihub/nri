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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FragmentSchema is the Schema of the fragments this package writes.
const FragmentSchema = 1

// Fragment is one run's results in the form written to disk.
type Fragment struct {
	Schema      int               `json:"schema"`
	Header      Header            `json:"header"`
	Environment Environment       `json:"environment"`
	Selection   string            `json:"selection"`
	Findings    []FragmentFinding `json:"findings"`
	Skips       []Skip            `json:"skips"`
	Coverage    []Coverage        `json:"coverage"`
}

// FragmentFinding is a Finding in written form. Error is the finding's error
// verbatim. Reason, Plugin and Expected are set only when that error is an
// InputMismatch: Reason is the clause of Error that follows the field name.
type FragmentFinding struct {
	Kind     string `json:"kind"`
	Test     string `json:"test"`
	Feature  string `json:"feature"`
	Surface  string `json:"surface"`
	Hook     string `json:"hook"`
	Error    string `json:"error"`
	Reason   string `json:"reason"`
	Plugin   string `json:"plugin"`
	Expected string `json:"expected"`
}

// Fragment returns the report's contents, with hdr, env and selection, in
// written form.
func (r *Report) Fragment(hdr Header, env Environment, selection string) Fragment {
	f := Fragment{
		Schema:      FragmentSchema,
		Header:      hdr,
		Environment: env,
		Selection:   selection,
		Findings:    []FragmentFinding{},
		Skips:       append([]Skip{}, r.Skips()...),
		Coverage:    append([]Coverage{}, r.CoverageOf()...),
	}

	for _, fd := range r.Findings() {
		ff := FragmentFinding{
			Kind:    fd.Kind.String(),
			Test:    fd.Test,
			Feature: fd.Feature,
			Surface: fd.Surface,
			Hook:    string(fd.Hook),
		}

		if fd.Err != nil {
			ff.Error = fd.Err.Error()

			var im *InputMismatch
			if errors.As(fd.Err, &im) {
				ff.Reason = im.Reason
				ff.Plugin = im.Plugin
				ff.Expected = im.Expected
			}
		}

		f.Findings = append(f.Findings, ff)
	}

	return f
}

var unsafeKeyChars = regexp.MustCompile(`[^a-z0-9._-]`)

// Key returns the file name stem of the fragment: runtime, version and
// handler, lowercased and safe in a file name.
func (f Fragment) Key() string {
	handler := f.Header.RuntimeHandler
	if handler == "" {
		handler = "default"
	}

	key := strings.Join([]string{f.Header.Runtime, f.Header.RuntimeVersion, handler}, "-")

	return unsafeKeyChars.ReplaceAllString(strings.ToLower(key), "-")
}

// WriteFragment writes f as <Key>.json in dir, creating dir. Unless force is
// set, it refuses to replace a file that is not a readable fragment, or a
// fragment that measured a field f did not.
func WriteFragment(dir string, f Fragment, force bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(dir, f.Key()+".json")

	if !force {
		old, err := readFragment(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return fmt.Errorf("%s exists and cannot be read as a fragment: %w; "+
				"use -nri-test.status-force to replace it", path, err)
		default:
			if lost := lostFields(old, f); len(lost) > 0 {
				return fmt.Errorf("%s measured %d fields this run did not (%s); "+
					"use -nri-test.status-force to replace it",
					path, len(lost), fieldList(lost))
			}
		}
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".fragment-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
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

// readFragment reads the fragment stored at path.
func readFragment(path string) (Fragment, error) {
	var f Fragment

	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}

	return f, json.Unmarshal(data, &f)
}

// lostFields returns the sorted distinct fields old measured that next did
// not.
func lostFields(old, next Fragment) []string {
	have := map[string]bool{}
	for _, c := range next.Coverage {
		have[c.Field] = true
	}

	seen := map[string]bool{}
	var lost []string
	for _, c := range old.Coverage {
		if !have[c.Field] && !seen[c.Field] {
			seen[c.Field] = true
			lost = append(lost, c.Field)
		}
	}
	sort.Strings(lost)

	return lost
}

// fieldList joins up to three fields and counts the rest.
func fieldList(fields []string) string {
	if len(fields) <= 3 {
		return strings.Join(fields, ", ")
	}

	return fmt.Sprintf("%s and %d more", strings.Join(fields[:3], ", "), len(fields)-3)
}
