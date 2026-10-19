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

// This file drives a ReplicaSet through a cluster, for the scalability
// measurement. It is deliberately NOT a framework.Driver: the measurement
// comes from NRI, which sees every pod on the node whoever created it, so
// scaling a ReplicaSet needs only enough of a client to create one, change
// its replica count, and remove what it made.

package k8s

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/containerd/nri/test/nri-test/framework"
)

const (
	// appLabel selects the ReplicaSet's pods.
	appLabel = "nri-test.containerd.io/scale-app"
)

// Scaler creates and scales one ReplicaSet in a namespace of its own.
type Scaler struct {
	cs    kubernetes.Interface
	cfg   *framework.Config
	ns    string
	name  string
	app   string
	image string

	// createdNS records that this Scaler created the namespace. Removal
	// refuses to delete one it did not create, so pointing the run at an
	// existing namespace cannot destroy it.
	createdNS bool
}

// NewScaler connects to the cluster named by kubeconfig and returns a Scaler
// that will work in a namespace derived from the run id.
//
// An empty kubeconfig path falls back to the usual client-go rules, so
// KUBECONFIG and the in-cluster service account both work.
func NewScaler(cfg *framework.Config, kubeconfig string) (*Scaler, error) {
	cs, err := newClientset(kubeconfig)
	if err != nil {
		return nil, err
	}

	if cfg.RunID == "" {
		return nil, errors.New("the run has no run id, so a scalability namespace " +
			"could not be told apart from another run's")
	}

	return &Scaler{
		cs:  cs,
		cfg: cfg,
		// Namespace, ReplicaSet and selector all carry the run id, so two
		// concurrent runs on one cluster cannot collide or adopt each other's
		// pods. The namespace comes from framework so that the name the
		// plugins filter on and the name the ReplicaSet lives in cannot drift
		// apart.
		ns:    framework.ScaleNamespace(cfg.RunID),
		name:  "nri-test-scale-" + cfg.RunID,
		app:   "nri-test-scale-" + cfg.RunID,
		image: cfg.Image,
	}, nil
}

// Namespace returns the namespace this Scaler works in. The suite points
// -nri-test.namespace at it, so the plugins' scope filter accepts exactly
// these pods: a Kubernetes pod's CRI sandbox namespace is its Kubernetes
// namespace.
func (s *Scaler) Namespace() string { return s.ns }

// Selector returns the label selector for the ReplicaSet's pods.
func (s *Scaler) Selector() string { return appLabel + "=" + s.app }

// EnsureNamespace creates the run's namespace, waiting out one left
// terminating by an earlier iteration.
//
// It refuses to proceed if the namespace already exists without this run's
// ownership label. A namespace is a container for other people's work, and the
// one destructive thing here is deleting it, so the predicate that permits
// that is derived from scratch rather than borrowed: created by us, labelled by
// us, named after this run.
func (s *Scaler) EnsureNamespace(ctx context.Context) error {
	usable, err := s.waitNamespaceUsable(ctx)
	if err != nil {
		return err
	}

	if usable {
		return nil
	}

	_, err = s.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   s.ns,
			Labels: map[string]string{ownedLabel: s.cfg.RunID},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create namespace %s: %w", s.ns, err)
	}

	s.createdNS = true

	return nil
}

// waitNamespaceUsable reports whether the run's namespace already exists and
// can be used as it is. False with no error means it is absent and the caller
// has to create it.
//
// The wait is what makes more than one iteration possible. Every iteration
// builds a Scaler with the same run-id-derived names, and the previous one's
// cleanup deletes the namespace without waiting for the namespace controller
// to finish. So the next iteration finds it Terminating and carrying this
// run's own ownership label: treating that as usable left createdNS false and
// sent Create at a terminating namespace, which the API server refuses. The
// conformance driver's ensureNamespace waits the same way, and for the same
// reason.
func (s *Scaler) waitNamespaceUsable(ctx context.Context) (bool, error) {
	for {
		existing, err := s.cs.CoreV1().Namespaces().Get(ctx, s.ns, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("failed to look up namespace %s: %w", s.ns, err)
		}

		if existing.Labels[ownedLabel] != s.cfg.RunID {
			return false, fmt.Errorf("namespace %s already exists and is not labelled "+
				"%s=%s, so this run will not touch it", s.ns, ownedLabel, s.cfg.RunID)
		}

		if existing.Status.Phase != corev1.NamespaceTerminating {
			// Ours from an earlier phase of this same run.
			return true, nil
		}

		select {
		case <-ctx.Done():
			return false, fmt.Errorf("namespace %s is still terminating: %w", s.ns, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// Create creates the ReplicaSet at the given replica count.
//
// The pod template carries the run id in both labels and annotations, so every
// replica arrives already attributable: the plugins' scope filter and the
// stale-workload predicate both read it, and the measurement can tell this
// burst's pods from anything else on the node.
func (s *Scaler) Create(ctx context.Context, replicas int32) error {
	labels := map[string]string{
		appLabel:             s.app,
		framework.RunIDLabel: s.cfg.RunID,
		ownedLabel:           s.cfg.RunID,
	}

	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.name,
			Namespace: s.ns,
			Labels:    labels,
		},
		Spec: appsv1.ReplicaSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{appLabel: s.app},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: map[string]string{framework.RunIDLabel: s.cfg.RunID},
				},
				Spec: corev1.PodSpec{
					// A long sleep for the same reason Suite.CtrSpec uses one:
					// busybox's default command exits at once without a TTY, and a
					// pod that exits is restarted, which would produce a second
					// burst of events on top of the one being measured.
					Containers: []corev1.Container{{
						Name:    "scale",
						Image:   s.image,
						Command: []string{"sleep", "3600"},
					}},
					// Nothing here sets a runtime class. The scalability number is
					// per low-level runtime too, but selecting one needs a
					// RuntimeClass object, which is cluster-scoped; that widening is
					// deliberately not part of this first cut.
					TerminationGracePeriodSeconds: int64Ptr(0),
				},
			},
		},
	}

	if _, err := s.cs.AppsV1().ReplicaSets(s.ns).Create(ctx, rs, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create ReplicaSet %s/%s: %w", s.ns, s.name, err)
	}

	return nil
}

// Scale sets the ReplicaSet's replica count.
//
// One API call once it succeeds, so the burst is as fast as the control plane
// will deliver it, which is the point: the measurement wants the arrivals
// bunched, not paced by the client.
//
// Retried on conflict, because this is a read-modify-write on the scale
// subresource and it races the ReplicaSet controller writing status. Measured
// on the test node, 2 of 5 scale-downs lost that race with "the object has
// been modified; please apply your changes to the latest version" -- which
// took out the whole teardown measurement for a reason that has nothing to do
// with what was being measured. Each attempt re-reads the scale, so the retry
// applies the replica count to the version the controller just wrote rather
// than to a stale one.
func (s *Scaler) Scale(ctx context.Context, replicas int32) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		scale, err := s.cs.AppsV1().ReplicaSets(s.ns).GetScale(ctx, s.name, metav1.GetOptions{})
		if err != nil {
			return err
		}

		scale.Spec.Replicas = replicas

		_, err = s.cs.AppsV1().ReplicaSets(s.ns).UpdateScale(
			ctx, s.name, scale, metav1.UpdateOptions{})

		return err
	})
	if err != nil {
		return fmt.Errorf("failed to scale %s/%s to %d: %w", s.ns, s.name, replicas, err)
	}

	return nil
}

// PodNames returns the names of the ReplicaSet's pods that currently exist,
// sorted, whatever phase they are in.
//
// Sorted so a diff against an earlier call is stable. Pods are returned
// regardless of phase because a pod that never started is still one whose
// events the measurement has to account for.
func (s *Scaler) PodNames(ctx context.Context) ([]string, error) {
	pods, err := s.cs.CoreV1().Pods(s.ns).List(ctx, metav1.ListOptions{
		LabelSelector: s.Selector(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list pods in %s: %w", s.ns, err)
	}

	names := make([]string, 0, len(pods.Items))
	for i := range pods.Items {
		names = append(names, pods.Items[i].Name)
	}

	sort.Strings(names)

	return names, nil
}

// WaitRunning waits until at least want of the ReplicaSet's pods are Running,
// polling until the deadline.
//
// Polling rather than a watch: the measurement does not come from here, so the
// only thing this has to do is be sure the cluster got there before the next
// step, and a poll cannot miss a transition it is not timing.
func (s *Scaler) WaitRunning(ctx context.Context, want int, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		pods, err := s.cs.CoreV1().Pods(s.ns).List(ctx, metav1.ListOptions{
			LabelSelector: s.Selector(),
		})
		if err != nil {
			return fmt.Errorf("failed to list pods in %s: %w", s.ns, err)
		}

		running := 0
		for i := range pods.Items {
			if pods.Items[i].Status.Phase == corev1.PodRunning {
				running++
			}
		}

		if running >= want {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("only %d of %d pods in %s were Running within %s",
				running, want, s.ns, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Cleanup removes the ReplicaSet and, if this Scaler created it, the
// namespace.
//
// The namespace is deleted only when this Scaler created it. A namespace that
// was already there when the run started is left alone even though it carries
// the ownership label, because the label makes it safe to *use*, not safe to
// destroy -- the same distinction the pod sweep draws between a filter and a
// removal.
func (s *Scaler) Cleanup(ctx context.Context) error {
	var errs []error

	policy := metav1.DeletePropagationForeground
	err := s.cs.AppsV1().ReplicaSets(s.ns).Delete(ctx, s.name, metav1.DeleteOptions{
		PropagationPolicy: &policy,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("failed to delete ReplicaSet %s/%s: %w", s.ns, s.name, err))
	}

	if s.createdNS {
		err := s.cs.CoreV1().Namespaces().Delete(ctx, s.ns, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete namespace %s: %w", s.ns, err))
		}
	}

	return errors.Join(errs...)
}

func int64Ptr(v int64) *int64 { return &v }
