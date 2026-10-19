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

package cri

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/nri/test/nri-test/framework"
)

const (
	// stopTimeout is the grace period given to a container being stopped.
	stopTimeout = int64(0)
)

// Driver creates and mutates test workloads over CRI.
type Driver struct {
	cfg    *framework.Config
	client *Client

	mu   sync.Mutex
	pods []*framework.Pod
	caps framework.Capability
	// stale is what the most recent listing saw of other runs' pods in our
	// namespace. Reported to the operator, never acted on; see StalePods.
	stale []framework.StalePod
}

var _ framework.Driver = &Driver{}

// NewDriver connects to the runtime and returns a Driver and an Inspector
// sharing one client.
//
// The signature stays two-plus-error although the Inspector now carries the exec
// surface too: framework.Setup discovers that by type assertion, so a new
// observation surface does not widen this seam. See framework.executorOf.
func NewDriver(ctx context.Context, cfg *framework.Config) (*Driver, *Inspector, error) {
	client, err := NewClient(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}

	d := &Driver{cfg: cfg, client: client}
	d.caps = d.probeCapabilities()

	return d, NewInspector(client), nil
}

// Name identifies the driver.
func (d *Driver) Name() string {
	return "cri"
}

// Capabilities returns what this driver can express.
func (d *Driver) Capabilities() framework.Capability {
	return d.caps
}

// probeCapabilities determines what the runtime behind this driver supports.
//
// Container lifecycle, error observation, container resource updates and exec
// are inherent to CRI: ExecSync is a required method of the runtime service, and
// whether the container's image has anything to run is the image's business, not
// the driver's.
//
// CapUpdatePodResources is deliberately never set. UpdatePodSandboxResources
// exists in cri-api, but whether a runtime implements it and maps it onto the NRI
// UpdatePodSandbox hook is unverified, and there is no side-effect-free way to
// ask. Tests needing it therefore always skip; see TestPodResourceUpdateEvents.
// When a runtime is confirmed to implement it, add the probe here.
func (d *Driver) probeCapabilities() framework.Capability {
	return framework.CapContainerLifecycle |
		framework.CapObserveOpError |
		framework.CapUpdateContainerResources |
		framework.CapExec
}

// CreatePod creates a pod sandbox and any containers its spec carries.
func (d *Driver) CreatePod(ctx context.Context, p *framework.PodSpec) (*framework.Pod, error) {
	cfg := PodSandboxConfig(p)

	id, err := d.client.Runtime.RunPodSandbox(ctx, cfg, "")
	if err != nil {
		return nil, fmt.Errorf("failed to run pod sandbox %s: %w", p.Name, err)
	}

	pod := &framework.Pod{ID: id, Spec: p}

	d.mu.Lock()
	d.pods = append(d.pods, pod)
	d.mu.Unlock()

	for _, c := range p.Containers {
		if _, err := d.CreateContainer(ctx, pod, c); err != nil {
			return pod, err
		}
	}

	return pod, nil
}

// UpdatePodResources updates a pod's resources. A nil r leaves the request's
// resources unset, which asks the runtime for no change.
func (d *Driver) UpdatePodResources(ctx context.Context, p *framework.Pod, r *framework.Resources) error {
	_, err := d.client.Runtime.UpdatePodSandboxResources(ctx,
		&runtimeapi.UpdatePodSandboxResourcesRequest{
			PodSandboxId: p.ID,
			Resources:    linuxResources(r),
			Overhead:     &runtimeapi.LinuxContainerResources{},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to update resources of pod %s: %w", p.ID, err)
	}

	return nil
}

// StopPod stops a pod sandbox.
func (d *Driver) StopPod(ctx context.Context, p *framework.Pod) error {
	if err := d.client.Runtime.StopPodSandbox(ctx, p.ID); err != nil {
		return fmt.Errorf("failed to stop pod sandbox %s: %w", p.ID, err)
	}

	return nil
}

// RemovePod removes a pod sandbox.
func (d *Driver) RemovePod(ctx context.Context, p *framework.Pod) error {
	if err := d.client.Runtime.RemovePodSandbox(ctx, p.ID); err != nil {
		return fmt.Errorf("failed to remove pod sandbox %s: %w", p.ID, err)
	}

	d.mu.Lock()
	d.pods = slices.DeleteFunc(d.pods, func(o *framework.Pod) bool {
		return o.ID == p.ID
	})
	d.mu.Unlock()

	return nil
}

// CreateContainer creates a container in a pod.
func (d *Driver) CreateContainer(ctx context.Context, p *framework.Pod, c *framework.ContainerSpec) (*framework.Container, error) {
	var (
		podCfg = PodSandboxConfig(p.Spec)
		ctrCfg = ContainerConfig(d.cfg, c)
	)

	id, err := d.client.Runtime.CreateContainer(ctx, p.ID, ctrCfg, podCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create container %s in pod %s: %w",
			c.Name, p.ID, err)
	}

	return &framework.Container{ID: id, PodID: p.ID, Spec: c}, nil
}

// StartContainer starts a created container.
func (d *Driver) StartContainer(ctx context.Context, c *framework.Container) error {
	if err := d.client.Runtime.StartContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("failed to start container %s: %w", c.ID, err)
	}

	return nil
}

// UpdateContainerResources updates a container's resources.
func (d *Driver) UpdateContainerResources(ctx context.Context, c *framework.Container, r *framework.Resources) error {
	err := d.client.Runtime.UpdateContainerResources(ctx, c.ID, ContainerResources(r))
	if err != nil {
		return fmt.Errorf("failed to update resources of container %s: %w", c.ID, err)
	}

	return nil
}

// StopContainer stops a running container.
func (d *Driver) StopContainer(ctx context.Context, c *framework.Container) error {
	if err := d.client.Runtime.StopContainer(ctx, c.ID, stopTimeout); err != nil {
		return fmt.Errorf("failed to stop container %s: %w", c.ID, err)
	}

	return nil
}

// RemoveContainer removes a stopped container.
func (d *Driver) RemoveContainer(ctx context.Context, c *framework.Container) error {
	if err := d.client.Runtime.RemoveContainer(ctx, c.ID); err != nil {
		return fmt.Errorf("failed to remove container %s: %w", c.ID, err)
	}

	return nil
}

// scopeFilter decides whether a listed pod, described by its namespace, labels
// and annotations, is one this listing wants. The only two implementations are
// framework.Scope's own predicates: Matches for what we may look at and account
// for, MatchesForRemoval for what we may destroy.
type scopeFilter func(namespace string, labels, annotations map[string]string) bool

// listPods returns the listed pods the given predicate accepts, and records the
// pods of other runs it walked past on the way; see StalePods.
//
// The recording rides along here rather than in a listing of its own because every
// listing already walks every pod on the node, so the notice costs no extra call to
// the runtime whichever listing happens to run. Recording rather than acting is the
// whole point: these are the pods removal refuses to touch.
//
// CRI's PodSandboxFilter has no namespace field -- only Id, State and
// LabelSelector -- so the namespace guard has to be applied client-side. We
// deliberately do NOT filter by run-id label server-side either: if a runtime
// does not round-trip labels, a label selector would silently match nothing and
// the leak guard would report no leak.
//
// The decision itself is framework.Scope's, not ours, so the driver can never
// disagree with the plugins about what belongs to this run. The predicate is a
// parameter rather than a second copy of this loop because the two callers need
// different rules -- see listOurPods and listPodsToRemove -- and one listing loop
// keeps the CRI-shape knowledge in one place. Note that in the CRI type the
// namespace lives under Metadata while the labels are top-level.
func (d *Driver) listPods(ctx context.Context, matches scopeFilter) ([]*runtimeapi.PodSandbox, error) {
	all, err := d.client.Runtime.ListPodSandbox(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list pod sandboxes: %w", err)
	}

	scope := d.cfg.Scope()

	var (
		ours  []*runtimeapi.PodSandbox
		stale []framework.StalePod
	)

	for _, pod := range all {
		var (
			namespace   = pod.GetMetadata().GetNamespace()
			labels      = pod.GetLabels()
			annotations = pod.GetAnnotations()
		)

		if scope.MatchesOtherRun(namespace, labels, annotations) {
			stale = append(stale, staleOf(pod))
		}

		if !matches(namespace, labels, annotations) {
			continue
		}

		ours = append(ours, pod)
	}

	// Replaced, not accumulated: the newest listing is the truth about the node, and
	// appending would grow a duplicate entry per listing for one stale pod.
	d.mu.Lock()
	d.stale = stale
	d.mu.Unlock()

	return ours, nil
}

// staleOf describes a listed pod of another run for the notice. The run id is read
// exactly as framework.Scope reads it, labels before annotations, so the notice
// cannot name a different id than the predicate matched on.
func staleOf(pod *runtimeapi.PodSandbox) framework.StalePod {
	runID := pod.GetLabels()[framework.RunIDLabel]
	if runID == "" {
		runID = pod.GetAnnotations()[framework.RunIDLabel]
	}

	return framework.StalePod{
		ID:    pod.GetId(),
		Name:  pod.GetMetadata().GetName(),
		RunID: runID,
	}
}

// StalePods returns the pods of other nri-test runs that this driver's most recent
// listing saw in our namespace. Reporting only: Cleanup deliberately leaves them
// alone, and nothing here removes or fails on them.
//
// It answers from the last listing rather than making a call of its own, so asking
// costs nothing and a caller that has never listed gets nothing.
func (d *Driver) StalePods() []framework.StalePod {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.stale)
}

// listOurPods returns the pods belonging to this run, by the same rule the
// plugins filter their callbacks with: within our namespace a pod carrying no run
// id at all is ours, so we do not go blind if a runtime drops labels. This is the
// observation predicate. Nothing destructive may use it; see listPodsToRemove.
func (d *Driver) listOurPods(ctx context.Context) ([]*runtimeapi.PodSandbox, error) {
	return d.listPods(ctx, d.cfg.Scope().Matches)
}

// listPodsToRemove returns the listed pods Cleanup may stop and remove: our
// namespace and our own run id, nobody else's. See
// framework.Scope.MatchesForRemoval for why destruction needs the narrower rule,
// and what the suite gives up by not reaping pods labelled with another run's id.
func (d *Driver) listPodsToRemove(ctx context.Context) ([]*runtimeapi.PodSandbox, error) {
	return d.listPods(ctx, d.cfg.Scope().MatchesForRemoval)
}

// podIDs returns the union of the pod ids this driver created and the ids of the
// listed pods returned by list, de-duplicated.
//
// Neither source alone is enough. Listing finds pods a crashed earlier case left
// behind, which the driver no longer tracks. The driver's own record covers the
// opposite failure: if a runtime under-reports Metadata.Namespace in
// ListPodSandbox, no scope predicate can recognise our own pods and only d.pods
// still knows their ids. The tracked arm is therefore unconditional -- those pods
// are ours by construction, whatever the listing says about them.
//
// A listing error is returned alongside the ids it could still gather, so the
// caller can clean up what it knows about and report the failure.
func (d *Driver) podIDs(ctx context.Context, list func(context.Context) ([]*runtimeapi.PodSandbox, error)) ([]string, error) {
	d.mu.Lock()
	ids := make([]string, 0, len(d.pods))
	for _, pod := range d.pods {
		if !slices.Contains(ids, pod.ID) {
			ids = append(ids, pod.ID)
		}
	}
	d.mu.Unlock()

	pods, err := list(ctx)
	for _, pod := range pods {
		if !slices.Contains(ids, pod.GetId()) {
			ids = append(ids, pod.GetId())
		}
	}

	return ids, err
}

// cleanupIDs returns the pod ids Cleanup may remove: the tracked pods, plus the
// listed pods that pass the removal predicate.
func (d *Driver) cleanupIDs(ctx context.Context) ([]string, error) {
	return d.podIDs(ctx, d.listPodsToRemove)
}

// ourPodIDs returns the ids of the pods that belong to this run: the tracked
// pods, plus the listed pods in scope. Used by the container leak guard, which
// reports rather than destroys, so it takes the accepting predicate.
func (d *Driver) ourPodIDs(ctx context.Context) ([]string, error) {
	return d.podIDs(ctx, d.listOurPods)
}

// Cleanup removes every pod this run created, whether the driver still tracks it
// or not, so a pod left behind by a crashed earlier case is removed too.
//
// What it will not touch is a pod in our namespace that does not carry our run
// id: neither an unlabelled one, which somebody else created however the leak
// guard chooses to report it, nor one labelled with another run's id, which may be
// a live peer's. See framework.Scope.MatchesForRemoval for both.
//
// The listing arm is therefore narrow by design, and the tracked arm does most of
// the work. That is not redundancy: the listing still catches a pod this run
// created in an earlier phase and lost track of, which is also what makes a
// re-used -nri-test.run-id sweep clean.
//
// Errors are accumulated rather than aborting: a pod we fail to remove must not
// stop us removing the rest.
func (d *Driver) Cleanup(ctx context.Context) error {
	var errs []error

	ids, err := d.cleanupIDs(ctx)
	if err != nil {
		errs = append(errs, err)
	}

	for _, id := range ids {
		if err := d.client.Runtime.StopPodSandbox(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop pod %s: %w", id, err))
		}

		// Attempt the removal even if the stop failed. Both containerd and CRI-O
		// force-stop a sandbox from inside RemovePodSandbox, so removal can
		// succeed where the explicit stop did not -- and removal is what actually
		// prevents the leak.
		if err := d.client.Runtime.RemovePodSandbox(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove pod %s: %w", id, err))
		}
	}

	d.mu.Lock()
	d.pods = nil
	d.mu.Unlock()

	return errors.Join(errs...)
}

// ListRunPods returns the pods this run created that still exist. Used by the
// leak guard.
func (d *Driver) ListRunPods(ctx context.Context) ([]string, error) {
	pods, err := d.listOurPods(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(pods))
	for _, pod := range pods {
		ids = append(ids, pod.GetId())
	}

	return ids, nil
}

// containerRunID returns the run id a listed container carries, in the order
// Scope.Matches reads it: labels first, then annotations.
func containerRunID(c *runtimeapi.Container) string {
	if id := c.GetLabels()[framework.RunIDLabel]; id != "" {
		return id
	}

	return c.GetAnnotations()[framework.RunIDLabel]
}

// listOurContainers returns the containers belonging to this run.
//
// A CRI Container carries PodSandboxId, Labels and Annotations, but no namespace
// of its own, so Scope.Matches cannot be applied to it directly. A container is
// ours if EITHER of the following holds -- a union, in this order:
//
//  1. its PodSandboxId is a pod we already know is ours, which is exactly the
//     tracked-plus-listed set ourPodIDs builds. That inherits both of
//     ourPodIDs' sources, so a container survives a runtime under-reporting
//     Metadata.Namespace as long as the driver still tracks its pod -- RemovePod
//     forgets a pod once RemovePodSandbox has returned success, so "we created
//     it" is not enough.
//  2. its own labels or annotations carry this run's id. This catches a container
//     whose pod the listing somehow missed.
//
// Resolving the namespace as d.cfg.Namespace instead -- assuming every listed
// container sits in our namespace -- would be wrong in the fatal direction's
// mirror image: an unlabelled foreign container would look like our leak, so on
// any busy node every run would fail its own leak guard, and a guard that always
// fires is a guard nobody reads.
//
// The asymmetry is deliberate. For a leak guard, missing a leak is fatal and
// over-reporting is merely noisy, so the union errs toward reporting. Note that
// arm 2 is a strict run-id comparison rather than Scope.Matches: Matches accepts
// an *absent* run id, which is safe when the namespace has already been checked
// and fails open when, as here, it has not.
//
// As with pods, we deliberately do not filter server-side. ContainerFilter does
// have a LabelSelector, unlike PodSandboxFilter, but a runtime that does not
// round-trip labels would make the selector match nothing and the guard would
// report no leak.
func (d *Driver) listOurContainers(ctx context.Context) ([]*runtimeapi.Container, error) {
	// A pod listing failure leaves arm 1 incomplete, so say so rather than report a
	// container set we know is short. Note this is ourPodIDs, not cleanupIDs: a
	// leak guard reports, so it inherits Scope.Matches and keeps counting an
	// unlabelled pod in our namespace as ours.
	ourPods, err := d.ourPodIDs(ctx)
	if err != nil {
		return nil, err
	}

	all, err := d.client.Runtime.ListContainers(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// Taken from the same Scope the plugins filter with, so the two can never
	// disagree about which run a workload belongs to.
	runID := d.cfg.Scope().RunID

	var ours []*runtimeapi.Container
	for _, ctr := range all {
		inOurPod := slices.Contains(ourPods, ctr.GetPodSandboxId())
		// Fail closed on an empty run id: with no run id configured, arm 2 would
		// otherwise claim every unlabelled container on the node.
		carriesOurID := runID != "" && containerRunID(ctr) == runID

		if !inOurPod && !carriesOurID {
			continue
		}

		ours = append(ours, ctr)
	}

	return ours, nil
}

// ListRunContainers returns the containers this run created that still exist.
// Used by the leak guard. See listOurContainers for what counts as ours.
func (d *Driver) ListRunContainers(ctx context.Context) ([]string, error) {
	ctrs, err := d.listOurContainers(ctx)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(ctrs))
	for _, ctr := range ctrs {
		ids = append(ids, ctr.GetId())
	}

	return ids, nil
}

// RuntimeVersion returns the runtime's name and version.
func (d *Driver) RuntimeVersion(ctx context.Context) (string, string, error) {
	rpl, err := d.client.Runtime.Version(ctx, "")
	if err != nil {
		return "", "", fmt.Errorf("failed to get runtime version: %w", err)
	}

	return rpl.GetRuntimeName(), rpl.GetRuntimeVersion(), nil
}

// PullImage pulls an image, so that container creation does not race a pull.
func (d *Driver) PullImage(ctx context.Context, image string) error {
	if image == "" {
		image = d.cfg.Image
	}

	_, err := d.client.Image.PullImage(ctx, &runtimeapi.ImageSpec{Image: image}, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", image, err)
	}

	return nil
}

// Close releases the driver's resources.
func (d *Driver) Close(ctx context.Context) error {
	return d.client.Close(ctx)
}
