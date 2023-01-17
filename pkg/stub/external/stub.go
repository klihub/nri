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
	"fmt"
	stdnet "net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
	nrilog "github.com/containerd/nri/pkg/log"
	"github.com/containerd/nri/pkg/net"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/nri/pkg/stub"
	"github.com/containerd/ttrpc"
)

// Plugin is an NRI plugin.
type Plugin = stub.Plugin

// ConfigureInterface handles Configure API request.
type ConfigureInterface interface {
	// Configure the plugin with the given NRI-supplied configuration.
	// If a non-zero EventMask is returned, the plugin will be subscribed
	// to the corresponding.
	Configure(config, runtime, version string) (api.EventMask, error)
}

// SynchronizeInterface handles Synchronize API requests.
type SynchronizeInterface interface {
	// Synchronize the state of the plugin with the runtime.
	// The plugin can request updates to containers in response.
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
	RunPodSandbox(*api.PodSandbox) error
}

// StopPodInterface handles StopPodSandbox API events.
type StopPodInterface interface {
	// StopPodSandbox relays a StopPodSandbox event to the plugin.
	StopPodSandbox(*api.PodSandbox) error
}

// RemovePodInterface handles RemovePodSandbox API events.
type RemovePodInterface interface {
	// RemovePodSandbox relays a RemovePodSandbox event to the plugin.
	RemovePodSandbox(*api.PodSandbox) error
}

// CreateContainerInterface handles CreateContainer API requests.
type CreateContainerInterface interface {
	// CreateContainer relays a CreateContainer request to the plugin.
	// The plugin can request adjustments to the container being created
	// and updates to other unstopped containers in response.
	CreateContainer(*api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
}

// StartContainerInterface handles StartContainer API requests.
type StartContainerInterface interface {
	// StartContainer relays a StartContainer event to the plugin.
	StartContainer(*api.PodSandbox, *api.Container) error
}

// UpdateContainerInterface handles UpdateContainer API requests.
type UpdateContainerInterface interface {
	// UpdateContainer relays an UpdateContainer request to the plugin.
	// The plugin can request updates both to the container being updated
	// (which then supersedes the original update) and to other unstopped
	// containers in response.
	UpdateContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// StopContainerInterface handles StopContainer API requests.
type StopContainerInterface interface {
	// StopContainer relays a StopContainer request to the plugin.
	// The plugin can request updates to unstopped containers in response.
	StopContainer(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
}

// RemoveContainerInterface handles RemoveContainer API events.
type RemoveContainerInterface interface {
	// RemoveContainer relays a RemoveContainer event to the plugin.
	RemoveContainer(*api.PodSandbox, *api.Container) error
}

// PostCreateContainerInterface handles PostCreateContainer API events.
type PostCreateContainerInterface interface {
	// PostCreateContainer relays a PostCreateContainer event to the plugin.
	PostCreateContainer(*api.PodSandbox, *api.Container) error
}

// PostStartContainerInterface handles PostStartContainer API events.
type PostStartContainerInterface interface {
	// PostStartContainer relays a PostStartContainer event to the plugin.
	PostStartContainer(*api.PodSandbox, *api.Container) error
}

// PostUpdateContainerInterface handles PostUpdateContainer API events.
type PostUpdateContainerInterface interface {
	// PostUpdateContainer relays a PostUpdateContainer event to the plugin.
	PostUpdateContainer(*api.PodSandbox, *api.Container) error
}

// Stub is the interface the stub provides for the plugin implementation.
type Stub interface {
	// Run the plugin. Starts the plugin then waits for an error or the plugin to stop
	Run(context.Context) error
	// Start the plugin.
	Start(context.Context) error
	// Stop the plugin.
	Stop()
	// Wait for the plugin to stop.
	Wait()

	// UpdateContainer requests unsolicited updates to containers.
	UpdateContainers([]*api.ContainerUpdate) ([]*api.ContainerUpdate, error)
}

const (
	// Plugin registration timeout.
	registrationTimeout = 2 * time.Second
)

var (
	// Logger for messages generated internally by the stub itself.
	log = nrilog.Get()

	// Used instead of a nil Context in logging.
	noCtx = context.TODO()
)

// EventMask holds a mask of events for plugin subscription.
type EventMask = api.EventMask

// Option to apply to a plugin during its creation.
type Option func(*external) error

// WithOnClose sets a notification function to call if the ttRPC connection goes down.
func WithOnClose(onClose func()) Option {
	return func(s *external) error {
		s.onClose = onClose
		return nil
	}
}

// WithPluginName sets the name to use in plugin registration.
func WithPluginName(name string) Option {
	return func(s *external) error {
		if s.name != "" {
			return fmt.Errorf("plugin name already set (%q)", s.name)
		}
		s.name = name
		return nil
	}
}

// WithPluginIdx sets the index to use in plugin registration.
func WithPluginIdx(idx string) Option {
	return func(s *external) error {
		if s.idx != "" {
			return fmt.Errorf("plugin ID already set (%q)", s.idx)
		}
		s.idx = idx
		return nil
	}
}

// WithSocketPath sets the NRI socket path to connect to.
func WithSocketPath(path string) Option {
	return func(s *external) error {
		s.socketPath = path
		return nil
	}
}

// WithConnection sets an existing NRI connection to use.
func WithConnection(conn stdnet.Conn) Option {
	return func(s *external) error {
		s.conn = conn
		return nil
	}
}

// WithDialer sets the dialer to use.
func WithDialer(d func(string) (stdnet.Conn, error)) Option {
	return func(s *external) error {
		s.dialer = d
		return nil
	}
}

// external implements and external Stub.
type external struct {
	sync.Mutex
	plugin     stub.Plugin
	handlers   handlers
	events     api.EventMask
	name       string
	idx        string
	socketPath string
	dialer     func(string) (stdnet.Conn, error)
	conn       stdnet.Conn
	onClose    func()
	rpcm       multiplex.Mux
	rpcl       stdnet.Listener
	rpcs       *ttrpc.Server
	rpcc       *ttrpc.Client
	runtime    api.RuntimeService
	closeOnce  sync.Once
	started    bool
	doneC      chan struct{}
	srvErrC    chan error
	cfgErrC    chan error
}

// Handlers for NRI plugin event and request.
type handlers struct {
	Configure           func(string, string, string) (api.EventMask, error)
	Synchronize         func([]*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error)
	Shutdown            func(*api.ShutdownRequest)
	RunPodSandbox       func(*api.PodSandbox) error
	StopPodSandbox      func(*api.PodSandbox) error
	RemovePodSandbox    func(*api.PodSandbox) error
	CreateContainer     func(*api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
	StartContainer      func(*api.PodSandbox, *api.Container) error
	UpdateContainer     func(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	StopContainer       func(*api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	RemoveContainer     func(*api.PodSandbox, *api.Container) error
	PostCreateContainer func(*api.PodSandbox, *api.Container) error
	PostStartContainer  func(*api.PodSandbox, *api.Container) error
	PostUpdateContainer func(*api.PodSandbox, *api.Container) error
}

// New creates a stub with the given plugin and options.
func New(p interface{}, opts ...Option) (Stub, error) {
	s := &external{
		plugin:     p,
		name:       os.Getenv(api.PluginNameEnvVar),
		idx:        os.Getenv(api.PluginIdxEnvVar),
		socketPath: api.DefaultSocketPath,
		dialer:     func(p string) (stdnet.Conn, error) { return stdnet.Dial("unix", p) },
		doneC:      make(chan struct{}),
	}

	for _, o := range opts {
		if err := o(s); err != nil {
			return nil, err
		}
	}

	if err := s.setupHandlers(); err != nil {
		return nil, err
	}

	if err := s.getIdentity(); err != nil {
		return nil, err
	}

	log.Infof(noCtx, "Created plugin %s (%s, handles %s)", s.Name(),
		filepath.Base(os.Args[0]), s.events.PrettyString())

	return s, nil
}

// Start event processing, register to NRI and wait for getting configured.
func (s *external) Start(ctx context.Context) (retErr error) {
	s.Lock()
	defer s.Unlock()

	if s.started {
		return fmt.Errorf("stub already started")
	}
	s.started = true

	err := s.connect()
	if err != nil {
		return err
	}

	rpcm := multiplex.Multiplex(s.conn)
	defer func() {
		if retErr != nil {
			rpcm.Close()
			s.rpcm = nil
		}
	}()

	rpcl, err := rpcm.Listen(multiplex.PluginServiceConn)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			rpcl.Close()
			s.rpcl = nil
		}
	}()

	rpcs, err := ttrpc.NewServer()
	if err != nil {
		return fmt.Errorf("failed to create ttrpc server: %w", err)
	}
	defer func() {
		if retErr != nil {
			rpcs.Close()
			s.rpcs = nil
		}
	}()

	api.RegisterPluginService(rpcs, s)

	conn, err := rpcm.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		return fmt.Errorf("failed to multiplex ttrpc client connection: %w", err)
	}
	rpcc := ttrpc.NewClient(conn,
		ttrpc.WithOnClose(func() {
			s.connClosed()
		}),
	)
	defer func() {
		if retErr != nil {
			rpcc.Close()
			s.rpcc = nil
		}
	}()

	s.srvErrC = make(chan error, 1)
	s.cfgErrC = make(chan error, 1)
	go func() {
		s.srvErrC <- rpcs.Serve(ctx, rpcl)
		close(s.doneC)
	}()

	s.rpcm = rpcm
	s.rpcl = rpcl
	s.rpcs = rpcs
	s.rpcc = rpcc

	s.runtime = api.NewRuntimeClient(rpcc)

	if err = s.register(ctx); err != nil {
		s.close()
		return err
	}

	if err = <-s.cfgErrC; err != nil {
		return err
	}

	log.Infof(ctx, "Started plugin %s...", s.Name())

	return nil
}

// Stop the plugin.
func (s *external) Stop() {
	log.Infof(noCtx, "Stopping plugin %s...", s.Name())

	s.Lock()
	defer s.Unlock()
	s.close()
}

func (s *external) close() {
	s.closeOnce.Do(func() {
		if s.rpcl != nil {
			s.rpcl.Close()
		}
		if s.rpcs != nil {
			s.rpcs.Close()
		}
		if s.rpcc != nil {
			s.rpcc.Close()
		}
		if s.rpcm != nil {
			s.rpcm.Close()
		}
		if s.srvErrC != nil {
			<-s.doneC
		}
	})
}

// Run the plugin. Start event processing then wait for an error or getting stopped.
func (s *external) Run(ctx context.Context) error {
	var err error

	if err = s.Start(ctx); err != nil {
		return err
	}

	err = <-s.srvErrC
	if err == ttrpc.ErrServerClosed {
		return nil
	}

	return err
}

// Wait for the plugin to stop.
func (s *external) Wait() {
	s.Lock()
	if s.srvErrC == nil {
		return
	}
	s.Unlock()
	<-s.doneC
}

// Name returns the full indexed name of the plugin.
func (s *external) Name() string {
	return s.idx + "-" + s.name
}

// Connect the plugin to NRI.
func (s *external) connect() error {
	if s.conn != nil {
		log.Infof(noCtx, "Using given plugin connection...")
		return nil
	}

	if env := os.Getenv(api.PluginSocketEnvVar); env != "" {
		log.Infof(noCtx, "Using connection %q from environment...", env)

		fd, err := strconv.Atoi(env)
		if err != nil {
			return fmt.Errorf("invalid socket in environment (%s=%q): %w",
				api.PluginSocketEnvVar, env, err)
		}

		s.conn, err = net.NewFdConn(fd)
		if err != nil {
			return fmt.Errorf("invalid socket (%d) in environment: %w", fd, err)
		}

		return nil
	}

	conn, err := s.dialer(s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to NRI service: %w", err)
	}

	s.conn = conn

	return nil
}

// Register the plugin with NRI.
func (s *external) register(ctx context.Context) error {
	log.Infof(ctx, "Registering plugin %s...", s.Name())

	ctx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()

	req := &api.RegisterPluginRequest{
		PluginName: s.name,
		PluginIdx:  s.idx,
	}
	if _, err := s.runtime.RegisterPlugin(ctx, req); err != nil {
		return fmt.Errorf("failed to register with NRI/Runtime: %w", err)
	}

	return nil
}

// Handle a lost connection.
func (s *external) connClosed() {
	s.close()
	if s.onClose != nil {
		s.onClose()
		return
	}

	os.Exit(0)
}

//
// plugin event and request handlers
//

// UpdateContainers requests unsolicited updates to containers.
func (s *external) UpdateContainers(update []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	ctx := context.Background()
	req := &api.UpdateContainersRequest{
		Update: update,
	}
	rpl, err := s.runtime.UpdateContainers(ctx, req)
	if rpl != nil {
		return rpl.Failed, err
	}
	return nil, err
}

// Configure the plugin.
func (s *external) Configure(ctx context.Context, req *api.ConfigureRequest) (rpl *api.ConfigureResponse, retErr error) {
	var (
		events api.EventMask
		err    error
	)

	log.Infof(ctx, "Configuring plugin %s for runtime %s/%s...", s.Name(),
		req.RuntimeName, req.RuntimeVersion)

	defer func() {
		s.cfgErrC <- retErr
	}()

	if handler := s.handlers.Configure; handler == nil {
		events = s.events
	} else {
		events, err = handler(req.Config, req.RuntimeName, req.RuntimeVersion)
		if err != nil {
			log.Errorf(ctx, "Plugin configuration failed: %v", err)
			return nil, err
		}

		if events == 0 {
			events = s.events
		}

		// Only allow plugins to subscribe to events they can handle.
		if extra := events & ^s.events; extra != 0 {
			log.Errorf(ctx, "Plugin subscribed for unhandled events %s (0x%x)",
				extra.PrettyString(), extra)
			return nil, fmt.Errorf("internal error: unhandled events %s (0x%x)",
				extra.PrettyString(), extra)
		}

		log.Infof(ctx, "Subscribing plugin %s (%s) for events %s", s.Name(),
			filepath.Base(os.Args[0]), events.PrettyString())
	}

	return &api.ConfigureResponse{
		Events: int32(events),
	}, nil
}

// Synchronize the state of the plugin with the runtime.
func (s *external) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	handler := s.handlers.Synchronize
	if handler == nil {
		return &api.SynchronizeResponse{}, nil
	}
	update, err := handler(req.Pods, req.Containers)
	return &api.SynchronizeResponse{
		Update: update,
	}, err
}

// Shutdown the plugin.
func (s *external) Shutdown(ctx context.Context, req *api.ShutdownRequest) (*api.ShutdownResponse, error) {
	handler := s.handlers.Shutdown
	if handler != nil {
		handler(req)
	}
	return &api.ShutdownResponse{}, nil
}

// CreateContainer request handler.
func (s *external) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	handler := s.handlers.CreateContainer
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
func (s *external) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	handler := s.handlers.UpdateContainer
	if handler == nil {
		return nil, nil
	}
	update, err := handler(req.Pod, req.Container)
	return &api.UpdateContainerResponse{
		Update: update,
	}, err
}

// StopContainer request handler.
func (s *external) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	handler := s.handlers.StopContainer
	if handler == nil {
		return nil, nil
	}
	update, err := handler(req.Pod, req.Container)
	return &api.StopContainerResponse{
		Update: update,
	}, err
}

// StateChange event handler.
func (s *external) StateChange(ctx context.Context, evt *api.StateChangeEvent) (*api.Empty, error) {
	var err error
	switch evt.Event {
	case api.Event_RUN_POD_SANDBOX:
		if handler := s.handlers.RunPodSandbox; handler != nil {
			err = handler(evt.Pod)
		}
	case api.Event_STOP_POD_SANDBOX:
		if handler := s.handlers.StopPodSandbox; handler != nil {
			err = handler(evt.Pod)
		}
	case api.Event_REMOVE_POD_SANDBOX:
		if handler := s.handlers.RemovePodSandbox; handler != nil {
			err = handler(evt.Pod)
		}
	case api.Event_POST_CREATE_CONTAINER:
		if handler := s.handlers.PostCreateContainer; handler != nil {
			err = handler(evt.Pod, evt.Container)
		}
	case api.Event_START_CONTAINER:
		if handler := s.handlers.StartContainer; handler != nil {
			err = handler(evt.Pod, evt.Container)
		}
	case api.Event_POST_START_CONTAINER:
		if handler := s.handlers.PostStartContainer; handler != nil {
			err = handler(evt.Pod, evt.Container)
		}
	case api.Event_POST_UPDATE_CONTAINER:
		if handler := s.handlers.PostUpdateContainer; handler != nil {
			err = handler(evt.Pod, evt.Container)
		}
	case api.Event_REMOVE_CONTAINER:
		if handler := s.handlers.RemoveContainer; handler != nil {
			err = handler(evt.Pod, evt.Container)
		}
	}

	return &api.StateChangeResponse{}, err
}

// getIdentity gets plugin index and name from the binary if those are unset.
func (s *external) getIdentity() error {
	if s.idx != "" && s.name != "" {
		return nil
	}

	if s.idx != "" {
		s.name = filepath.Base(os.Args[0])
		return nil
	}

	idx, name, err := api.ParsePluginName(filepath.Base(os.Args[0]))
	if err != nil {
		return err
	}

	s.name = name
	s.idx = idx

	return nil
}

// Set up event handlers and the subscription mask for the plugin.
func (s *external) setupHandlers() error {
	if plugin, ok := s.plugin.(ConfigureInterface); ok {
		s.handlers.Configure = plugin.Configure
	}
	if plugin, ok := s.plugin.(SynchronizeInterface); ok {
		s.handlers.Synchronize = plugin.Synchronize
	}
	if plugin, ok := s.plugin.(ShutdownInterface); ok {
		s.handlers.Shutdown = plugin.Shutdown
	}

	if plugin, ok := s.plugin.(RunPodInterface); ok {
		s.handlers.RunPodSandbox = plugin.RunPodSandbox
		s.events.Set(api.Event_RUN_POD_SANDBOX)
	}
	if plugin, ok := s.plugin.(StopPodInterface); ok {
		s.handlers.StopPodSandbox = plugin.StopPodSandbox
		s.events.Set(api.Event_STOP_POD_SANDBOX)
	}
	if plugin, ok := s.plugin.(RemovePodInterface); ok {
		s.handlers.RemovePodSandbox = plugin.RemovePodSandbox
		s.events.Set(api.Event_REMOVE_POD_SANDBOX)
	}
	if plugin, ok := s.plugin.(CreateContainerInterface); ok {
		s.handlers.CreateContainer = plugin.CreateContainer
		s.events.Set(api.Event_CREATE_CONTAINER)
	}
	if plugin, ok := s.plugin.(StartContainerInterface); ok {
		s.handlers.StartContainer = plugin.StartContainer
		s.events.Set(api.Event_START_CONTAINER)
	}
	if plugin, ok := s.plugin.(UpdateContainerInterface); ok {
		s.handlers.UpdateContainer = plugin.UpdateContainer
		s.events.Set(api.Event_UPDATE_CONTAINER)
	}
	if plugin, ok := s.plugin.(StopContainerInterface); ok {
		s.handlers.StopContainer = plugin.StopContainer
		s.events.Set(api.Event_STOP_CONTAINER)
	}
	if plugin, ok := s.plugin.(RemoveContainerInterface); ok {
		s.handlers.RemoveContainer = plugin.RemoveContainer
		s.events.Set(api.Event_REMOVE_CONTAINER)
	}
	if plugin, ok := s.plugin.(PostCreateContainerInterface); ok {
		s.handlers.PostCreateContainer = plugin.PostCreateContainer
		s.events.Set(api.Event_POST_CREATE_CONTAINER)
	}
	if plugin, ok := s.plugin.(PostStartContainerInterface); ok {
		s.handlers.PostStartContainer = plugin.PostStartContainer
		s.events.Set(api.Event_POST_START_CONTAINER)
	}
	if plugin, ok := s.plugin.(PostUpdateContainerInterface); ok {
		s.handlers.PostUpdateContainer = plugin.PostUpdateContainer
		s.events.Set(api.Event_POST_UPDATE_CONTAINER)
	}

	if s.events == 0 {
		return fmt.Errorf("internal error: plugin %T does not implement any NRI request handlers",
			s.plugin)
	}

	return nil
}
