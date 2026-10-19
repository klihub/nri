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

package framework

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/api"
)

const (
	// RunIDLabel identifies the nri-test run that created a workload.
	RunIDLabel = "nri-test.containerd.io/run-id"
	// CaseLabel identifies the test case that created a workload.
	CaseLabel = "nri-test.containerd.io/case"

	// DefaultImage is the image used for test containers. It serves both
	// create-only adjustment checks, which never start the container, and tests
	// that need a running one.
	//
	// The image MUST HAVE A SHELL and the usual coreutils-shaped tools (`cat`,
	// `sleep`). In-container verification reads what the kernel actually applied
	// by running a command inside the container over ExecSync, and there is
	// nothing to run in an image that carries no executables. That is why
	// registry.k8s.io/pause -- which this used to be -- cannot be used: it holds
	// a single static binary that blocks in a signal wait and cannot be exec'd
	// into at all.
	//
	// busybox is the smallest image that qualifies: 2.1 MiB. Its default command
	// exits immediately without a TTY, so Suite.CtrSpec gives containers a
	// keep-alive command; see there.
	DefaultImage = "registry.k8s.io/e2e-test-images/busybox:1.36.1-1"

	// DefaultNamespace is the pod namespace test workloads are created in. It is
	// the outer isolation guard: plugins ignore everything outside of it, so no
	// suite can adjust, reject, or inspect a workload it did not create.
	DefaultNamespace = "nri-test"

	// DefaultTimeout bounds a single operation or event wait.
	DefaultTimeout = 30 * time.Second
)

// DefaultRuntimeEndpoints are probed in order when no endpoint is given,
// mirroring what crictl does.
var DefaultRuntimeEndpoints = []string{
	"unix:///run/containerd/containerd.sock",
	"unix:///run/crio/crio.sock",
}

// Config holds the test suite's configuration.
type Config struct {
	// RuntimeEndpoint is the CRI runtime service endpoint.
	RuntimeEndpoint string
	// ImageEndpoint is the CRI image service endpoint.
	ImageEndpoint string
	// NRISocketPath is the NRI socket external plugins connect to.
	NRISocketPath string
	// Namespace is the pod namespace test workloads are created in. It is the
	// outer isolation guard: plugins ignore everything outside of it, so no test
	// can adjust, reject, or inspect a workload it did not create.
	Namespace string
	// Image is the image used for test containers.
	Image string
	// ConfigSuite selects an opt-in, runtime-configuration-dependent suite.
	ConfigSuite string
	// KeepArtifacts skips cleanup, for debugging.
	KeepArtifacts bool
	// NoPull skips pulling Image during setup, for air-gapped nodes where the
	// image is already in the image store and no registry is reachable.
	NoPull bool
	// CollectFailures reports every expectation failure of a test instead of
	// ending the test at its first one, and prints them all in the run's
	// summary. It is what makes a first run against an unfamiliar runtime
	// enumerate the differences in one go rather than one run per difference.
	//
	// It changes when a test stops, never whether a failure is reported: a
	// failure is recorded in both modes, and a run that recorded one exits
	// non-zero in both.
	CollectFailures bool
	// AllowKubelet lets the run proceed in a driver mode that kubelet would
	// garbage-collect the workloads of while kubelet is running. Without it,
	// setup refuses to start while a kubelet process is found.
	AllowKubelet bool
	// RuntimeHandler is the low-level runtime every workload of the run is created
	// with -- runc, crun, a VM runtime -- or the runtime's default one when empty.
	//
	// Single-valued, and a sweep over handlers is two invocations of the binary
	// rather than one run doing both. A flag that accepted a list and then used one
	// entry of it would be a flag that lies about what it ran, which is the defect
	// that got -nri-test.differential removed from this suite; and a run's summary
	// names one handler, so a run that used two could not be quoted at all.
	RuntimeHandler string
	// Timeout bounds a single operation or event wait.
	Timeout time.Duration
	// RunID scopes everything this run creates. Generated if empty.
	RunID string

	// endpointCandidates is overridable for testing.
	endpointCandidates []string
	// procDir is the proc filesystem scanned for a running kubelet. Empty means
	// /proc. Overridable for testing.
	procDir string
}

// TestConfig is the configuration the suite runs with.
var TestConfig = &Config{}

// RegisterFlags registers the suite's flags. Call it from init(), so that the
// flags exist before the testing package parses them.
func RegisterFlags() {
	flag.StringVar(&TestConfig.RuntimeEndpoint, "runtime-endpoint", "",
		"CRI runtime service endpoint; autodetected when empty")
	flag.StringVar(&TestConfig.ImageEndpoint, "image-endpoint", "",
		"CRI image service endpoint; defaults to the runtime endpoint")
	flag.StringVar(&TestConfig.NRISocketPath, "nri-socket", api.DefaultSocketPath,
		"NRI socket for external plugins")
	flag.StringVar(&TestConfig.Namespace, "nri-test.namespace", DefaultNamespace,
		"pod namespace to create test workloads in; workloads outside it are ignored")
	flag.StringVar(&TestConfig.Image, "nri-test.image", DefaultImage,
		"image used for test containers")
	flag.StringVar(&TestConfig.ConfigSuite, "nri-test.config", "",
		"opt-in runtime-configuration-dependent suite to run")
	// No -nri-test.differential flag: the differential check against
	// runtime-tools/generate is milestone 9 and unbuilt, and the flag was registered
	// and read by nothing, so setting it silently did nothing. A flag that lies to
	// its user is worse than a missing one. It belongs with the check itself.
	flag.BoolVar(&TestConfig.KeepArtifacts, "nri-test.keep-artifacts", false,
		"do not clean up created pods and containers")
	flag.BoolVar(&TestConfig.NoPull, "nri-test.no-pull", false,
		"do not pull the test image during setup; it must already be in the image store")
	// Default false: a collecting default would make CI green on a real failure
	// unless CI remembered to pass the flag, and the mode that reports the most
	// is not the mode that should decide an unattended run's verdict.
	flag.BoolVar(&TestConfig.CollectFailures, "nri-test.collect-failures", false,
		"collect expectation failures and report them at the end of the run "+
			"instead of failing at the first one")
	flag.BoolVar(&TestConfig.AllowKubelet, "nri-test.allow-kubelet", false,
		"run in CRI mode while kubelet is running; kubelet may garbage-collect "+
			"the test's sandboxes mid-run")
	// Single-valued; see Config.RuntimeHandler for why a list would be a flag that
	// lies. The usage states what an empty value means, because "" is the ordinary
	// case rather than a missing setting.
	flag.StringVar(&TestConfig.RuntimeHandler, "nri-test.runtime-handler", "",
		"low-level runtime to create every workload with, e.g. runc or crun; "+
			"empty means the runtime's default handler. Setup refuses a handler the "+
			"node does not have. One handler per run: sweep by running the suite again")
	flag.DurationVar(&TestConfig.Timeout, "nri-test.timeout", DefaultTimeout,
		"timeout for a single operation or event wait")
	flag.StringVar(&TestConfig.RunID, "nri-test.run-id", "",
		"run id scoping created workloads; generated when empty")
}

// Complete fills in defaults and validates the configuration.
func (c *Config) Complete() error {
	if c.NRISocketPath == "" {
		c.NRISocketPath = api.DefaultSocketPath
	}
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace
	}
	if c.Image == "" {
		c.Image = DefaultImage
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.RunID == "" {
		id, err := generateRunID()
		if err != nil {
			return err
		}
		c.RunID = id
	}
	if c.RuntimeEndpoint == "" {
		ep, err := c.detectRuntimeEndpoint()
		if err != nil {
			return err
		}
		c.RuntimeEndpoint = ep
	}
	if c.ImageEndpoint == "" {
		c.ImageEndpoint = c.RuntimeEndpoint
	}

	return nil
}

// PodLabels returns the labels every workload of the given case must carry.
func (c *Config) PodLabels(caseName string) map[string]string {
	return map[string]string{
		RunIDLabel: c.RunID,
		CaseLabel:  caseName,
	}
}

func (c *Config) detectRuntimeEndpoint() (string, error) {
	candidates := c.endpointCandidates
	if candidates == nil {
		candidates = DefaultRuntimeEndpoints
	}

	var found []string

	for _, ep := range candidates {
		path := strings.TrimPrefix(ep, "unix://")
		if _, err := os.Stat(path); err == nil {
			found = append(found, ep)
		}
	}

	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
	default:
		// Refused rather than picking the first. On a node running both
		// runtimes the NRI socket belongs to one of them, and a CRI endpoint
		// from the other makes the suite wait for events that never come.
		return "", fmt.Errorf("several CRI runtime endpoints found (%s); "+
			"use -runtime-endpoint to say which runtime to test",
			strings.Join(found, ", "))
	}

	return "", fmt.Errorf("no CRI runtime endpoint found among %s; "+
		"use -runtime-endpoint", strings.Join(candidates, ", "))
}

func generateRunID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate run id: %w", err)
	}

	return hex.EncodeToString(buf), nil
}
