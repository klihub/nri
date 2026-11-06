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
