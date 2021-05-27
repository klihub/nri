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
	"context"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/pkg/errors"

	api "github.com/containerd/nri/api/plugin/vproto"
)

const (
	// DefaultPluginPath is the default path to search for plugins.
	DefaultPluginPath = "/opt/nri/bin"
	// DefaultConfigPath is the default path to read configuration from.
	DefaultConfigPath = "/etc/nri/nri.conf"
	// DefaultSocketPath is the default path for the NRI socket.
	DefaultSocketPath = "/var/run/nri.sock"
)

// SyncFn is a runtime function used by NRI for state synchronization.
type SyncFn func(context.Context, SyncCB) error

// SyncCB is an NRI function used to synchronize plugins with the runtime.
type SyncCB func(context.Context, []*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error)

// Client is used by the runtime to interact with NRI.
type Client struct {
	lock       sync.Mutex   // serialize requests and plugin registration
	syncFn     SyncFn       // runtime state synchronization function
	configPath string       // NRI configuration file
	config     *Config      // NRI configuration
	pluginPath string       // path to search for 'static' plugins
	socketPath string       // socket path
	listener   net.Listener //   and listener for 'dynamic' plugin connections
	plugins    []*plugin    // registered plugins, sorted by invocation order
}

// NewClient creates an NRI runtime client with the given options.
func NewClient(syncFn SyncFn, options ...Option) (*Client, error) {
	client := &Client{
		syncFn:     syncFn,
		configPath: DefaultConfigPath,
		pluginPath: DefaultPluginPath,
		socketPath: DefaultSocketPath,
		config:     &Config{},
	}

	for _, o := range options {
		if err := client.apply(o); err != nil {
			return nil, errors.Wrap(err, "NRI: failed to apply client option")
		}
	}

	if err := client.config.read(client.configPath); err != nil {
		return nil, err
	}

	return client, nil
}

// Start the NRI client.
func (c *Client) Start() error {
	log.Info("NRI: starting up...")

	c.lock.Lock()
	defer c.lock.Unlock()

	if err := c.startPlugins(); err != nil {
		return err
	}
	if err := c.startListener(); err != nil {
		return err
	}

	return nil
}

// Stop the NRI client.
func (c *Client) Stop() {
	log.Info("NRI: shutting down...")

	c.lock.Lock()
	defer c.lock.Unlock()

	c.stopListener()
	c.stopPlugins()
}

// Starts all static plugins (sans the disabled ones).
func (c *Client) startPlugins() error {
	staticPlugins, err := ioutil.ReadDir(c.pluginPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to read plugins from `%s`", c.pluginPath)
	}

	for _, p := range staticPlugins {
		if p.IsDir() {
			continue
		}

		name := p.Name()
		if !c.config.isPluginEnabled(name) {
			log.Infof("NRI: skipping disabled plugin `%s`", name)
			continue
		}

		conf, err := c.config.getPluginConfig(name)
		if err != nil {
			return errors.Wrapf(err,
				"failed to get confguration for plugin `%s`", name)
		}

		plugin, err := startStaticPlugin(c.pluginPath, name, conf)
		if err != nil {
			return errors.Wrapf(err, "failed to start plugin `%s`", name)
		}

		log.Infof("NRI: started static plugin `%s`...", plugin.id)

		c.plugins = append(c.plugins, plugin)
	}

	return nil
}

// Shut down all plugins.
func (c *Client) stopPlugins() {
	ctx := context.Background()
	for _, plugin := range c.plugins {
		log.Infof("NRI: shutting down plugin `%s`...", plugin.name)
		if err := plugin.shutdown(ctx); err != nil {
			log.Errorf("NRI: failed to stop plugin: %v", err)
		}
	}
	c.plugins = nil
}

// Start listening for new plugin connections.
func (c *Client) startListener() error {
	if c.config.DisableDynamicPlugins {
		log.Info("NRI: dynamically connected plugins disabled")
		return nil
	}

	os.Remove(c.socketPath)
	if err := os.MkdirAll(filepath.Dir(c.socketPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory for socket %s",
			c.socketPath)
	}

	network := "unix"
	l, err := net.ListenUnix(network, &net.UnixAddr{
		Name: c.socketPath,
		Net:  network,
	})
	if err != nil {
		return errors.Wrapf(err, "failed to create socket %s", c.socketPath)
	}

	c.listener = l
	go c.acceptConnections()

	return nil
}

// Handle connections from dynamic plugins.
func (c *Client) acceptConnections() {
	ctx := context.Background()
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}

		plugin, err := newDynamicPlugin(conn)
		if err != nil {
			log.Errorf("NRI: failed to create dynamic plugin: %v", err)
			continue
		}

		err = c.syncDynamicPlugin(ctx, plugin)
		if err != nil {
			log.Errorf("NRI: failed to synchronize dynamic plugin: %v", err)
			plugin.shutdown(ctx)
			plugin.close()
		}

		log.Infof("NRI: registered dynmic plugin `%s`", plugin.id)
	}
}

// Sycnhronize a dynamic plugin and add it among plugins on success.
func (c *Client) syncDynamicPlugin(ctx context.Context, plugin *plugin) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	err := c.syncFn(ctx, plugin.synchronize)
	if err != nil {
		return err
	}

	c.plugins = append(c.plugins, plugin)
	sort.Slice(c.plugins, func(i, j int) bool {
		iName := c.plugins[i].name
		jName := c.plugins[j].name
		switch {
		case iName != "" && jName != "":
			return iName < jName
		case iName != "" && jName == "":
			return true
		case iName == "" && jName != "":
			return false
		}
		return c.plugins[i].id < c.plugins[j].id
	})

	return nil
}

// stopListener stops listening for new plugin connections.
func (c *Client) stopListener() {
	if c.listener != nil {
		c.listener.Close()
	}
}

// removeClosedPlugins removes closed plugins.
func (c *Client) removeClosedPlugins() {
	var plugins []*plugin

	for _, p := range c.plugins {
		if p.isClosed() {
			log.Infof("NRI: plugin `%s` closed connection, removing it", p.id)
			if p.name != "" {
				log.Warn("NRI: XXX TODO plugin should be restarted")
			}
			p.stop()
			continue
		}
		plugins = append(plugins, p)
	}

	c.plugins = plugins
}

// RunPodSandbox request relays the corresponding CRI request to plgins.
func (c *Client) RunPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	for _, plugin := range c.plugins {
		err := plugin.runPodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StopPodSandbox request relays the corresponding CRI request to plugins.
func (c *Client) StopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	for _, plugin := range c.plugins {
		err := plugin.stopPodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// RemovePodSandbox request relays the corresponding CRI request to plugins.
func (c *Client) RemovePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	for _, plugin := range c.plugins {
		err := plugin.removePodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// CreateContainer relays the corresponding CRI request to plugins.
func (c *Client) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	result := newCreateResponseCumulator(req)
	for _, plugin := range c.plugins {
		rpl, err := plugin.createContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.applyCreateResponse(plugin.id, rpl)
		if err != nil {
			return nil, err
		}
	}

	return result.CreateContainerResponse(), nil
}

// StartContainer relays the corresponding CRI request to plugins.
func (c *Client) StartContainer(ctx context.Context, req *api.StartContainerRequest) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	for _, plugin := range c.plugins {
		err := plugin.startContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// UpdateContainer relays the corresponding CRI request to plugins.
func (c *Client) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	result := newResponseCumulator()
	for _, plugin := range c.plugins {
		rpl, err := plugin.updateContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.applyUpdateResponse(plugin.id, rpl)
		if err != nil {
			return nil, err
		}
	}

	return result.UpdateContainerResponse(), nil
}

// StopContainer relays the corresponding CRI request to plugins.
func (c *Client) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	result := newResponseCumulator()
	for _, plugin := range c.plugins {
		rpl, err := plugin.stopContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.applyStopResponse(plugin.id, rpl)
		if err != nil {
			return nil, err
		}
	}

	return result.StopContainerResponse(), nil
}

// RemoveContainer relays the corresponding CRI request to plugins.
func (c *Client) RemoveContainer(ctx context.Context, req *api.RemoveContainerRequest) error {
	c.lock.Lock()
	defer c.lock.Unlock()
	defer c.removeClosedPlugins()

	for _, plugin := range c.plugins {
		err := plugin.removeContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}
