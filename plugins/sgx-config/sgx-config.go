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
	"os"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
)

const (
	// Key used for SGX annotations.
	sgxEpcKey = "sgx-epc.nri.io"
)

var (
	log     *logrus.Logger
	verbose bool
)

// our injector plugin
type plugin struct {
	stub stub.Stub
}

// CreateContainer handles container creation requests.
func (p *plugin) CreateContainer(_ context.Context, pod *api.PodSandbox, container *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	var (
		ctrName string
		sgxEpc  uint64
		err     error
	)

	ctrName = containerName(pod, container)

	if verbose {
		dump("CreateContainer", "pod", pod, "container", container)
	} else {
		log.Infof("CreateContainer %s", ctrName)
	}

	adjust := &api.ContainerAdjustment{}

	sgxEpc, err = parseSgxEpc(container.Name, pod.Annotations)
	if err != nil {
		log.Errorf("failed to parse SGX EPC annotation: %v", err)
		return nil, nil, err
	}

	if sgxEpc > 0 {
		adjust.AddLinuxUnified("misc.max", "sgx_epc "+strconv.FormatUint(sgxEpc, 10))

		if verbose {
			dump(ctrName, "ContainerAdjustment", adjust)
		} else {
			log.Infof("adjusted SGX EPC to %d", sgxEpc)
		}
	} else {
		log.Infof("no SGX EPC annotations")
	}

	return adjust, nil, nil
}

func parseSgxEpc(ctr string, annotations map[string]string) (uint64, error) {
	// check container-specific or pod-global SGX EPC annotation and parse it
	for _, key := range []string{
		sgxEpcKey + "/container." + ctr,
		sgxEpcKey + "/pod",
		sgxEpcKey,
	} {
		if value, ok := annotations[key]; ok {
			epc, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("failed to parse annotation %s: %w", value, err)
			}
			return epc, nil
		}
	}

	return 0, nil
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
