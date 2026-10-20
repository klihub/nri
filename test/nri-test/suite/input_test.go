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
	"fmt"
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

		s.Covered(framework.RunPodSandbox,
			"PodSandbox.name",
			"PodSandbox.uid",
			"PodSandbox.labels",
			"PodSandbox.annotations")

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

		s.Covered(framework.RunPodSandbox,
			"PodSandbox.runtime_handler")

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

		s.Covered(framework.RunPodSandbox,
			"LinuxPodSandbox.cgroup_parent")

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

		s.Covered(framework.RunPodSandbox,
			"LinuxPodSandbox.pod_overhead",
			"LinuxPodSandbox.pod_resources")

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

		s.Covered(framework.CreateContainer,
			"Container.name",
			"Container.pod_sandbox_id")

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

		s.Covered(framework.CreateContainer,
			"Container.annotations",
			"Container.labels")

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
		s.Covered(framework.CreateContainer,
			"Container.args",
			"Container.env")

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

		s.Covered(framework.CreateContainer,
			"Container.mounts")

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

		s.Covered(framework.CreateContainer,
			"Container.image")

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

		s.Covered(framework.CreateContainer,
			"LinuxContainer.sysctl")

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

		s.Covered(framework.CreateContainer,
			"LinuxContainer.seccomp_profile")

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

		s.Covered(framework.CreateContainer,
			"LinuxContainer.oom_score_adj")

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

		s.Covered(framework.CreateContainer,
			"Container.user")

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

// The agreement tier. The plugin's view is compared against what the runtime
// realized -- the OCI spec it wrote, or CRI's own status -- so a failure is a
// runtime whose report and whose container disagree.
//
// Every check is a subset plus a non-vacuity guard. Subset alone cannot fail
// for a runtime that reports nothing, since the empty set is a subset of
// anything, and reporting nothing is the defect this tier exists to catch.
// Where a field may legitimately be empty, the subtest says so and omits the
// guard: the omission is a claim that has to be argued, not a default.

func TestInputAgreement(t *testing.T) {
	t.Run("should report rlimits matching the realized spec", func(t *testing.T) {
		// CRI has no rlimit field at all, so this can only be agreement: the
		// values are the runtime's own defaults, and the question is whether a
		// plugin is told the same ones the container got.
		s := framework.NewSuite(t, "agree-rlimits")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		// The oracle first: Inspect ends the case through fatalf, and a field
		// recorded before its oracle was obtained would be counted as measured
		// by a run that never managed to look.
		spec := s.Inspect(ctr.ID).Spec

		s.Covered(framework.CreateContainer,
			"Container.rlimits")

		var shown, realized []string
		for _, r := range got.Ctr.GetRlimits() {
			shown = append(shown, fmt.Sprintf("%s=%d:%d", r.GetType(), r.GetSoft(), r.GetHard()))
		}

		if spec != nil && spec.Process != nil {
			for _, r := range spec.Process.Rlimits {
				realized = append(realized, fmt.Sprintf("%s=%d:%d", r.Type, r.Soft, r.Hard))
			}
		}

		s.ExpectAgrees("rlimits", "realized OCI spec",
			framework.ExpectStringsSubset("rlimits", shown, realized))
		s.ExpectAgrees("rlimits", "realized OCI spec",
			framework.ExpectPopulated("rlimits", len(shown), len(realized)))
	})

	t.Run("should report namespaces matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-namespaces")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		spec := s.Inspect(ctr.ID).Spec

		s.Covered(framework.CreateContainer,
			"LinuxContainer.namespaces")

		var shown, realized []string
		for _, n := range got.Ctr.GetLinux().GetNamespaces() {
			shown = append(shown, n.GetType())
		}

		if spec != nil && spec.Linux != nil {
			for _, n := range spec.Linux.Namespaces {
				realized = append(realized, string(n.Type))
			}
		}

		s.ExpectAgrees("namespaces", "realized OCI spec",
			framework.ExpectStringsSubset("namespaces", shown, realized))
		s.ExpectAgrees("namespaces", "realized OCI spec",
			framework.ExpectPopulated("namespaces", len(shown), len(realized)))
	})

	t.Run("should report the cgroups path matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-cgroups-path")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		var realized string
		if spec := s.Inspect(ctr.ID).Spec; spec != nil && spec.Linux != nil {
			realized = spec.Linux.CgroupsPath
		}

		s.Covered(framework.CreateContainer,
			"LinuxContainer.cgroups_path")

		s.ExpectAgrees("cgroups_path", "realized OCI spec",
			framework.ExpectScalarEqual("cgroups_path",
				got.Ctr.GetLinux().GetCgroupsPath(), realized))
	})

	t.Run("should report the seccomp policy matching the realized spec", func(t *testing.T) {
		// Paired with the fidelity check on seccomp_profile. A runtime can apply
		// a profile and not report it, or report one it did not apply, and only
		// running both says which happened.
		s := framework.NewSuite(t, "agree-seccomp")
		framework.RequireCaps(t, framework.CapSecurityContext)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// The runtime's own profile, deliberately, NOT unconfined. Unconfined
		// means the realized spec carries no seccomp section at all, so the
		// only assertion that could hold is "neither side has one" -- and the
		// direction that matters, a runtime applying a policy and showing the
		// plugin nothing, would be undetectable. That is the omission case this
		// tier exists for, and arranging the state that hides it while
		// recording the field as measured is a test that cannot fail. The
		// paired fidelity check on seccomp_profile covers unconfined.
		ctrSpec.SeccompProfile = &framework.SeccompProfile{
			Type: framework.SeccompRuntimeDefault,
		}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		// The oracle before Covered: Inspect ends the case through fatalf, and
		// a field recorded before its oracle was obtained would be counted as
		// measured by a run that never managed to look.
		realizedHasSeccomp := false
		if spec := s.Inspect(ctr.ID).Spec; spec != nil && spec.Linux != nil {
			realizedHasSeccomp = spec.Linux.Seccomp != nil
		}

		s.Covered(framework.CreateContainer,
			"LinuxContainer.seccomp_policy")

		shown := got.Ctr.GetLinux().GetSeccompPolicy()

		s.ExpectAgrees("seccomp_policy", "realized OCI spec",
			framework.ExpectScalarEqual("seccomp_policy carries a policy",
				shown != nil, realizedHasSeccomp))

		// And it has to say something rather than merely exist: an empty policy
		// message is one a presence check cannot tell from a real one.
		if realizedHasSeccomp {
			s.ExpectAgrees("seccomp_policy", "realized OCI spec",
				framework.ExpectPopulated("seccomp_policy.syscalls",
					len(shown.GetSyscalls()), 1))
		}
	})

	t.Run("should report the container pid at PostStartContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pid")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))
		s.StartContainer(ctr)

		got := s.CapturedAt(plugin, framework.PostStartContainer, ctr)
		if got == nil {
			return
		}

		// The oracle first; see the seccomp subtest for why Covered follows it.
		realized := s.Inspect(ctr.ID).Pid

		s.Covered(framework.PostStartContainer,
			"Container.pid")

		s.ExpectAgrees("pid", "CRI status",
			framework.ExpectScalarEqual("pid", got.Ctr.GetPid(), realized))
		// Inspect leaves Pid at zero when the runtime reports none, so equality
		// alone passes on 0 == 0. The field table requires a pid by this hook,
		// which is the claim this second assertion rests on.
		s.ExpectAgrees("pid", "hook request",
			framework.ExpectRequired("pid", int(min(got.Ctr.GetPid(), 1))))
	})

	t.Run("should report created_at at PostCreateContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-created-at")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		got := s.CapturedAt(plugin, framework.PostCreateContainer, ctr)
		if got == nil {
			return
		}

		// Non-zero rather than equal to CRI's own timestamp: the two are read at
		// different moments and CRI reports nanoseconds since the epoch, so an
		// equality check would be comparing one clock reading against another.
		// What is in question is whether the field is populated at all.
		s.Covered(framework.PostCreateContainer,
			"Container.created_at")

		s.ExpectAgrees("created_at", "hook request",
			framework.ExpectRequired("created_at", int(min(got.Ctr.GetCreatedAt(), 1))))
	})

	t.Run("should report started_at at PostStartContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-started-at")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))
		s.StartContainer(ctr)

		got := s.CapturedAt(plugin, framework.PostStartContainer, ctr)
		if got == nil {
			return
		}

		s.Covered(framework.PostStartContainer,
			"Container.started_at")

		s.ExpectAgrees("started_at", "hook request",
			framework.ExpectRequired("started_at", int(min(got.Ctr.GetStartedAt(), 1))))
	})

	t.Run("should report pod ips at RunPodSandbox", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pod-ips")

		plugin := inputPlugin(s)
		pod := s.CreatePod(s.PodSpec("pod0"))

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		realized := s.InspectPod(pod).IPs

		s.Covered(framework.RunPodSandbox,
			"PodSandbox.ips")

		s.ExpectAgrees("ips", "CRI status",
			framework.ExpectStringsSubset("ips", got.Pod.GetIps(), realized))
		// No non-vacuity guard where the sandbox has no address of its own: a
		// host-network pod is correct with none, and this suite does not pin the
		// node's network mode.
		if len(realized) > 0 {
			s.ExpectAgrees("ips", "CRI status",
				framework.ExpectPopulated("ips", len(got.Pod.GetIps()), len(realized)))
		}
	})

	t.Run("should report the pod pid at RunPodSandbox", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pod-pid")

		plugin := inputPlugin(s)
		pod := s.CreatePod(s.PodSpec("pod0"))

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		realized := s.InspectPod(pod).Pid

		s.Covered(framework.RunPodSandbox,
			"PodSandbox.pid")

		// Agreement only where there is an oracle to agree with. CRI-O reports
		// no sandbox pid over CRI at all, and comparing against a zero we were
		// never given would blame the runtime for our own missing oracle.
		if realized > 0 {
			s.ExpectAgrees("pid", "CRI status",
				framework.ExpectScalarEqual("pid", got.Pod.GetPid(), realized))
		}

		// The field table requires a pid at this hook, whether or not CRI will
		// tell us the sandbox's own.
		s.ExpectAgrees("pid", "hook request",
			framework.ExpectRequired("pid", int(min(got.Pod.GetPid(), 1))))
	})

	// The pod's own realized state. Its oracle is the sandbox's OCI spec,
	// which both runtimes carry in the verbose PodSandboxStatus; the sandbox
	// pid, which only containerd carries, is a separate field.
	t.Run("should report the sandbox cgroups path matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pod-cgroups-path")

		plugin := inputPlugin(s)
		pod := s.CreatePod(s.PodSpec("pod0"))

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		spec := s.InspectPod(pod).Spec
		if spec == nil || spec.Linux == nil {
			framework.Skipped(t, "this runtime reports no sandbox OCI spec, so there "+
				"is nothing to compare the pod's cgroups path against")
		}

		s.Covered(framework.RunPodSandbox,
			"LinuxPodSandbox.cgroups_path")

		s.ExpectAgrees("cgroups_path", "realized OCI spec",
			framework.ExpectScalarEqual("cgroups_path",
				got.Pod.GetLinux().GetCgroupsPath(), spec.Linux.CgroupsPath))
	})

	t.Run("should report the sandbox namespaces matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pod-namespaces")

		plugin := inputPlugin(s)
		pod := s.CreatePod(s.PodSpec("pod0"))

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		spec := s.InspectPod(pod).Spec
		if spec == nil || spec.Linux == nil {
			framework.Skipped(t, "this runtime reports no sandbox OCI spec, so there "+
				"is nothing to compare the pod's namespaces against")
		}

		s.Covered(framework.RunPodSandbox,
			"LinuxPodSandbox.namespaces")

		var shown, realized []string
		for _, n := range got.Pod.GetLinux().GetNamespaces() {
			shown = append(shown, n.GetType())
		}

		for _, n := range spec.Linux.Namespaces {
			realized = append(realized, string(n.Type))
		}

		s.ExpectAgrees("namespaces", "realized OCI spec",
			framework.ExpectStringsSubset("namespaces", shown, realized))
		// A sandbox always has namespaces of its own, so an empty view is the
		// omission case rather than a pod that happens to have none.
		s.ExpectAgrees("namespaces", "realized OCI spec",
			framework.ExpectPopulated("namespaces", len(shown), len(realized)))
	})

	t.Run("should report the sandbox resources matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-pod-resources")
		framework.RequireCaps(t, framework.CapRawResources)

		plugin := inputPlugin(s)
		// A limit of the test's own choosing, so the realized side cannot be
		// empty and the comparison cannot be vacuous.
		podSpec := s.PodSpec("pod0")
		podSpec.PodResources = &framework.Resources{MemoryLimit: 200 * 1024 * 1024}
		pod := s.CreatePod(podSpec)

		got := s.CapturedPodAt(plugin, framework.RunPodSandbox, pod)
		if got == nil {
			return
		}

		spec := s.InspectPod(pod).Spec
		if spec == nil || spec.Linux == nil || spec.Linux.Resources == nil {
			framework.Skipped(t, "this runtime reports no sandbox resources in its "+
				"OCI spec, so there is nothing to compare against")
		}

		s.Covered(framework.RunPodSandbox,
			"LinuxPodSandbox.resources")

		shown := got.Pod.GetLinux().GetResources()
		s.ExpectAgrees("resources", "realized OCI spec",
			framework.ExpectRequired("resources", int(min(len(shown.String()), 1))))

		if mem := spec.Linux.Resources.Memory; mem != nil && mem.Limit != nil {
			s.ExpectAgrees("resources.memory.limit", "realized OCI spec",
				framework.ExpectScalarEqual("resources.memory.limit",
					shown.GetMemory().GetLimit().GetValue(), *mem.Limit))
		}
	})

	t.Run("should report a container device matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-devices")
		framework.RequireCaps(t, framework.CapDevices)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// /dev/null exists on every node and exposing it a second time under a
		// name of ours is harmless, so the realized side is non-empty by
		// construction rather than by luck.
		ctrSpec.Devices = []*framework.Device{
			{HostPath: "/dev/null", ContainerPath: "/dev/test-device", Permissions: "rwm"},
		}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		spec := s.Inspect(ctr.ID).Spec

		s.Covered(framework.CreateContainer,
			"LinuxContainer.devices")

		var shown, realized []string
		for _, d := range got.Ctr.GetLinux().GetDevices() {
			shown = append(shown, d.GetPath())
		}

		if spec != nil && spec.Linux != nil {
			for _, d := range spec.Linux.Devices {
				realized = append(realized, d.Path)
			}
		}

		s.ExpectAgrees("devices", "realized OCI spec",
			framework.ExpectStringsSubset("devices", shown, realized))
		s.ExpectAgrees("devices", "realized OCI spec",
			framework.ExpectPopulated("devices", len(shown), len(realized)))
		// The device this test asked for, named: a runtime reporting some
		// other device would satisfy the subset check above on its own.
		s.ExpectAgrees("devices", "realized OCI spec",
			framework.ExpectStringsSubset("devices", []string{"/dev/test-device"}, shown))
	})

	t.Run("should report container resources matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-ctr-resources")
		framework.RequireCaps(t, framework.CapRawResources)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		ctrSpec.Resources = &framework.Resources{
			MemoryLimit: 200 * 1024 * 1024,
			CPUShares:   512,
		}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		spec := s.Inspect(ctr.ID).Spec
		if spec == nil || spec.Linux == nil || spec.Linux.Resources == nil {
			framework.Skipped(t, "the realized spec carries no resources, so there is "+
				"nothing to compare against")
		}

		s.Covered(framework.CreateContainer,
			"LinuxContainer.resources")

		shown := got.Ctr.GetLinux().GetResources()
		s.ExpectAgrees("resources", "hook request",
			framework.ExpectRequired("resources", int(min(len(shown.String()), 1))))

		if mem := spec.Linux.Resources.Memory; mem != nil && mem.Limit != nil {
			s.ExpectAgrees("resources.memory.limit", "realized OCI spec",
				framework.ExpectScalarEqual("resources.memory.limit",
					shown.GetMemory().GetLimit().GetValue(), *mem.Limit))
		}

		if cpu := spec.Linux.Resources.CPU; cpu != nil && cpu.Shares != nil {
			s.ExpectAgrees("resources.cpu.shares", "realized OCI spec",
				framework.ExpectScalarEqual("resources.cpu.shares",
					shown.GetCpu().GetShares().GetValue(), *cpu.Shares))
		}
	})

	t.Run("should report the OCI hooks matching the realized spec", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-hooks")

		plugin := inputPlugin(s)
		_, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		spec := s.Inspect(ctr.ID).Spec

		var realized []string
		if spec != nil && spec.Hooks != nil {
			for _, h := range spec.Hooks.CreateRuntime {
				realized = append(realized, h.Path)
			}

			for _, h := range spec.Hooks.CreateContainer {
				realized = append(realized, h.Path)
			}

			for _, h := range spec.Hooks.StartContainer {
				realized = append(realized, h.Path)
			}
		}

		// CRI cannot ask for a hook, so whether the container has one is the
		// node's configuration to decide. With none realized there is nothing
		// a report could omit, and recording the field as measured would count
		// a comparison that could not have failed -- the mistake the
		// seccomp_policy check made in its first form.
		if len(realized) == 0 {
			framework.Skipped(t, "this node's runtime adds no OCI hooks to a container, "+
				"so a report of none cannot be distinguished from an omission")
		}

		s.Covered(framework.CreateContainer,
			"Container.hooks")

		var shown []string
		for _, h := range got.Ctr.GetHooks().GetCreateRuntime() {
			shown = append(shown, h.GetPath())
		}

		for _, h := range got.Ctr.GetHooks().GetCreateContainer() {
			shown = append(shown, h.GetPath())
		}

		for _, h := range got.Ctr.GetHooks().GetStartContainer() {
			shown = append(shown, h.GetPath())
		}

		s.ExpectAgrees("hooks", "realized OCI spec",
			framework.ExpectStringsSubset("hooks", shown, realized))
		s.ExpectAgrees("hooks", "realized OCI spec",
			framework.ExpectPopulated("hooks", len(shown), len(realized)))
	})

	t.Run("should report the termination reason at RemoveContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-status-reason")
		framework.RequireCaps(t, framework.CapContainerLifecycle)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// A container that fails on its own: a reason and a message are what a
		// runtime fills in for an abnormal exit, and a container we stop
		// ourselves may have neither.
		ctrSpec.Command = []string{"/bin/sh", "-c", "exit 3"}
		pod, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		s.StartContainer(ctr)
		s.StopContainer(ctr)

		// The oracle before the removal, which is what makes the status
		// readable at all: a removed container has no status to read.
		observed := s.Inspect(ctr.ID)

		s.RemoveContainer(ctr)
		s.StopPod(pod)
		s.RemovePod(pod)

		got := s.CapturedAt(plugin, framework.RemoveContainer, ctr)
		if got == nil {
			return
		}

		if observed.Reason == "" && observed.Message == "" {
			framework.Skipped(t, "this runtime reports no termination reason or message "+
				"over CRI for a container that exited non-zero, so a report of none "+
				"cannot be distinguished from an omission")
		}

		// Coverage is recorded per field and only where there is something to
		// compare against: CRI-O fills in a reason and no message, and
		// counting the message as measured would report a comparison that
		// never ran. A field left out here prints as not exercised, which is
		// what it was.
		if observed.Reason != "" {
			s.Covered(framework.RemoveContainer, "Container.status_reason")
			s.ExpectAgrees("status_reason", "CRI status",
				framework.ExpectScalarEqual("status_reason",
					got.Ctr.GetStatusReason(), observed.Reason))
		}

		if observed.Message != "" {
			s.Covered(framework.RemoveContainer, "Container.status_message")
			s.ExpectAgrees("status_message", "CRI status",
				framework.ExpectScalarEqual("status_message",
					got.Ctr.GetStatusMessage(), observed.Message))
		}
	})

	t.Run("should report exit status at RemoveContainer", func(t *testing.T) {
		s := framework.NewSuite(t, "agree-exit")
		framework.RequireCaps(t, framework.CapContainerLifecycle)

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		// A command that exits with a code of its own, so the reported code is
		// one this test chose rather than whatever a killed process yields.
		ctrSpec.Command = []string{"/bin/sh", "-c", "exit 3"}
		pod, ctr := s.CreatePodWithContainer("pod0", ctrSpec)

		s.StartContainer(ctr)
		s.StopContainer(ctr)
		s.RemoveContainer(ctr)
		s.StopPod(pod)
		s.RemovePod(pod)

		got := s.CapturedAt(plugin, framework.RemoveContainer, ctr)
		if got == nil {
			return
		}

		s.Covered(framework.RemoveContainer,
			"Container.exit_code",
			"Container.finished_at")

		s.ExpectAgrees("exit_code", "hook request",
			framework.ExpectScalarEqual("exit_code", int32(3), got.Ctr.GetExitCode()))
		s.ExpectAgrees("finished_at", "hook request",
			framework.ExpectRequired("finished_at", int(min(got.Ctr.GetFinishedAt(), 1))))
	})
}

func TestInputStateLadder(t *testing.T) {
	t.Run("should report the state each hook implies", func(t *testing.T) {
		// Container.state is populated at every hook, so a presence check proves
		// nothing. What carries information is whether the value tracks the
		// lifecycle -- and CONTAINER_UNKNOWN is the enum's zero value, so the
		// ladder is also the check that the field was set at all.
		s := framework.NewSuite(t, "input-state-ladder")
		framework.RequireCaps(t, framework.CapContainerLifecycle)

		plugin := inputPlugin(s)
		pod, ctr := s.CreatePodWithContainer("pod0", s.CtrSpec("ctr0"))

		s.StartContainer(ctr)
		s.StopContainer(ctr)
		s.RemoveContainer(ctr)
		s.StopPod(pod)
		s.RemovePod(pod)

		for _, hook := range []framework.EventType{
			framework.PostCreateContainer,
			framework.PostStartContainer,
			framework.RemoveContainer,
		} {
			got := s.CapturedAt(plugin, hook, ctr)
			if got == nil {
				continue
			}

			// Recorded per rung, against the hook actually read. Recording it
			// once against CreateContainer named a hook this test never asserts
			// at -- and one ExpectStateAt refuses, since a container does not
			// exist there -- and counted the field even when no rung arrived.
			s.Covered(hook, "Container.state")

			s.ExpectAgrees("state", "hook request",
				framework.ExpectStateAt(hook, got.Ctr.GetState()))
		}
	})
}

// TestInputCDIDevices is the test this whole layer was built for.
//
// containerd delivers CRI-injected CDI devices to plugins and CRI-O does not,
// and the suite ran green against both before this existed. A conformance tool
// whose central claim is that it finds such divergence cannot be silent about
// one its maintainer already knows.
func TestInputCDIDevices(t *testing.T) {
	t.Run("should report a CRI-injected CDI device", func(t *testing.T) {
		s := framework.NewSuite(t, "input-cdi")
		framework.RequireCaps(t, framework.CapCDIDevices)

		device := framework.CDIDeviceName("null")

		plugin := inputPlugin(s)
		ctrSpec := s.CtrSpec("ctr0")
		ctrSpec.CDIDevices = []string{device}
		_, ctr := s.CreatePodWithContainer("pod0", ctrSpec)
		s.StartContainer(ctr)

		got := s.CapturedAt(plugin, framework.CreateContainer, ctr)
		if got == nil {
			return
		}

		// Two assertions, and they are independent on purpose. A runtime can
		// report a device it did not inject, or inject one it did not report,
		// and those are different findings about it. Running only the first
		// would leave the more interesting half of the second case unsaid.
		//
		// A runtime that refuses the creation outright never reaches here: that
		// is a failed operation, recorded by the driver call above, and counting
		// it as two unmet expectations would overstate what the run measured.
		var shown []string
		for _, d := range got.Ctr.GetCDIDevices() {
			shown = append(shown, d.GetName())
		}

		s.Covered(framework.CreateContainer,
			"Container.CDI_devices")

		s.ExpectShown("CDI_devices", framework.ExpectShownStrings(
			"CDI_devices", []string{device}, shown))

		// The second surface: the container's own kernel. /dev/test-null is the
		// name the test CDI spec gives it, so a device under that name cannot
		// have come from the image or from the runtime's own defaults.
		s.ExpectAgrees("CDI_devices", "container kernel",
			framework.ExpectScalarEqual("/dev/test-null is a character device",
				"yes", s.Exec(ctr, "sh", "-c",
					"test -c /dev/test-null && echo yes || echo no")))
	})
}
