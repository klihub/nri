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

package runtime

import (
	"github.com/containerd/nri/v2alpha1/pkg/api"
)

type (
	RegisterPluginRequest    = api.RegisterPluginRequest
	RegisterPluginResponse   = api.Empty
	AdjustContainersRequest  = api.AdjustContainersRequest
	AdjustContainersResponse = api.AdjustContainersResponse

	ConfigureRequest         = api.ConfigureRequest
	ConfigureResponse        = api.ConfigureResponse
	SynchronizeRequest       = api.SynchronizeRequest
	SynchronizeResponse      = api.SynchronizeResponse
	RunPodSandboxRequest     = api.RunPodSandboxRequest
	StopPodSandboxRequest    = api.StopPodSandboxRequest
	RemovePodSandboxRequest  = api.RemovePodSandboxRequest
	CreateContainerRequest   = api.CreateContainerRequest
	CreateContainerResponse  = api.CreateContainerResponse
	StartContainerRequest    = api.StartContainerRequest
	StartContainerResponse   = api.StartContainerResponse
	UpdateContainerRequest   = api.UpdateContainerRequest
	UpdateContainerResponse  = api.UpdateContainerResponse
	StopContainerRequest     = api.StopContainerRequest
	StopContainerResponse    = api.StopContainerResponse
	RemoveContainerRequest   = api.RemoveContainerRequest
	RemoveContainerResponse  = api.RemoveContainerResponse

	PostCreateContainerRequest  = api.PostCreateContainerRequest
	PostCreateContainerResponse = api.PostCreateContainerResponse
	PostStartContainerRequest   = api.PostStartContainerRequest
	PostStartContainerResponse  = api.PostStartContainerResponse
	PostUpdateContainerRequest  = api.PostUpdateContainerRequest
	PostUpdateContainerResponse = api.PostUpdateContainerResponse

	PodSandbox                = api.PodSandbox
	Container                 = api.Container
	ContainerAdjustment       = api.ContainerAdjustment
	ContainerCreateAdjustment = api.ContainerCreateAdjustment

	ContainerState            = api.ContainerState
	KeyValue                  = api.KeyValue
	Mount                     = api.Mount
	MountPropagation          = api.MountPropagation
	Device                    = api.Device
	LinuxContainerResources   = api.LinuxContainerResources
	HugepageLimit             = api.HugepageLimit
	Hook                      = api.Hook
	Hooks                     = api.Hooks
)
