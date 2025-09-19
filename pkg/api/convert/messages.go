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

package convert

import (
	v1alpha1 "github.com/containerd/nri/pkg/api"
	v1beta1 "github.com/containerd/nri/pkg/api/v1beta1"
)

// RegisterPluginRequest converts the request between v1alpha1 and v1beta1.
func RegisterPluginRequest(v1a1 *v1alpha1.RegisterPluginRequest) *v1beta1.RegisterPluginRequest {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.RegisterPluginRequest{
		PluginName: v1a1.PluginName,
		PluginIdx:  v1a1.PluginIdx,
	}
}

// RegisterPluginResponse converts the reply between v1alpha1 and v1beta1.
func RegisterPluginResponse(v1b1 *v1beta1.RegisterPluginResponse) *v1alpha1.Empty {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.Empty{}
}

// UpdateContainersRequest converts the request between v1alpha1 and v1beta1.
func UpdateContainersRequest(v1a1 *v1alpha1.UpdateContainersRequest) *v1beta1.UpdateContainersRequest {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.UpdateContainersRequest{}
}

// UpdateContainersResponse converts the reply between v1alpha1 and v1beta1.
func UpdateContainersResponse(v1b1 *v1beta1.UpdateContainersResponse) *v1alpha1.UpdateContainersResponse {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.UpdateContainersResponse{}
}

// ConfigureRequest converts the request between v1alpha1 and v1beta1.
func ConfigureRequest(v1b1 *v1beta1.ConfigureRequest) *v1alpha1.ConfigureRequest {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.ConfigureRequest{
		Config:              v1b1.Config,
		RuntimeName:         v1b1.RuntimeName,
		RuntimeVersion:      v1b1.RuntimeVersion,
		RegistrationTimeout: v1b1.RegistrationTimeout,
		RequestTimeout:      v1b1.RequestTimeout,
	}
}

// ConfigureResponse converts the reply between v1alpha1 and v1beta1.
func ConfigureResponse(v1a1 *v1alpha1.ConfigureResponse) *v1beta1.ConfigureResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.ConfigureResponse{
		Events: v1a1.Events,
	}
}

// SynchronizeRequest converts the request between v1alpha1 and v1beta1.
func SynchronizeRequest(v1b1 *v1beta1.SynchronizeRequest) *v1alpha1.SynchronizeRequest {
	if v1b1 == nil {
		return nil
	}

	v1a1 := &v1alpha1.SynchronizeRequest{
		Pods:       make([]*v1alpha1.PodSandbox, 0, len(v1b1.Pods)),
		Containers: make([]*v1alpha1.Container, 0, len(v1b1.Containers)),
	}

	for _, pod := range v1b1.Pods {
		v1a1.Pods = append(v1a1.Pods, PodSandboxToV1alpha1(pod))
	}

	for _, ctr := range v1b1.Containers {
		v1a1.Containers = append(v1a1.Containers, ContainerToV1alpha1(ctr))
	}

	return v1a1
}

// SynchronizeResponse converts the reply between v1alpha1 and v1beta1.
func SynchronizeResponse(v1a1 *v1alpha1.SynchronizeResponse) *v1beta1.SynchronizeResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.SynchronizeResponse{
		Update: ContainerUpdatesToV1beta1(v1a1.Update),
		More:   v1a1.More,
	}
}

// RunPodSandboxRequest converts the request between v1alpha1 and v1beta1.
func RunPodSandboxRequest(v1b1 *v1beta1.RunPodSandboxRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	v1a1 := &v1alpha1.StateChangeEvent{
		Event: v1alpha1.Event_RUN_POD_SANDBOX,
		Pod:   PodSandboxToV1alpha1(v1b1.Pod),
	}

	return v1a1
}

// RunPodSandboxResponse converts the reply between v1alpha1 and v1beta1.
func RunPodSandboxResponse(v1a1 *v1alpha1.Empty) *v1beta1.RunPodSandboxResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.RunPodSandboxResponse{}
}

// UpdatePodSandboxRequest converts the request between v1alpha1 and v1beta1.
func UpdatePodSandboxRequest(v1b1 *v1beta1.UpdatePodSandboxRequest) *v1alpha1.UpdatePodSandboxRequest {
	if v1b1 == nil {
		return nil
	}

	v1a1 := &v1alpha1.UpdatePodSandboxRequest{
		Pod:                    PodSandboxToV1alpha1(v1b1.Pod),
		OverheadLinuxResources: LinuxResourcesToV1alpha1(v1b1.OverheadLinuxResources),
		LinuxResources:         LinuxResourcesToV1alpha1(v1b1.LinuxResources),
	}

	return v1a1
}

// UpdatePodSandboxResponse converts the reply between v1alpha1 and v1beta1.
func UpdatePodSandboxResponse(v1a1 *v1alpha1.UpdatePodSandboxResponse) *v1beta1.UpdatePodSandboxResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.UpdatePodSandboxResponse{}
}

// PostUpdatePodSandboxRequest converts the request between v1alpha1 and v1beta1.
func PostUpdatePodSandboxRequest(v1b1 *v1beta1.PostUpdatePodSandboxRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event: v1alpha1.Event_STOP_POD_SANDBOX,
		Pod:   PodSandboxToV1alpha1(v1b1.Pod),
	}
}

// PostUpdatePodSandboxResponse converts the reply between v1alpha1 and v1beta1.
func PostUpdatePodSandboxResponse(v1a1 *v1alpha1.Empty) *v1beta1.PostUpdatePodSandboxResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.PostUpdatePodSandboxResponse{}
}

// StopPodSandboxRequest converts the request between v1alpha1 and v1beta1.
func StopPodSandboxRequest(v1b1 *v1beta1.StopPodSandboxRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event: v1alpha1.Event_STOP_POD_SANDBOX,
		Pod:   PodSandboxToV1alpha1(v1b1.Pod),
	}
}

// StopPodSandboxResponse converts the reply between v1alpha1 and v1beta1.
func StopPodSandboxResponse(v1a1 *v1alpha1.Empty) *v1beta1.StopPodSandboxResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.StopPodSandboxResponse{}
}

// RemovePodSandboxRequest converts the request between v1alpha1 and v1beta1.
func RemovePodSandboxRequest(v1b1 *v1beta1.RemovePodSandboxRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event: v1alpha1.Event_REMOVE_POD_SANDBOX,
		Pod:   PodSandboxToV1alpha1(v1b1.Pod),
	}
}

// RemovePodSandboxResponse converts the reply between v1alpha1 and v1beta1.
func RemovePodSandboxResponse(v1a1 *v1alpha1.Empty) *v1beta1.RemovePodSandboxResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.RemovePodSandboxResponse{}
}

// CreateContainerRequest converts the request between v1alpha1 and v1beta1.
func CreateContainerRequest(v1b1 *v1beta1.CreateContainerRequest) *v1alpha1.CreateContainerRequest {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.CreateContainerRequest{
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// CreateContainerResponse converts the reply between v1alpha1 and v1beta1.
func CreateContainerResponse(v1a1 *v1alpha1.CreateContainerResponse) *v1beta1.CreateContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.CreateContainerResponse{
		Adjust: ContainerAdjustmentToV1beta1(v1a1.Adjust),
		Update: ContainerUpdatesToV1beta1(v1a1.Update),
		Evict:  ContainerEvictionsToV1beta1(v1a1.Evict),
	}
}

// PostCreateContainerRequest converts the request between v1alpha1 and v1beta1.
func PostCreateContainerRequest(v1b1 *v1beta1.PostCreateContainerRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event:     v1alpha1.Event_POST_CREATE_CONTAINER,
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// PostCreateContainerResponse converts the reply between v1alpha1 and v1beta1.
func PostCreateContainerResponse(v1a1 *v1alpha1.Empty) *v1beta1.PostCreateContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.PostCreateContainerResponse{}
}

// StartContainerRequest converts the request between v1alpha1 and v1beta1.
func StartContainerRequest(v1b1 *v1beta1.StartContainerRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event:     v1alpha1.Event_START_CONTAINER,
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// StartContainerResponse converts the reply between v1alpha1 and v1beta1.
func StartContainerResponse(v1a1 *v1alpha1.Empty) *v1beta1.StartContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.StartContainerResponse{}
}

// PostStartContainerRequest converts the request between v1alpha1 and v1beta1.
func PostStartContainerRequest(v1b1 *v1beta1.PostStartContainerRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event:     v1alpha1.Event_POST_START_CONTAINER,
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// PostStartContainerResponse converts the reply between v1alpha1 and v1beta1.
func PostStartContainerResponse(v1a1 *v1alpha1.Empty) *v1beta1.PostStartContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.PostStartContainerResponse{}
}

// UpdateContainerRequest converts the request between v1alpha1 and v1beta1.
func UpdateContainerRequest(v1b1 *v1beta1.UpdateContainerRequest) *v1alpha1.UpdateContainerRequest {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.UpdateContainerRequest{
		Pod:            PodSandboxToV1alpha1(v1b1.Pod),
		Container:      ContainerToV1alpha1(v1b1.Container),
		LinuxResources: LinuxResourcesToV1alpha1(v1b1.LinuxResources),
	}
}

// UpdateContainerResponse converts the reply between v1alpha1 and v1beta1.
func UpdateContainerResponse(v1a1 *v1alpha1.UpdateContainerResponse) *v1beta1.UpdateContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.UpdateContainerResponse{
		Update: ContainerUpdatesToV1beta1(v1a1.Update),
		Evict:  ContainerEvictionsToV1beta1(v1a1.Evict),
	}
}

// PostUpdateContainerRequest converts the request between v1alpha1 and v1beta1.
func PostUpdateContainerRequest(v1b1 *v1beta1.PostUpdateContainerRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event:     v1alpha1.Event_POST_START_CONTAINER,
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// PostUpdateContainerResponse converts the reply between v1alpha1 and v1beta1.
func PostUpdateContainerResponse(v1a1 *v1alpha1.Empty) *v1beta1.PostUpdateContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.PostUpdateContainerResponse{}
}

// StopContainerRequest converts the request between v1alpha1 and v1beta1.
func StopContainerRequest(v1b1 *v1beta1.StopContainerRequest) *v1alpha1.StopContainerRequest {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StopContainerRequest{
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// StopContainerResponse converts the reply between v1alpha1 and v1beta1.
func StopContainerResponse(v1a1 *v1alpha1.StopContainerResponse) *v1beta1.StopContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.StopContainerResponse{
		Update: ContainerUpdatesToV1beta1(v1a1.Update),
	}
}

// RemoveContainerRequest converts the request between v1alpha1 and v1beta1.
func RemoveContainerRequest(v1b1 *v1beta1.RemoveContainerRequest) *v1alpha1.StateChangeEvent {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.StateChangeEvent{
		Event:     v1alpha1.Event_REMOVE_CONTAINER,
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
	}
}

// RemoveContainerResponse converts the reply between v1alpha1 and v1beta1.
func RemoveContainerResponse(v1a1 *v1alpha1.Empty) *v1beta1.RemoveContainerResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.RemoveContainerResponse{}
}

// ValidateContainerAdjustmentRequest converts the request between v1alpha1 and v1beta1.
func ValidateContainerAdjustmentRequest(v1b1 *v1beta1.ValidateContainerAdjustmentRequest) *v1alpha1.ValidateContainerAdjustmentRequest {
	if v1b1 == nil {
		return nil
	}

	return &v1alpha1.ValidateContainerAdjustmentRequest{
		Pod:       PodSandboxToV1alpha1(v1b1.Pod),
		Container: ContainerToV1alpha1(v1b1.Container),
		Adjust:    ContainerAdjustmentToV1alpha1(v1b1.Adjust),
		Update:    ContainerUpdatesToV1alpha1(v1b1.Update),
		Owners:    OwningPluginsToV1alpha1(v1b1.Owners),
		Plugins:   PluginInstancesToV1alpha1(v1b1.Plugins),
	}
}

// ValidateContainerAdjustmentResponse converts the reply between v1alpha1 and v1beta1.
func ValidateContainerAdjustmentResponse(v1a1 *v1alpha1.ValidateContainerAdjustmentResponse) *v1beta1.ValidateContainerAdjustmentResponse {
	if v1a1 == nil {
		return nil
	}

	return &v1beta1.ValidateContainerAdjustmentResponse{
		Reject: v1a1.Reject,
		Reason: v1a1.Reason,
	}
}
