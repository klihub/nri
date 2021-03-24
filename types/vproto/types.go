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
	"os"

	//cri "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
	//oci "github.com/containerd/containerd/oci"
)

const (
	// Version is this version of NRI.
	Version = "0.2"
)

// Plugin represents a configured NRI plugin.
type Plugin struct {
	// Type of the plugin, used also to derive its path.
	Type string `json:"type"`
	// Configuration data specific to this plugin.
	Config string `json:"conf,omitempty"`
	// resolved plugin path.
	path string
}

// Config is the NRI runtime configuration.
type Config struct {
	// Version is the expected NRI version.
	Version string
	// Plugins is the list of configured NRI plugins.
	Plugins []*Plugin
	// Stat()'ed info of source file.
	info *os.FileInfo
}

// Sandbox contains sandbox data of potential interest for a plugin.
type Sandbox struct {
	// ID of the sandbox.
	ID string `json:"id"`
	// Name of the sandbox.
	Name string `json:"id"`
	// Namespace of the sandbox.
	Namespace string `json:"namespace,"omitempty"`
	// Labels for the sandbox.
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations for the sandbox.
	Annotations map[string]string `json:"annotations,omitempty"`
	// Namespaces of the sandbox.
	Namespaces map[string]string `json:"namespaces,omitempty"`
	// CgroupParent of the sandbox.
	CgroupParent string `json:"cgroupParent,omitempty"`
	// RuntimeHandler is the sandbox runtime handler.
	RuntimeHandler string `json:"runtimeHandler,omitempty"`
	// CgroupsPath from the OCI spec of the sandbox.
	CgroupsPath string `json:"cgroupsPath,omitempty"`

}

// Action describes why a call to a plugin has been made.
type Action string

const (
	// CreateSandbox corresponds to sandbox creation (CRI RunPodSandbox()).
	CreateSandbox Action = "createsandbox"
	// RemoveSandbox corresponds to stopping/removing a sandbox.
	RemoveSandbox Action = "removesandbox"
)

// ConfigureRequest is used to pass configuration data to the plugin.
type ConfigureRequest struct {
	// Version is the expected/NRI version.
	Version string `json:"version"`
	// RawConfig is yaml or json configuration data for the plugin.
	RawConfig string `json:"rawConfig,omitempty"`
}

// ConfigureResponse is a response to a configuration request.
type ConfigureResponse struct {
}

// CreateSandboxRequest is a sandbox creation request.
type CreateSandboxRequest struct {
	Sandbox *Sandbox `json:"sandbox,omitempty"`
}

// CreateSandboxResponse is a response to a sandbox creation request.
type CreateSandboxResponse struct {
}

// RemoveSandboxRequest is a sandbox removal request.
type RemoveSandboxRequest struct {
	Sandbox *Sandbox `json:"sandbox,omitempty"`
}

// RemoveSandboxResponse is a sandbox removal response.
type RemoveSandboxResponse struct {
}

// AnyRequest is used to pass requests to connectionless plugins.
type AnyRequest struct {
	Config        *ConfigureRequest `json:"config"`
	CreateSandbox *CreateSandboxRequest `json:"createSandbox"`
	RemoveSandbox *RemoveSandboxRequest `json:"removeSandbox"`
}

// AnyResponse is used to get responses from connectionless plugins.
type AnyResponse struct {
	Config        *ConfigureResponse `json:"config"`
	CreateSandbox *CreateSandboxResponse `json:"createSandbox"`
	RemoveSandbox *RemoveSandboxResponse `json:"removeSandbox"`
	Error         string `json:"error,omitempty"`
}


