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
	"context"
	"os"
	"path/filepath"
	stdnet "net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/containerd/ttrpc"
	"github.com/containerd/nri/v2alpha1/pkg/api"
	nrilog "github.com/containerd/nri/v2alpha1/pkg/log"
	"github.com/containerd/nri/v2alpha1/pkg/net"
	"github.com/containerd/nri/v2alpha1/pkg/net/multiplex"
)

// Option for creating a plugin stub.
type Option func (*options) error

// Plugin is the interface the stub expect a plugin to implement.
type Plugin interface {
	Configure(config string) (SubscribeMask, error)
	Synchronize([]*api.PodSandbox, []*api.Container) ([]*api.ContainerAdjustment, error)
	Shutdown()

	RunPodSandbox(*api.PodSandbox)
	StopPodSandbox(*api.PodSandbox)
	RemovePodSandbox(*api.PodSandbox)

	CreateContainer(*api.PodSandbox, *api.Container) (*api.ContainerCreateAdjustment, []*api.ContainerAdjustment, error)
	PostCreateContainer(*api.PodSandbox, *api.Container)
	StartContainer(*api.PodSandbox, *api.Container)
	PostStartContainer(*api.PodSandbox, *api.Container)
	UpdateContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerAdjustment, error)
	PostUpdateContainer(*api.PodSandbox, *api.Container)
	StopContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerAdjustment, error)
	RemoveContainer(*api.PodSandbox, *api.Container)
}

type Stub interface {
	AdjustContainers([]*api.ContainerAdjustment) ([]*api.ContainerAdjustment, error)
	Start(context.Context) error
	Stop() error
	Wait()
	Run(context.Context) error
}

// SubscribeMask is the mask of requests and events the plugin wants to subscribe for.
type SubscribeMask int

const (
	// RunPodSandbox event mask.
	RunPodSandbox SubscribeMask = 1 << iota
	// StopPodSandbox event mask.
	StopPodSandbox
	// RemovePodSandbox event mask.
	RemovePodSandbox
	// CreateContainer request mask.
	CreateContainer
	// PostCreateContainer event mask.
	PostCreateContainer
	// StartContainer event mask.
	StartContainer
	// PostStartContainer event mask.
	PostStartContainer
	// UpdateContainer request mask.
	UpdateContainer
	// PostUpdateContainer event mask.
	PostUpdateContainer
	// StopContainer request mask.
	StopContainer
	// RemoveContainer event mask.
	RemoveContainer
	// AllEvents mask.
	AllEvents SubscribeMask = 0x7ffffff

	// Plugin registration timeout.
	registerTimeout = 5 * time.Second
)

var (
	// ErrNoConn is the error returned if no pre-connected NRI socket is found.
	ErrNoConn = errors.New("NRI pre-connected socket not found")
	// Socket path used to connect to NRI.
	socketPath = api.DefaultSocketPath

	//
	log = nrilog.Get()
)

type options struct {
	conn    stdnet.Conn
	name    string
	id      string
	onClose OnClose
}

type stub struct {
	opts     options
	rpcm     multiplex.Mux
	rpcl     stdnet.Listener
	rpcs     *ttrpc.Server
	runtime  api.RuntimeService
	rpcc     *ttrpc.Client
	errC     chan error
	cfgC     chan error
	stopOnce sync.Once
	stopC    chan struct{}
	plugin   Plugin
}

// OnClose is a function to call when the plugin connection goes down.
type OnClose func()

func WithOnClose(onClose OnClose) Option {
	return func (o *options) error {
		o.onClose = onClose
		return nil
	}
}

// WithPluginName returns an option to set the registered plugin name.
func WithPluginName(name string) Option {
	return func (o *options) error {
		log.Infof(nil, "setting plugin name %q...", name)
		if o.name != "" {
			return errors.Errorf("plugin name already set (%q)", o.name)
		}
		o.name = name
		return nil
	}
}

// WithPluginID returns an option to set the registered plugin id.
func WithPluginID(id string) Option {
	return func (o *options) error {
		log.Infof(nil, "setting plugin ID %q...", id)
		if o.id != "" {
			return errors.Errorf("plugin ID already set (%q)", o.id)
		}
		o.id = id
		return nil
	}
}

// WithSocketPath returns an option to use the NRI socket path.
func WithSocketPath(path string) Option {
	return func (o *options) error {
		conn, err := Dial(path)
		if err != nil {
			return errors.Wrapf(err, "failed to set socket path")
		}
		o.conn = conn
		return nil
	}
}

// WithConnection returns an option to use the NRI socket connection.
func WithConnection(conn stdnet.Conn) Option {
	return func (o *options) error {
		o.conn = conn
		return nil
	}
}

// New creates a new plugin stub.
func New(plugin Plugin, opts ...Option) (Stub, error) {
	var (
		p   *stub
		err error
	)

	log.Infof(nil, "Creating plugin stub...")

	p = &stub{
		stopC:  make(chan struct{}),
		plugin: plugin,
	}

	opts = append([]Option{
		WithPluginName(os.Getenv(api.PluginNameEnvVar)),
		WithPluginID(os.Getenv(api.PluginIDEnvVar)), },
		opts...
	)
	for _, o := range opts {
		if err := o(&p.opts); err != nil {
			return nil, err
		}
	}

	if p.opts.conn == nil {
		p.opts.conn, err = Connect()
		if err != nil {
			return nil, err
		}
	}

	if p.opts.name == "" {
		p.opts.name = filepath.Base(os.Args[0])
	}
	if p.opts.id == "" {
		split := strings.Split(p.opts.name, "-")
		if l := len(split); l > 1 && 1 <= len(split[0]) && len(split[0]) <= 2 {
			p.opts.id = split[0]
			p.opts.name = strings.Join(split[1:], "-")
		} else {
			p.opts.id = "00"
		}
	}

	p.rpcm = multiplex.Multiplex(p.opts.conn)

	p.rpcl, err = p.rpcm.Listen(multiplex.PluginServiceConn)
	if err != nil {
		return nil, err
	}
	p.rpcs, err = ttrpc.NewServer()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ttrpc server")
	}
	api.RegisterPluginService(p.rpcs, p)

	rconn, err := p.rpcm.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		return nil, err
	}
	p.rpcc = ttrpc.NewClient(rconn,
		ttrpc.WithOnClose(
			func () {
				p.rpcClosed()
			}))
	p.runtime = api.NewRuntimeClient(p.rpcc)

	return p, nil
}

// Run starts event processing by the plugin.
func (p *stub) Run(ctx context.Context) error {
	log.Infof(nil, "Running plugin stub...")

	err := p.Start(ctx)
	if err != nil {
		return err
	}

	select {
	case err = <-p.errC:
		return err
	case <-p.stopC:
	}

	return nil
}

// Start starts the plugin.
func (p *stub) Start(ctx context.Context) error {
	log.Infof(nil, "Starting plugin stub...")

	var err error

	p.errC = make(chan error, 1)

	go func() {
		err := p.rpcs.Serve(ctx, p.rpcl)
		p.errC <- err
	}()

	p.cfgC = make(chan error, 1)
	if err := p.register(ctx); err != nil {
		p.Stop()
		return err
	}
	err = <-p.cfgC

	return err
}

// Stop stops the plugin.
func (p *stub) Stop() error {
	log.Infof(nil, "Stopping plugin stub...")

	var err error

	p.stopOnce.Do(func () {
		p.rpcm.Close()
		p.rpcl.Close()
		p.rpcs.Close()
		p.rpcc.Close()
		close(p.errC)
		close(p.stopC)
		if p.cfgC != nil {
			close(p.cfgC)
		}
	})
	return err
}

// Wait for the plugin stub to stop.
func (p *stub) Wait() {
	<-p.stopC
}

func (p *stub) rpcClosed() {
	p.Stop()
	if p.opts.onClose != nil {
		p.opts.onClose()
		return
	}
	os.Exit(0)
}

// register the plugin with NRI.
func (p *stub) register(ctx context.Context) error {
	log.Infof(nil, "Registering plugin %s-%s...", p.opts.id, p.opts.name)

	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	_, err := p.runtime.RegisterPlugin(ctx, &api.RegisterPluginRequest{
		PluginName: p.opts.name,
		PluginId:   p.opts.id,
	})
	if err != nil {
		return errors.Wrapf(err, "failed to register to NRI")
	}
	return nil
}

// AdjustContainers relays a plugin container adjustment request to NRI/runtime.
func (p *stub) AdjustContainers(adjust []*api.ContainerAdjustment) ([]*api.ContainerAdjustment, error) {
	ctx := context.Background()
	req := &api.AdjustContainersRequest{
		Adjust: adjust,
	}
	rpl, err := p.runtime.AdjustContainers(ctx, req)
	if rpl != nil {
		return rpl.Failed, err
	} else {
		return nil, err
	}
}

// Configure the plugin.
func (p *stub) Configure(ctx context.Context, req *api.ConfigureRequest) (rpl *api.ConfigureResponse, retErr error) {
	log.Infof(nil, "Configuring plugin...")

	defer func() { p.cfgC <- retErr	}()

	var (
		mask SubscribeMask
		err  error
	)

	if mask, err = p.plugin.Configure(req.Config); err != nil {
		return nil, err
	}

	eventMasks := map[SubscribeMask]api.Event{
		RunPodSandbox:       api.Event_RUN_POD_SANDBOX,
		StopPodSandbox:      api.Event_STOP_POD_SANDBOX,
		RemovePodSandbox:    api.Event_REMOVE_POD_SANDBOX,
		CreateContainer:     api.Event_CREATE_CONTAINER,
		PostCreateContainer: api.Event_POST_CREATE_CONTAINER,
		StartContainer:      api.Event_START_CONTAINER,
		PostStartContainer:  api.Event_POST_START_CONTAINER,
		UpdateContainer:     api.Event_UPDATE_CONTAINER,
		PostUpdateContainer: api.Event_POST_UPDATE_CONTAINER,
		StopContainer:       api.Event_STOP_CONTAINER,
		RemoveContainer:     api.Event_REMOVE_CONTAINER,
	}

	rpl = &api.ConfigureResponse{}

	if mask == AllEvents {
		rpl.Subscribe = []api.Event { api.Event_ALL }
	} else {
		for m, e := range eventMasks {
			if m & mask != 0 {
				rpl.Subscribe = append(rpl.Subscribe, e)
			}
		}
	}

	if len(rpl.Subscribe) < 1 {
		rpl.Subscribe = []api.Event{ api.Event_ALL }
	}

	return rpl, nil
}

// Synchronize plugin with the state of the container.
func (p *stub) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	adjust, err := p.plugin.Synchronize(req.Pods, req.Containers)
	return &api.SynchronizeResponse{
		Adjust: adjust,
	}, err
}

// Shutdown the plugin.
func (p *stub) Shutdown(ctx context.Context, req *api.ShutdownRequest) (*api.ShutdownResponse, error) {
	return &api.ShutdownResponse{}, nil
}

// RunPodSandbox event handler.
func (p *stub) RunPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) (*api.RunPodSandboxResponse, error) {
	p.plugin.RunPodSandbox(req.Pod)
	return &api.RunPodSandboxResponse{}, nil
}

// StopPodSandbox event handler.
func (p *stub) StopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) (*api.StopPodSandboxResponse, error) {
	p.plugin.StopPodSandbox(req.Pod)
	return &api.StopPodSandboxResponse{}, nil
}

// RemovePodSandbox event handler.
func (p *stub) RemovePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) (*api.RemovePodSandboxResponse, error) {
	p.plugin.RemovePodSandbox(req.Pod)
	return &api.RemovePodSandboxResponse{}, nil
}

// CreateContainer request handler.
func (p *stub) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	create, adjust, err := p.plugin.CreateContainer(req.Pod, req.Container)
	return &api.CreateContainerResponse{
		Create: create,
		Adjust: adjust,
	}, err
}

// PostCreateContainer event handler.
func (p *stub) PostCreateContainer(ctx context.Context, req *api.PostCreateContainerRequest) (*api.PostCreateContainerResponse, error) {
	p.plugin.PostCreateContainer(req.Pod, req.Container)
	return &api.PostCreateContainerResponse{}, nil
}

// StartContainer event handler.
func (p *stub) StartContainer(ctx context.Context, req *api.StartContainerRequest) (*api.StartContainerResponse, error) {
	p.plugin.StartContainer(req.Pod, req.Container)
	return &api.StartContainerResponse{}, nil
}

// PostStartContainer event handler.
func (p *stub) PostStartContainer(ctx context.Context, req *api.PostStartContainerRequest) (*api.PostStartContainerResponse, error) {
	p.plugin.PostStartContainer(req.Pod, req.Container)
	return &api.PostStartContainerResponse{}, nil
}

// UpdateContainer request handler.
func (p *stub) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	adjust, err := p.plugin.UpdateContainer(req.Pod, req.Container)
	return &api.UpdateContainerResponse{
		Adjust: adjust,
	}, err
}

// PostUpdateContainer event handler.
func (p *stub) PostUpdateContainer(ctx context.Context, req *api.PostUpdateContainerRequest) (*api.PostUpdateContainerResponse, error) {
	p.plugin.PostUpdateContainer(req.Pod, req.Container)
	return &api.PostUpdateContainerResponse{}, nil
}

// StopContainer request handler.
func (p *stub) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	adjust, err := p.plugin.StopContainer(req.Pod, req.Container)
	return &api.StopContainerResponse{
		Adjust: adjust,
	}, err
}

// RemoveContainer event handler.
func (p *stub) RemoveContainer(ctx context.Context, req *api.RemoveContainerRequest) (*api.RemoveContainerResponse, error) {
	p.plugin.PostUpdateContainer(req.Pod, req.Container)
	return &api.RemoveContainerResponse{}, nil
}

// Connect returns a connection to the NRI/runtime.
func Connect() (stdnet.Conn, error) {
	conn, err := ConnFromEnv()
	if err == ErrNoConn {
		conn, err = Dial(SocketPath())
	}

	return conn, err
}

// ConnFromEnv returns the connection pre-created by NRI.
func ConnFromEnv() (stdnet.Conn, error) {
	log.Infof(nil, "Getting connection from environment...")

	v := os.Getenv(api.PluginSocketEnvVar)
	if v == "" {
		return nil, ErrNoConn
	}

	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid socket in environment (%s=%q)",
			api.PluginSocketEnvVar, v)
	}

	conn, err := net.NewFdConn(fd)
	if err != nil {
		return nil, errors.Wrapf(err, "invalid socket (%d) in environment", fd)
	}

	return conn, nil
}

// Dial connects to the NRI socket.
func Dial(path string) (stdnet.Conn, error) {
	log.Infof(nil, "Connecting to %s...", path)

	conn, err := stdnet.Dial("unix", path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect to socket")
	}
	return conn, nil
}

// SocketPath returns the socket path used to connect to NRI.
func SocketPath() string {
	return socketPath
}

// SetSocketPath overrides the default socket path for NRI.
func SetSocketPath(path string) {
	socketPath = path
}

