/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an Sub"AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"os"
	"sort"
	"strings"
	"sigs.k8s.io/yaml"

	"github.com/pkg/errors"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/stub"
)

type config struct {
	LogFile string   `json:"logFile"`
	Events  []string `json:"events"`
}

type plugin struct {
	stub stub.Stub
	Logger
}

func (p *plugin) Configure(nriCfg string) (stub.SubscribeMask, error) {
	if nriCfg == "" {
		return optSubscribe.Mask(), nil
	}

	cfg := config{}
	err := yaml.Unmarshal([]byte(nriCfg), &cfg)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse provided configuration")
	}

	mask := stub.SubscribeMask(0)
	for _, name := range cfg.Events {
		m, ok := eventMask[strings.ToLower(name)]
		if !ok {
			return 0, errors.Errorf("invalid event name %q in configuration", name)
		}
		mask |= m
	}

	return mask, nil
}

func (p *plugin) Synchronize(pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerAdjustment, error) {
	p.dump("Synchronize", "pods", pods, "containers", containers)
	return nil, nil
}

func (p *plugin) Shutdown() {
	p.dump("Shutdown")
}

func (p *plugin) RunPodSandbox(pod *api.PodSandbox) {
	p.dump("RunPodSandbox", "pod", pod)
}

func (p *plugin) StopPodSandbox(pod *api.PodSandbox) {
	p.dump("StopPodSandbox", "pod", pod)
}

func (p *plugin) RemovePodSandbox(pod *api.PodSandbox) {
	p.dump("RemovePodSandbox", "pod", pod)
}

func (p *plugin) CreateContainer(pod *api.PodSandbox, container *api.Container) (*api.ContainerCreateAdjustment, []*api.ContainerAdjustment, error) {
	p.dump("CreateContainer", "pod", pod, "container", container)
	return nil, nil, nil
}

func (p *plugin) PostCreateContainer(pod *api.PodSandbox, container *api.Container) {
	p.dump("PostCreateContainer", "pod", pod, "container", container)
}

func (p *plugin) StartContainer(pod *api.PodSandbox, container *api.Container) {
	p.dump("StartContainer", "pod", pod, "container", container)
}

func (p *plugin) PostStartContainer(pod *api.PodSandbox, container *api.Container) {
	p.dump("PostStartContainer", "pod", pod, "container", container)
}

func (p *plugin) UpdateContainer(pod *api.PodSandbox, container *api.Container) ([]*api.ContainerAdjustment, error) {
	p.dump("UpdateContainer", "pod", pod, "container", container)
	return nil, nil
}

func (p *plugin) PostUpdateContainer(pod *api.PodSandbox, container *api.Container) {
	p.dump("PostUpdateContainer", "pod", pod, "container", container)
}

func (p *plugin) StopContainer(pod *api.PodSandbox, container *api.Container) ([]*api.ContainerAdjustment, error) {
	p.dump("StopContainer", "pod", pod, "container", container)
	return nil, nil
}

func (p *plugin) RemoveContainer(pod *api.PodSandbox, container *api.Container) {
	p.dump("RemoveContainer", "pod", pod, "container", container)
}

// dump a message.
func (p *plugin) dump(msgType string, args ...interface{}) {
	if len(args) & 0x1 == 1 {
		p.Fatal("invalid dump: no argument for tag %q", args[len(args)-1])
	}

	for {
		tag := args[0]
		val := args[1]
		msg, err := yaml.Marshal(val)
		if err != nil {
			p.Warn("failed to marshal object %q: %v", tag, err)
			continue
		}

		p.Info("%s: %s:", msgType, tag)
		prefix := msgType+":   "
		p.InfoBlock(prefix, "%s", msg)

		args = args[2:]
		if len(args) == 0 {
			break
		}
	}
}

func (p *plugin) onClose() {
	os.Exit(0)
}

type nameOption string
type idOption string
type subscribeOption stub.SubscribeMask

func (o *nameOption) String() string {
	return string(*o)
}

func (o *nameOption) Set(value string) error {
	*o = nameOption(value)
	return nil
}

func (o *idOption) String() string {
	return string(*o)
}

func (o *idOption) Set(value string) error {
	*o = idOption(value)
	return nil
}

func (o *subscribeOption) Mask() stub.SubscribeMask {
	return stub.SubscribeMask(*o)
}

func (o *subscribeOption) String() string {
	var events []string
	for name, mask := range eventMask {
		if name == "pods" || name == "containers" || name == "all" {
			continue
		}
		if o.Mask() & mask != 0 {
			events = append(events, name)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		return eventMask[events[i]] < eventMask[events[j]]
	})

	return strings.Join(events, ",")
}

func (o *subscribeOption) Set(value string) error {
	var mask stub.SubscribeMask
	for _, name := range strings.Split(strings.ToLower(value), ",") {
		m, ok := eventMask[name]
		if !ok {
			return errors.Errorf("invalid event name %q", name)
		}
		mask |= m
	}
	*o = subscribeOption(mask)
	return nil
}

var eventMask = map[string]stub.SubscribeMask{
	"runpodsandbox":       stub.RunPodSandbox,
	"stoppodsandbox":      stub.StopPodSandbox,
	"removepodsandbox":    stub.RemovePodSandbox,
	"createcontainer":     stub.CreateContainer,
	"postcreatecontainer": stub.PostCreateContainer,
	"startcontainer":      stub.StartContainer,
	"poststartcontainer":  stub.PostStartContainer,
	"updatecontainer":     stub.UpdateContainer,
	"postupdatecontainer": stub.PostUpdateContainer,
	"stopcontainer":       stub.StopContainer,
	"removecontainer":     stub.RemoveContainer,
	"all":                 stub.AllEvents,

	"pods": stub.RunPodSandbox | stub.StopPodSandbox | stub.RemovePodSandbox,
	"containers": stub.CreateContainer | stub.PostCreateContainer |
		stub.StartContainer | stub.PostStartContainer |
		stub.UpdateContainer | stub.PostUpdateContainer |
		stub.StopContainer |
		stub.RemoveContainer,
}

var (
	optSubscribe subscribeOption
	optName nameOption
	optID idOption
)

func main() {
	flag.Var(&optSubscribe, "events", "comma-separated list of events to subscribe for")
	flag.Var(&optName, "name", "plugin name to register")
	flag.Var(&optID, "id", "plugin ID (two-letter sorting order) to register")
	flag.Parse()

	p := &plugin{
		Logger: LogWriter(os.Stdout).Get("nri-logger"),
	}

	opts := []stub.Option{ stub.WithOnClose(p.onClose) }
	if optName.String() != "" {
		opts = append(opts, stub.WithPluginName(optName.String()))
	}
	if optID.String() != "" {
		opts = append(opts, stub.WithPluginID(optID.String()))
	}

	s, err := stub.New(p, opts...)
	if err != nil {
		p.Fatal("failed to create plugin stub: %v", err)
	}
	p.stub = s

	err = p.stub.Run(context.Background())
	if err != nil {
		p.Error("Plugin exited with error %v", err)
	}
}
