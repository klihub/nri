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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// onlineSet returns a kernel online list such as "0-15", or "" when the node
// does not expose it. The suite runs on the node, so this is the node's own.
func onlineSet(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Logf("cannot read %s: %v", path, err)
		return ""
	}

	return strings.TrimSpace(string(raw))
}

func TestSmoke(t *testing.T) {
	t.Run("should deliver a full pod and container lifecycle to a plugin", func(t *testing.T) {
		s := framework.NewSuite(t, "smoke")

		plugin := s.NewPlugin("smoke", "00", api.MustParseEventMask(
			"RunPodSandbox,StopPodSandbox,RemovePodSandbox",
			"CreateContainer,PostCreateContainer",
			"StartContainer,PostStartContainer",
			"StopContainer,RemoveContainer",
		))
		s.StartPlugins(plugin)

		pod, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))
		name := plugin.FullName()

		s.StartContainer(ctr)
		removeWorkload(s, pod, ctr)

		for _, want := range []framework.EventType{
			framework.RunPodSandbox,
			framework.CreateContainer,
			framework.PostCreateContainer,
			framework.StartContainer,
			framework.PostStartContainer,
			framework.StopContainer,
			framework.RemoveContainer,
			framework.StopPodSandbox,
			framework.RemovePodSandbox,
		} {
			_, err := s.Q().WaitFor(
				&framework.Event{Type: want, Plugin: name, PodID: pod.ID},
				s.Timeout(),
			)
			require.NoError(t, err, "plugin %s did not receive %s", name, want)
		}
	})

	t.Run("should deliver the container's stop and removal when only its pod is removed", func(t *testing.T) {
		// The path a driver that cannot touch containers apart from their pod has,
		// and so what the other tests rely on when they skip the per-container steps.
		s := framework.NewSuite(t, "smoke-pod-teardown")

		plugin := s.NewPlugin("teardown", "00", api.MustParseEventMask(
			"StopContainer,RemoveContainer",
		))
		s.StartPlugins(plugin)

		pod, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))
		s.StartContainer(ctr)
		s.StopPod(pod)
		s.RemovePod(pod)

		for _, want := range []framework.EventType{framework.StopContainer, framework.RemoveContainer} {
			_, err := s.Q().WaitFor(&framework.Event{
				Type: want, Plugin: plugin.FullName(), PodID: pod.ID, CtrID: ctr.ID,
			}, s.Timeout())
			require.NoError(t, err, "plugin %s did not receive %s", plugin.FullName(), want)
		}
	})

	t.Run("should expose the created container's OCI spec", func(t *testing.T) {
		s := framework.NewSuite(t, "smoke-inspect")

		ctrSpec := s.CtrSpec("ctr0")
		// Set a distinctive environment variable that the test itself controls,
		// so we can verify the inspector returns THIS container's spec, not just any spec.
		envValue := "run-" + framework.TestConfig.RunID
		ctrSpec.Env = map[string]string{
			"NRI_TEST_SMOKE": envValue,
		}
		_, ctr := s.CreatePodWithContainer("pod", ctrSpec)

		o := s.Inspect(ctr.ID)
		require.NotNil(t, o.Spec)
		require.NotNil(t, o.Spec.Process)
		require.NotNil(t, o.Spec.Root)
		require.NotNil(t, o.Spec.Linux)
		// Created when the driver leaves the container for the test to start, running
		// when the driver's containers start on their own. The spec is the same.
		require.Contains(t, []framework.ContainerState{
			framework.StateCreated, framework.StateRunning,
		}, o.State)

		// CRI pod and container annotations do not reach the container's OCI spec
		// unless the runtime is configured to pass them through (containerd's
		// PodAnnotations/ContainerAnnotations allowlists, empty by default).
		// This says nothing about NRI adjustments, which write annotations into
		// the spec directly through a different mechanism.

		// Verify the spec is THIS container's by checking the environment variable
		// we set. Both containerd and CRI-O must honour CRI container env.
		envKey := "NRI_TEST_SMOKE=" + envValue
		require.Contains(t, o.Spec.Process.Env, envKey,
			"the container's OCI spec should contain the test-set environment variable")

		// Verify the spec belongs to this specific container by checking that
		// the cgroup path contains the container id.
		require.Contains(t, o.Spec.Linux.CgroupsPath, ctr.ID,
			"the container's cgroup path should contain its container id")

		// Verify the spec is valid and present.
		require.NotEmpty(t, o.Spec.Version,
			"the container's OCI spec version should be set")
	})

	t.Run("should observe from inside a container what the kernel applied to it", func(t *testing.T) {
		// The end-to-end proof of exec-based verification: an image with a shell, a
		// keep-alive command that outlives the test, a started container, ExecSync,
		// the /proc/self/status parser, and an assertion on what the kernel says.
		// Everything the adjustment tests will read from inside a container goes
		// through this same chain, so if this subtest passes the chain works and if
		// it fails nothing built on it can be trusted.
		s := framework.NewSuite(t, "smoke-exec")
		framework.RequireCaps(t, framework.CapExec)

		ctrSpec := s.CtrSpec("ctr0")
		// A driver that applies a cpuset verbatim pins CPU 0, because every
		// machine has one, so this is safe on any host. Set through the driver
		// rather than through an NRI adjustment on purpose: what is under test
		// here is the observation channel, and an adjustment would put NRI in
		// the way of the thing measuring it.
		//
		// A driver that cannot pin leaves the container with what the node
		// allows it, which is the whole online set: the Kubernetes driver
		// refuses to run unless the kubelet's CPU and memory managers are off.
		wantCPUs, wantMems := onlineSet(t, "/sys/devices/system/cpu/online"),
			onlineSet(t, "/sys/devices/system/node/online")
		require.NotEmpty(t, wantCPUs, "the node's online CPUs must be readable")
		pinned := s.Driver().Capabilities().Has(framework.CapRawResources)
		if pinned {
			wantCPUs = "0"
			ctrSpec.Resources = &framework.Resources{CpusetCpus: wantCPUs}
		}

		_, ctr := s.CreatePodWithContainer("pod", ctrSpec)

		// Started, not merely created: exec needs a running container. Spec-only
		// assertions deliberately skip this, and pay no flake for it.
		s.StartContainer(ctr)

		status := s.ProcSelfStatus(ctr)

		// A positive control on the read, asserted before anything is read out of
		// it. Without it an empty map -- an exec that returned nothing, a parser
		// that dropped every line -- would present as an affinity of "" against an
		// expectation of "0", blaming cpuset for a broken channel.
		require.Contains(t, status, "Name",
			"the parsed /proc/self/status must carry a Name field; got %v", status)

		// The kernel's own answer, which is what makes this authoritative: the OCI
		// spec would only say what the runtime recorded.
		require.Equal(t, wantCPUs, status["Cpus_allowed_list"],
			"container %s was created with CpusetCpus %q, so the kernel inside it "+
				"must report Cpus_allowed_list %q; it reported %q",
			ctr.ID, wantCPUs, wantCPUs, status["Cpus_allowed_list"])

		// Only with two or more NUMA nodes: a single-node machine reports "0"
		// whatever the container was given, so the check would say nothing.
		if !pinned && strings.ContainsAny(wantMems, "-,") {
			require.Equal(t, wantMems, status["Mems_allowed_list"],
				"an unpinned container %s must see every online memory node", ctr.ID)
		}
	})
}
