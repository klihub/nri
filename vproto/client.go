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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/containerd/containerd/log"
	types "github.com/containerd/nri/types/vproto"
	"github.com/pkg/errors"
)

const (
	// DefaultBinaryPath is the default path to search plugins for.
	DefaultBinaryPath = "/opt/nri/bin"
	// DefaultConfigPath is the default to the NRI configuration file.
	DefaultConfigPath = "/etc/nri/nri.conf"
)

// Option to apply to a client.
type Option func(*Client) error

// Client is used to interact with NRI plugins.
type Client struct {
	lock    sync.RWMutex
	cfgPath string
	binPath string
	config  types.Config
}

// WithBinaryPath returns an option to override the default binary path.
func WithBinaryPath(path string) func (*Client) error {
	return func (c *Client) error {
		c.binPath = path
		return nil
	}
}

// WithConfigPath returns an option to override the default config path.
func WithConfigPath(path string) func (*Client) error {
	return func (c *Client) error {
		c.cfgPath = path
		return nil
	}
}

// New creates a new NRI client.
func New(options ...Option) (*Client, error) {
	c := &Client{
		binPath: DefaultBinaryPath,
		cfgPath: DefaultConfigPath,
	}
	for _, o := range options {
		if err := c.apply(o); err != nil {
			return nil, err
		}
	}

	if err := c.config.Reload(c.cfgPath); err != nil {
		return nil, err
	}

	for _, plugin := range c.config.Plugins {
		plugin.Resolve(c.binPath)
		log.L.Infof("plugin %q", plugin.Type)
		log.L.Infof("  - path:   %q", plugin.Path())
		log.L.Infof("  - config: %q", plugin.Config)
	}

	return c, nil
}

// apply an option to the client.
func (c *Client) apply(o Option) error {
	return o(c)
}

// HasPlugins returns true if NRI is configured with some plugins.
func (c *Client) HasPlugins() bool {
	return len(c.config.Plugins) > 0
}

// CreateSandbox runs all plugins with a CreateSandbox request.
func (c *Client) CreateSandbox(ctx context.Context, r *types.CreateSandboxRequest) (*types.CreateSandboxResponse, error) {
	response := &types.CreateSandboxResponse{}

	log.L.Infof("nri:CreateSandbox()...")

	if !c.HasPlugins() {
		return response, nil
	}

	action := types.CreateSandbox
	request := &types.AnyRequest{
		CreateSandbox: r,
	}
	for _, plugin := range c.config.Plugins {
		log.L.Infof("- plugin %s...", plugin.Type)
		path := plugin.Path()
		log.L.Infof("  => path %s", plugin.Type, path)
		if path == "" {
			continue
		}

		request.Config = &types.ConfigureRequest{
			Version: types.Version,
			RawConfig: plugin.Config,
		}
		rpl, err := c.execPlugin(ctx, path, string(action), request)
		if err != nil {
			log.L.Infof("  <= failed with %v", err)
			return nil, errors.Wrapf(err, "plugins %s", plugin.Type)
		}
		log.L.Infof("  <= OK")

		// XXX TODO: collect/combine responses from all plugins...
		response = rpl.CreateSandbox
	}

	return response, nil
}

// RemoveSandbox runs all plugins with a RemoveSandbox request.
func (c *Client) RemoveSandbox(ctx context.Context, r *types.RemoveSandboxRequest) (*types.RemoveSandboxResponse, error) {
	response := &types.RemoveSandboxResponse{}

	if !c.HasPlugins() {
		return response, nil
	}

	action := types.RemoveSandbox
	request := &types.AnyRequest{
		RemoveSandbox: r,
	}
	for _, plugin := range c.config.Plugins {
		path := plugin.Path()
		if path == "" {
			continue
		}

		request.Config = &types.ConfigureRequest{
			Version: types.Version,
			RawConfig: plugin.Config,
		}
		rpl, err := c.execPlugin(ctx, path, string(action), request)
		if err != nil {
			return nil, errors.Wrapf(err, "plugins %s", plugin.Type)
		}

		// XXX TODO: collect/combine responses from all plugins...
		response = rpl.RemoveSandbox
	}

	return response, nil
}

// execute a single plugin with a request to get a response.
func (c *Client) execPlugin(ctx context.Context, path, action string, request *types.AnyRequest) (*types.AnyResponse, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, path, action)
	cmd.Stdin = bytes.NewBuffer(payload)
	cmd.Stderr = os.Stderr

	reply, err := cmd.Output()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// plugin ran, but exited with an error
			return nil, fmt.Errorf("plugin %s exited with error %d",
				path, exitErr.ExitCode())
		} else {
			// plugin failed to exec
			return nil, errors.Wrapf(err, "failed to exec plugin %s", path)
		}
	}

	response := &types.AnyResponse{}
	if err = json.Unmarshal(reply, response); err != nil {
		return nil, errors.Errorf("failed to unmarshal plugin response: %s",
			err.Error())
	}
	if response.Error != "" {
		return nil, errors.New(response.Error)
	}

	return response, nil
}
