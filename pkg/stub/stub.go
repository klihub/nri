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

package stub

import (
	"github.com/containerd/nri/pkg/api"
)

// Plugin can implement a number of interfaces related to Pod and Container
// lifecycle events. No any single such inteface is mandatory, therefore the
// Plugin interface itself is empty. Plugins are required to implement at
// least one of these interfaces and this is verified during stub creation.
// Trying to create a stub for a plugin violating this requirement will fail
// with and error.
type Plugin interface{}

// ConfigureInterface handles Configure API request.
type ConfigureInterface interface {
	// Configure the plugin with the given NRI-supplied configuration.
	// If a non-zero EventMask is returned, the plugin will be subscribed
	// to the corresponding.
	Configure(config, runtime, version string) (api.EventMask, error)
}

// SynchronizeInterface handles Synchronize API requests.
type SynchronizeInterface interface {
	// Synchronize the state of the plugin with the runtime.
	// The plugin can request updates to containers in response.
	Synchronize([]*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error)
}

// ShutdownInterface handles a Shutdown API request.
type ShutdownInterface interface {
	// Shutdown notifies the plugin about the runtime shutting down.
	Shutdown(*api.ShutdownRequest)
}

// RunPodInterface handles RunPodSandbox API events.
type RunPodInterface interface {
	// RunPodSandbox relays a RunPodSandbox event to the plugin.
	RunPodSandbox(*api.PodSandbox) error
}

// StopPodInterface handles StopPodSandbox API events.
type StopPodInterface interface {
	// StopPodSandbox relays a StopPodSandbox event to the plugin.
	StopPodSandbox(*api.PodSandbox) error
}

// RemovePodInterface handles RemovePodSandbox API events.
type RemovePodInterface interface {
	// RemovePodSandbox relays a RemovePodSandbox event to the plugin.
	RemovePodSandbox(*api.PodSandbox) error
}

// CreateContainerInterface handles CreateContainer API requests.
type CreateContainerInterface interface {
	// CreateContainer relays a CreateContainer request to the plugin.
	// The plugin can request adjustments to the container being created
	// and updates to other unstopped containers in response.
	CreateContainer(*api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
}

// StartContainerInterface handles StartContainer API requests.
type StartContainerInterface interface {
	// StartContainer relays a StartContainer event to the plugin.
	StartContainer(*api.PodSandbox, *api.Container) error
}

// UpdateContainerInterface handles UpdateContainer API requests.
type UpdateContainerInterface interface {
	// UpdateContainer relays an UpdateContainer request to the plugin.
	// The plugin can request updates both to the container being updated
	// (which then supersedes the original update) and to other unstopped
	// containers in response.
	UpdateContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// StopContainerInterface handles StopContainer API requests.
type StopContainerInterface interface {
	// StopContainer relays a StopContainer request to the plugin.
	// The plugin can request updates to unstopped containers in response.
	StopContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// RemoveContainerInterface handles RemoveContainer API events.
type RemoveContainerInterface interface {
	// RemoveContainer relays a RemoveContainer event to the plugin.
	RemoveContainer(*api.PodSandbox, *api.Container) error
}

// PostCreateContainerInterface handles PostCreateContainer API events.
type PostCreateContainerInterface interface {
	// PostCreateContainer relays a PostCreateContainer event to the plugin.
	PostCreateContainer(*api.PodSandbox, *api.Container) error
}

// PostStartContainerInterface handles PostStartContainer API events.
type PostStartContainerInterface interface {
	// PostStartContainer relays a PostStartContainer event to the plugin.
	PostStartContainer(*api.PodSandbox, *api.Container) error
}

// PostUpdateContainerInterface handles PostUpdateContainer API events.
type PostUpdateContainerInterface interface {
	// PostUpdateContainer relays a PostUpdateContainer event to the plugin.
	PostUpdateContainer(*api.PodSandbox, *api.Container) error
}
