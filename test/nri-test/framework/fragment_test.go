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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFragmentRoundTrips(t *testing.T) {
	r := &Report{}
	r.AddFinding(Finding{Kind: KindExpectation, Test: "A", Feature: "annotation", Surface: "spec", Err: fmt.Errorf("missing")})
	r.AddFinding(Finding{Kind: KindOperation, Test: "B", Feature: "CreatePod", Err: fmt.Errorf("refused")})
	r.AddFinding(Finding{Test: "C", Feature: "Container.pid", Hook: "CreateContainer", Surface: "request",
		Err: &InputMismatch{Field: "Container.pid", Plugin: "0", Expected: "7", Reason: "differs"}})
	r.AddSkip(Skip{Test: "D", Reason: "no cgroup v2"})
	r.AddCoverage(Coverage{Field: "Container.pid", Hook: "CreateContainer"})
	r.AddCoverage(Coverage{Field: "Pod.uid", Hook: "RunPodSandbox"})

	want := r.Fragment(Header{Runtime: "cri-o", RuntimeVersion: "1.37.2", Failed: true},
		Environment{Driver: "cri", Arch: "amd64"}, "TestX")

	data, err := json.Marshal(want)
	require.NoError(t, err)

	var got Fragment
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, want, got)
	require.Equal(t, 1, got.Schema)
	require.Len(t, got.Findings, 3)
}

func TestFragmentFindingCarriesBothSides(t *testing.T) {
	r := &Report{}
	r.AddFinding(Finding{Err: &InputMismatch{Field: "f", Plugin: "shown", Expected: "wanted", Reason: "r"}})
	r.AddFinding(Finding{Err: fmt.Errorf("plain")})

	fs := r.Fragment(Header{}, Environment{}, "").Findings
	require.Equal(t, "shown", fs[0].Plugin)
	require.Equal(t, "wanted", fs[0].Expected)
	require.NotEmpty(t, fs[0].Error)
	require.Empty(t, fs[1].Plugin)
	require.Empty(t, fs[1].Expected)
	require.Equal(t, "plain", fs[1].Error)
}

func TestKeyIsFilesystemSafe(t *testing.T) {
	key := func(rt, v, h string) string {
		return Fragment{Header: Header{Runtime: rt, RuntimeVersion: v, RuntimeHandler: h}}.Key()
	}

	require.Equal(t, "cri-o-1.37.2-runc", key("cri-o", "1.37.2", "runc"))
	require.Equal(t, "cri-o-1.37.2-default", key("cri-o", "1.37.2", ""))
	require.Equal(t, "containerd-v2.0-rc.1-crun", key("containerd", "V2.0-RC.1", "crun"))

	got := key("cri-o", "1.37/2 beta", "runc")
	require.NotContains(t, got, "/")
	require.NotContains(t, got, " ")
}

func fragmentWith(n int, marker string) Fragment {
	f := Fragment{Schema: 1, Header: Header{Runtime: "cri-o", RuntimeVersion: "1", RuntimeHandler: "runc"}, Selection: marker}
	for i := 0; i < n; i++ {
		f.Coverage = append(f.Coverage, Coverage{Field: fmt.Sprintf("Container.f%d", i), Hook: "CreateContainer"})
	}
	return f
}

func TestWriteFragmentRefusesToShrinkARecord(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteFragment(dir, fragmentWith(39, "full"), false))

	path := filepath.Join(dir, "cri-o-1-runc.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	err = WriteFragment(dir, fragmentWith(5, "filtered"), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "measured 34 fields this run did not (Container.f10, Container.f11, Container.f12 and 31 more)")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))

	require.NoError(t, WriteFragment(dir, fragmentWith(5, "filtered"), true))
	got, err := readFragment(path)
	require.NoError(t, err)
	require.Len(t, got.Coverage, 5)
}

func TestWriteFragmentReplacesAnEqualOrFullerRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cri-o-1-runc.json")
	require.NoError(t, WriteFragment(dir, fragmentWith(39, "a"), false))

	require.NoError(t, WriteFragment(dir, fragmentWith(39, "b"), false))
	got, err := readFragment(path)
	require.NoError(t, err)
	require.Equal(t, "b", got.Selection)

	require.NoError(t, WriteFragment(dir, fragmentWith(41, "c"), false))
	got, err = readFragment(path)
	require.NoError(t, err)
	require.Equal(t, "c", got.Selection)
	require.Len(t, got.Coverage, 41)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left behind")
}

func TestWriteFragmentRefusesToReplaceAnUnparseableRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cri-o-1-runc.json")
	garbage := []byte("{\"schema\": 2, \"cover")
	require.NoError(t, os.WriteFile(path, garbage, 0o644))

	err := WriteFragment(dir, fragmentWith(5, "new"), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), path)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, garbage, after)

	require.NoError(t, WriteFragment(dir, fragmentWith(5, "new"), true))
	got, err := readFragment(path)
	require.NoError(t, err)
	require.Equal(t, "new", got.Selection)
}

func fragmentOf(cov ...Coverage) Fragment {
	f := fragmentWith(0, "")
	f.Coverage = cov
	return f
}

func TestWriteFragmentComparesFieldsNotEntries(t *testing.T) {
	cov := func(field, hook string) Coverage { return Coverage{Field: field, Hook: EventType(hook)} }

	dir := t.TempDir()
	path := filepath.Join(dir, "cri-o-1-runc.json")

	// Three entries over one field, replaced by two entries over two fields.
	require.NoError(t, WriteFragment(dir, fragmentOf(
		cov("Container.pid", "a"), cov("Container.pid", "b"), cov("Container.pid", "c")), false))
	require.NoError(t, WriteFragment(dir, fragmentOf(
		cov("Container.pid", "a"), cov("Container.user", "a")), false))
	got, err := readFragment(path)
	require.NoError(t, err)
	require.Len(t, got.Coverage, 2)

	// Fewer entries, but a field the existing record measured is missing.
	err = WriteFragment(dir, fragmentOf(cov("Container.pid", "a")), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "measured 1 fields this run did not (Container.user)")

	// Equal counts over different fields are refused too.
	err = WriteFragment(dir, fragmentOf(cov("Container.pid", "a"), cov("Pod.uid", "a")), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Container.user")
}
