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

package mock

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// runtimeWithContainer returns a runtime holding one container with a
// baseline spec, and that container's id. It starts no Adaptation: these
// tests are about the apply step alone.
func runtimeWithContainer(t *testing.T) (*Runtime, string) {
	t.Helper()

	r := &Runtime{
		socketPath: filepath.Join(t.TempDir(), "nri.sock"),
		runID:      "testrun",
		pods:       map[string]*pod{},
		containers: map[string]*container{},
	}

	p := &pod{id: "pod0", name: "pod0", namespace: "nri-test"}
	p.spec = sandboxSpec(&framework.PodSpec{Name: "pod0"}, p)
	r.pods[p.id] = p

	c := &container{id: "ctr0", podID: p.id, name: "ctr0", state: api.ContainerState_CONTAINER_CREATED}
	c.spec = containerSpec(&framework.ContainerSpec{Name: "ctr0"}, p, c)
	r.containers[c.id] = c
	p.containers = append(p.containers, c.id)

	return r, c.id
}

// hasMount reports whether the spec carries a mount at destination.
func hasMount(r *Runtime, id, destination string) bool {
	for _, m := range r.containers[id].spec.Mounts {
		if m.Destination == destination {
			return true
		}
	}

	return false
}

func TestApplyAdjustment(t *testing.T) {
	t.Run("should land an adjustment in the container's realized spec", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)

		require.NoError(t, r.applyAdjustment(ctr, &api.ContainerAdjustment{
			Env:    []*api.KeyValue{{Key: "INJECTED", Value: "yes"}},
			Mounts: []*api.Mount{{Source: "/tmp", Destination: "/host-tmp", Type: "bind"}},
		}))

		require.Contains(t, r.containers[ctr].spec.Process.Env, "INJECTED=yes")
		require.True(t, hasMount(r, ctr, "/host-tmp"),
			"the mount the adjustment asked for has to be in the spec")
	})

	t.Run("should refuse an adjustment for a container it does not know", func(t *testing.T) {
		r, _ := runtimeWithContainer(t)

		err := r.applyAdjustment("ghost", &api.ContainerAdjustment{})
		require.Error(t, err)
		require.ErrorContains(t, err, "ghost")
	})

	// The merge across plugins is Adaptation's, so the mock applies what it
	// is handed rather than merging again: the later value in the merged
	// adjustment is what lands.
	t.Run("should apply exactly the merged adjustment it is handed", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)

		require.NoError(t, r.applyAdjustment(ctr, &api.ContainerAdjustment{
			Env: []*api.KeyValue{{Key: "K", Value: "first"}, {Key: "K", Value: "second"}},
		}))

		require.Contains(t, r.containers[ctr].spec.Process.Env, "K=second")
		require.NotContains(t, r.containers[ctr].spec.Process.Env, "K=first")
	})

	t.Run("should leave the spec alone for a nil adjustment", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)
		before := len(r.containers[ctr].spec.Process.Env)

		require.NoError(t, r.applyAdjustment(ctr, nil))
		require.Len(t, r.containers[ctr].spec.Process.Env, before)
	})

	// An empty adjustment is what Adaptation returns when no plugin asked for
	// anything, so it has to be a no-op and not an error.
	t.Run("should accept an empty adjustment", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)

		require.NoError(t, r.applyAdjustment(ctr, &api.ContainerAdjustment{}))
	})
}

func TestApplyUpdates(t *testing.T) {
	t.Run("should land a resource update in the realized spec", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)

		require.NoError(t, r.applyUpdates([]*api.ContainerUpdate{{
			ContainerId: ctr,
			Linux: &api.LinuxContainerUpdate{Resources: &api.LinuxResources{
				Memory: &api.LinuxMemory{Limit: api.Int64(209715200)},
			}},
		}}))

		require.NotNil(t, r.containers[ctr].spec.Linux.Resources.Memory.Limit)
		require.EqualValues(t, 209715200, *r.containers[ctr].spec.Linux.Resources.Memory.Limit)
	})

	t.Run("should refuse an update for a container it does not know", func(t *testing.T) {
		r, _ := runtimeWithContainer(t)

		err := r.applyUpdates([]*api.ContainerUpdate{{ContainerId: "ghost"}})
		require.Error(t, err)
		require.ErrorContains(t, err, "ghost")
	})

	// The protocol lets a plugin say an update is not worth failing over.
	t.Run("should skip an update for an unknown container that may be ignored", func(t *testing.T) {
		r, _ := runtimeWithContainer(t)

		require.NoError(t, r.applyUpdates([]*api.ContainerUpdate{{
			ContainerId:   "ghost",
			IgnoreFailure: true,
		}}))
	})

	t.Run("should accept no updates at all", func(t *testing.T) {
		r, _ := runtimeWithContainer(t)

		require.NoError(t, r.applyUpdates(nil))
	})
}

// Two containers in a pod must not share the map their sysctls live in:
// generate's sysctl adjustment writes in place, so one container's adjustment
// would otherwise rewrite the pod's map and every sibling's realized spec.
// The finding would then be reported against NRI.
func TestApplyDoesNotAliasThePodsSysctls(t *testing.T) {
	r := &Runtime{
		socketPath: filepath.Join(t.TempDir(), "nri.sock"),
		runID:      "testrun",
		pods:       map[string]*pod{},
		containers: map[string]*container{},
	}

	podSpec := &framework.PodSpec{Name: "pod0", Sysctls: map[string]string{"net.ipv4.ip_forward": "1"}}
	p := &pod{id: "pod0", name: "pod0", sysctls: podSpec.Sysctls}
	p.spec = sandboxSpec(podSpec, p)
	r.pods[p.id] = p

	for _, name := range []string{"ctr0", "ctr1"} {
		req := &framework.ContainerSpec{Name: name}
		c := &container{id: name, podID: p.id, name: name}
		c.spec = containerSpec(req, p, c)
		r.containers[c.id] = c
		p.containers = append(p.containers, c.id)
	}

	require.NoError(t, r.applyAdjustment("ctr0", &api.ContainerAdjustment{
		Linux: &api.LinuxContainerAdjustment{Sysctl: map[string]string{"net.ipv4.ip_forward": "0"}},
	}))

	require.Equal(t, "1", p.sysctls["net.ipv4.ip_forward"],
		"the pod's own sysctls must not change")
	require.Equal(t, "1", r.containers["ctr1"].spec.Linux.Sysctl["net.ipv4.ip_forward"],
		"a sibling's realized spec must not change")
	require.Equal(t, "0", r.containers["ctr0"].spec.Linux.Sysctl["net.ipv4.ip_forward"],
		"the adjusted container's own spec must change")
}
