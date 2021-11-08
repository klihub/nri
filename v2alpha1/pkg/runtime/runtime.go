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
	"context"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/pkg/errors"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/log"
)

const (
	// DefaultConfigPath is the default path to the NRI configuration.
	DefaultConfigPath = "/etc/nri/nri.conf"
	// DefaultPluginPath is the default path to search for NRI plugins.
	DefaultPluginPath = "/opt/nri/plugins"
	// DefaultSocketPath is the default socket path for external plugins.
	DefaultSocketPath = api.DefaultSocketPath
)

// SyncFn is a container runtime function for state synchronization.
type SyncFn func(context.Context, SyncCB) error

// SyncCB is an NRI function used to synchronize plugins with the runtime.
type SyncCB func(context.Context, []*PodSandbox, []*Container) ([]*ContainerUpdate, error)

// UpdateFn is a container runtime function for unsolicited container updates.
type UpdateFn func(context.Context, []*ContainerUpdate) ([]*ContainerUpdate, error)

// Runtime is the NRI abstraction for container runtimes.
type Runtime struct {
	sync.Mutex
	name       string
	version    string
	configPath string
	pluginPath string
	socketPath string
	syncFn     SyncFn
	updateFn   UpdateFn
	cfg        *Config
	listener   net.Listener
	plugins    []*plugin
}

var (
	// Used instead of nil Context in logging.
	noCtx = context.TODO()
)

// Option to apply to the NRI runtime.
type Option func(*Runtime) error

// WithConfigPath returns an option to override the default NRI config path.
func WithConfigPath(path string) Option {
	return func(r *Runtime) error {
		r.configPath = path
		return nil
	}
}

// WithConfig returns an option to provide a pre-parsed NRI configuration.
func WithConfig(cfg *Config) Option {
	return func(r *Runtime) error {
		r.cfg = cfg
		r.configPath = cfg.path
		return nil
	}
}

// WithPluginPath returns an option to override the default NRI plugin path.
func WithPluginPath(path string) Option {
	return func(r *Runtime) error {
		r.pluginPath = path
		return nil
	}
}

// WithSocketPath returns an option to override the default NRI socket path.
func WithSocketPath(path string) Option {
	return func(r *Runtime) error {
		r.socketPath = path
		return nil
	}
}

// New creates a new NRI Runtime.
func New(name, version string, syncFn SyncFn, updateFn UpdateFn, opts ...Option) (*Runtime, error) {
	var err error

	r := &Runtime{
		name:       name,
		version:    version,
		syncFn:     syncFn,
		updateFn:   updateFn,
		configPath: DefaultConfigPath,
		pluginPath: DefaultPluginPath,
		socketPath: DefaultSocketPath,
	}

	for _, o := range opts {
		if err = o(r); err != nil {
			return nil, errors.Wrap(err, "failed to apply option")
		}
	}

	if r.cfg == nil {
		if r.cfg, err = ReadConfig(r.configPath); err != nil {
			return nil, err
		}
	}

	log.Infof(noCtx, "runtime interface created")

	return r, nil
}

// Start up the NRI runtime.
func (r *Runtime) Start() error {
	log.Infof(noCtx, "runtime interface starting up...")

	r.Lock()
	defer r.Unlock()

	if err := r.startPlugins(); err != nil {
		return err
	}

	if err := r.startListener(); err != nil {
		return err
	}

	return nil
}

// Stop the NRI runtime.
func (r *Runtime) Stop() {
	log.Infof(noCtx, "runtime interface shutting down...")

	r.Lock()
	defer r.Unlock()

	r.stopListener()
	r.stopPlugins()
}

// RunPodSandbox relays the corresponding CRI event to plugins.
func (r *Runtime) RunPodSandbox(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_RUN_POD_SANDBOX
	return r.StateChange(ctx, evt)
}

// StopPodSandbox relays the corresponding CRI event to plugins.
func (r *Runtime) StopPodSandbox(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_STOP_POD_SANDBOX
	return r.StateChange(ctx, evt)
}

// RemovePodSandbox relays the corresponding CRI event to plugins.
func (r *Runtime) RemovePodSandbox(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_REMOVE_POD_SANDBOX
	return r.StateChange(ctx, evt)
}

// CreateContainer relays the corresponding CRI request to plugins.
func (r *Runtime) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*CreateContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := collectCreateContainerResult(req)
	for _, plugin := range r.plugins {
		rpl, err := plugin.createContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return nil, err
		}
	}

	return result.createContainerResponse(), nil
}

// PostCreateContainer relays the corresponding CRI event to plugins.
func (r *Runtime) PostCreateContainer(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_POST_CREATE_CONTAINER
	return r.StateChange(ctx, evt)
}

// StartContainer relays the corresponding CRI event to plugins.
func (r *Runtime) StartContainer(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_START_CONTAINER
	return r.StateChange(ctx, evt)
}

// PostStartContainer relays the corresponding CRI event to plugins.
func (r *Runtime) PostStartContainer(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_POST_START_CONTAINER
	return r.StateChange(ctx, evt)
}

// UpdateContainer relays the corresponding CRI request to plugins.
func (r *Runtime) UpdateContainer(ctx context.Context, req *UpdateContainerRequest) (*UpdateContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := collectUpdateContainerResult(req)
	for _, plugin := range r.plugins {
		rpl, err := plugin.updateContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return nil, err
		}
	}

	return result.updateContainerResponse(), nil
}

// PostUpdateContainer relays the corresponding CRI event to plugins.
func (r *Runtime) PostUpdateContainer(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_POST_UPDATE_CONTAINER
	return r.StateChange(ctx, evt)
}

// StopContainer relays the corresponding CRI request to plugins.
func (r *Runtime) StopContainer(ctx context.Context, req *StopContainerRequest) (*StopContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := collectStopContainerResult()
	for _, plugin := range r.plugins {
		rpl, err := plugin.stopContainer(ctx, req)
		if err != nil {
			return nil, err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return nil, err
		}
	}

	return result.stopContainerResponse(), nil
}

// RemoveContainer relays the corresponding CRI event to plugins.
func (r *Runtime) RemoveContainer(ctx context.Context, evt *StateChangeEvent) error {
	evt.Event = Event_REMOVE_CONTAINER
	return r.StateChange(ctx, evt)
}

// StateChange relays pod- or container events to plugins.
func (r *Runtime) StateChange(ctx context.Context, evt *StateChangeEvent) error {
	if evt.Event == Event_UNKNOWN {
		return errors.New("invalid (unset) event in state change notification")
	}

	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.StateChange(ctx, evt)
		if err != nil {
			return err
		}
	}

	return nil
}

// Perform a set of unsolicited container updates requested by a plugin.
func (r *Runtime) updateContainers(ctx context.Context, req []*ContainerUpdate) ([]*ContainerUpdate, error) {
	r.Lock()
	defer r.Unlock()

	return r.updateFn(ctx, req)
}

// Start up pre-installed plugins.
func (r *Runtime) startPlugins() (retErr error) {
	var plugins []*plugin

	log.Infof(noCtx, "starting plugins...")

	ids, names, configs, err := r.discoverPlugins()
	if err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			for _, p := range plugins {
				p.stop()
			}
		}
	}()

	for i, name := range names {
		log.Infof(noCtx, "starting plugin %q...", name)

		id := ids[i]

		p, err := newLaunchedPlugin(r.pluginPath, id, name, configs[i])
		if err != nil {
			return errors.Wrapf(err, "failed to start NRI plugin %q", name)
		}

		if err := p.start(r.name, r.version); err != nil {
			return err
		}

		plugins = append(plugins, p)
	}

	r.plugins = plugins
	return nil
}

// Stop plugins.
func (r *Runtime) stopPlugins() {
	log.Infof(noCtx, "stopping plugins...")

	for _, p := range r.plugins {
		p.stop()
	}
	r.plugins = nil
}

func (r *Runtime) removeClosedPlugins() {
	active := []*plugin{}
	for _, p := range r.plugins {
		if !p.closed {
			active = append(active, p)
		}
	}
	r.plugins = active
}

func (r *Runtime) startListener() error {
	if r.cfg.DisablePluginConnections {
		log.Infof(noCtx, "connection from external plugins disabled")
		return nil
	}

	os.Remove(r.socketPath)
	if err := os.MkdirAll(filepath.Dir(r.socketPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create socket %q", r.socketPath)
	}

	l, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: r.socketPath,
		Net:  "unix",
	})
	if err != nil {
		return errors.Wrapf(err, "failed to create socket %q", r.socketPath)
	}

	r.acceptPluginConnections(l)

	return nil
}

func (r *Runtime) stopListener() {
	if r.listener != nil {
		r.listener.Close()
	}
}

func (r *Runtime) acceptPluginConnections(l net.Listener) error {
	r.listener = l

	ctx := context.Background()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				log.Infof(ctx, "stopped accepting plugin connections (%v)", err)
				return
			}

			p, err := newExternalPlugin(conn)
			if err != nil {
				log.Errorf(ctx, "failed to create external plugin: %v", err)
				continue
			}

			if err := p.start(r.name, r.version); err != nil {
				log.Errorf(ctx, "failed to start external plugin: %v", err)
				continue
			}

			r.Lock()

			err = r.syncFn(ctx, p.synchronize)
			if err != nil {
				log.Infof(ctx, "failed to synchronize plugin: %v", err)
			} else {
				r.plugins = append(r.plugins, p)
				r.sortPlugins()
			}

			r.Unlock()

			log.Infof(ctx, "plugin %q connected", p.name())
		}
	}()

	return nil
}

func (r *Runtime) discoverPlugins() ([]string, []string, []string, error) {
	var (
		plugins []string
		indices []string
		configs []string
		entries []os.FileInfo
		err     error
	)

	if entries, err = ioutil.ReadDir(r.pluginPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, errors.Wrapf(err,
			"failed to discover plugins in %q", r.pluginPath)
	}

	for _, e := range entries {
		if e.IsDir() || e.Mode()&os.FileMode(0111) == 0 {
			continue
		}

		name := e.Name()
		idx, base, err := api.ParsePlugin(name)
		if err != nil {
			return nil, nil, nil, errors.Wrapf(err,
				"failed to discover plugins in %q", r.pluginPath)
		}

		if !r.cfg.isPluginEnabled(idx, base) {
			log.Infof(noCtx, "skipping disabled plugin %q", name)
			continue
		}

		cfg, err := r.cfg.getPluginConfig(idx, base)
		if err != nil {
			return nil, nil, nil, errors.Wrapf(err,
				"failed to discover plugins in %q", r.pluginPath)
		}

		log.Infof(noCtx, "discovered plugin %q", name)

		indices = append(indices, idx)
		plugins = append(plugins, base)
		configs = append(configs, cfg)
	}

	return indices, plugins, configs, nil
}

func (r *Runtime) sortPlugins() {
	r.removeClosedPlugins()
	sort.Slice(r.plugins, func(i, j int) bool {
		return r.plugins[i].idx < r.plugins[j].idx
	})
	if len(r.plugins) > 0 {
		log.Infof(noCtx, "plugin invocation order")
		for i, p := range r.plugins {
			log.Infof(noCtx, "  #%d: %q (%s)", i+1, p.name(), p.qualifiedName())
		}
	}
}
