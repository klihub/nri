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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/containerd/nri/test/nri-test/framework"
)

func fragment(driver, runtime, version, handler string) framework.Fragment {
	hdr := framework.Header{
		Runtime:        runtime,
		RuntimeVersion: version,
		RuntimeHandler: handler,
	}
	env := framework.Environment{Driver: driver}

	return new(framework.Report).Fragment(hdr, env, "")
}

func write(t *testing.T, dir, name string, f framework.Fragment) {
	t.Helper()

	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRefusesANonCRIFragment(t *testing.T) {
	for _, driver := range []string{framework.DriverMock, framework.DriverK8s} {
		dir := t.TempDir()
		write(t, dir, "odd.json", fragment(driver, "containerd", "2.0", ""))

		_, err := Load(dir)
		if err == nil {
			t.Fatalf("driver %q: Load succeeded", driver)
		}
		for _, want := range []string{"odd.json", driver} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("driver %q: error %q does not name %q",
					driver, err, want)
			}
		}
	}
}

func TestLoadRefusesAnUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	f := fragment(framework.DriverCRI, "containerd", "2.0", "")
	f.Schema = framework.FragmentSchema + 1
	write(t, dir, "future.json", f)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load succeeded")
	}
	if !strings.Contains(err.Error(), "future.json") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestLoadRefusesAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "bad.json") {
		t.Errorf("error %v does not name the file", err)
	}
}

func TestLoadIsOrderedByKey(t *testing.T) {
	dir := t.TempDir()
	in := []framework.Fragment{
		fragment(framework.DriverCRI, "crio", "1.30", "b"),
		fragment(framework.DriverCRI, "containerd", "2.0", "a"),
		fragment(framework.DriverCRI, "crio", "1.30", "a"),
		fragment(framework.DriverCRI, "containerd", "1.7", "z"),
	}
	for i, f := range in {
		write(t, dir, string(rune('a'+3-i))+".json", f)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	want := []Key{
		{"containerd", "1.7", "z"},
		{"containerd", "2.0", "a"},
		{"crio", "1.30", "a"},
		{"crio", "1.30", "b"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d fragments, want %d", len(got), len(want))
	}
	for i, f := range got {
		if KeyOf(f) != want[i] {
			t.Errorf("fragment %d: key %v, want %v", i, KeyOf(f), want[i])
		}
	}
}

func TestLoadOnAnEmptyDirectory(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want an empty non-nil slice", got)
	}
}

func TestLoadRefusesAMissingDirectory(t *testing.T) {
	// A mistyped directory globbed to nothing, which built a view with no
	// column and rendered a document holding no measurement at all over the
	// one in the tree.
	dir := filepath.Join(t.TempDir(), "fragments")

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load succeeded on a directory that does not exist")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error %q does not name the directory", err)
	}
}

func TestLoadRefusesAFileInsteadOfADirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fragments")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("error %v does not name the file", err)
	}
}

func TestLoadRefusesAFragmentThatNamesNoRuntime(t *testing.T) {
	// Teardown writes a fragment even when Setup failed, so a run against the
	// wrong endpoint leaves one whose header has no runtime in it. Rendered,
	// that is a column with no name.
	for name, f := range map[string]framework.Fragment{
		"no runtime": fragment(framework.DriverCRI, "", "1.37.2", "runc"),
		"no version": fragment(framework.DriverCRI, "cri-o", "", "runc"),
		"neither":    fragment(framework.DriverCRI, "", "", ""),
	} {
		dir := t.TempDir()
		write(t, dir, "default.json", f)

		_, err := Load(dir)
		if err == nil {
			t.Errorf("%s: Load succeeded", name)

			continue
		}
		if !strings.Contains(err.Error(), "default.json") {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
	}
}

func TestLoadRefusesADuplicateKey(t *testing.T) {
	dir := t.TempDir()
	f := fragment(framework.DriverCRI, "crio", "1.30", "a")
	write(t, dir, "one.json", f)
	write(t, dir, "two.json", f)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load succeeded")
	}
	for _, want := range []string{"one.json", "two.json", "crio", "1.30"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadReportsTheSchemaBeforeTheShape(t *testing.T) {
	dir := t.TempDir()
	body := fmt.Sprintf(`{"schema": %d, "findings": "not a list"}`,
		framework.FragmentSchema+1)
	if err := os.WriteFile(filepath.Join(dir, "future.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "schema") ||
		strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("error %v should report the schema, not the type", err)
	}
}
