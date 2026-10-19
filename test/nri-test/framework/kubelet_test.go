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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeComm creates <root>/<pid>/comm holding content.
func writeComm(t *testing.T, root, pid, content string) {
	t.Helper()

	dir := filepath.Join(root, pid)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "comm"), []byte(content), 0o644))
}

func TestKubeletRunning(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, root string)
		wantFound bool
		wantPid   string
	}{
		{
			name: "kubelet at a numeric pid",
			setup: func(t *testing.T, root string) {
				writeComm(t, root, "123", "kubelet\n")
			},
			wantFound: true,
			wantPid:   "123",
		},
		{
			name: "another process",
			setup: func(t *testing.T, root string) {
				writeComm(t, root, "123", "containerd\n")
			},
		},
		{
			// /proc/self is this process, not a second one.
			name: "self is not a pid",
			setup: func(t *testing.T, root string) {
				writeComm(t, root, "self", "kubelet\n")
			},
		},
		{
			name: "comm absent",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "456"), 0o755))
			},
		},
		{
			// A comm that cannot be read as a file stands in for one another
			// user's process refuses to show us. It works as root too.
			name: "comm unreadable",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "789", "comm"), 0o755))
			},
		},
		{
			// An unreadable comm must be skipped, not abort the scan: a process
			// can exit between ReadDir and the read, and aborting there would
			// stop refusing exactly on a busy node. 100 sorts before 200.
			name: "unreadable comm does not stop the scan",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "100", "comm"), 0o755))
				writeComm(t, root, "200", "kubelet\n")
			},
			wantFound: true,
			wantPid:   "200",
		},
		{
			name:  "empty tree",
			setup: func(t *testing.T, root string) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(t, root)

			found, pid := kubeletRunning(root)

			require.Equal(t, tc.wantFound, found)
			require.Equal(t, tc.wantPid, pid)
		})
	}
}

// scaleConflictDriver conflicts with kubelet, like the CRI driver. Scalability
// mode must ignore that and demand a kubelet anyway, so the fake has to say
// yes for the inversion to be worth testing.
type scaleConflictDriver struct{ noOpDriver }

func (d *scaleConflictDriver) ConflictsWithKubelet() bool { return true }

func TestCheckKubeletInvertsForScalabilityMode(t *testing.T) {
	withKubelet := t.TempDir()
	writeComm(t, withKubelet, "4242", "kubelet\n")

	withoutKubelet := t.TempDir()
	writeComm(t, withoutKubelet, "4242", "containerd\n")

	d := &scaleConflictDriver{}

	t.Run("a ReplicaSet burst requires a kubelet", func(t *testing.T) {
		cfg := &Config{ScaleReplicas: 10, ScaleDriver: ScaleDriverK8s, procDir: withoutKubelet}

		err := checkKubelet(cfg, d)

		require.Error(t, err, "a burst is a ReplicaSet, which needs kubelet")
		require.ErrorContains(t, err, "-nri-test.scale-replicas",
			"the message names the flag that put the run in this mode")
		require.ErrorContains(t, err, "10",
			"and the replica count, so the reader knows which run this was")
	})

	t.Run("a ReplicaSet burst accepts a kubelet", func(t *testing.T) {
		cfg := &Config{ScaleReplicas: 10, ScaleDriver: ScaleDriverK8s, procDir: withKubelet}

		require.NoError(t, checkKubelet(cfg, d),
			"kubelet is the prerequisite here, not the hazard")
	})

	t.Run("a conformance run still refuses a kubelet", func(t *testing.T) {
		cfg := &Config{procDir: withKubelet}

		err := checkKubelet(cfg, d)

		require.Error(t, err, "the original refusal is unchanged outside scale mode")
		require.ErrorContains(t, err, "-nri-test.allow-kubelet")
	})

	t.Run("a ReplicaSet burst does not need the allow flag", func(t *testing.T) {
		// If the inversion were implemented as "skip when AllowKubelet", a
		// ReplicaSet burst would still have to pass the flag, and a run on a
		// node with no kubelet would get an empty burst instead of a reason.
		cfg := &Config{
			ScaleReplicas: 10, ScaleDriver: ScaleDriverK8s,
			procDir: withKubelet, AllowKubelet: false,
		}

		require.NoError(t, checkKubelet(cfg, d))
	})

	t.Run("a CRI burst still refuses a kubelet", func(t *testing.T) {
		// The two drivers are opposite here, and this is the half that is easy
		// to get wrong: a CRI burst creates sandboxes over CRI like every
		// other test, so kubelet would collect them mid-measurement.
		cfg := &Config{ScaleReplicas: 10, ScaleDriver: ScaleDriverCRI, procDir: withKubelet}

		err := checkKubelet(cfg, d)

		require.Error(t, err)
		require.ErrorContains(t, err, "-nri-test.allow-kubelet",
			"a CRI burst gets the ordinary refusal, not the inversion")
	})

	t.Run("a CRI burst does not require a kubelet", func(t *testing.T) {
		cfg := &Config{ScaleReplicas: 10, ScaleDriver: ScaleDriverCRI, procDir: withoutKubelet}

		require.NoError(t, checkKubelet(cfg, d),
			"the inversion must not leak across to the CRI driver")
	})
}
