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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// The creation-adjustment subjects, one test each: a plugin adjusts a container
// while it is being created, and the test verifies the adjustment reached the
// container.
//
// Every test here ends by requiring that the plugin actually received
// CreateContainer for the container it inspected. That is not ceremony: a plugin
// that never fired leaves the assertion above it to pass on whatever the default
// happened to be, which is the failure mode every subject in this file shares,
// and the one that is hardest to notice because it presents as a pass. Two of the
// tests need a second guard for the other half of the same problem -- an
// expectation whose wanted value is already the default cannot fail even when the
// plugin does fire -- and skip through framework.SkipUnlessCPUs and
// SkipUnlessNUMANodes rather than assert vacuously.

// adjustingPlugin returns a plugin subscribed to CreateContainer whose callback
// applies adjust to the container being created. The plugin must be started before
// the container is created: CreateContainer is the hook that carries the
// adjustment, so a container created first would never be adjusted, and the test
// would fail for a reason unrelated to its subject.
func adjustingPlugin(s *framework.Suite, name string,
	adjust func(*api.ContainerAdjustment)) *framework.TestPlugin {

	p := s.NewPlugin(name, "00", api.MustParseEventMask("CreateContainer"))
	p.CreateContainerFn = func(_ context.Context, _ *api.PodSandbox, _ *api.Container) (
		*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
		a := &api.ContainerAdjustment{}
		adjust(a)

		return a, nil, nil
	}

	return p
}

// probeContainer creates and starts a container, in a pod of its own, to read the node's unadjusted
// view of a container from.
//
// Call it before this case's plugins are started, which is what makes it a probe:
// no plugin is connected yet, so nothing has adjusted it, and what its kernel
// reports is what a container gets on this node by default. That is the number a
// vacuity gate needs. Reading the same numbers from the adjusted container under
// test would answer with the adjustment itself -- Cpus_allowed_list "0" because
// the plugin asked for "0" -- and the gate would skip on every host, measuring
// nothing while looking careful.
//
// Started, because the gates read /proc/self/status through an exec.
func probeContainer(s *framework.Suite) *framework.Container {
	_, probe := s.CreatePodWithContainer("probe", s.CtrSpec("probe"))
	s.StartContainer(probe)

	return probe
}

// requireSawCreateContainer fails the test unless one of this case's plugins
// received CreateContainer for this container.
//
// Every test in this file ends with it, and the ~20 deferred creation-adjustment
// subjects should too, which is why it is a helper rather than five copies of the
// same five lines. It converts the most dangerous silent failure in the file --
// a plugin that never fired, leaving the assertion above to pass on whatever the
// default happened to be -- from something an implementer reasoned about into
// something the suite checks on every run.
//
// Two details of the check are load-bearing. It matches on CtrID, so a hook
// received for some other container of the same case (a probe, a second
// container) cannot satisfy it. And it is Has rather than WaitFor, with no
// timeout: CreateContainer is synchronous, the runtime cannot have returned from
// the creation this test already completed without the hook having run, and the
// event is recorded before the adjustment callback is invoked. A WaitFor here
// would suggest a race that does not exist.
func requireSawCreateContainer(t *testing.T, s *framework.Suite, ctr *framework.Container) {
	t.Helper()

	require.True(t, s.Q().Has(&framework.Event{
		Type: framework.CreateContainer, CtrID: ctr.ID,
	}), "the plugin never saw CreateContainer for this container, so whatever "+
		"the assertion above observed, the adjustment did not cause it")
}

func TestAdjustAnnotation(t *testing.T) {
	t.Run("should reach the container's OCI spec", func(t *testing.T) {
		s := framework.NewSuite(t, "adjust-annotation")
		s.StartPlugins(adjustingPlugin(s, "adjust-annotation", func(a *api.ContainerAdjustment) {
			a.AddAnnotation("nri-test.containerd.io/adjusted", "yes")
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		// Created is enough: the spec is readable before start, and an annotation
		// has no in-container expression to check.
		s.ExpectAnnotation(ctr, "nri-test.containerd.io/adjusted", "yes")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestAdjustEnv(t *testing.T) {
	t.Run("should reach the container's OCI spec and a process started in it", func(t *testing.T) {
		s := framework.NewSuite(t, "adjust-env")
		framework.RequireCaps(t, framework.CapExec)

		// A key in the suite's own namespace, so nothing else on the node could
		// supply it. An adjustment of a name an image plausibly defines -- PATH,
		// HOME, TERM -- would be satisfied by the image on a runtime that ignored
		// the adjustment entirely.
		const (
			key  = "NRI_TEST_ADJUSTED"
			want = "yes"
		)

		s.StartPlugins(adjustingPlugin(s, "adjust-env", func(a *api.ContainerAdjustment) {
			a.AddEnv(key, want)
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		// Started, unlike the annotation case: reading /proc/self/environ needs a
		// process to read it from.
		s.StartContainer(ctr)

		// Deliberately not described as two surfaces agreeing, unlike the mount and
		// cpuset tests. Both containerd and CRI-O build an exec'd process's
		// environment from the container's stored OCI spec, so the environ read here
		// is the inspector's document reached by a second path rather than an
		// independent witness: it proves the recorded value is used to start
		// processes in this container, and it would not catch a runtime that recorded
		// the adjustment without applying it to the container's own init process. See
		// framework.ExpectEnv, which spells out both halves and why observing init is
		// deferred.
		s.ExpectEnv(ctr, key, want)

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestAdjustMount(t *testing.T) {
	t.Run("should reach the container's OCI spec and its mount table", func(t *testing.T) {
		s := framework.NewSuite(t, "adjust-mount")
		framework.RequireCaps(t, framework.CapExec)

		// Under /mnt, which busybox has, and named after the suite so a stray mount
		// is attributable. The source is /tmp because every node has one and its
		// contents do not matter here.
		const dst = "/mnt/nri-test-adjusted"

		s.StartPlugins(adjustingPlugin(s, "adjust-mount", func(a *api.ContainerAdjustment) {
			a.AddMount(&api.Mount{
				Destination: dst,
				Source:      "/tmp",
				Type:        "bind",
				// "bind" so the runtime binds an existing directory rather than
				// mounting a filesystem; "ro" is the subject of the second
				// assertion below; "rprivate" so the propagation of the result does
				// not depend on what the host's /tmp happens to be.
				Options: []string{"bind", "ro", "rprivate"},
			})
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		// Started: the mount table is the container's own, read through an exec.
		s.StartContainer(ctr)

		// rprivate is deliberately not asserted: it is in the spec and not in
		// /proc/self/mounts, which carries kernel flags only, so it would fail here.
		s.ExpectMount(ctr, dst, "ro")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestAdjustCpuset(t *testing.T) {
	t.Run("should reach the kernel's Cpus_allowed_list", func(t *testing.T) {
		s := framework.NewSuite(t, "adjust-cpuset")
		framework.RequireCaps(t, framework.CapExec)

		// CPU 0 alone. Every node has a CPU 0, which is why this is the value to
		// ask for -- and on a node that has only CPU 0 it is also the value an
		// unadjusted container already gets, so the gate below is what makes the
		// assertion able to fail. See framework.SkipUnlessCPUs.
		const wantCPUs = "0"

		probe := probeContainer(s)
		framework.SkipUnlessCPUs(t, s, probe, 2)

		s.StartPlugins(adjustingPlugin(s, "adjust-cpuset", func(a *api.ContainerAdjustment) {
			a.SetLinuxCPUSetCPUs(wantCPUs)
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))
		s.StartContainer(ctr)

		// The kernel's own answer, not the runtime's record of the request: a
		// runtime that wrote the cpuset into the spec and failed to apply it looks
		// correct on the spec and wrong here.
		s.ExpectCpuset(ctr, wantCPUs)

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestAdjustMems(t *testing.T) {
	t.Run("should reach the kernel's Mems_allowed_list", func(t *testing.T) {
		s := framework.NewSuite(t, "adjust-mems")
		framework.RequireCaps(t, framework.CapExec)

		// NUMA node 0, for the reason the cpuset test asks for CPU 0, and gated for
		// the same reason: a single-node node reports Mems_allowed_list "0" whatever
		// was requested, so this expectation cannot fail there. Expect this test to
		// skip on most hosts; that is the honest outcome, and the skip message says
		// what went unmeasured.
		const wantMems = "0"

		probe := probeContainer(s)
		framework.SkipUnlessNUMANodes(t, s, probe, 2)

		s.StartPlugins(adjustingPlugin(s, "adjust-mems", func(a *api.ContainerAdjustment) {
			a.SetLinuxCPUSetMems(wantMems)
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))
		s.StartContainer(ctr)

		s.ExpectMems(ctr, wantMems)

		requireSawCreateContainer(t, s, ctr)
	})
}
