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
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/test/nri-test/framework"
)

// newTestDriver returns a mock driver on a socket of its own, closed when the
// test ends.
func newTestDriver(t *testing.T) (framework.Driver, framework.Inspector) {
	t.Helper()

	d, i, err := NewDriver(context.Background(), &framework.Config{
		NRISocketPath: filepath.Join(t.TempDir(), "nri.sock"),
		RunID:         "testrun",
		Namespace:     "nri-test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	return d, i
}

// podWithContainer creates one pod holding one container.
func podWithContainer(t *testing.T, d framework.Driver) *framework.Pod {
	t.Helper()

	pod, err := d.CreatePod(context.Background(), &framework.PodSpec{
		Name:       "pod0",
		Namespace:  "nri-test",
		Containers: []*framework.ContainerSpec{{Name: "ctr0"}},
	})
	require.NoError(t, err)

	return pod
}

func TestDriverLifecycle(t *testing.T) {
	t.Run("should create a pod with its container and report both as the run's", func(t *testing.T) {
		d, _ := newTestDriver(t)

		pod := podWithContainer(t, d)
		require.NotEmpty(t, pod.ID)
		require.Len(t, pod.Containers, 1)
		require.NotEmpty(t, pod.Containers[0].ID)

		pods, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{pod.ID}, pods)

		ctrs, err := d.ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{pod.Containers[0].ID}, ctrs)
	})

	// The leak guards run after Cleanup, so a model left populated reports a
	// phantom leak on every run.
	t.Run("should report nothing left after Cleanup", func(t *testing.T) {
		d, _ := newTestDriver(t)
		podWithContainer(t, d)

		require.NoError(t, d.Cleanup(context.Background()))

		pods, err := d.ListRunPods(context.Background())
		require.NoError(t, err)
		require.Empty(t, pods)

		ctrs, err := d.ListRunContainers(context.Background())
		require.NoError(t, err)
		require.Empty(t, ctrs)
	})

	t.Run("should take a container through its states", func(t *testing.T) {
		d, i := newTestDriver(t)
		pod := podWithContainer(t, d)
		ctr := pod.Containers[0]

		o, err := i.Inspect(context.Background(), ctr.ID)
		require.NoError(t, err)
		require.Equal(t, framework.StateCreated, o.State)

		require.NoError(t, d.StartContainer(context.Background(), ctr))
		o, err = i.Inspect(context.Background(), ctr.ID)
		require.NoError(t, err)
		require.Equal(t, framework.StateRunning, o.State)

		require.NoError(t, d.StopContainer(context.Background(), ctr))
		o, err = i.Inspect(context.Background(), ctr.ID)
		require.NoError(t, err)
		require.Equal(t, framework.StateExited, o.State)
	})

	t.Run("should withhold every capability it cannot honestly express", func(t *testing.T) {
		d, _ := newTestDriver(t)

		caps := d.Capabilities()
		require.True(t, caps.Has(framework.CapContainerLifecycle))
		require.True(t, caps.Has(framework.CapRawResources))

		for _, c := range []framework.Capability{
			framework.CapExec,
			framework.CapObserveOpError,
			framework.CapUpdatePodResources,
		} {
			require.False(t, caps.Has(c), c.String())
		}
	})

	t.Run("should name itself as no runtime at all", func(t *testing.T) {
		d, _ := newTestDriver(t)
		require.Equal(t, "mock", d.Name())

		v, ok := d.(interface {
			RuntimeVersion(context.Context) (string, string, error)
		})
		require.True(t, ok, "the suite reads the summary's runtime from this")

		name, version, err := v.RuntimeVersion(context.Background())
		require.NoError(t, err)
		require.Contains(t, name, "mock")
		require.Contains(t, version, "no runtime",
			"the version must not read like a runtime's own")
	})
}

func TestDriverInspect(t *testing.T) {
	t.Run("should refuse to inspect a container it does not know", func(t *testing.T) {
		_, i := newTestDriver(t)

		_, err := i.Inspect(context.Background(), "nope")
		require.Error(t, err)
		require.ErrorContains(t, err, "nope")
	})

	t.Run("should refuse to inspect a removed container", func(t *testing.T) {
		d, i := newTestDriver(t)
		pod := podWithContainer(t, d)
		ctr := pod.Containers[0]

		require.NoError(t, d.StopContainer(context.Background(), ctr))
		require.NoError(t, d.RemoveContainer(context.Background(), ctr))

		_, err := i.Inspect(context.Background(), ctr.ID)
		require.Error(t, err)
		require.ErrorContains(t, err, ctr.ID)
	})

	t.Run("should refuse to inspect a pod it does not know", func(t *testing.T) {
		_, i := newTestDriver(t)

		_, err := i.InspectPod(context.Background(), "nope")
		require.Error(t, err)
		require.ErrorContains(t, err, "nope")
	})

	// The sandbox spec is the only oracle for what the runtime realized for
	// the pod itself, so it has to be there and it has to be plausible.
	t.Run("should report a sandbox spec with the fields a runtime would have written", func(t *testing.T) {
		d, i := newTestDriver(t)
		pod := podWithContainer(t, d)

		o, err := i.InspectPod(context.Background(), pod.ID)
		require.NoError(t, err)
		require.NotNil(t, o.Spec)
		require.NotNil(t, o.Spec.Linux)
		require.NotEmpty(t, o.Spec.Linux.CgroupsPath)
		require.NotEmpty(t, o.Spec.Linux.Namespaces)
	})

	// An empty baseline would make every agreement check that leans on
	// runtime-authored defaults either vacuous or a phantom finding.
	t.Run("should report a container spec with the fields a runtime would have written", func(t *testing.T) {
		d, i := newTestDriver(t)
		pod := podWithContainer(t, d)

		o, err := i.Inspect(context.Background(), pod.Containers[0].ID)
		require.NoError(t, err)
		require.NotNil(t, o.Spec)
		require.NotEmpty(t, o.Spec.Version)
		require.NotNil(t, o.Spec.Process)
		require.NotEmpty(t, o.Spec.Process.Args)
		require.NotNil(t, o.Spec.Linux)
		require.NotEmpty(t, o.Spec.Linux.CgroupsPath)
		require.NotEmpty(t, o.Spec.Linux.Namespaces)
		require.NotEmpty(t, o.Spec.Process.Rlimits)
	})
}

// The suite's seccomp_policy check compares "the plugin was shown a policy"
// against "the realized spec has one". A runtime that applies the profile it
// was asked for has both; this driver ignored the request entirely, so both
// sides were empty and the comparison passed while asking nothing.
func TestDriverAppliesTheSecurityContext(t *testing.T) {
	t.Run("should realize a seccomp policy and report it", func(t *testing.T) {
		d, i := newTestDriver(t)

		uid, gid := int64(1234), int64(5678)
		pod, err := d.CreatePod(context.Background(), &framework.PodSpec{
			Name: "pod0", Namespace: "nri-test",
			Containers: []*framework.ContainerSpec{{
				Name:               "ctr0",
				SeccompProfile:     &framework.SeccompProfile{Type: framework.SeccompRuntimeDefault},
				RunAsUser:          &uid,
				RunAsGroup:         &gid,
				SupplementalGroups: []int64{4321},
			}},
		})
		require.NoError(t, err)

		o, err := i.Inspect(context.Background(), pod.Containers[0].ID)
		require.NoError(t, err)

		require.NotNil(t, o.Spec.Linux.Seccomp,
			"a runtime asked for a profile writes one into the spec")
		require.NotEmpty(t, o.Spec.Linux.Seccomp.Syscalls,
			"an empty policy would leave the agreement check with nothing to compare")

		require.EqualValues(t, uid, o.Spec.Process.User.UID)
		require.EqualValues(t, gid, o.Spec.Process.User.GID)
		require.Contains(t, o.Spec.Process.User.AdditionalGids, uint32(4321))
	})

	// And the message carries what was realized, so the two sides of the
	// agreement check are independent of each other.
	t.Run("should report the policy it realized", func(t *testing.T) {
		r, ctr := runtimeWithContainer(t)

		r.containers[ctr].request = &framework.ContainerSpec{
			Name:           "ctr0",
			SeccompProfile: &framework.SeccompProfile{Type: framework.SeccompRuntimeDefault},
		}
		r.containers[ctr].spec = containerSpec(r.containers[ctr].request, r.pods["pod0"], r.containers[ctr])

		m := containerMessage(r.containers[ctr], r.pods["pod0"])
		require.NotNil(t, m.GetLinux().GetSeccompPolicy(),
			"a plugin has to be shown the policy the container got")
		require.NotEmpty(t, m.GetLinux().GetSeccompPolicy().GetSyscalls())
	})
}

func TestDriverAppliesTheContainerAnnotations(t *testing.T) {
	t.Run("should write the container's annotations into the realized spec", func(t *testing.T) {
		d, i := newTestDriver(t)

		pod, err := d.CreatePod(context.Background(), &framework.PodSpec{
			Name: "pod0", Namespace: "nri-test",
			Containers: []*framework.ContainerSpec{{
				Name:        "ctr0",
				Annotations: map[string]string{"nri-test.containerd.io/initial": "yes"},
			}},
		})
		require.NoError(t, err)

		o, err := i.Inspect(context.Background(), pod.Containers[0].ID)
		require.NoError(t, err)

		// An adjustment that overwrites or removes an annotation has nothing to
		// act on unless the realized spec carries one to begin with.
		require.Equal(t, "yes", o.Spec.Annotations["nri-test.containerd.io/initial"],
			"a runtime writes a container's annotations into its spec")
	})
}

func TestDriverAdvertisesInTreeAdjustments(t *testing.T) {
	d, _ := newTestDriver(t)

	// This driver applies adjustments with the in-tree spec generator, which is
	// the precondition the generate tests gate on.
	require.True(t, d.Capabilities().Has(framework.CapInTreeAdjustments),
		"a driver whose spec comes from the in-tree generator has to say so")
}
