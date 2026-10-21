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
	"fmt"
	"maps"
	"slices"

	rspec "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/containerd/nri/pkg/adaptation"
	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// NewDriver returns a mock driver and the inspector that reads its model.
//
// The two are one object: with no runtime to ask, the model the driver
// mutates is the only place an observation can come from.
func NewDriver(_ context.Context, cfg *framework.Config) (framework.Driver, framework.Inspector, error) {
	r, err := NewRuntime(cfg)
	if err != nil {
		return nil, nil, err
	}

	return r, r, nil
}

// withStablePlugins runs fn with the plugin set frozen.
//
// A plugin becomes eligible for events only after Adaptation has appended it,
// which happens after its Synchronize call returns -- so a plugin that
// considers itself ready can still miss a hook dispatched in that window. A
// runtime closes it by holding this block for the length of an operation, and
// a runtime fast enough to create a pod inside the window has to.
func (r *Runtime) withStablePlugins(fn func() error) error {
	block := r.adaptation.BlockPluginSync()
	defer block.Unblock()

	return fn()
}

// Name identifies the driver in test output and skip messages.
func (r *Runtime) Name() string {
	return "mock"
}

// Capabilities returns what this driver can express.
//
// It withholds CapExec, because nothing is running to exec into;
// CapObserveOpError, because the errors it would observe are its own;
// CapObservedRuntimeState, because the pids, timestamps and states it would
// report are its own invention; CapCDIDevices, because it runs no CDI
// registry and so could only report a device it never injected; and
// CapUpdatePodResources, which no driver advertises.
func (r *Runtime) Capabilities() framework.Capability {
	return framework.CapContainerLifecycle |
		framework.CapUpdateContainerResources |
		framework.CapRawResources |
		framework.CapContainerMetadata |
		framework.CapDevices |
		framework.CapSysctls |
		framework.CapSecurityContext |
		framework.CapInTreeAdjustments
}

// RuntimeVersion returns what the summary names as the runtime under test.
// It says there is none, because a reader must not take a mock run for a
// runtime result.
func (r *Runtime) RuntimeVersion(context.Context) (string, string, error) {
	return mockRuntimeName, mockRuntimeVersion, nil
}

// CreatePod creates a pod and any containers its spec carries.
func (r *Runtime) CreatePod(ctx context.Context, spec *framework.PodSpec) (*framework.Pod, error) {
	p := &pod{
		id:           r.newID("pod"),
		name:         spec.Name,
		namespace:    spec.Namespace,
		uid:          spec.UID,
		labels:       spec.Labels,
		annotations:  spec.Annotations,
		sysctls:      spec.Sysctls,
		cgroupParent: spec.CgroupParent,
		overhead:     specResources(spec.PodOverhead),
		resources:    specResources(spec.PodResources),
	}
	p.spec = sandboxSpec(spec, p)

	r.mu.Lock()
	r.pods[p.id] = p
	r.mu.Unlock()

	out := &framework.Pod{ID: p.id, Spec: spec}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.RunPodSandbox(ctx, &adaptation.RunPodSandboxRequest{
			Pod: podMessage(p),
		})
	}); err != nil {
		return out, fmt.Errorf("mock RunPodSandbox for %s: %w", p.id, err)
	}

	// The containers are reported even when one of them fails, so what was
	// created can still be cleaned up and attributed.
	for _, cs := range spec.Containers {
		c, err := r.CreateContainer(ctx, out, cs)
		if c != nil {
			out.Containers = append(out.Containers, c)
		}

		if err != nil {
			return out, err
		}
	}

	return out, nil
}

// CreateContainer creates a container in a pod.
func (r *Runtime) CreateContainer(ctx context.Context, p *framework.Pod, spec *framework.ContainerSpec) (*framework.Container, error) {
	r.mu.Lock()
	parent, ok := r.pods[p.ID]
	r.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("mock CreateContainer: no such pod %s", p.ID)
	}

	c := &container{
		id:      r.newID("ctr"),
		podID:   parent.id,
		name:    spec.Name,
		state:   api.ContainerState_CONTAINER_CREATED,
		request: spec,
	}
	c.spec = containerSpec(spec, parent, c)

	r.mu.Lock()
	r.containers[c.id] = c
	parent.containers = append(parent.containers, c.id)
	r.mu.Unlock()

	out := &framework.Container{ID: c.id, PodID: parent.id, Spec: spec}

	var rpl *adaptation.CreateContainerResponse

	err := r.withStablePlugins(func() error {
		var err error
		rpl, err = r.adaptation.CreateContainer(ctx, &adaptation.CreateContainerRequest{
			Pod:       podMessage(parent),
			Container: containerMessage(c, parent),
		})

		return err
	})
	if err != nil {
		return out, fmt.Errorf("mock CreateContainer for %s: %w", c.id, err)
	}

	if err := r.applyAdjustment(c.id, rpl.GetAdjust()); err != nil {
		return out, err
	}

	if err := r.applyUpdates(rpl.GetUpdate()); err != nil {
		return out, err
	}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.PostCreateContainer(ctx, &adaptation.PostCreateContainerRequest{
			Pod:       podMessage(parent),
			Container: containerMessage(c, parent),
		})
	}); err != nil {
		return out, fmt.Errorf("mock PostCreateContainer for %s: %w", c.id, err)
	}

	return out, nil
}

// StartContainer starts a created container.
func (r *Runtime) StartContainer(ctx context.Context, ctr *framework.Container) error {
	c, p, err := r.lookup(ctr.ID)
	if err != nil {
		return err
	}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.StartContainer(ctx, &adaptation.StartContainerRequest{
			Pod:       podMessage(p),
			Container: containerMessage(c, p),
		})
	}); err != nil {
		return fmt.Errorf("mock StartContainer for %s: %w", c.id, err)
	}

	r.setState(c.id, api.ContainerState_CONTAINER_RUNNING)

	return r.post(ctx, c.id, func(c *container, p *pod) error {
		return r.withStablePlugins(func() error {
			return r.adaptation.PostStartContainer(ctx, &adaptation.PostStartContainerRequest{
				Pod:       podMessage(p),
				Container: containerMessage(c, p),
			})
		})
	})
}

// UpdateContainerResources updates a container's resources.
func (r *Runtime) UpdateContainerResources(ctx context.Context, ctr *framework.Container, res *framework.Resources) error {
	c, p, err := r.lookup(ctr.ID)
	if err != nil {
		return err
	}

	var rpl *adaptation.UpdateContainerResponse

	err = r.withStablePlugins(func() error {
		var err error
		rpl, err = r.adaptation.UpdateContainer(ctx, &adaptation.UpdateContainerRequest{
			Pod:            podMessage(p),
			Container:      containerMessage(c, p),
			LinuxResources: linuxResources(res),
		})

		return err
	})
	if err != nil {
		return fmt.Errorf("mock UpdateContainer for %s: %w", c.id, err)
	}

	// What the test asked for lands first, then whatever the plugins
	// returned, so a plugin's answer is what wins -- as it does in a runtime.
	if err := r.applyResources(c.id, linuxResources(res)); err != nil {
		return err
	}

	if err := r.applyUpdates(rpl.GetUpdate()); err != nil {
		return err
	}

	return r.post(ctx, c.id, func(c *container, p *pod) error {
		return r.withStablePlugins(func() error {
			return r.adaptation.PostUpdateContainer(ctx, &adaptation.PostUpdateContainerRequest{
				Pod:       podMessage(p),
				Container: containerMessage(c, p),
			})
		})
	})
}

// StopContainer stops a running container.
func (r *Runtime) StopContainer(ctx context.Context, ctr *framework.Container) error {
	c, p, err := r.lookup(ctr.ID)
	if err != nil {
		return err
	}

	var rpl *adaptation.StopContainerResponse

	err = r.withStablePlugins(func() error {
		var err error
		rpl, err = r.adaptation.StopContainer(ctx, &adaptation.StopContainerRequest{
			Pod:       podMessage(p),
			Container: containerMessage(c, p),
		})

		return err
	})
	if err != nil {
		return fmt.Errorf("mock StopContainer for %s: %w", c.id, err)
	}

	r.setState(c.id, api.ContainerState_CONTAINER_STOPPED)

	return r.applyUpdates(rpl.GetUpdate())
}

// RemoveContainer removes a stopped container.
func (r *Runtime) RemoveContainer(ctx context.Context, ctr *framework.Container) error {
	c, p, err := r.lookup(ctr.ID)
	if err != nil {
		return err
	}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.RemoveContainer(ctx, &adaptation.RemoveContainerRequest{
			Pod:       podMessage(p),
			Container: containerMessage(c, p),
		})
	}); err != nil {
		return fmt.Errorf("mock RemoveContainer for %s: %w", c.id, err)
	}

	r.mu.Lock()
	delete(r.containers, c.id)
	r.mu.Unlock()

	return nil
}

// UpdatePodResources updates a pod's resources. The mock does not advertise
// CapUpdatePodResources, so no test reaches this.
func (r *Runtime) UpdatePodResources(context.Context, *framework.Pod, *framework.Resources) error {
	return fmt.Errorf("the mock driver does not update pod resources")
}

// StopPod stops a pod.
func (r *Runtime) StopPod(ctx context.Context, p *framework.Pod) error {
	parent, err := r.lookupPod(p.ID)
	if err != nil {
		return err
	}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.StopPodSandbox(ctx, &adaptation.StopPodSandboxRequest{
			Pod: podMessage(parent),
		})
	}); err != nil {
		return fmt.Errorf("mock StopPodSandbox for %s: %w", parent.id, err)
	}

	return nil
}

// RemovePod removes a stopped pod and whatever containers it still holds.
func (r *Runtime) RemovePod(ctx context.Context, p *framework.Pod) error {
	parent, err := r.lookupPod(p.ID)
	if err != nil {
		return err
	}

	// A pod's removal takes its containers with it, as a runtime's does, so
	// the plugins see each one go.
	for _, id := range append([]string{}, parent.containers...) {
		r.mu.Lock()
		c, ok := r.containers[id]
		r.mu.Unlock()

		if !ok {
			continue
		}

		out := &framework.Container{ID: c.id, PodID: parent.id}

		// Stopped first, as a runtime does: a container going away with its
		// pod is still stopped before it is removed, and a plugin is told so.
		if c.state == api.ContainerState_CONTAINER_RUNNING {
			if err := r.StopContainer(ctx, out); err != nil {
				return err
			}
		}

		if err := r.RemoveContainer(ctx, out); err != nil {
			return err
		}
	}

	if err := r.withStablePlugins(func() error {
		return r.adaptation.RemovePodSandbox(ctx, &adaptation.RemovePodSandboxRequest{
			Pod: podMessage(parent),
		})
	}); err != nil {
		return fmt.Errorf("mock RemovePodSandbox for %s: %w", parent.id, err)
	}

	r.mu.Lock()
	delete(r.pods, parent.id)
	r.mu.Unlock()

	return nil
}

// ListRunPods returns the ids of this run's pods that still exist.
func (r *Runtime) ListRunPods(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.pods))
	for id := range r.pods {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids, nil
}

// ListRunContainers returns the ids of this run's containers that still
// exist.
func (r *Runtime) ListRunContainers(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ids := make([]string, 0, len(r.containers))
	for id := range r.containers {
		ids = append(ids, id)
	}

	slices.Sort(ids)

	return ids, nil
}

// Cleanup removes everything this driver created.
//
// It empties the model, which is what the leak guards read: a model left
// populated would report a leak on every run.
func (r *Runtime) Cleanup(ctx context.Context) error {
	pods, err := r.ListRunPods(ctx)
	if err != nil {
		return err
	}

	for _, id := range pods {
		if err := r.RemovePod(ctx, &framework.Pod{ID: id}); err != nil {
			return err
		}
	}

	return nil
}

// Inspect returns the container's realized state.
func (r *Runtime) Inspect(_ context.Context, id string) (*framework.Observed, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.containers[id]
	if !ok {
		return nil, fmt.Errorf("the mock runtime has no container %s", id)
	}

	return &framework.Observed{
		Spec:  c.spec,
		State: observedState(c.state),
	}, nil
}

// InspectPod returns the pod sandbox's realized state.
func (r *Runtime) InspectPod(_ context.Context, id string) (*framework.ObservedPod, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.pods[id]
	if !ok {
		return nil, fmt.Errorf("the mock runtime has no pod %s", id)
	}

	return &framework.ObservedPod{Spec: p.spec}, nil
}

// observedState maps NRI's container state onto the framework's.
func observedState(s api.ContainerState) framework.ContainerState {
	switch s {
	case api.ContainerState_CONTAINER_CREATED:
		return framework.StateCreated
	case api.ContainerState_CONTAINER_RUNNING:
		return framework.StateRunning
	case api.ContainerState_CONTAINER_STOPPED:
		return framework.StateExited
	}

	return framework.StateUnknown
}

// newID returns an id of this run's own, recognisable and stable within it.
func (r *Runtime) newID(kind string) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++

	return fmt.Sprintf("mock-%s-%s-%d", kind, r.runID, r.nextID)
}

// lookup returns a container and its pod, or an error naming the id.
func (r *Runtime) lookup(id string) (*container, *pod, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.containers[id]
	if !ok {
		return nil, nil, fmt.Errorf("the mock runtime has no container %s", id)
	}

	p, ok := r.pods[c.podID]
	if !ok {
		return nil, nil, fmt.Errorf("the mock runtime has no pod %s for container %s", c.podID, id)
	}

	return c, p, nil
}

// lookupPod returns a pod, or an error naming the id.
func (r *Runtime) lookupPod(id string) (*pod, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.pods[id]
	if !ok {
		return nil, fmt.Errorf("the mock runtime has no pod %s", id)
	}

	return p, nil
}

// setState records a container's new state.
func (r *Runtime) setState(id string, s api.ContainerState) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if c, ok := r.containers[id]; ok {
		c.state = s
	}
}

// post runs a post-event hook with the model as it stands now, so the
// message carries the state the event announces rather than the one before
// it.
func (r *Runtime) post(_ context.Context, id string, fn func(*container, *pod) error) error {
	c, p, err := r.lookup(id)
	if err != nil {
		return err
	}

	return fn(c, p)
}

// linuxResources converts the framework's resources into NRI's.
func linuxResources(res *framework.Resources) *api.LinuxResources {
	if res == nil {
		return nil
	}

	l := &api.LinuxResources{
		Cpu:    &api.LinuxCPU{},
		Memory: &api.LinuxMemory{},
	}

	if res.CPUShares != 0 {
		l.Cpu.Shares = api.UInt64(uint64(res.CPUShares))
	}
	if res.CPUQuota != 0 {
		l.Cpu.Quota = api.Int64(res.CPUQuota)
	}
	if res.CPUPeriod != 0 {
		l.Cpu.Period = api.UInt64(uint64(res.CPUPeriod))
	}
	if res.CpusetCpus != "" {
		l.Cpu.Cpus = res.CpusetCpus
	}
	if res.CpusetMems != "" {
		l.Cpu.Mems = res.CpusetMems
	}
	if res.MemoryLimit != 0 {
		l.Memory.Limit = api.Int64(res.MemoryLimit)
	}
	if len(res.Unified) > 0 {
		l.Unified = res.Unified
	}

	return l
}

// sandboxSpec is the OCI spec a runtime would have written for a sandbox
// before any plugin spoke.
//
// Not an empty spec: the agreement checks for the pod's cgroup, namespaces
// and resources read this, and against an empty one they would either pass
// vacuously or report a finding this runtime caused.
func sandboxSpec(spec *framework.PodSpec, p *pod) *rspec.Spec {
	s := baseSpec()
	s.Linux.CgroupsPath = fmt.Sprintf("%s:mock:%s", cgroupParent(spec), p.id)

	if spec.PodResources != nil {
		s.Linux.Resources = specResources(spec.PodResources)
	}

	return s
}

// containerSpec is the OCI spec a runtime would have written for a container
// before any plugin spoke.
func containerSpec(spec *framework.ContainerSpec, p *pod, c *container) *rspec.Spec {
	s := baseSpec()
	// From the id, as a runtime builds it: a test reads the container back out
	// of the path.
	s.Linux.CgroupsPath = fmt.Sprintf("%s:mock:%s", cgroupParent(nil), c.id)

	// Appended to a copy, never to the caller's slice: a spare capacity in
	// ContainerSpec.Command would otherwise be written through.
	if args := append(append([]string{}, spec.Command...), spec.Args...); len(args) > 0 {
		s.Process.Args = args
	}

	// A copy, for the reason the pod's sysctls are cloned below: an adjustment
	// rewrites the realized spec in place.
	if len(spec.Annotations) > 0 {
		s.Annotations = maps.Clone(spec.Annotations)
	}

	for k, v := range spec.Env {
		s.Process.Env = append(s.Process.Env, k+"="+v)
	}

	slices.Sort(s.Process.Env)

	for _, m := range spec.Mounts {
		s.Mounts = append(s.Mounts, rspec.Mount{
			Source:      m.Source,
			Destination: m.Destination,
			Type:        "bind",
			Options:     []string{"rbind"},
		})
	}

	for _, d := range spec.Devices {
		s.Linux.Devices = append(s.Linux.Devices, rspec.LinuxDevice{
			Path: d.ContainerPath,
			Type: "c",
		})
	}

	// A copy per container: the pod's map is shared, and an adjustment writes
	// the realized spec in place, so aliasing it would let one container's
	// adjustment rewrite the pod and every sibling.
	if len(p.sysctls) > 0 {
		s.Linux.Sysctl = maps.Clone(p.sysctls)
	}

	if spec.Resources != nil {
		s.Linux.Resources = specResources(spec.Resources)
	}

	applySecurityContext(s, spec)

	return s
}

// applySecurityContext writes a container's user, groups and seccomp profile
// into its realized spec.
//
// This driver advertises CapSecurityContext, which means "applied exactly as
// the test states them", so it has to apply them. Leaving the seccomp section
// out in particular left the suite's seccomp_policy check comparing two
// empties, which is a pass that asked nothing.
func applySecurityContext(s *rspec.Spec, spec *framework.ContainerSpec) {
	if spec.RunAsUser != nil {
		s.Process.User.UID = uint32(*spec.RunAsUser)
	}

	if spec.RunAsGroup != nil {
		s.Process.User.GID = uint32(*spec.RunAsGroup)
	}

	for _, g := range spec.SupplementalGroups {
		s.Process.User.AdditionalGids = append(s.Process.User.AdditionalGids, uint32(g))
	}

	if spec.SeccompProfile == nil {
		return
	}

	// Unconfined is the absence of a policy, which is why the suite's check
	// asks for the runtime default instead: a profile has to leave a section
	// behind for the comparison to have two sides.
	if spec.SeccompProfile.Type == framework.SeccompUnconfined {
		return
	}

	s.Linux.Seccomp = &rspec.LinuxSeccomp{
		DefaultAction: rspec.ActErrno,
		Architectures: []rspec.Arch{rspec.ArchX86_64},
		Syscalls: []rspec.LinuxSyscall{
			{Names: []string{"read", "write", "exit", "exit_group"}, Action: rspec.ActAllow},
		},
	}
}

// cgroupParent is the pod's cgroup parent, or the default a runtime uses.
func cgroupParent(spec *framework.PodSpec) string {
	if spec != nil && spec.CgroupParent != "" {
		return spec.CgroupParent
	}

	return "mock.slice"
}

// specResources converts the framework's resources into an OCI spec's.
func specResources(res *framework.Resources) *rspec.LinuxResources {
	if res == nil {
		return nil
	}

	out := &rspec.LinuxResources{CPU: &rspec.LinuxCPU{}, Memory: &rspec.LinuxMemory{}}

	if res.CPUShares != 0 {
		shares := uint64(res.CPUShares)
		out.CPU.Shares = &shares
	}
	if res.CPUQuota != 0 {
		quota := res.CPUQuota
		out.CPU.Quota = &quota
	}
	if res.CPUPeriod != 0 {
		period := uint64(res.CPUPeriod)
		out.CPU.Period = &period
	}
	if res.CpusetCpus != "" {
		out.CPU.Cpus = res.CpusetCpus
	}
	if res.CpusetMems != "" {
		out.CPU.Mems = res.CpusetMems
	}
	if res.MemoryLimit != 0 {
		limit := res.MemoryLimit
		out.Memory.Limit = &limit
	}

	return out
}

// baseSpec is the part every spec this runtime writes starts from: the
// sections a runtime fills in on its own, before any plugin or any test has
// said anything.
func baseSpec() *rspec.Spec {
	return &rspec.Spec{
		Version: rspec.Version,
		Process: &rspec.Process{
			Args: []string{"/pause"},
			Env:  []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Cwd:  "/",
			User: rspec.User{UID: 0, GID: 0},
			Rlimits: []rspec.POSIXRlimit{
				{Type: "RLIMIT_NOFILE", Hard: 1024, Soft: 1024},
			},
		},
		Root: &rspec.Root{Path: "rootfs"},
		Linux: &rspec.Linux{
			Namespaces: []rspec.LinuxNamespace{
				{Type: rspec.PIDNamespace},
				{Type: rspec.IPCNamespace},
				{Type: rspec.UTSNamespace},
				{Type: rspec.MountNamespace},
				{Type: rspec.NetworkNamespace},
			},
			Resources: &rspec.LinuxResources{},
		},
	}
}
