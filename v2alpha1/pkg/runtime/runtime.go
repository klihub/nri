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
	"fmt"
	"io/fs"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/errors"

	pluginapi "github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/log"
	//"github.com/containerd/ttrpc"
)

const (
	// DefaultConfigPath is the default path to the NRI configuration.
	DefaultConfigPath = "/etc/nri/nri.conf"
	// DefaultPluginPath is the default path to search for NRI plugins.
	DefaultPluginPath = "/opt/nri/bin"
	// DefaultSocketPath is the default socket path for external plugins.
	DefaultSocketPath = pluginapi.DefaultSocketPath
)


// SyncFn is a container runtime function for state synchronization.
type SyncFn func(context.Context, SyncCB) error

// SyncCB is an NRI function used to synchronize plugins with the runtime.
type SyncCB func(context.Context, []*PodSandbox, []*Container) ([]*ContainerAdjustment, error)

// AdjustFn is a container runtime function for unsolicited container adjustment.
type AdjustFn func(context.Context, []*ContainerAdjustment) ([]*ContainerAdjustment, error)

// Runtime is the NRI abstraction for container runtimes.
type Runtime struct {
	lock       sync.Mutex
	configPath string
	pluginPath string
	socketPath string
	syncFn     SyncFn
	adjustFn   AdjustFn
	cfg        *Config
	listener   net.Listener
	plugins    []*plugin

	ownerLock  sync.Mutex
	ownerStack []byte
}

// Option to apply to the NRI runtime.
type Option func (*Runtime) error

// WithConfigPath returns an option to override the default NRI config path.
func WithConfigPath(path string) Option {
	return func (r *Runtime) error {
		r.configPath = path
		return nil
	}
}

// WithPluginPath returns an option to override the default NRI plugin path.
func WithPluginPath(path string) Option {
	return func (r *Runtime) error {
		r.pluginPath = path
		return nil
	}
}

// WithSocketPath returns an option to override the default NRI socket path.
func WithSocketPath(path string) Option {
	return func (r *Runtime) error {
		r.socketPath = path
		return nil
	}
}

// New creates a new NRI Runtime.
func New(syncFn SyncFn, adjustFn AdjustFn, options ...Option) (*Runtime, error) {
	var err error

	r := &Runtime{
		syncFn:     syncFn,
		adjustFn:   adjustFn,
		configPath: DefaultConfigPath,
		pluginPath: DefaultPluginPath,
		socketPath: DefaultSocketPath,
	}

	for _, o := range options {
		if err = o(r); err != nil {
			return nil, errors.Wrap(err, "NRI: failed to apply option")
		}
	}

	if r.cfg, err = ReadConfig(r.configPath); err != nil {
		return nil, err
	}

	log.Infof(nil, "NRI: runtime interface created")

	return r, nil
}

// Start up the NRI runtime.
func (r *Runtime) Start() error {
	log.Infof(nil, "NRI: runtime interface starting up...")

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
	log.Infof(nil, "NRI: runtime interface shutting down...")

	r.Lock()
	defer r.Unlock()

	r.stopListener()
	r.stopPlugins()
}

// RunPodSandbox relays the corresponding CRI request to plugins.
func (r *Runtime) RunPodSandbox(ctx context.Context, req *RunPodSandboxRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.runPodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StopPodSandbox relays the corresponding CRI request to plugins.
func (r *Runtime) StopPodSandbox(ctx context.Context, req *StopPodSandboxRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.stopPodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// RemovePodSandbox relays the corresponding CRI request to plugins.
func (r *Runtime) RemovePodSandbox(ctx context.Context, req *RemovePodSandboxRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.removePodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// CreateContainer relays the corresponding CRI request to plugins.
func (r *Runtime) CreateContainer(ctx context.Context, req *CreateContainerRequest) (*CreateContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := newCreateResultCollector(req)
	for _, plugin := range r.plugins {
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

// PostCreateContainer relays the corresponding event to plugins.
func (r *Runtime) PostCreateContainer(ctx context.Context, req *PostCreateContainerRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.postCreateContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StartContainer relays the corresponding CRI request to plugins.
func (r *Runtime) StartContainer(ctx context.Context, req *StartContainerRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.startContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// PostStartContainer relays the corresponding event to plugins.
func (r *Runtime) PostStartContainer(ctx context.Context, req *PostStartContainerRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.postStartContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// UpdateContainer relays the corresponding CRI request to plugins.
func (r *Runtime) UpdateContainer(ctx context.Context, req *UpdateContainerRequest) (*UpdateContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := newResultCollector()
	for _, plugin := range r.plugins {
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

// PostUpdateContainer relays the corresponding event to plugins.
func (r *Runtime) PostUpdateContainer(ctx context.Context, req *PostUpdateContainerRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.postUpdateContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StopContainer relays the corresponding CRI request to plugins.
func (r *Runtime) StopContainer(ctx context.Context, req *StopContainerRequest) (*StopContainerResponse, error) {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	result := newResultCollector()
	for _, plugin := range r.plugins {
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
func (r *Runtime) RemoveContainer(ctx context.Context, req *RemoveContainerRequest) error {
	r.Lock()
	defer r.Unlock()
	defer r.removeClosedPlugins()

	for _, plugin := range r.plugins {
		err := plugin.removeContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// Perform a set of unsolicited container adjustments requested by a plugin.
func (r *Runtime) adjustContainers(ctx context.Context, req []*ContainerAdjustment) ([]*ContainerAdjustment, error) {
	r.Lock()
	defer r.Unlock()

	return r.adjustFn(ctx, req)
}

// Start up pre-installed plugins.
func (r *Runtime) startPlugins() (retErr error) {
	var plugins []*plugin

	log.Infof(nil, "NRI: starting plugins...")

	names, configs, err := r.discoverPlugins()
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
		log.Infof(nil, "NRI: starting plugin %q...", name)

		p, err := newLaunchedPlugin(r.pluginPath, name, configs[i])
		if err != nil {
			return errors.Wrapf(err, "failed to start NRI plugin %q", name)
		}

		if err := p.start(); err != nil {
			return err
		}

		plugins = append(plugins, p)
	}

	r.plugins = plugins
	return nil
}

// Stop plugins.
func (r *Runtime) stopPlugins() {
	log.Infof(nil, "NRI: stopping plugins...")

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
		log.Infof(nil, "NRI: connection from external plugins disabled")
		return nil
	}

	os.Remove(r.socketPath)
	if err := os.MkdirAll(filepath.Dir(r.socketPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create socket %q", r.socketPath)
	}

	l, err := net.ListenUnix("unix", &net.UnixAddr{
		Name:  r.socketPath,
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
				log.Infof(ctx, "NRI: stopped accepting plugin connections (%v)", err)
				return
			}

			p, err := newExternalPlugin(conn)
			if err != nil {
				log.Errorf(ctx, "NRI: failed to create external plugin: %v", err)
				continue
			}

			if err := p.start(); err != nil {
				log.Errorf(ctx, "NRI: failed to start external plugin: %v", err)
				continue
			}

			r.Lock()

			err = r.syncFn(ctx, p.synchronize)
			if err != nil {
				log.Infof(ctx, "NRI: failed to synchronize plugin: %v", err)
			} else {
				r.plugins = append(r.plugins, p)
				r.sortPlugins()
			}

			r.Unlock()

			log.Infof(ctx, "NRI: plugin %q connected", p.Name())
		}
	}()

	return nil
}

func (r *Runtime) discoverPlugins() ([]string, []string, error) {
	var (
		plugins []string
		configs []string
		entries []fs.FileInfo
		err     error
	)

	if entries, err = ioutil.ReadDir(r.pluginPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, errors.Wrapf(err,
			"failed to discover plugins in %q", r.pluginPath)
	}

	for _, e := range entries {
		if e.IsDir() || e.Mode()&fs.FileMode(0111) == 0 {
			continue
		}

		name := e.Name()
		if !r.cfg.isPluginEnabled(name) {
			log.Infof(nil, "NRI: skipping disabled plugin %q", name)
			continue
		}

		cfg, err := r.cfg.getPluginConfig(name)
		if err != nil {
			return nil, nil, err
		}

		log.Infof(nil, "NRI: discovered plugin %q", name)

		plugins = append(plugins, name)
		configs = append(configs, cfg)
	}

	return plugins, configs, nil
}

func (r *Runtime) sortPlugins() {
	r.removeClosedPlugins()
	sort.Slice(r.plugins, func (i, j int) bool {
		return r.plugins[i].id < r.plugins[j].id
	})
	if len(r.plugins) > 0 {
		log.Infof(nil, "NRI: plugin execution order")
		for i, p := range r.plugins {
			log.Infof(nil, "  #%d: %q", i+1, p.Name())
		}
	}
}

func (r *Runtime) Lock() {
	ourStack := debug.Stack()
	r.ownerLock.Lock()
	if r.ownerStack != nil {
		fmt.Printf("Runtime lock contention:\n")
		fmt.Printf("%s", formatStack("    owner: ", r.ownerStack))
		fmt.Printf("%s", formatStack("    asker: ", ourStack))
	}
	r.ownerLock.Unlock()
	r.lock.Lock()
	r.ownerStack = ourStack
}

func (r *Runtime) Unlock() {
	r.ownerLock.Lock()
	r.ownerStack = nil
	r.ownerLock.Unlock()
	r.lock.Unlock()
}

func formatStack(prefix string, stack []byte) string {
	b := strings.Builder{}
	s := strings.Builder{}
	b.Write(stack)
	for _, line := range strings.Split(b.String(), "\n") {
		s.Write([]byte(prefix + line + "\n"))
	}
	return s.String()
}
