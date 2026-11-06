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

// Package mock runs the conformance suite against pkg/adaptation instead of
// against a container runtime. Nothing is created on the node: this package
// plays the runtime, so what a test measures is the NRI side of the protocol
// -- the stub, the proto round trip, the merge across plugins, and the
// adjustment-to-spec mapping -- and nothing below it.
//
// It is a control arm, not a third runtime. A clean run here says the NRI
// chain held; it says nothing about CRI-O or containerd.
package mock

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	rspec "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/containerd/nri/pkg/adaptation"
	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/test/nri-test/framework"
)

// pod is a sandbox this runtime pretends to have created.
type pod struct {
	id          string
	name        string
	namespace   string
	uid         string
	labels      map[string]string
	annotations map[string]string
	// sysctls are the pod's, which is where CRI carries them. NRI reports
	// them per container, so a container's spec is written with these.
	sysctls map[string]string
	// cgroupParent, overhead and resources are what CRI puts on the sandbox
	// config and NRI reports in LinuxPodSandbox.
	cgroupParent string
	overhead     *rspec.LinuxResources
	resources    *rspec.LinuxResources
	// spec is the sandbox's realized OCI spec, which is the only oracle for
	// what the runtime did with the pod itself.
	spec *rspec.Spec
	// containers are the ids of the containers in this pod, in creation
	// order.
	containers []string
}

// container is a container this runtime pretends to have created.
type container struct {
	id    string
	podID string
	name  string
	state api.ContainerState
	// spec is what the container was asked to be. A runtime keeps the request
	// it was given and reports from it; so does this.
	request *framework.ContainerSpec
	// spec is the container's realized OCI spec: the baseline this runtime
	// chose, with every applied adjustment and update on top.
	spec *rspec.Spec
}

// Runtime is the mock runtime: an Adaptation listening on a socket of its
// own, plus the pod and container model the suite's assertions are read from.
//
// It is the framework.Driver and the framework.Inspector both, because the
// model is the only place either can be answered from.
type Runtime struct {
	mu sync.Mutex

	adaptation *adaptation.Adaptation
	socketPath string
	// pluginPath is an empty directory of this runtime's own. Adaptation
	// launches whatever it finds there, and a plugin directory on the host
	// must not join a conformance run.
	pluginPath string
	runID      string

	pods       map[string]*pod
	containers map[string]*container
	nextID     int

	closed bool
}

// mockSocketPath returns the socket this runtime should listen on, refusing
// one that would take a node's own.
//
// Adaptation's listener unlinks whatever sits at the path before binding, so
// pointing a mock run at a live runtime's socket would steal it and remove it
// again on close, leaving that runtime unreachable to new plugins. Two guards:
// the node's default path is never used, since a driver that creates nothing
// has no business there; and a path something answers on is refused.
func mockSocketPath(cfg *framework.Config) (string, error) {
	socket := cfg.NRISocketPath
	if socket == "" || socket == api.DefaultSocketPath {
		dir, err := os.MkdirTemp("", "nri-test-mock-")
		if err != nil {
			return "", fmt.Errorf("failed to create a socket directory for the mock runtime: %w", err)
		}

		return filepath.Join(dir, "nri.sock"), nil
	}

	if c, err := net.DialTimeout("unix", socket, time.Second); err == nil {
		_ = c.Close()

		return "", fmt.Errorf("refusing to listen on %q for the mock runtime: something "+
			"is already listening there, and binding would unlink it", socket)
	}

	return socket, nil
}

// NewRuntime returns a mock runtime listening on a socket of its own, with
// Adaptation started.
func NewRuntime(cfg *framework.Config) (*Runtime, error) {
	socket, err := mockSocketPath(cfg)
	if err != nil {
		return nil, err
	}

	// Published back, because the suite's plugins connect to the config's
	// path: a socket this runtime moved and kept to itself is one no plugin
	// can reach.
	cfg.NRISocketPath = socket

	// Beside the socket, so one temporary directory holds the whole runtime
	// and a stale one is recognisable.
	pluginPath := filepath.Join(filepath.Dir(socket), "plugins")
	configPath := filepath.Join(filepath.Dir(socket), "conf.d")

	r := &Runtime{
		socketPath: socket,
		pluginPath: pluginPath,
		runID:      cfg.RunID,
		pods:       map[string]*pod{},
		containers: map[string]*container{},
	}

	// Every path here derives from the socket, so the socket is what a
	// failure names: it is the one an operator passed and the only one they
	// can fix.
	for _, dir := range []string{pluginPath, configPath} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("failed to prepare the mock runtime for socket %q: %w",
				socket, err)
		}
	}

	a, err := adaptation.New(mockRuntimeName, mockRuntimeVersion, r.synchronize, r.updateContainers,
		adaptation.WithSocketPath(socket),
		adaptation.WithPluginPath(pluginPath),
		adaptation.WithPluginConfigPath(configPath),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create the mock runtime's NRI adaptation: %w", err)
	}

	if err := a.Start(); err != nil {
		return nil, fmt.Errorf("failed to start the mock runtime on %q: %w", socket, err)
	}

	r.adaptation = a

	return r, nil
}

const (
	// mockRuntimeName is the runtime name this reports over NRI. Plugins are
	// told it, and the summary names it, and those two have to agree: a test
	// asserts that a plugin was configured for the runtime the run says it
	// measured.
	mockRuntimeName = "mock"
	// mockRuntimeVersion is the version it reports. It is not a runtime
	// version and must not read like one.
	mockRuntimeVersion = "0 (no runtime)"
)

// SocketPath returns the socket external plugins connect to.
func (r *Runtime) SocketPath() string {
	return r.socketPath
}

// Close stops the runtime. Calling it again does nothing: Cleanup and Close
// both reach it, and a run that failed part way reaches it twice.
func (r *Runtime) Close(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	r.closed = true

	if r.adaptation != nil {
		r.adaptation.Stop()
	}

	os.Remove(r.socketPath)

	return nil
}

// synchronize replays the current model to a plugin that has just
// registered, and applies whatever updates it asks for in return.
func (r *Runtime) synchronize(ctx context.Context, cb adaptation.SyncCB) error {
	pods, containers := r.snapshot()

	updates, err := cb(ctx, pods, containers)
	if err != nil {
		return err
	}

	return r.applyUpdates(updates)
}

// updateContainers applies the unsolicited updates a plugin sends.
func (r *Runtime) updateContainers(_ context.Context, updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	if err := r.applyUpdates(updates); err != nil {
		return nil, err
	}

	return nil, nil
}

// snapshot renders the model as the NRI messages a synchronization carries.
func (r *Runtime) snapshot() ([]*api.PodSandbox, []*api.Container) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var (
		pods       []*api.PodSandbox
		containers []*api.Container
	)

	for _, p := range r.pods {
		pods = append(pods, podMessage(p))

		for _, id := range p.containers {
			if c, ok := r.containers[id]; ok {
				containers = append(containers, containerMessage(c, p))
			}
		}
	}

	return pods, containers
}

// podMessage renders a pod as the NRI message a plugin is shown.
func podMessage(p *pod) *api.PodSandbox {
	return &api.PodSandbox{
		Id:          p.id,
		Name:        p.name,
		Uid:         p.uid,
		Namespace:   p.namespace,
		Labels:      p.labels,
		Annotations: p.annotations,
		Linux:       linuxPodMessage(p),
	}
}

// containerMessage renders a container as the NRI message a plugin is shown.
//
// Everything a runtime would report is here, drawn from the request it was
// given and from the spec it realized. A field left out would make the
// fidelity tier read as a runtime that does not deliver it, which is a
// finding this runtime has no business producing.
func containerMessage(c *container, p *pod) *api.Container {
	m := &api.Container{
		Id:           c.id,
		PodSandboxId: c.podID,
		Name:         c.name,
		State:        c.state,
		Labels:       map[string]string{},
		Annotations:  map[string]string{},
		Linux:        linuxContainerMessage(c.spec, c.request, p),
	}

	req := c.request
	if req == nil {
		return m
	}

	if req.Labels != nil {
		m.Labels = req.Labels
	}
	if req.Annotations != nil {
		m.Annotations = req.Annotations
	}

	m.Args = append(append([]string{}, req.Command...), req.Args...)

	for k, v := range req.Env {
		m.Env = append(m.Env, k+"="+v)
	}

	slices.Sort(m.Env)

	for _, mnt := range req.Mounts {
		m.Mounts = append(m.Mounts, &api.Mount{
			Source:      mnt.Source,
			Destination: mnt.Destination,
			Type:        "bind",
		})
	}

	if req.Image != "" {
		m.Image = &api.Image{Name: req.Image}
	}

	for _, d := range req.CDIDevices {
		m.CDIDevices = append(m.CDIDevices, &api.CDIDevice{Name: d})
	}

	m.User = &api.User{}
	if req.RunAsUser != nil {
		m.User.Uid = uint32(*req.RunAsUser)
	}
	if req.RunAsGroup != nil {
		m.User.Gid = uint32(*req.RunAsGroup)
	}
	for _, g := range req.SupplementalGroups {
		m.User.AdditionalGids = append(m.User.AdditionalGids, uint32(g))
	}

	if c.spec != nil && c.spec.Process != nil {
		for _, rl := range c.spec.Process.Rlimits {
			m.Rlimits = append(m.Rlimits, &api.POSIXRlimit{
				Type: rl.Type, Hard: rl.Hard, Soft: rl.Soft,
			})
		}
	}

	return m
}

// linuxPodMessage projects the sandbox's realized spec, and the request
// behind it, into NRI's view of the sandbox.
func linuxPodMessage(p *pod) *api.LinuxPodSandbox {
	spec := p.spec
	if spec == nil || spec.Linux == nil {
		return nil
	}

	l := &api.LinuxPodSandbox{
		CgroupsPath:  spec.Linux.CgroupsPath,
		CgroupParent: p.cgroupParent,
		Resources:    nriResources(spec.Linux.Resources),
		PodOverhead:  nriResources(p.overhead),
		PodResources: nriResources(p.resources),
	}

	for _, ns := range spec.Linux.Namespaces {
		l.Namespaces = append(l.Namespaces, &api.LinuxNamespace{
			Type: string(ns.Type),
			Path: ns.Path,
		})
	}

	return l
}

// linuxContainerMessage projects a container's realized spec, and the request
// behind it, into NRI's view of the container.
func linuxContainerMessage(spec *rspec.Spec, req *framework.ContainerSpec, p *pod) *api.LinuxContainer {
	if spec == nil || spec.Linux == nil {
		return nil
	}

	l := &api.LinuxContainer{
		CgroupsPath: spec.Linux.CgroupsPath,
		// From the realized spec and copied, as a runtime reports what the
		// container got rather than handing out its own map.
		Sysctl:    maps.Clone(spec.Linux.Sysctl),
		Resources: nriResources(spec.Linux.Resources),
	}

	for _, ns := range spec.Linux.Namespaces {
		l.Namespaces = append(l.Namespaces, &api.LinuxNamespace{
			Type: string(ns.Type),
			Path: ns.Path,
		})
	}

	for _, d := range spec.Linux.Devices {
		l.Devices = append(l.Devices, &api.LinuxDevice{
			Path: d.Path,
			Type: d.Type,
		})
	}

	if req == nil {
		return l
	}

	if req.OOMScoreAdj != nil {
		l.OomScoreAdj = api.Int(int(*req.OOMScoreAdj))
	} else if spec.Linux.Resources != nil && spec.Linux.Resources.Memory != nil {
		l.OomScoreAdj = api.Int(0)
	}

	if req.SeccompProfile != nil {
		l.SeccompProfile = seccompProfile(req.SeccompProfile)
	}

	l.SeccompPolicy = seccompPolicy(spec.Linux.Seccomp)

	return l
}

// seccompPolicy projects the realized spec's seccomp section into NRI's view.
// A runtime reports the policy the container got, and the suite compares the
// two: a report drawn from the request instead would compare the request
// against itself.
func seccompPolicy(sc *rspec.LinuxSeccomp) *api.LinuxSeccomp {
	if sc == nil {
		return nil
	}

	policy := &api.LinuxSeccomp{
		DefaultAction: string(sc.DefaultAction),
	}

	for _, arch := range sc.Architectures {
		policy.Architectures = append(policy.Architectures, string(arch))
	}

	for _, sc := range sc.Syscalls {
		policy.Syscalls = append(policy.Syscalls, &api.LinuxSyscall{
			Names:  sc.Names,
			Action: string(sc.Action),
		})
	}

	return policy
}

// nriResources projects an OCI spec's resources into NRI's.
func nriResources(res *rspec.LinuxResources) *api.LinuxResources {
	if res == nil {
		return nil
	}

	out := &api.LinuxResources{Cpu: &api.LinuxCPU{}, Memory: &api.LinuxMemory{}}

	if res.CPU != nil {
		out.Cpu.Cpus = res.CPU.Cpus
		out.Cpu.Mems = res.CPU.Mems

		if res.CPU.Shares != nil {
			out.Cpu.Shares = api.UInt64(*res.CPU.Shares)
		}
		if res.CPU.Quota != nil {
			out.Cpu.Quota = api.Int64(*res.CPU.Quota)
		}
		if res.CPU.Period != nil {
			out.Cpu.Period = api.UInt64(*res.CPU.Period)
		}
	}

	if res.Memory != nil && res.Memory.Limit != nil {
		out.Memory.Limit = api.Int64(*res.Memory.Limit)
	}

	if len(res.Unified) > 0 {
		out.Unified = res.Unified
	}

	return out
}

// seccompProfile projects the profile a container asked for into NRI's view.
//
// A runtime reports what it applied, and this runtime applies what it was
// asked for: the CRI-O finding that a profile can be applied and misreported
// is a runtime behaviour, not something to imitate here. The framework has no
// localhost profile, deliberately, so there are two cases.
func seccompProfile(p *framework.SeccompProfile) *api.SecurityProfile {
	switch p.Type {
	case framework.SeccompUnconfined:
		return &api.SecurityProfile{ProfileType: api.SecurityProfile_UNCONFINED}
	}

	return &api.SecurityProfile{ProfileType: api.SecurityProfile_RUNTIME_DEFAULT}
}
