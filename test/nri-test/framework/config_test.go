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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigComplete(t *testing.T) {
	t.Run("should generate a run id", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null"}
		require.NoError(t, c.Complete())
		require.NotEmpty(t, c.RunID)
	})

	t.Run("should keep an explicitly given run id", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null", RunID: "fixed"}
		require.NoError(t, c.Complete())
		require.Equal(t, "fixed", c.RunID)
	})

	t.Run("should default the image endpoint to the runtime endpoint", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///run/containerd/containerd.sock"}
		require.NoError(t, c.Complete())
		require.Equal(t, "unix:///run/containerd/containerd.sock", c.ImageEndpoint)
	})

	t.Run("should not override an explicit image endpoint", func(t *testing.T) {
		c := &Config{
			RuntimeEndpoint: "unix:///run/containerd/containerd.sock",
			ImageEndpoint:   "unix:///run/other.sock",
		}
		require.NoError(t, c.Complete())
		require.Equal(t, "unix:///run/other.sock", c.ImageEndpoint)
	})

	t.Run("should default the CRI namespaces to kubelet's", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null"}
		require.NoError(t, c.Complete())
		require.Equal(t, NamespacesK8s, c.CRINamespaces)
	})

	t.Run("should refuse an unknown CRI namespaces mode", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null", CRINamespaces: "host"}
		require.Error(t, c.Complete())
	})

	t.Run("should fail when no runtime endpoint can be found", func(t *testing.T) {
		c := &Config{endpointCandidates: []string{"unix:///nonexistent/nri-test.sock"}}
		require.Error(t, c.Complete())
	})
}

func TestConfigPodLabels(t *testing.T) {
	t.Run("should carry the run id and the case name", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null", RunID: "run0"}
		require.NoError(t, c.Complete())

		labels := c.PodLabels("my-case")
		require.Equal(t, "run0", labels[RunIDLabel])
		require.Equal(t, "my-case", labels[CaseLabel])
	})
}

func TestDetectRuntimeEndpointRefusesToChooseBetweenRuntimes(t *testing.T) {
	dir := t.TempDir()
	a, b := dir+"/a.sock", dir+"/b.sock"

	for _, p := range []string{a, b} {
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}

	c := &Config{endpointCandidates: []string{"unix://" + a, "unix://" + b}}

	_, err := c.detectRuntimeEndpoint()
	require.ErrorContains(t, err, "several CRI runtime endpoints")
	require.ErrorContains(t, err, "-runtime-endpoint")
}

func TestDetectRuntimeEndpointUsesTheOnlyOneThatExists(t *testing.T) {
	dir := t.TempDir()
	a := dir + "/a.sock"

	require.NoError(t, os.WriteFile(a, nil, 0o600))

	c := &Config{endpointCandidates: []string{"unix://" + dir + "/gone.sock", "unix://" + a}}

	ep, err := c.detectRuntimeEndpoint()
	require.NoError(t, err)
	require.Equal(t, "unix://"+a, ep)
}

func TestConfigDefaults(t *testing.T) {
	t.Run("should default the NRI socket path and timeout", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null"}
		require.NoError(t, c.Complete())
		require.Equal(t, "/var/run/nri/nri.sock", c.NRISocketPath)
		require.Equal(t, 30*time.Second, c.Timeout)
	})
}

func TestConfigNamespace(t *testing.T) {
	t.Run("should default namespace to nri-test", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null"}
		require.NoError(t, c.Complete())
		require.Equal(t, DefaultNamespace, c.Namespace)
	})

	t.Run("should keep an explicitly given namespace", func(t *testing.T) {
		c := &Config{RuntimeEndpoint: "unix:///dev/null", Namespace: "custom-ns"}
		require.NoError(t, c.Complete())
		require.Equal(t, "custom-ns", c.Namespace)
	})
}

func TestStopGrace(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.Complete())
	require.Zero(t, c.StopGrace, "an explicit zero must stay zero: it means kill at once")

	c = &Config{StopGrace: 5 * time.Second}
	require.NoError(t, c.Complete())
	require.Equal(t, int64(5), c.StopGraceSeconds())

	require.Error(t, (&Config{StopGrace: -time.Second}).Complete())
}

func TestProcView(t *testing.T) {
	c := &Config{}
	require.NoError(t, c.Complete())
	require.Equal(t, ProcViewSelf, c.ProcView)

	require.Error(t, (&Config{ProcView: "nonsense"}).Complete())
}
func TestCompleteLeavesTheNamespaceAloneOutsideScalabilityMode(t *testing.T) {
	c := &Config{RunID: "abc123", Namespace: "mine", endpointCandidates: []string{}}

	_ = c.Complete()

	require.Equal(t, "mine", c.Namespace)
}

// One direction of one burst gets its own budget. The CRI burst used to create
// grace wait had already spent part of it, so a slow burst left the teardown
// calls with an expired context -- they failed instantly, their errors were
// discarded, and the teardown report was noise shaped like data.
func TestCompleteAcceptsTheMockDriver(t *testing.T) {
	c := &Config{RuntimeEndpoint: "unix:///dev/null", Driver: DriverMock}

	require.NoError(t, c.Complete())
	require.Equal(t, DriverMock, c.Driver)
}
func TestCompleteRefusesAnUnknownDriver(t *testing.T) {
	c := &Config{RuntimeEndpoint: "unix:///dev/null", Driver: "pretend"}

	err := c.Complete()
	require.Error(t, err)
	require.ErrorContains(t, err, "pretend")
	require.ErrorContains(t, err, DriverMock, "the message has to name the drivers that exist")
}
