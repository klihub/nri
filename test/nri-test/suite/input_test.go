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
	"strconv"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// Input conformance: what a runtime tells a plugin about a pod and a container,
// as opposed to what it does with what a plugin returns.
//
// This is the layer the adjustment tests rest on. Those inject constants, so
// they would pass whether or not the Container a plugin was handed bore any
// resemblance to the container being created -- while a real plugin computes
// its adjustment from the input. The field table these tests iterate is
// framework.InputFields.
//
// Two forms, and the difference matters to what a failure means. A fidelity
// test sets the value over CRI and reads it back off the hook request, so a
// failure is a runtime that did not report what it was given. An agreement test
// compares the plugin's view against what the runtime realized, so a failure is
// a runtime whose report and whose container disagree.

// inputHooks is every hook the input tests read a payload from.
var inputHooks = []string{
	"RunPodSandbox,StopPodSandbox,RemovePodSandbox",
	"CreateContainer,PostCreateContainer",
	"StartContainer,PostStartContainer",
	"StopContainer",
	"RemoveContainer",
}

// inputPlugin returns a started plugin that retains what every input hook was
// given.
//
// Capture before Start, never after: the hooks run on the plugin's own
// goroutine, so flipping it afterwards is a data race and cannot say which
// hooks it applied to.
func inputPlugin(s *framework.Suite) *framework.TestPlugin {
	p := s.NewPlugin("input", "00", api.MustParseEventMask(inputHooks...))
	p.Capture(true)
	s.StartPlugins(p)

	return p
}

func TestInputFidelity(t *testing.T) {
	t.Run("should report the pod's identity at RunPodSandbox", func(t *testing.T) {
		s := framework.NewSuite(t, "input-pod-identity")

		plugin := inputPlugin(s)
		spec := s.PodSpec("pod0")
		pod := s.CreatePod(spec)

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		s.ExpectShown("name", framework.ExpectShownScalar(
			"name", spec.Name, got.Pod.GetName()))
		s.ExpectShown("uid", framework.ExpectShownScalar(
			"uid", spec.UID, got.Pod.GetUid()))
		s.ExpectShown("labels", framework.ExpectShownMap(
			"labels", spec.Labels, got.Pod.GetLabels()))
		s.ExpectShown("annotations", framework.ExpectShownMap(
			"annotations", spec.Annotations, got.Pod.GetAnnotations()))
	})

	t.Run("should report the runtime handler at RunPodSandbox", func(t *testing.T) {
		// Only where the run selected one. With none selected the runtime's
		// default applies, and NRI reports the handler's name rather than the
		// empty string the request carried -- so an expectation of "" would fail
		// against a correct runtime.
		s := framework.NewSuite(t, "input-pod-handler")

		spec := s.PodSpec("pod0")
		if spec.RuntimeHandler == "" {
			framework.Skipped(t, "no runtime handler selected for this run, so "+
				"there is no value to compare the reported one against; run with "+
				"-nri-test.runtime-handler to measure it")
		}

		plugin := inputPlugin(s)
		pod := s.CreatePod(spec)

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		s.ExpectShown("runtime_handler", framework.ExpectShownScalar(
			"runtime_handler", spec.RuntimeHandler, got.Pod.GetRuntimeHandler()))
	})

	t.Run("should report the pod's cgroup parent at RunPodSandbox", func(t *testing.T) {
		s := framework.NewSuite(t, "input-pod-cgroup")

		plugin := inputPlugin(s)
		spec := s.PodSpec("pod0")
		spec.CgroupParent = "nri-test-input.slice"
		pod := s.CreatePod(spec)

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		s.ExpectShown("cgroup_parent", framework.ExpectShownScalar(
			"cgroup_parent", spec.CgroupParent, got.Pod.GetLinux().GetCgroupParent()))
	})

	t.Run("should report the pod's overhead and resources at RunPodSandbox", func(t *testing.T) {
		s := framework.NewSuite(t, "input-pod-resources")
		framework.RequireCaps(t, framework.CapRawResources)

		plugin := inputPlugin(s)
		spec := s.PodSpec("pod0")
		spec.PodOverhead = &framework.Resources{MemoryLimit: 16 << 20}
		spec.PodResources = &framework.Resources{CPUShares: 512}
		pod := s.CreatePod(spec)

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		linux := got.Pod.GetLinux()
		s.ExpectShown("pod_overhead", framework.ExpectShownScalar(
			"pod_overhead", int64(16<<20),
			linux.GetPodOverhead().GetMemory().GetLimit().GetValue()))
		s.ExpectShown("pod_resources", framework.ExpectShownScalar(
			"pod_resources", uint64(512),
			linux.GetPodResources().GetCpu().GetShares().GetValue()))
	})

	t.Run("should report the container's identity at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-identity")

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		pod, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		s.ExpectShown("name", framework.ExpectShownScalar(
			"name", ctrSpec.Name, got.Ctr.GetName()))
		s.ExpectShown("pod_sandbox_id", framework.ExpectShownScalar(
			"pod_sandbox_id", pod.ID, got.Ctr.GetPodSandboxId()))
	})

	t.Run("should report container labels and annotations at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-metadata")
		framework.RequireCaps(t, framework.CapContainerMetadata)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// Distinct keys, deliberately: one key in both maps cannot say which of
		// the two a reported value came from, and the first run of this test
		// showed the annotation carrying the label's value.
		ctrSpec.Annotations["nri-test.containerd.io/input-annotation"] = "annotated"
		ctrSpec.Labels["nri-test.containerd.io/input-label"] = "labelled"
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		s.ExpectShown("annotations", framework.ExpectShownMap(
			"annotations", ctrSpec.Annotations, got.Ctr.GetAnnotations()))
		s.ExpectShown("labels", framework.ExpectShownMap(
			"labels", ctrSpec.Labels, got.Ctr.GetLabels()))
	})

	t.Run("should report the container's command, args and env at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-process")

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		ctrSpec.Env = map[string]string{"NRI_TEST_INPUT": "yes"}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		// Container.args carries the whole command line, entry point first: NRI
		// has one field where CRI has Command and Args.
		s.ExpectShown("args", framework.ExpectShownStrings(
			"args", ctrSpec.Command, got.Ctr.GetArgs()))
		s.ExpectShown("env", framework.ExpectShownStrings(
			"env", []string{"NRI_TEST_INPUT=yes"}, got.Ctr.GetEnv()))
	})

	t.Run("should report the container's mounts at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-mounts")

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		ctrSpec.Mounts = []*framework.Mount{
			{Source: "/tmp", Destination: "/nri-test-input", Readonly: true},
		}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		var destinations []string
		for _, m := range got.Ctr.GetMounts() {
			destinations = append(destinations, m.GetDestination())
		}

		s.ExpectShown("mounts", framework.ExpectShownStrings(
			"mounts", []string{"/nri-test-input"}, destinations))
	})

	t.Run("should report the container's image at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-image")

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		image := got.Ctr.GetImage()

		// Two findings, not one. "the image message is empty" and "the name is
		// empty but the digest is there" are different statements about a
		// runtime, and a reader of the summary acts on them differently.
		s.ExpectShown("image", framework.ExpectPopulated("image",
			len(image.GetName())+len(image.GetDigest())+len(image.GetConfigDigest()), 1))
		s.ExpectShown("image.name", framework.ExpectShownScalar(
			"image.name", ctrSpec.Image, image.GetName()))
	})

	t.Run("should report sysctls at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-sysctl")
		framework.RequireCaps(t, framework.CapSysctls)

		plugin := inputPlugin(s)
		spec := s.PodSpec("pod0", s.CtrSpec("ctr0"))
		// Set on the pod, where CRI carries it, and read back per container,
		// where NRI reports it. A non-default value: the kernel's own default
		// for ip_forward is 0, so 1 cannot be satisfied by a container nobody
		// configured.
		spec.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"}
		pod := s.CreatePod(spec)
		ctr := pod.Containers[0]

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		s.ExpectShown("sysctl", framework.ExpectShownMap(
			"sysctl", spec.Sysctls, got.Ctr.GetLinux().GetSysctl()))
	})

	t.Run("should report the seccomp profile at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-seccomp")
		framework.RequireCaps(t, framework.CapSecurityContext)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// Unconfined, which is distinguishable from the runtime's default and
		// needs no profile file on the node.
		ctrSpec.SeccompProfile = &framework.SeccompProfile{
			Type: framework.SeccompUnconfined,
		}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		s.ExpectShown("seccomp_profile", framework.ExpectShownScalar(
			"seccomp_profile", api.SecurityProfile_UNCONFINED,
			got.Ctr.GetLinux().GetSeccompProfile().GetProfileType()))
	})

	t.Run("should report the oom score adjustment at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-oom")
		framework.RequireCaps(t, framework.CapRawResources)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// Not zero. Zero is CRI's default, so an expectation of zero passes
		// whether or not the runtime read the field at all.
		adj := int64(-17)
		ctrSpec.OOMScoreAdj = &adj
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		s.ExpectShown("oom_score_adj", framework.ExpectShownScalar(
			"oom_score_adj", int64(-17),
			got.Ctr.GetLinux().GetOomScoreAdj().GetValue()))
	})

	t.Run("should report the user and groups at CreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "input-ctr-user")
		framework.RequireCaps(t, framework.CapSecurityContext)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// Non-zero literals, away from the image's root default, so the
		// assertion cannot be satisfied by a container nobody configured.
		uid, gid := int64(1234), int64(1234)
		ctrSpec.RunAsUser = &uid
		ctrSpec.RunAsGroup = &gid
		ctrSpec.SupplementalGroups = []int64{4321}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		user := got.Ctr.GetUser()
		s.ExpectShown("user", framework.ExpectShownScalar(
			"user.uid", uint32(1234), user.GetUid()))
		s.ExpectShown("user", framework.ExpectShownScalar(
			"user.gid", uint32(1234), user.GetGid()))

		var additional []string
		for _, g := range user.GetAdditionalGids() {
			additional = append(additional, strconv.FormatUint(uint64(g), 10))
		}

		s.ExpectShown("user", framework.ExpectShownStrings(
			"user.additional_gids", []string{"4321"}, additional))
	})
}
