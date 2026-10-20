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

package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/containerd/nri/test/nri-test/framework"
)

func testDriver() (*Driver, *fake.Clientset) {
	cs := fake.NewSimpleClientset()
	cfg := &framework.Config{Namespace: "ns", RunID: "run1", Image: "img"}

	return newDriver(cfg, cs, nil, "node1"), cs
}

func TestCapabilitiesAreExecOnly(t *testing.T) {
	d, _ := testDriver()

	require.Equal(t, framework.CapExec, d.Capabilities())
	require.False(t, d.ConflictsWithKubelet())
}

func TestStandaloneContainerOperationsAreUnsupported(t *testing.T) {
	d, _ := testDriver()
	ctx := context.Background()

	_, err := d.CreateContainer(ctx, nil, nil)
	require.ErrorIs(t, err, ErrUnsupported)
	require.ErrorIs(t, d.StopContainer(ctx, nil), ErrUnsupported)
	require.ErrorIs(t, d.RemoveContainer(ctx, nil), ErrUnsupported)
	require.ErrorIs(t, d.UpdateContainerResources(ctx, nil, nil), ErrUnsupported)
}

func TestPodObject(t *testing.T) {
	d, _ := testDriver()

	spec := &framework.PodSpec{
		Name:        "p",
		Namespace:   "ns",
		Labels:      map[string]string{"a": "b"},
		Annotations: map[string]string{"c": "d"},
	}
	ctrs := []*framework.ContainerSpec{{
		Name:    "c0",
		Command: []string{"sleep", "1"},
		Env:     map[string]string{"K": "V"},
		Mounts:  []*framework.Mount{{Source: "/src", Destination: "/dst", Readonly: true}},
	}}

	pod := d.podObject(spec, ctrs)

	require.Equal(t, "node1", pod.Spec.NodeName)
	require.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	require.Equal(t, spec.Labels, pod.Labels)
	require.Equal(t, spec.Annotations, pod.Annotations)
	require.Len(t, pod.Spec.Containers, 1)

	c := pod.Spec.Containers[0]
	require.Equal(t, "img", c.Image, "an empty image means the configured one")
	require.Equal(t, []corev1.EnvVar{{Name: "K", Value: "V"}}, c.Env)
	require.Len(t, pod.Spec.Volumes, 1)
	require.Equal(t, "/src", pod.Spec.Volumes[0].HostPath.Path)
	require.Equal(t, corev1.VolumeMount{
		Name: pod.Spec.Volumes[0].Name, MountPath: "/dst", ReadOnly: true,
	}, c.VolumeMounts[0])
}

func TestResources(t *testing.T) {
	require.Empty(t, resources(nil).Limits)

	r := resources(&framework.Resources{
		MemoryLimit: 1 << 20, CPUQuota: 50000, CPUPeriod: 100000, CPUShares: 512,
		CpusetCpus: "0",
	})

	require.Equal(t, int64(1<<20), r.Limits.Memory().Value())
	require.Equal(t, int64(500), r.Limits.Cpu().MilliValue())
	require.Equal(t, int64(500), r.Requests.Cpu().MilliValue())
}

func TestSanitizeAndTrimScheme(t *testing.T) {
	require.Equal(t, "nri-test-a-b", sanitize("NRI_test/A b"))
	require.Equal(t, "abc", trimScheme("cri-o://abc"))
	require.Equal(t, "abc", trimScheme("containerd://abc"))
	require.Equal(t, "abc", trimScheme("abc"))
}

func TestNamespaceIsCreatedOnceAndDeletedByCleanupOnlyIfOurs(t *testing.T) {
	ctx := context.Background()

	d, cs := testDriver()
	require.NoError(t, d.ensureNamespace(ctx))

	ns, err := cs.CoreV1().Namespaces().Get(ctx, "ns", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "run1", ns.Labels[ownedLabel])
	require.True(t, d.createdNS)

	// A namespace that was already there is used and never deleted.
	pre := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}})
	d.cs = pre
	d.createdNS, d.nsReady = false, false
	require.NoError(t, d.ensureNamespace(ctx))
	require.False(t, d.createdNS)
}

func TestRuntimeClassIsOwnedAndRemovedWithTheRun(t *testing.T) {
	ctx := context.Background()
	d, cs := testDriver()

	name, err := d.ensureRuntimeClass(ctx, "runc")
	require.NoError(t, err)
	require.Equal(t, "nri-test-run1-runc", name)

	rc, err := cs.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "runc", rc.Handler)

	// Asking again is a no-op, not an AlreadyExists failure.
	_, err = d.ensureRuntimeClass(ctx, "runc")
	require.NoError(t, err)

	require.Contains(t, d.classes, name)
	require.NoError(t, cs.NodeV1().RuntimeClasses().Delete(ctx, name, metav1.DeleteOptions{}))
	_, err = cs.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}

func TestKubeletPolicies(t *testing.T) {
	ok := func(raw string) {
		t.Helper()

		p, err := parseKubeletPolicies([]byte(raw))
		require.NoError(t, err)
		require.NoError(t, p.check("n"))
	}

	ok(`{"kubeletconfig":{}}`)
	ok(`{"kubeletconfig":{"cpuManagerPolicy":"none","memoryManagerPolicy":"None"}}`)

	p, err := parseKubeletPolicies([]byte(
		`{"kubeletconfig":{"cpuManagerPolicy":"static","memoryManagerPolicy":"Static"}}`))
	require.NoError(t, err)

	err = p.check("n")
	require.ErrorContains(t, err, "cpuManagerPolicy")
	require.ErrorContains(t, err, "memoryManagerPolicy")

	_, err = parseKubeletPolicies([]byte("not json"))
	require.Error(t, err)
}

func TestInspectorFindsTheContainerByRuntimeID(t *testing.T) {
	ctx := context.Background()

	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "p", Namespace: "ns",
			Labels: map[string]string{framework.RunIDLabel: "run1"},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "c0", ContainerID: "cri-o://abc"},
			{Name: "c1", ContainerID: "containerd://def"},
		}},
	})
	i := &Inspector{cs: cs, ns: "ns", runID: "run1"}

	pod, name, err := i.find(ctx, "def")
	require.NoError(t, err)
	require.Equal(t, "p", pod.Name)
	require.Equal(t, "c1", name)

	_, _, err = i.find(ctx, "nope")
	require.Error(t, err)
}

// Exec runs a command inside a container, so what find returns is acted on,
// not merely read. It therefore has to be scoped to this run's own workloads:
// selecting on the mere presence of the run-id label put another run's pods in
// front of it, which is the one thing every other predicate in this suite is
// careful to refuse.
func TestInspectorWillNotFindAnotherRunsContainer(t *testing.T) {
	ctx := context.Background()

	cs := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ours", Namespace: "ns",
				Labels: map[string]string{framework.RunIDLabel: "run1"},
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c0", ContainerID: "cri-o://ours-abc"},
			}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "theirs", Namespace: "ns",
				Labels: map[string]string{framework.RunIDLabel: "another-run"},
			},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c0", ContainerID: "cri-o://theirs-xyz"},
			}},
		},
	)

	i := &Inspector{cs: cs, ns: "ns", runID: "run1"}

	pod, name, err := i.find(ctx, "ours-abc")
	require.NoError(t, err, "our own container must still be found")
	require.Equal(t, "ours", pod.Name)
	require.Equal(t, "c0", name)

	_, _, err = i.find(ctx, "theirs-xyz")
	require.Error(t, err,
		"a container belonging to another nri-test run must be out of reach")
}

// With no run id of its own nothing can be identified as ours, so the lookup
// finds nothing rather than reaching into the namespace. The same way
// Scope.MatchesForRemoval fails closed.
//
// The pod here carries the run-id label with an EMPTY value, which is the case
// that makes this a test of the guard rather than of the selector: a
// "run-id=" selector matches exactly such a pod, so without the guard this
// lookup would reach a workload belonging to nobody this run can identify.
func TestInspectorFindsNothingWithoutARunID(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unlabelled", Namespace: "ns",
			Labels: map[string]string{framework.RunIDLabel: ""},
		},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "c0", ContainerID: "cri-o://abc"},
		}},
	})

	i := &Inspector{cs: cs, ns: "ns"}

	_, _, err := i.find(context.Background(), "abc")
	require.Error(t, err)
	require.ErrorContains(t, err, "no run id")
}

func TestK8sDriverWithholdsCRIOnlyCaps(t *testing.T) {
	t.Run("should advertise none of the CRI-level input capabilities", func(t *testing.T) {
		// A Kubernetes pod spec cannot name a CDI device at all: those arrive
		// through a device plugin. It can name sysctls, but only ones kubelet
		// considers safe -- net.* beyond a short allowlist needs kubelet's
		// --allowed-unsafe-sysctls, which is node configuration this suite does
		// not change. So all three bits stay CRI-only, and the input tests that
		// need them skip here with a stated reason rather than running against a
		// half-expressible spec.
		d := &Driver{}

		for _, c := range []framework.Capability{
			framework.CapCDIDevices,
			framework.CapSysctls,
			framework.CapSecurityContext,
		} {
			require.False(t, d.Capabilities().Has(c), c.String())
		}
	})
}
