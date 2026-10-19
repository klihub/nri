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

	p, err := NewScalePlan(n, events, delays)
	require.NoError(t, err)

	return p
}

func TestScalePlanBareDelayAppliesToEveryEvent(t *testing.T) {
	p := mustPlan(t, 3, nil, []string{"50ms"})

	for i := 0; i < 3; i++ {
		for _, ev := range simulatableEvents {
			require.Equal(t, 50*time.Millisecond, p.Delay(i, ev), "plugin %d %s", i, ev)
		}
	}
}

func TestScalePlanNoDelayMeansNone(t *testing.T) {
	p := mustPlan(t, 2, nil, nil)

	require.Zero(t, p.Delay(1, CreateContainer))
}

func TestScalePlanPerEventDelay(t *testing.T) {
	p := mustPlan(t, 2, nil, []string{"CreateContainer=200ms,StopContainer=200ms"})

	require.Equal(t, 200*time.Millisecond, p.Delay(0, CreateContainer))
	require.Equal(t, 200*time.Millisecond, p.Delay(1, StopContainer))
	require.Zero(t, p.Delay(1, RunPodSandbox))
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
			_, err := NewScalePlan(tc.n, tc.events, tc.delays)
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

func TestScalePlanDescribeSkipsEventsTheRecorderDoesNotGet(t *testing.T) {
	p := mustPlan(t, 1, nil, []string{"50ms"})

	d := p.Describe()

	require.Contains(t, d[0], "CreateContainer=50ms")
	require.NotContains(t, d[0], "UpdateContainer")
	require.NotContains(t, d[0], "UpdatePodSandbox")
}
