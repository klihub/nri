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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// base is an arbitrary fixed instant. Using a fixed one rather than
// time.Now() keeps every expected duration in these tests a literal, so a
// wrong answer is a wrong literal rather than a flake.
var base = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// at returns base plus ms milliseconds.
func at(ms int) time.Time {
	return base.Add(time.Duration(ms) * time.Millisecond)
}

// ev builds one recorded event.
func ev(t EventType, pod string, ms int) *Event {
	return &Event{Type: t, PodName: pod, PodID: pod + "-id", At: at(ms)}
}

// podEvents returns a full five-stage sequence for one pod, offset by start
// and spaced by step milliseconds.
func podEvents(pod string, start, step int) []*Event {
	out := make([]*Event, 0, len(scaleStages))
	for i, s := range scaleStages {
		out = append(out, ev(s, pod, start+i*step))
	}

	return out
}

func TestClientBurstStartsAtTheFirstCall(t *testing.T) {
	tl := &Timeline{
		Terminal: PostStartContainer,
		Pods: []*PodTimeline{
			{PodName: "a", At: map[EventType]time.Time{PostStartContainer: at(900)}},
			{PodName: "b", At: map[EventType]time.Time{PostStartContainer: at(1200)}},
		},
	}
	ops := []*OpRecord{
		{Op: RunPodSandbox, Start: at(300)},
		{Op: RunPodSandbox, Start: at(100)},
	}

	require.Equal(t, 1100*time.Millisecond, ClientBurst(ops, tl))
}

func TestClientBurstIsZeroWithoutCallsOrArrivals(t *testing.T) {
	tl := &Timeline{Terminal: PostStartContainer, Pods: []*PodTimeline{{At: map[EventType]time.Time{}}}}

	require.Zero(t, ClientBurst(nil, tl))
	require.Zero(t, ClientBurst([]*OpRecord{{Start: time.Now()}}, tl))
}

func TestNewTimelineGroupsByPodAndKeepsFirstTimestamp(t *testing.T) {
	events := []*Event{
		ev(RunPodSandbox, "a", 100),
		ev(RunPodSandbox, "a", 150), // a retry: the first must win
		ev(CreateContainer, "a", 200),
		ev(RunPodSandbox, "b", 120),
	}

	tl := NewTimeline(events, []string{"a", "b"}, StartupPhase(), StartContainer)

	require.Len(t, tl.Pods, 2)
	require.Equal(t, "a", tl.Pods[0].PodName, "pods are in first-seen order")
	require.Equal(t, "a-id", tl.Pods[0].PodID)

	got, ok := tl.Pods[0].Stage(RunPodSandbox)
	require.True(t, ok)
	require.Equal(t, at(100), got, "a repeated stage keeps its first timestamp")
}

func TestNewTimelineIgnoresForeignAndIrrelevantEvents(t *testing.T) {
	events := []*Event{
		ev(RunPodSandbox, "mine", 100),
		ev(RunPodSandbox, "someone-elses", 100),
		ev(StopContainer, "mine", 200), // not a scale stage
		{Type: RunPodSandbox, PodName: "", At: at(100)},
		nil,
	}

	tl := NewTimeline(events, []string{"mine"}, StartupPhase(), StartContainer)

	require.Len(t, tl.Pods, 1, "only the named pod is counted")
	require.Equal(t, "mine", tl.Pods[0].PodName)
	require.Len(t, tl.Pods[0].At, 1, "a non-stage event is not recorded as a stage")
}

func TestPodTimelineStageReportsAMissingStage(t *testing.T) {
	tl := NewTimeline([]*Event{ev(RunPodSandbox, "a", 100)}, []string{"a"}, StartupPhase(), StartContainer)
	p := tl.Pods[0]

	_, ok := p.Stage(StartContainer)
	require.False(t, ok, "a stage that never fired is absent, not zero")

	_, ok = p.Total(StartupPhase(), StartContainer)
	require.False(t, ok, "a total needs both ends")
}

func TestBurstSpansFirstStartToLastTerminal(t *testing.T) {
	// Three pods, deliberately interleaved and out of order in the slice, so
	// the burst cannot come from the slice's order.
	var events []*Event
	events = append(events, podEvents("c", 300, 10)...)
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, podEvents("b", 200, 10)...)

	tl := NewTimeline(events, []string{"a", "b", "c"}, StartupPhase(), PostStartContainer)

	d, started, finished := tl.Burst()

	require.Equal(t, 3, started)
	require.Equal(t, 3, finished)
	// a starts at 100; c's PostStartContainer is at 300 + 4*10 = 340.
	require.Equal(t, 240*time.Millisecond, d)
}

func TestBurstCountsStartedAndFinishedSeparately(t *testing.T) {
	// Two pods complete, one only starts: the window closed early on it.
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, podEvents("b", 200, 10)...)
	events = append(events, ev(RunPodSandbox, "slow", 250))

	tl := NewTimeline(events, []string{"a", "b", "slow"}, StartupPhase(), PostStartContainer)

	d, started, finished := tl.Burst()

	require.Equal(t, 3, started, "the slow pod started")
	require.Equal(t, 2, finished, "the slow pod did not finish")
	require.Equal(t, 140*time.Millisecond, d, "the burst ends at the last finisher")
}

func TestBurstIsZeroWhenNothingFinished(t *testing.T) {
	tl := NewTimeline([]*Event{ev(RunPodSandbox, "a", 100)}, []string{"a"}, StartupPhase(), StartContainer)

	d, started, finished := tl.Burst()

	require.Zero(t, d)
	require.Equal(t, 1, started)
	require.Equal(t, 0, finished)
}

func TestSamplesSkipsPodsMissingAnEnd(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, ev(RunPodSandbox, "b", 200)) // no CreateContainer

	tl := NewTimeline(events, []string{"a", "b"}, StartupPhase(), StartContainer)

	samples, skipped := tl.Samples(RunPodSandbox, CreateContainer)

	require.Equal(t, []time.Duration{10 * time.Millisecond}, samples)
	require.Equal(t, 1, skipped, "the incomplete pod is counted, not dropped silently")
}

func TestSpreadIsGapsBetweenConsecutiveArrivals(t *testing.T) {
	events := []*Event{
		ev(RunPodSandbox, "a", 100),
		ev(RunPodSandbox, "c", 400), // out of time order in the slice
		ev(RunPodSandbox, "b", 150),
	}

	tl := NewTimeline(events, []string{"a", "b", "c"}, StartupPhase(), StartContainer)

	gaps := tl.Spread(RunPodSandbox)

	require.Equal(t, []time.Duration{50 * time.Millisecond, 250 * time.Millisecond}, gaps,
		"gaps are in time order regardless of the slice's order")
}

func TestSpreadNeedsTwoArrivals(t *testing.T) {
	tl := NewTimeline([]*Event{ev(RunPodSandbox, "a", 100)}, []string{"a"}, StartupPhase(), StartContainer)

	require.Nil(t, tl.Spread(RunPodSandbox), "one arrival has no gap")
}

func TestNewStatOrderStatistics(t *testing.T) {
	// Ten samples, 10ms to 100ms, so every percentile has a literal answer
	// under nearest-rank: p50 is the 5th, p90 the 9th, p99 the 10th.
	var samples []time.Duration
	for i := 1; i <= 10; i++ {
		samples = append(samples, time.Duration(i*10)*time.Millisecond)
	}

	s := NewStat("x", samples)

	require.Equal(t, 10, s.N)
	require.Equal(t, 10*time.Millisecond, s.Min)
	require.Equal(t, 100*time.Millisecond, s.Max)
	require.Equal(t, 50*time.Millisecond, s.P50)
	require.Equal(t, 90*time.Millisecond, s.P90)
	require.Equal(t, 100*time.Millisecond, s.P99)
	require.Equal(t, 55*time.Millisecond, s.Mean)
}

func TestNewStatDoesNotReorderItsInput(t *testing.T) {
	samples := []time.Duration{3, 1, 2}

	NewStat("x", samples)

	require.Equal(t, []time.Duration{3, 1, 2}, samples,
		"the caller's slice is left alone, since the raw order is the arrival order")
}

func TestNewStatOnNoSamples(t *testing.T) {
	s := NewStat("x", nil)

	require.Equal(t, 0, s.N)
	require.Zero(t, s.Max, "nothing is reported rather than a misleading zero set")
}

func TestNewStatStdDevIsZeroForIdenticalSamples(t *testing.T) {
	s := NewStat("x", []time.Duration{5, 5, 5, 5})

	require.Equal(t, time.Duration(5), s.Mean)
	require.Zero(t, s.StdDev)
}

func TestNewHistogramBucketsByInclusiveUpperBound(t *testing.T) {
	bounds := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	samples := []time.Duration{
		5 * time.Millisecond,  // bucket 0
		10 * time.Millisecond, // bucket 0, bound is inclusive
		15 * time.Millisecond, // bucket 1
		20 * time.Millisecond, // bucket 1
		25 * time.Millisecond, // overflow
	}

	h := NewHistogram("x", samples, bounds)

	require.Len(t, h.Counts, len(bounds)+1, "one bucket per bound plus an overflow")
	require.Equal(t, []int{2, 2, 1}, h.Counts)
}

func TestNewHistogramCountsEverySample(t *testing.T) {
	samples := []time.Duration{
		time.Microsecond, time.Hour, 100 * time.Millisecond,
	}

	h := NewHistogram("x", samples, nil)

	require.Equal(t, len(samples), total(h.Counts),
		"no sample falls outside the buckets, however extreme")
}

func TestNewScaleReportSummarizesEveryTransition(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, podEvents("b", 200, 20)...)

	tl := NewTimeline(events, []string{"a", "b"}, StartupPhase(), PostStartContainer)
	tl.RecorderCost = []time.Duration{time.Microsecond, 2 * time.Microsecond}

	r := NewScaleReport(1, 2, tl)

	require.Equal(t, 1, r.Iteration)
	require.Equal(t, 2, r.Replicas)
	require.Equal(t, 2, r.Started)
	require.Equal(t, 2, r.Finished)

	names := make([]string, 0, len(r.Stats))
	for _, s := range r.Stats {
		names = append(names, s.Name)
	}

	// One offset per non-anchor stage, one per consecutive stage pair, one
	// arrival gap per stage, and the recorder's own cost. The anchor offset to
	// the terminal stage is the per-pod total, so there is no separate entry
	// for it.
	require.Contains(t, names, "anchor..PostStartContainer")
	require.Contains(t, names, "RunPodSandbox..CreateContainer")
	require.Contains(t, names, "StartContainer..PostStartContainer")
	require.Contains(t, names, "arrival gap RunPodSandbox")
	require.Contains(t, names, "recorder queue cost")
	require.Len(t, r.Hists, len(r.Stats), "every distribution is also bucketed")
}

func TestScaleReportPrintNamesTheIncompleteWindow(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, ev(RunPodSandbox, "slow", 200))

	tl := NewTimeline(events, []string{"a", "slow"}, StartupPhase(), PostStartContainer)
	r := NewScaleReport(2, 2, tl)

	var b strings.Builder
	r.Print(&b)
	out := b.String()

	require.Contains(t, out, "iteration 2")
	require.Contains(t, out, "NOTE:",
		"a window that closed early must say so, not just report a shorter burst")
	require.Contains(t, out, "2 pod(s) started and 1 finished",
		"both pods reached RunPodSandbox; only one reached the terminal stage")
}

func TestScaleReportPrintCarriesTheCaveats(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)

	tl := NewTimeline(events, []string{"a"}, StartupPhase(), PostStartContainer)

	var b strings.Builder
	NewScaleReport(1, 1, tl).Print(&b)
	out := b.String()

	// Each of these is a way the number can be misread. A report that prints
	// the duration without them says more than the measurement supports.
	require.Contains(t, out, "our plugin's callback ran")
	require.Contains(t, out, "One plugin is the floor")
	require.Contains(t, out, "Kubelet and the control plane are inside the window")
}

func TestScaleReportPrintClientBurstNoteNamesTheAnchorOnlyWhenThereIsOne(t *testing.T) {
	events := []*Event{ev(StopPodSandbox, "a", 100), ev(RemovePodSandbox, "a", 150)}

	down := NewScaleReport(1, 1, NewTimeline(events, []string{"a"}, TeardownPhase(), ""))
	down.ClientBurst = time.Second

	var b strings.Builder
	down.Print(&b)
	require.NotContains(t, b.String(), "first  callback",
		"a phase with no anchor stage has no stage to name")
	require.Contains(t, b.String(), "starts at each pod's first callback")

	up := NewScaleReport(1, 1, NewTimeline(podEvents("a", 100, 10), []string{"a"}, StartupPhase(), ""))
	up.ClientBurst = time.Second

	b.Reset()
	up.Print(&b)
	require.Contains(t, b.String(), "starts at the first RunPodSandbox callback")
}

func TestScaleReportPrintSaysWhenItWasMeasuredFromSpans(t *testing.T) {
	tl := NewTimeline(podEvents("a", 100, 10), []string{"a"}, StartupPhase(), "")
	r := NewScaleReport(1, 1, tl)
	r.Traced = true

	var b strings.Builder
	r.Print(&b)
	require.Contains(t, b.String(), "OpenTelemetry spans")
	require.NotContains(t, b.String(), "when our plugin's callback ran, so NRI")
}

func TestScaleReportPrintSaysWhenADistributionWasNotMeasured(t *testing.T) {
	// A single pod has no arrival gaps, so that distribution is empty.
	tl := NewTimeline(podEvents("a", 100, 10), []string{"a"}, StartupPhase(), PostStartContainer)
	tl.RecorderCost = nil

	var b strings.Builder
	NewScaleReport(1, 1, tl).Print(&b)

	require.NotContains(t, b.String(), "arrival gap",
		"an empty distribution is omitted rather than printed as zeroes")
}

// teardownEvents returns a teardown sequence for one pod in the order a
// reader expects, offset by start and spaced by step milliseconds.
func teardownEvents(pod string, start, step int) []*Event {
	out := make([]*Event, 0, len(teardownStages))
	for i, s := range teardownStages {
		out = append(out, ev(s, pod, start+i*step))
	}

	return out
}

func TestTeardownPhaseAnchorsOnTheFirstStageReached(t *testing.T) {
	// No StopContainer at all: the anchor must fall back to whichever stage
	// this pod did reach first, which is what a phase with no established
	// event order has to do.
	events := []*Event{
		ev(StopPodSandbox, "a", 300),
		ev(RemoveContainer, "a", 200),
		ev(RemovePodSandbox, "a", 400),
	}

	tl := NewTimeline(events, []string{"a"}, TeardownPhase(), "")

	require.Equal(t, RemovePodSandbox, tl.Terminal,
		"an empty terminal takes the phase's last stage")

	anchor, ok := tl.Pods[0].Anchor(tl.Phase)
	require.True(t, ok)
	require.Equal(t, at(200), anchor, "the earliest stage reached is the anchor")

	d, ok := tl.Pods[0].Total(tl.Phase, RemovePodSandbox)
	require.True(t, ok)
	require.Equal(t, 200*time.Millisecond, d)
}

func TestTeardownPhaseIsNotOrdered(t *testing.T) {
	require.False(t, TeardownPhase().Ordered,
		"teardown event order is a conformance question, not an assumption")
	require.True(t, StartupPhase().Ordered)
	require.Equal(t, RunPodSandbox, StartupPhase().Anchor)
	require.Empty(t, TeardownPhase().Anchor,
		"an unordered phase anchors per pod, not on a named stage")
}

func TestTeardownReportOmitsConsecutiveTransitions(t *testing.T) {
	var events []*Event
	events = append(events, teardownEvents("a", 100, 10)...)
	events = append(events, teardownEvents("b", 200, 10)...)

	tl := NewTimeline(events, []string{"a", "b"}, TeardownPhase(), "")
	r := NewScaleReport(1, 2, tl)

	names := make([]string, 0, len(r.Stats))
	for _, s := range r.Stats {
		names = append(names, s.Name)
	}

	// Offsets from the anchor are meaningful without an order; a transition
	// between two stages that may not follow one another is not.
	require.Contains(t, names, "anchor..RemovePodSandbox")
	require.NotContains(t, names, "StopContainer..RemoveContainer",
		"an unordered phase must not report consecutive transitions")
}

func TestStartupReportKeepsConsecutiveTransitions(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)

	tl := NewTimeline(events, []string{"a"}, StartupPhase(), PostStartContainer)
	r := NewScaleReport(1, 1, tl)

	names := make([]string, 0, len(r.Stats))
	for _, s := range r.Stats {
		names = append(names, s.Name)
	}

	require.Contains(t, names, "RunPodSandbox..CreateContainer")
	require.Contains(t, names, "anchor..PostStartContainer",
		"an ordered phase reports both the transitions and the offsets")
}

func TestNegativeOffsetsAreReportedNotClamped(t *testing.T) {
	// StartContainer before RunPodSandbox: impossible in reality, and exactly
	// what a wrong order assumption looks like in the data.
	events := []*Event{
		ev(RunPodSandbox, "a", 300),
		ev(StartContainer, "a", 100),
	}

	tl := NewTimeline(events, []string{"a"}, StartupPhase(), StartContainer)

	d, ok := tl.Pods[0].Total(tl.Phase, StartContainer)
	require.True(t, ok)
	require.Equal(t, -200*time.Millisecond, d, "the sign is kept")

	neg := tl.Negatives()
	require.Equal(t, 1, neg[StartContainer])

	var b strings.Builder
	NewScaleReport(1, 1, tl).Print(&b)
	out := b.String()

	require.Contains(t, out, "NOTE:")
	require.Contains(t, out, "arrived BEFORE",
		"a wrong order assumption must be stated, not absorbed")
	require.Contains(t, out, "StartContainer")
}

func TestLadderCoversEveryStageOfThePhase(t *testing.T) {
	var events []*Event
	events = append(events, podEvents("a", 100, 10)...)
	events = append(events, podEvents("b", 150, 10)...)

	tl := NewTimeline(events, []string{"a", "b"}, StartupPhase(), PostStartContainer)
	ladder := tl.Ladder()

	require.Len(t, ladder, len(scaleStages),
		"all five startup stages get a rung, not just the terminal one")

	require.Equal(t, RunPodSandbox, ladder[0].Stage)
	require.True(t, ladder[0].PodLevel, "RunPodSandbox is a pod stage")
	require.False(t, ladder[1].PodLevel, "CreateContainer is a container stage")

	// b starts 50ms after a, so the rung to the last RunPodSandbox is 50ms.
	require.Equal(t, 50*time.Millisecond, ladder[0].D)
	// b's PostStartContainer is at 150+4*10 = 190; a's anchor is 100.
	require.Equal(t, 90*time.Millisecond, ladder[4].D)

	for _, r := range ladder {
		require.Equal(t, 2, r.Started)
		require.Equal(t, 2, r.Reached)
	}
}

func TestLadderMarksTeardownPodStages(t *testing.T) {
	tl := NewTimeline(teardownEvents("a", 100, 10), []string{"a"}, TeardownPhase(), "")
	ladder := tl.Ladder()

	byStage := map[EventType]BurstRung{}
	for _, r := range ladder {
		byStage[r.Stage] = r
	}

	require.False(t, byStage[StopContainer].PodLevel)
	require.False(t, byStage[RemoveContainer].PodLevel)
	require.True(t, byStage[StopPodSandbox].PodLevel)
	require.True(t, byStage[RemovePodSandbox].PodLevel)
}

// The configured terminal stage. Introducing the phases dropped the wiring:
// the call sites went from cfg.ScaleTerminal() to a literal empty terminal, so
// -nri-test.scale-terminal became a flag nothing read. These pin it through
// the burst duration rather than only the field, so a timeline that ignored
// the configuration fails on a number.
func TestNewPhaseTimelineMeasuresToTheConfiguredTerminal(t *testing.T) {
	events := podEvents("a", 100, 10)

	cfg := &Config{ScaleTerminalEvent: string(StartContainer)}
	tl := NewPhaseTimeline(cfg, events, []string{"a"}, StartupPhase())

	require.Equal(t, StartContainer, tl.Terminal)

	d, _, reached := tl.Burst()
	require.Equal(t, 1, reached)
	require.Equal(t, 30*time.Millisecond, d,
		"RunPodSandbox at 100ms to StartContainer at 130ms")
}

func TestNewPhaseTimelineDefaultsToTheLastStartupStage(t *testing.T) {
	events := podEvents("a", 100, 10)

	tl := NewPhaseTimeline(&Config{}, events, []string{"a"}, StartupPhase())

	require.Equal(t, PostStartContainer, tl.Terminal,
		"the headline reading of a burst is the last event")

	d, _, _ := tl.Burst()
	require.Equal(t, 40*time.Millisecond, d,
		"RunPodSandbox at 100ms to PostStartContainer at 140ms")
}

func TestNewPhaseTimelineIgnoresATerminalThatIsNotAStageOfThePhase(t *testing.T) {
	// A startup stage cannot be a teardown's terminal. The rule is that the
	// configured stage applies only where the phase actually has it, so no
	// phase has to be special-cased by name.
	cfg := &Config{ScaleTerminalEvent: string(StartContainer)}

	tl := NewPhaseTimeline(cfg, teardownEvents("a", 1000, 10), []string{"a"}, TeardownPhase())

	require.Equal(t, RemovePodSandbox, tl.Terminal,
		"the teardown measures to its own last stage, not to a startup one")
}

// Naming one iteration's report file. The name used to carry a wall-clock
// timestamp to seconds, so two iterations that finished inside the same second
// wrote to the same path and the first was lost. The iteration number is
// unique by construction, which is the property the name actually needs.
func TestScaleReportPathIsUniquePerIteration(t *testing.T) {
	seen := map[string]int{}

	for i := 1; i <= 5; i++ {
		seen[ScaleReportPath("/tmp/burst.json", i)]++
	}

	require.Len(t, seen, 5, "every iteration must get a path of its own")
}

func TestScaleReportPathLeavesTheFirstIterationAlone(t *testing.T) {
	require.Equal(t, "/tmp/burst.json", ScaleReportPath("/tmp/burst.json", 1),
		"a single-iteration run writes exactly the path it was given")
	require.Equal(t, "/tmp/burst.json.2", ScaleReportPath("/tmp/burst.json", 2))
}
