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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// runLifecycle drives a pod and one container through their whole lifecycle,
// exercising every NRI hook and recording the events a plugin receives.
//
// It creates a pod with a container, starts the container, updates container
// resources, stops and removes the container if the driver can do that apart from its
// pod, then stops and removes the pod. Either way the sequence exercises RunPodSandbox, CreateContainer, PostCreateContainer,
// StartContainer, PostStartContainer, UpdateContainer, PostUpdateContainer,
// StopContainer, RemoveContainer, StopPodSandbox, and RemovePodSandbox hooks.
func runLifecycle(t *testing.T, s *framework.Suite) (*framework.Pod, *framework.Container) {
	t.Helper()

	pod, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

	// Every call goes through the Suite, so each one is bounded by
	// Config.Timeout. See Suite.do for why a bare context.Background() here is a
	// leak waiting to happen.
	s.StartContainer(ctr)
	if s.Driver().Capabilities().Has(framework.CapUpdateContainerResources) {
		s.UpdateContainerResources(ctr, &framework.Resources{CPUShares: 512})
	}
	removeWorkload(s, pod, ctr)

	return pod, ctr
}

// removeWorkload takes a pod and its container down. The container is stopped and
// removed on its own first when the driver can, which is how the runtime is asked
// for those two events directly; otherwise they are the runtime's to send when the
// pod goes.
func removeWorkload(s *framework.Suite, pod *framework.Pod, ctr *framework.Container) {
	if s.Driver().Capabilities().Has(framework.CapContainerLifecycle) {
		s.StopContainer(ctr)
		s.RemoveContainer(ctr)
	}

	s.StopPod(pod)
	s.RemovePod(pod)
}

// requireUpdatesIfSubscribed skips a case that subscribes to an update event
// when the driver cannot make the runtime send one. A case that does not
// subscribe to one runs with every driver: runLifecycle only updates the
// container when it can.
func requireUpdatesIfSubscribed(t *testing.T, subscriptions ...string) {
	t.Helper()

	for _, mask := range subscriptions {
		if strings.Contains(mask, "UpdateContainer") {
			framework.RequireCaps(t, framework.CapUpdateContainerResources)
			return
		}
	}
}

func TestPodAndContainerRequestsAndEvents(t *testing.T) {
	t.Run("should succeed when no plugin is connected", func(t *testing.T) {
		s := framework.NewSuite(t, "events-no-plugins")

		runLifecycle(t, s)
	})
}

// subscriptionTable checks that a plugin subscribed to exactly the given event
// masks receives every event it subscribed to, and nothing it did not.
func subscriptionTable(subscriptions ...string) func(*testing.T) {
	return func(t *testing.T) {
		s := framework.NewSuite(t, "events-subscribed")
		requireUpdatesIfSubscribed(t, subscriptions...)

		plugin := s.NewPlugin("subscribed", "00", api.MustParseEventMask(subscriptions...))
		s.StartPlugins(plugin)

		pod, _ := runLifecycle(t, s)

		var subscribed []framework.EventType
		for _, mask := range subscriptions {
			for _, name := range strings.Split(mask, ",") {
				subscribed = append(subscribed, framework.EventType(name))
			}
		}

		for _, want := range subscribed {
			_, err := s.Q().WaitFor(&framework.Event{
				Type:   want,
				Plugin: plugin.FullName(),
				PodID:  pod.ID,
			}, s.Timeout())
			require.NoError(t, err,
				"plugin subscribed to %s did not receive it", want)
		}

		// Nothing beyond the subscription, and nothing from another workload. The
		// plugin lifecycle events are exempt: nothing subscribes to them. Asking
		// IsHook rather than listing them here is what keeps the exemption complete
		// -- the hand-written list this replaced omitted Shutdown and Disconnected.
		for _, got := range s.Q().EventsFor(plugin.FullName()) {
			if !got.Type.IsHook() {
				continue
			}

			require.Contains(t, subscribed, got.Type,
				"plugin received %s without subscribing to it", got.Type)
			require.Equal(t, pod.ID, got.PodID,
				"plugin received an event for a workload it does not own")
		}
	}
}

func TestEventSubscriptions(t *testing.T) {
	t.Run("should honor a plugin's event subscriptions", func(t *testing.T) {
		t.Run("with RunPodSandbox", subscriptionTable("RunPodSandbox"))
		t.Run("with StopPodSandbox", subscriptionTable("StopPodSandbox"))
		t.Run("with RemovePodSandbox", subscriptionTable("RemovePodSandbox"))
		t.Run("with CreateContainer", subscriptionTable("CreateContainer"))
		t.Run("with PostCreateContainer", subscriptionTable("PostCreateContainer"))
		t.Run("with StartContainer", subscriptionTable("StartContainer"))
		t.Run("with PostStartContainer", subscriptionTable("PostStartContainer"))
		t.Run("with UpdateContainer", subscriptionTable("UpdateContainer"))
		t.Run("with PostUpdateContainer", subscriptionTable("PostUpdateContainer"))
		t.Run("with StopContainer", subscriptionTable("StopContainer"))
		t.Run("with RemoveContainer", subscriptionTable("RemoveContainer"))

		t.Run("with all pod events",
			subscriptionTable("RunPodSandbox,StopPodSandbox,RemovePodSandbox"))
		t.Run("with all container requests",
			subscriptionTable("CreateContainer,UpdateContainer,StopContainer"))
		t.Run("with all container requests and events", subscriptionTable(
			"CreateContainer,PostCreateContainer",
			"StartContainer,PostStartContainer",
			"UpdateContainer,PostUpdateContainer",
			"StopContainer",
			"RemoveContainer",
		))
		t.Run("with all pod and container requests and events", subscriptionTable(
			"RunPodSandbox,StopPodSandbox,RemovePodSandbox",
			"CreateContainer,PostCreateContainer",
			"StartContainer,PostStartContainer",
			"UpdateContainer,PostUpdateContainer",
			"StopContainer",
			"RemoveContainer",
		))
	})

	// UpdatePodSandbox and PostUpdatePodSandbox are deliberately absent from the
	// table above. See TestPodResourceUpdateEvents.
}

func TestPodResourceUpdateEvents(t *testing.T) {
	t.Run("should deliver UpdatePodSandbox and PostUpdatePodSandbox", func(t *testing.T) {
		// TODO: pod-level resource updates are not known to be fully implemented
		// in containerd or CRI-O, and the CRI-to-NRI mapping for
		// UpdatePodSandboxResources is unverified. When a runtime does implement
		// it, drop this skip, subscribe to
		// "UpdatePodSandbox,PostUpdatePodSandbox", call
		// Driver.UpdatePodResources, and assert both events arrive for the pod.
		framework.Skipped(t, "pod resource updates are not verified in any runtime yet; "+
			"see TODO in TestPodResourceUpdateEvents")

		s := framework.NewSuite(t, "events-pod-update")
		framework.RequireCaps(t, framework.CapUpdatePodResources)

		plugin := s.NewPlugin("podupdate", "00",
			api.MustParseEventMask("UpdatePodSandbox,PostUpdatePodSandbox"))
		s.StartPlugins(plugin)

		pod := s.CreatePod(s.PodSpec("pod"))

		err := s.Driver().UpdatePodResources(context.Background(), pod,
			&framework.Resources{CPUShares: 512})
		require.NoError(t, err)

		for _, want := range []framework.EventType{
			framework.UpdatePodSandbox,
			framework.PostUpdatePodSandbox,
		} {
			_, err := s.Q().WaitFor(&framework.Event{
				Type:   want,
				Plugin: plugin.FullName(),
				PodID:  pod.ID,
			}, s.Timeout())
			require.NoError(t, err, "plugin did not receive %s", want)
		}
	})
}
