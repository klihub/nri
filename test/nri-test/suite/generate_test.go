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
	"strings"
	"testing"

	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// How an adjustment merges into a spec that already has something there.
//
// Gated on framework.CapInTreeAdjustments, so these run only on a driver that
// applies adjustments with the in-tree pkg/runtime-tools/generate and has no
// runtime beneath it. There the spec read back is that library's output and
// nothing else, so an assertion on it is a test of the library. On a runtime the
// same assertion would exercise the runtime's own vendored copy, which the
// subjects in adjust_test.go already cover.
//
// The subject is the merge, not the write: replacing a value the spec already
// carries, and honouring a leading "-" on a key as a removal marker. Nothing
// else on this arm reaches any of it.
//
// Only what a plugin can actually reach is tested here. pkg/adaptation resolves
// some removals itself rather than forwarding the marker, so generate's removal
// branch is reachable for annotations, sysctls, mounts and namespaces but not
// for env or devices; and it refuses two adjustments of one env key outright, so
// generate's own last-one-wins never runs. One consequence is that AdjustEnv's
// modify-existing block has no reachable effect at all -- AddProcessEnv replaces
// a key in place by itself, and the block's other job is removal -- so no test
// here can cover it. See RUNTIME-STATUS.md.
//
// These use plain require rather than Suite.Expect*, deliberately: a failure
// here is this repository's own library misbehaving, not a runtime failing a
// conformance expectation, and recording it as a finding would put a claim about
// a runtime into the summary of a run that had none.

// specEnvValues returns the values the spec's process environment carries for
// key, in order. More than one means a merge duplicated the key.
func specEnvValues(env []string, key string) []string {
	var values []string

	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == key {
			values = append(values, v)
		}
	}

	return values
}

// specMounts returns the spec's mounts for destination, in order. More than one
// means a merge duplicated the destination.
func specMounts(mounts []rspec.Mount, destination string) []rspec.Mount {
	var found []rspec.Mount

	for _, m := range mounts {
		if m.Destination == destination {
			found = append(found, m)
		}
	}

	return found
}

// specNamespaceTypes returns the types of the spec's Linux namespaces.
func specNamespaceTypes(spec *rspec.Spec) []string {
	var types []string

	for _, n := range spec.Linux.Namespaces {
		types = append(types, string(n.Type))
	}

	return types
}

func TestGenerateAdjustsEnv(t *testing.T) {
	const key = "NRI_TEST_GENERATE_ENV"

	t.Run("should replace the value of a variable the spec already has", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-env-replace")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-env-replace", func(a *api.ContainerAdjustment) {
			a.AddEnv(key, "after")
		}))

		spec := s.CtrSpec("ctr0")
		spec.Env = map[string]string{key: "before"}
		_, ctr := s.CreatePodWithContainer("pod", spec)

		// One entry, not two: the adjustment replaces the existing value in place
		// rather than appending a second definition of the same key.
		require.Equal(t, []string{"after"},
			specEnvValues(s.Inspect(ctr.ID).Spec.Process.Env, key),
			"an adjustment of a key the spec has replaces that one entry")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should append a variable the spec does not have", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-env-append")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-env-append", func(a *api.ContainerAdjustment) {
			a.AddEnv(key, "added")
		}))

		// No initial value for key, so the append path is the one under test.
		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		require.Equal(t, []string{"added"},
			specEnvValues(s.Inspect(ctr.ID).Spec.Process.Env, key),
			"a key the spec lacks is appended exactly once")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should leave the arguments alone when the adjustment sets none", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-env-keeps-args")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-env-keeps-args", func(a *api.ContainerAdjustment) {
			a.AddEnv(key, "added")
		}))

		spec := s.CtrSpec("ctr0")
		want := append([]string{}, spec.Command...)
		_, ctr := s.CreatePodWithContainer("pod", spec)

		// An adjustment carrying no args must not clear the ones the container
		// was created with, which is what an unguarded SetProcessArgs would do.
		require.Equal(t, want, s.Inspect(ctr.ID).Spec.Process.Args,
			"an adjustment that sets no arguments leaves the container's own")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsArgs(t *testing.T) {
	t.Run("should replace the arguments the spec already has", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-args-replace")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		want := []string{"/bin/sleep", "3600"}
		s.StartPlugins(adjustingPlugin(s, "generate-args-replace", func(a *api.ContainerAdjustment) {
			a.SetArgs(want)
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		require.Equal(t, want, s.Inspect(ctr.ID).Spec.Process.Args,
			"a non-empty argument adjustment replaces the container's own wholesale")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsAnnotations(t *testing.T) {
	const key = "nri-test.containerd.io/generate"

	t.Run("should overwrite an annotation the spec already has", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-annotation-overwrite")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-annotation-overwrite", func(a *api.ContainerAdjustment) {
			a.AddAnnotation(key, "after")
		}))

		spec := s.CtrSpec("ctr0")
		spec.Annotations[key] = "before"
		_, ctr := s.CreatePodWithContainer("pod", spec)

		require.Equal(t, "after", s.Inspect(ctr.ID).Spec.Annotations[key],
			"an adjustment of an annotation the spec has replaces its value")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should drop an annotation marked for removal", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-annotation-remove")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-annotation-remove", func(a *api.ContainerAdjustment) {
			a.RemoveAnnotation(key)
		}))

		spec := s.CtrSpec("ctr0")
		spec.Annotations[key] = "before"
		_, ctr := s.CreatePodWithContainer("pod", spec)

		require.NotContains(t, s.Inspect(ctr.ID).Spec.Annotations, key,
			"an annotation marked for removal is gone from the spec, not merely emptied")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsSysctl(t *testing.T) {
	const (
		kept    = "net.ipv4.ip_forward"
		removed = "net.ipv4.conf.all.arp_filter"
		added   = "net.ipv4.conf.all.rp_filter"
	)

	// A pod's sysctls, which the driver realizes into each container's spec, so
	// there is something for the adjustment below to remove.
	podWithSysctls := func(s *framework.Suite, name string) (*framework.Pod, *framework.Container) {
		spec := s.PodSpec(name, s.CtrSpec("ctr0"))
		spec.Sysctls = map[string]string{kept: "1", removed: "1"}
		pod := s.CreatePod(spec)

		return pod, pod.Containers[0]
	}

	t.Run("should add a sysctl the spec does not have", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-sysctl-add")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-sysctl-add", func(a *api.ContainerAdjustment) {
			a.SetLinuxSysctl(added, "1")
		}))

		_, ctr := podWithSysctls(s, "pod")

		require.Equal(t, "1", s.Inspect(ctr.ID).Spec.Linux.Sysctl[added],
			"an adjusted sysctl reaches the spec")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should drop a sysctl marked for removal", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-sysctl-remove")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-sysctl-remove", func(a *api.ContainerAdjustment) {
			a.SetLinuxSysctl(api.MarkForRemoval(removed), "")
		}))

		_, ctr := podWithSysctls(s, "pod")

		sysctl := s.Inspect(ctr.ID).Spec.Linux.Sysctl
		require.NotContains(t, sysctl, removed,
			"a sysctl marked for removal is gone from the spec")
		require.Equal(t, "1", sysctl[kept],
			"removing one sysctl leaves the container's others in place")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsMounts(t *testing.T) {
	const dst = "/mnt/nri-test-generate"

	ctrWithMount := func(s *framework.Suite) *framework.ContainerSpec {
		spec := s.CtrSpec("ctr0")
		spec.Mounts = []*framework.Mount{{Source: "/tmp", Destination: dst}}

		return spec
	}

	t.Run("should replace a mount at a destination the spec already has", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-mount-replace")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-mount-replace", func(a *api.ContainerAdjustment) {
			a.AddMount(&api.Mount{
				Destination: dst,
				Source:      "/var/tmp",
				Type:        "bind",
				Options:     []string{"bind", "ro", "rprivate"},
			})
		}))

		_, ctr := s.CreatePodWithContainer("pod", ctrWithMount(s))

		// One mount at that destination, not two: a second entry for one
		// destination is a spec a runtime would reject or apply arbitrarily.
		mounts := specMounts(s.Inspect(ctr.ID).Spec.Mounts, dst)
		require.Len(t, mounts, 1,
			"an adjusted mount replaces the one already at its destination")
		require.Equal(t, "/var/tmp", mounts[0].Source,
			"the surviving mount is the adjusted one")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should drop a mount marked for removal", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-mount-remove")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-mount-remove", func(a *api.ContainerAdjustment) {
			a.RemoveMount(dst)
		}))

		_, ctr := s.CreatePodWithContainer("pod", ctrWithMount(s))

		require.Empty(t, specMounts(s.Inspect(ctr.ID).Spec.Mounts, dst),
			"a mount marked for removal is gone from the spec")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsNamespaces(t *testing.T) {
	t.Run("should replace a namespace of a type the spec already has", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-namespace-replace")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		const path = "/proc/1/ns/ipc"
		s.StartPlugins(adjustingPlugin(s, "generate-namespace-replace", func(a *api.ContainerAdjustment) {
			a.AddOrReplaceNamespace(&api.LinuxNamespace{
				Type: string(rspec.IPCNamespace),
				Path: path,
			})
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		spec := s.Inspect(ctr.ID).Spec
		var found []rspec.LinuxNamespace
		for _, n := range spec.Linux.Namespaces {
			if n.Type == rspec.IPCNamespace {
				found = append(found, n)
			}
		}

		// Replaced in place, so the type appears once and carries the new path. A
		// second entry of one type is what AddOrReplace exists to prevent.
		require.Len(t, found, 1, "a namespace type appears once in the spec")
		require.Equal(t, path, found[0].Path, "the surviving namespace is the adjusted one")

		requireSawCreateContainer(t, s, ctr)
	})

	t.Run("should drop a namespace marked for removal", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-namespace-remove")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		s.StartPlugins(adjustingPlugin(s, "generate-namespace-remove", func(a *api.ContainerAdjustment) {
			a.RemoveNamespace(&api.LinuxNamespace{Type: string(rspec.IPCNamespace)})
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		types := specNamespaceTypes(s.Inspect(ctr.ID).Spec)
		require.NotContains(t, types, string(rspec.IPCNamespace),
			"a namespace marked for removal is gone from the spec")
		require.Contains(t, types, string(rspec.PIDNamespace),
			"removing one namespace leaves the container's others in place")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsResources(t *testing.T) {
	t.Run("should replace the fields it is given and leave the rest", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-resources-merge")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		const (
			memory    = int64(64 * 1024 * 1024)
			wantCPU   = uint64(1024)
			beforeCPU = int64(512)
		)

		s.StartPlugins(adjustingPlugin(s, "generate-resources-merge", func(a *api.ContainerAdjustment) {
			a.SetLinuxCPUShares(wantCPU)
		}))

		spec := s.CtrSpec("ctr0")
		spec.Resources = &framework.Resources{CPUShares: beforeCPU, MemoryLimit: memory}
		_, ctr := s.CreatePodWithContainer("pod", spec)

		res := s.Inspect(ctr.ID).Spec.Linux.Resources
		require.NotNil(t, res.CPU)
		require.NotNil(t, res.CPU.Shares)
		require.Equal(t, wantCPU, *res.CPU.Shares, "an adjusted field takes the new value")

		// The half that makes this a merge test: a field the adjustment said
		// nothing about keeps the value the container was created with.
		require.NotNil(t, res.Memory, "an adjustment of one resource must not drop the others")
		require.NotNil(t, res.Memory.Limit)
		require.Equal(t, memory, *res.Memory.Limit,
			"a resource the adjustment did not mention keeps its value")

		requireSawCreateContainer(t, s, ctr)
	})
}

func TestGenerateAdjustsRlimits(t *testing.T) {
	t.Run("should carry an adjusted rlimit into the spec", func(t *testing.T) {
		s := framework.NewSuite(t, "generate-rlimit")
		framework.RequireCaps(t, framework.CapInTreeAdjustments)

		const (
			typ  = "RLIMIT_NOFILE"
			hard = uint64(4096)
			soft = uint64(2048)
		)

		s.StartPlugins(adjustingPlugin(s, "generate-rlimit", func(a *api.ContainerAdjustment) {
			a.AddRlimit(typ, hard, soft)
		}))

		_, ctr := s.CreatePodWithContainer("pod", s.CtrSpec("ctr0"))

		// Presence only. AdjustRlimits appends without deduplicating and honours
		// no removal marker, unlike every other Adjust* entry point, so a
		// container created with this limit ends up with two entries for it. That
		// is recorded in RUNTIME-STATUS.md rather than asserted here, because a
		// test asserting it would have to be rewritten by whoever fixes it.
		var found []rspec.POSIXRlimit
		for _, l := range s.Inspect(ctr.ID).Spec.Process.Rlimits {
			if l.Type == typ && l.Hard == hard && l.Soft == soft {
				found = append(found, l)
			}
		}
		require.NotEmpty(t, found, "an adjusted rlimit reaches the spec")

		requireSawCreateContainer(t, s, ctr)
	})
}
