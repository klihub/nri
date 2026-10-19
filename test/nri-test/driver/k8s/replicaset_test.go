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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/containerd/nri/test/nri-test/framework"
)

func testScaler(cs *fake.Clientset) *Scaler {
	cfg := &framework.Config{RunID: "run1", Image: "img"}

	return &Scaler{
		cs:    cs,
		cfg:   cfg,
		ns:    framework.ScaleNamespace(cfg.RunID),
		name:  "nri-test-scale-" + cfg.RunID,
		app:   "nri-test-scale-" + cfg.RunID,
		image: cfg.Image,
	}
}

func ourNamespace(name string, phase corev1.NamespacePhase) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{ownedLabel: "run1"}},
		Status:     corev1.NamespaceStatus{Phase: phase},
	}
}

func TestEnsureNamespaceCreatesAndOwnsAMissingNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset()
	s := testScaler(cs)

	require.NoError(t, s.EnsureNamespace(context.Background()))
	require.True(t, s.createdNS, "a namespace we made is ours to delete")

	ns, err := cs.CoreV1().Namespaces().Get(context.Background(), s.Namespace(), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "run1", ns.Labels[ownedLabel])
}

func TestEnsureNamespaceRefusesOneThatIsNotOurs(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: framework.ScaleNamespace("run1")},
	})
	s := testScaler(cs)

	err := s.EnsureNamespace(context.Background())

	require.Error(t, err, "a namespace holding somebody else's work is not ours to use")
	require.ErrorContains(t, err, ownedLabel)
	require.False(t, s.createdNS)
}

func TestEnsureNamespaceAcceptsOurOwnLiveNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset(ourNamespace(framework.ScaleNamespace("run1"), corev1.NamespaceActive))
	s := testScaler(cs)

	require.NoError(t, s.EnsureNamespace(context.Background()))
	require.False(t, s.createdNS,
		"a namespace from an earlier phase of this run is ours to use, not ours to delete")
}

// The second iteration of a run. Iteration one's cleanup deletes the namespace
// without waiting, so iteration two finds it Terminating and carrying this
// run's own ownership label. Returning nil there left createdNS false and sent
// Create straight at a terminating namespace, which the API server refuses --
// so -nri-test.scale-iterations greater than one could never work.
func TestEnsureNamespaceWaitsOutATerminatingNamespaceAndRecreatesIt(t *testing.T) {
	cs := fake.NewSimpleClientset(
		ourNamespace(framework.ScaleNamespace("run1"), corev1.NamespaceTerminating))

	// Terminating for the first two reads, then really gone -- removed from
	// the tracker, so the Create that follows sees what it would see on a real
	// cluster rather than a leftover object the fixture never cleared.
	gets := 0
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets <= 2 {
			return true, ourNamespace(framework.ScaleNamespace("run1"), corev1.NamespaceTerminating), nil
		}

		_ = cs.Tracker().Delete(
			corev1.SchemeGroupVersion.WithResource("namespaces"), "", framework.ScaleNamespace("run1"))

		return false, nil, nil
	})

	s := testScaler(cs)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, s.EnsureNamespace(ctx))
	require.Greater(t, gets, 2, "the wait must poll rather than give up on the first read")
	require.True(t, s.createdNS, "the namespace we recreated after the wait is ours")
}

// A namespace that never finishes terminating must fail with a reason that
// names the cause, not time out somewhere later with an unrelated one.
func TestEnsureNamespaceGivesUpOnANamespaceThatNeverTerminates(t *testing.T) {
	cs := fake.NewSimpleClientset(
		ourNamespace(framework.ScaleNamespace("run1"), corev1.NamespaceTerminating))
	s := testScaler(cs)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := s.EnsureNamespace(ctx)

	require.Error(t, err)
	require.ErrorContains(t, err, "terminating")
}

// Scaling is a read-modify-write on the ReplicaSet's scale subresource, so it
// races the ReplicaSet controller writing status: measured on the test node,
// 2 of 5 runs failed the scale-down with "the object has been modified".
// Losing that race must cost a retry, not the measurement.
func TestScaleRetriesAConflict(t *testing.T) {
	cs := fake.NewSimpleClientset()

	gets, updates := 0, 0

	cs.PrependReactor("get", "replicasets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "scale" {
			return false, nil, nil
		}

		gets++

		return true, &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{Name: "nri-test-scale-run1", Namespace: framework.ScaleNamespace("run1")},
			Spec:       autoscalingv1.ScaleSpec{Replicas: 1},
		}, nil
	})

	cs.PrependReactor("update", "replicasets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "scale" {
			return false, nil, nil
		}

		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "replicasets"},
				"nri-test-scale-run1", errors.New("the object has been modified"))
		}

		return true, &autoscalingv1.Scale{}, nil
	})

	s := testScaler(cs)

	require.NoError(t, s.Scale(context.Background(), 5),
		"a conflict is the controller having written status, not a failure of the scale")
	require.Equal(t, 2, updates, "the update must be retried after a conflict")
	require.Equal(t, 2, gets, "the retry must re-read the scale, not reuse the stale one")
}
