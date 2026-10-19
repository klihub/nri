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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client-side operation timing, for a burst driven over CRI.
//
// This is what the CRI driver buys over a ReplicaSet: we issue the calls, so
// we know when each one was asked for and when it returned, not only when our
// plugin was called back. That decomposes the one thing a plugin-side clock
// cannot separate on its own -- how much of an operation was the runtime's own
// work and how much was the NRI round trip to us.
//
// With both halves in hand, for one operation:
//
//	call start ....... NRI callback ....... call end
//	|<-- before -->|  |<-- in us -->|   |<-- after -->|
//
// "before" is the runtime's work up to the hook, "after" is its work from the
// hook to the client's answer, and the middle is ours. None of the three is
// available from the plugin side alone, and none needs a trace exporter.

// OpRecord is one client-side driver call.
type OpRecord struct {
	// Op is the operation's name, matching the NRI stage it triggers where
	// there is one: RunPodSandbox, CreateContainer, StartContainer.
	Op EventType
	// PodName and PodID identify the pod the call was for.
	PodName string
	PodID   string
	// CtrName and CtrID identify the container, for container operations.
	CtrName string
	CtrID   string
	// Start and End bracket the call as the caller saw it.
	Start, End time.Time
	// Err is the call's error, if it failed. A failed call's timings are still
	// recorded: how long a burst takes to fail is a result too, and dropping
	// them would make a run that half-failed look faster than one that worked.
	Err error
}

// Duration returns how long the call took as the caller saw it.
func (o *OpRecord) Duration() time.Duration { return o.End.Sub(o.Start) }

// OpLog collects OpRecords from the goroutines driving a burst.
//
// A burst is concurrent by definition, so this is the one piece of the
// scalability measurement that has to be safe for several writers. It is
// preallocated for the expected call count for the same reason the event queue
// is: a reallocation inside the window being measured is time attributed to
// the runtime that belongs to the instrument.
type OpLog struct {
	mu   sync.Mutex
	recs []*OpRecord
}

// NewOpLog returns an OpLog with room for n records.
func NewOpLog(n int) *OpLog {
	return &OpLog{recs: make([]*OpRecord, 0, n)}
}

// Add records one call.
func (l *OpLog) Add(r *OpRecord) {
	if l == nil || r == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.recs = append(l.recs, r)
}

// Records returns a snapshot, in recording order.
func (l *OpLog) Records() []*OpRecord {
	if l == nil {
		return nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.recs)
}

// Time runs fn, records how long it took as one OpRecord, and returns fn's
// error.
//
// The record is added whether fn succeeded or not, and the caller gets the
// error back to decide what to do with it. A burst's driver goroutines cannot
// use require: testify's FailNow has to run on the test's own goroutine, so
// errors come back here and are asserted once the burst is over.
func (l *OpLog) Time(r *OpRecord, fn func() error) error {
	r.Start = time.Now()
	err := fn()
	r.End = time.Now()
	r.Err = err

	l.Add(r)

	return err
}

// Failures returns the records whose call failed.
func (l *OpLog) Failures() []*OpRecord {
	var out []*OpRecord

	for _, r := range l.Records() {
		if r.Err != nil {
			out = append(out, r)
		}
	}

	return out
}

// FailureReports returns one message per failed call, naming the phase it
// belongs to.
//
// It exists because a phase whose failures are not reported produces a report
// that reads as a measurement. A burst's goroutines cannot use require --
// testify's FailNow has to run on the test's own goroutine -- so every call is
// issued with its error going to the log instead, and a log nobody asserts on
// is the same as no log at all.
//
// The phase is named in every message rather than printed once as a heading:
// both directions report through the same test, so a message that said only
// which operation failed would leave a reader guessing which direction it was.
func (l *OpLog) FailureReports(phase string) []string {
	failures := l.Failures()
	if len(failures) == 0 {
		return nil
	}

	out := make([]string, 0, len(failures))
	for _, f := range failures {
		out = append(out, fmt.Sprintf("%s for pod %s failed during the %s, so the "+
			"%s numbers are of a phase that did not fully succeed: %v",
			f.Op, f.PodName, phase, phase, f.Err))
	}

	return out
}

// ClientBurst returns how long the burst took as the client saw it: from the
// earliest call starting to the latest arrival of the timeline's terminal
// stage. It returns zero when there are no calls or no arrivals.
//
// This is wider than Timeline.Burst, which starts at the first RunPodSandbox
// callback. A runtime fires that hook at the end of its sandbox work, so the
// work before it is outside Burst and inside this.
func ClientBurst(ops []*OpRecord, t *Timeline) time.Duration {
	var first, last time.Time

	for _, o := range ops {
		if first.IsZero() || o.Start.Before(first) {
			first = o.Start
		}
	}

	for _, p := range t.Pods {
		if at, ok := p.Stage(t.Terminal); ok && at.After(last) {
			last = at
		}
	}

	if first.IsZero() || last.IsZero() {
		return 0
	}

	return last.Sub(first)
}

// OpStats summarizes the client-side view of one operation.
type OpStats struct {
	// Op is the operation.
	Op EventType
	// Call is how long the call took, as the caller saw it.
	Call Stat
	// Before is from the call starting to our plugin being called back.
	Before Stat
	// After is from our callback to the call returning.
	After Stat
	// Correlated is how many calls were matched to an NRI callback, and
	// Uncorrelated how many were not.
	Correlated, Uncorrelated int
}

// opToStage maps a driver call to the one NRI stage of the same name that it
// triggers.
//
// One stage per call, not every stage the call produces: a StopPod call also
// fires StopContainer and RemoveContainer, but what a client-side clock can
// decompose is the runtime's work either side of a single hook, so the
// same-named one is the one worth bracketing. UpdateContainerResources
// triggers UpdateContainer, which neither direction of a burst uses.
var opToStage = map[EventType]EventType{
	RunPodSandbox:    RunPodSandbox,
	CreateContainer:  CreateContainer,
	StartContainer:   StartContainer,
	StopPodSandbox:   StopPodSandbox,
	RemovePodSandbox: RemovePodSandbox,
}

// CorrelateOps matches client-side calls against the NRI callbacks they
// triggered and summarizes the three intervals per operation.
//
// Matching is by pod name for the pod-level operations and by container id
// for the container ones, because a container's id is what the runtime gives
// both us and the plugin, while its name is only unique within its pod.
//
// A call with no matching callback is counted, not dropped. The two commonest
// reasons are a call that failed before the runtime reached the hook, and a
// hook the plugin was not subscribed to, and both are findings rather than
// noise.
func CorrelateOps(ops []*OpRecord, events []*Event) []OpStats {
	byStage := map[EventType]map[string]time.Time{}

	for _, e := range events {
		if e == nil {
			continue
		}

		key := e.PodName
		if e.CtrID != "" {
			key = e.CtrID
		}

		if key == "" {
			continue
		}

		m, ok := byStage[e.Type]
		if !ok {
			m = map[string]time.Time{}
			byStage[e.Type] = m
		}

		// First callback wins, matching how a PodTimeline records a stage.
		if _, seen := m[key]; !seen {
			m[key] = e.At
		}
	}

	type bucket struct {
		call, before, inPlugin, after []time.Duration
		correlated, uncorrelated      int
	}

	buckets := map[EventType]*bucket{}
	order := []EventType{}

	for _, o := range ops {
		b, ok := buckets[o.Op]
		if !ok {
			b = &bucket{}
			buckets[o.Op] = b
			order = append(order, o.Op)
		}

		b.call = append(b.call, o.Duration())

		stage, ok := opToStage[o.Op]
		if !ok {
			b.uncorrelated++

			continue
		}

		key := o.PodName
		if o.CtrID != "" {
			key = o.CtrID
		}

		at, ok := byStage[stage][key]
		if !ok || at.IsZero() {
			b.uncorrelated++

			continue
		}

		b.correlated++
		b.before = append(b.before, at.Sub(o.Start))
		b.after = append(b.after, o.End.Sub(at))
	}

	out := make([]OpStats, 0, len(order))

	for _, op := range order {
		b := buckets[op]
		out = append(out, OpStats{
			Op:           op,
			Call:         NewStat(string(op)+" call", b.call),
			Before:       NewStat(string(op)+" call start..NRI hook", b.before),
			After:        NewStat(string(op)+" NRI hook..call end", b.after),
			Correlated:   b.correlated,
			Uncorrelated: b.uncorrelated,
		})
	}

	return out
}

// WriteOpStats renders the client-side decomposition.
func WriteOpStats(b *strings.Builder, stats []OpStats) {
	if len(stats) == 0 {
		return
	}

	fmt.Fprintf(b, "\n  client-side view, per driver call:\n\n")
	fmt.Fprintf(b, "    %-38s %5s %9s %9s %9s\n", "interval", "n", "p50", "p90", "max")

	for _, s := range stats {
		for _, st := range []Stat{s.Call, s.Before, s.After} {
			if st.N == 0 {
				continue
			}

			fmt.Fprintf(b, "    %-38s %5d %9s %9s %9s\n",
				st.Name, st.N, st.P50, st.P90, st.Max)
		}

		if s.Uncorrelated > 0 {
			fmt.Fprintf(b, "    %-38s %5d calls had no matching NRI callback\n",
				string(s.Op)+":", s.Uncorrelated)
		}

		fmt.Fprintln(b)
	}

	fmt.Fprintf(b, "    \"call start..NRI hook\" is the runtime's work before it\n")
	fmt.Fprintf(b, "    reached us; \"NRI hook..call end\" is its work afterwards.\n")
	fmt.Fprintf(b, "    Neither is available from the plugin's clock alone, which is\n")
	fmt.Fprintf(b, "    what driving the burst ourselves buys.\n")
}

// SortOps returns the records sorted by start time, for a raw table a reader
// can follow down the page.
func SortOps(ops []*OpRecord) []*OpRecord {
	out := slices.Clone(ops)
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })

	return out
}
