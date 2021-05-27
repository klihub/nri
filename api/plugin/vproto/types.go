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

package vproto

import (
	protobuf "github.com/golang/protobuf/ptypes/empty"
	cri "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

type (
	// KeyValue aliases the same CRI API type.
	KeyValue = cri.KeyValue
	// MountPropagation aliases the same CRI API type.
	MountPropagation = cri.MountPropagation
	// Mount aliases the same CRI API type.
	Mount = cri.Mount
	// Device aliases the same CRI API type.
	Device = cri.Device
	// LinuxContainerResources are an alias for the same CRI API type.
	LinuxContainerResources = cri.LinuxContainerResources
	// HugepageLimit aliases the same CRI API type.
	HugepageLimit = cri.HugepageLimit

	// RunPodSandboxResponse is empty. Plugins can't respond with changes to the request.
	RunPodSandboxResponse = protobuf.Empty
	// StopPodSandboxResponse is empty. Plugins can't respond with changes to the request.
	StopPodSandboxResponse = protobuf.Empty
	// RemovePodSandboxResponse is empty. Plugins can't respond with changes to the request.
	RemovePodSandboxResponse = protobuf.Empty
	// StartContainerResponse is empty. Plugins can't respond with changes to the request.
	StartContainerResponse = protobuf.Empty
	// RemoveContainerResponse is empty. Plugins can't respond with changes to the request.
	RemoveContainerResponse = protobuf.Empty
	// ShutdownRequest is empty.
	ShutdownRequest = protobuf.Empty
	// ShutdownResponse is empty.
	ShutdownResponse = protobuf.Empty
)

// nolint
const (
	// MountPropagation_PROPAGATION_PRIVATE aliases the same CRI API constant.
	MountPropagation_PROPAGATION_PRIVATE = cri.MountPropagation_PROPAGATION_PRIVATE
	// MountPropagation_PROPAGATION_HOST_TO_CONTAINER aliases the same CRI API constant.
	MountPropagation_PROPAGATION_HOST_TO_CONTAINER = cri.MountPropagation_PROPAGATION_HOST_TO_CONTAINER
	// MountPropagation_PROPAGATION_BIDIRECTIONAL aliases the same CRI API constant.
	MountPropagation_PROPAGATION_BIDIRECTIONAL = cri.MountPropagation_PROPAGATION_BIDIRECTIONAL
)
