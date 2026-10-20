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
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The scalability analysis. It turns the events a recorder plugin collected
// during a burst of pod creations into per-pod timelines and the statistics
// over them.
//
// Everything here is pure: it takes events and returns values, so the
// percentiles and the bucket boundaries can be checked against synthetic
// input. Statistics computed only from a live run cannot be wrong in any way
// anybody would notice, which is the same failure this suite guards against
// everywhere else.

// scaleStages are the per-pod event types a timeline is built from, in the
// order a pod passes through them.
//
// RunPodSandbox is the pod-level start; the other four are container-level. A
// burst of single-container pods is the subject, so one of each per pod is
// expected and a pod with more than one container records the first of each.
//
// All five are measured rather than a chosen pair. Each answers a different
// question about where a burst's time goes -- when the runtime learned about
// the pods, when it asked about their containers, when it was ready to start
// them, and when it said they had started -- and a burst that is slow in one
// of them and fast in the others is the result worth having.
var scaleStages = []EventType{
	RunPodSandbox,
	CreateContainer,
	PostCreateContainer,
	StartContainer,
	PostStartContainer,
}

// teardownStages are the stages a pod passes through on the way down.
//
// Listed in the order a reader expects, but nothing here depends on that
// order being right. Which NRI events a runtime fires on teardown, and in
// which order, is a conformance question this suite has not answered: kubelet
// calls StopPodSandbox and then RemovePodSandbox, and whether the per-container
// events arrive before, between or after is the runtime's business. So the
// teardown analysis is anchored on whichever stage a pod reached first rather
// than on this slice's order, and a stage that arrives "before" its
// predecessor is reported rather than clamped.
var teardownStages = []EventType{
	StopContainer,
	RemoveContainer,
	StopPodSandbox,
	RemovePodSandbox,
}

// Phase is one direction of a scaling measurement: which stages it is made of
// and what per-pod durations are measured from.
type Phase struct {
	// Name is "startup" or "teardown".
	Name string
	// Stages are the stages this phase records, in the order a report lists
	// them.
	Stages []EventType
	// Anchor is the stage per-pod durations are measured from. An empty Anchor
	// means "whichever of Stages that pod reached first", which is what a
	// phase whose event order is not established has to use.
	Anchor EventType
	// Ordered says whether consecutive pairs of Stages are a meaningful
	// transition. False suppresses the consecutive-transition statistics,
	// since a transition between stages that do not reliably follow one
	// another is a number with no meaning.
	Ordered bool
}

// StartupPhase is the scale-up measurement: a pod from its sandbox being
// created to its container having started.
func StartupPhase() Phase {
	return Phase{
		Name:    "startup",
		Stages:  slices.Clone(scaleStages),
		Anchor:  RunPodSandbox,
		Ordered: true,
	}
}

// TeardownPhase is the scale-down measurement: a pod's containers and sandbox
// being stopped and removed.
func TeardownPhase() Phase {
	return Phase{
		Name:    "teardown",
		Stages:  slices.Clone(teardownStages),
		Ordered: false,
	}
}

// podLevelStages are the stages that describe the pod rather than a container
// in it. Reported separately because a burst can be slow at getting sandboxes
// created and fast at the containers inside them, or the reverse, and the two
// have different causes.
var podLevelStages = []EventType{RunPodSandbox, StopPodSandbox, RemovePodSandbox}

// IsPodLevel reports whether a stage describes the pod rather than one of its
// containers.
func IsPodLevel(e EventType) bool { return slices.Contains(podLevelStages, e) }

// PodTimeline is when each stage fired for one pod.
//
// A stage that never fired has a zero At. That is not an error on its own: a
// burst measured to StartContainer legitimately has no PostStartContainer for
// the pods still starting when the window closed.
type PodTimeline struct {
	// PodName identifies the pod the timeline belongs to.
	PodName string
	// PodID is the sandbox id the runtime assigned.
	PodID string
	// At holds the first timestamp seen for each stage.
	At map[EventType]time.Time
}

// Stage returns the time a stage fired and whether it fired at all.
func (t *PodTimeline) Stage(e EventType) (time.Time, bool) {
	at, ok := t.At[e]

	return at, ok && !at.IsZero()
}

// Anchor returns the time this pod entered the phase: the phase's Anchor
// stage, or the earliest stage it reached when the phase has none.
//
// A phase with no established event order cannot anchor on a named stage
// without assuming that order, so it anchors on whichever stage arrived first
// for that pod. That is the honest zero: the first thing we were told about
// this pod going down.
func (t *PodTimeline) Anchor(p Phase) (time.Time, bool) {
	if p.Anchor != "" {
		return t.Stage(p.Anchor)
	}

	var first time.Time

	for _, st := range p.Stages {
		if at, ok := t.Stage(st); ok {
			if first.IsZero() || at.Before(first) {
				first = at
			}
		}
	}

	return first, !first.IsZero()
}

// Total returns the time from this pod's phase anchor to the given stage, and
// whether both ends were seen.
func (t *PodTimeline) Total(p Phase, stage EventType) (time.Duration, bool) {
	start, ok := t.Anchor(p)
	if !ok {
		return 0, false
	}

	end, ok := t.Stage(stage)
	if !ok {
		return 0, false
	}

	return end.Sub(start), true
}

// Timeline is the whole burst: one PodTimeline per pod, in first-seen order.
type Timeline struct {
	// Phase is the direction this timeline measures.
	Phase Phase
	// Pods are the per-pod timelines, ordered by their first event.
	Pods []*PodTimeline
	// Terminal is the stage the burst was measured to.
	Terminal EventType
	// RecorderCost is how long each recorded event took to reach the queue.
	// It is the instrument's own cost, and EventQ.Costs explains what it does
	// and does not span.
	RecorderCost []time.Duration
}

// NewTimeline builds a Timeline from the events a recorder collected,
// restricted to the named pods, measured to terminal.
//
// Events for pods outside names are ignored rather than rejected: the plugin
// is node-global and a node running anything else will deliver events this
// burst did not cause.
//
// The first timestamp for a stage wins. A runtime that retries a hook, or a
// pod with more than one container, would otherwise make a stage's time depend
// on how many times it fired.
func NewTimeline(events []*Event, names []string, phase Phase, terminal EventType) *Timeline {
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	if terminal == "" && len(phase.Stages) > 0 {
		terminal = phase.Stages[len(phase.Stages)-1]
	}

	t := &Timeline{Phase: phase, Terminal: terminal}
	byName := make(map[string]*PodTimeline, len(names))

	for _, e := range events {
		if e == nil || e.PodName == "" {
			continue
		}

		if _, ok := want[e.PodName]; !ok {
			continue
		}

		if !slices.Contains(t.Phase.Stages, e.Type) {
			continue
		}

		pt, ok := byName[e.PodName]
		if !ok {
			pt = &PodTimeline{
				PodName: e.PodName,
				PodID:   e.PodID,
				At:      map[EventType]time.Time{},
			}
			byName[e.PodName] = pt
			t.Pods = append(t.Pods, pt)
		}

		if pt.PodID == "" {
			pt.PodID = e.PodID
		}

		if _, seen := pt.At[e.Type]; !seen {
			pt.At[e.Type] = e.At
		}
	}

	return t
}

// NewPhaseTimeline builds the timeline of one phase of a run, measured to the
// stage the run's configuration asks for.
//
// Every timeline goes through here so that -nri-test.scale-terminal cannot be
// lost again. Introducing the phases is what lost it: the call sites went from
// passing cfg.ScaleTerminal() to passing a literal empty terminal, which falls
// back to the phase's last stage, so a run asking to be measured to
// StartContainer was measured to PostStartContainer instead and the flag was
// read by nothing -- the defect that got -nri-test.differential deleted from
// this suite.
//
// The configured stage applies only where the phase actually has it. That is
// one rule rather than a list of phases: a teardown cannot be measured to
// StartContainer, so the startup default falls through to the teardown's own
// last stage without TeardownPhase being named here.
func NewPhaseTimeline(cfg *Config, events []*Event, names []string, phase Phase) *Timeline {
	terminal := cfg.ScaleTerminal()
	if !slices.Contains(phase.Stages, terminal) {
		terminal = ""
	}

	return NewTimeline(events, names, phase, terminal)
}

// Burst returns the headline number: the time from the earliest
// RunPodSandbox in the timeline to the latest terminal stage, and the number
// of pods that reached each end.
//
// Both counts are returned because they are the thing that makes the duration
// readable or not. A burst of fifty that reached its terminal stage for six
// pods is a measurement of a window that closed too early, and the duration
// alone does not say so.
func (t *Timeline) Burst() (d time.Duration, started, finished int) {
	return t.BurstTo(t.Terminal)
}

// BurstTo returns the time from the earliest RunPodSandbox in the timeline to
// the latest arrival of the given stage, with how many pods reached each end.
//
// One of these per stage is the burst ladder: it says how long the whole burst
// took to get every pod as far as each stage, which is what "how well do we
// scale" means when there is more than one stage to scale through.
func (t *Timeline) BurstTo(stage EventType) (d time.Duration, started, reached int) {
	var first, last time.Time

	for _, p := range t.Pods {
		if at, ok := p.Anchor(t.Phase); ok {
			started++

			if first.IsZero() || at.Before(first) {
				first = at
			}
		}

		if at, ok := p.Stage(stage); ok {
			reached++

			if last.IsZero() || at.After(last) {
				last = at
			}
		}
	}

	if first.IsZero() || last.IsZero() {
		return 0, started, reached
	}

	return last.Sub(first), started, reached
}

// BurstRung is one rung of the burst ladder.
type BurstRung struct {
	// Stage is the stage this rung measures to.
	Stage EventType
	// PodLevel distinguishes a pod stage from a container stage.
	PodLevel bool
	// D is the time from the first RunPodSandbox to the last arrival of Stage.
	D time.Duration
	// Started and Reached are how many pods reached each end.
	Started, Reached int
}

// Ladder returns one rung per stage, in stage order.
func (t *Timeline) Ladder() []BurstRung {
	rungs := make([]BurstRung, 0, len(t.Phase.Stages))

	for _, st := range t.Phase.Stages {
		d, started, reached := t.BurstTo(st)
		rungs = append(rungs, BurstRung{
			Stage:    st,
			PodLevel: IsPodLevel(st),
			D:        d,
			Started:  started,
			Reached:  reached,
		})
	}

	return rungs
}

// Stat is a distribution summarized.
type Stat struct {
	// Name is what was measured.
	Name string
	// N is how many samples it was computed from.
	N int
	// Min, P50, P90, P99 and Max are the order statistics.
	Min, P50, P90, P99, Max time.Duration
	// Mean and StdDev describe the shape.
	Mean, StdDev time.Duration
}

// NewStat summarizes samples under a name. An empty sample set yields a Stat
// with N zero and nothing else set, so a caller can report "not measured"
// rather than a misleading set of zeroes.
func NewStat(name string, samples []time.Duration) Stat {
	s := Stat{Name: name, N: len(samples)}
	if len(samples) == 0 {
		return s
	}

	sorted := slices.Clone(samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	s.Min = sorted[0]
	s.Max = sorted[len(sorted)-1]
	s.P50 = percentile(sorted, 50)
	s.P90 = percentile(sorted, 90)
	s.P99 = percentile(sorted, 99)

	var sum float64
	for _, d := range sorted {
		sum += float64(d)
	}

	mean := sum / float64(len(sorted))
	s.Mean = time.Duration(mean)

	var sq float64
	for _, d := range sorted {
		diff := float64(d) - mean
		sq += diff * diff
	}

	s.StdDev = time.Duration(math.Sqrt(sq / float64(len(sorted))))

	return s
}

// percentile returns the p-th percentile of an ascending slice, by nearest
// rank. Nearest rank rather than interpolation because the samples are
// durations from a modest number of pods: interpolating between two observed
// values invents one that was never measured.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}

	rank := int(math.Ceil(float64(p)/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}

	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}

	return sorted[rank]
}

// Histogram is a bucketed count of a distribution.
type Histogram struct {
	// Name is what was measured.
	Name string
	// Bounds are the inclusive upper bounds of each bucket, ascending.
	Bounds []time.Duration
	// Counts has one entry per bound plus a final overflow bucket, so
	// len(Counts) == len(Bounds)+1.
	Counts []int
}

// defaultBounds are the histogram buckets a pod-startup measurement uses.
// They are logarithmic because startup latency is: the interesting structure
// is between ten milliseconds and ten seconds, and a linear scale over that
// range puts everything in one bucket.
var defaultBounds = []time.Duration{
	1 * time.Millisecond,
	5 * time.Millisecond,
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	2500 * time.Millisecond,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// NewHistogram buckets samples under a name. Passing nil bounds uses
// defaultBounds.
func NewHistogram(name string, samples []time.Duration, bounds []time.Duration) Histogram {
	if bounds == nil {
		bounds = defaultBounds
	}

	h := Histogram{
		Name:   name,
		Bounds: slices.Clone(bounds),
		Counts: make([]int, len(bounds)+1),
	}

	for _, d := range samples {
		i := 0
		for i < len(bounds) && d > bounds[i] {
			i++
		}

		h.Counts[i]++
	}

	return h
}

// Samples returns the per-pod durations of one stage transition, skipping any
// pod that is missing either end.
//
// The skipped pods are counted rather than silently dropped, because "forty of
// fifty pods" and "fifty of fifty" are different measurements and the
// difference is exactly what a burst that outran its window looks like.
func (t *Timeline) Samples(from, to EventType) (samples []time.Duration, skipped int) {
	for _, p := range t.Pods {
		a, aok := p.Stage(from)
		b, bok := p.Stage(to)

		if !aok || !bok {
			skipped++

			continue
		}

		samples = append(samples, b.Sub(a))
	}

	return samples, skipped
}

// Totals returns each pod's anchor-to-terminal duration.
func (t *Timeline) Totals() (samples []time.Duration, skipped int) {
	return t.Offsets(t.Terminal)
}

// Offsets returns, per pod, how long after entering the phase that pod reached
// the given stage.
//
// This is the order-free measurement: it needs no claim about which stage
// follows which, so it is meaningful for a teardown whose event order has not
// been established. A negative offset would mean the phase's declared anchor
// is not actually first, which Negatives counts rather than hides.
func (t *Timeline) Offsets(stage EventType) (samples []time.Duration, skipped int) {
	for _, p := range t.Pods {
		d, ok := p.Total(t.Phase, stage)
		if !ok {
			skipped++

			continue
		}

		samples = append(samples, d)
	}

	return samples, skipped
}

// Negatives counts the per-pod offsets that came out negative, which means a
// stage arrived before the phase's anchor.
//
// Reported rather than clamped. A negative offset is not noise: it says the
// order this phase assumed is wrong, which for teardown is a live possibility
// and is exactly the kind of thing a measurement should surface instead of
// quietly taking an absolute value.
func (t *Timeline) Negatives() map[EventType]int {
	out := map[EventType]int{}

	for _, st := range t.Phase.Stages {
		n := 0

		for _, p := range t.Pods {
			if d, ok := p.Total(t.Phase, st); ok && d < 0 {
				n++
			}
		}

		if n > 0 {
			out[st] = n
		}
	}

	return out
}

// Spread returns the gaps between consecutive arrivals of one stage, in time
// order.
//
// This is what shows serialization. If the runtime hands NRI one call at a
// time, the gaps cluster around the per-call cost; if it fans out, they
// cluster near zero with one long tail at the front.
func (t *Timeline) Spread(e EventType) []time.Duration {
	var at []time.Time

	for _, p := range t.Pods {
		if ts, ok := p.Stage(e); ok {
			at = append(at, ts)
		}
	}

	if len(at) < 2 {
		return nil
	}

	sort.Slice(at, func(i, j int) bool { return at[i].Before(at[j]) })

	gaps := make([]time.Duration, 0, len(at)-1)
	for i := 1; i < len(at); i++ {
		gaps = append(gaps, at[i].Sub(at[i-1]))
	}

	return gaps
}

// stageTransitions are the consecutive stage pairs a report summarizes.
func stageTransitions(stages []EventType) [][2]EventType {
	pairs := make([][2]EventType, 0, len(stages))
	for i := 1; i < len(stages); i++ {
		pairs = append(pairs, [2]EventType{stages[i-1], stages[i]})
	}

	return pairs
}

// ScaleReportPath returns where one iteration's raw table is written, given
// the path the run was configured with.
//
// The first iteration writes the configured path unchanged, so an ordinary
// single-burst run produces exactly the file it asked for. Later iterations
// get the iteration number appended, because it is unique by construction: a
// wall-clock suffix is not, and two iterations finishing inside the same
// second would overwrite each other.
func ScaleReportPath(path string, iteration int) string {
	if iteration <= 1 {
		return path
	}

	return path + "." + strconv.Itoa(iteration)
}

// ScaleReport is everything one burst measured.
type ScaleReport struct {
	// Iteration is which burst this was, counting from one. A node's first
	// burst differs from its fifth, so a reader needs to know which.
	Iteration int
	// Replicas is the target the ReplicaSet was scaled to.
	Replicas int
	// Timeline is the per-pod data the rest is derived from.
	Timeline *Timeline
	// Burst is the duration to the Timeline's terminal stage, which is the
	// last rung of Ladder.
	Burst time.Duration
	// Ladder is one entry per stage: how long the burst took to get every pod
	// as far as that stage.
	Ladder []BurstRung
	// Started and Finished are how many pods reached each end of it.
	Started, Finished int
	// Stats and Hists summarize the totals, each stage transition, the
	// arrival spread and the recorder's own cost.
	Stats []Stat
	Hists []Histogram
	// Skipped records, per summarized distribution, how many pods were
	// missing an end.
	Skipped map[string]int
	// Negatives records stages that arrived before the phase's anchor, which
	// means the phase's assumed order is wrong.
	Negatives map[EventType]int
	// OpStats is the client-side decomposition, when the burst was driven over
	// CRI and so has one. A ReplicaSet burst leaves it empty: kubelet issued
	// the calls, so there is no client-side clock to decompose with.
	OpStats []OpStats
	// ClientBurst is the burst as the client saw it, from the first call
	// starting to the last terminal stage arriving. Zero when there is no
	// client-side clock. See ClientBurst.
	ClientBurst time.Duration
	// Traced is set when the timeline was built from the runtime's spans
	// rather than from a recorder plugin, and Hooks are then the runtime's own
	// per-hook timings.
	Traced bool
	Hooks  []HookStat
}

// NewScaleReport summarizes a burst.
func NewScaleReport(iteration, replicas int, t *Timeline) *ScaleReport {
	r := &ScaleReport{
		Iteration: iteration,
		Replicas:  replicas,
		Timeline:  t,
		Skipped:   map[string]int{},
		Negatives: t.Negatives(),
	}

	r.Burst, r.Started, r.Finished = t.Burst()
	r.Ladder = t.Ladder()

	add := func(name string, samples []time.Duration, skipped int) {
		r.Stats = append(r.Stats, NewStat(name, samples))
		r.Hists = append(r.Hists, NewHistogram(name, samples, nil))

		if skipped > 0 {
			r.Skipped[name] = skipped
		}
	}

	// Offsets from the phase anchor, one per stage. Order-free, so this is the
	// measurement that holds for a phase whose event order is not established.
	for _, st := range t.Phase.Stages {
		if st == t.Phase.Anchor {
			continue
		}

		samples, sk := t.Offsets(st)
		add(fmt.Sprintf("anchor..%s", st), samples, sk)
	}

	// Consecutive transitions, only where the stages reliably follow one
	// another. For a phase that cannot promise that, a "transition" between
	// two stages is a number with no meaning.
	if t.Phase.Ordered {
		for _, p := range stageTransitions(t.Phase.Stages) {
			samples, sk := t.Samples(p[0], p[1])
			add(fmt.Sprintf("%s..%s", p[0], p[1]), samples, sk)
		}
	}

	for _, e := range t.Phase.Stages {
		if gaps := t.Spread(e); len(gaps) > 0 {
			add(fmt.Sprintf("arrival gap %s", e), gaps, 0)
		}
	}

	if len(t.RecorderCost) > 0 {
		add("recorder queue cost", t.RecorderCost, 0)
	}

	return r
}

// Print writes the report as plain text.
//
// The caveats are printed with the numbers rather than kept in a document,
// because a number quoted without them says something the measurement does
// not support: the clock is the plugin's own, one plugin is the floor rather
// than the typical case, and kubelet and the control plane are inside the
// window.
func (r *ScaleReport) Print(w io.Writer) {
	var b strings.Builder

	fmt.Fprintf(&b, "\n=== nri-test scalability: %s, iteration %d ===\n\n",
		r.Timeline.Phase.Name, r.Iteration)
	fmt.Fprintf(&b, "  replicas requested: %d\n", r.Replicas)
	fmt.Fprintf(&b, "  pods seen:          %d\n", len(r.Timeline.Pods))
	fmt.Fprintf(&b, "  entered the phase:  %d\n", r.Started)
	fmt.Fprintf(&b, "  reached %-12s %d\n", string(r.Timeline.Terminal)+":", r.Finished)

	if r.Started != r.Finished {
		fmt.Fprintf(&b, "\n  NOTE: %d pod(s) started and %d finished, so the burst below\n",
			r.Started, r.Finished)
		fmt.Fprintf(&b, "  measures a window that closed before every pod was done.\n")
	}

	anchor := string(r.Timeline.Phase.Anchor)
	if anchor == "" {
		anchor = "stage each pod reached first"
	}

	fmt.Fprintf(&b, "\n  burst ladder, each from the first %s:\n\n", anchor)
	fmt.Fprintf(&b, "    %-22s %-10s %9s %8s\n", "to last", "level", "elapsed", "pods")

	for _, rung := range r.Ladder {
		level := "container"
		if rung.PodLevel {
			level = "pod"
		}

		elapsed := rung.D.String()
		if rung.Reached == 0 {
			elapsed = "-"
		}

		fmt.Fprintf(&b, "    %-22s %-10s %9s %4d/%-3d\n",
			rung.Stage, level, elapsed, rung.Reached, rung.Started)
	}

	fmt.Fprintf(&b, "\n%-34s %5s %9s %9s %9s %9s %9s\n",
		"distribution", "n", "min", "p50", "p90", "p99", "max")

	for _, s := range r.Stats {
		if s.N == 0 {
			fmt.Fprintf(&b, "%-34s %5d  not measured\n", s.Name, 0)

			continue
		}

		fmt.Fprintf(&b, "%-34s %5d %9s %9s %9s %9s %9s\n",
			s.Name, s.N, s.Min, s.P50, s.P90, s.P99, s.Max)
	}

	if len(r.Negatives) > 0 {
		fmt.Fprintf(&b, "\n  NOTE: some stages arrived BEFORE this phase's anchor, so the\n")
		fmt.Fprintf(&b, "  order %s assumes is not the order this runtime fires them in:\n",
			r.Timeline.Phase.Name)

		for _, st := range r.Timeline.Phase.Stages {
			if n, ok := r.Negatives[st]; ok {
				fmt.Fprintf(&b, "    %-22s %d pod(s)\n", st, n)
			}
		}

		fmt.Fprintf(&b, "  Those pods' offsets are negative below, not clamped.\n")
	}

	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "\n  incomplete pods per distribution:\n")

		for _, s := range r.Stats {
			if n, ok := r.Skipped[s.Name]; ok {
				fmt.Fprintf(&b, "    %-40s %d\n", s.Name, n)
			}
		}
	}

	for _, h := range r.Hists {
		if total(h.Counts) == 0 {
			continue
		}

		fmt.Fprintf(&b, "\n  %s\n", h.Name)
		writeHistogram(&b, h)
	}

	if r.ClientBurst > 0 {
		fmt.Fprintf(&b, "\n  client-side burst, first call to last %s: %s\n",
			r.Timeline.Terminal, r.ClientBurst)
		if r.Timeline.Phase.Anchor != "" {
			fmt.Fprintf(&b, "  (the ladder above starts at the first %s callback, which a\n",
				r.Timeline.Phase.Anchor)
			fmt.Fprintf(&b, "  runtime fires after its own sandbox work, so this is wider)\n")
		} else {
			fmt.Fprintf(&b, "  (the ladder above starts at each pod's first callback, so this\n")
			fmt.Fprintf(&b, "  is wider)\n")
		}
	}

	WriteOpStats(&b, r.OpStats)
	WriteHookStats(&b, r.Hooks)

	fmt.Fprintf(&b, "\n  How to read this:\n")
	if r.Traced {
		fmt.Fprintf(&b, "    - Measured from the runtime's OpenTelemetry spans, not from a\n")
		fmt.Fprintf(&b, "      recorder plugin. A stage's time is when the runtime started\n")
		fmt.Fprintf(&b, "      its first plugin call for it. Tracing at full sampling slows\n")
		fmt.Fprintf(&b, "      the runtime, so these are traced timings.\n")
	} else if len(r.OpStats) == 0 {
		fmt.Fprintf(&b, "    - Every timestamp is when our plugin's callback ran, so NRI\n")
		fmt.Fprintf(&b, "      transport and the runtime's scheduling of plugin calls are\n")
		fmt.Fprintf(&b, "      inside it. It is not runtime latency on its own.\n")
	} else {
		fmt.Fprintf(&b, "    - The stage timestamps are when our plugin's callback ran, but\n")
		fmt.Fprintf(&b, "      this burst was driven over CRI, so the client-side table\n")
		fmt.Fprintf(&b, "      above separates the runtime's work from the NRI round trip.\n")
	}
	fmt.Fprintf(&b, "    - One plugin is the floor. NRI invokes plugins in index order,\n")
	fmt.Fprintf(&b, "      so N plugins serialize per callback.\n")
	if len(r.OpStats) == 0 {
		fmt.Fprintf(&b, "    - Kubelet and the control plane are inside the window. Compare\n")
		fmt.Fprintf(&b, "      against a CRI-driven burst to separate their share.\n")
	} else {
		fmt.Fprintf(&b, "    - Kubelet is NOT in this window: the burst was issued directly\n")
		fmt.Fprintf(&b, "      over CRI. Compare against a ReplicaSet burst for the\n")
		fmt.Fprintf(&b, "      realistic path.\n")
	}
	if r.Timeline.Phase.Ordered {
		fmt.Fprintf(&b, "    - %s is a notification after the fact, so measuring\n", PostStartContainer)
		fmt.Fprintf(&b, "      to it answers a different question than measuring to %s.\n",
			StartContainer)
	} else {
		fmt.Fprintf(&b, "    - This phase's stages are anchored on whichever arrived first\n")
		fmt.Fprintf(&b, "      per pod, because which teardown events a runtime fires, and\n")
		fmt.Fprintf(&b, "      in what order, is not something this suite has established.\n")
	}

	fmt.Fprint(w, b.String())
}

func total(counts []int) int {
	n := 0
	for _, c := range counts {
		n += c
	}

	return n
}

// writeHistogram renders one histogram as a bar chart scaled to the widest
// bucket, so the shape is visible without the reader doing arithmetic.
func writeHistogram(b *strings.Builder, h Histogram) {
	const width = 40

	max := 0
	for _, c := range h.Counts {
		if c > max {
			max = c
		}
	}

	if max == 0 {
		return
	}

	label := func(i int) string {
		if i == len(h.Bounds) {
			return fmt.Sprintf("> %s", h.Bounds[len(h.Bounds)-1])
		}

		return fmt.Sprintf("<= %s", h.Bounds[i])
	}

	for i, c := range h.Counts {
		if c == 0 {
			continue
		}

		bars := c * width / max
		if bars == 0 {
			bars = 1
		}

		fmt.Fprintf(b, "    %-12s %5d %s\n", label(i), c, strings.Repeat("#", bars))
	}
}
