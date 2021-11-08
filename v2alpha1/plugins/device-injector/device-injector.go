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
	"strings"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/stub"
)

const (
	// Prefix of the key used for device annotations.
	deviceKey = "devices.nri.io"
)

var (
	log     *logrus.Logger
	verbose bool
)

// an annotated device
type device struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Major    int64  `json:"major"`
	Minor    int64  `json:"minor"`
	FileMode uint32 `json:"file_mode"`
	UID      uint32 `json:"uid"`
	GID      uint32 `json:"gid"`
}

// our test device injector plugin
type plugin struct {
	stub stub.Stub
}

// CreateContainer handles container creation requests.
func (p *plugin) CreateContainer(pod *api.PodSandbox, container *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	var (
		ctrName    string
		key        string
		annotation []byte
		devices    []device
	)

	ctrName = containerName(pod, container)

	if verbose {
		dump("CreateContainer", "pod", pod, "container", container)
	}

	// look up effective device annotation and unmarshal devices
	for _, key = range []string{
		deviceKey + "/container." + container.Name,
		deviceKey + "/pod",
		deviceKey,
	} {
		if value, ok := pod.Annotations[key]; ok {
			annotation = []byte(value)
			break
		}
	}

	if annotation == nil {
		log.Infof("%s: no device annotations, ignoring...", ctrName)
		return nil, nil, nil
	}

	if err := yaml.Unmarshal(annotation, &devices); err != nil {
		log.Errorf("%s: failed to unmarshal device annotation %q: %v", ctrName, key, err)
		return nil, nil, errors.Wrapf(err, "invalid device annotation %q", key)
	}

	// inject devices to container
	if len(devices) == 0 {
		log.Infof("%s: no devices annotated, ignoring...", ctrName)
		return nil, nil, nil
	}

	if verbose {
		dump(ctrName, "Devices", devices)
	}

	adjust := &api.ContainerAdjustment{}

	for _, d := range devices {
		adjust.AddDevice(d.toAPI())
		if !verbose {
			log.Infof("%s: injected device %q...", ctrName, d.Path)
		}
	}

	if verbose {
		dump(ctrName, "ContainerAdjustment", adjust)
	}

	return adjust, nil, nil
}

// Convert a device to the NRI API representation.
func (d *device) toAPI() *api.LinuxDevice {
	apiDev := &api.LinuxDevice{
		Path:  d.Path,
		Type:  d.Type,
		Major: d.Major,
		Minor: d.Minor,
	}
	if d.FileMode != 0 {
		apiDev.FileMode = api.FileMode(d.FileMode)
	}
	if d.UID != 0 {
		apiDev.Uid = api.UInt32(d.UID)
	}
	if d.GID != 0 {
		apiDev.Gid = api.UInt32(d.GID)
	}
	return apiDev
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
