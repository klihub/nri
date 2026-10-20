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
	"crypto/rand"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// The scalability measurement: a burst of pods created at once, and then torn
// down at once, with a recorder plugin timestamping every NRI callback either
// produces.
//
// Two drivers, measuring different things:
//
//   - cri  drives the runtime directly. We issue every call, so each operation
//     has a client-side start and end as well as an NRI callback, and the
//     runtime's work either side of the hook can be separated. It also fixes
//     the burst's shape, since we choose the concurrency.
//   - k8s  scales a ReplicaSet. Less precise about when the burst began,
//     because kubelet decides that, but it is the realistic path and has the
//     control plane and kubelet inside the window.
//
// Both are opt-in on -nri-test.scale-replicas, so an ordinary conformance run
// measures nothing here. They have opposite requirements about kubelet, which
// framework.Config.ScaleViaK8s explains and framework.Setup enforces.
//
// Both directions are measured. A scale-up says how long a burst takes to come
// up; a scale-down says how long it takes to go away, and the teardown events
// are ones the recorder sees anyway.

// scaleEventsPerPod is how many recorded stages a single-container pod
// produces across both directions: five on the way up, four on the way down.
// Used only to size the queue before the burst, so the recorder does not grow
// a slice inside the window it is measuring.
const scaleEventsPerPod = 10

// scaleMask is what the recorder subscribes to: every startup and teardown
// stage, and nothing else. A recorder that could adjust would be measuring its
// own adjustment path rather than the runtime's scheduling.
var scaleMask = recorderMask()

func recorderMask() api.EventMask {
	names := make([]string, 0, len(framework.RecorderEvents))
	for _, ev := range framework.RecorderEvents {
		names = append(names, string(ev))
	}

	return api.MustParseEventMask(names...)
}

// eventSource is where a burst's NRI events come from.
type eventSource interface {
	Events() []*framework.Event
}

// scaleSource is a run's instrument: either the recorder plugin's queue, or the
// runtime's own OpenTelemetry spans.
type scaleSource struct {
	q    *framework.EventQ
	recv *framework.TraceReceiver
	// known maps sandbox ids to pod names for attributing spans, filled in as
	// the burst creates pods.
	mu    sync.Mutex
	known map[string]string
	// traces maps the ids of traces started by the client to pod names.
	traces map[string]string
}

func (s *scaleSource) traced() bool { return s.recv != nil }

// Events returns every event so far. For spans that is rebuilt from what the
// receiver holds, since attribution can only be settled once a pod's spans
// have all arrived.
func (s *scaleSource) Events() []*framework.Event {
	return s.hooks().Events
}

func (s *scaleSource) hooks() *framework.HookTrace {
	if s.recv == nil {
		return &framework.HookTrace{Events: s.q.Events()}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return framework.NewHookTrace(s.recv.Spans(), s.known, s.traces)
}

// ctxFor returns ctx carrying a trace context of its own for a call about the
// named pod, which the runtime's spans for the call then join. This is how
// hooks are attributed to pods for runtimes whose spans carry no ids.
func (s *scaleSource) ctxFor(ctx context.Context, pod string) context.Context {
	if s.recv == nil {
		return ctx
	}

	var sc trace.SpanContext

	tid, sid := make([]byte, 16), make([]byte, 8)
	_, _ = rand.Read(tid)
	_, _ = rand.Read(sid)

	sc = trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID(tid),
		SpanID:     trace.SpanID(sid),
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})

	s.mu.Lock()
	s.traces[sc.TraceID().String()] = pod
	s.mu.Unlock()

	return trace.ContextWithSpanContext(ctx, sc)
}

// learn tells the source a sandbox id's pod name.
func (s *scaleSource) learn(id, name string) {
	if s.recv == nil || id == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.known[id] = name
}

// costs returns the recorder's own queue costs, of which spans have none.
func (s *scaleSource) costs() []time.Duration {
	if s.q == nil {
		return nil
	}

	return s.q.Costs()
}

// grace is how long to wait for the trailing events of a burst. The runtime
// exports spans in batches, so they arrive well after the calls they describe.
func (s *scaleSource) grace() time.Duration {
	if s.recv != nil {
		return traceGrace
	}

	return 5 * time.Second
}

// traceGrace allows for the exporter's batch interval, 5s by default, with room
// to spare.
const traceGrace = 30 * time.Second

// summarize says what the receiver got, so a run that attributed nothing can
// be told apart from one that received nothing, and writes the spans out when
// asked.
func (s *scaleSource) summarize(t *testing.T) {
	t.Helper()

	// Waited for rather than merely checked. The exporter batches, so "no
	// spans yet" and "no spans ever" look the same at any single instant, and
	// the second is the one worth a message of its own: every other failure a
	// traced run can produce reads as the measurement having gone wrong rather
	// than as the runtime never having been configured to export.
	err := s.recv.WaitSpans(traceGrace)
	require.NoError(t, err, "no spans arrived within %s, so the runtime is not "+
		"exporting to %s; see the README on measuring from the runtime's traces",
		traceGrace, s.recv.Addr())

	spans := s.recv.Spans()
	ht := s.hooks()

	t.Logf("received %d spans, %d NRI hook calls attributed to a pod and %d that were not",
		len(spans), len(ht.Calls), len(ht.Orphans))

	if path := framework.TestConfig.ScaleTraceDump; path != "" {
		require.NoError(t, s.recv.WriteSpans(path), "failed to write the spans")
		t.Logf("wrote the spans to %s", path)
	}
}

// report builds a burst's report from the source, adding the runtime's own
// per-hook timings when measuring from spans.
func (s *scaleSource) report(iteration, n int, t *framework.Timeline, names []string) *framework.ScaleReport {
	r := framework.NewScaleReport(iteration, n, t)

	if s.traced() {
		r.Traced = true
		r.Hooks = s.hooks().HookStats(names, t.Phase.Stages)
	}

	return r
}

// startScalePlugins starts the plan's plugins and returns the source the burst
// is measured from.
//
// With a recorder, plugin 00 is the recorder and the rest are simulated; the
// simulated ones record into queues of their own, so the recorder's queue and
// the timelines built from it hold only the measured stages. When measuring
// from spans there is no recorder, and every plugin is a simulated one. NRI
// calls plugins in index order, so each one's delay adds to the burst.
func startScalePlugins(t *testing.T, s *framework.Suite) *scaleSource {
	t.Helper()

	cfg := framework.TestConfig
	plan := cfg.ScalePlan

	src := &scaleSource{known: map[string]string{}, traces: map[string]string{}}
	plugins := []*framework.TestPlugin{}
	first := 0

	if cfg.ScaleTrace != "" {
		recv, err := framework.NewTraceReceiver(cfg.ScaleTraceProtocol, cfg.ScaleTrace)
		require.NoError(t, err)

		t.Cleanup(recv.Close)
		t.Logf("measuring from the runtime's spans, received over OTLP/%s on %s",
			cfg.ScaleTraceProtocol, recv.Addr())

		src.recv = recv
	} else {
		// No SimulateWork for the recorder: a plan cannot carry a delay for
		// it, so installing one could only ever be a no-op.
		recorder := s.NewPlugin("scale-recorder", "00", scaleMask)

		plugins = append(plugins, recorder)
		src.q = s.Q()
		src.q.Grow(cfg.ScaleReplicas * scaleEventsPerPod)
		src.q.TrackCost(true, cfg.ScaleReplicas*scaleEventsPerPod)
		first = 1
	}

	for i := first; i < plan.Plugins; i++ {
		mask, err := plan.Mask(i)
		require.NoError(t, err)

		p := framework.NewTestPlugin(fmt.Sprintf("scale-sim%02d", i), fmt.Sprintf("%02d", i),
			mask, &framework.EventQ{}, s.Scope())
		plan.SimulateWork(p, i)

		plugins = append(plugins, p)
	}

	for _, line := range plan.Describe() {
		t.Log(line)
	}

	s.StartPlugins(plugins...)

	return src
}

func TestScalability(t *testing.T) {
	cfg := framework.TestConfig

	if !cfg.Scaling() {
		framework.Skipped(t, "requires -nri-test.scale-replicas; an ordinary "+
			"conformance run does not measure scalability")
	}

	require.Greater(t, cfg.ScaleReplicas, 1,
		"a burst needs at least two pods to measure anything")

	for i := 1; i <= cfg.ScaleIterations; i++ {
		if cfg.ScaleViaK8s() {
			t.Run("replicaset", func(t *testing.T) { runK8sBurst(t, i) })

			continue
		}

		t.Run("cri", func(t *testing.T) { runCRIBurst(t, i) })
	}
}

// runCRIBurst creates the burst over CRI, measures it coming up and going
// down, and reports.
func runCRIBurst(t *testing.T, iteration int) {
	cfg := framework.TestConfig
	n := cfg.ScaleReplicas

	s := framework.NewSuite(t, "scale-cri")
	framework.RequireCaps(t, framework.CapContainerLifecycle)

	q := startScalePlugins(t, s)

	// Specs are built up front, serially, so the burst itself does nothing but
	// issue calls. PodSpec stamps the run id and the case label, so the
	// stale-workload sweep and the leak guard cover these pods like any other.
	specs := make([]*framework.PodSpec, 0, n)
	for i := 0; i < n; i++ {
		specs = append(specs, s.PodSpec("burst"))
	}

	ops := framework.NewOpLog(n * 3)

	// What the burst created, so teardown measures the same set and nothing is
	// left behind if a later step fails.
	var (
		mu    sync.Mutex
		pods  []*framework.Pod
		ctrs  []*framework.Container
		names []string
	)

	t.Cleanup(func() {
		if cfg.KeepArtifacts {
			return
		}

		// Best effort, with its own budget: the measurement's context may be
		// spent, and a pod left behind here is a pod the next run reports as a
		// stale peer rather than removes.
		cctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()

		for _, p := range pods {
			_ = s.Driver().StopPod(cctx, p)
			_ = s.Driver().RemovePod(cctx, p)
		}
	})

	// The burst. Every pod gets its own goroutine so the calls land together:
	// the point is to put the runtime under simultaneous load, and pacing them
	// from the client would measure the pacing.
	//
	// Driver calls are used directly rather than through Suite's wrappers,
	// because those end the test through require, and testify's FailNow has to
	// run on the test's own goroutine. Errors come back through the OpLog and
	// are asserted once the burst is over.
	// The startup's own budget. The teardown gets a second one below rather
	// than sharing this: the grace wait between them spends part of this one,
	// so a burst that ran long would hand the teardown an already-expired
	// context, every teardown call would fail the moment it was issued, and
	// the teardown report would be of a phase that never happened.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ScalePhaseBudget())
	defer cancel()

	var wg sync.WaitGroup

	for _, spec := range specs {
		wg.Add(1)

		go func(spec *framework.PodSpec) {
			defer wg.Done()

			var pod *framework.Pod

			err := ops.Time(&framework.OpRecord{
				Op: framework.RunPodSandbox, PodName: spec.Name,
			}, func() error {
				var err error
				pod, err = s.Driver().CreatePod(q.ctxFor(ctx, spec.Name), spec)

				return err
			})

			if pod != nil {
				q.learn(pod.ID, spec.Name)

				mu.Lock()
				pods = append(pods, pod)
				names = append(names, spec.Name)
				mu.Unlock()
			}

			if err != nil || pod == nil {
				return
			}

			cspec := s.CtrSpec("burst")

			var ctr *framework.Container

			crec := &framework.OpRecord{
				Op: framework.CreateContainer, PodName: spec.Name,
				PodID: pod.ID, CtrName: cspec.Name,
			}

			err = ops.Time(crec, func() error {
				var err error
				ctr, err = s.Driver().CreateContainer(q.ctxFor(ctx, spec.Name), pod, cspec)

				return err
			})

			// The id only exists once the call has returned, and CorrelateOps
			// keys container operations on it, so filling it in afterwards is
			// what lets this call be matched to its callback at all. Safe to
			// write here: the records are read after the burst's goroutines
			// have joined.
			if ctr != nil {
				crec.CtrID = ctr.ID
			}

			if err != nil || ctr == nil {
				return
			}

			mu.Lock()
			ctrs = append(ctrs, ctr)
			mu.Unlock()

			_ = ops.Time(&framework.OpRecord{
				Op: framework.StartContainer, PodName: spec.Name,
				PodID: pod.ID, CtrName: cspec.Name, CtrID: ctr.ID,
			}, func() error {
				return s.Driver().StartContainer(q.ctxFor(ctx, spec.Name), ctr)
			})
		}(spec)
	}

	wg.Wait()

	// The client knows when it finished, so there is nothing to wait for: the
	// hooks are synchronous, so every callback this burst will produce has
	// already been recorded by the time the last call returned. The one
	// exception is PostStartContainer, which is a notification and may still
	// be in flight.
	// The stage the run asked to be measured to, not a fixed one: waiting for
	// an event the measurement does not use would spend the grace on nothing.
	waitForStage(q, names, cfg.ScaleTerminal(), len(names), q.grace())

	events := q.Events()

	up := framework.NewPhaseTimeline(cfg, events, names, framework.StartupPhase())
	up.RecorderCost = q.costs()

	upReport := q.report(iteration, n, up, names)
	upReport.OpStats = framework.CorrelateOps(ops.Records(), events)
	upReport.ClientBurst = framework.ClientBurst(ops.Records(), up)
	upReport.Print(os.Stdout)

	// Teardown, measured the same way, and it is also the cleanup: the pods
	// this burst created are stopped and removed together, so the measurement
	// and the tidy-up are the same act rather than two.
	downOps := framework.NewOpLog(len(pods) * 2)

	downCtx, downCancel := context.WithTimeout(context.Background(), cfg.ScalePhaseBudget())
	defer downCancel()

	tearDownCRIBurst(t, s, q, pods, downOps, downCtx)

	waitForStage(q, names, framework.RemovePodSandbox, len(names), q.grace())

	events = q.Events()

	down := framework.NewPhaseTimeline(cfg, events, names, framework.TeardownPhase())
	downReport := q.report(iteration, n, down, names)
	downReport.OpStats = framework.CorrelateOps(downOps.Records(), events)
	downReport.ClientBurst = framework.ClientBurst(downOps.Records(), down)
	downReport.Print(os.Stdout)

	if cfg.ScaleReportPath != "" {
		writeScaleReports(t, cfg.ScaleReportPath, iteration, upReport, downReport)
	}

	// The deliverable is the measurement, so the assertions are about whether
	// it is trustworthy rather than about a latency budget. A threshold would
	// be a number nobody has measured yet.
	// Both directions, through the same helper. The teardown's failures used
	// to be discarded entirely -- its calls are issued with `_ =` because a
	// burst goroutine cannot use require -- so a scale-down in which
	// StopPodSandbox or RemovePodSandbox failed still printed a teardown
	// report that read as a measurement.
	for _, phase := range []struct {
		name string
		ops  *framework.OpLog
	}{{"burst", ops}, {"teardown", downOps}} {
		for _, msg := range phase.ops.FailureReports(phase.name) {
			t.Errorf("%s", msg)
		}
	}

	// A pod whose RunPodSandbox failed is reported above and left out of the
	// measurement, so what is checked here is that every pod was attempted and
	// that the recorder saw every pod that was created.
	attempted := 0

	for _, r := range ops.Records() {
		if r.Op == framework.RunPodSandbox {
			attempted++
		}
	}

	require.Equal(t, n, attempted, "every pod of the burst must have been attempted")

	if q.traced() {
		q.summarize(t)
	}

	require.Equal(t, len(names), upReport.Started,
		"every pod needs a RunPodSandbox callback, or the recorder missed events "+
			"and the burst is measured over an incomplete set")
}

// tearDownCRIBurst stops and removes the burst's pods together, timing each
// call.
func tearDownCRIBurst(t *testing.T, s *framework.Suite, q *scaleSource, pods []*framework.Pod,
	ops *framework.OpLog, ctx context.Context) {
	t.Helper()

	var wg sync.WaitGroup

	for _, pod := range pods {
		wg.Add(1)

		go func(pod *framework.Pod) {
			defer wg.Done()

			// StopPod first, then RemovePod, which is the order a client has to
			// use. Both are timed: the NRI teardown events are spread across
			// them and which one carries which is a runtime's choice, which is
			// why the teardown phase does not assume an order.
			_ = ops.Time(&framework.OpRecord{
				Op: framework.StopPodSandbox, PodName: pod.Spec.Name, PodID: pod.ID,
			}, func() error {
				return s.Driver().StopPod(q.ctxFor(ctx, pod.Spec.Name), pod)
			})

			_ = ops.Time(&framework.OpRecord{
				Op: framework.RemovePodSandbox, PodName: pod.Spec.Name, PodID: pod.ID,
			}, func() error {
				return s.Driver().RemovePod(q.ctxFor(ctx, pod.Spec.Name), pod)
			})
		}(pod)
	}

	wg.Wait()
}

// waitForStage waits until want of the named pods have recorded the given
// stage, or the timeout expires.
//
// For a CRI burst only the trailing notifications need this, since the hooks
// are synchronous. For a ReplicaSet burst it is how the window closes at all.
func waitForStage(q eventSource, names []string, stage framework.EventType,
	want int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	for {
		if countReached(q, names, stage) >= want || time.Now().After(deadline) {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// countReached returns how many of the named pods have an event of the given
// type recorded.
func countReached(q eventSource, pods []string, e framework.EventType) int {
	seen := map[string]struct{}{}

	for _, ev := range q.Events() {
		if ev.Type != e || ev.PodName == "" {
			continue
		}

		if slices.Contains(pods, ev.PodName) {
			seen[ev.PodName] = struct{}{}
		}
	}

	return len(seen)
}
