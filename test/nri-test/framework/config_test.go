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

func TestScaleTerminalDefaultsToTheLaterEvent(t *testing.T) {
	require.Equal(t, PostStartContainer, (&Config{}).ScaleTerminal(),
		"the headline reading of a burst is the last event")
	require.Equal(t, StartContainer,
		(&Config{ScaleTerminalEvent: string(StartContainer)}).ScaleTerminal())
	require.Equal(t, PostStartContainer,
		(&Config{ScaleTerminalEvent: "nonsense"}).ScaleTerminal(),
		"a Config that never went through Complete falls back rather than "+
			"measuring to nothing; Complete itself refuses the value, see "+
			"TestCompleteRefusesAnUnknownScaleTerminal")
}

func TestCompleteRefusesAnUnknownScaleTerminal(t *testing.T) {
	c := &Config{RuntimeEndpoint: "unix:///dev/null", ScaleTerminalEvent: "nonsense"}

	err := c.Complete()

	require.Error(t, err, "a typo must not silently measure to another stage")
	require.ErrorContains(t, err, "nonsense")
	require.ErrorContains(t, err, string(StartContainer))
	require.ErrorContains(t, err, string(PostStartContainer))
}

func TestCompleteAcceptsBothScaleTerminals(t *testing.T) {
	for _, stage := range []EventType{StartContainer, PostStartContainer} {
		c := &Config{RuntimeEndpoint: "unix:///dev/null", ScaleTerminalEvent: string(stage)}
		require.NoError(t, c.Complete())
		require.Equal(t, stage, c.ScaleTerminal())
	}

	c := &Config{RuntimeEndpoint: "unix:///dev/null"}
	require.NoError(t, c.Complete(), "an unset terminal is the default, not an error")
	require.Equal(t, PostStartContainer, c.ScaleTerminal())
}

func TestCompleteOverridesTheNamespaceForAReplicaSetBurst(t *testing.T) {
	c := &Config{
		RunID:              "abc123",
		Namespace:          "ignored-by-design",
		ScaleReplicas:      5,
		ScaleDriver:        ScaleDriverK8s,
		endpointCandidates: []string{},
	}

	// detectRuntimeEndpoint will fail with no candidates, which is fine: the
	// namespace is set before that and is what this pins.
	_ = c.Complete()

	require.Equal(t, ScaleNamespace("abc123"), c.Namespace,
		"the plugins filter on this, so it must be the ReplicaSet's namespace")
	require.Equal(t, "nri-test-scale-abc123", c.Namespace)
}

func TestCompleteLeavesTheNamespaceAloneOutsideScalabilityMode(t *testing.T) {
	c := &Config{RunID: "abc123", Namespace: "mine", endpointCandidates: []string{}}

	_ = c.Complete()

	require.Equal(t, "mine", c.Namespace)
}

func TestScaleBudgetGrowsWithIterations(t *testing.T) {
	one := (&Config{ScaleIterations: 1, ScaleSettle: time.Minute}).ScaleBudget()
	three := (&Config{ScaleIterations: 3, ScaleSettle: time.Minute}).ScaleBudget()

	require.Equal(t, 2*time.Minute, one, "settle time for the burst and the scale-down")
	require.Equal(t, 6*time.Minute, three)
	require.Equal(t, 2*time.Minute, (&Config{ScaleSettle: time.Minute}).ScaleBudget(),
		"an unset iteration count counts as one rather than zero")
}

func TestCompleteLeavesTheNamespaceAloneForACRIBurst(t *testing.T) {
	c := &Config{
		RunID:              "abc123",
		Namespace:          "mine",
		ScaleReplicas:      5,
		ScaleDriver:        ScaleDriverCRI,
		endpointCandidates: []string{},
	}

	_ = c.Complete()

	require.Equal(t, "mine", c.Namespace,
		"a CRI burst creates its own sandboxes in the suite's namespace, so the "+
			"stale sweep and the leak guard keep covering them")
}

func TestCompleteRefusesAnUnknownScaleDriver(t *testing.T) {
	c := &Config{RunID: "abc123", ScaleReplicas: 5, ScaleDriver: "kubernetes"}

	err := c.Complete()

	require.Error(t, err, "a typo must not silently pick a driver")
	require.ErrorContains(t, err, "kubernetes")
	require.ErrorContains(t, err, ScaleDriverCRI)
	require.ErrorContains(t, err, ScaleDriverK8s)
}

func TestCompleteDefaultsTheScaleDriverToCRI(t *testing.T) {
	c := &Config{RunID: "abc123", endpointCandidates: []string{}}

	_ = c.Complete()

	require.Equal(t, ScaleDriverCRI, c.ScaleDriver)
	require.False(t, c.ScaleViaK8s(), "not scaling at all is not scaling via k8s")
}

func TestScaleViaK8sNeedsBothFlags(t *testing.T) {
	require.False(t, (&Config{ScaleDriver: ScaleDriverK8s}).ScaleViaK8s(),
		"a driver choice without replicas is not a scalability run")
	require.True(t, (&Config{ScaleReplicas: 2, ScaleDriver: ScaleDriverK8s}).ScaleViaK8s())
	require.False(t, (&Config{ScaleReplicas: 2, ScaleDriver: ScaleDriverCRI}).ScaleViaK8s())
}

// One direction of one burst gets its own budget. The CRI burst creates a
// context per phase rather than sharing one: the grace wait between them
// spends part of the first, so a shared context would leave the teardown calls
// expired, failing the moment they were issued.
func TestScalePhaseBudgetIsNeverZero(t *testing.T) {
	require.Equal(t, DefaultScaleSettle, (&Config{}).ScalePhaseBudget(),
		"a zero budget is an already-expired context, which is the failure "+
			"a per-phase budget exists to prevent")
	require.Equal(t, time.Minute, (&Config{ScaleSettle: time.Minute}).ScalePhaseBudget())
}

func TestScaleBudgetIsTwoPhasesPerIteration(t *testing.T) {
	for _, c := range []*Config{
		{ScaleSettle: time.Minute},
		{ScaleSettle: 90 * time.Second, ScaleIterations: 3},
		{},
	} {
		iterations := max(c.ScaleIterations, 1)

		require.Equal(t, time.Duration(2*iterations)*c.ScalePhaseBudget(), c.ScaleBudget(),
			"the whole-run budget must stay the sum of the per-phase ones, or "+
				"the two drift and one of them is wrong")
	}
}

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

// A burst against an in-process mock would measure Go calls, not a runtime,
// and a number produced that way would be quoted as if it meant something.
func TestCompleteRefusesScalingWithTheMockDriver(t *testing.T) {
	c := &Config{RuntimeEndpoint: "unix:///dev/null", Driver: DriverMock, ScaleReplicas: 5}

	err := c.Complete()
	require.Error(t, err)
	require.ErrorContains(t, err, "scalability")
}
