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

package suite

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/test/nri-test/driver/k8s"
	"github.com/containerd/nri/test/nri-test/framework"
)

// The ReplicaSet scalability burst. Less precise than the CRI one about when
// the burst began, because kubelet decides that and we only see the NRI
// callbacks, but it is the realistic path: the control plane and kubelet are
// inside the window, which is what a user actually waits for.
//
// Scale-up is 1 -> N and scale-down is N -> 0, so the teardown measurement
// covers every pod the startup measurement saw. That correlation is worth
// having: it says whether the pods that were slow to start are also the ones
// slow to stop.

// runK8sBurst scales a ReplicaSet up and down, measuring both.
func runK8sBurst(t *testing.T, iteration int) {
	cfg := framework.TestConfig
	n := cfg.ScaleReplicas

	scaler, err := k8s.NewScaler(cfg, cfg.Kubeconfig)
	require.NoError(t, err, "failed to reach the cluster")

	// The plugins' scope filter keys on the pod's namespace, which for a
	// Kubernetes pod is its Kubernetes namespace. Config.Complete points the
	// run's namespace at the Scaler's for exactly this reason, so a mismatch
	// here means that wiring broke rather than that the operator did something
	// wrong.
	require.Equal(t, scaler.Namespace(), cfg.Namespace,
		"the run's namespace must be the Scaler's, or the recorder sees nothing")

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ScaleBudget())
	defer cancel()

	require.NoError(t, scaler.EnsureNamespace(ctx))

	t.Cleanup(func() {
		if cfg.KeepArtifacts {
			t.Logf("keeping namespace %s and its ReplicaSet", scaler.Namespace())

			return
		}

		// Its own budget: the measurement's context may be spent, and a
		// ReplicaSet left behind keeps recreating pods.
		cctx, ccancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer ccancel()

		if err := scaler.Cleanup(cctx); err != nil {
			t.Errorf("failed to clean up the scalability workload: %v", err)
		}
	})

	require.NoError(t, scaler.Create(ctx, 1), "failed to create the ReplicaSet")

	// Wait for the first replica before scaling. It pulls the image, so the
	// burst measures scheduling rather than a registry round trip, and it
	// proves the pod template works before N of them fail the same way.
	require.NoError(t, scaler.WaitRunning(ctx, 1, cfg.ScaleSettle, time.Second),
		"the first replica never reached Running, so the burst was not attempted")

	before, err := scaler.PodNames(ctx)
	require.NoError(t, err)

	s := framework.NewSuite(t, "scale-k8s")

	q := s.Q()
	q.Grow(n * scaleEventsPerPod)
	q.TrackCost(true, n*scaleEventsPerPod)

	startScalePlugins(t, s)

	require.NoError(t, scaler.Scale(ctx, int32(n)), "failed to scale up")

	newPods := waitForNewPods(t, ctx, scaler, q, before, n-len(before),
		cfg.ScaleTerminal())

	up := framework.NewPhaseTimeline(cfg, q.Events(), newPods, framework.StartupPhase())
	up.RecorderCost = q.Costs()

	upReport := framework.NewScaleReport(iteration, n, up)
	upReport.Print(os.Stdout)

	// Down to zero, so the teardown covers every pod including the original.
	// The pods measured on the way up are a subset, so a reader can line the
	// two reports up pod by pod.
	all, err := scaler.PodNames(ctx)
	require.NoError(t, err)

	require.NoError(t, scaler.Scale(ctx, 0), "failed to scale down")

	waitForStage(q, all, framework.RemovePodSandbox, len(all), cfg.ScaleSettle)

	down := framework.NewPhaseTimeline(cfg, q.Events(), all, framework.TeardownPhase())
	downReport := framework.NewScaleReport(iteration, n, down)
	downReport.Print(os.Stdout)

	if cfg.ScaleReportPath != "" {
		writeScaleReports(t, cfg.ScaleReportPath, iteration, upReport, downReport)
	}

	require.NotEmpty(t, newPods, "the burst produced no new pods to measure")
	require.Equal(t, len(newPods), upReport.Started,
		"every new pod needs a RunPodSandbox callback, or the recorder missed "+
			"events and the burst is measured over an incomplete set")

	if upReport.Finished < upReport.Started {
		t.Logf("WARNING: %d of %d pods reached %s within %s; the burst is a "+
			"lower bound. Raise -nri-test.scale-settle to close the window.",
			upReport.Finished, upReport.Started, up.Terminal, cfg.ScaleSettle)
	}
}

// waitForNewPods waits until wantNew pods have appeared and reached the given
// stage, and returns their names.
//
// It polls the event queue as well as the API server: the events are what the
// measurement is made of, so the window closes when the data is complete
// rather than when Kubernetes says the pods are ready.
func waitForNewPods(t *testing.T, ctx context.Context, scaler *k8s.Scaler,
	q *framework.EventQ, before []string, wantNew int,
	stage framework.EventType) []string {
	t.Helper()

	deadline := time.Now().Add(framework.TestConfig.ScaleSettle)

	var newPods []string

	for {
		names, err := scaler.PodNames(ctx)
		require.NoError(t, err)

		newPods = newPods[:0]

		for _, n := range names {
			if !containsString(before, n) {
				newPods = append(newPods, n)
			}
		}

		enough := len(newPods) >= wantNew &&
			countReached(q, newPods, stage) >= wantNew

		if enough || time.Now().After(deadline) {
			out := make([]string, len(newPods))
			copy(out, newPods)

			return out
		}

		select {
		case <-ctx.Done():
			out := make([]string, len(newPods))
			copy(out, newPods)

			return out
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}

	return false
}

// writeScaleReports writes the raw per-pod tables as JSON, so a distribution
// can be plotted without re-running the burst. Percentiles hide bimodality,
// and a burst is where bimodality shows up.
func writeScaleReports(t *testing.T, path string, iteration int,
	reports ...*framework.ScaleReport) {
	t.Helper()

	type row struct {
		Pod    string            `json:"pod"`
		PodID  string            `json:"podID"`
		Stages map[string]string `json:"stages"`
	}

	type phase struct {
		Phase    string   `json:"phase"`
		Terminal string   `json:"terminal"`
		Burst    string   `json:"burst"`
		Started  int      `json:"started"`
		Reached  int      `json:"reached"`
		Lost     int      `json:"lost"`
		Pods     []row    `json:"pods"`
		Cost     []string `json:"recorderQueueCost,omitempty"`
	}

	out := struct {
		Iteration int      `json:"iteration"`
		Replicas  int      `json:"replicas"`
		Driver    string   `json:"driver"`
		Plugins   []string `json:"plugins"`
		Phases    []phase  `json:"phases"`
	}{
		Iteration: iteration,
		Replicas:  framework.TestConfig.ScaleReplicas,
		Driver:    framework.TestConfig.ScaleDriver,
		Plugins:   framework.TestConfig.ScalePlan.Describe(),
	}

	for _, r := range reports {
		ph := phase{
			Phase:    r.Timeline.Phase.Name,
			Terminal: string(r.Timeline.Terminal),
			Burst:    r.Burst.String(),
			Started:  r.Started,
			Reached:  r.Finished,
			Lost:     r.Replicas - len(r.Timeline.Pods),
		}

		for _, p := range r.Timeline.Pods {
			stages := map[string]string{}

			for k, v := range p.At {
				// RFC3339 with nanoseconds: the gaps being measured are
				// sub-millisecond at the low end, and a truncated timestamp
				// would quietly round them away.
				stages[string(k)] = v.Format(time.RFC3339Nano)
			}

			ph.Pods = append(ph.Pods, row{Pod: p.PodName, PodID: p.PodID, Stages: stages})
		}

		for _, d := range r.Timeline.RecorderCost {
			ph.Cost = append(ph.Cost, d.String())
		}

		out.Phases = append(out.Phases, ph)
	}

	blob, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)

	name := framework.ScaleReportPath(path, iteration)

	require.NoError(t, os.WriteFile(name, blob, 0o644),
		"failed to write the scalability report")

	t.Logf("wrote the raw per-pod tables to %s", name)
}
