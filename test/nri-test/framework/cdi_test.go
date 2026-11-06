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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// useCDIDir points the CDI helpers at a temporary directory, so these run
// without root and without touching the node's own /var/run/cdi.
func useCDIDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	saved := cdiDir
	cdiDir = dir

	t.Cleanup(func() { cdiDir = saved })

	return dir
}

func TestCDISpecPathIsRunScoped(t *testing.T) {
	useCDIDir(t)

	t.Run("should name the run in the file", func(t *testing.T) {
		// Two runs of the suite can share a node, so the file has to say whose
		// it is -- that is what lets teardown remove only its own.
		require.Contains(t, filepath.Base(CDISpecPath("run0")), "run0")
		require.NotEqual(t, CDISpecPath("run0"), CDISpecPath("run1"))
	})
}

func TestCDIDeviceName(t *testing.T) {
	t.Run("should be fully qualified and namespaced to the tests", func(t *testing.T) {
		// The vendor is what keeps this recognisable as ours on a shared node.
		require.Equal(t, "nri-test.containerd.io/device=null", CDIDeviceName("null"))
	})
}

func TestWriteCDISpec(t *testing.T) {
	dir := useCDIDir(t)

	t.Run("should declare the harmless devices under test names", func(t *testing.T) {
		require.NoError(t, WriteCDISpec("run0"))

		raw, err := os.ReadFile(CDISpecPath("run0"))
		require.NoError(t, err)

		var spec struct {
			CDIVersion string `json:"cdiVersion"`
			Kind       string `json:"kind"`
			Devices    []struct {
				Name           string `json:"name"`
				ContainerEdits struct {
					DeviceNodes []struct {
						Path     string `json:"path"`
						HostPath string `json:"hostPath"`
					} `json:"deviceNodes"`
				} `json:"containerEdits"`
			} `json:"devices"`
		}
		require.NoError(t, json.Unmarshal(raw, &spec))

		require.Equal(t, "nri-test.containerd.io/device", spec.Kind)
		require.NotEmpty(t, spec.CDIVersion)

		// Only /dev/null and /dev/zero, exposed under names of our own. A test
		// fixture must not be able to hand a container anything that matters.
		hosts := map[string]string{}
		for _, d := range spec.Devices {
			require.Len(t, d.ContainerEdits.DeviceNodes, 1, d.Name)
			node := d.ContainerEdits.DeviceNodes[0]
			hosts[node.Path] = node.HostPath
		}

		require.Equal(t, map[string]string{
			"/dev/test-null": "/dev/null",
			"/dev/test-zero": "/dev/zero",
		}, hosts)
	})

	t.Run("should create the directory when the node has none", func(t *testing.T) {
		cdiDir = filepath.Join(dir, "missing", "cdi")

		require.NoError(t, WriteCDISpec("run1"))
		require.FileExists(t, CDISpecPath("run1"))
	})
}

func TestRemoveCDISpec(t *testing.T) {
	t.Run("should leave a file the suite did not create", func(t *testing.T) {
		// The rule the suite states twice: never destroy node state it did not
		// create. /var/run/cdi is a directory other things own devices in, and a
		// teardown that swept it would take a real device away from a real
		// workload.
		dir := useCDIDir(t)

		foreign := filepath.Join(dir, "vendor-gpu.json")
		require.NoError(t, os.WriteFile(foreign, []byte(`{"kind":"vendor.com/gpu"}`), 0o644))
		require.NoError(t, WriteCDISpec("run0"))

		require.NoError(t, RemoveCDISpec("run0"))

		require.NoFileExists(t, CDISpecPath("run0"), "our own spec must be removed")
		require.FileExists(t, foreign, "a foreign CDI spec must survive teardown")
	})

	t.Run("should leave another run's spec alone", func(t *testing.T) {
		dir := useCDIDir(t)
		_ = dir

		require.NoError(t, WriteCDISpec("run0"))
		require.NoError(t, WriteCDISpec("run1"))

		require.NoError(t, RemoveCDISpec("run0"))

		require.NoFileExists(t, CDISpecPath("run0"))
		require.FileExists(t, CDISpecPath("run1"),
			"a concurrent run's spec is not ours to remove")
	})

	t.Run("should tolerate a spec that was never written", func(t *testing.T) {
		// Teardown runs after a Setup that may have failed before writing it.
		useCDIDir(t)

		require.NoError(t, RemoveCDISpec("run0"))
	})
}

func TestRemoveCDISpecCleansUpADirectoryItCreated(t *testing.T) {
	t.Run("should remove an empty directory the suite created", func(t *testing.T) {
		// /var/run/cdi does not exist on a node with no CDI devices, so writing
		// the spec creates it. Leaving it behind is node state the suite made
		// and did not take away.
		base := t.TempDir()
		saved := cdiDir
		cdiDir = filepath.Join(base, "cdi")

		t.Cleanup(func() { cdiDir = saved })

		require.NoError(t, WriteCDISpec("run0"))
		require.DirExists(t, cdiDir)

		require.NoError(t, RemoveCDISpec("run0"))
		require.NoDirExists(t, cdiDir, "a directory the suite created must not outlive it")
	})

	t.Run("should keep a directory that still holds something", func(t *testing.T) {
		// Another run's spec, or a real vendor's. Removing a non-empty directory
		// is exactly the sweep this must never become.
		dir := useCDIDir(t)

		foreign := filepath.Join(dir, "vendor-gpu.json")
		require.NoError(t, os.WriteFile(foreign, []byte(`{}`), 0o644))
		require.NoError(t, WriteCDISpec("run0"))

		require.NoError(t, RemoveCDISpec("run0"))

		require.DirExists(t, dir)
		require.FileExists(t, foreign)
	})

	t.Run("should keep a directory that predates the run", func(t *testing.T) {
		// An empty /var/run/cdi that was already there is the node's, not ours.
		dir := useCDIDir(t)

		require.NoError(t, RemoveCDISpec("run0"))
		require.DirExists(t, dir, "a directory the suite did not create is not ours to remove")
	})
}
