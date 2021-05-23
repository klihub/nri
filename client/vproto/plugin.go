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
	"io"
	"net"
	"os/exec"
	"time"

	"github.com/pkg/errors"

	api "github.com/containerd/nri/api/plugin/vproto"
	"github.com/containerd/ttrpc"
)

const (
	// event subscription masks
	runPodSandbox int = (1 << iota)
	stopPodSandbox
	removePodSandbox
	createContainer
	startContainer
	updateContainer
	stopContainer
	removeContainer
	allEvents = 0x7fffffff

	// timeout for communicating with plugins
	pluginTimeout = 3 * time.Second
)

// plugin is the stub for a registered (configured or connected) plugin.
//
// Plugins are either declared statically or connected dynamically. Static
// plugins are declared in the NRI configuration file. They are started by
// the NRI runtime client when it gets instantiated/started by the runtime.
// Dynamic plugins are launched by means external to NRI and they register
// themselves by connecting to a dedicated NRI socket at a well-known path.
//
// Configuration for static plugins is part of the NRI configuration. Dynamic
// plugins can acquire their configuration by some other means, external to
// NRI.
type plugin struct {
	name   string            // plugin name
	id     string            // plugin id (sort order for dynamic plugins)
	pid    int               // plugin pid for dynamic plugins
	cmd    *exec.Cmd         // plugin Cmd (if exec()ed by us)
	stdin  io.WriteCloser    // plugin stdin (if exec()ed by us)
	conn   net.Conn          // plugin connection
	ttrpcc *ttrpc.Client     // plugin ttRPC client
	client api.PluginService // plugin service
	events int               // plugin event subscription
	closed bool              // plugin has closed connection
}

// close the connection to the plugin.
func (p *plugin) close() error {
	if p.ttrpcc != nil {
		if err := p.ttrpcc.Close(); err != nil {
			if err != ttrpc.ErrClosed {
				return err
			}
		}
		p.ttrpcc = nil
	}
	if p.conn != nil {
		if err := p.conn.Close(); err != nil {
			return err
		}
	}
	return nil
}

// stop the plugin.
func (p *plugin) stop() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Process.Wait()
		p.cmd.Process.Release()
	}
}

// configure the plugin and subscribe it for the events it requests.
func (p *plugin) configure(ctx context.Context, config string) error {
	ctx, cancel := context.WithTimeout(ctx, pluginTimeout)
	defer cancel()

	rpl, err := p.client.Configure(ctx, &api.ConfigureRequest{
		Config: config,
	})
	if err != nil {
		return errors.Wrap(err, "failed to configure plugin")
	}

	if rpl.Id != "" {
		p.id = rpl.Id
	} else {
		p.id = p.name
	}

	if p.cmd == nil && p.id == "" {
		return errors.New("dynamic plugin failed to identify itself")
	}

	eventMask := map[api.Event]int{
		api.Event_RUN_POD_SANDBOX:    runPodSandbox,
		api.Event_STOP_POD_SANDBOX:   stopPodSandbox,
		api.Event_REMOVE_POD_SANDBOX: removePodSandbox,
		api.Event_CREATE_CONTAINER:   createContainer,
		api.Event_START_CONTAINER:    startContainer,
		api.Event_UPDATE_CONTAINER:   updateContainer,
		api.Event_STOP_CONTAINER:     stopContainer,
		api.Event_REMOVE_CONTAINER:   removeContainer,
		api.Event_ALL:                allEvents,
	}

	for _, event := range rpl.Subscribe {
		mask, ok := eventMask[event]
		if !ok {
			return errors.Errorf("invalid plugin event: %v", event)
		}
		p.events |= mask
	}

	if p.events == 0 {
		p.events = allEvents
	}

	if p.cmd == nil {
		p.pid, err = getPeerPid(p.conn)
		if err != nil {
			return errors.Wrap(err, "failed to determine peer process")
		}
	}

	return nil
}

// synchronize the plugin with the state of the runtime.
func (p *plugin) synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	log.Infof("NRI: synchronizing plugin `%s`...", p.id)

	req := &api.SynchronizeRequest{
		Pods:       pods,
		Containers: containers,
	}
	rpl, err := p.client.Synchronize(ctx, req)
	if err != nil {
		return nil, err
	}

	return rpl.Updates, nil
}

// Relay RunPodSandbox request to the plugin.
func (p *plugin) runPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) error {
	if !p.handlesEvent(runPodSandbox) {
		return nil
	}

	_, err := p.client.RunPodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil
	}

	return err
}

// Relay StopPodSandbox request to the plugin.
func (p *plugin) stopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) error {
	if !p.handlesEvent(stopPodSandbox) {
		return nil
	}

	_, err := p.client.StopPodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil
	}

	return err
}

// Relay RemovePodSandbox request to the plugin.
func (p *plugin) removePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) error {
	if !p.handlesEvent(removePodSandbox) {
		return nil
	}

	_, err := p.client.RemovePodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil
	}

	return err
}

// Relay CreateContainer request to plugin.
func (p *plugin) createContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	if !p.handlesEvent(createContainer) {
		return nil, nil
	}

	rpl, err := p.client.CreateContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rpl, nil
}

// Relay StartContainer request to the plugin.
func (p *plugin) startContainer(ctx context.Context, req *api.StartContainerRequest) error {
	if !p.handlesEvent(startContainer) {
		return nil
	}

	_, err := p.client.StartContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil
	}

	return err
}

// Relay UpdateContainer request to plugin.
func (p *plugin) updateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	if !p.handlesEvent(updateContainer) {
		return nil, nil
	}

	rpl, err := p.client.UpdateContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rpl, nil
}

// Relay StopContainer request to plugin.
func (p *plugin) stopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	if !p.handlesEvent(stopContainer) {
		return nil, nil
	}

	rpl, err := p.client.StopContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return rpl, nil
}

// Relay RemoveContainer request to the plugin.
func (p *plugin) removeContainer(ctx context.Context, req *api.RemoveContainerRequest) error {
	if !p.handlesEvent(removeContainer) {
		return nil
	}
	_, err := p.client.RemoveContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.closed = true
		return nil
	}
	return err
}

// check if the plugin (ttRPC) connection has been marked closed.
func (p *plugin) isClosed() bool {
	return p.closed
}

// shutdown shuts down the client.
func (p *plugin) shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pluginTimeout)
	defer cancel()

	_, err := p.client.Shutdown(ctx, &api.ShutdownRequest{})
	return err
}

// handlesEvent returns true if the plugin subcribes for the given event.
func (p *plugin) handlesEvent(e int) bool {
	return p.events&e != 0
}
