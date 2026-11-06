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
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

func TestPluginSynchronization(t *testing.T) {
	t.Run("should synchronize a late-joining plugin with existing workloads", func(t *testing.T) {
		s := framework.NewSuite(t, "sync-late-join")

		pod0, ctr0 := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))
		pod1, ctr1 := s.CreatePodWithContainer("pod1", s.CtrSpec("ctr1"))

		s.StartContainer(ctr0)
		s.StartContainer(ctr1)

		// Connect only now, so the runtime must reconcile us with what exists.
		plugin := s.NewPlugin("late", "00", 0)
		s.StartPlugins(plugin)

		pods := plugin.SyncedPods()
		require.Contains(t, pods, pod0.ID, "synchronization must include pod0")
		require.Contains(t, pods, pod1.ID, "synchronization must include pod1")

		ctrs := plugin.SyncedCtrs()
		require.Contains(t, ctrs, ctr0.ID, "synchronization must include ctr0")
		require.Contains(t, ctrs, ctr1.ID, "synchronization must include ctr1")
	})

	t.Run("should describe synchronized pods faithfully", func(t *testing.T) {
		s := framework.NewSuite(t, "sync-pod-fields")

		spec := s.PodSpec("pod0")
		pod := s.CreatePod(spec)

		plugin := s.NewPlugin("fields", "00", 0)
		s.StartPlugins(plugin)

		// Read the unfiltered protos rather than capturing through SynchronizeFn.
		// The fields measured below are the fields the scope filter keys on, so the
		// filtered view would be circular: a runtime reporting a wrong namespace
		// would be filtered out and reported as a pod the runtime never mentioned,
		// which is a different -- and wrong -- conformance finding.
		synced := plugin.RawSyncedPods()

		idx := slices.IndexFunc(synced, func(p *api.PodSandbox) bool {
			return p.GetId() == pod.ID
		})
		require.GreaterOrEqual(t, idx, 0, "our pod must appear in synchronization")

		got := synced[idx]
		require.Equal(t, spec.Name, got.GetName(), "pod name")
		require.Equal(t, spec.Namespace, got.GetNamespace(), "pod namespace")
		require.Equal(t, spec.UID, got.GetUid(), "pod uid")
		require.Equal(t, framework.TestConfig.RunID,
			got.GetLabels()[framework.RunIDLabel], "pod run-id label")
	})

	t.Run("should describe synchronized containers faithfully", func(t *testing.T) {
		s := framework.NewSuite(t, "sync-ctr-fields")

		ctrSpec := s.CtrSpec("ctr0")
		ctrSpec.Env = map[string]string{"NRI_TEST_SYNC": "yes"}
		ownMetadata := s.Driver().Capabilities().Has(framework.CapContainerMetadata)
		if ownMetadata {
			ctrSpec.Annotations["nri-test.containerd.io/sync"] = "annotated"
		}

		pod, ctr := s.CreatePodWithContainer("pod0", ctrSpec)
		s.StartContainer(ctr)

		plugin := s.NewPlugin("ctrfields", "00", 0)
		s.StartPlugins(plugin)

		// Unfiltered, like the pod fidelity subtest above: PodSandboxId is asserted
		// on below and is also what the scope filter matches containers by, so a
		// runtime reporting it wrongly must show up as a wrong pod id and not as a
		// container the runtime never mentioned.
		synced := plugin.RawSyncedCtrs()

		idx := slices.IndexFunc(synced, func(c *api.Container) bool {
			return c.GetId() == ctr.ID
		})
		require.GreaterOrEqual(t, idx, 0, "our container must appear in synchronization")

		got := synced[idx]
		require.Equal(t, ctrSpec.Name, got.GetName(), "container name")
		require.Equal(t, pod.ID, got.GetPodSandboxId(), "container pod id")
		require.Equal(t, api.ContainerState_CONTAINER_RUNNING, got.GetState(),
			"a started container must be reported as running")

		// These fields are the spec's open question about Container proto
		// fidelity. Report rather than fail, so the suite stays usable while the
		// gap is taken upstream, but say plainly what is missing.
		if !slices.Contains(got.GetEnv(), "NRI_TEST_SYNC=yes") {
			t.Errorf("runtime %s does not report container environment during "+
				"Synchronize; got env %v",
				framework.RuntimeName(), got.GetEnv())
		}
		if ownMetadata && got.GetAnnotations()["nri-test.containerd.io/sync"] != "annotated" {
			t.Errorf("runtime %s does not report container annotations during "+
				"Synchronize; got annotations %v",
				framework.RuntimeName(), got.GetAnnotations())
		}
	})

	t.Run("should synchronize a plugin that reconnects", func(t *testing.T) {
		s := framework.NewSuite(t, "sync-reconnect")

		pod := s.CreatePod(s.PodSpec("pod0"))

		first := s.NewPlugin("reconnect", "00", 0)
		s.StartPlugins(first)
		require.Contains(t, first.SyncedPods(), pod.ID)

		first.Stop()

		// A fresh plugin with the same identity stands in for a reconnect: the
		// stub does not support restarting a stopped instance.
		second := s.NewPlugin("reconnect", "00", 0)
		s.StartPlugins(second)
		require.Contains(t, second.SyncedPods(), pod.ID,
			"a reconnecting plugin must be resynchronized with existing pods")
	})
}
