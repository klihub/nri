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
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"

	"github.com/container-orchestrated-devices/container-device-interface/pkg/cdi"
	rspec "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/stub"
)

const (
	// Annotation value used for CDI devices by CDI Device Plugin.
	cdiDeviceAnnotation = "CDI_Device"
)

var (
	log     *logrus.Logger
	verbose bool
)

type plugin struct {
	stub stub.Stub
}

// CreateContainer handles container creation requests.
func (p *plugin) CreateContainer(pod *api.PodSandbox, container *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	var (
		registry   = cdi.GetRegistry()
		annotated  []string
		cdiDevices []string
		unresolved []string
		err        error
	)

	ctrName := containerName(pod, container)

	if verbose {
		dump("CreateContainer", "pod", pod, "container", container)
	}

	if container.Linux == nil || len(container.Linux.Devices) == 0 {
		log.Infof("%s: no devices, ignoring...", ctrName)
		return nil, nil, nil
	}

	annotated = []string{}
	for k, v := range container.Annotations {
		if v == cdiDeviceAnnotation {
			annotated = append(annotated, k)
		}
	}

	log.Infof("annotated CDI Devices: %s", strings.Join(annotated, ", "))

	registry.Refresh()
	for _, devRef := range annotated {
		var (
			vendor string
			name   string
		)
		split := strings.SplitN(devRef, "/", 2)
		if len(split) != 2 {
			return nil, nil, errors.Errorf("malformed CDI device annotation %q", devRef)
		}
		vendor, name = split[0], split[1]

		for _, device := range registry.DeviceDB().ListDevices() {
			if match, _ := filepath.Match(vendor+"/*="+name, device); match {
				cdiDevices = append(cdiDevices, device)
			}
		}
	}

	log.Infof("resolved to CDI Devices: %s", strings.Join(cdiDevices, ", "))

	ociSpec := &rspec.Spec{
		Process: &rspec.Process{
			Args: container.Args,
		},
	}

	unresolved, err = registry.InjectDevices(ociSpec, cdiDevices...)

	if len(unresolved) != 0 {
		log.Errorf("unresolved CDI devices: %s", strings.Join(unresolved, ", "))
	}
	if err != nil {
		return nil, nil, err
	}

	adjust := &api.ContainerAdjustment{
		Mounts: api.FromOCIMounts(ociSpec.Mounts),
		Env:    api.FromOCIEnv(ociSpec.Process.Env),
		Hooks:  api.FromOCIHooks(ociSpec.Hooks),
	}

	for _, path := range annotated {
		adjust.RemoveDevice(path)
		if !verbose {
			log.Infof("%s: removed virtual CDI device %q...", ctrName, path)
		}
	}

	for _, d := range api.FromOCILinuxDevices(ociSpec.Linux.Devices) {
		adjust.AddDevice(d)
		if !verbose {
			log.Infof("%s: injected resolved CDI device %q...", ctrName, d.Path)
		}
	}

	if verbose {
		dump(ctrName, "ContainerAdjustment", adjust)
	}

	return adjust, nil, nil
}

// Construct a container name for log messages.
func containerName(pod *api.PodSandbox, container *api.Container) string {
	if pod != nil {
		return pod.Name + "/" + container.Name
	}
	return container.Name
}

// Dump one or more objects, with an optional global prefix and per-object tags.
func dump(args ...interface{}) {
	var (
		prefix string
		idx    int
	)

	if len(args)&0x1 == 1 {
		prefix = args[0].(string)
		idx++
	}

	for ; idx < len(args)-1; idx += 2 {
		tag, obj := args[idx], args[idx+1]
		msg, err := yaml.Marshal(obj)
		if err != nil {
			log.Infof("%s: %s: failed to dump object: %v", prefix, tag, err)
			continue
		}

		if prefix != "" {
			log.Infof("%s: %s:", prefix, tag)
			for _, line := range strings.Split(strings.TrimSpace(string(msg)), "\n") {
				log.Infof("%s:    %s", prefix, line)
			}
		} else {
			log.Infof("%s:", tag)
			for _, line := range strings.Split(strings.TrimSpace(string(msg)), "\n") {
				log.Infof("  %s", line)
			}
		}
	}
}

func main() {
	var (
		pluginName string
		pluginIdx  string
		opts       []stub.Option
		err        error
	)

	log = logrus.StandardLogger()
	log.SetFormatter(&logrus.TextFormatter{
		PadLevelText: true,
	})

	flag.StringVar(&pluginName, "name", "", "plugin name to register to NRI")
	flag.StringVar(&pluginIdx, "idx", "", "plugin index to register to NRI")
	flag.BoolVar(&verbose, "verbose", false, "enable (more) verbose logging")
	flag.Parse()

	if pluginName != "" {
		opts = append(opts, stub.WithPluginName(pluginName))
	}
	if pluginIdx != "" {
		opts = append(opts, stub.WithPluginIdx(pluginIdx))
	}

	p := &plugin{}
	if p.stub, err = stub.New(p, opts...); err != nil {
		log.Fatalf("failed to create plugin stub: %v", err)
	}

	err = p.stub.Run(context.Background())
	if err != nil {
		log.Errorf("plugin exited with error %v", err)
		os.Exit(1)
	}
}
