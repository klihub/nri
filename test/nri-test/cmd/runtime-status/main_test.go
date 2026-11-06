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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAFailingTemplateLeavesTheDocumentAlone(t *testing.T) {
	dir := t.TempDir()
	tmpl := filepath.Join(dir, "broken.tmpl")
	out := filepath.Join(dir, "doc.md")
	const kept = "committed document\n"

	if err := os.WriteFile(tmpl, []byte("some text\n{{.Missing.Field}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte(kept), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(dir, tmpl, out, false, false); err == nil {
		t.Fatal("a failing template reported no error")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != kept {
		t.Errorf("document changed to %q, want %q", got, kept)
	}
}

func TestAMistypedFragmentDirectoryLeavesTheDocumentAlone(t *testing.T) {
	// The two halves of the worst outcome this command has, end to end: a
	// directory that is not there globs to nothing, and the render of nothing
	// would otherwise be written over the document.
	dir := t.TempDir()
	out := filepath.Join(dir, "doc.md")
	const kept = "committed document\n"

	if err := os.WriteFile(out, []byte(kept), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run(filepath.Join(dir, "frgments"), "", out, false, false); err == nil {
		t.Fatal("a fragment directory that does not exist reported no error")
	}

	// And with the directory there but empty, the render of no measurement is
	// refused rather than written.
	if err := run(dir, "", out, false, false); err == nil {
		t.Fatal("a render of no measurement reported no error")
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != kept {
		t.Errorf("document changed to %q, want %q", got, kept)
	}
}
