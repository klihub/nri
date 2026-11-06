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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// orderOf returns the plugin names that recorded the given event type, in the
// order the events were recorded.
func orderOf(q *framework.EventQ, podID string, t framework.EventType) []string {
	var order []string
	for _, e := range q.Events() {
		if e.Type == t && e.PodID == podID {
			order = append(order, e.Plugin)
		}
	}

	return order
}

// waitForEach waits until every one of the given plugins has recorded the given
// event for the pod.
//
// Every event-based assertion in the suite has to wait first. NRI's own dispatch
// is serial, but whether the runtime invokes the NRI hook inline in the CRI
// handler that returned to us is a containerd and CRI-O implementation detail. A
// record that lands a moment late would otherwise be read as an ordering
// violation -- "plugins must be invoked in ascending index order" for what is
// really a timing problem, which is the exact misdirection this suite keeps being
// burned by.
func waitForEach(t *testing.T, s *framework.Suite, pod *framework.Pod,
	event framework.EventType, plugins ...*framework.TestPlugin) {
	t.Helper()

	for _, p := range plugins {
		_, err := s.Q().WaitFor(&framework.Event{
			Type:   event,
			Plugin: p.FullName(),
			PodID:  pod.ID,
		}, s.Timeout())
		require.NoError(t, err, "plugin %s did not receive %s", p.FullName(), event)
	}
}

func TestMultiPluginOrdering(t *testing.T) {
	orderingTable := func(event framework.EventType, subscriptions ...string) func(*testing.T) {
		return func(t *testing.T) {
			s := framework.NewSuite(t, "order-"+string(event))
			requireUpdatesIfSubscribed(t, subscriptions...)

			mask := api.MustParseEventMask(subscriptions...)

			// Started out of index order deliberately: the runtime must sort by
			// index, not by connection order.
			var (
				mid  = s.NewPlugin("mid", "20", mask)
				last = s.NewPlugin("last", "99", mask)
				frst = s.NewPlugin("first", "00", mask)
			)
			s.StartPlugins(mid, last, frst)

			pod, _ := runLifecycle(t, s)

			waitForEach(t, s, pod, event, frst, mid, last)

			require.Equal(t,
				[]string{frst.FullName(), mid.FullName(), last.FullName()},
				orderOf(s.Q(), pod.ID, event),
				"plugins must be invoked in ascending index order")
		}
	}

	t.Run("should invoke plugins in index order", func(t *testing.T) {
		t.Run("for RunPodSandbox", orderingTable(framework.RunPodSandbox,
			"RunPodSandbox"))
		t.Run("for CreateContainer", orderingTable(framework.CreateContainer,
			"CreateContainer"))
		t.Run("for StartContainer", orderingTable(framework.StartContainer,
			"StartContainer"))
		t.Run("for StopContainer", orderingTable(framework.StopContainer,
			"StopContainer"))
		t.Run("for RemovePodSandbox", orderingTable(framework.RemovePodSandbox,
			"RemovePodSandbox"))

		t.Run("with all container requests", orderingTable(framework.CreateContainer,
			"CreateContainer,StartContainer,UpdateContainer,StopContainer,RemoveContainer"))
		t.Run("with all pod and container requests and events",
			orderingTable(framework.StartContainer,
				"RunPodSandbox,StopPodSandbox,RemovePodSandbox",
				"CreateContainer,PostCreateContainer",
				"StartContainer,PostStartContainer",
				"UpdateContainer,PostUpdateContainer",
				"StopContainer",
				"RemoveContainer",
			))
	})

	t.Run("should invoke every subscribed plugin exactly once per event", func(t *testing.T) {
		s := framework.NewSuite(t, "order-once")

		mask := api.MustParseEventMask("RunPodSandbox")

		var (
			p0 = s.NewPlugin("first", "00", mask)
			p1 = s.NewPlugin("mid", "20", mask)
			p2 = s.NewPlugin("last", "99", mask)
		)
		s.StartPlugins(p0, p1, p2)

		pod, _ := runLifecycle(t, s)

		// Wait before counting, for the reason waitForEach documents: a record that
		// has not arrived yet would be counted as zero and read as the runtime
		// having skipped the plugin.
		waitForEach(t, s, pod, framework.RunPodSandbox, p0, p1, p2)

		for _, p := range []*framework.TestPlugin{p0, p1, p2} {
			var count int
			for _, e := range s.Q().EventsFor(p.FullName()) {
				if e.Type == framework.RunPodSandbox && e.PodID == pod.ID {
					count++
				}
			}
			require.Equal(t, 1, count,
				"plugin %s should receive RunPodSandbox exactly once", p.FullName())
		}
	})
}
