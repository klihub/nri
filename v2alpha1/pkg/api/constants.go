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
	// DefaultSocketPath is the default socket path for external plugins.
	DefaultSocketPath = "/var/run/nri.sock"
	// PluginSocketEnvVar is used to inform plugins about pre-connected sockets.
	PluginSocketEnvVar = "NRI_PLUGIN_SOCKET"
	// PluginNameEnvVar is used to inform NRI-launched plugins about their name.
	PluginNameEnvVar = "NRI_PLUGIN_NAME"
	// PluginIDEnvVar is used to inform NRI-launched plugins about their ID.
	PluginIDEnvVar = "NRI_PLUGIN_ID"
)

