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

package framework

import (
	"context"
	"strings"
)

// Capability describes what a Driver is able to express.
type Capability uint

const (
	// CapContainerLifecycle means containers can be created in a pod that
	// already exists, and started, stopped and removed apart from it, rather
	// than only as part of a pod. No test needs this to do its job, so it is
	// not a gate for ordinary tests: they create pods with their containers.
	// It is for a test whose subject is the gap between those steps.
	CapContainerLifecycle Capability = 1 << iota
	// CapObserveOpError means an operation's error is surfaced verbatim to the
	// caller, so a test can assert that an operation failed.
	CapObserveOpError
	// CapUpdateContainerResources means a running container's resources can be
	// updated.
	CapUpdateContainerResources
	// CapUpdatePodResources means a pod's resources can be updated.
	CapUpdatePodResources
	// CapExec means a command can be run inside a running container and its
	// output collected, so a test can verify what the kernel actually applied
	// rather than only what the runtime recorded. See Executor.
	//
	// New bits go at the END of this list. The values come from 1 << iota and are
	// depended on; renumbering an existing one silently changes what a stored or
	// logged capability set means.
	CapExec
	// CapRawResources means a container's resources are applied exactly as
	// the test states them: a cpuset, CPU shares. CRI takes them verbatim. A
	// Kubernetes pod derives them from requests and limits, and cannot pin a
	// cpuset at all without a static CPU manager policy.
	CapRawResources
	// CapContainerMetadata means a container can carry labels and annotations
	// of its own. A Kubernetes container cannot: its metadata is its pod's.
	CapContainerMetadata
	// CapCDIDevices means a container can be created naming a CDI device. CRI
	// carries the names verbatim; a Kubernetes pod cannot name one at all,
	// since those arrive through a device plugin.
	CapCDIDevices
	// CapSysctls means a pod can be created with arbitrary sysctls. CRI takes
	// them verbatim. A Kubernetes pod can only set the ones kubelet considers
	// safe, and the rest need kubelet's --allowed-unsafe-sysctls, which is node
	// configuration this suite does not change.
	CapSysctls
	// CapSecurityContext means a container's user, groups and seccomp profile
	// are applied exactly as the test states them. A Kubernetes pod expresses
	// all three, but through admission and defaulting that can rewrite them.
	CapSecurityContext
)

var capabilityNames = []struct {
	cap  Capability
	name string
}{
	{CapContainerLifecycle, "ContainerLifecycle"},
	{CapObserveOpError, "ObserveOpError"},
	{CapUpdateContainerResources, "UpdateContainerResources"},
	{CapUpdatePodResources, "UpdatePodResources"},
	{CapExec, "Exec"},
	{CapRawResources, "RawResources"},
	{CapContainerMetadata, "ContainerMetadata"},
	{CapCDIDevices, "CDIDevices"},
	{CapSysctls, "Sysctls"},
	{CapSecurityContext, "SecurityContext"},
}

// Has reports whether all capabilities in o are present in c.
func (c Capability) Has(o Capability) bool {
	return c&o == o
}

func (c Capability) String() string {
	var names []string
	for _, e := range capabilityNames {
		if c.Has(e.cap) {
			names = append(names, e.name)
		}
	}

	if len(names) == 0 {
		return "none"
	}

	return strings.Join(names, ",")
}

// Driver creates and mutates test workloads. The CRI driver implements it over
// CRI; a future Kubernetes driver will implement it over client-go.
type Driver interface {
	// Name identifies the driver in test output and skip messages.
	Name() string
	// Capabilities returns what this driver can express.
	Capabilities() Capability

	// CreatePod creates a pod, and any containers p.Containers carries, which
	// the returned Pod lists even if creation failed part way. A driver with
	// CapContainerLifecycle may also be handed containers one at a time via
	// CreateContainer.
	CreatePod(ctx context.Context, p *PodSpec) (*Pod, error)
	// UpdatePodResources updates a pod's resources.
	UpdatePodResources(ctx context.Context, p *Pod, r *Resources) error
	// StopPod stops a pod.
	StopPod(ctx context.Context, p *Pod) error
	// RemovePod removes a stopped pod.
	RemovePod(ctx context.Context, p *Pod) error

	// CreateContainer creates a container in a pod.
	CreateContainer(ctx context.Context, p *Pod, c *ContainerSpec) (*Container, error)
	// StartContainer starts a created container. A driver whose containers
	// start on their own makes this wait until the container is running, so
	// that the same test works with either kind.
	StartContainer(ctx context.Context, c *Container) error
	// UpdateContainerResources updates a container's resources.
	UpdateContainerResources(ctx context.Context, c *Container, r *Resources) error
	// StopContainer stops a running container.
	StopContainer(ctx context.Context, c *Container) error
	// RemoveContainer removes a stopped container.
	RemoveContainer(ctx context.Context, c *Container) error

	// ListRunPods returns the ids of the pods this run created that still
	// exist. It is the leak guard: the suite calls it after Cleanup to notice
	// that cleanup missed something. It is part of this interface rather than
	// discovered by an optional type assertion, because a driver that did not
	// implement it would disable the one mechanism whose job is noticing a
	// missed cleanup, and would do so silently.
	ListRunPods(ctx context.Context) ([]string, error)

	// ListRunContainers returns the ids of the containers this run created that
	// still exist. Required, not optional, for the same reason ListRunPods is: a
	// leak guard a driver can silently opt out of is not a leak guard.
	//
	// It is a separate method rather than part of ListRunPods because a pod-level
	// guard cannot express "this rejected creation leaked no container": the
	// container's pod is one the case created on purpose, so the pod guard is
	// silent about it either way.
	ListRunContainers(ctx context.Context) ([]string, error)

	// Cleanup removes everything this driver created.
	Cleanup(ctx context.Context) error
	// Close releases the driver's resources.
	Close(ctx context.Context) error
}
