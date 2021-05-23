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

// Option to apply to a client.
type Option func(*Client) error

// WithConfigPath returns an option to override the default NRI config path.
func WithConfigPath(path string) Option {
	return func(c *Client) error {
		c.configPath = path
		return nil
	}
}

// WithPluginPath returns an option to override the default NRI plugin path.
func WithPluginPath(path string) Option {
	return func(c *Client) error {
		c.pluginPath = path
		return nil
	}
}

// WithSocketPath returns an option to override the default NRI socket path.
func WithSocketPath(path string) Option {
	return func(c *Client) error {
		c.socketPath = path
		return nil
	}
}

// Apply an option to the client.
func (c *Client) apply(o Option) error {
	return o(c)
}
