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
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/driver/cri"
	"github.com/containerd/nri/test/nri-test/framework"
)

// removeAttempts bounds how often RemovePod repeats a removal that lost a race
// with kubelet's own.
const removeAttempts = 5

const (
	podUIDLabel  = "io.kubernetes.pod.uid"
	pollInterval = 100 * time.Millisecond

	// keepAliveName is the container given to a pod whose spec has none. A
	// Kubernetes pod cannot exist without a container.
	keepAliveName = "nri-test-keepalive"
)

var (
	// ErrUnsupported is returned for operations a Kubernetes pod cannot
	// express: containers are created, started, stopped and removed with
	// their pod, never apart from it.
	ErrUnsupported = errors.New("not supported by the k8s driver")

	invalidNameChars = regexp.MustCompile(`[^a-z0-9.-]+`)
)

// Driver creates test workloads as Kubernetes pods, so kubelet creates them
// the way it does a user's.
//
// Observation, exec, image pulling and the leak sweeps stay with the CRI
// driver it wraps: the suite runs on the node and the runtime's own view is
// what the tests compare against. Only creation and removal go through the API
// server.
type Driver struct {
	cfg  *framework.Config
	cs   kubernetes.Interface
	cri  *cri.Driver
	node string

	mu        sync.Mutex
	pods      map[string]*corev1.Pod
	ctrPod    map[*framework.Container]*corev1.Pod
	classes   map[string]bool
	createdNS bool
	nsReady   bool
}

var _ framework.Driver = &Driver{}

// NewDriver connects to the cluster and to the node's runtime. It returns the
// driver and the CRI inspector the suite observes containers with.
func NewDriver(ctx context.Context, cfg *framework.Config) (*Driver, *Inspector, error) {
	cs, rc, err := newClients(cfg.Kubeconfig)
	if err != nil {
		return nil, nil, err
	}

	node := cfg.K8sNode
	if node == "" {
		host, err := os.Hostname()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to find this host's name: %w", err)
		}

		node = strings.ToLower(strings.SplitN(host, ".", 2)[0])
	}

	crid, insp, err := cri.NewDriver(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	d := newDriver(cfg, cs, crid, node)
	if err := checkKubeletPolicies(ctx, cs, node); err != nil {
		_ = crid.Close(ctx)
		return nil, nil, err
	}

	return d, &Inspector{
		Inspector: insp,
		cs:        cs,
		rc:        rc,
		ns:        cfg.Namespace,
		runID:     cfg.RunID,
	}, nil
}

func newDriver(cfg *framework.Config, cs kubernetes.Interface, crid *cri.Driver, node string) *Driver {
	return &Driver{
		cfg:     cfg,
		cs:      cs,
		cri:     crid,
		node:    node,
		pods:    map[string]*corev1.Pod{},
		ctrPod:  map[*framework.Container]*corev1.Pod{},
		classes: map[string]bool{},
	}
}

// Name identifies the driver.
func (d *Driver) Name() string { return "k8s" }

// Capabilities returns what this driver can express: exec, and nothing that
// needs a container apart from its pod or resources a pod spec cannot state.
// Exec goes through the API server, see Inspector.
func (d *Driver) Capabilities() framework.Capability { return framework.CapExec }

// ConflictsWithKubelet is false: kubelet running is this driver's precondition.
func (d *Driver) ConflictsWithKubelet() bool { return false }

// ensureNamespace creates the run's namespace if it does not exist. Only a
// namespace created here is deleted again, by the next Cleanup.
//
// It happens on first use, not at construction, because Setup's opening sweep
// calls Cleanup, which would otherwise delete the namespace just made. A
// namespace still terminating from the previous Cleanup is waited out.
func (d *Driver) ensureNamespace(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.nsReady {
		return nil
	}

	ns := d.cfg.Namespace

	for {
		cur, err := d.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if err == nil && cur.Status.Phase != corev1.NamespaceTerminating {
			d.nsReady = true
			return nil
		}

		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to look up namespace %s: %w", ns, err)
		}

		if err != nil {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("namespace %s is still terminating: %w", ns, ctx.Err())
		case <-time.After(pollInterval):
		}
	}

	var err error

	_, err = d.cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{ownedLabel: d.cfg.RunID},
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create namespace %s: %w", ns, err)
	}

	d.createdNS = err == nil
	d.nsReady = true

	return nil
}

// ensureRuntimeClass makes sure a RuntimeClass exists for handler, and returns
// its name. It is owned by this run and removed by Cleanup.
func (d *Driver) ensureRuntimeClass(ctx context.Context, handler string) (string, error) {
	name := fmt.Sprintf("nri-test-%s-%s", d.cfg.RunID, sanitize(handler))

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.classes[name] {
		return name, nil
	}

	_, err := d.cs.NodeV1().RuntimeClasses().Create(ctx, &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{ownedLabel: d.cfg.RunID},
		},
		Handler: handler,
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return "", fmt.Errorf("failed to create RuntimeClass %s: %w", name, err)
	}

	d.classes[name] = true

	return name, nil
}

// CreatePod creates a pod with its containers, and returns once each has a
// runtime id. It writes the API server's pod UID and the pod name it used back
// into the spec, since the tests compare both with what the runtime reports.
func (d *Driver) CreatePod(ctx context.Context, spec *framework.PodSpec) (*framework.Pod, error) {
	spec.Name = sanitize(spec.Name)
	if spec.Namespace == "" {
		spec.Namespace = d.cfg.Namespace
	}

	if err := d.ensureNamespace(ctx); err != nil {
		return nil, err
	}

	ctrs := spec.Containers
	if len(ctrs) == 0 {
		ctrs = []*framework.ContainerSpec{{
			Name:    keepAliveName,
			Command: framework.KeepAliveCommand(),
		}}
	}

	obj := d.podObject(spec, ctrs)
	if spec.RuntimeHandler != "" {
		class, err := d.ensureRuntimeClass(ctx, spec.RuntimeHandler)
		if err != nil {
			return nil, err
		}

		obj.Spec.RuntimeClassName = &class
	}

	created, err := d.cs.CoreV1().Pods(spec.Namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod %s: %w", spec.Name, err)
	}

	spec.UID = string(created.UID)

	d.mu.Lock()
	d.pods[podKey(created)] = created
	d.mu.Unlock()

	pod := &framework.Pod{Spec: spec}

	// The pod is returned with whatever was resolved, so a failure part way
	// still leaves something the suite can clean up and attribute.
	status, err := d.waitContainers(ctx, created, ctrs, false)
	pod.Containers = containers(ctrs, status)

	d.mu.Lock()
	for _, c := range pod.Containers {
		d.ctrPod[c] = created
	}
	d.mu.Unlock()

	if err != nil {
		return pod, err
	}

	id, err := d.sandboxID(ctx, created.UID)
	if err != nil {
		return pod, err
	}

	pod.ID = id
	for _, c := range pod.Containers {
		c.PodID = id
	}

	return pod, nil
}

func containers(ctrs []*framework.ContainerSpec, status *corev1.Pod) []*framework.Container {
	var out []*framework.Container

	for _, c := range ctrs {
		ctr := &framework.Container{Spec: c}

		if status != nil {
			for _, cs := range status.Status.ContainerStatuses {
				if cs.Name == c.Name {
					ctr.ID = trimScheme(cs.ContainerID)
				}
			}
		}

		out = append(out, ctr)
	}

	return out
}

// waitContainers polls until every container has a runtime id, or, when
// running is set, is running. A container kubelet cannot create is an error
// rather than a wait, so a rejected creation fails the case at once instead of
// at its timeout.
func (d *Driver) waitContainers(ctx context.Context, p *corev1.Pod,
	ctrs []*framework.ContainerSpec, running bool) (*corev1.Pod, error) {
	var last *corev1.Pod

	for {
		cur, err := d.cs.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if err != nil {
			return last, fmt.Errorf("failed to get pod %s: %w", p.Name, err)
		}

		last = cur

		ready := 0
		for _, c := range ctrs {
			cs := findStatus(cur, c.Name)
			if cs == nil {
				continue
			}

			if w := cs.State.Waiting; w != nil && strings.HasPrefix(w.Reason, "CreateContainer") {
				return last, fmt.Errorf("container %s of pod %s: %s: %s",
					c.Name, p.Name, w.Reason, w.Message)
			}

			if cs.ContainerID == "" {
				continue
			}

			if running && cs.State.Running == nil && cs.State.Terminated == nil {
				continue
			}

			ready++
		}

		if ready == len(ctrs) {
			return last, nil
		}

		select {
		case <-ctx.Done():
			return last, fmt.Errorf("pod %s: containers not ready: %w", p.Name, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// sandboxID finds the runtime's sandbox for a pod uid. Kubelet labels every
// sandbox with it.
func (d *Driver) sandboxID(ctx context.Context, uid types.UID) (string, error) {
	rt := d.cri.Client().Runtime

	for {
		list, err := rt.ListPodSandbox(ctx, &runtimeapi.PodSandboxFilter{
			LabelSelector: map[string]string{podUIDLabel: string(uid)},
		})
		if err != nil {
			return "", fmt.Errorf("failed to list sandboxes: %w", err)
		}

		if len(list) > 0 {
			return list[0].GetId(), nil
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("no sandbox for pod uid %s: %w", uid, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// StartContainer waits until the container is running. Kubelet starts it.
func (d *Driver) StartContainer(ctx context.Context, c *framework.Container) error {
	d.mu.Lock()
	p := d.podFor(c)
	d.mu.Unlock()

	if p == nil {
		return fmt.Errorf("container %s: no such pod known to the driver", c.ID)
	}

	_, err := d.waitContainers(ctx, p, []*framework.ContainerSpec{c.Spec}, true)

	return err
}

func (d *Driver) podFor(c *framework.Container) *corev1.Pod { return d.ctrPod[c] }

// UpdatePodResources is not supported.
func (d *Driver) UpdatePodResources(context.Context, *framework.Pod, *framework.Resources) error {
	return ErrUnsupported
}

// StopPod deletes the pod object, and waits until the API server has let go of
// it, which is when kubelet has been told to tear it down.
func (d *Driver) StopPod(ctx context.Context, p *framework.Pod) error {
	grace := d.cfg.StopGraceSeconds()

	err := d.cs.CoreV1().Pods(p.Spec.Namespace).Delete(ctx, p.Spec.Name,
		metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pod %s: %w", p.Spec.Name, err)
	}

	for {
		_, err := d.cs.CoreV1().Pods(p.Spec.Namespace).Get(ctx, p.Spec.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("failed to get pod %s: %w", p.Spec.Name, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("pod %s not deleted: %w", p.Spec.Name, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// RemovePod makes sure the pod's sandbox and containers are gone from the
// runtime. Kubelet garbage-collects them on its own schedule, which is far
// slower than a test waits, so the sandbox is removed here as well; the
// runtime treats the loser of that race as already done.
func (d *Driver) RemovePod(ctx context.Context, p *framework.Pod) error {
	if err := d.StopPod(ctx, p); err != nil {
		return err
	}

	d.mu.Lock()
	delete(d.pods, p.Spec.Namespace+"/"+p.Spec.Name)
	d.mu.Unlock()

	if p.ID == "" {
		return nil
	}

	rt := d.cri.Client().Runtime

	// Stop is attempted regardless: removal force-stops in both runtimes, but
	// the stop is what a kubelet-less teardown would have done first.
	if err := rt.StopPodSandbox(ctx, p.ID); err != nil && !isNotFound(err) {
		return fmt.Errorf("failed to stop sandbox %s: %w", p.ID, err)
	}

	var err error

	for range removeAttempts {
		err = rt.RemovePodSandbox(ctx, p.ID)
		if err == nil || isNotFound(err) {
			return nil
		}

		if !isRaceLost(err) {
			break
		}

		fmt.Fprintf(os.Stderr, "nri-test: removal of sandbox %s lost a race with kubelet, retrying: %v\n", p.ID, err)

		select {
		case <-ctx.Done():
		case <-time.After(pollInterval):
		}
	}

	return fmt.Errorf("failed to remove sandbox %s: %w", p.ID, err)
}

// CreateContainer is not supported: a pod's containers are created with it.
func (d *Driver) CreateContainer(context.Context, *framework.Pod, *framework.ContainerSpec) (*framework.Container, error) {
	return nil, ErrUnsupported
}

// UpdateContainerResources is not supported.
func (d *Driver) UpdateContainerResources(context.Context, *framework.Container, *framework.Resources) error {
	return ErrUnsupported
}

// StopContainer is not supported: stopping is the pod's.
func (d *Driver) StopContainer(context.Context, *framework.Container) error {
	return ErrUnsupported
}

// RemoveContainer is not supported: removal is the pod's.
func (d *Driver) RemoveContainer(context.Context, *framework.Container) error {
	return ErrUnsupported
}

// Cleanup deletes this run's pods, RuntimeClasses and, if it created it, the
// namespace, then lets the CRI driver sweep what the runtime still holds.
func (d *Driver) Cleanup(ctx context.Context) error {
	var errs []error

	grace := d.cfg.StopGraceSeconds()
	sel := metav1.ListOptions{LabelSelector: framework.RunIDLabel + "=" + d.cfg.RunID}

	err := d.cs.CoreV1().Pods(d.cfg.Namespace).DeleteCollection(ctx,
		metav1.DeleteOptions{GracePeriodSeconds: &grace}, sel)
	if err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("failed to delete pods: %w", err))
	}

	d.mu.Lock()
	classes := d.classes
	d.classes = map[string]bool{}
	d.pods = map[string]*corev1.Pod{}
	d.ctrPod = map[*framework.Container]*corev1.Pod{}
	d.mu.Unlock()

	for name := range classes {
		err := d.cs.NodeV1().RuntimeClasses().Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete RuntimeClass %s: %w", name, err))
		}
	}

	if err := d.cri.Cleanup(ctx); err != nil {
		errs = append(errs, err)
	}

	d.mu.Lock()
	created := d.createdNS
	d.createdNS, d.nsReady = false, false
	d.mu.Unlock()

	if created {
		err := d.cs.CoreV1().Namespaces().Delete(ctx, d.cfg.Namespace, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("failed to delete namespace %s: %w", d.cfg.Namespace, err))
		}
	}

	return errors.Join(errs...)
}

// ListRunPods reports the runtime's sandboxes this run owns.
func (d *Driver) ListRunPods(ctx context.Context) ([]string, error) {
	return d.cri.ListRunPods(ctx)
}

// ListRunContainers reports the runtime's containers this run owns.
func (d *Driver) ListRunContainers(ctx context.Context) ([]string, error) {
	return d.cri.ListRunContainers(ctx)
}

// StalePods reports other runs' pods the CRI listing walked past.
func (d *Driver) StalePods() []framework.StalePod { return d.cri.StalePods() }

// RuntimeVersion reports the runtime kubelet drives.
func (d *Driver) RuntimeVersion(ctx context.Context) (string, string, error) {
	return d.cri.RuntimeVersion(ctx)
}

// RuntimeHandlers reports the handlers the node's runtime has installed, which
// are what a RuntimeClass can name.
func (d *Driver) RuntimeHandlers(ctx context.Context) ([]string, error) {
	return d.cri.RuntimeHandlers(ctx)
}

// PullImage pre-pulls the test image into the node's runtime.
func (d *Driver) PullImage(ctx context.Context, image string) error {
	return d.cri.PullImage(ctx, image)
}

// Close releases the CRI connection.
func (d *Driver) Close(ctx context.Context) error { return d.cri.Close(ctx) }

func podKey(p *corev1.Pod) string { return p.Namespace + "/" + p.Name }

func findStatus(p *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range p.Status.ContainerStatuses {
		if p.Status.ContainerStatuses[i].Name == name {
			return &p.Status.ContainerStatuses[i]
		}
	}

	return nil
}

// trimScheme strips the "cri-o://" or "containerd://" prefix kubelet puts on a
// container id.
func trimScheme(id string) string {
	if i := strings.Index(id, "://"); i >= 0 {
		return id[i+3:]
	}

	return id
}

func sanitize(s string) string {
	s = invalidNameChars.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-.")

	if len(s) > 253 {
		s = strings.Trim(s[:253], "-.")
	}

	return s
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

// isRaceLost reports whether err is CRI-O's account of a removal that collided
// with kubelet removing the same container: its index no longer has an id that
// the sandbox still lists. The sandbox is not gone, only half removed, and
// removing it again finishes the job.
func isRaceLost(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such id")
}

func (d *Driver) podObject(spec *framework.PodSpec, ctrs []*framework.ContainerSpec) *corev1.Pod {
	grace := d.cfg.StopGraceSeconds()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        spec.Name,
			Namespace:   spec.Namespace,
			Labels:      spec.Labels,
			Annotations: spec.Annotations,
		},
		Spec: corev1.PodSpec{
			// The suite runs on one node and inspects through its runtime,
			// so the pod must land there. Naming the node skips the
			// scheduler.
			NodeName:                      d.node,
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &grace,
		},
	}

	for i, c := range ctrs {
		image := c.Image
		if image == "" {
			image = d.cfg.Image
		}

		kc := corev1.Container{
			Name:            c.Name,
			Image:           image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         c.Command,
			Args:            c.Args,
			Resources:       resources(c.Resources),
		}

		for k, v := range c.Env {
			kc.Env = append(kc.Env, corev1.EnvVar{Name: k, Value: v})
		}

		for j, m := range c.Mounts {
			vol := fmt.Sprintf("m%d-%d", i, j)
			hostPath := corev1.HostPathUnset

			pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
				Name: vol,
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: m.Source, Type: &hostPath},
				},
			})
			kc.VolumeMounts = append(kc.VolumeMounts, corev1.VolumeMount{
				Name: vol, MountPath: m.Destination, ReadOnly: m.Readonly,
			})
		}

		pod.Spec.Containers = append(pod.Spec.Containers, kc)
	}

	return pod
}

// resources maps what a pod spec can state. Anything else -- cpusets, OOM
// score, unified cgroup values -- has no Kubernetes expression, which is what
// CapRawResources reports.
func resources(r *framework.Resources) corev1.ResourceRequirements {
	var out corev1.ResourceRequirements

	if r == nil {
		return out
	}

	set := func(m *corev1.ResourceList, name corev1.ResourceName, q resource.Quantity) {
		if *m == nil {
			*m = corev1.ResourceList{}
		}

		(*m)[name] = q
	}

	if r.MemoryLimit > 0 {
		set(&out.Limits, corev1.ResourceMemory, *resource.NewQuantity(r.MemoryLimit, resource.BinarySI))
	}

	if r.CPUQuota > 0 && r.CPUPeriod > 0 {
		set(&out.Limits, corev1.ResourceCPU,
			*resource.NewMilliQuantity(r.CPUQuota*1000/r.CPUPeriod, resource.DecimalSI))
	}

	if r.CPUShares > 0 {
		set(&out.Requests, corev1.ResourceCPU,
			*resource.NewMilliQuantity(r.CPUShares*1000/1024, resource.DecimalSI))
	}

	return out
}
