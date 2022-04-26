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
	stdnet "net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	nrilog "github.com/containerd/nri/v2alpha1/pkg/log"
	"github.com/containerd/nri/v2alpha1/pkg/net"
	"github.com/containerd/nri/v2alpha1/pkg/net/multiplex"
	"github.com/containerd/ttrpc"
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
	// If a non-zero EventMask is returned, the stub will subscribe
	// the plugin in NRI for the returned events.
	Configure(config, runtime, version string) (api.EventMask, error)
}

// SynchronizeInterface handles Synchronize API requests.
type SynchronizeInterface interface {
	// Synchronize the plugin with the current state of the runtime.
	// The plugin can request updates to a set of containers in response.
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
	RunPodSandbox(*api.PodSandbox)
}

// StopPodInterface handles StopPodSandbox API events.
type StopPodInterface interface {
	// StopPodSandbox relays a StopPodSandbox event to the plugin.
	StopPodSandbox(*api.PodSandbox)
}

// RemovePodInterface handles RemovePodSandbox API events.
type RemovePodInterface interface {
	// RemovePodSandbox relays a RemovePodSandbox event to the plugin.
	RemovePodSandbox(*api.PodSandbox)
}

// CreateContainerInterface handles CreateContainer API requests.
type CreateContainerInterface interface {
	// CreateContainer relays a CreateContainer request to the plugin.
	// The plugin can in response request adjustments to the container being
	// created and updates to a set of other containers prior to this.
	CreateContainer(*api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
}

// StartContainerInterface handles StartContainer API requests.
type StartContainerInterface interface {
	// StartContainer relays a StartContainer event to the plugin.
	StartContainer(*api.PodSandbox, *api.Container)
}

// UpdateContainerInterface handles UpdateContainer API requests.
type UpdateContainerInterface interface {
	// UpdateContainer relays an UpdateContainer request to the plugin.
	// The plugin can in response request updates to a set of containers.
	// This can also override the modifications to the requested container.
	UpdateContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// StopContainerInterface handles StopContainer API requests.
type StopContainerInterface interface {
	// StopContainer relays an StopContainer request to the plugin.
	// The plugin can in response request updates to a set of containers.
	StopContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// RemoveContainerInterface handles RemoveContainer API events.
type RemoveContainerInterface interface {
	// RemoveContainer relays an RemoveContainer event to the plugin.
	RemoveContainer(*api.PodSandbox, *api.Container)
}

// PostCreateContainerInterface handles PostCreateContainer API events.
type PostCreateContainerInterface interface {
	// PostCreateContainer relays an PostCreateContainer event to the plugin.
	PostCreateContainer(*api.PodSandbox, *api.Container)
}

// PostStartContainerInterface handles PostStartContainer API events.
type PostStartContainerInterface interface {
	// PostStartContainer relays an PostStartContainer event to the plugin.
	PostStartContainer(*api.PodSandbox, *api.Container)
}

// PostUpdateContainerInterface handles PostUpdateContainer API events.
type PostUpdateContainerInterface interface {
	// PostUpdateContainer relays an PostUpdateContainer event to the plugin.
	PostUpdateContainer(*api.PodSandbox, *api.Container)
}

// Stub is the interface the stub (library) provides for the plugin implementation.
type Stub interface {
	// Run the plugin (Start() then wait for an error or being stopped).
	Run(context.Context) error
	// Start the plugin.
	Start(context.Context) error
	// Stop the plugin.
	Stop() error
	// Wait for the plugin to stop.
	Wait()

	// UpdateContainer requests updates of the given set of containers from NRI.
	UpdateContainers([]*api.ContainerUpdate) ([]*api.ContainerUpdate, error)
}

const (
	// Plugin registration timeout.
	registerTimeout = 5 * time.Second
)

var (
	// ErrNoConn is the error returned if a required pre-connected NRI socket is not found.
	ErrNoConn = errors.New("NRI pre-connected socket not found")

	// Logger for messages generated internally by the stub itself.
	log = nrilog.Get()

	// Used instead of a nil Context in logging.
	noCtx = context.TODO()
)

// EventMask holds a mask of events, typically for plugin subscription.
type EventMask = api.EventMask

// Option to apply to a plugin during its creation.
type Option func(*stub) error

// WithOnClose returns an option for calling a function when the connection goes down.
func WithOnClose(onClose func()) Option {
	return func(s *stub) error {
		s.onClose = onClose
		return nil
	}
}

// WithPluginName returns an option for setting the registered plugin name.
func WithPluginName(name string) Option {
	return func(s *stub) error {
		if s.name != "" {
			return errors.Errorf("plugin name already set (%q)", s.name)
		}
		s.name = name
		return nil
	}
}

// WithPluginIdx returns an option for setting the registered plugin id.
func WithPluginIdx(idx string) Option {
	return func(s *stub) error {
		if s.idx != "" {
			return errors.Errorf("plugin ID already set (%q)", s.idx)
		}
		s.idx = idx
		return nil
	}
}

// WithSocketPath returns an option for setting the NRI socket path to connect to.
func WithSocketPath(path string) Option {
	return func(s *stub) error {
		conn, err := Dial(path)
		if err != nil {
			return errors.Wrapf(err, "failed to set socket path")
		}
		s.conn = conn
		return nil
	}
}

// WithConnection returns an option for using an esablished NRI connection.
func WithConnection(conn stdnet.Conn) Option {
	return func(s *stub) error {
		s.conn = conn
		return nil
	}
}

// stub is our implementation of Stub.
type stub struct {
	plugin   interface{}
	handlers handlers
	events   api.EventMask
	name     string
	idx      string
	conn     stdnet.Conn
	onClose  func()
	rpcm     multiplex.Mux
	rpcl     stdnet.Listener
	rpcs     *ttrpc.Server
	rpcc     *ttrpc.Client
	runtime  api.RuntimeService
	stopOnce sync.Once
	stopC    chan struct{}
	errC     chan error
	cfgC     chan error
}

// Handlers for the full set of event and request a plugin can implement.
type handlers struct {
	Configure           func(string, string, string) (api.EventMask, error)
	Synchronize         func([]*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error)
	Shutdown            func(*api.ShutdownRequest)
	RunPodSandbox       func(*api.PodSandbox)
	StopPodSandbox      func(*api.PodSandbox)
	RemovePodSandbox    func(*api.PodSandbox)
	CreateContainer     func(*api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
	StartContainer      func(*api.PodSandbox, *api.Container)
	UpdateContainer     func(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	StopContainer       func(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	RemoveContainer     func(*api.PodSandbox, *api.Container)
	PostCreateContainer func(*api.PodSandbox, *api.Container)
	PostStartContainer  func(*api.PodSandbox, *api.Container)
	PostUpdateContainer func(*api.PodSandbox, *api.Container)
}

// New creates a stub with the given plugin and options.
func New(p interface{}, opts ...Option) (Stub, error) {
	var err error

	stub := &stub{
		plugin: p,
		name:   os.Getenv(api.PluginNameEnvVar),
		idx:    os.Getenv(api.PluginIdxEnvVar),
		stopC:  make(chan struct{}),
	}

	if err := stub.identityFromEnv(); err != nil {
		return nil, err
	}

	if err := stub.setupHandlers(); err != nil {
		return nil, errors.New("internal error: invalid plugin, can't handle any NRI events")
	}

	for _, o := range opts {
		if err = o(stub); err != nil {
			return nil, err
		}
	}

	if stub.name == "" {
		if err := stub.identityFromBinary(); err != nil {
			return nil, err
		}
	}

	// connect to NRI Runtime Service socket and set up multiplexing
	if stub.conn == nil {
		if stub.conn, err = Connect(); err != nil {
			return nil, err
		}
	}
	stub.rpcm = multiplex.Multiplex(stub.conn)

	// create NRI Plugin Service ttrpc server
	if stub.rpcl, err = stub.rpcm.Listen(multiplex.PluginServiceConn); err != nil {
		return nil, err
	}
	if stub.rpcs, err = ttrpc.NewServer(); err != nil {
		return nil, errors.Wrap(err, "failed to create ttrpc server")
	}
	api.RegisterPluginService(stub.rpcs, stub)

	// create NRI Runtime Service ttrpc client
	conn, err := stub.rpcm.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		return nil, errors.Wrap(err, "failed to multiplex ttrpc client connection")
	}
	stub.rpcc = ttrpc.NewClient(conn,
		ttrpc.WithOnClose(func() {
			stub.connClosed()
		}),
	)
	stub.runtime = api.NewRuntimeClient(stub.rpcc)

	log.Infof(noCtx, "Created plugin %s (%s, handles %s)", stub.Name(),
		filepath.Base(os.Args[0]), stub.events.PrettyString())

	return stub, nil
}

// Run the plugin (start event processing, wait for an error or getting stopped).
func (stub *stub) Run(ctx context.Context) error {
	var err error

	if err = stub.Start(ctx); err != nil {
		return err
	}

	select {
	case err = <-stub.errC:
		return err
	case <-stub.stopC:
	}

	return nil
}

// Start processing events and requests relayed by the NRI Runtime.
func (stub *stub) Start(ctx context.Context) error {
	var err error

	stub.errC = make(chan error, 1)
	go func() {
		err := stub.rpcs.Serve(ctx, stub.rpcl)
		stub.errC <- err
	}()

	stub.cfgC = make(chan error, 1)
	if err := stub.register(ctx); err != nil {
		stub.Stop()
		return err
	}
	err = <-stub.cfgC

	if err == nil {
		log.Infof(ctx, "Started plugin %s...", stub.Name())
	}

	return err
}

// Stop the plugin.
func (stub *stub) Stop() error {
	var err error

	log.Infof(noCtx, "Stopping plugin %s...", stub.Name())

	stub.stopOnce.Do(func() {
		stub.rpcm.Close()
		stub.rpcl.Close()
		stub.rpcs.Close()
		stub.rpcc.Close()
		close(stub.errC)
		close(stub.stopC)
		if stub.cfgC != nil {
			close(stub.cfgC)
		}
	})
	return err
}

// Wait for the plugin to stop.
func (stub *stub) Wait() {
	<-stub.stopC
}

// Name returns the full indexed name of the plugin.
func (stub *stub) Name() string {
	return stub.idx + "-" + stub.name
}

// Connect returns a connection to the NRI/runtime.
func Connect() (stdnet.Conn, error) {
	conn, err := ConnFromEnv()
	if err == ErrNoConn {
		conn, err = Dial(api.DefaultSocketPath)
	}

	return conn, err
}

// ConnFromEnv returns the connection pre-created by NRI.
func ConnFromEnv() (stdnet.Conn, error) {
	log.Infof(noCtx, "Getting connection from environment...")

	v := os.Getenv(api.PluginSocketEnvVar)
	if v == "" {
		log.Infof(noCtx, "No connection found...")

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
	log.Infof(noCtx, "Connecting to %s...", path)

	conn, err := stdnet.Dial("unix", path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect to socket")
	}
	return conn, nil
}

// Handle a lost connection to the NRI Runtime.
func (stub *stub) connClosed() {
	stub.Stop()
	if stub.onClose != nil {
		stub.onClose()
		return
	}
	os.Exit(0)
}

// Register the plugin with the NRI Runtime.
func (stub *stub) register(ctx context.Context) error {
	log.Infof(ctx, "Registering plugin %s...", stub.Name())

	ctx, cancel := context.WithTimeout(ctx, registerTimeout)
	defer cancel()

	req := &api.RegisterPluginRequest{
		PluginName: stub.name,
		PluginIdx:  stub.idx,
	}
	if _, err := stub.runtime.RegisterPlugin(ctx, req); err != nil {
		return errors.Wrapf(err, "failed to register with NRI/Runtime")
	}

	return nil
}

//
// plugin event and request handlers
//

// UpdateContainers as requested by the plugin.
func (stub *stub) UpdateContainers(update []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	ctx := context.Background()
	req := &api.UpdateContainersRequest{
		Update: update,
	}
	rpl, err := stub.runtime.UpdateContainers(ctx, req)
	if rpl != nil {
		return rpl.Failed, err
	}
	return nil, err
}

// Configure the plugin.
func (stub *stub) Configure(ctx context.Context, req *api.ConfigureRequest) (rpl *api.ConfigureResponse, retErr error) {
	var (
		events api.EventMask
		err    error
	)

	log.Infof(ctx, "Configuring plugin %s for runtime %s/%s...", stub.Name(),
		req.RuntimeName, req.RuntimeVersion)

	defer func() {
		stub.cfgC <- retErr
	}()

	if handler := stub.handlers.Configure; handler == nil {
		events = stub.events
	} else {
		events, err = handler(req.Config, req.RuntimeName, req.RuntimeVersion)
		if err != nil {
			log.Errorf(ctx, "Plugin configuration failed: %v", err)
			return nil, err
		}

		// Only allow plugins to subscribe to events they can handle.
		if extra := events & ^stub.events; extra != 0 {
			log.Errorf(ctx, "Plugin subscribed for unhandled events %s (0x%x)",
				extra.PrettyString(), extra)
			return nil, errors.Errorf("internal error: unhandled events %s (0x%x)",
				extra.PrettyString(), extra)
		}

		log.Infof(ctx, "Subscribing plugin %s (%s) for events %s", stub.Name(),
			filepath.Base(os.Args[0]), events.PrettyString())
	}

	return &api.ConfigureResponse{
		Events: int32(events),
	}, nil
}

// Synchronize the plugin with the state of the runtime.
func (stub *stub) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	handler := stub.handlers.Synchronize
	if handler == nil {
		return &api.SynchronizeResponse{}, nil
	}
	update, err := handler(req.Pods, req.Containers)
	return &api.SynchronizeResponse{
		Update: update,
	}, err
}

// Shutdown the plugin.
func (stub *stub) Shutdown(ctx context.Context, req *api.ShutdownRequest) (*api.ShutdownResponse, error) {
	handler := stub.handlers.Shutdown
	if handler != nil {
		handler(req)
	}
	return &api.ShutdownResponse{}, nil
}

// CreateContainer request handler.
func (stub *stub) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	handler := stub.handlers.CreateContainer
	if handler == nil {
		return nil, nil
	}
	adjust, update, err := handler(req.Pod, req.Container)
	return &api.CreateContainerResponse{
		Adjust: adjust,
		Update: update,
	}, err
}

// UpdateContainer request handler.
func (stub *stub) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	handler := stub.handlers.UpdateContainer
	if handler == nil {
		return nil, nil
	}
	update, err := handler(req.Pod, req.Container)
	return &api.UpdateContainerResponse{
		Update: update,
	}, err
}

// StopContainer request handler.
func (stub *stub) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	handler := stub.handlers.StopContainer
	if handler == nil {
		return nil, nil
	}
	update, err := handler(req.Pod, req.Container)
	return &api.StopContainerResponse{
		Update: update,
	}, err
}

// StateChange event handler.
func (stub *stub) StateChange(ctx context.Context, evt *api.StateChangeEvent) (*api.Empty, error) {
	switch evt.Event {
	case api.Event_RUN_POD_SANDBOX:
		if handler := stub.handlers.RunPodSandbox; handler != nil {
			handler(evt.Pod)
		}
	case api.Event_STOP_POD_SANDBOX:
		if handler := stub.handlers.StopPodSandbox; handler != nil {
			handler(evt.Pod)
		}
	case api.Event_REMOVE_POD_SANDBOX:
		if handler := stub.handlers.RemovePodSandbox; handler != nil {
			handler(evt.Pod)
		}
	case api.Event_POST_CREATE_CONTAINER:
		if handler := stub.handlers.PostCreateContainer; handler != nil {
			handler(evt.Pod, evt.Container)
		}
	case api.Event_START_CONTAINER:
		if handler := stub.handlers.StartContainer; handler != nil {
			handler(evt.Pod, evt.Container)
		}
	case api.Event_POST_START_CONTAINER:
		if handler := stub.handlers.PostStartContainer; handler != nil {
			handler(evt.Pod, evt.Container)
		}
	case api.Event_POST_UPDATE_CONTAINER:
		if handler := stub.handlers.PostUpdateContainer; handler != nil {
			handler(evt.Pod, evt.Container)
		}
	case api.Event_REMOVE_CONTAINER:
		if handler := stub.handlers.RemoveContainer; handler != nil {
			handler(evt.Pod, evt.Container)
		}
	}

	return &api.StateChangeResponse{}, nil
}

// identityFromEnv tries to extract the plugin name and index from the environment.
func (stub *stub) identityFromEnv() error {
	stub.name = os.Getenv(api.PluginNameEnvVar)
	stub.idx = os.Getenv(api.PluginIdxEnvVar)

	if stub.name != "" && stub.idx == "" {
		return errors.New("invalid environment, plugin name set without plugin index")
	}

	if stub.name == "" && stub.idx != "" {
		return errors.New("invalid environment, plugin index set without plugin name")
	}

	return nil
}

// identityFromBinary tries to extract the plugin name and index from the binary name.
func (stub *stub) identityFromBinary() error {
	if stub.idx != "" {
		stub.name = filepath.Base(os.Args[0])
		return nil
	}

	idx, name, err := api.ParsePluginName(filepath.Base(os.Args[0]))
	if err != nil {
		return err
	}

	stub.name = name
	stub.idx = idx

	return nil
}

// Get the event handlers and subscription mask for a plugin.
func (stub *stub) setupHandlers() error {
	if plugin, ok := stub.plugin.(ConfigureInterface); ok {
		stub.handlers.Configure = plugin.Configure
	}
	if plugin, ok := stub.plugin.(SynchronizeInterface); ok {
		stub.handlers.Synchronize = plugin.Synchronize
	}
	if plugin, ok := stub.plugin.(ShutdownInterface); ok {
		stub.handlers.Shutdown = plugin.Shutdown
	}

	if plugin, ok := stub.plugin.(RunPodInterface); ok {
		stub.handlers.RunPodSandbox = plugin.RunPodSandbox
		stub.events.Set(api.Event_RUN_POD_SANDBOX)
	}
	if plugin, ok := stub.plugin.(StopPodInterface); ok {
		stub.handlers.StopPodSandbox = plugin.StopPodSandbox
		stub.events.Set(api.Event_STOP_POD_SANDBOX)
	}
	if plugin, ok := stub.plugin.(RemovePodInterface); ok {
		stub.handlers.RemovePodSandbox = plugin.RemovePodSandbox
		stub.events.Set(api.Event_REMOVE_POD_SANDBOX)
	}
	if plugin, ok := stub.plugin.(CreateContainerInterface); ok {
		stub.handlers.CreateContainer = plugin.CreateContainer
		stub.events.Set(api.Event_CREATE_CONTAINER)
	}
	if plugin, ok := stub.plugin.(StartContainerInterface); ok {
		stub.handlers.StartContainer = plugin.StartContainer
		stub.events.Set(api.Event_START_CONTAINER)
	}
	if plugin, ok := stub.plugin.(UpdateContainerInterface); ok {
		stub.handlers.UpdateContainer = plugin.UpdateContainer
		stub.events.Set(api.Event_UPDATE_CONTAINER)
	}
	if plugin, ok := stub.plugin.(StopContainerInterface); ok {
		stub.handlers.StopContainer = plugin.StopContainer
		stub.events.Set(api.Event_STOP_CONTAINER)
	}
	if plugin, ok := stub.plugin.(RemoveContainerInterface); ok {
		stub.handlers.RemoveContainer = plugin.RemoveContainer
		stub.events.Set(api.Event_REMOVE_CONTAINER)
	}
	if plugin, ok := stub.plugin.(PostCreateContainerInterface); ok {
		stub.handlers.PostCreateContainer = plugin.PostCreateContainer
		stub.events.Set(api.Event_POST_CREATE_CONTAINER)
	}
	if plugin, ok := stub.plugin.(PostStartContainerInterface); ok {
		stub.handlers.PostStartContainer = plugin.PostStartContainer
		stub.events.Set(api.Event_POST_START_CONTAINER)
	}
	if plugin, ok := stub.plugin.(PostUpdateContainerInterface); ok {
		stub.handlers.PostUpdateContainer = plugin.PostUpdateContainer
		stub.events.Set(api.Event_POST_UPDATE_CONTAINER)
	}

	if stub.events == 0 {
		return errors.New("internal error: plugin does not implement any NRI request handlers")
	}

	return nil
}
