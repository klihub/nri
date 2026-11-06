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

func TestKernelReleaseTrimsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "osrelease")
	require.NoError(t, os.WriteFile(path, []byte("7.2.11-200.fc44.x86_64\n"), 0o600))

	require.Equal(t, "7.2.11-200.fc44.x86_64", kernelRelease(path))
}

func TestKernelReleaseIsEmptyWhenUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")

	require.Equal(t, "", kernelRelease(path))
}

func TestCurrentEnvironmentNamesTheDriver(t *testing.T) {
	useConfig(t, &Config{Driver: DriverCRI})

	require.Equal(t, DriverCRI, CurrentEnvironment().Driver)
}
