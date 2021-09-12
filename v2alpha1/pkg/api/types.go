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

package api

const (
	ContainerStateCreated = ContainerState_CONTAINER_CREATED
	ContainerStateRunning = ContainerState_CONTAINER_RUNNING
	ContainerStateExited  = ContainerState_CONTAINER_EXITED
)

type (
	RunPodSandboxRequest     = RunPodSandboxEvent
	RunPodSandboxResponse    = Empty
	StopPodSandboxRequest    = StopPodSandboxEvent
	StopPodSandboxResponse   = Empty
	RemovePodSandboxRequest  = RemovePodSandboxEvent
	RemovePodSandboxResponse = Empty

	StartContainerRequest   = StartContainerEvent
	StartContainerResponse  = Empty
	RemoveContainerRequest  = RemoveContainerEvent
	RemoveContainerResponse = Empty

	PostCreateContainerRequest  = PostCreateContainerEvent
	PostCreateContainerResponse = Empty
	PostStartContainerRequest   = PostStartContainerEvent
	PostStartContainerResponse  = Empty
	PostUpdateContainerRequest  = PostUpdateContainerEvent
	PostUpdateContainerResponse = Empty

	ShutdownRequest  = Empty
	ShutdownResponse = Empty
)

func (hooks *Hooks) Append(h *Hooks) *Hooks {
	if h == nil {
		return hooks
	}
	hooks.Prestart = append(hooks.Prestart, h.Prestart...)
	hooks.CreateRuntime = append(hooks.CreateRuntime, h.CreateRuntime...)
	hooks.CreateContainer = append(hooks.CreateContainer, h.CreateContainer...)
	hooks.StartContainer = append(hooks.StartContainer, h.StartContainer...)
	hooks.Poststart = append(hooks.Poststart, h.Poststart...)
	hooks.Poststop = append(hooks.Poststop, h.Poststop...)

	return hooks
}

func (h *Hooks) Hooks() *Hooks {
	if h == nil {
		return nil
	}

	if len(h.Prestart) > 0 {
		return h
	}
	if len(h.CreateRuntime) > 0 {
		return h
	}
	if len(h.CreateContainer) > 0 {
		return h
	}
	if len(h.StartContainer) > 0 {
		return h
	}
	if len(h.Poststart) > 0 {
		return h
	}
	if len(h.Poststop) > 0 {
		return h
	}

	return nil
}

func (m *Mount) Cmp(v *Mount) bool {
	if v == nil {
		return false
	}
	return m.ContainerPath != v.ContainerPath || m.HostPath != v.HostPath ||
		m.Readonly != v.Readonly ||	m.SelinuxRelabel != v.SelinuxRelabel ||
		m.Propagation != v.Propagation
}

func (d *Device) Cmp(v *Device) bool {
	if v == nil {
		return false
	}
	return d.ContainerPath != v.ContainerPath || d.HostPath != v.HostPath ||
		d.Permissions != v.Permissions
}
