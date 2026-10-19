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

	// DefaultStopGrace is how long a test container or pod is given to stop
	// before it is killed. Test containers exit on TERM (see KeepAliveCommand),
	// so a stop normally takes far less; this only bounds a misbehaving one. It
	// is the one place the suite's grace periods come from: the CRI stop
	// timeout and the Kubernetes pod's terminationGracePeriodSeconds and
	// deletion grace. A test that needs another value can still set its own.
	DefaultStopGrace = 2 * time.Second

	// ProcViewSelf and ProcViewInit name which process's /proc entry the suite
	// reads to see a container's applied cpuset and NUMA nodes. ProcViewSelf
	// reads /proc/self/status of the exec'd process, which matched the
	// container's cgroup in every measured combination. ProcViewInit reads
	// /proc/1/status, which is right only where PID 1 is the container's own
	// init, that is with a per-container PID namespace. It exists to
	// cross-check the exec path against runtimes where exec is suspected of
	// placing its process differently.
	ProcViewSelf = "self"
	ProcViewInit = "init"

	// DefaultTimeout bounds a single operation or event wait.
	DefaultTimeout = 30 * time.Second

	// DriverCRI and DriverK8s name the drivers a conformance run can use.
	// The CRI driver talks to the runtime directly and needs kubelet stopped.
	// The Kubernetes driver creates pods through the API server and needs
	// kubelet running, so its workloads arrive the way a user's do.
	DriverCRI = "cri"
	DriverK8s = "k8s"

	// NamespacesK8s and NamespacesCRI name the namespace options the CRI driver
	// creates pods with. NamespacesK8s sends what kubelet sends for an ordinary
	// pod, so a container has a PID namespace of its own and its PID 1 is its
	// own init process. NamespacesCRI sends no options, and cri-api's zero
	// values share one PID namespace per pod, whose PID 1 is the sandbox's
	// pause process. The Kubernetes driver is not affected: kubelet decides.
	NamespacesK8s = "k8s"
	NamespacesCRI = "cri"
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
	// ListImages prints the images the suite uses and exits, without needing a
	// runtime.
	ListImages bool
	// PullImages pulls the images the suite uses into the node's image store and
	// exits, without running any test.
	PullImages bool
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
	// Driver selects what creates the conformance tests' workloads: DriverCRI
	// or DriverK8s.
	Driver string
	// CRINamespaces selects the namespace options the CRI driver creates pods
	// and containers with: NamespacesK8s or NamespacesCRI.
	CRINamespaces string
	// K8sNode is the node the Kubernetes driver places pods on. It is the node
	// this suite runs on, because the suite inspects containers through that
	// node's runtime. Empty means this host's name.
	K8sNode string
	// Kubeconfig names the cluster the Kubernetes driver uses. Empty uses
	// client-go's usual rules, so KUBECONFIG and an in-cluster service
	// account both work.
	Kubeconfig string
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
	// ProcView selects ProcViewSelf (the default) or ProcViewInit.
	ProcView string
	// StopGrace is the grace period given to stopping a container or a pod.
	// Zero kills at once.
	StopGrace time.Duration
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
	flag.BoolVar(&TestConfig.ListImages, "nri-test.list-images", false,
		"print the images the suite uses, one per line, and exit")
	flag.BoolVar(&TestConfig.PullImages, "nri-test.pull-images", false,
		"pull the images the suite uses into the node's image store and exit")
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
	flag.StringVar(&TestConfig.Driver, "nri-test.driver", DriverCRI,
		"what creates the tests' workloads: cri (the runtime directly, needs kubelet "+
			"stopped) or k8s (pods through the API server, needs kubelet running)")
	flag.StringVar(&TestConfig.CRINamespaces, "nri-test.cri-namespaces", NamespacesK8s,
		"namespace options the cri driver creates pods with: k8s (what kubelet "+
			"sends, a PID namespace per container) or cri (none, so a PID namespace "+
			"per pod)")
	flag.StringVar(&TestConfig.K8sNode, "nri-test.k8s-node", "",
		"node the k8s driver places pods on; empty uses this host's name")
	flag.StringVar(&TestConfig.Kubeconfig, "nri-test.kubeconfig", "",
		"kubeconfig for the k8s driver; empty uses client-go's usual rules")
	// Single-valued; see Config.RuntimeHandler for why a list would be a flag that
	// lies. The usage states what an empty value means, because "" is the ordinary
	// case rather than a missing setting.
	flag.StringVar(&TestConfig.RuntimeHandler, "nri-test.runtime-handler", "",
		"low-level runtime to create every workload with, e.g. runc or crun; "+
			"empty means the runtime's default handler. Setup refuses a handler the "+
			"node does not have. One handler per run: sweep by running the suite again")
	flag.StringVar(&TestConfig.ProcView, "nri-test.proc-view", ProcViewSelf,
		"which process's /proc/<pid>/status shows a container's applied cpuset and NUMA nodes: self or init")
	flag.DurationVar(&TestConfig.StopGrace, "nri-test.stop-grace", DefaultStopGrace,
		"grace period for stopping test containers and pods; 0 kills at once")
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
	if c.ProcView == "" {
		c.ProcView = ProcViewSelf
	}
	if c.ProcView != ProcViewSelf && c.ProcView != ProcViewInit {
		return fmt.Errorf("unknown -nri-test.proc-view %q: use %q or %q",
			c.ProcView, ProcViewSelf, ProcViewInit)
	}
	if c.StopGrace < 0 {
		return fmt.Errorf("-nri-test.stop-grace must not be negative, got %v", c.StopGrace)
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Driver == "" {
		c.Driver = DriverCRI
	}
	if c.Driver != DriverCRI && c.Driver != DriverK8s {
		return fmt.Errorf("unknown -nri-test.driver %q: use %q or %q",
			c.Driver, DriverCRI, DriverK8s)
	}
	if c.CRINamespaces == "" {
		c.CRINamespaces = NamespacesK8s
	}
	if c.CRINamespaces != NamespacesK8s && c.CRINamespaces != NamespacesCRI {
		return fmt.Errorf("unknown -nri-test.cri-namespaces %q: use %q or %q",
			c.CRINamespaces, NamespacesK8s, NamespacesCRI)
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

// StopGraceSeconds returns StopGrace in the whole seconds CRI and Kubernetes
// take.
func (c *Config) StopGraceSeconds() int64 {
	return int64(c.StopGrace / time.Second)
}
