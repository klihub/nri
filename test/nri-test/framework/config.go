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
	// DriverMock runs the suite against pkg/adaptation with no runtime at
	// all, so what it measures is the NRI side of the protocol and nothing
	// below it. It is a control arm: a clean run says the NRI chain held, not
	// that any runtime is conformant.
	DriverMock = "mock"

	// NamespacesK8s and NamespacesCRI name the namespace options the CRI driver
	// creates pods with. NamespacesK8s sends what kubelet sends for an ordinary
	// pod, so a container has a PID namespace of its own and its PID 1 is its
	// own init process. NamespacesCRI sends no options, and cri-api's zero
	// values share one PID namespace per pod, whose PID 1 is the sandbox's
	// pause process. The Kubernetes driver is not affected: kubelet decides.
	NamespacesK8s = "k8s"
	NamespacesCRI = "cri"

	// ScaleDriverCRI drives the burst over CRI, and is the default. We issue
	// the calls, so every operation has a client-side start and end as well as
	// an NRI callback, and the runtime's work either side of the hook can be
	// separated without a trace exporter. It also controls the burst's shape,
	// where a ReplicaSet's shape is kubelet's choice.
	ScaleDriverCRI = "cri"
	// ScaleDriverK8s scales a ReplicaSet. Less precise about when the burst
	// began, because kubelet decides that, but it is the realistic path and
	// includes the control plane and kubelet in the window.
	ScaleDriverK8s = "k8s"

	// DefaultScaleSettle bounds how long a scalability burst's events are
	// waited for. Generous compared with DefaultTimeout because a burst of
	// pods is slower than any single operation, and a window that closes early
	// reports a burst that is only a lower bound.
	DefaultScaleSettle = 2 * time.Minute
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
	// Kubeconfig names the cluster the Kubernetes driver or a scalability run
	// uses. Empty uses client-go's usual rules, so KUBECONFIG and an
	// in-cluster service account both work.
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

	// ScaleReplicas is the replica count a scalability burst scales to. Zero
	// turns the scalability test off, which is what an ordinary conformance
	// run wants: it is the only mode that talks to Kubernetes.
	ScaleReplicas int
	// ScaleIterations is how many bursts to measure.
	ScaleIterations int
	// ScaleSettle bounds how long a burst's events are waited for.
	ScaleSettle time.Duration
	// ScaleTerminalEvent names the stage a burst is measured to, either
	// StartContainer or PostStartContainer. They answer different questions;
	// see ScaleTerminal.
	ScaleTerminalEvent string
	// ScalePlugins is how many plugins a scalability run keeps registered,
	// the recorder included, or all simulated when measuring from spans.
	ScalePlugins int
	// ScaleEvents holds the raw -nri-test.scale-events entries. ScaleDelays
	// holds the -nri-test.scale-delay and -nri-test.scale-delay-file entries
	// interleaved in the order they appeared, which is what lets a later one
	// override an earlier one across the two flags. ScalePlan is what they
	// parse to.
	ScaleEvents []string
	ScaleDelays []DelaySource
	ScalePlan   *ScalePlan
	// ScaleTrace is the address the run listens on for the runtime's
	// OpenTelemetry spans. When set, the burst is measured from those spans
	// instead of from a recorder plugin, and every registered plugin is a
	// simulated one. Empty measures with the recorder.
	ScaleTrace string
	// ScaleTraceDump is where every received span is written, as JSON lines,
	// when measuring from spans.
	ScaleTraceDump string
	// ScaleTraceProtocol is the OTLP transport ScaleTrace speaks: TraceHTTP
	// or TraceGRPC.
	ScaleTraceProtocol string
	// ScaleReportPath is where the raw per-pod table is written, if anywhere.
	ScaleReportPath string
	// ScaleDriver selects what creates the burst: "cri" drives the runtime
	// directly, "k8s" scales a ReplicaSet. They measure different things and
	// have opposite requirements about kubelet; see ScaleViaK8s.
	ScaleDriver string

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
		"kubeconfig for the k8s driver or a scalability run; empty uses "+
			"client-go's usual rules")
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
	flag.IntVar(&TestConfig.ScaleReplicas, "nri-test.scale-replicas", 0,
		"replica count a scalability burst scales to; 0 skips the scalability test")
	flag.IntVar(&TestConfig.ScaleIterations, "nri-test.scale-iterations", 1,
		"how many scalability bursts to measure")
	flag.DurationVar(&TestConfig.ScaleSettle, "nri-test.scale-settle", DefaultScaleSettle,
		"how long to wait for a burst's events before reporting what arrived")
	flag.StringVar(&TestConfig.ScaleTerminalEvent, "nri-test.scale-terminal",
		string(PostStartContainer),
		"stage a burst is measured to: StartContainer or PostStartContainer")
	flag.IntVar(&TestConfig.ScalePlugins, "nri-test.scale-plugins", 1,
		"plugins kept registered during a scalability run, the recorder included; "+
			"the others simulate plugins that subscribe and take time to answer")
	flag.Func("nri-test.scale-events",
		"events simulated plugins subscribe to, as [INDEX=]EVENT,EVENT...; repeatable; "+
			"the default is every hook except ValidateContainerAdjustment",
		func(v string) error { TestConfig.ScaleEvents = append(TestConfig.ScaleEvents, v); return nil })
	flag.Func("nri-test.scale-delay",
		"simulated processing time, as DURATION (every event) or [INDEX/]EVENT=DURATION, "+
			"comma separated and repeatable; later entries win; EVENT may be 'all'",
		func(v string) error {
			TestConfig.ScaleDelays = append(TestConfig.ScaleDelays, DelaySource{Spec: v})

			return nil
		})
	flag.Func("nri-test.scale-delay-file",
		"delay profiles as YAML: a profiles section of named per-event durations and "+
			"a plugins section mapping a plugin index, or \"*\" for the rest, to a "+
			"profile name; repeatable, and interleaved with -nri-test.scale-delay in "+
			"the order given, so a later entry wins per plugin and event",
		func(v string) error {
			TestConfig.ScaleDelays = append(TestConfig.ScaleDelays, DelaySource{Path: v})

			return nil
		})
	flag.StringVar(&TestConfig.ScaleTrace, "nri-test.scale-trace", "",
		"listen for the runtime's OpenTelemetry spans on this address and measure the "+
			"burst from them, with no recorder plugin; the runtime must be configured "+
			"to export there")
	flag.StringVar(&TestConfig.ScaleTraceProtocol, "nri-test.scale-trace-protocol", TraceHTTP,
		"OTLP transport -nri-test.scale-trace speaks: http (containerd) or grpc (CRI-O)")
	flag.StringVar(&TestConfig.ScaleTraceDump, "nri-test.scale-trace-dump", "",
		"write every span received for -nri-test.scale-trace here, as JSON lines")
	flag.StringVar(&TestConfig.ScaleReportPath, "nri-test.scale-report", "",
		"write the raw per-pod timing table here as JSON")
	flag.StringVar(&TestConfig.ScaleDriver, "nri-test.scale-driver", ScaleDriverCRI,
		"what creates a scalability burst: cri (direct, exact client-side timing, "+
			"needs kubelet stopped) or k8s (a ReplicaSet, needs kubelet running)")
}

// ScaleNamespace returns the Kubernetes namespace a scalability run works in.
//
// It is derived from the run id so two concurrent runs on one cluster cannot
// collide or adopt each other's pods, and so the namespace the plugins filter
// on and the namespace the ReplicaSet lives in are the same string by
// construction rather than by being set consistently in two places.
func ScaleNamespace(runID string) string {
	return "nri-test-scale-" + runID
}

// ScaleTerminal returns the stage a burst is measured to.
//
// PostStartContainer is the default because "the last event" is the natural
// reading of how long a burst took. It is not interchangeable with
// StartContainer: StartContainer is the runtime asking before it starts a
// container, so measuring to it answers "how long until the runtime was ready
// to start everything", while PostStartContainer is a notification afterwards
// and answers "how long until we were told everything had started".
func (c *Config) ScaleTerminal() EventType {
	if EventType(c.ScaleTerminalEvent) == StartContainer {
		return StartContainer
	}

	return PostStartContainer
}

// ScalePhaseBudget returns how long one direction of one burst may take.
//
// Each direction gets its own, for the reason withTimeout documents: a burst
// that ran long would otherwise leave the teardown with an already-spent
// deadline, so every teardown call would fail the moment it was issued and the
// teardown report would be of a phase that never happened.
//
// It is never zero. A zero duration makes context.WithTimeout return an
// already-expired context, which is exactly that failure arrived at from the
// other side.
func (c *Config) ScalePhaseBudget() time.Duration {
	if c.ScaleSettle <= 0 {
		return DefaultScaleSettle
	}

	return c.ScaleSettle
}

// ScaleBudget returns how long a whole scalability run may take: one
// ScalePhaseBudget per direction of every iteration.
//
// Derived from ScalePhaseBudget rather than from ScaleSettle directly, so the
// whole-run budget and the per-phase ones cannot drift into disagreeing about
// how long a run is allowed to take.
func (c *Config) ScaleBudget() time.Duration {
	iterations := c.ScaleIterations
	if iterations < 1 {
		iterations = 1
	}

	return 2 * time.Duration(iterations) * c.ScalePhaseBudget()
}

// Scaling reports whether this run measures scalability.
func (c *Config) Scaling() bool { return c.ScaleReplicas > 0 }

// ScaleViaK8s reports whether the burst comes from a ReplicaSet rather than
// from the CRI driver.
//
// The distinction decides the kubelet requirement, and the two are opposite: a
// CRI burst creates sandboxes kubelet would garbage-collect, so kubelet must be
// stopped, while a ReplicaSet burst is brought up by kubelet and cannot happen
// without it. Getting this the wrong way round produces either a burst whose
// pods vanish underneath it or one that never starts, so it is read from one
// place.
func (c *Config) ScaleViaK8s() bool {
	return c.Scaling() && c.ScaleDriver == ScaleDriverK8s
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
	if c.Driver != DriverCRI && c.Driver != DriverK8s && c.Driver != DriverMock {
		return fmt.Errorf("unknown -nri-test.driver %q: use %q, %q or %q",
			c.Driver, DriverCRI, DriverK8s, DriverMock)
	}
	// A burst against an in-process mock measures Go calls, and a number
	// produced that way would be read as a runtime's.
	if c.Driver == DriverMock && c.Scaling() {
		return fmt.Errorf("-nri-test.driver %q cannot measure scalability: a burst "+
			"needs a real runtime; drop -nri-test.scale-replicas or choose %q or %q",
			DriverMock, DriverCRI, DriverK8s)
	}
	if c.CRINamespaces == "" {
		c.CRINamespaces = NamespacesK8s
	}
	if c.CRINamespaces != NamespacesK8s && c.CRINamespaces != NamespacesCRI {
		return fmt.Errorf("unknown -nri-test.cri-namespaces %q: use %q or %q",
			c.CRINamespaces, NamespacesK8s, NamespacesCRI)
	}
	if c.ScaleSettle == 0 {
		c.ScaleSettle = DefaultScaleSettle
	}
	if c.ScaleIterations < 1 {
		c.ScaleIterations = 1
	}
	// Refused rather than defaulted, as the driver and namespace modes above
	// are. The two stages answer different questions, so a typo that silently
	// measured to the other one would produce a number labelled as something
	// it is not, and nothing downstream could tell.
	if c.ScaleTerminalEvent != "" &&
		EventType(c.ScaleTerminalEvent) != StartContainer &&
		EventType(c.ScaleTerminalEvent) != PostStartContainer {
		return fmt.Errorf("unknown -nri-test.scale-terminal %q: use %q or %q",
			c.ScaleTerminalEvent, StartContainer, PostStartContainer)
	}
	if c.ScaleDriver == "" {
		c.ScaleDriver = ScaleDriverCRI
	}
	if c.ScalePlugins < 1 {
		c.ScalePlugins = 1
	}
	newPlan := NewScalePlan
	if c.ScaleTrace != "" {
		newPlan = NewTracedScalePlan
	}
	plan, err := newPlan(c.ScalePlugins, c.ScaleEvents, c.ScaleDelays)
	if err != nil {
		return err
	}
	c.ScalePlan = plan
	if c.ScaleTraceProtocol == "" {
		c.ScaleTraceProtocol = TraceHTTP
	}
	if c.ScaleTraceProtocol != TraceHTTP && c.ScaleTraceProtocol != TraceGRPC {
		return fmt.Errorf("unknown -nri-test.scale-trace-protocol %q: use %q or %q",
			c.ScaleTraceProtocol, TraceHTTP, TraceGRPC)
	}
	if c.Scaling() && c.ScaleDriver != ScaleDriverCRI && c.ScaleDriver != ScaleDriverK8s {
		// Refused rather than defaulted. The two drivers have opposite kubelet
		// requirements, so a typo that silently picked one would either have
		// the burst's pods collected underneath it or never see a pod at all,
		// and the reason would be nowhere near the flag.
		return fmt.Errorf("unknown -nri-test.scale-driver %q: use %q or %q",
			c.ScaleDriver, ScaleDriverCRI, ScaleDriverK8s)
	}
	if c.RunID == "" {
		id, err := generateRunID()
		if err != nil {
			return err
		}
		c.RunID = id
	}
	// In scalability mode the workload is a ReplicaSet, so the pod namespace is
	// a real Kubernetes namespace rather than a convention the suite imposes.
	// Overriding it here rather than asking the operator to keep two flags in
	// agreement is what makes the plugins' scope filter accept exactly the
	// burst's pods: a Kubernetes pod's CRI sandbox namespace is its Kubernetes
	// namespace.
	// Only the ReplicaSet driver needs this. A CRI burst creates its own
	// sandboxes in the suite's ordinary namespace, exactly as every other test
	// does, so overriding it there would move the workload away from the
	// stale-workload sweep and the leak guard for no reason.
	if c.ScaleViaK8s() {
		c.Namespace = ScaleNamespace(c.RunID)
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
