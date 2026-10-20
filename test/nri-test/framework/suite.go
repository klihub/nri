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
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
)

// Package-level state shared by every case. The suite is strictly serial, so a
// single driver, inspector and set of probed facts serve all of it.
var (
	driver            Driver
	inspector         Inspector
	executor          Executor
	runtimeName       string
	runtimeVersion    string
	runtimeNRIVersion string
	// runtimeHandlers are the selectable low-level runtimes the node has
	// installed, as discovery found them. Empty means either that nothing was
	// discovered or that the driver could not be asked; a gate treats both the
	// same, since neither can confirm a handler is there.
	runtimeHandlers []string
)

// DriverFactory builds the driver and inspector a run uses. The suite package
// sets it, so that framework does not depend on any driver implementation.
type DriverFactory func(ctx context.Context, cfg *Config) (Driver, Inspector, error)

// withTimeout runs fn with a context of its own, bounded by timeout.
//
// It exists so that consecutive startup and shutdown phases each get an
// independent budget. Do not collapse two phases back into one shared deadline:
// whichever phase runs first would then be able to consume the next one's time,
// and the next one would fail with its own diagnostic for a reason that has
// nothing to do with it.
func withTimeout(timeout time.Duration, fn func(context.Context) error) error {
	return withTimeoutOf(context.Background(), timeout, fn)
}

// withTimeoutOf is withTimeout under a parent context, for a phase that is
// itself a loop of independently bounded steps: each step gets a full budget
// of its own, while cancelling the parent still stops the whole loop.
func withTimeoutOf(parent context.Context, timeout time.Duration,
	fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	return fn(ctx)
}

// cgroupRoot is where the cgroup hierarchy is mounted, a variable for tests.
var cgroupRoot = "/sys/fs/cgroup"

// checkCgroupV2 refuses to run on a host without the unified cgroup hierarchy.
// The suite's expectations about cpuset and memory placement, and its measured
// facts, are for cgroup v2 only; cgroup v1 is obsolete and unsupported. The
// file is present at the root of a v2 mount only: a v1 or hybrid host has a tmpfs
// there.
func checkCgroupV2() error {
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return fmt.Errorf("%s is not a cgroup v2 (unified) hierarchy: %w\n"+
			"The suite supports cgroup v2 only. Boot the node with "+
			"systemd.unified_cgroup_hierarchy=1", cgroupRoot, err)
	}

	return nil
}

// Setup connects to the runtime, probes it, and verifies that NRI plugins can
// connect. Call it from TestMain before running any test.
//
// Each phase gets its own timeout rather than sharing one deadline. Sharing one
// would let a slow driver connect eat the preflight's remainder, making the probe
// report that no NRI plugin could connect against a runtime that is configured
// correctly -- the most misleading possible form of the one message whose whole
// job is legibility -- and would equally let it eat the stale-workload cleanup's,
// so cleanup could fail for a reason that has nothing to do with cleanup.
func Setup(cfg *Config, newDriver DriverFactory) error {
	if err := checkCgroupV2(); err != nil {
		return err
	}

	err := withTimeout(cfg.Timeout, func(ctx context.Context) error {
		d, i, err := newDriver(ctx, cfg)
		if err != nil {
			return err
		}

		// A factory that reported no error must have produced a driver. Said here
		// rather than left to the first call: every use of driver below is a method
		// call on an interface, which on a nil one panics with a bare nil-pointer
		// dereference somewhere inside the framework.
		if d == nil {
			return errors.New("driver factory returned no driver and no error")
		}

		driver, inspector = d, i
		executor = executorOf(i)

		return nil
	})
	if err != nil {
		return err
	}

	// Before anything that creates or touches a workload: a kubelet that
	// garbage-collects this run's sandboxes must be caught before the first one
	// exists. The driver is what declares the sensitivity, hence after it is built.
	if err := checkKubelet(cfg, driver); err != nil {
		return err
	}

	// Before preflight, and deliberately so. The plan this task came from put it
	// after, on the grounds that the probe's timeout budget must not pay for a CRI
	// Status call -- which is satisfied by the withTimeout below wherever the phase
	// sits. Placed after preflight it would be unreachable on any host whose
	// runtime cannot accept a plugin connection, so a mistyped handler would be
	// reported as "no NRI plugin could connect" and the refusal below could never
	// be observed at all. Validating a flag's value is input validation, and that
	// belongs before network work.
	if err := withTimeout(cfg.Timeout, func(ctx context.Context) error {
		return selectRuntimeHandler(ctx, cfg)
	}); err != nil {
		return err
	}

	if err := checkExecCapability(); err != nil {
		return err
	}

	if err := withTimeout(cfg.Timeout, func(ctx context.Context) error {
		return preflight(ctx, cfg)
	}); err != nil {
		return err
	}

	// Deliberately not wrapped in a budget of its own: pullImage bounds each
	// image separately, and a cap here would put them all back on one shared
	// deadline, which is the thing it exists to avoid. This is the one phase
	// whose budget is per step rather than per phase, as PullImages is.
	if err := pullImage(context.Background(), cfg); err != nil {
		return err
	}

	// This sweep is close to a no-op in the common case, and deliberately so: since
	// Scope.MatchesForRemoval removes only pods carrying this run's own id, and this
	// run has created nothing yet, a fresh run finds nothing to clean. The real
	// cleanup happens in Teardown and per-case, where the driver's tracked pods
	// carry it. Do not read the call as dead code: it is what cleans up after a
	// re-used -nri-test.run-id, and it catches a pod an earlier phase of this run
	// created and stopped tracking. Reaping other runs' leftovers is not its job --
	// a label cannot tell a crashed run from a live one.
	if err := withTimeout(cfg.Timeout, driver.Cleanup); err != nil {
		return fmt.Errorf("failed to clean up stale workloads before starting: %w", err)
	}

	// Reported here because the sweep above is what listed the namespace, so the
	// notice costs no extra call to the runtime, and reported once per run rather
	// than again in Teardown, which would only reprint the same list. It cannot
	// fail Setup: another run's pods are the node's business, not this run's fault.
	reportStalePods(os.Stdout, driver, cfg.Namespace)

	return nil
}

// executorOf returns the Executor an Inspector also is, or nil.
//
// The exec surface reaches the suite by type assertion on the inspector rather
// than as a third return value from DriverFactory, which is the same idiom
// preflight uses for versioner and pullImage for imagePuller. Three reasons.
// It keeps DriverFactory's signature -- the one seam every driver, present and
// future, has to implement -- from growing a return value each time a new
// observation surface is added; the next one after exec would make it five. It
// puts exec where it belongs architecturally: Driver mutates, Inspector
// observes, and exec observes, so an inspector is the thing that has it. And it
// leaves exec genuinely optional, so a driver that cannot exec -- a Kubernetes
// driver reaching a cluster it has no node access to, say -- stays a legal
// driver rather than having to return a stub that errors.
//
// The cost of an optional interface is that a driver can advertise CapExec and
// still not provide one; checkExecCapability closes that.
func executorOf(i Inspector) Executor {
	e, ok := i.(Executor)
	if !ok {
		return nil
	}

	return e
}

// checkExecCapability fails Setup when the driver's advertised capabilities and
// its actual exec surface disagree.
//
// CapExec is what tests gate on, so the two must agree or the gate lies. Caught
// here it is one message at startup naming the driver; caught later it is a
// "provides no Executor" failure in every exec-verified test, none of which is
// about the test that reports it.
func checkExecCapability() error {
	if !driver.Capabilities().Has(CapExec) {
		return nil
	}

	if executor == nil {
		return fmt.Errorf("driver %q advertises CapExec but its inspector does "+
			"not implement framework.Executor", driver.Name())
	}

	return nil
}

// runtimeHandlerLister is implemented by a driver that can say which low-level
// runtimes the node has installed.
//
// Optional, and discovered by type assertion exactly as versioner, imagePuller
// and kubeletConflicter are: a driver with no way to ask -- a Kubernetes driver,
// where the equivalent question is about RuntimeClasses in the cluster rather
// than handlers on a node -- should not have to implement it to be usable. What
// it costs is that a requested handler cannot be checked against such a driver,
// which selectRuntimeHandler refuses rather than ignores.
type runtimeHandlerLister interface {
	RuntimeHandlers(ctx context.Context) ([]string, error)
}

// selectRuntimeHandler discovers the node's installed low-level runtimes and
// verifies that the one the run asked for is among them.
//
// Three rules, and which is which is the point:
//
//   - An empty Config.RuntimeHandler is the runtime's default and is never
//     validated. The default is legitimate, it is what every ordinary run uses,
//     and it is never in the discovered list -- CRI-O reports it as an entry with
//     no name at all. Validating it would refuse every default run.
//   - A requested handler absent from the discovered list fails the run here,
//     with one message naming what was asked for and what is installed. Left to
//     the tests it is a per-test CreatePod failure that names neither.
//   - A discovery that could not be done -- the driver cannot list, or the call
//     failed -- is fatal only when a handler was requested. A run that asked for
//     nothing gets the default and does not care what Status would have said.
//
// The discovered list is recorded before validation, so the refusal and
// SkipUnlessRuntimeHandler describe the node from the same set.
func selectRuntimeHandler(ctx context.Context, cfg *Config) error {
	// Reset first: Setup may run more than once in a process (the framework's own
	// tests do), and a stale list would let a gate skip or admit on the previous
	// run's node.
	runtimeHandlers = nil

	lister, ok := driver.(runtimeHandlerLister)
	if !ok {
		if cfg.RuntimeHandler == "" {
			return nil
		}

		return fmt.Errorf("-nri-test.runtime-handler=%s was requested, but driver %q "+
			"cannot list the runtime handlers this node has installed, so the request "+
			"cannot be honoured or checked; drop the flag to use the runtime's default "+
			"handler", cfg.RuntimeHandler, driver.Name())
	}

	handlers, err := lister.RuntimeHandlers(ctx)
	if err != nil {
		if cfg.RuntimeHandler == "" {
			return nil
		}

		return fmt.Errorf("-nri-test.runtime-handler=%s was requested, but the runtime "+
			"handlers this node has installed could not be listed, so the request cannot "+
			"be checked: %w", cfg.RuntimeHandler, err)
	}

	runtimeHandlers = handlers

	if cfg.RuntimeHandler == "" {
		return nil
	}

	if !slices.Contains(runtimeHandlers, cfg.RuntimeHandler) {
		return fmt.Errorf("-nri-test.runtime-handler=%s is not installed on this node, "+
			"which reports %s; the runtime would refuse every workload of this run. %s",
			cfg.RuntimeHandler, installedHandlers(), handlerRemedy())
	}

	return nil
}

// handlerRemedy is the advice a not-installed refusal ends with.
//
// Branched, because on a node whose runtime advertises nothing there is no "one
// of those" to select: telling an operator to pick from an empty list is the
// message that reads as a bug in the suite rather than as a fact about the node,
// and that node is precisely the one this refusal is most likely to be read on.
// Read through RuntimeHandlers(), as the gate is, so that one run's refusal and
// its skips cannot come to disagree about which handlers the node has.
func handlerRemedy() string {
	if len(RuntimeHandlers()) == 0 {
		return "This runtime names no handlers at all, so no handler can be " +
			"selected here: drop the flag to use its default, or run the suite on a " +
			"node whose runtime reports the handler you want."
	}

	return "Select one of those handlers, or drop the flag to use the runtime's " +
		"default."
}

// installedHandlers describes the discovered handlers for a message that has to
// say what the node has.
//
// Shared by Setup's refusal and by SkipUnlessRuntimeHandler's skip reason, so the
// two cannot describe the same node differently. The empty case says what it
// means rather than printing an empty list: a runtime that advertises no handlers
// is not a runtime with no low-level runtime, it is one that does not say.
//
// Read through RuntimeHandlers() for the reason handlerRemedy's note gives.
func installedHandlers() string {
	handlers := RuntimeHandlers()
	if len(handlers) == 0 {
		return "no selectable runtime handlers at all"
	}

	return "runtime handler(s) " + strings.Join(handlers, ", ")
}

// RuntimeHandlers returns the selectable low-level runtimes the node has
// installed, as discovery found them. Empty when the runtime advertises none or
// the driver cannot be asked.
func RuntimeHandlers() []string { return slices.Clone(runtimeHandlers) }

// RuntimeHandler returns the low-level runtime the run was configured to use, or
// "" when none was asked for and the runtime's default applies.
//
// What was configured, not what was verified: it answers from Config alone, so it
// says the same thing before Setup has run, after a Setup that refused the
// handler, and on the -test.list path that never calls Setup at all. Only
// RuntimeHandlers() reflects what the node turned out to have.
func RuntimeHandler() string { return TestConfig.RuntimeHandler }

// imagePuller is implemented by a driver that can pre-pull the test image.
//
// Optional, and discovered by type assertion exactly as preflight discovers
// versioner: a future driver that has no image service of its own -- a Kubernetes
// driver, say, where the kubelet does the pulling -- should not have to implement
// it to be usable.
type imagePuller interface {
	PullImage(ctx context.Context, image string) error
}

// pullImage makes sure every test image (see Images) is in the node's image store before any
// test tries to create a container from it.
//
// Nothing else pulls it. CRI's CreateContainer does not pull, so on a node where
// the image is absent every container-creating test fails with "failed to create
// container ... in pod ..." wrapping a not-found -- a message that mentions
// neither the image nor pulling, and that arrives once per test instead of once.
func pullImage(ctx context.Context, cfg *Config) error {
	if cfg.NoPull {
		return nil
	}

	puller, ok := driver.(imagePuller)
	if !ok {
		return nil
	}

	// A budget per image, not one shared across them, for the reason
	// withTimeout documents and which PullImages already follows. Images may
	// be large and they are pulled from a registry, so one slow or absent
	// image would otherwise spend the budget of every image after it -- and
	// those would then fail on a deadline something else used up, with a
	// message naming the wrong image.
	for _, image := range Images(cfg) {
		err := withTimeoutOf(ctx, cfg.Timeout, func(ictx context.Context) error {
			return puller.PullImage(ictx, image)
		})
		if err != nil {
			return fmt.Errorf("failed to pull the test image %s: %w\n"+
				"Tests that create a container need it. Either make the image "+
				"pullable from this node, or pull it by hand and pass "+
				"-nri-test.no-pull, or select another image with -nri-test.image",
				image, err)
		}
	}

	return nil
}

// preflight records the runtime's identity and confirms that an NRI plugin can
// connect, so a misconfigured runtime produces one clear error instead of a
// failure in every test.
func preflight(ctx context.Context, cfg *Config) error {
	// RuntimeVersion is deliberately not on the Driver interface: it is a
	// convenience for the banner and for inferring the NRI version, not something
	// a test depends on, and a driver that cannot report it should not have to.
	type versioner interface {
		RuntimeVersion(context.Context) (string, string, error)
	}

	if v, ok := driver.(versioner); ok {
		name, version, err := v.RuntimeVersion(ctx)
		if err != nil {
			return err
		}

		runtimeName, runtimeVersion = name, version
	}

	// The probe gets a budget of its own, for the same reason the phases in Setup
	// do: the RuntimeVersion call above talks to the runtime too, and if it were
	// allowed to consume the probe's time the probe would blame NRI for it.
	return withTimeout(cfg.Timeout, func(ctx context.Context) error {
		probe := NewTestPlugin("preflight", "00", 0, &EventQ{}, cfg.Scope())
		if err := probe.Start(ctx, cfg.NRISocketPath, cfg.Timeout); err != nil {
			return fmt.Errorf("no NRI plugin could connect to %s: %w\n"+
				"Check that the runtime has NRI enabled and external plugin "+
				"connections allowed (containerd: disable=false and "+
				"disable_connections=false; CRI-O: enable_nri=true and "+
				"nri_disable_connections=false)",
				cfg.NRISocketPath, err)
		}
		// Deferred inside this function, so it runs before withTimeout cancels the
		// context. Start ties the stub's Run goroutine to that context, so stopping
		// the probe first is what makes the shutdown orderly rather than a
		// cancellation the stub has to discover.
		defer probe.Stop()

		runtimeNRIVersion = probe.RuntimeNRIVersion()
		if runtimeNRIVersion == "" || runtimeNRIVersion == api.UnknownVersion {
			// Older runtimes do not advertise it; infer from the runtime version.
			if inferred, err := api.InferVersionFromRuntime(runtimeName, runtimeVersion); err == nil {
				runtimeNRIVersion = inferred
			}
		}

		return nil
	})
}

// Teardown releases the run's resources and prints the summary. Call it from
// TestMain after all tests, passing what m.Run() returned.
//
// The exit code is an argument because the summary cannot work it out. Only
// Suite.Expect* and the recorded driver calls reach a Report; every bare require
// and t.Errorf in the suite fails its test without recording anything, so an
// empty report is not the same fact as a clean run and m.Run's verdict is the
// only thing that can tell them apart. See Header.Failed.
//
// Cleanup and Close get separate timeouts for the same reason Setup's phases do:
// a cleanup that runs long must not leave Close with an already-expired context
// and so report a failure to close that never happened.
func Teardown(code int) {
	// Deferred rather than written at the end: it must print on the no-driver
	// path too -- a run that collected findings and then failed to produce a
	// driver must still say what it collected -- and it must print after the
	// cleanup warnings below, since the summary is meant to be the last thing a
	// run says.
	defer func() {
		TestReport.Print(os.Stdout, summaryHeader(code != 0))
	}()

	if driver == nil {
		return
	}

	if !TestConfig.KeepArtifacts {
		if err := withTimeout(TestConfig.Timeout, driver.Cleanup); err != nil {
			fmt.Printf("WARNING: cleanup failed: %v\n", err)
		}
	}

	if err := withTimeout(TestConfig.Timeout, driver.Close); err != nil {
		fmt.Printf("WARNING: closing driver failed: %v\n", err)
	}
}

// summaryHeader describes the run the summary belongs to.
//
// RuntimeHandler is the selected handler, which is also what Suite.PodSpec
// stamped on every workload, so the header states what actually produced the
// result. An empty one is the absence of a selection rather than a handler named
// "", and the summary prints it as the runtime's default; it must not be filled
// in from the discovered list, since which of those the runtime would have
// defaulted to is not something this suite knows.
//
// Taken through RuntimeHandler() rather than from TestConfig directly, so the
// header and the accessor cannot come to disagree about one run.
//
// failed comes from the caller because nothing in this package knows it: see
// Teardown and Header.Failed.
func summaryHeader(failed bool) Header {
	return Header{
		Runtime:        runtimeName,
		RuntimeVersion: runtimeVersion,
		NRIVersion:     runtimeNRIVersion,
		RuntimeHandler: RuntimeHandler(),
		RunID:          TestConfig.RunID,
		Collecting:     TestConfig.CollectFailures,
		Failed:         failed,
	}
}

// RuntimeName returns the runtime's name, as reported over CRI.
func RuntimeName() string { return runtimeName }

// RuntimeVersion returns the runtime's version, as reported over CRI.
func RuntimeVersion() string { return runtimeVersion }

// RuntimeNRIVersion returns the runtime's NRI version, advertised or inferred.
func RuntimeNRIVersion() string { return runtimeNRIVersion }

// suiteT is the part of *testing.T a Suite uses.
//
// It is an interface so that the framework's own unit tests can observe what a
// Suite reports: cleanup's leak guard reports through Errorf, and a failure
// reported to the real *testing.T would fail the test doing the observing. It is
// a superset of require.TestingT, so it can still be handed to require.
type suiteT interface {
	require.TestingT

	Helper()
	Logf(format string, args ...any)
	Name() string
}

// pluginStopper is all cleanup needs from a plugin.
//
// Declaring the plugin list as this interface rather than as []*TestPlugin is
// what lets a unit test put a fake in it and observe the plugin context at the
// moment Stop is called. That ordering -- plugins stopped first, plugin context
// cancelled after -- is what commit 409e62c fixed, and nothing else can pin it.
type pluginStopper interface {
	Stop()
}

// podSeq numbers generated pod names and uids uniquely within the process.
//
// Suite.seq is not enough on its own: every subtest builds a fresh Suite, so seq
// restarts at 0 and uniqueness rests entirely on the caseName passed to NewSuite,
// which is duplicated across rows of more than one table. containerd reserves a
// sandbox name for a live (name, namespace, uid, attempt) tuple, so a duplicate
// name plus a removal that has not completed yet makes the *next* subtest fail
// with "sandbox name is reserved" -- a failure attributed to the wrong test.
var podSeq atomic.Uint64

// Suite is one test case's view of the runtime. It names every workload it
// creates after the case, cleans them up when the case ends, and fails the case
// if anything leaks.
type Suite struct {
	t            suiteT
	caseName     string
	scope        *Scope
	q            *EventQ
	plugins      []pluginStopper
	pods         []*Pod
	ctrs         []*Container
	removed      map[string]bool
	seq          int
	pluginCtx    context.Context
	pluginCancel context.CancelFunc
}

// NewSuite returns a Suite for the given case, registering its cleanup.
func NewSuite(t *testing.T, caseName string) *Suite {
	t.Helper()

	if driver == nil {
		t.Fatal("framework.Setup has not been called")
	}

	pluginCtx, pluginCancel := context.WithCancel(context.Background())

	s := &Suite{
		t:            t,
		caseName:     caseName,
		scope:        TestConfig.Scope(),
		q:            &EventQ{},
		removed:      map[string]bool{},
		pluginCtx:    pluginCtx,
		pluginCancel: pluginCancel,
	}

	t.Cleanup(s.cleanup)

	return s
}

// Driver returns the active driver.
func (s *Suite) Driver() Driver { return driver }

// Inspector returns the active inspector.
func (s *Suite) Inspector() Inspector { return inspector }

// Executor returns the active executor, or nil when the driver has none. Gate on
// CapExec rather than testing this for nil.
func (s *Suite) Executor() Executor { return executor }

// Config returns the run's configuration.
func (s *Suite) Config() *Config { return TestConfig }

// Scope returns the run's scope.
func (s *Suite) Scope() *Scope { return s.scope }

// Q returns the event queue every plugin of this case records into.
func (s *Suite) Q() *EventQ { return s.q }

// NewPlugin returns a plugin for this case, subscribed to mask. It is not
// started; assign its callbacks, then pass it to StartPlugins.
func (s *Suite) NewPlugin(name, idx string, mask stub.EventMask) *TestPlugin {
	return NewTestPlugin(name, idx, mask, s.q, s.scope)
}

// StartPlugins starts plugins and waits for each to synchronize.
func (s *Suite) StartPlugins(plugins ...*TestPlugin) {
	s.t.Helper()

	for _, p := range plugins {
		// Record before asserting, for the reason CreatePod documents: a plugin
		// whose Start failed part way may still hold a connection, fatalf does not
		// return, and only cleanup can stop it.
		s.plugins = append(s.plugins, p)

		err := p.Start(s.pluginCtx, TestConfig.NRISocketPath, TestConfig.Timeout)
		s.fatalf("StartPlugins", err, "failed to start plugin %s", p.FullName())
	}
}

// StartPluginExpectingError starts a plugin that is expected to fail to
// register, and returns the error.
func (s *Suite) StartPluginExpectingError(p *TestPlugin) error {
	s.t.Helper()

	// Recorded whether or not Start succeeds, for the same reason StartPlugins
	// records before asserting: a plugin whose Start failed part way may still hold
	// a connection and still be recording into this case's queue, and only cleanup
	// can stop it. Stop is a no-op on a plugin that never connected, so recording
	// one that failed outright costs nothing.
	s.plugins = append(s.plugins, p)

	return p.Start(s.pluginCtx, TestConfig.NRISocketPath, TestConfig.Timeout)
}

// caseLabels returns the labels every workload of this case must carry.
//
// The case label is taken from the test's name, not from the caseName passed to
// NewSuite: t.Name() is unique by construction, and telling a leaked pod's reader
// which case created it is the label's whole purpose. caseName duplicates across
// table rows, so as a label it can point at the wrong row.
func (s *Suite) caseLabels() map[string]string {
	return TestConfig.PodLabels(labelValue(s.t.Name()))
}

// PodSpec returns a pod spec for this case, uniquely named and carrying the
// run-id and case labels and annotations that keep plugins scoped to it.
//
// The generated name and uid carry a process-wide sequence number as well as the
// per-Suite one, so they are unique whatever caseName the caller passed. See
// podSeq for why that matters.
func (s *Suite) PodSpec(name string, ctrs ...*ContainerSpec) *PodSpec {
	s.seq++

	uniq := podSeq.Add(1)
	labels := s.caseLabels()

	return &PodSpec{
		Name: fmt.Sprintf("nri-test-%s-%s-%d-%d", s.caseName, name, s.seq, uniq),
		// The configured namespace, not a hardcoded one: it is the suite's outer
		// isolation guard, and plugins are scoped to it, so a suite creating pods
		// anywhere else would be creating pods its own plugins ignore.
		Namespace: TestConfig.Namespace,
		UID:       fmt.Sprintf("%s-%s-%d-%d", TestConfig.RunID, name, s.seq, uniq),
		// Both labels and annotations, so a plugin can scope itself either way
		// and so the driver can list this run's pods by label.
		Labels:      labels,
		Annotations: maps.Clone(labels),
		Containers:  ctrs,
		// Stamped here rather than left to each test, for the same reason the
		// namespace is: a test that forgot would create its workloads with the
		// runtime's default handler while the run's summary named the selected one,
		// which is a false result rather than a missing one. Empty stays empty and
		// asks the runtime for its default.
		RuntimeHandler: TestConfig.RuntimeHandler,
	}
}

// KeepAliveCommand returns the command that keeps a test container running. It
// is a shell with a TERM trap rather than a bare `sleep`: in a container's own
// PID namespace (kubelet's default) the command is PID 1, for which the kernel
// drops signals that have no handler, so a bare `sleep` ignores SIGTERM and
// every stop waits out the whole grace period. The loop sleeps one second at a
// time because a trap only runs between foreground commands. busybox has sh,
// and `sleep 1` needs no GNU extension.
func KeepAliveCommand() []string {
	return []string{"/bin/sh", "-c", "trap 'exit 0' TERM; while true; do sleep 1; done"}
}

// CtrSpec returns a container spec named within this case, carrying a keep-alive
// command.
func (s *Suite) CtrSpec(name string) *ContainerSpec {
	return &ContainerSpec{
		Name:  name,
		Image: TestConfig.Image,
		// A keep-alive command, overriding the image's own. DO NOT REMOVE IT: the
		// default image is busybox, whose default command is a shell that finds no
		// TTY and no stdin and exits immediately, so a started container would be
		// gone before anything could look inside it and every exec-verified
		// assertion would fail on a container that is not running. pause needed no
		// such command because blocking forever is the only thing it does; a real
		// image has to be told.
		//
		// A shell loop that traps TERM, see KeepAliveCommand.
		Command:     KeepAliveCommand(),
		Labels:      s.caseLabels(),
		Annotations: s.caseLabels(),
	}
}

// labelValue makes s usable as a Kubernetes label value: at most 63 characters
// of alphanumerics, '-', '_' and '.', starting and ending alphanumeric.
//
// Test names are not: t.Name() contains '/' between the levels of a subtest, may
// contain punctuation from the subtest description, and easily exceeds 63
// characters. No runtime is known to reject an ill-formed label, but a pod
// creation refused for the sake of a purely informational label would fail every
// test in the suite for a reason that has nothing to do with NRI.
//
// An over-long value keeps its tail rather than its head, because the shared
// prefix of a table's rows is the part that does not identify the case.
func labelValue(s string) string {
	const maxLen = 63

	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, s)

	if len(mapped) > maxLen {
		mapped = mapped[len(mapped)-maxLen:]
	}

	mapped = strings.Trim(mapped, "-_.")
	if mapped == "" {
		return "unnamed"
	}

	return mapped
}

// CreatePod creates a pod, failing the case if it cannot.
//
// The containers in the spec are created with the pod and recorded with it, so the
// leak guard attributes them to this case.
func (s *Suite) CreatePod(spec *PodSpec) *Pod {
	s.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), TestConfig.Timeout)
	defer cancel()

	pod, err := driver.CreatePod(ctx, spec)
	// Record before asserting: a partially created pod must still be cleaned up,
	// and fatalf does not return.
	if pod != nil {
		s.pods = append(s.pods, pod)
		s.ctrs = append(s.ctrs, pod.Containers...)
	}
	s.fatalf("CreatePod", err, "failed to create pod %s", spec.Name)

	return pod
}

// CreatePodWithContainer creates a pod named name that carries the one
// container ctr, failing the case if it cannot. It is the usual way to get a
// workload: it works with every driver, and the leak guard attributes the
// container to this case.
func (s *Suite) CreatePodWithContainer(name string, ctr *ContainerSpec) (*Pod, *Container) {
	s.t.Helper()

	pod := s.CreatePod(s.PodSpec(name, ctr))
	require.Len(s.t, pod.Containers, 1,
		"the driver must report the container it created with pod %s", pod.Spec.Name)

	return pod, pod.Containers[0]
}

// CreateContainer creates a container, failing the case if it cannot.
//
// It needs CapContainerLifecycle. Prefer CreatePodWithContainer unless the test
// is about creating a container apart from its pod.
func (s *Suite) CreateContainer(pod *Pod, spec *ContainerSpec) *Container {
	s.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), TestConfig.Timeout)
	defer cancel()

	ctr, err := driver.CreateContainer(ctx, pod, spec)
	// Record before asserting, for the reason CreatePod documents: fatalf does not
	// return, so a container appended after it would never reach the leak guard --
	// and a creation that failed part way is exactly the case the container guard
	// exists for.
	if ctr != nil {
		s.ctrs = append(s.ctrs, ctr)
	}
	s.fatalf("CreateContainer", err, "failed to create container %s", spec.Name)

	return ctr
}

// Inspect returns a container's realized state, failing the case if it cannot.
func (s *Suite) Inspect(id string) *Observed {
	s.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), TestConfig.Timeout)
	defer cancel()

	o, err := inspector.Inspect(ctx, id)
	s.fatalf("Inspect", err, "failed to inspect container %s", id)

	return o
}

// Exec runs cmd inside a running container and returns its trimmed standard
// output, failing the case if the command cannot be run or exits non-zero.
//
// Trimmed because every caller wants it that way: what is being read is a line
// out of /proc or /sys, and the trailing newline is never part of the answer. A
// test that needs the bytes as they came, or that means to assert the command
// failed, wants TryExec.
//
// The trim is safe for the callers that read a whole file rather than a line --
// ExpectEnv's /proc/self/environ, ExpectMount's /proc/self/mounts,
// ProcSelfStatus's /proc/self/status. NUL is not Unicode whitespace, so the
// environ's separators and its trailing terminator all survive; measured against
// a real 4049-byte environ, the trim is a byte-for-byte no-op and all 57 entries
// come through. Nothing upstream reshapes the bytes either: the CRI executor
// returns string(stdout) as it came.
func (s *Suite) Exec(ctr *Container, cmd ...string) string {
	s.t.Helper()

	stdout, stderr, err := s.TryExec(ctr, cmd...)
	s.fatalf("Exec", err,
		"failed to exec %v in container %s (stdout %q, stderr %q)",
		cmd, ctr.ID, stdout, stderr)

	return strings.TrimSpace(stdout)
}

// TryExec runs cmd inside a running container and returns its output and error
// rather than failing the case, for a test that asserts on a failure.
//
// The output is returned whichever way the command went: a non-zero exit is an
// error, and what the command printed before exiting is the diagnosis.
func (s *Suite) TryExec(ctr *Container, cmd ...string) (string, string, error) {
	s.t.Helper()

	if executor == nil {
		// Reported as an error rather than as a failure, so Exec's require and a
		// caller's own assertion both see it. A test reaching this did not gate on
		// CapExec, which is the actual bug.
		return "", "", errors.New("the active driver provides no Executor; " +
			"a test that execs must gate on framework.CapExec")
	}

	var stdout, stderr string

	// Through s.do, so the call is bounded by Config.Timeout like every other
	// driver call. Doubly load-bearing here: the CRI executor derives the exec's
	// own timeout from this context's deadline, so an exec run without one would
	// ask the runtime to wait forever for a command that never returns.
	err := s.do(func(ctx context.Context) error {
		var err error
		stdout, stderr, err = executor.Exec(ctx, ctr.ID, cmd)

		return err
	})

	return stdout, stderr, err
}

// ProcSelfStatus returns the parsed /proc/self/status of a command run inside a
// container, failing the case if it cannot be read.
//
// /proc/self, never /proc/1. With a pod-wide PID namespace -- cri-api's default
// NamespaceOption.Pid of POD, selected by -nri-test.cri-namespaces=cri, or a pod
// that shares its process namespace -- PID 1 is the sandbox's pause process,
// whose affinity is the sandbox's, not this container's. Measured on
// containerd v2.3.3 with CpusetCpus "0-1": /proc/self/status reported
// Cpus_allowed_list 0-1 while /proc/1/status reported 0-43, the host's every CPU.
// /proc/self is correct whether or not a test sets Pid=CONTAINER, so it is the
// only form to use. -nri-test.proc-view=init reads /proc/1 instead, as a
// cross-check that is valid only with a per-container PID namespace. By default the suite sends kubelet's namespace options, a
// PID namespace per container, where PID 1 is the container's own init and
// agrees with /proc/self; the choice of /proc/self does not depend on it.
// (oom_score_adj is the exception, and is read from /proc/1/oom_score_adj only
// where PID 1 is the container's init.)
func (s *Suite) ProcSelfStatus(ctr *Container) map[string]string {
	s.t.Helper()

	path := "/proc/self/status"
	if TestConfig.ProcView == ProcViewInit {
		path = "/proc/1/status"
	}

	return ProcStatus(s.Exec(ctr, "cat", path))
}

// expect records an unmet expectation and reports it, ending the case unless
// the run collects failures.
//
// Both modes record the finding: the mode decides when the case stops, not
// whether the failure is reported, so the run's summary and its exit code are
// the same either way. Recording before reporting is what makes that true in
// the default mode as well, where require.NoError does not return.
//
// Expectations go through here; driver calls go through fatalf, which records
// just as this does and then aborts whatever the mode says. A collected
// CreateContainer failure would leave the body running with a nil *Container,
// and the derived panics and follow-on findings would bury the one real
// failure in the summary this exists to make readable.
func (s *Suite) expect(feature, surface string, err error) {
	s.t.Helper()

	if err == nil {
		return
	}

	TestReport.AddFinding(Finding{
		Kind:    KindExpectation,
		Test:    s.t.Name(),
		Feature: feature,
		Surface: surface,
		Err:     err,
	})

	if TestConfig.CollectFailures {
		s.t.Errorf("%v", err)

		return
	}

	require.NoError(s.t, err)
}

// fatalf records a failed operation and ends the case, unconditionally.
//
// expect's sibling, and deliberately not expect's twin. The mode decides
// whether an unmet expectation stops the case; it does not apply here, because
// what failed is a call the rest of the body depends on -- a collected
// CreateContainer failure leaves the body running with a nil *Container, and
// the derived panics would bury the one real failure. So: record, then abort,
// every time.
//
// Recording is what was missing. A fatal driver call used to be reported to the
// test and nowhere else, so the first CRI-O sweep ended 34 tests on a refused
// CreateContainer and then printed "No expectation failures were collected" --
// a suite reporting success while asserting nothing, which is worse than
// printing no summary at all, because a reader believes it.
//
// format and args are the wrapper's own message, naming the pod or container
// involved. It goes into the recorded error rather than only to require,
// because a finding is read in a summary far from the output require writes to;
// ctrError does the same for expectations and for the same reason.
func (s *Suite) fatalf(op string, err error, format string, args ...any) {
	s.t.Helper()

	if err == nil {
		return
	}

	// Formatted once and used twice, so the finding and the test's own output
	// cannot come to describe the same failure differently. testify takes a
	// single already-formatted string verbatim, which is what keeps each
	// wrapper's message exactly what it was before it was routed through here.
	msg := fmt.Sprintf(format, args...)

	TestReport.AddFinding(Finding{
		Kind:    KindOperation,
		Test:    s.t.Name(),
		Feature: op,
		Err:     fmt.Errorf("%s: %w", msg, err),
	})

	require.NoError(s.t, err, msg)
}

// CapturedAt waits for a container hook to arrive and returns the payload the
// plugin was given, or nil after recording a finding if it never arrived.
//
// The wait is what makes this safe to call straight after a driver operation:
// the hooks run on the plugin's own goroutine, so a test reading the capture
// directly would race the callback and read nothing on a fast node.
//
// A missing hook is a finding here rather than a nil for the caller to check.
// A test that went on to read a field off a nil capture would panic, and the
// panic would bury the finding in a stack trace; and the hook not arriving is
// itself a conformance statement worth reporting under its own name.
func (s *Suite) CapturedAt(p *TestPlugin, hook EventType, ctr *Container) *Captured {
	s.t.Helper()

	return s.capturedAt(p, hook, ctr.PodID, ctr.ID)
}

// CapturedPodAt is CapturedAt for a pod hook, which carries no container.
func (s *Suite) CapturedPodAt(p *TestPlugin, hook EventType, pod *Pod) *Captured {
	s.t.Helper()

	return s.capturedAt(p, hook, pod.ID, "")
}

func (s *Suite) capturedAt(p *TestPlugin, hook EventType, podID, ctrID string) *Captured {
	s.t.Helper()

	_, err := s.Q().WaitFor(&Event{
		Type:   hook,
		Plugin: p.FullName(),
		PodID:  podID,
		CtrID:  ctrID,
	}, s.Timeout())
	if err != nil {
		s.expect(string(hook), "hook request",
			fmt.Errorf("plugin %s was never invoked for %s on pod %s container %q: %w",
				p.FullName(), hook, podID, ctrID, err))

		return nil
	}

	got := p.Captured(hook, podID, ctrID)
	if got == nil {
		// The event arrived and the payload did not. That is this suite looking
		// in the wrong place -- capture was never turned on, or a hook recorded
		// its event without retaining anything -- and not the runtime failing
		// to report something.
		//
		// So fatalf, not expect. An expectation is a statement about the
		// runtime, and recording a harness bug as one would put a conformance
		// finding about our own defect into a summary somebody quotes. A failed
		// operation is the right channel: nothing about the runtime's NRI
		// behaviour was measured here.
		s.fatalf("Captured",
			fmt.Errorf("plugin %s received %s but retained no payload; was "+
				"Capture(true) called before StartPlugins?", p.FullName(), hook),
			"cannot read the %s payload for pod %s container %q", hook, podID, ctrID)
	}

	return got
}

// expectInput records an input conformance finding under the field's own name.
//
// A thin wrapper over expect so that every input assertion names the field as
// its Feature and where the answer was read as its Surface. Those two are what
// make a finding readable in a summary long after the test output scrolled
// past: "CDI_devices missing from the hook request" and "CDI_devices named in
// the request but absent from the container" are different findings about a
// runtime, and only Surface separates them.
func (s *Suite) expectInput(field, surface string, err error) {
	s.t.Helper()

	s.expect(field, surface, err)
}

// ExpectShown records a fidelity finding: the plugin was not shown what the
// test set. The surface is the hook request, because that is the only thing
// such a check read.
func (s *Suite) ExpectShown(field string, err error) {
	s.t.Helper()

	s.expectInput(field, "hook request", err)
}

// ExpectAgrees records an agreement finding: the plugin's view and what the
// runtime realized disagree. The surface names where the second read came
// from -- the realized OCI spec, CRI status, or the container's own kernel.
func (s *Suite) ExpectAgrees(field, surface string, err error) {
	s.t.Helper()

	s.expectInput(field, surface, err)
}

// ExpectAnnotation fails the case unless the container's OCI spec carries the
// annotation. See ExpectAnnotation for which surface this reads and why -- in
// particular, that it verifies an NRI adjustment rather than a CRI annotation.
func (s *Suite) ExpectAnnotation(ctr *Container, key, want string) {
	s.t.Helper()

	err := ExpectAnnotation(s.Inspect(ctr.ID).Spec, key, want)
	s.expect("annotation", "OCI spec", ctrError(ctr, err))
}

// ExpectEnv fails the case unless both the container's OCI spec and the
// environment of a command run inside the container have the variable. See
// ExpectEnv for what that second read does and does not prove -- it is not an
// independent witness, since the runtime builds an exec'd process's environment
// from the same stored spec -- and note that gathering both is why this does one
// Inspect and one Exec.
func (s *Suite) ExpectEnv(ctr *Container, key, want string) {
	s.t.Helper()

	spec := s.Inspect(ctr.ID).Spec
	environ := s.Exec(ctr, "cat", "/proc/self/environ")

	s.expect("env", "OCI spec + /proc/self/environ",
		ctrError(ctr, ExpectEnv(spec, environ, key, want)))
}

// ExpectMount fails the case unless both the container's OCI spec and the
// container's own mount table have a mount at destination, and, when wantOpts
// is given, the mount table records each of them for it. See ExpectMount for
// why both are required, and for why wantOpts is matched against the mount
// table alone.
func (s *Suite) ExpectMount(ctr *Container, destination string, wantOpts ...string) {
	s.t.Helper()

	spec := s.Inspect(ctr.ID).Spec
	mounts := s.Exec(ctr, "cat", "/proc/self/mounts")

	s.expect("mount", "OCI spec + /proc/self/mounts",
		ctrError(ctr, ExpectMount(spec, mounts, destination, wantOpts...)))
}

// ExpectCpuset fails the case unless the kernel inside the container allows
// exactly the CPUs asked for. No Inspect: the kernel alone is authoritative
// here, for the reason ExpectCpuset documents, and the spec would only say what
// the runtime recorded.
func (s *Suite) ExpectCpuset(ctr *Container, want string) {
	s.t.Helper()

	err := ExpectCpuset(s.ProcSelfStatus(ctr), want)
	s.expect("cpuset", "/proc/self/status", ctrError(ctr, err))
}

// ExpectMems fails the case unless the kernel inside the container allows
// exactly the NUMA memory nodes asked for. The kernel alone, as with
// ExpectCpuset.
func (s *Suite) ExpectMems(ctr *Container, want string) {
	s.t.Helper()

	err := ExpectMems(s.ProcSelfStatus(ctr), want)
	s.expect("mems", "/proc/self/status", ctrError(ctr, err))
}

// ctrError names the container an expectation failed for, or returns nil for a
// met expectation.
//
// The container id used to reach the reader as require.NoError's message
// argument, which a recorded Finding has no equivalent of: a finding carries
// one error and is read in a summary far from the test's own output, so the id
// has to be in the error itself.
func ctrError(ctr *Container, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("container %s: %w", ctr.ID, err)
}

// do runs a driver call under a fresh TestConfig.Timeout budget.
//
// Every driver call a test makes goes through one of the wrappers around it --
// the lifecycle ones below, Exec above -- rather than through Driver() and a bare
// context.Background(). An unbounded call on a
// runtime that wedges hangs until go test's own -timeout, which panics the whole
// process: no t.Cleanup runs, so this case's leak guard never fires, Teardown
// never runs either, and the workloads genuinely leak. One stuck RPC would
// otherwise produce exactly the outcome the leak guard exists to prevent.
func (s *Suite) do(fn func(context.Context) error) error {
	return withTimeout(TestConfig.Timeout, fn)
}

// StartContainer starts a container, failing the case if it cannot.
func (s *Suite) StartContainer(ctr *Container) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.StartContainer(ctx, ctr)
	})
	s.fatalf("StartContainer", err, "failed to start container %s", ctr.ID)
}

// StopContainer stops a container, failing the case if it cannot.
func (s *Suite) StopContainer(ctr *Container) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.StopContainer(ctx, ctr)
	})
	s.fatalf("StopContainer", err, "failed to stop container %s", ctr.ID)
}

// RemoveContainer removes a container, failing the case if it cannot.
func (s *Suite) RemoveContainer(ctr *Container) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.RemoveContainer(ctx, ctr)
	})
	s.fatalf("RemoveContainer", err, "failed to remove container %s", ctr.ID)
}

// UpdateContainerResources updates a container's resources, failing the case if
// it cannot.
func (s *Suite) UpdateContainerResources(ctr *Container, r *Resources) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.UpdateContainerResources(ctx, ctr, r)
	})
	s.fatalf("UpdateContainerResources", err, "failed to update resources of container %s", ctr.ID)
}

// StopPod stops a pod, failing the case if it cannot.
func (s *Suite) StopPod(pod *Pod) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.StopPod(ctx, pod)
	})
	s.fatalf("StopPod", err, "failed to stop pod %s", pod.ID)
}

// RemovePod removes a pod, failing the case if it cannot.
//
// A pod removed here is remembered as removed, so cleanup does not try again and
// report a not-found it provoked itself. It stays in s.pods regardless, so that
// if the removal did not take effect the leak guard still names it as this case's.
func (s *Suite) RemovePod(pod *Pod) {
	s.t.Helper()

	err := s.do(func(ctx context.Context) error {
		return driver.RemovePod(ctx, pod)
	})
	if err == nil {
		s.markRemoved(pod)
	}
	s.fatalf("RemovePod", err, "failed to remove pod %s", pod.ID)
}

func (s *Suite) markRemoved(pod *Pod) {
	if s.removed == nil {
		s.removed = map[string]bool{}
	}

	s.removed[pod.ID] = true
}

// Timeout returns the configured per-operation timeout.
func (s *Suite) Timeout() time.Duration { return TestConfig.Timeout }

// droppedUpdates returns the container ids of the updates this case's plugins had
// dropped by the filter, in recording order and without repeats.
func (s *Suite) droppedUpdates() []string {
	var ids []string

	for _, e := range s.q.Events() {
		if e.Type != UpdateDropped {
			continue
		}

		if !slices.Contains(ids, e.CtrID) {
			ids = append(ids, e.CtrID)
		}
	}

	return ids
}

// cleanup removes this case's workloads, stops its plugins and guards against
// leaks. Registered by NewSuite, so it runs even when the case fails.
func (s *Suite) cleanup() {
	// Defer cancelling the plugin context so it happens after plugins are stopped,
	// ensuring an orderly shutdown: Stop calls close(doneC) which waits for the
	// server goroutine, so the context is still live when Run's select completes
	// and returns (logging "ttrpc server closed" rather than calling Stop again).
	defer s.pluginCancel()

	removeErrs := s.removePods()

	for _, p := range s.plugins {
		p.Stop()
	}

	// A dropped update means one of this case's plugins returned an update naming a
	// container outside the run, and the filter refused it. Recording the drop is
	// not enough on its own: nothing else in the suite reads UpdateDropped -- the
	// wholesale event scan skips non-hooks, correctly, since a drop is not an
	// unsubscribed hook -- so the queue entry would be a record no assertion looks
	// at, and a case whose request was silently emptied would still pass. That is
	// the failure the event exists to prevent, so it is failed here.
	//
	// Unconditional, with no way to opt out. A per-case latch muted the guard for
	// every later drop in the case, across all its plugins, since they share one
	// queue -- the same shape of hole the guard exists to close. A case that
	// legitimately trips the filter will need an exception naming the container ids
	// it expects to be dropped, and that belongs with the first case that wants one.
	//
	// Checked after the plugins are stopped, so no further callback can add to the
	// queue, and before the KeepArtifacts return, because this is a statement about
	// the test rather than about leftover workloads.
	if dropped := s.droppedUpdates(); len(dropped) > 0 {
		s.t.Errorf("this case returned %d update(s) for container(s) outside the "+
			"run, which were dropped rather than sent: %s; the case did not do what "+
			"it asked for",
			len(dropped), strings.Join(dropped, ", "))
	}

	if TestConfig.KeepArtifacts {
		return
	}

	// ListRunPods is a required method on Driver: the leak guard must not be
	// something a driver can silently opt out of, since noticing a missed cleanup
	// is precisely its job.
	//
	// Its own budget, for the reason withTimeout documents. Sharing one deadline
	// with the removals above is what made the guard disable itself exactly when it
	// was needed: the removals that leaked the pods also exhausted the budget, so
	// ListRunPods was called with an expired context, returned an error, and the
	// error was discarded.
	var leaked []string

	listErr := withTimeout(TestConfig.Timeout, func(ctx context.Context) error {
		ids, err := driver.ListRunPods(ctx)
		leaked = ids

		return err
	})

	// A stage of its own, with a budget of its own, for the same reason. A container
	// guard sharing the pod guard's context would go quiet whenever the pod listing
	// was slow, which is exactly the shape of hole the per-stage budgets close.
	//
	// It is a separate question from the pod guard, not a refinement of it: a
	// container left behind by a creation the runtime rejected sits in a pod the
	// case created on purpose and then removed, so the pod guard is silent about it
	// either way.
	var leakedCtrs []string

	ctrListErr := withTimeout(TestConfig.Timeout, func(ctx context.Context) error {
		ids, err := driver.ListRunContainers(ctx)
		leakedCtrs = ids

		return err
	})

	// The removal failures are the direct cause of any leak, so report them before
	// the leak itself. On their own they are only a note -- the case may
	// legitimately have removed the pod already -- but if either guard did find
	// something, or could not look, they are the diagnosis.
	if len(removeErrs) > 0 {
		report := s.t.Logf
		if listErr != nil || ctrListErr != nil || len(leaked) > 0 || len(leakedCtrs) > 0 {
			report = s.t.Errorf
		}

		report("cleanup could not remove %d of this case's pod(s): %s",
			len(removeErrs), strings.Join(removeErrs, "; "))
	}

	// Every outcome is reported, and one failing stage never returns early on
	// another's behalf. An `err == nil &&` condition in this position is what
	// disabled the guard once already; a `return` after the first failure is the
	// same mistake wearing different clothes, since it would hide the second
	// stage's answer whenever the first could not answer at all.
	if listErr != nil {
		s.t.Errorf("leak guard could not list this run's pods: %v", listErr)
	}

	if ctrListErr != nil {
		s.t.Errorf("leak guard could not list this run's containers: %v", ctrListErr)
	}

	if len(leaked) > 0 {
		s.reportLeak(leaked)
	}

	if len(leakedCtrs) > 0 {
		s.reportContainerLeak(leakedCtrs)
	}

	// Deliberately no driver.Cleanup here. Teardown already runs one run-wide
	// cleanup, with better-scoped intent; a per-case call would remove the pods of
	// whatever else is using the namespace, which is exactly what makes an
	// unrelated pod's presence destructive rather than merely noisy.
}

// removePods stops and removes the pods this case created, returning a
// description of every failure.
//
// Each pod gets a budget of its own rather than all of them sharing one, for the
// reason withTimeout documents: a slow removal must not consume the next pod's
// time, nor the leak guard's.
//
// Removal is best effort, because the case may already have removed the pod. The
// errors are still returned: they are the direct cause of any leak, and reporting
// the leak without them leaves the operator with the symptom and no cause.
func (s *Suite) removePods() []string {
	if TestConfig.KeepArtifacts {
		return nil
	}

	var errs []string

	for _, pod := range s.pods {
		if s.removed[pod.ID] {
			continue
		}

		err := s.do(func(ctx context.Context) error {
			// Attempt the removal even when the stop failed: both containerd and
			// CRI-O force-stop a sandbox from inside RemovePodSandbox, and removal
			// is what actually prevents the leak.
			return errors.Join(driver.StopPod(ctx, pod), driver.RemovePod(ctx, pod))
		})
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", pod.ID, err))

			continue
		}

		s.markRemoved(pod)
	}

	return errs
}

// reportLeak fails the case, naming which of the leaked pods it created and which
// it did not.
//
// The distinction matters because a pod in the run's namespace that carries no run
// id is treated as ours by design (see Scope.Matches), so a stray from a killed
// earlier run -- or a user's own pod in a namespace that happens to be called
// nri-test -- is reported by every single case. Saying which ids are this case's
// turns that cascade into a message an operator can act on.
func (s *Suite) reportLeak(leaked []string) {
	// The namespace is a checked fact here: it is how the pod matched at all, since
	// Scope.Matches rejects any pod outside it.
	kind := leakKind{
		noun:  "pod",
		where: " in namespace " + TestConfig.Namespace,
		stray: "an unrelated pod in that namespace",
	}

	s.reportLeakOf(kind, leaked, func(id string) bool {
		return slices.ContainsFunc(s.pods, func(p *Pod) bool { return p != nil && p.ID == id })
	})
}

// reportContainerLeak fails the case, naming which of the leaked containers it
// created and which it did not. Same rule as reportLeak, and same reason: a
// container in the run's namespace carrying no run id is ours by design.
//
// The container message deliberately names no namespace, unlike the pod one. A
// container has no namespace of its own, and the driver's second arm matches one on
// its run-id label alone, in a pod it knows nothing about -- which is the whole
// point of that arm. Printing the run's namespace for such a container would state
// something nobody checked and send its reader looking in the wrong place.
func (s *Suite) reportContainerLeak(leaked []string) {
	kind := leakKind{
		noun:  "container",
		where: "",
		stray: "an unrelated container that matches this run",
	}

	s.reportLeakOf(kind, leaked, func(id string) bool {
		return slices.ContainsFunc(s.ctrs, func(c *Container) bool { return c != nil && c.ID == id })
	})
}

// leakKind is what a leak guard says about the things it found.
//
// where and stray exist because the pod message states a namespace twice and the
// container message must not state one at all; see reportContainerLeak. Keeping
// them here rather than branching inside reportLeakOf keeps the two guards on one
// piece of wording.
type leakKind struct {
	// noun is what leaked, singular, used as "%s(s)".
	noun string
	// where locates the leak, already prefixed with a space, or is empty when we
	// cannot honestly locate it.
	where string
	// stray describes the innocent explanation an operator should rule out.
	stray string
}

// reportLeakOf is the wording both guards share, parameterised by what leaked and
// by how to tell whether this case created it. Shared so the two can never drift
// into describing the same situation differently.
func (s *Suite) reportLeakOf(kind leakKind, leaked []string, mine func(id string) bool) {
	var ours, others []string

	for _, id := range leaked {
		if mine(id) {
			ours = append(ours, id)
		} else {
			others = append(others, id)
		}
	}

	switch {
	case len(ours) > 0 && len(others) > 0:
		s.t.Errorf("case leaked %d %s(s) it created: %v; "+
			"%d further %s(s)%s match this run but were not created "+
			"by this case: %v",
			len(ours), kind.noun, ours, len(others), kind.noun, kind.where, others)
	case len(ours) > 0:
		s.t.Errorf("case leaked %d %s(s) it created: %v", len(ours), kind.noun, ours)
	default:
		s.t.Errorf("%d %s(s)%s match this run but were not created "+
			"by this case: %v; they pre-existed it, so look for an earlier case that "+
			"leaked, a killed earlier run, or %s",
			len(others), kind.noun, kind.where, others, kind.stray)
	}
}
