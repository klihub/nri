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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
)

func testPod(id string) *api.PodSandbox {
	return &api.PodSandbox{Id: id, Name: id, Uid: "uid-" + id, Namespace: "default"}
}

func testCtr(podID, id string) *api.Container {
	return &api.Container{Id: id, PodSandboxId: podID, Name: id}
}

func TestEventMatches(t *testing.T) {
	t.Run("should match on type alone when ids are unset", func(t *testing.T) {
		e := CtrEvent("00-a", testPod("pod0"), testCtr("pod0", "ctr0"), StartContainer)
		require.True(t, e.Matches(&Event{Type: StartContainer}))
		require.False(t, e.Matches(&Event{Type: StopContainer}))
	})

	t.Run("should not match a different container", func(t *testing.T) {
		e := CtrEvent("00-a", testPod("pod0"), testCtr("pod0", "ctr0"), StartContainer)
		o := CtrEvent("00-a", testPod("pod0"), testCtr("pod0", "ctr1"), StartContainer)
		require.False(t, e.Matches(o))
	})

	t.Run("should not match a different pod", func(t *testing.T) {
		e := PodEvent("00-a", testPod("pod0"), RunPodSandbox)
		o := PodEvent("00-a", testPod("pod1"), RunPodSandbox)
		require.False(t, e.Matches(o))
	})

	t.Run("should not match a different plugin when one is given", func(t *testing.T) {
		e := PodEvent("00-a", testPod("pod0"), RunPodSandbox)
		require.True(t, e.Matches(&Event{Type: RunPodSandbox}))
		require.True(t, e.Matches(&Event{Type: RunPodSandbox, Plugin: "00-a"}))
		require.False(t, e.Matches(&Event{Type: RunPodSandbox, Plugin: "01-b"}))
	})
}

func TestEventQ(t *testing.T) {
	t.Run("should report a recorded event", func(t *testing.T) {
		q := &EventQ{}
		q.Add(PodEvent("00-a", testPod("pod0"), RunPodSandbox))
		require.True(t, q.Has(&Event{Type: RunPodSandbox}))
		require.False(t, q.Has(&Event{Type: StopPodSandbox}))
	})

	t.Run("should preserve recording order", func(t *testing.T) {
		q := &EventQ{}
		q.Add(PodEvent("00-a", testPod("pod0"), RunPodSandbox))
		q.Add(PodEvent("20-b", testPod("pod0"), RunPodSandbox))
		q.Add(PodEvent("99-c", testPod("pod0"), RunPodSandbox))

		var plugins []string
		for _, e := range q.Events() {
			plugins = append(plugins, e.Plugin)
		}
		require.Equal(t, []string{"00-a", "20-b", "99-c"}, plugins)
	})

	t.Run("should filter events by plugin", func(t *testing.T) {
		q := &EventQ{}
		q.Add(PodEvent("00-a", testPod("pod0"), RunPodSandbox))
		q.Add(PodEvent("20-b", testPod("pod0"), RunPodSandbox))

		events := q.EventsFor("20-b")
		require.Len(t, events, 1)
		require.Equal(t, "20-b", events[0].Plugin)
	})

	t.Run("should return an already recorded event immediately", func(t *testing.T) {
		q := &EventQ{}
		q.Add(PodEvent("00-a", testPod("pod0"), RunPodSandbox))

		e, err := q.WaitFor(&Event{Type: RunPodSandbox}, time.Millisecond)
		require.NoError(t, err)
		require.Equal(t, RunPodSandbox, e.Type)
	})

	t.Run("should wait for an event recorded later", func(t *testing.T) {
		q := &EventQ{}
		go func() {
			time.Sleep(10 * time.Millisecond)
			q.Add(PodEvent("00-a", testPod("pod0"), StopPodSandbox))
		}()

		e, err := q.WaitFor(&Event{Type: StopPodSandbox}, time.Second)
		require.NoError(t, err)
		require.Equal(t, StopPodSandbox, e.Type)
	})

	t.Run("should time out naming what it waited for", func(t *testing.T) {
		q := &EventQ{}
		_, err := q.WaitFor(&Event{Type: RemoveContainer}, 10*time.Millisecond)
		require.Error(t, err)
		require.Contains(t, err.Error(), string(RemoveContainer))
	})

	t.Run("should serve several concurrent waiters", func(t *testing.T) {
		q := &EventQ{}

		const waiters = 8

		errs := make(chan error, waiters)
		for range waiters {
			go func() {
				_, err := q.WaitFor(&Event{Type: PostStartContainer}, 5*time.Second)
				errs <- err
			}()
		}

		// Give the waiters time to arm, then wake them all with one event.
		time.Sleep(50 * time.Millisecond)
		q.Add(PodEvent("00-a", testPod("pod0"), PostStartContainer))

		for range waiters {
			require.NoError(t, <-errs)
		}
	})

	t.Run("should not block or panic on adds around a completed wait", func(t *testing.T) {
		// Exercises the failure mode of the original implementation: an Add
		// racing a Wait that has just returned.
		q := &EventQ{}

		done := make(chan struct{})
		go func() {
			defer close(done)

			for range 200 {
				q.Add(PodEvent("00-a", testPod("pod0"), StartContainer))
			}
		}()

		for range 50 {
			_, _ = q.WaitFor(&Event{Type: StartContainer}, time.Second)
		}

		<-done
		require.NotEmpty(t, q.Events())
	})
}

func TestEventTypeIsHook(t *testing.T) {
	// Every EventType, classified. A negative assertion -- "the plugin received
	// nothing it did not subscribe to" -- exempts the non-hooks, so a lifecycle
	// event misclassified as a hook makes every such assertion fail spuriously.
	// Disconnected is the one that matters: the stub's WithOnClose callback fires on
	// an explicit Stop too, from an unsynchronised goroutine.
	nonHooks := map[EventType]bool{
		Configured:   true,
		Synchronized: true,
		Shutdown:     true,
		Disconnected: true,
		Stopped:      true,
		// UpdateDropped is recorded by filterUpdates about the suite's own
		// behaviour, not by the runtime invoking a hook, so a negative assertion
		// must exempt it too.
		UpdateDropped: true,
	}

	all := []EventType{
		Configured, Synchronized, Shutdown, Disconnected, Stopped, UpdateDropped,

		RunPodSandbox, UpdatePodSandbox, PostUpdatePodSandbox, StopPodSandbox,
		RemovePodSandbox,

		CreateContainer, PostCreateContainer, StartContainer, PostStartContainer,
		UpdateContainer, PostUpdateContainer, StopContainer, RemoveContainer,

		ValidateContainerAdjustment,
	}

	for _, e := range all {
		require.Equal(t, !nonHooks[e], e.IsHook(), "IsHook(%s)", e)
	}

	// Cross-check against pkg/api rather than against this file's own list, so that
	// an NRI hook added upstream and not mirrored into the EventType constants above
	// fails here instead of quietly dropping out of the negative assertions.
	for _, name := range api.EventPrettyNames {
		e := EventType(name)

		require.Contains(t, all, e,
			"pkg/api knows event %s; add it to the EventType constants", name)
		require.True(t, e.IsHook(), "%s is an NRI hook", name)
	}

	// An unknown type is a hook: IsHook exempts only the listed lifecycle events.
	require.True(t, EventType("SomethingNew").IsHook())
}
