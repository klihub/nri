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

// PodSpec describes a pod to create, independently of how it gets created.
type PodSpec struct {
	// Name, Namespace and UID identify the pod.
	Name      string
	Namespace string
	UID       string
	// Labels and Annotations are set on the pod.
	Labels      map[string]string
	Annotations map[string]string
	// CgroupParent is the pod's cgroup parent, when the driver needs one.
	CgroupParent string
	// RuntimeHandler selects the low-level runtime the pod and its containers are
	// created with -- runc, crun, a VM runtime -- or the runtime's default one
	// when empty. Empty is not a handler named "": it is the absence of a
	// selection, and every driver has to pass it through as such.
	//
	// Mode-neutral on purpose, as the rest of this spec is. The CRI driver maps it
	// onto RunPodSandboxRequest.RuntimeHandler; a Kubernetes driver will map it
	// onto pod.spec.runtimeClassName, which is a name for the same choice one
	// indirection further out.
	//
	// It exists so that a failure can be located rather than merely observed: the
	// chain under test is plugin -> NRI -> runtime -> OCI spec -> low-level runtime
	// -> kernel, and a feature that fails under one low-level runtime and passes
	// under another is a defect in that runtime, not in NRI.
	RuntimeHandler string
	// Containers are created together with the pod by drivers that lack
	// CapContainerLifecycle. Drivers that have it may create them lazily.
	Containers []*ContainerSpec
}

// ContainerSpec describes a container to create.
type ContainerSpec struct {
	// Name identifies the container within its pod.
	Name string
	// Image is the container image. Empty means Config.Image.
	Image string
	// Command and Args are the container's entry point and arguments.
	Command []string
	Args    []string
	// Env are environment variables to set.
	Env map[string]string
	// Mounts are mounts to add.
	Mounts []*Mount
	// Resources are the container's initial resource limits.
	Resources *Resources
	// Labels and Annotations are set on the container.
	Labels      map[string]string
	Annotations map[string]string
}

// Mount is a mount to add to a container.
type Mount struct {
	Source      string
	Destination string
	Readonly    bool
}

// Resources are a container's or pod's Linux resource limits. Only the fields
// the suite actually sets or updates are present.
type Resources struct {
	CPUShares  int64
	CPUQuota   int64
	CPUPeriod  int64
	CpusetCpus string
	CpusetMems string

	MemoryLimit int64
	OOMScoreAdj int64

	Unified map[string]string
}

// Pod is a created pod.
type Pod struct {
	// ID is the pod sandbox id the runtime assigned.
	ID string
	// Spec is what the pod was created from.
	Spec *PodSpec
}

// Container is a created container.
type Container struct {
	// ID is the container id the runtime assigned.
	ID string
	// PodID is the id of the container's pod.
	PodID string
	// Spec is what the container was created from.
	Spec *ContainerSpec
}

// ContainerState is a container's lifecycle state.
type ContainerState string

const (
	StateUnknown ContainerState = "unknown"
	StateCreated ContainerState = "created"
	StateRunning ContainerState = "running"
	StateExited  ContainerState = "exited"
)
