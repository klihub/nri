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

package adapt

import (
	"github.com/containerd/nri/v2alpha1/pkg/api"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

//
// These are the interfaces the runtime needs to implement for its NRI adaptation.
//

// PodSandbox is the runtime's Pod NRI adaptation interface.
type PodSandbox interface {
	GetID() string
	GetName() string
	GetUID() string
	GetNamespace() string
	GetAnnotations() map[string]string
	GetLabels() map[string]string
	GetCgroupParent() string
	GetRuntimeHandler() string
}

// PodToNRI returns the NRI representation for the Pod.
func PodToNRI(p PodSandbox) *api.PodSandbox {
	return &api.PodSandbox{
		Id:             p.GetID(),
		Name:           p.GetName(),
		Uid:            p.GetUID(),
		Namespace:      p.GetNamespace(),
		Annotations:    p.GetAnnotations(),
		Labels:         p.GetLabels(),
		CgroupParent:   p.GetCgroupParent(),
		RuntimeHandler: p.GetRuntimeHandler(),
	}
}

// Container is the runtime's Container NRI adaptation interface.
type Container interface {
	GetSpec() *specs.Spec
	GetID() string
	GetPodSandboxID() string
	GetName() string
	GetState() api.ContainerState
	GetLabels() map[string]string
	GetAnnotations() map[string]string
	GetArgs() []string
	GetEnv() []string
	GetMounts() []*api.Mount
	GetHooks() *api.Hooks
	GetLinuxNamespaces() []*api.LinuxNamespace
	GetLinuxDevices() []*api.LinuxDevice
	GetLinuxResources() *api.LinuxResources
	GetOOMScoreAdj() *int
	GetCgroupsPath() string
}

// ContainerToNRI returns the NRI representation for the container.
func ContainerToNRI(c Container) *api.Container {
	return &api.Container{
		Id:           c.GetID(),
		PodSandboxId: c.GetPodSandboxID(),
		Name:         c.GetName(),
		State:        c.GetState(),
		Labels:       c.GetLabels(),
		Annotations:  c.GetAnnotations(),
		Args:         c.GetArgs(),
		Env:          c.GetEnv(),
		Mounts:       c.GetMounts(),
		Hooks:        c.GetHooks(),
		Linux: &api.LinuxContainer{
			Namespaces:  c.GetLinuxNamespaces(),
			Devices:     c.GetLinuxDevices(),
			Resources:   c.GetLinuxResources(),
			OomScoreAdj: api.Int(c.GetOOMScoreAdj()),
			CgroupsPath: c.GetCgroupsPath(),
		},
	}
}
