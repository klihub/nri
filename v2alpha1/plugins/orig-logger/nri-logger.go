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
	"strconv"
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
	Logger
	rpcm    multiplex.Mux
	rpcl    stdnet.Listener
	rpcs    *ttrpc.Server
	rpcc    *ttrpc.Client
	runtime api.RuntimeService
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
				fmt.Sprintf("logger-with-pid-%d", os.Getpid()) : "was-here",
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
	p.Warn("connection to NRI/runtime lost, exiting...")
	p.rpcm.Close()
	p.rpcl.Close()
	p.rpcs.Close()
	p.rpcc.Close()
	os.Exit(0)
}

// create the logger plugin using the given connection.
func create(conn stdnet.Conn, log Logger) (*plugin, error) {
	var err error

	p := &plugin{
		Logger: log,
		rpcm:   multiplex.Multiplex(conn),
	}

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
				p.ttrpcClosed()
			}))
	p.runtime = api.NewRuntimeClient(p.rpcc)

	return p, nil
}

// run the plugin.
func (p *plugin) run(ctx context.Context) error {

	doneC := make(chan error)

	go func() {
		err := p.rpcs.Serve(ctx, p.rpcl)
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
	var (
		p    *plugin
		conn stdnet.Conn
		err  error
	)

	flag.Var(flags, "events", "comma-separated list of events to subscribe to")
	flag.Parse()

	log := LogWriter(os.Stdout).Get("nri-logger")

	if v := os.Getenv(api.PluginSocketEnvVar); v != "" {
		var fd int

		log.Info("running as NRI-launched plugin...")

		if flag.NFlag() + flag.NArg() > 0 {
			log.Fatal("NRI-launched plugin does not accept command line arguments")
		}

		fd, err = strconv.Atoi(v)
		if err != nil {
			log.Fatal("invalid socket in environment (%s=%q): %v",
				api.PluginSocketEnvVar, v, err)
		}
		conn, err = net.NewFdConn(fd)
		if err != nil {
			log.Fatal("failed to use socket (%d) in environment: %v", fd, err)
		}
	} else {
		var path string

		log.Info("running as external NRI plugin...")

		switch flag.NArg() {
		case 0:
			path = api.DefaultSocketPath
		case 1:
			path = flag.Args()[0]
		default:
			log.Fatal("invalid command line arguments %q",
				strings.Join(flag.Args(), " "))
		}

		conn, err = stdnet.Dial("unix", path)
		if err != nil {
			log.Fatal("failed to connect to socket %q: %v", path, err)
		}
	}

	p, err = create(conn, log)
	if err != nil {
		log.Fatal("failed to create logger plugin: %v", err)
	}

	err = p.run(context.Background())
	if err != nil {
		log.Fatal("failed to run plugin: %v", err)
	}
}
