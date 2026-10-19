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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ctrEv builds one recorded container event.
func ctrEv(t EventType, pod, ctr string, ms int) *Event {
	return &Event{Type: t, PodName: pod, PodID: pod + "-id", CtrID: ctr, CtrName: ctr + "-name", At: at(ms)}
}

// op builds one client-side call spanning [start, end] milliseconds.
func op(o EventType, pod, ctr string, start, end int) *OpRecord {
	return &OpRecord{
		Op: o, PodName: pod, PodID: pod + "-id", CtrID: ctr,
		Start: at(start), End: at(end),
	}
}

// statsFor returns the OpStats of one operation.
func statsFor(t *testing.T, stats []OpStats, o EventType) OpStats {
	t.Helper()

	for _, s := range stats {
		if s.Op == o {
			return s
		}
	}

	require.FailNow(t, "no OpStats", "operation %s is missing from %v", o, stats)

	return OpStats{}
}

func TestCorrelateOpsSplitsAPodCallAroundItsHook(t *testing.T) {
	ops := []*OpRecord{op(RunPodSandbox, "a", "", 0, 500)}
	events := []*Event{ev(RunPodSandbox, "a", 480)}

	s := statsFor(t, CorrelateOps(ops, events), RunPodSandbox)

	require.Equal(t, 1, s.Correlated)
	require.Zero(t, s.Uncorrelated)
	require.Equal(t, 500*time.Millisecond, s.Call.Max, "the whole call")
	require.Equal(t, 480*time.Millisecond, s.Before.Max, "call start to the hook")
	require.Equal(t, 20*time.Millisecond, s.After.Max, "the hook to call end")
}

func TestCorrelateOpsMatchesAContainerCallByContainerID(t *testing.T) {
	ops := []*OpRecord{op(CreateContainer, "a", "c1", 100, 300)}
	events := []*Event{ctrEv(CreateContainer, "a", "c1", 120)}

	s := statsFor(t, CorrelateOps(ops, events), CreateContainer)

	require.Equal(t, 1, s.Correlated)
	require.Equal(t, 20*time.Millisecond, s.Before.Max)
	require.Equal(t, 180*time.Millisecond, s.After.Max)
}

// A container call recorded without its id cannot be matched, because the
// container's id is what the match is keyed on. That is how a CreateContainer
// whose id was filled in only after the call went unmatched in a real run, so
// the failure has to be visible rather than quietly dropped.
func TestCorrelateOpsCountsAContainerCallWithNoIDAsUncorrelated(t *testing.T) {
	ops := []*OpRecord{op(CreateContainer, "a", "", 100, 300)}
	events := []*Event{ctrEv(CreateContainer, "a", "c1", 120)}

	s := statsFor(t, CorrelateOps(ops, events), CreateContainer)

	require.Zero(t, s.Correlated)
	require.Equal(t, 1, s.Uncorrelated)
	require.Equal(t, 1, s.Call.N, "the call itself is still timed")
	require.Zero(t, s.Before.N)
}

func TestCorrelateOpsDoesNotMatchAContainerCallToAnotherContainer(t *testing.T) {
	ops := []*OpRecord{op(CreateContainer, "a", "c1", 100, 300)}
	events := []*Event{ctrEv(CreateContainer, "a", "c2", 120)}

	s := statsFor(t, CorrelateOps(ops, events), CreateContainer)

	require.Zero(t, s.Correlated, "same pod, different container id")
	require.Equal(t, 1, s.Uncorrelated)
}

func TestCorrelateOpsDoesNotMatchAcrossStages(t *testing.T) {
	ops := []*OpRecord{op(StartContainer, "a", "c1", 100, 140)}
	events := []*Event{ctrEv(CreateContainer, "a", "c1", 120)}

	s := statsFor(t, CorrelateOps(ops, events), StartContainer)

	require.Zero(t, s.Correlated, "a CreateContainer callback is not a StartContainer's")
	require.Equal(t, 1, s.Uncorrelated)
}

// StopPodSandbox and RemovePodSandbox were once missing from the table that
// maps a call to its stage, so every teardown call counted as unmatched and the
// scale-down got no decomposition at all.
func TestCorrelateOpsCorrelatesTheTeardownPodCalls(t *testing.T) {
	ops := []*OpRecord{
		op(StopPodSandbox, "a", "", 1000, 1100),
		op(RemovePodSandbox, "a", "", 1200, 1260),
	}
	events := []*Event{
		ev(StopPodSandbox, "a", 1030),
		ev(RemovePodSandbox, "a", 1210),
	}

	stats := CorrelateOps(ops, events)

	stop := statsFor(t, stats, StopPodSandbox)
	require.Equal(t, 1, stop.Correlated)
	require.Equal(t, 30*time.Millisecond, stop.Before.Max)
	require.Equal(t, 70*time.Millisecond, stop.After.Max)

	rm := statsFor(t, stats, RemovePodSandbox)
	require.Equal(t, 1, rm.Correlated)
	require.Equal(t, 10*time.Millisecond, rm.Before.Max)
	require.Equal(t, 50*time.Millisecond, rm.After.Max)
}

func TestCorrelateOpsKeepsTheFirstCallbackOfAStage(t *testing.T) {
	ops := []*OpRecord{op(RunPodSandbox, "a", "", 0, 500)}
	events := []*Event{
		ev(RunPodSandbox, "a", 100),
		ev(RunPodSandbox, "a", 400), // a retry: the first must win
	}

	s := statsFor(t, CorrelateOps(ops, events), RunPodSandbox)

	require.Equal(t, 100*time.Millisecond, s.Before.Max)
	require.Equal(t, 400*time.Millisecond, s.After.Max)
}

func TestCorrelateOpsCountsACallWithNoCallbackAndAnUnmappedOperation(t *testing.T) {
	ops := []*OpRecord{
		op(RunPodSandbox, "a", "", 0, 500),
		op(RunPodSandbox, "b", "", 0, 600), // no callback for b
		op(UpdateContainer, "a", "c1", 0, 10),
	}
	events := []*Event{ev(RunPodSandbox, "a", 480)}

	stats := CorrelateOps(ops, events)

	run := statsFor(t, stats, RunPodSandbox)
	require.Equal(t, 1, run.Correlated)
	require.Equal(t, 1, run.Uncorrelated)
	require.Equal(t, 2, run.Call.N, "both calls are timed, matched or not")
	require.Equal(t, 600*time.Millisecond, run.Call.Max)

	upd := statsFor(t, stats, UpdateContainer)
	require.Zero(t, upd.Correlated, "UpdateContainer maps to no stage of a burst")
	require.Equal(t, 1, upd.Uncorrelated)
}

func TestCorrelateOpsSummarizesSeveralPods(t *testing.T) {
	ops := []*OpRecord{
		op(RunPodSandbox, "a", "", 0, 500),
		op(RunPodSandbox, "b", "", 0, 700),
	}
	events := []*Event{
		ev(RunPodSandbox, "a", 480),
		ev(RunPodSandbox, "b", 650),
	}

	s := statsFor(t, CorrelateOps(ops, events), RunPodSandbox)

	require.Equal(t, 2, s.Correlated)
	require.Equal(t, 480*time.Millisecond, s.Before.Min)
	require.Equal(t, 650*time.Millisecond, s.Before.Max)
	require.Equal(t, 20*time.Millisecond, s.After.Min)
	require.Equal(t, 50*time.Millisecond, s.After.Max)
}

// Reporting a phase's failed calls. A burst's goroutines cannot use require,
// so every call is issued with its error collected here instead; a phase whose
// collected errors nobody asserts on prints a report that reads as a
// measurement. The messages name the phase because both directions report into
// one test, and "which direction failed" is the first thing a reader needs.
func TestOpLogFailureReportsIsEmptyWhenEveryCallSucceeded(t *testing.T) {
	l := NewOpLog(2)
	l.Add(op(RunPodSandbox, "a", "", 0, 500))
	l.Add(op(StopPodSandbox, "a", "", 1000, 1100))

	require.Empty(t, l.FailureReports("teardown"))
}

func TestOpLogFailureReportsNamesThePhaseOperationPodAndError(t *testing.T) {
	l := NewOpLog(2)

	ok := op(StopPodSandbox, "a", "", 1000, 1100)
	bad := op(RemovePodSandbox, "b", "", 1200, 1260)
	bad.Err = errors.New("no such sandbox")

	l.Add(ok)
	l.Add(bad)

	reports := l.FailureReports("teardown")

	require.Len(t, reports, 1, "only the failed call is reported")
	require.Contains(t, reports[0], "teardown")
	require.Contains(t, reports[0], string(RemovePodSandbox))
	require.Contains(t, reports[0], "b")
	require.Contains(t, reports[0], "no such sandbox")
}

func TestOpLogFailureReportsReportsEveryFailure(t *testing.T) {
	l := NewOpLog(2)

	for _, pod := range []string{"a", "b"} {
		r := op(RunPodSandbox, pod, "", 0, 10)
		r.Err = errors.New("boom")
		l.Add(r)
	}

	require.Len(t, l.FailureReports("burst"), 2,
		"one message per failed call, not one for the first")
}
