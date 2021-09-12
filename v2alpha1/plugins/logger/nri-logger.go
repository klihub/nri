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

package main

import (
	"context"
	"flag"
	"fmt"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/pkg/errors"
	"github.com/containerd/ttrpc"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/net"
	"github.com/containerd/nri/v2alpha1/pkg/net/multiplex"
)

// config data for our logger plugin.
type config struct {
	LogFile string   `json:"logFile"`
	Events  []string `json:"events"`
}

// plugin is a logger for NRI events.
type plugin struct {
	listener stdnet.Listener
	server   *ttrpc.Server
	Logger
	ttrpcc   *ttrpc.Client
	runtime  api.RuntimeService
}

// configuration specified on the command line.
var flags *config = &config{}

func (p *plugin) Configure(ctx context.Context, req *api.ConfigureRequest) (*api.ConfigureResponse, error) {
	cfg := config{}

	if req.Config != "" {
		p.Error("parsing configuration %q...", req.Config)
		err := yaml.Unmarshal([]byte(req.Config), &cfg)
		if err != nil {
			p.Error("failed to parse configuration: %v", err)
			return nil, errors.Wrap(err, "invalid configuration")
		}
		*flags = cfg
	} else {
		cfg.Events = flags.Events
	}

	rpl := &api.ConfigureResponse{}

	events := map[string]api.Event{
		"runpodsandbox":    api.Event_RUN_POD_SANDBOX,
		"stoppodsandbox":   api.Event_STOP_POD_SANDBOX,
		"removepodsandbox": api.Event_REMOVE_POD_SANDBOX,
		"createcontainer":  api.Event_CREATE_CONTAINER,
		"startcontainer":   api.Event_START_CONTAINER,
		"updatecontainer":  api.Event_UPDATE_CONTAINER,
		"stopcontainer":    api.Event_STOP_CONTAINER,
		"removecontainer":  api.Event_REMOVE_CONTAINER,
		"all":              api.Event_ALL,
	}
	for _, name := range cfg.Events {
		e, ok := events[strings.ToLower(name)]
		if !ok {
			return nil, errors.Errorf("invalid event %q", name)
		}
		rpl.Subscribe = append(rpl.Subscribe, e)
	}
	if len(rpl.Subscribe) < 1 {
		rpl.Subscribe = []api.Event{ api.Event_ALL }
	}

	if cfg.LogFile != "" {
		w, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to open log file %q", cfg.LogFile)
		}
		p.SetWriter(w)
	}

	return rpl, nil
}

func (p *plugin) Register(name, id string) error {
	p.Info("registering plugin...")

	ctx := context.Background()
	_, err := p.runtime.RegisterPlugin(ctx, &api.RegisterPluginRequest{
		PluginName: name,
		PluginId:   id,
	})

	return err
}

func (p *plugin) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	p.dump(req)

	return &api.SynchronizeResponse{}, nil
}

func (p *plugin) Shutdown(ctx context.Context, req *api.ShutdownRequest) (*api.ShutdownResponse, error) {
	return &api.ShutdownResponse{}, nil
}

func (p *plugin) RunPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) (*api.RunPodSandboxResponse, error) {
	p.dump(req)
	return &api.RunPodSandboxResponse{}, nil
}

func (p *plugin) StopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) (*api.StopPodSandboxResponse, error) {
	p.dump(req)
	return &api.StopPodSandboxResponse{}, nil
}

func (p *plugin) RemovePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) (*api.RemovePodSandboxResponse, error) {
	p.dump(req)
	return &api.RemovePodSandboxResponse{}, nil
}

func (p *plugin) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	p.dump(req)
	return &api.CreateContainerResponse{
		Create: &api.ContainerCreateAdjustment{
			Labels: map[string]string{
				fmt.Sprintf("logger%d", os.Getpid()) : "was-here",
			},
		},
	}, nil

}

func (p *plugin) PostCreateContainer(ctx context.Context, req *api.PostCreateContainerRequest) (*api.PostCreateContainerResponse, error) {
	p.dump(req)
	return &api.PostCreateContainerResponse{}, nil
}

func (p *plugin) StartContainer(ctx context.Context, req *api.StartContainerRequest) (*api.StartContainerResponse, error) {
	p.dump(req)
	return &api.StartContainerResponse{}, nil
}

func (p *plugin) PostStartContainer(ctx context.Context, req *api.PostStartContainerRequest) (*api.PostStartContainerResponse, error) {
	p.dump(req)
	return &api.PostStartContainerResponse{}, nil
}

func (p *plugin) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	p.dump(req)
	return &api.UpdateContainerResponse{}, nil
}

func (p *plugin) PostUpdateContainer(ctx context.Context, req *api.PostUpdateContainerRequest) (*api.PostUpdateContainerResponse, error) {
	p.dump(req)
	return &api.PostUpdateContainerResponse{}, nil
}

func (p *plugin) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	p.dump(req)
	return &api.StopContainerResponse{}, nil
}

func (p *plugin) RemoveContainer(ctx context.Context, req *api.RemoveContainerRequest) (*api.RemoveContainerResponse, error) {
	p.dump(req)
	return &api.RemoveContainerResponse{}, nil
}

func (p *plugin) ttrpcClosed() {
	p.Info("connection to NRI/runtime lost...")
	p.listener.Close()
	p.server.Close()
	os.Exit(0)
}

// create a plugin using a pre-connected socketpair.
func create(sockFd int, l Logging) (*plugin, error) {
	server, err := ttrpc.NewServer()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ttrpc server")
	}

	conn, err := net.NewFdConn(sockFd)
	if err != nil {
		return nil, err
	}

	mux := multiplex.Multiplex(conn)
	listener, err := mux.Listen(multiplex.PluginServiceConn)
	if err != nil {
		mux.Close()
		return nil, err
	}

	p := &plugin{
		listener: listener,
		server:   server,
		Logger:   l.Get("plugin"),
	}

	sconn, err := mux.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		listener.Close()
		mux.Close()
		return nil, err
	}

	p.ttrpcc = ttrpc.NewClient(sconn,
		ttrpc.WithOnClose(
			func () {
				p.ttrpcClosed()
			}))
	p.runtime = api.NewRuntimeClient(p.ttrpcc)

	return p, nil
}

// connect to the given NRI socket and create a plugin.
func connect(path string, l Logging) (*plugin, error) {
	conn, err := stdnet.Dial("unix", path)
	if err != nil {
		return nil, err
	}

	server, err := ttrpc.NewServer()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ttrpc server")
	}

	mux := multiplex.Multiplex(conn)
	listener, err := mux.Listen(multiplex.PluginServiceConn)
	if err != nil {
		listener.Close()
		mux.Close()
		return nil, errors.Wrap(err, "failed to create mux listener")
	}

	sconn, err := mux.Open(multiplex.RuntimeServiceConn)
	if err != nil {
		listener.Close()
		mux.Close()
		return nil, errors.Wrap(err, "failed to open mux connection")
	}

	p := &plugin{
		listener: listener,
		server:   server,
		Logger:   l.Get("plugin"),
	}

	p.ttrpcc = ttrpc.NewClient(sconn,
		ttrpc.WithOnClose(
			func () {
				p.ttrpcClosed()
			}))
	p.runtime = api.NewRuntimeClient(p.ttrpcc)
	api.RegisterPluginService(p.server, p)

	return p, nil
}

// run the plugin.
func (p *plugin) run(ctx context.Context) error {

	doneC := make(chan error)

	go func() {
		err := p.server.Serve(ctx, p.listener)
		doneC <- err
	}()

	name := os.Getenv("NRI_PLUGIN_NAME")
	if name == "" {
		name = filepath.Base(os.Args[0])
	}
	id := os.Getenv("NRI_PLUGIN_ID")
	if id == "" {
		id = "00"
	}

	if err := p.Register(name, id); err != nil {
		return err
	}

	err := <- doneC

	return err
}

// dump a message.
func (p *plugin) dump(obj interface{}) {
	msg, err := yaml.Marshal(obj)
	if err != nil {
		return
	}
	prefix := ""+strings.TrimPrefix(fmt.Sprintf("%T", obj), "*api.")
	prefix = strings.TrimSuffix(prefix, "Request")
	prefix = strings.TrimSuffix(prefix, "Event") + ": "
	p.InfoBlock(prefix, "%s", msg)
}

func (c *config) String() string {
	return strings.Join(c.Events, ",")
}

func (c *config) Set(value string) error {
	valid := map[string]struct{}{
		"runpodsandbox":       struct{}{},
		"stoppodsandbox":      struct{}{},
		"removepodsandbox":    struct{}{},
		"createcontainer":     struct{}{},
		"postcreatecontainer": struct{}{},
		"startcontainer":      struct{}{},
		"poststartcontainer":  struct{}{},
		"updatecontainer":     struct{}{},
		"postupdatecontainer": struct{}{},
		"stopcontainer":       struct{}{},
		"removecontainer":     struct{}{},
		"all":                 struct{}{},
	}

	for _, event := range strings.Split(value, ",") {
		e := strings.ToLower(event)
		if _, ok := valid[e]; !ok {
			return fmt.Errorf("invalid event '%s'", event)
		}
		c.Events = append(c.Events, e)
	}

	return nil
}

func main() {
	var p *plugin

	flag.Var(flags, "events", "comma-separated list of events to subscribe to")
	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		logFile, err := os.OpenFile("/tmp/nri-logger.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			os.Exit(1)
		}

		sockFd := 3

		p, err = create(sockFd, LogWriter(logFile))
		if err != nil {
			fmt.Printf("failed to set up plugin: %v\n", err)
			os.Exit(1)
		}
	} else {
		var err error

		p, err = connect(args[0], LogWriter(os.Stdout))
		if err != nil {
			fmt.Printf("failed to connect to NRI server: %v\n", err)
			os.Exit(1)
		}
	}

	if err := p.run(context.Background()); err != nil && err != ttrpc.ErrServerClosed {
		p.Info("plugin exited with error %v\n", err)
		os.Exit(1)
	}
}
