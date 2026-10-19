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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mustPlan(t *testing.T, n int, events, delays []string) *ScalePlan {
	t.Helper()

	p, err := NewScalePlan(n, events, inlineDelays(delays...))
	require.NoError(t, err)

	return p
}

// inlineDelays is the -nri-test.scale-delay half of a delay source list, for
// the tests that do not exercise a file.
func inlineDelays(specs ...string) []DelaySource {
	var sources []DelaySource

	for _, s := range specs {
		sources = append(sources, DelaySource{Spec: s})
	}

	return sources
}

// A bare duration is a wildcard, and a wildcard covers the simulated plugins
// only. Delaying the recorder perturbs the instrument the run measures with,
// and -nri-test.scale-events has always read an indexless list as "every
// simulated plugin", so this is also what makes the two flags agree about what
// no index means.
func TestScalePlanBareDelayAppliesToEverySimulatedPluginAndEvent(t *testing.T) {
	p := mustPlan(t, 3, nil, []string{"50ms"})

	for _, ev := range simulatableEvents {
		require.Equal(t, 50*time.Millisecond, p.Delay(1, ev), "plugin 1 %s", ev)
		require.Equal(t, 50*time.Millisecond, p.Delay(2, ev), "plugin 2 %s", ev)
		require.Zero(t, p.Delay(0, ev), "the recorder must never be delayed: %s", ev)
	}
}

// Naming the recorder outright is a mistake worth a refusal rather than a
// warning: NewScalePlan promises to refuse rather than simulate something
// other than what was asked for, and these runs produce published numbers.
func TestScalePlanRefusesToDelayTheRecorder(t *testing.T) {
	_, err := NewScalePlan(2, nil, inlineDelays("0/CreateContainer=50ms"))
	require.ErrorContains(t, err, "recorder")

	_, err = NewScalePlan(2, nil, inlineDelays("0/all=1ms"))
	require.ErrorContains(t, err, "recorder")

	// Asking for no delay is not asking to delay it.
	_, err = NewScalePlan(2, nil, inlineDelays("0/CreateContainer=0s"))
	require.NoError(t, err)
}

// The same mistake -nri-test.scale-events refuses: a wildcard with nothing to
// apply it to was accepted and then did nothing.
func TestScalePlanRefusesAWildcardDelayWithNoSimulatedPlugin(t *testing.T) {
	_, err := NewScalePlan(1, nil, inlineDelays("50ms"))
	require.ErrorContains(t, err, "no simulated plugins")

	_, err = NewScalePlan(1, nil, inlineDelays("CreateContainer=50ms"))
	require.ErrorContains(t, err, "no simulated plugins")
}

func TestScalePlanNoDelayMeansNone(t *testing.T) {
	p := mustPlan(t, 2, nil, nil)

	require.Zero(t, p.Delay(1, CreateContainer))
}

func TestScalePlanPerEventDelay(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"CreateContainer=200ms,StopContainer=200ms"})

	require.Equal(t, 200*time.Millisecond, p.Delay(1, CreateContainer))
	require.Equal(t, 200*time.Millisecond, p.Delay(1, StopContainer))
	require.Zero(t, p.Delay(1, RunPodSandbox))
	require.Zero(t, p.Delay(0, CreateContainer), "an event selector is still a wildcard over plugins")
}

func TestScalePlanPerPluginDelay(t *testing.T) {
	p := mustPlan(t, 3, nil, []string{"2/CreateContainer=1s"})

	require.Equal(t, time.Second, p.Delay(2, CreateContainer))
	require.Zero(t, p.Delay(1, CreateContainer))
	require.Zero(t, p.Delay(0, CreateContainer))
}

func TestScalePlanLaterRuleWins(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"50ms", "CreateContainer=200ms"})

	require.Equal(t, 200*time.Millisecond, p.Delay(1, CreateContainer))
	require.Equal(t, 50*time.Millisecond, p.Delay(1, StartContainer))

	p = mustPlan(t, 2, nil, []string{"CreateContainer=200ms", "50ms"})

	require.Equal(t, 50*time.Millisecond, p.Delay(1, CreateContainer))
}

func TestScalePlanDefaultEventsExcludeValidation(t *testing.T) {
	p := mustPlan(t, 2, nil, nil)

	require.NotContains(t, p.Events(1), ValidateContainerAdjustment)
	require.Contains(t, p.Events(1), UpdateContainer)
	require.Len(t, p.Events(1), 13)

	mask, err := p.Mask(1)
	require.NoError(t, err)
	require.NotZero(t, mask)
}

func TestScalePlanEventsForOnePlugin(t *testing.T) {
	p := mustPlan(t, 3, []string{"2=RunPodSandbox,StartContainer"}, nil)

	require.Equal(t, []EventType{RunPodSandbox, StartContainer}, p.Events(2))
	require.Len(t, p.Events(1), 13)
}

func TestScalePlanEventsWithoutIndexApplyToAllSimulated(t *testing.T) {
	p := mustPlan(t, 3, []string{"CreateContainer"}, nil)

	require.Equal(t, []EventType{CreateContainer}, p.Events(1))
	require.Equal(t, []EventType{CreateContainer}, p.Events(2))
}

func TestScalePlanRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		n      int
		events []string
		delays []string
	}{
		"no plugins":        {n: 0},
		"unknown event":     {n: 2, delays: []string{"Bogus=1s"}},
		"validate event":    {n: 2, events: []string{"ValidateContainerAdjustment"}},
		"bad duration":      {n: 2, delays: []string{"fast"}},
		"negative duration": {n: 2, delays: []string{"-1s"}},
		"recorder events":   {n: 2, events: []string{"0=RunPodSandbox"}},
		"index too high":    {n: 2, delays: []string{"2/CreateContainer=1s"}},
		"events index high": {n: 2, events: []string{"2=RunPodSandbox"}},
		"empty entry":       {n: 2, delays: []string{"50ms,,"}},
		// A bare event list with no simulated plugin to put it on used to be
		// accepted and then ignored: the target loop ran from the first
		// simulated index to the plugin count, which is empty when the only
		// plugin is the recorder. -nri-test.scale-delay already refused the
		// equivalent, so the two flags disagreed about the same mistake.
		"events but only the recorder": {n: 1, events: []string{"CreateContainer"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewScalePlan(tc.n, tc.events, inlineDelays(tc.delays...))
			require.Error(t, err)
		})
	}
}

func TestScalePlanSimulateWorkOnlyDelaysConfiguredEvents(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"CreateContainer=40ms"})
	tp := NewTestPlugin("x", "01", 0, &EventQ{}, nil)

	p.SimulateWork(tp, 1)

	require.NotNil(t, tp.CreateContainerFn)
	require.Nil(t, tp.RunPodSandboxFn)
	require.Nil(t, tp.StopContainerFn)

	start := time.Now()
	adj, upd, err := tp.CreateContainerFn(context.Background(), nil, nil)

	require.NoError(t, err)
	require.Nil(t, adj)
	require.Nil(t, upd)
	require.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
}

// The end-to-end form of the rule: whatever a run asks for, no handler that
// waits is ever installed on the recorder.
func TestScalePlanSimulateWorkNeverDelaysTheRecorder(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"50ms", "0/CreateContainer=0s"})
	tp := NewTestPlugin("rec", "00", 0, &EventQ{}, nil)

	p.SimulateWork(tp, 0)

	require.Nil(t, tp.CreateContainerFn)
	require.Nil(t, tp.RunPodSandboxFn)
	require.Nil(t, tp.StopContainerFn)
}

func TestScalePlanSimulateWorkGivesUpWhenCancelled(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"RunPodSandbox=1h"})
	tp := NewTestPlugin("x", "01", 0, &EventQ{}, nil)

	p.SimulateWork(tp, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	require.NoError(t, tp.RunPodSandboxFn(ctx, nil))
	require.Less(t, time.Since(start), time.Minute)
}

func TestScalePlanDescribeNamesEveryPlugin(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"1/CreateContainer=200ms"})

	d := p.Describe()

	require.Len(t, d, 2)
	require.Contains(t, d[0], "delays none")
	require.Contains(t, d[1], "CreateContainer=200ms")
}

// A delay on an event the plugin does not subscribe to never fires, so
// printing it would describe a run that cannot happen.
func TestScalePlanDescribeSkipsEventsAPluginDoesNotGet(t *testing.T) {
	p := mustPlan(t, 2, []string{"1=CreateContainer,StartContainer"}, []string{"50ms"})

	d := p.Describe()

	require.Contains(t, d[1], "CreateContainer=50ms")
	require.Contains(t, d[1], "StartContainer=50ms")
	require.NotContains(t, d[1], "UpdateContainer")
	require.NotContains(t, d[1], "RunPodSandbox")
}
