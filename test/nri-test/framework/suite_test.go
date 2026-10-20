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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/containerd/nri/pkg/api"
)

func TestWithTimeoutGivesEachCallAnIndependentBudget(t *testing.T) {
	// The point of the helper: a call that consumes most of its budget must not
	// shorten the next call's. Two sequential calls each get the full timeout, so
	// both see a deadline comfortably in the future.
	const timeout = 200 * time.Millisecond

	var remaining []time.Duration

	for range 2 {
		err := withTimeout(timeout, func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "withTimeout must install a deadline")

			time.Sleep(timeout / 2)

			remaining = append(remaining, time.Until(deadline))

			return nil
		})
		require.NoError(t, err)
	}

	require.Len(t, remaining, 2)
	for i, left := range remaining {
		require.Positive(t, left, "call %d ran out of budget", i)
	}
}

func TestWithTimeoutKeepsTheContextLiveUntilTheFunctionHasReturned(t *testing.T) {
	// preflight relies on this: it stops its probe from a defer, and the probe's
	// stub Run goroutine is tied to the context withTimeout supplies. If cancel
	// fired before the function's defers, the stub would be torn down by
	// cancellation instead of by an orderly Stop.
	var errWhenDeferRan error

	err := withTimeout(time.Minute, func(ctx context.Context) error {
		defer func() { errWhenDeferRan = ctx.Err() }()

		return nil
	})
	require.NoError(t, err)
	require.NoError(t, errWhenDeferRan,
		"the context must still be live while the function's defers run")
}

func TestWithTimeoutReturnsTheFunctionsError(t *testing.T) {
	sentinel := errors.New("boom")

	err := withTimeout(time.Minute, func(context.Context) error {
		return sentinel
	})

	require.ErrorIs(t, err, sentinel)
}

// fakeT stands in for *testing.T where a test needs to observe what a Suite
// reports. A Suite reports a leak through Errorf, so handing it the real
// *testing.T would fail the test doing the observing.
type fakeT struct {
	name   string
	errors []string
	logs   []string
}

func (f *fakeT) Helper() {}

func (f *fakeT) Name() string {
	if f.name == "" {
		return "fakeT"
	}

	return f.name
}

func (f *fakeT) Errorf(format string, args ...any) {
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

func (f *fakeT) Logf(format string, args ...any) {
	f.logs = append(f.logs, fmt.Sprintf(format, args...))
}

// FailNow is only reachable through require, which no cleanup path uses. Panic
// rather than return, so a test that trips it says so instead of continuing with
// a half-failed Suite.
func (f *fakeT) FailNow() {
	panic("FailNow called on a fakeT: " + strings.Join(f.errors, "; "))
}

func (f *fakeT) allErrors() string { return strings.Join(f.errors, "\n") }
func (f *fakeT) allLogs() string   { return strings.Join(f.logs, "\n") }

var _ suiteT = &fakeT{}

// fakePlugin observes a context at the moment it is stopped. It is what lets a
// test pin the ordering between cleanup's plugin loop and its pluginCancel.
type fakePlugin struct {
	observe func() error

	stops     int
	ctxErrs   []error
	stoppedAt int
}

func (p *fakePlugin) Stop() {
	p.stops++
	p.ctxErrs = append(p.ctxErrs, p.observe())
}

var _ pluginStopper = &fakePlugin{}

// fakeDriver is a Driver whose cleanup-relevant methods are programmable, so a
// test can make removal slow, make it fail, and control what the leak guard sees.
type fakeDriver struct {
	noOpDriver

	// caps is what the driver advertises. CapContainerLifecycle is what decides
	// whether CreatePod accepts containers in the spec.
	caps Capability
	// opDelay is how long StopPod and RemovePod take. A delay long enough that
	// several pods cannot share one budget is how the shared-deadline defect is
	// reproduced.
	opDelay time.Duration
	// podCtrs are the containers CreatePod reports having created.
	podCtrs []*Container
	// createPodErr is what CreatePod fails with. The pod is returned alongside
	// it, as a real driver's partially created sandbox is, so the suite's
	// record-before-asserting path is exercised too.
	createPodErr error
	stopErr      error
	removeErr    error
	listPods     []string
	listErr      error
	// listDelay is how long ListRunPods takes. Longer than the timeout is how a
	// budget the pod guard shares with the container guard is reproduced: the pod
	// listing spends it, and the container listing is handed the remains.
	listDelay  time.Duration
	listCtrs   []string
	listCtrErr error
	// createdCtr is what CreateContainer returns, alongside createCtrErr. Both at
	// once models a partially created container: the id exists and must be tracked,
	// but the creation failed.
	createdCtr   *Container
	createCtrErr error

	createdPods    []string
	stopped        []string
	removed        []string
	cleanups       int
	listCalls      int
	listCtxErrs    []error
	listCtrCalls   int
	listCtrCtxErrs []error
}

func (d *fakeDriver) Capabilities() Capability { return d.caps }

// ConflictsWithKubelet makes fakeDriver kubelet-sensitive, as the CRI driver is.
// A driver that is not sensitive is modelled by noOpDriver, which lacks it.
func (d *fakeDriver) ConflictsWithKubelet() bool { return true }

func (d *fakeDriver) CreatePod(_ context.Context, spec *PodSpec) (*Pod, error) {
	d.createdPods = append(d.createdPods, spec.Name)

	return &Pod{ID: spec.Name, Spec: spec, Containers: d.podCtrs}, d.createPodErr
}

// wait models an RPC that takes opDelay and fails if the context expires first.
func (d *fakeDriver) wait(ctx context.Context) error {
	if d.opDelay == 0 {
		return ctx.Err()
	}

	select {
	case <-time.After(d.opDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *fakeDriver) StopPod(ctx context.Context, p *Pod) error {
	if err := d.wait(ctx); err != nil {
		return fmt.Errorf("stop %s: %w", p.ID, err)
	}

	d.stopped = append(d.stopped, p.ID)

	return d.stopErr
}

func (d *fakeDriver) RemovePod(ctx context.Context, p *Pod) error {
	if err := d.wait(ctx); err != nil {
		return fmt.Errorf("remove %s: %w", p.ID, err)
	}

	d.removed = append(d.removed, p.ID)

	return d.removeErr
}

func (d *fakeDriver) ListRunPods(ctx context.Context) ([]string, error) {
	d.listCalls++
	d.listCtxErrs = append(d.listCtxErrs, ctx.Err())

	if d.listDelay > 0 {
		select {
		case <-time.After(d.listDelay):
		case <-ctx.Done():
		}
	}

	// A real driver's RPC fails outright on an expired context. Modelling that is
	// the whole point: the guard being handed an already-spent context is what made
	// it go quiet.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}

	if d.listErr != nil {
		return nil, d.listErr
	}

	return d.listPods, nil
}

func (d *fakeDriver) ListRunContainers(ctx context.Context) ([]string, error) {
	d.listCtrCalls++
	d.listCtrCtxErrs = append(d.listCtrCtxErrs, ctx.Err())

	// Modelled on ListRunPods above, and for the same reason: a stage handed an
	// already-spent context must be visible as such.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	if d.listCtrErr != nil {
		return nil, d.listCtrErr
	}

	return d.listCtrs, nil
}

func (d *fakeDriver) CreateContainer(context.Context, *Pod, *ContainerSpec) (*Container, error) {
	return d.createdCtr, d.createCtrErr
}

func (d *fakeDriver) Cleanup(context.Context) error {
	d.cleanups++

	return nil
}

var _ Driver = &fakeDriver{}

// useDriver installs d as the package-level driver for the duration of the test.
func useDriver(t *testing.T, d Driver) {
	t.Helper()

	saved := driver
	t.Cleanup(func() { driver = saved })
	driver = d
}

// useExecutor installs e as the package-level executor for the duration of the
// test.
func useExecutor(t *testing.T, e Executor) {
	t.Helper()

	saved := executor
	t.Cleanup(func() { executor = saved })
	executor = e
}

// useInspector installs i as the package-level inspector for the duration of the
// test.
func useInspector(t *testing.T, i Inspector) {
	t.Helper()

	saved := inspector
	t.Cleanup(func() { inspector = saved })
	inspector = i
}

// useConfig installs cfg as TestConfig for the duration of the test.
//
// Every test that reaches cleanup must do this: TestConfig.Timeout is zero in a
// zero Config, and a zero timeout means every context cleanup builds is already
// expired before it is used.
func useConfig(t *testing.T, cfg *Config) {
	t.Helper()

	saved := TestConfig
	t.Cleanup(func() { TestConfig = saved })
	TestConfig = cfg
}

// newFakeSuite returns a Suite wired to ft with a live plugin context, as NewSuite
// would build it, but without needing a runtime.
func newFakeSuite(ft *fakeT, pods ...*Pod) *Suite {
	pluginCtx, pluginCancel := context.WithCancel(context.Background())

	return &Suite{
		t:            ft,
		caseName:     "fake",
		scope:        &Scope{},
		q:            &EventQ{},
		pods:         pods,
		removed:      map[string]bool{},
		pluginCtx:    pluginCtx,
		pluginCancel: pluginCancel,
	}
}

func TestSuiteCleanupGivesEveryStageItsOwnBudget(t *testing.T) {
	// The regression: cleanup used to share one TestConfig.Timeout context across
	// every StopPod, every RemovePod and the leak guard's ListRunPods. Pod removal
	// spent the budget, ListRunPods was called with an expired context, returned an
	// error, and the error was discarded -- so the one mechanism whose job is
	// noticing a missed cleanup went quiet exactly when cleanup was missed.
	//
	// Three pods at two 40ms calls each is 240ms of work under a 100ms timeout: far
	// more than one shared budget, comfortably within a per-pod one.
	const timeout = 100 * time.Millisecond

	d := &fakeDriver{
		opDelay:  40 * time.Millisecond,
		listPods: []string{"pod-0"},
	}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: timeout, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft,
		&Pod{ID: "pod-0"}, &Pod{ID: "pod-1"}, &Pod{ID: "pod-2"})

	s.cleanup()

	require.Equal(t, []string{"pod-0", "pod-1", "pod-2"}, d.stopped,
		"every pod must be stopped, each on its own budget")
	require.Equal(t, []string{"pod-0", "pod-1", "pod-2"}, d.removed,
		"every pod must be removed, each on its own budget")

	require.Equal(t, 1, d.listCalls, "the leak guard must run exactly once")
	require.Equal(t, []error{nil}, d.listCtxErrs,
		"the leak guard must be given a live context of its own")

	require.Equal(t, 1, d.listCtrCalls, "the container guard must run exactly once")
	require.Equal(t, []error{nil}, d.listCtrCtxErrs,
		"the container guard must be given a live context of its own, not the pod guard's")

	require.Contains(t, ft.allErrors(), "pod-0",
		"the leak guard must report the pod it found")
	require.NotContains(t, ft.allErrors(), "leak guard could not list",
		"the leak guard must not be reduced to reporting its own failure")
}

func TestSuiteCleanupReportsALeakGuardFailure(t *testing.T) {
	// The other half of the regression: an unusable leak guard must say so rather
	// than silently report nothing.
	d := &fakeDriver{listErr: errors.New("runtime is not answering")}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft, &Pod{ID: "pod-0"})

	s.cleanup()

	require.Contains(t, ft.allErrors(), "leak guard could not list this run's pods")
	require.Contains(t, ft.allErrors(), "runtime is not answering")
}

func TestSuiteCleanupReportsALeakedContainer(t *testing.T) {
	// A pod-level guard cannot express "this rejected creation leaked no
	// container": the container's pod is one the case created on purpose, so the
	// pod guard is silent either way. The container's own leak has to be reported
	// on its own, with the same this-case/pre-existing split the pod guard makes --
	// a container in the namespace carrying no run id counts as ours by design, so
	// without the split a stray makes every case accuse itself.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	for _, tc := range []struct {
		name        string
		ctrs        []*Container
		leaked      []string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "one it created",
			ctrs:        []*Container{{ID: "mine", PodID: "pod-0"}},
			leaked:      []string{"mine"},
			wantContain: []string{"leaked 1 container(s) it created", "mine"},
			wantAbsent:  []string{"were not created by this case"},
		},
		{
			name:        "one it did not create",
			ctrs:        []*Container{{ID: "mine", PodID: "pod-0"}},
			leaked:      []string{"stray"},
			wantContain: []string{"were not created by this case", "stray"},
			wantAbsent:  []string{"leaked 1 container(s) it created"},
		},
		{
			name:   "some of each",
			ctrs:   []*Container{{ID: "mine", PodID: "pod-0"}},
			leaked: []string{"mine", "stray"},
			wantContain: []string{
				"leaked 1 container(s) it created", "mine",
				"were not created by this case", "stray",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDriver{listCtrs: tc.leaked}
			useDriver(t, d)

			ft := &fakeT{}
			s := newFakeSuite(ft, &Pod{ID: "pod-0"})
			s.ctrs = tc.ctrs

			s.cleanup()

			require.Equal(t, 1, d.listCtrCalls, "the container guard must run exactly once")
			for _, want := range tc.wantContain {
				require.Contains(t, ft.allErrors(), want)
			}
			for _, absent := range tc.wantAbsent {
				require.NotContains(t, ft.allErrors(), absent)
			}
		})
	}
}

func TestSuiteCleanupReportsAContainerLeakGuardFailure(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	t.Run("on its own", func(t *testing.T) {
		// An unusable guard must say so rather than silently report nothing.
		d := &fakeDriver{listCtrErr: errors.New("runtime is not answering")}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "leak guard could not list this run's containers")
		require.Contains(t, ft.allErrors(), "runtime is not answering")
	})

	t.Run("alongside a pod guard failure", func(t *testing.T) {
		// Both stages report. Returning after the first failure would hide the
		// second, which is the swallowing shape this guard has already died of once.
		d := &fakeDriver{
			listErr:    errors.New("pods are not answering"),
			listCtrErr: errors.New("containers are not answering"),
		}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "pods are not answering")
		require.Contains(t, ft.allErrors(), "containers are not answering")
	})
}

func TestSuiteCleanupGivesTheContainerGuardABudgetOfItsOwn(t *testing.T) {
	// The pod listing here overruns the whole timeout, which is what the original
	// defect looked like from the container guard's seat: sharing that context would
	// hand the container guard an already-spent one, it would fail, and the run
	// would learn nothing about the containers it leaked. Its own budget means the
	// pod guard's bad day cannot silence it.
	const timeout = 100 * time.Millisecond

	d := &fakeDriver{
		listDelay: 2 * timeout,
		listCtrs:  []string{"mine"},
	}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: timeout, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft, &Pod{ID: "pod-0"})
	s.ctrs = []*Container{{ID: "mine", PodID: "pod-0"}}

	s.cleanup()

	require.Contains(t, ft.allErrors(), "leak guard could not list this run's pods",
		"the pod guard must report that it ran out of time")
	require.Equal(t, []error{nil}, d.listCtrCtxErrs,
		"the container guard must be given a live context of its own")
	require.Contains(t, ft.allErrors(), "leaked 1 container(s) it created",
		"the container guard must still report its leak")
}

func TestSuiteCreatePodRecordsTheContainersItCreated(t *testing.T) {
	// The containers created with a pod are on s.ctrs, so the leak guard names
	// them as this case's. That holds for a driver that can also create containers
	// individually: there is no second, better path to refuse them for.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	for _, caps := range []Capability{0, CapContainerLifecycle} {
		d := &fakeDriver{caps: caps, podCtrs: []*Container{{ID: "ctr-0"}}}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft)

		pod := s.CreatePod(s.PodSpec("pod", s.CtrSpec("ctr0")))

		require.Empty(t, ft.errors, "driver capabilities %v", caps)
		require.Len(t, d.createdPods, 1)
		require.Equal(t, pod.Containers, s.ctrs)
	}

	t.Run("records them when creating the pod failed part way", func(t *testing.T) {
		useReport(t)

		d := &fakeDriver{podCtrs: []*Container{{ID: "ctr-0"}}, createPodErr: errors.New("refused")}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft)

		require.Panics(t, func() { s.CreatePod(s.PodSpec("pod", s.CtrSpec("ctr0"))) })
		require.Len(t, s.ctrs, 1)
	})
}

func TestSuiteCreateContainerRecordsTheContainerBeforeAsserting(t *testing.T) {
	// Record before asserting, for the reason CreatePod documents: fatalf does not
	// return, so a container appended after it is a container the leak guard can
	// never attribute to this case -- and a creation that failed part way is
	// exactly the case phase two asserts leaks nothing.
	useReport(t)

	d := &fakeDriver{
		createdCtr:   &Container{ID: "ctr-0", PodID: "pod-0"},
		createCtrErr: errors.New("create refused"),
	}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft, &Pod{ID: "pod-0"})

	// fakeT.FailNow panics instead of unwinding a goroutine, which is what lets this
	// inspect the state fatalf left behind.
	require.Panics(t, func() { s.CreateContainer(&Pod{ID: "pod-0"}, s.CtrSpec("ctr0")) })

	require.Equal(t, []*Container{{ID: "ctr-0", PodID: "pod-0"}}, s.ctrs,
		"a container whose creation failed must still be tracked")
}

func TestSuiteCleanupReportsWhichLeakedPodsTheCaseCreated(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	for _, tc := range []struct {
		name        string
		pods        []*Pod
		leaked      []string
		wantContain []string
		wantAbsent  []string
	}{
		{
			// A pod in the namespace carrying no run id is treated as ours by
			// design (Scope.Matches), so a stray from a killed earlier run makes
			// every case's guard fire. Saying it pre-existed the case is what makes
			// that legible instead of a cascade of identical accusations.
			name:        "none of them",
			pods:        []*Pod{{ID: "mine"}},
			leaked:      []string{"stray"},
			wantContain: []string{"were not created by this case", "stray"},
			wantAbsent:  []string{"leaked 1 pod(s) it created"},
		},
		{
			name:        "all of them",
			pods:        []*Pod{{ID: "mine"}},
			leaked:      []string{"mine"},
			wantContain: []string{"leaked 1 pod(s) it created", "mine"},
			wantAbsent:  []string{"were not created by this case"},
		},
		{
			name:   "some of them",
			pods:   []*Pod{{ID: "mine"}},
			leaked: []string{"mine", "stray"},
			wantContain: []string{
				"leaked 1 pod(s) it created", "mine",
				"were not created by this case", "stray",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDriver{listPods: tc.leaked}
			useDriver(t, d)

			ft := &fakeT{}
			s := newFakeSuite(ft, tc.pods...)

			s.cleanup()

			for _, want := range tc.wantContain {
				require.Contains(t, ft.allErrors(), want)
			}
			for _, absent := range tc.wantAbsent {
				require.NotContains(t, ft.allErrors(), absent)
			}
		})
	}
}

func TestSuiteCleanupReportsRemovalFailures(t *testing.T) {
	// StopPod and RemovePod stay best effort -- the case may legitimately have
	// removed the pod itself -- but their errors are the direct cause of any leak,
	// so they must reach the operator instead of being dropped on the floor.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	t.Run("as a note when nothing leaked", func(t *testing.T) {
		d := &fakeDriver{removeErr: errors.New("sandbox not found")}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})

		s.cleanup()

		require.Contains(t, ft.allLogs(), "sandbox not found")
		require.Empty(t, ft.errors, "a removal failure alone must not fail the case")
	})

	t.Run("as a failure when the leak guard found something", func(t *testing.T) {
		d := &fakeDriver{
			removeErr: errors.New("sandbox not found"),
			listPods:  []string{"pod-0"},
		}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "sandbox not found",
			"the cause must be reported alongside the leak it explains")
		require.Contains(t, ft.allErrors(), "leaked 1 pod(s) it created")
	})

	// The escalation condition names four reasons, and the two container-shaped ones
	// were added by the container guard. Without these rows the condition reverts to
	// its pod-only form with everything still green, and the note above it would be a
	// comment claiming behaviour nothing defends.
	t.Run("as a failure when only the container guard found something", func(t *testing.T) {
		d := &fakeDriver{
			removeErr: errors.New("sandbox not found"),
			listCtrs:  []string{"ctr-0"},
		}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})
		s.ctrs = []*Container{{ID: "ctr-0", PodID: "pod-0"}}

		s.cleanup()

		require.Contains(t, ft.allErrors(), "sandbox not found",
			"a container leak is a leak: the removal failure is its diagnosis too")
		require.Contains(t, ft.allErrors(), "leaked 1 container(s) it created")
		require.NotContains(t, ft.allLogs(), "sandbox not found",
			"the note must have escalated rather than been logged as well")
	})

	t.Run("as a failure when only the container guard could not look", func(t *testing.T) {
		d := &fakeDriver{
			removeErr:  errors.New("sandbox not found"),
			listCtrErr: errors.New("runtime is not answering"),
		}
		useDriver(t, d)

		ft := &fakeT{}
		s := newFakeSuite(ft, &Pod{ID: "pod-0"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "sandbox not found",
			"a guard that could not look cannot clear the removal failure either")
		require.NotContains(t, ft.allLogs(), "sandbox not found",
			"the note must have escalated rather than been logged as well")
	})
}

func TestSuiteLeakMessagesStateOnlyWhatWasChecked(t *testing.T) {
	// Full messages, not substrings, for two reasons. The pod wording is shared with
	// the container guard through reportLeakOf, and "unchanged" is a property
	// something should notice breaking rather than one that holds by construction.
	// And the container message must name no namespace at all: a container has none
	// of its own, and the driver's second arm matches one on its run-id label in a
	// pod it knows nothing about, so printing the run's namespace would state
	// something nobody checked.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})
	useDriver(t, &fakeDriver{})

	for _, tc := range []struct {
		name   string
		report func(s *Suite, leaked []string)
		leaked []string
		want   string
	}{
		{
			name:   "a pod leak names the namespace that matched it",
			report: (*Suite).reportLeak,
			leaked: []string{"mine", "stray"},
			want: "case leaked 1 pod(s) it created: [mine]; " +
				"1 further pod(s) in namespace nri-test match this run but were not " +
				"created by this case: [stray]",
		},
		{
			name:   "a pre-existing pod names it too, and what to rule out",
			report: (*Suite).reportLeak,
			leaked: []string{"stray"},
			want: "1 pod(s) in namespace nri-test match this run but were not created " +
				"by this case: [stray]; they pre-existed it, so look for an earlier case " +
				"that leaked, a killed earlier run, or an unrelated pod in that namespace",
		},
		{
			name:   "a container leak claims no namespace",
			report: (*Suite).reportContainerLeak,
			leaked: []string{"mine", "stray"},
			want: "case leaked 1 container(s) it created: [mine]; " +
				"1 further container(s) match this run but were not created by this " +
				"case: [stray]",
		},
		{
			name:   "a pre-existing container claims none either",
			report: (*Suite).reportContainerLeak,
			leaked: []string{"stray"},
			want: "1 container(s) match this run but were not created by this case: " +
				"[stray]; they pre-existed it, so look for an earlier case that leaked, " +
				"a killed earlier run, or an unrelated container that matches this run",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeT{}
			s := newFakeSuite(ft, &Pod{ID: "mine"})
			s.ctrs = []*Container{{ID: "mine", PodID: "pod-0"}}

			tc.report(s, tc.leaked)

			require.Equal(t, []string{tc.want}, ft.errors)
		})
	}
}

func TestSuiteCleanupDoesNotRunARunWideCleanup(t *testing.T) {
	// A per-case driver.Cleanup removes every pod in the namespace that matches the
	// run, including ones this case never created. Teardown already runs one
	// run-wide cleanup, with better-scoped intent.
	d := &fakeDriver{listPods: []string{"pod-0", "stray"}}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft, &Pod{ID: "pod-0"})

	s.cleanup()

	require.Zero(t, d.cleanups,
		"cleanup must not remove pods it cannot attribute to this case")
}

func TestSuiteCleanupSkipsPodsTheCaseAlreadyRemoved(t *testing.T) {
	// Without this, every case that removes its own pod -- which is most of them --
	// provokes a not-found from cleanup and then reports it as a removal failure.
	d := &fakeDriver{}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ft := &fakeT{}
	pod := &Pod{ID: "pod-0"}
	s := newFakeSuite(ft, pod)

	s.RemovePod(pod)
	require.Equal(t, []string{"pod-0"}, d.removed)

	s.cleanup()

	require.Equal(t, []string{"pod-0"}, d.removed,
		"cleanup must not remove a pod the case already removed")
	require.Empty(t, ft.errors)
	require.Empty(t, ft.logs)

	require.Len(t, s.pods, 1,
		"a removed pod stays on the list, so the leak guard can still attribute it")
}

func TestSuiteCleanupFailsOnADroppedUpdate(t *testing.T) {
	// Recording a drop is only half of "loud". Nothing else in the suite reads
	// UpdateDropped -- the wholesale event scan skips non-hooks, correctly -- so
	// without this guard a case whose update request was emptied by the filter does
	// nothing and passes, which is the very failure the event exists to prevent.
	newSuite := func(ft *fakeT) *Suite {
		useDriver(t, &fakeDriver{})
		useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

		return newFakeSuite(ft)
	}

	t.Run("should fail the case and name the containers", func(t *testing.T) {
		ft := &fakeT{}
		s := newSuite(ft)

		s.Q().Add(&Event{Type: UpdateDropped, Plugin: "00-test", CtrID: "not-mine"})
		s.Q().Add(&Event{Type: UpdateDropped, Plugin: "00-test", CtrID: "also-not-mine"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "not-mine")
		require.Contains(t, ft.allErrors(), "also-not-mine")
		require.Contains(t, ft.allErrors(), "did not do what it asked for")
	})

	t.Run("should report each container once", func(t *testing.T) {
		ft := &fakeT{}
		s := newSuite(ft)

		s.Q().Add(&Event{Type: UpdateDropped, CtrID: "not-mine"})
		s.Q().Add(&Event{Type: UpdateDropped, CtrID: "not-mine"})

		s.cleanup()

		require.Equal(t, 1, strings.Count(ft.allErrors(), "not-mine"))
		require.Contains(t, ft.allErrors(), "returned 1 update(s)")
	})

	t.Run("should stay quiet when nothing was dropped", func(t *testing.T) {
		ft := &fakeT{}
		s := newSuite(ft)

		// A queue with ordinary events in it must not trip the guard.
		s.Q().Add(PodEvent("00-test", testPod("pod0"), RunPodSandbox))
		s.Q().Add(CtrEvent("00-test", testPod("pod0"), testCtr("pod0", "ctr0"), StartContainer))

		s.cleanup()

		require.Empty(t, ft.errors)
	})

	t.Run("should guard even with KeepArtifacts", func(t *testing.T) {
		// KeepArtifacts is about leftover workloads. Whether the case did what it
		// said is a different question, so the guard runs before that return.
		ft := &fakeT{}
		useDriver(t, &fakeDriver{})
		useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test", KeepArtifacts: true})

		s := newFakeSuite(ft)
		s.Q().Add(&Event{Type: UpdateDropped, CtrID: "not-mine"})

		s.cleanup()

		require.Contains(t, ft.allErrors(), "not-mine")
	})
}

func TestSuiteCleanupStopsPluginsBeforeCancellingTheirContext(t *testing.T) {
	// The regression commit 409e62c fixed. Stop calls close(doneC), which waits for
	// the plugin's server goroutine, so the context must still be live when Run's
	// select completes -- otherwise the stub is torn down by cancellation and Run
	// calls Stop a second time.
	//
	// The plugin list is a pluginStopper interface so that this fake can be put in
	// it and read the context at the exact moment Stop is called. Nothing else can
	// pin the ordering: with an empty plugin list, moving pluginCancel to the top of
	// cleanup passes.
	d := &fakeDriver{}
	useDriver(t, d)
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ft := &fakeT{}
	s := newFakeSuite(ft)

	p0 := &fakePlugin{observe: func() error { return s.pluginCtx.Err() }}
	p1 := &fakePlugin{observe: func() error { return s.pluginCtx.Err() }}
	s.plugins = []pluginStopper{p0, p1}

	require.NoError(t, s.pluginCtx.Err(), "the plugin context starts live")

	s.cleanup()

	for i, p := range []*fakePlugin{p0, p1} {
		require.Equal(t, 1, p.stops, "plugin %d must be stopped exactly once", i)
		require.Equal(t, []error{nil}, p.ctxErrs,
			"plugin %d must observe a live plugin context while it is stopping", i)
	}

	require.ErrorIs(t, s.pluginCtx.Err(), context.Canceled,
		"the plugin context must be cancelled once cleanup has run")
}

func TestSuiteCleanupCancelsThePluginContextEvenWithKeepArtifacts(t *testing.T) {
	// KeepArtifacts returns early, so the cancellation has to be on every path.
	d := &fakeDriver{}
	useDriver(t, d)
	useConfig(t, &Config{
		Timeout:       time.Minute,
		Namespace:     "nri-test",
		KeepArtifacts: true,
	})

	ft := &fakeT{}
	s := newFakeSuite(ft, &Pod{ID: "pod-0"})

	p := &fakePlugin{observe: func() error { return s.pluginCtx.Err() }}
	s.plugins = []pluginStopper{p}

	s.cleanup()

	require.ErrorIs(t, s.pluginCtx.Err(), context.Canceled,
		"the plugin context must be cancelled even when KeepArtifacts is set")
	require.Equal(t, []error{nil}, p.ctxErrs,
		"the plugin must still see a live context while it is stopping")

	require.Empty(t, d.removed, "KeepArtifacts must keep the pods")
	require.Zero(t, d.listCalls, "KeepArtifacts must not run the leak guard")
	require.Zero(t, d.listCtrCalls, "KeepArtifacts must not run the container guard")
	require.Empty(t, ft.errors)
}

func TestSuiteStartPluginsRecordsAPluginBeforeAssertingOnItsStart(t *testing.T) {
	// Record before asserting, for the reason CreatePod documents: fatalf does not
	// return, so a plugin appended after it is a plugin cleanup never gets to
	// stop.
	useReport(t)
	useDriver(t, &fakeDriver{})
	useConfig(t, &Config{
		Timeout:       100 * time.Millisecond,
		Namespace:     "nri-test",
		NRISocketPath: filepath.Join(t.TempDir(), "no-such.sock"),
	})

	ft := &fakeT{}
	s := newFakeSuite(ft)

	p := s.NewPlugin("cannot-connect", "00", 0)

	// fakeT.FailNow panics instead of unwinding a goroutine, which is what lets this
	// inspect the state fatalf left behind.
	require.Panics(t, func() { s.StartPlugins(p) })
	require.Contains(t, ft.allErrors(), "failed to start plugin 00-cannot-connect")

	require.Equal(t, []pluginStopper{p}, s.plugins,
		"a plugin whose Start failed must still be on the list cleanup stops")
}

func TestSuiteStartPluginExpectingErrorStillRecordsThePlugin(t *testing.T) {
	// A plugin whose Start failed part way may still hold a connection and still be
	// recording into this case's queue, so cleanup must get to stop it -- and
	// cleanup's dropped-update guard runs after those stops, on the promise that
	// nothing can still be appending to the queue. Recording only on success broke
	// that promise for exactly the plugins most likely to be in a half-state.
	useDriver(t, &fakeDriver{})
	useConfig(t, &Config{
		Timeout:       100 * time.Millisecond,
		Namespace:     "nri-test",
		NRISocketPath: filepath.Join(t.TempDir(), "no-such.sock"),
	})

	ft := &fakeT{}
	s := newFakeSuite(ft)

	p := s.NewPlugin("cannot-connect", "00", 0)

	require.Error(t, s.StartPluginExpectingError(p),
		"nothing is listening, so Start must fail")

	require.Equal(t, []pluginStopper{p}, s.plugins,
		"a plugin whose Start failed must still be on the list cleanup stops")

	require.Empty(t, ft.errors)
}

func TestSuitePodSpecNamesAndUIDsAreUniquePerProcess(t *testing.T) {
	// Every subtest builds its own Suite, so Suite.seq restarts at 0 and the only
	// thing left distinguishing two cases' pods is the caseName they were handed --
	// which is duplicated across rows in more than one table. containerd reserves a
	// sandbox name for a live (name, namespace, uid, attempt) tuple, so a duplicate
	// plus a removal that has not landed yet fails the *next* subtest with "sandbox
	// name is reserved".
	useConfig(t, &Config{
		Timeout:   time.Minute,
		Namespace: "nri-test",
		RunID:     "deadbeef",
		Image:     "test-image",
	})

	seen := map[string]bool{}

	for range 3 {
		// Same caseName, same pod name, same fresh Suite: exactly the collision the
		// tables produce.
		s := newFakeSuite(&fakeT{name: "TestX/row"})

		for range 2 {
			spec := s.PodSpec("pod")

			require.False(t, seen[spec.Name], "duplicate pod name %q", spec.Name)
			require.False(t, seen[spec.UID], "duplicate pod uid %q", spec.UID)

			seen[spec.Name] = true
			seen[spec.UID] = true
		}
	}

	require.Len(t, seen, 12)
}

func TestSuitePodSpecLabelsTheCaseByTestName(t *testing.T) {
	// The case label's whole purpose is telling a leaked pod's reader which case
	// created it, and t.Name() is unique by construction where caseName is not.
	useConfig(t, &Config{
		Timeout:   time.Minute,
		Namespace: "nri-test",
		RunID:     "deadbeef",
		Image:     "test-image",
	})

	s := newFakeSuite(&fakeT{name: "TestOrdering/index_order/for_CreateContainer"})

	spec := s.PodSpec("pod")
	require.Equal(t, "TestOrdering_index_order_for_CreateContainer",
		spec.Labels[CaseLabel])
	require.Equal(t, "deadbeef", spec.Labels[RunIDLabel])
	require.Equal(t, spec.Labels, spec.Annotations)

	ctr := s.CtrSpec("ctr0")
	require.Equal(t, spec.Labels[CaseLabel], ctr.Labels[CaseLabel])
	require.Equal(t, spec.Labels[CaseLabel], ctr.Annotations[CaseLabel])
}

func TestLabelValueProducesALegalKubernetesLabelValue(t *testing.T) {
	long := "TestEventSubscriptions/should_honor_a_plugin's_event_subscriptions/" +
		"with_all_pod_and_container_requests_and_events"

	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "already legal", in: "smoke-inspect", want: "smoke-inspect"},
		{
			name: "subtest separators and punctuation",
			in:   "TestX/a b/c'd",
			want: "TestX_a_b_c_d",
		},
		{
			// The tail is what identifies the row; the head is the table's shared
			// prefix.
			name: "too long keeps the tail",
			in:   long,
			want: "nt_subscriptions_with_all_pod_and_container_requests_and_events",
		},
		{name: "empty", in: "", want: "unnamed"},
		{name: "only separators", in: "///", want: "unnamed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := labelValue(tc.in)

			require.Equal(t, tc.want, got)
			require.LessOrEqual(t, len(got), 63)
			require.NotEmpty(t, got)
		})
	}
}

// pullDriver is a driver that records the images it was asked to pull.
type pullDriver struct {
	noOpDriver

	err    error
	pulled []string

	// delay is how long each pull takes, and budgets what each was given.
	delay   time.Duration
	budgets []time.Duration
}

func (d *pullDriver) PullImage(ctx context.Context, image string) error {
	d.pulled = append(d.pulled, image)

	// What budget this pull was handed, so a test can tell a per-image one
	// from a shared one that the previous image already spent.
	if deadline, ok := ctx.Deadline(); ok {
		d.budgets = append(d.budgets, time.Until(deadline))
	} else {
		d.budgets = append(d.budgets, 0)
	}

	time.Sleep(d.delay)

	return d.err
}

var _ imagePuller = &pullDriver{}

func TestPullImage(t *testing.T) {
	// Nothing else pulls the test image: CRI's CreateContainer does not, so without
	// this phase every container-creating test fails on a node where the image is
	// absent, with a message that names neither the image nor pulling.
	cfg := &Config{Image: "registry.k8s.io/pause:3.10"}

	t.Run("pulls the configured image", func(t *testing.T) {
		d := &pullDriver{}
		useDriver(t, d)

		require.NoError(t, pullImage(context.Background(), cfg))
		require.Equal(t, []string{cfg.Image}, d.pulled)
	})

	t.Run("reports a pull failure actionably", func(t *testing.T) {
		d := &pullDriver{err: errors.New("no such host")}
		useDriver(t, d)

		err := pullImage(context.Background(), cfg)
		require.Error(t, err)
		require.ErrorContains(t, err, cfg.Image, "the message must name the image")
		require.ErrorContains(t, err, "-nri-test.no-pull",
			"the message must name the flag that skips the pull")
		require.ErrorContains(t, err, "no such host")
	})

	t.Run("skips the pull when -nri-test.no-pull is set", func(t *testing.T) {
		d := &pullDriver{err: errors.New("no such host")}
		useDriver(t, d)

		require.NoError(t, pullImage(context.Background(),
			&Config{Image: cfg.Image, NoPull: true}))
		require.Empty(t, d.pulled)
	})

	t.Run("gives each image a budget of its own", func(t *testing.T) {
		// One shared budget meant the first image's pull spent the second's:
		// on a node where the first is slow or absent, the later images fail
		// on a deadline the pull before them used up, and the message blames
		// the wrong image. Each image gets its own, as PullImages already did.
		useExtraImages(t, "second/img:1")

		d := &pullDriver{delay: 60 * time.Millisecond}
		useDriver(t, d)

		budget := &Config{Image: cfg.Image, Timeout: 500 * time.Millisecond}

		require.NoError(t, pullImage(context.Background(), budget))
		require.Len(t, d.pulled, 2, "both images must be pulled")
		require.Len(t, d.budgets, 2)

		for i, got := range d.budgets {
			require.Greater(t, got, 400*time.Millisecond,
				"image %d (%s) was handed %v of a %v budget, so the pulls are "+
					"sharing one deadline rather than each having its own",
				i, d.pulled[i], got, budget.Timeout)
		}
	})

	t.Run("is optional for a driver that cannot pull", func(t *testing.T) {
		// Discovered by interface, exactly as preflight discovers versioner: a
		// future driver with no image service must not have to implement it.
		useDriver(t, &noOpDriver{})

		require.NoError(t, pullImage(context.Background(), cfg))
	})
}

// fakeExecutor records what it was asked to run and what context it was handed,
// and returns programmed output.
type fakeExecutor struct {
	stdout string
	stderr string
	err    error

	cmds        [][]string
	ids         []string
	hadDeadline []bool
}

func (e *fakeExecutor) Exec(ctx context.Context, containerID string, cmd []string) (string, string, error) {
	_, ok := ctx.Deadline()

	e.ids = append(e.ids, containerID)
	e.cmds = append(e.cmds, cmd)
	e.hadDeadline = append(e.hadDeadline, ok)

	return e.stdout, e.stderr, e.err
}

var _ Executor = &fakeExecutor{}

// fakeInspector is an Inspector that is not also an Executor, standing in for a
// driver that cannot exec.
type fakeInspector struct{}

func (*fakeInspector) Inspect(context.Context, string) (*Observed, error) { return nil, nil }

func (*fakeInspector) InspectPod(context.Context, string) (*ObservedPod, error) {
	return nil, nil
}

var _ Inspector = &fakeInspector{}

// execInspector is an Inspector that is also an Executor, as the CRI one is.
type execInspector struct {
	fakeInspector
	fakeExecutor
}

func TestSuiteExec(t *testing.T) {
	useDriver(t, &fakeDriver{})
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	ctr := &Container{ID: "ctr0"}

	t.Run("trims the command's standard output", func(t *testing.T) {
		// Every caller reads a line out of /proc or /sys, where the trailing newline
		// is not part of the answer.
		e := &fakeExecutor{stdout: "0-1\n"}
		useExecutor(t, e)

		s := newFakeSuite(&fakeT{})

		require.Equal(t, "0-1", s.Exec(ctr, "cat", "/proc/self/status"))
		require.Equal(t, [][]string{{"cat", "/proc/self/status"}}, e.cmds)
		require.Equal(t, []string{"ctr0"}, e.ids)
	})

	t.Run("bounds the call by the configured timeout", func(t *testing.T) {
		// Through Suite.do like every other driver call, for the reason do documents.
		// The CRI executor also derives the command's own timeout from this deadline,
		// so an exec without one asks the runtime to wait forever.
		e := &fakeExecutor{}
		useExecutor(t, e)

		newFakeSuite(&fakeT{}).Exec(ctr, "true")

		require.Equal(t, []bool{true}, e.hadDeadline,
			"an exec must be handed a context with a deadline")
	})

	t.Run("fails the case when the command fails", func(t *testing.T) {
		e := &fakeExecutor{stdout: "partial", stderr: "cat: no such file", err: errors.New("exit 1")}
		useExecutor(t, e)
		// A failed Exec is a recorded operation failure now, so it must record into
		// a report of this test's own rather than into the run's.
		useReport(t)

		ft := &fakeT{}
		s := newFakeSuite(ft)

		// fakeT.FailNow panics rather than unwinding, which is how the reported
		// message can be inspected at all.
		require.Panics(t, func() { s.Exec(ctr, "cat", "/nope") })

		// Both streams belong in the message: a shell says why it failed on stderr.
		require.Contains(t, ft.allErrors(), "exit 1")
		require.Contains(t, ft.allErrors(), "cat: no such file")
		require.Contains(t, ft.allErrors(), "partial")
		require.Contains(t, ft.allErrors(), "ctr0")
	})

	t.Run("returns the output and error from TryExec instead of failing", func(t *testing.T) {
		e := &fakeExecutor{stdout: "out", stderr: "err", err: errors.New("exit 2")}
		useExecutor(t, e)

		ft := &fakeT{}

		stdout, stderr, err := newFakeSuite(ft).TryExec(ctr, "false")
		require.ErrorContains(t, err, "exit 2")
		require.Equal(t, "out", stdout, "TryExec must not trim, so a test can assert on the bytes")
		require.Equal(t, "err", stderr)
		require.Empty(t, ft.errors, "TryExec must leave the verdict to its caller")
	})

	t.Run("reports a driver with no executor actionably", func(t *testing.T) {
		useExecutor(t, nil)

		_, _, err := newFakeSuite(&fakeT{}).TryExec(ctr, "true")
		require.ErrorContains(t, err, "no Executor")
		require.ErrorContains(t, err, "CapExec",
			"the message must name the gate the test should have used")
	})
}

func TestSuiteProcSelfStatus(t *testing.T) {
	// /proc/self, never /proc/1: CRI's default NamespaceOption.Pid is POD, so PID 1
	// is the sandbox's pause process and its affinity is the sandbox's.
	useDriver(t, &fakeDriver{})
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	e := &fakeExecutor{stdout: "Name:\tsleep\nCpus_allowed_list:\t0\n"}
	useExecutor(t, e)

	status := newFakeSuite(&fakeT{}).ProcSelfStatus(&Container{ID: "ctr0"})

	require.Equal(t, [][]string{{"cat", "/proc/self/status"}}, e.cmds,
		"the status must be read from /proc/self, not from /proc/1")
	require.Equal(t, "0", status["Cpus_allowed_list"])
	require.Equal(t, "sleep", status["Name"])
}

// useReport installs a fresh Report as TestReport for the duration of the test
// and returns it, so a test can assert on what an expectation recorded without
// the run's own report carrying it afterwards.
//
// A helper rather than a Reset method on Report: a reset has no caller outside
// a test, and a production Report is written once and printed once.
func useReport(t *testing.T) *Report {
	t.Helper()

	saved := TestReport
	t.Cleanup(func() { TestReport = saved })
	TestReport = &Report{}

	return TestReport
}

func TestExpectCollectsInCollectingMode(t *testing.T) {
	// Collecting mode reports the failure through Errorf and returns, so the rest
	// of the test body runs and its later expectations are evaluated too. That is
	// the whole point: one run enumerates every difference a runtime has, instead
	// of one run per difference.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test", CollectFailures: true})

	r := useReport(t)
	ft := &fakeT{name: "TestCollectingCase"}

	// No require.Panics around this call: fakeT.FailNow panics, so reaching the
	// end of expect without a panic is itself the assertion that the test was not
	// aborted.
	newFakeSuite(ft).expect("annotation", "OCI spec", errors.New("annotation was not applied"))

	require.Contains(t, ft.allErrors(), "annotation was not applied",
		"a collected failure must still be reported against the test")

	findings := r.Findings()
	require.Len(t, findings, 1)
	require.Equal(t, "TestCollectingCase", findings[0].Test)
	require.Equal(t, "annotation", findings[0].Feature)
	require.Equal(t, "OCI spec", findings[0].Surface)
	require.EqualError(t, findings[0].Err, "annotation was not applied")
}

func TestExpectAbortsInDefaultMode(t *testing.T) {
	// The mode decides whether the test aborts, not whether the failure is
	// reported: the finding is recorded first, so the summary and the exit code
	// are the same in both modes.
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	r := useReport(t)
	ft := &fakeT{name: "TestDefaultCase"}

	// fakeT.FailNow panics instead of unwinding a goroutine, which is what lets
	// this test observe that require aborted rather than returned.
	require.Panics(t, func() {
		newFakeSuite(ft).expect("cpuset", "/proc/self/status", errors.New("cpuset was not applied"))
	}, "the default mode must end the test at its first expectation failure")

	require.Len(t, r.Findings(), 1,
		"the finding must be recorded before the abort, or the abort loses it")
	require.Contains(t, ft.allErrors(), "cpuset was not applied")
}

func TestDriverCallFailureIsRecordedAndFatal(t *testing.T) {
	// Both halves, in one test, because either on its own is a defect. Fatal is
	// the design decision Task 3 made and it still holds: a collected
	// CreateContainer failure would leave the test body running with a nil
	// *Container, and the derived panics would bury the one real failure.
	// Recorded is what was missing, and what let a 34-failure sweep print "No
	// expectation failures were collected".
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	d := &fakeDriver{createPodErr: errors.New("sandbox name is reserved")}
	useDriver(t, d)

	r := useReport(t)
	ft := &fakeT{name: "TestCreatePodCase"}
	s := newFakeSuite(ft)

	// fakeT.FailNow panics instead of unwinding a goroutine, which is what lets
	// this test observe that the call aborted rather than returned.
	require.Panics(t, func() {
		s.CreatePod(s.PodSpec("pod0"))
	}, "a failed driver call must still end its test")

	findings := r.Findings()
	require.Len(t, findings, 1, "a failed driver call is recorded exactly once")
	require.Equal(t, KindOperation, findings[0].Kind,
		"a failed driver call is an operation failure, not an unmet expectation")
	require.Equal(t, "TestCreatePodCase", findings[0].Test)
	require.Equal(t, "CreatePod", findings[0].Feature,
		"the finding must name the operation that failed")
	require.ErrorIs(t, findings[0].Err, d.createPodErr,
		"the finding must carry the driver's own error")

	// The wrapper's message names the workload, and a finding is read in a
	// summary far from the test output the message would otherwise reach -- so it
	// has to be in the recorded error, as ctrError documents for expectations.
	require.Contains(t, findings[0].Err.Error(), "failed to create pod",
		"the wrapper's own message must reach the summary")

	require.Contains(t, ft.allErrors(), "sandbox name is reserved",
		"the failure must still be reported against the test")
}

func TestExpectRecordsNothingForAMetExpectation(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test", CollectFailures: true})

	r := useReport(t)
	ft := &fakeT{}

	newFakeSuite(ft).expect("mount", "OCI spec + /proc/self/mounts", nil)

	require.False(t, r.HasFindings(), "a met expectation records nothing")
	require.Empty(t, ft.allErrors(), "a met expectation reports nothing")
}

func TestSummaryHeaderDescribesTheRun(t *testing.T) {
	// The header is the only thing that makes a summary quotable, and it is built
	// from package vars the summary itself cannot see. Pinned here because a
	// field left unwired prints as a plausible-looking blank rather than as a
	// failure.
	savedName, savedVersion, savedNRI := runtimeName, runtimeVersion, runtimeNRIVersion
	t.Cleanup(func() {
		runtimeName, runtimeVersion, runtimeNRIVersion = savedName, savedVersion, savedNRI
	})

	runtimeName, runtimeVersion, runtimeNRIVersion = "containerd", "2.3.3", "0.9.0"
	useConfig(t, &Config{RunID: "run0", CollectFailures: true, RuntimeHandler: "crun"})

	require.Equal(t,
		Header{
			Runtime:        "containerd",
			RuntimeVersion: "2.3.3",
			NRIVersion:     "0.9.0",
			RuntimeHandler: "crun",
			RunID:          "run0",
			Collecting:     true,
			Failed:         true,
		},
		summaryHeader(true))

	TestConfig.CollectFailures = false
	require.False(t, summaryHeader(true).Collecting,
		"the header must report the mode the run actually used")

	// Both directions, because this is the field that decides which of the two
	// nothing-collected sentences the summary prints, and a header that always said
	// one of them would make the other dead code.
	require.False(t, summaryHeader(false).Failed,
		"a run go test passed must not be reported as failed")
	require.True(t, summaryHeader(true).Failed,
		"a run go test failed must be reported as failed, whatever the report holds")

	// The default is reported as the absence of a selection, which is what the
	// summary prints as "<runtime default>". A header that named some handler for a
	// run that selected none would be the one failure the field exists to prevent.
	TestConfig.RuntimeHandler = ""
	require.Empty(t, summaryHeader(true).RuntimeHandler,
		"a run that selected no handler must not claim one")
}

func TestExecutorOf(t *testing.T) {
	// The exec surface reaches the suite by type assertion on the inspector rather
	// than by widening DriverFactory; see executorOf.
	t.Run("finds an executor on an inspector that is one", func(t *testing.T) {
		i := &execInspector{}
		i.stdout = "hello"

		e := executorOf(i)
		require.NotNil(t, e)

		// Calling it, rather than comparing identities: what matters is that the
		// suite reaches the inspector's own exec surface, not a copy of it.
		stdout, _, err := e.Exec(context.Background(), "ctr0", []string{"echo", "hello"})
		require.NoError(t, err)
		require.Equal(t, "hello", stdout)
		require.Equal(t, []string{"ctr0"}, i.ids)
	})

	t.Run("returns nil for an inspector that is not", func(t *testing.T) {
		require.Nil(t, executorOf(&fakeInspector{}))
	})

	t.Run("returns nil for no inspector at all", func(t *testing.T) {
		// A driver may legitimately return a nil inspector; the assertion must not
		// hand back a non-nil Executor wrapping it.
		require.Nil(t, executorOf(nil))
	})
}

// capDriver is a driver advertising a fixed capability set.
type capDriver struct {
	noOpDriver

	caps Capability
}

func (d *capDriver) Capabilities() Capability { return d.caps }
func (d *capDriver) Name() string             { return "capdriver" }

func TestCheckExecCapability(t *testing.T) {
	// CapExec is what tests gate on, so an advertised capability with no Executor
	// behind it makes the gate lie. Caught in Setup it is one message naming the
	// driver; caught later it is a failure in every exec-verified test.
	t.Run("rejects a driver that advertises CapExec without one", func(t *testing.T) {
		useDriver(t, &capDriver{caps: CapExec})
		useExecutor(t, nil)

		err := checkExecCapability()
		require.ErrorContains(t, err, "capdriver")
		require.ErrorContains(t, err, "CapExec")
	})

	t.Run("accepts a driver that advertises CapExec and has one", func(t *testing.T) {
		useDriver(t, &capDriver{caps: CapExec})
		useExecutor(t, &fakeExecutor{})

		require.NoError(t, checkExecCapability())
	})

	t.Run("accepts a driver that cannot exec and does not claim to", func(t *testing.T) {
		useDriver(t, &capDriver{caps: CapContainerLifecycle})
		useExecutor(t, nil)

		require.NoError(t, checkExecCapability())
	})
}

func TestSuiteCtrSpecCarriesAKeepAliveCommand(t *testing.T) {
	// Without it the default busybox image's own command exits immediately with no
	// TTY and no stdin, so a started container is gone before anything can look
	// inside it. Not `sleep infinity`, which is a GNU extension busybox's sleep
	// does not parse.
	useConfig(t, &Config{
		Timeout:   time.Minute,
		Namespace: "nri-test",
		RunID:     "deadbeef",
		Image:     "test-image",
	})

	spec := newFakeSuite(&fakeT{}).CtrSpec("ctr0")

	require.Equal(t, KeepAliveCommand(), spec.Command,
		"a test container must be given a command that keeps it running")
}

// kubeletSetupConfig returns a Config for exercising Setup's kubelet check. Its
// NRI socket does not exist, so once the check has passed, preflight fails at
// connect without touching any socket on the host.
func kubeletSetupConfig(procDir string) *Config {
	return &Config{
		NRISocketPath: "/nonexistent/nri-test/nri.sock",
		Timeout:       2 * time.Second,
		procDir:       procDir,
	}
}

// setupWith runs Setup with d as the driver. Setup overwrites the package
// globals, so they are saved and restored here.
func setupWith(t *testing.T, cfg *Config, d Driver) error {
	t.Helper()

	useDriver(t, &noOpDriver{})
	useInspector(t, nil)
	useExecutor(t, nil)

	return Setup(cfg, func(context.Context, *Config) (Driver, Inspector, error) {
		return d, nil, nil
	})
}

// requireFellThroughKubeletCheck asserts that Setup got past the kubelet check
// and failed in preflight, for the unrelated reason the socket guarantees. It
// must not merely be an error: the kubelet refusal's own text is the only
// thing that would make it a refusal.
func requireFellThroughKubeletCheck(t *testing.T, err error) {
	t.Helper()

	require.ErrorContains(t, err, "no NRI plugin could connect")
	require.NotContains(t, err.Error(), "-nri-test.allow-kubelet")
}

func TestSetupRefusesWithKubeletRunning(t *testing.T) {
	procDir := t.TempDir()
	writeComm(t, procDir, "123", "kubelet\n")

	t.Run("RefusesWithKubeletRunning", func(t *testing.T) {
		err := setupWith(t, kubeletSetupConfig(procDir), &fakeDriver{})

		require.Error(t, err)
		require.ErrorContains(t, err, "kubelet")
		require.ErrorContains(t, err, "pid 123")
		require.ErrorContains(t, err, "-nri-test.allow-kubelet")
		// Refused before preflight: no NRI connect was attempted.
		require.NotContains(t, err.Error(), "no NRI plugin could connect")
	})

	t.Run("AllowKubeletProceedsPastTheCheck", func(t *testing.T) {
		cfg := kubeletSetupConfig(procDir)
		cfg.AllowKubelet = true

		requireFellThroughKubeletCheck(t, setupWith(t, cfg, &fakeDriver{}))
	})

	t.Run("NonConflictingDriverProceedsPastTheCheck", func(t *testing.T) {
		requireFellThroughKubeletCheck(t,
			setupWith(t, kubeletSetupConfig(procDir), &noOpDriver{}))
	})

	t.Run("NoKubeletRunningProceedsPastTheCheck", func(t *testing.T) {
		requireFellThroughKubeletCheck(t,
			setupWith(t, kubeletSetupConfig(t.TempDir()), &fakeDriver{}))
	})
}

// handlerDriver is a driver that lists runtime handlers, as the CRI driver does
// through the unexported runtimeHandlerLister interface. It embeds noOpDriver
// rather than fakeDriver so that it is not kubelet-sensitive: the handler phase
// is what these tests are about, and a kubelet refusal would pre-empt it.
type handlerDriver struct {
	noOpDriver

	handlers []string
	err      error

	calls int
	// ctxErrs is the state of the context each call was handed, so a test can pin
	// that discovery got a live budget of its own rather than a spent one.
	ctxErrs []error
}

func (d *handlerDriver) RuntimeHandlers(ctx context.Context) ([]string, error) {
	d.calls++
	d.ctxErrs = append(d.ctxErrs, ctx.Err())

	if d.err != nil {
		return nil, d.err
	}

	return d.handlers, nil
}

var _ runtimeHandlerLister = &handlerDriver{}

// useRuntimeHandlers installs handlers as the discovered set for the duration of
// the test. Every test that runs Setup or a handler gate must do this: the set is
// a package var, and a test that left its own behind would decide another's gate.
func useRuntimeHandlers(t *testing.T, handlers []string) {
	t.Helper()

	saved := runtimeHandlers
	t.Cleanup(func() { runtimeHandlers = saved })
	runtimeHandlers = handlers
}

// handlerSetupConfig returns a Config for exercising Setup's runtime-handler
// phase, requesting handler. Its NRI socket does not exist, for the reason
// kubeletSetupConfig's does not: once the phase has passed, preflight fails at
// connect without touching any socket on the host.
//
// It is installed as TestConfig as well as returned, because that is how a real
// run is wired -- TestMain passes TestConfig to Setup -- and because
// RuntimeHandler() answers from TestConfig.
func handlerSetupConfig(t *testing.T, handler string) *Config {
	t.Helper()

	cfg := kubeletSetupConfig(t.TempDir())
	cfg.RuntimeHandler = handler

	useConfig(t, cfg)
	useRuntimeHandlers(t, nil)

	return cfg
}

// requireFellThroughRuntimeHandlerCheck asserts that Setup got past the
// runtime-handler phase and failed in preflight, for the unrelated reason the
// nonexistent socket guarantees.
//
// Two-sided on purpose, exactly as requireFellThroughKubeletCheck is. A
// pass-expecting subtest cannot assert NoError -- preflight always fails here,
// since it connects a real plugin to a socket this host does not have -- so
// "validation passed" has to be pinned as "the error is preflight's AND is not
// the handler phase's". Asserting merely that some error came back would pass
// just as well if the handler phase had refused the run, which is the one thing
// these subtests exist to rule out.
func requireFellThroughRuntimeHandlerCheck(t *testing.T, err error) {
	t.Helper()

	require.ErrorContains(t, err, "no NRI plugin could connect")
	require.NotContains(t, err.Error(), "-nri-test.runtime-handler")
}

func TestSetupRejectsAnUninstalledRuntimeHandler(t *testing.T) {
	// One message naming both halves of the mismatch. A run asking for a handler
	// the node does not have would otherwise fail at the first CreatePod, where CRI
	// reports a handler error per test and nothing says which handlers exist.
	cfg := handlerSetupConfig(t, "gvisor")

	err := setupWith(t, cfg, &handlerDriver{handlers: []string{"runc"}})

	require.Error(t, err)
	require.ErrorContains(t, err, "gvisor", "the refusal must name what was asked for")
	require.ErrorContains(t, err, "runc", "the refusal must name what is installed")
	require.ErrorContains(t, err, "-nri-test.runtime-handler",
		"the refusal must name the flag that caused it")
	require.ErrorContains(t, err, "Select one of those handlers",
		"a node that has handlers must be told to pick one of them")
	// Refused before preflight, which is the whole reason the phase sits where it
	// does: a validation phase after preflight would never be reached on a host
	// with no runtime, so this refusal would be unobservable.
	require.NotContains(t, err.Error(), "no NRI plugin could connect")
}

func TestSetupRefusalAdviceFitsANodeWithNoHandlers(t *testing.T) {
	// The node this refusal is most likely to be read on: a runtime that advertises
	// no handlers at all refuses any requested one, and the advice must not tell its
	// operator to select one of an empty list, which reads as a bug in the suite
	// rather than as a fact about the node.
	cfg := handlerSetupConfig(t, "crun")

	err := setupWith(t, cfg, &handlerDriver{handlers: nil})

	require.ErrorContains(t, err, "crun")
	require.ErrorContains(t, err, "names no handlers at all",
		"the advice must say the node offers nothing to select")
	require.NotContains(t, err.Error(), "Select one of those handlers",
		"there is no 'one of those' on a node that reports an empty list")
	require.NotContains(t, err.Error(), "no NRI plugin could connect")
}

func TestSetupAcceptsAnInstalledRuntimeHandler(t *testing.T) {
	cfg := handlerSetupConfig(t, "runc")
	d := &handlerDriver{handlers: []string{"crun", "runc"}}

	// Fell through to preflight's failure, not the handler phase's: see
	// requireFellThroughRuntimeHandlerCheck for why this cannot be a NoError.
	requireFellThroughRuntimeHandlerCheck(t, setupWith(t, cfg, d))

	require.Equal(t, 1, d.calls, "discovery must ask the driver exactly once")
	require.Equal(t, []error{nil}, d.ctxErrs,
		"discovery must be given a live budget of its own")

	require.Equal(t, []string{"crun", "runc"}, RuntimeHandlers(),
		"the discovered handlers must reach the accessor the gates read")
	require.Equal(t, "runc", RuntimeHandler(),
		"the selected handler must reach the accessor the summary reads")
}

func TestSetupToleratesHandlerDiscoveryFailure(t *testing.T) {
	// A run that asked for nothing gets the runtime's default handler and does not
	// care whether Status answered; a run that asked for one cannot be allowed to
	// proceed as if it had got it.
	t.Run("NoHandlerRequested", func(t *testing.T) {
		cfg := handlerSetupConfig(t, "")

		// Seeded non-empty, so the assertion below can fail. With the list left nil
		// by handlerSetupConfig, "a failed discovery leaves no handlers behind" is
		// true before Setup is even called, and the assertion would be a guard that
		// reads as one without being one. Seeded, it pins the reset at the top of
		// selectRuntimeHandler: a run whose discovery failed must not inherit the
		// previous run's node.
		useRuntimeHandlers(t, []string{"stale"})

		// Again preflight's failure, for an unrelated reason, and specifically not
		// the handler phase's: a bare require.Error here would be satisfied by the
		// discovery failure this subtest exists to prove is tolerated.
		requireFellThroughRuntimeHandlerCheck(t,
			setupWith(t, cfg, &handlerDriver{err: errors.New("status is not answering")}))

		require.Empty(t, RuntimeHandlers(),
			"a failed discovery must leave no handlers behind for a gate to read")
	})

	t.Run("NoHandlerRequestedStillDiscovers", func(t *testing.T) {
		// The phase is two jobs, and only one of them is about the flag. Discovery
		// has to run on an ordinary default run as well, because the list it fills in
		// is the only thing SkipUnlessRuntimeHandler reads: short-circuit the phase on
		// an empty Config.RuntimeHandler and every handler gate silently skips on
		// every ordinary run, with no test saying so.
		cfg := handlerSetupConfig(t, "")
		d := &handlerDriver{handlers: []string{"crun", "runc"}}

		requireFellThroughRuntimeHandlerCheck(t, setupWith(t, cfg, d))

		require.Equal(t, 1, d.calls,
			"discovery must run even when no handler was requested")
		require.Equal(t, []string{"crun", "runc"}, RuntimeHandlers(),
			"the gates' list must be filled in on an ordinary default run too")
	})

	t.Run("HandlerRequested", func(t *testing.T) {
		cfg := handlerSetupConfig(t, "crun")

		err := setupWith(t, cfg,
			&handlerDriver{err: errors.New("status is not answering")})

		require.Error(t, err)
		require.ErrorContains(t, err, "crun", "the failure must name what was asked for")
		require.ErrorContains(t, err, "status is not answering",
			"the failure must name the discovery failure underneath it")
		require.ErrorContains(t, err, "-nri-test.runtime-handler")
		require.NotContains(t, err.Error(), "no NRI plugin could connect",
			"the run must be refused before preflight")
	})

	t.Run("DriverCannotList", func(t *testing.T) {
		// A driver that does not implement the optional interface at all. Asking for
		// a handler it cannot check is the same situation as a discovery that failed,
		// and must not be silently downgraded to the runtime's default.
		cfg := handlerSetupConfig(t, "crun")

		err := setupWith(t, cfg, &noOpDriver{})

		require.ErrorContains(t, err, "crun")
		require.ErrorContains(t, err, "-nri-test.runtime-handler")
		require.NotContains(t, err.Error(), "no NRI plugin could connect")
		// The refusal must be this path's own. Every assertion above is satisfied by
		// the not-installed refusal as well, so without this one the cannot-list path
		// could be collapsed into the empty-list path unnoticed -- and that path says
		// the node "names no handlers at all", which is the wrong statement about a
		// driver that was never able to be asked.
		require.ErrorContains(t, err, "cannot list",
			"a driver that cannot be asked must not be reported as a node with no handlers")
		require.ErrorContains(t, err, "noop", "the refusal must name the driver")
		require.NotContains(t, err.Error(), "names no handlers at all")
	})

	t.Run("DriverCannotListAndNoneRequested", func(t *testing.T) {
		requireFellThroughRuntimeHandlerCheck(t,
			setupWith(t, handlerSetupConfig(t, ""), &noOpDriver{}))
	})
}

func TestPodSpecCarriesTheConfiguredHandler(t *testing.T) {
	// Stamped by PodSpec rather than by each test: a test that forgot would create
	// its pods with the runtime's default handler while the run's summary claimed
	// the selected one, which is a false result rather than a missing feature.
	t.Run("stamps the selected handler", func(t *testing.T) {
		useConfig(t, &Config{
			Timeout:        time.Minute,
			Namespace:      "nri-test",
			RunID:          "deadbeef",
			RuntimeHandler: "crun",
		})

		require.Equal(t, "crun", newFakeSuite(&fakeT{}).PodSpec("pod0").RuntimeHandler)
	})

	t.Run("leaves it empty when none was selected", func(t *testing.T) {
		// Empty must stay empty rather than become a name: it is what asks the
		// runtime for its default handler.
		useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test", RunID: "deadbeef"})

		require.Empty(t, newFakeSuite(&fakeT{}).PodSpec("pod0").RuntimeHandler)
	})
}

// noOpDriver is a minimal driver implementation for testing.
type noOpDriver struct{}

func (*noOpDriver) Name() string                                               { return "noop" }
func (*noOpDriver) Capabilities() Capability                                   { return 0 }
func (*noOpDriver) CreatePod(context.Context, *PodSpec) (*Pod, error)          { return nil, nil }
func (*noOpDriver) UpdatePodResources(context.Context, *Pod, *Resources) error { return nil }
func (*noOpDriver) StopPod(context.Context, *Pod) error                        { return nil }
func (*noOpDriver) RemovePod(context.Context, *Pod) error                      { return nil }
func (*noOpDriver) CreateContainer(context.Context, *Pod, *ContainerSpec) (*Container, error) {
	return nil, nil
}
func (*noOpDriver) StartContainer(context.Context, *Container) error { return nil }
func (*noOpDriver) UpdateContainerResources(context.Context, *Container, *Resources) error {
	return nil
}
func (*noOpDriver) StopContainer(context.Context, *Container) error     { return nil }
func (*noOpDriver) RemoveContainer(context.Context, *Container) error   { return nil }
func (*noOpDriver) ListRunPods(context.Context) ([]string, error)       { return nil, nil }
func (*noOpDriver) ListRunContainers(context.Context) ([]string, error) { return nil, nil }
func (*noOpDriver) Cleanup(context.Context) error                       { return nil }
func (*noOpDriver) Close(context.Context) error                         { return nil }

func TestCheckCgroupV2(t *testing.T) {
	saved := cgroupRoot
	t.Cleanup(func() { cgroupRoot = saved })

	cgroupRoot = t.TempDir()
	require.ErrorContains(t, checkCgroupV2(), "cgroup v2",
		"a root with no cgroup.controllers is a v1 or hybrid hierarchy")

	require.NoError(t, os.WriteFile(filepath.Join(cgroupRoot, "cgroup.controllers"), nil, 0o600))
	require.NoError(t, checkCgroupV2())
}

func TestSuiteProcStatusInitView(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test", ProcView: ProcViewInit})

	e := &fakeExecutor{stdout: "Name:\tsleep\n"}
	useExecutor(t, e)

	newFakeSuite(&fakeT{}).ProcSelfStatus(&Container{ID: "ctr0"})

	require.Equal(t, [][]string{{"cat", "/proc/1/status"}}, e.cmds)
}

func TestSuiteCapturedAt(t *testing.T) {
	useConfig(t, &Config{Timeout: 200 * time.Millisecond, Namespace: "nri-test",
		CollectFailures: true})

	newSuite := func() (*Suite, *fakeT, *TestPlugin) {
		ft := &fakeT{}
		s := newFakeSuite(ft)
		// A real scope: an empty one deliberately matches nothing, so the hooks
		// below would be filtered out and the helper would be tested against a
		// plugin that was never invoked.
		s.scope = &Scope{Namespaces: []string{"nri-test"}}

		p := NewTestPlugin("input", "00", 0, s.q, s.scope)
		p.Capture(true)

		return s, ft, p
	}

	ctr := &Container{ID: "ctr0", PodID: "pod0"}

	t.Run("should return the payload once the hook has arrived", func(t *testing.T) {
		s, ft, p := newSuite()

		_, _, err := p.CreateContainer(context.Background(),
			&api.PodSandbox{Id: "pod0", Name: "pod0", Namespace: "nri-test"},
			&api.Container{Id: "ctr0", PodSandboxId: "pod0", Name: "ctr0"})
		require.NoError(t, err)

		got := s.CapturedAt(p, CreateContainer, ctr)
		require.NotNil(t, got)
		require.Equal(t, "ctr0", got.Ctr.GetName())
		require.Empty(t, ft.allErrors())
	})

	t.Run("should record a finding when the hook never arrives", func(t *testing.T) {
		// Not a nil return for the caller to check: a test that read a field off
		// a nil capture would panic, and the panic would bury the finding. The
		// hook not arriving is itself the thing to report.
		s, ft, p := newSuite()

		require.Nil(t, s.CapturedAt(p, CreateContainer, ctr))
		require.Contains(t, ft.allErrors(), "CreateContainer")
	})

	t.Run("should return a pod payload for a pod hook", func(t *testing.T) {
		s, ft, p := newSuite()

		require.NoError(t, p.RunPodSandbox(context.Background(),
			&api.PodSandbox{Id: "pod0", Name: "pod0", Namespace: "nri-test"}))

		got := s.CapturedPodAt(p, RunPodSandbox, &Pod{ID: "pod0"})
		require.NotNil(t, got)
		require.Equal(t, "pod0", got.Pod.GetName())
		require.Nil(t, got.Ctr)
		require.Empty(t, ft.allErrors())
	})
}

func TestSuiteCapturedAtSeparatesSuiteBugsFromRuntimeFindings(t *testing.T) {
	useConfig(t, &Config{Timeout: 200 * time.Millisecond, Namespace: "nri-test",
		CollectFailures: true})

	t.Run("should end the case when the hook arrived but nothing was retained", func(t *testing.T) {
		// Capture was never turned on, so the event is there and the payload is
		// not. That is this suite looking in the wrong place, not the runtime
		// failing to report something -- and the report counts the two
		// separately so a reader can tell a broken harness from a broken
		// runtime. Recording it as an unmet expectation would publish a
		// conformance finding about our own bug.
		// A report of its own: every other test here uses the same fakeT name
		// and records into the shared TestReport, so a filter by test name
		// would count their findings too.
		saved := TestReport
		TestReport = &Report{}

		t.Cleanup(func() { TestReport = saved })

		ft := &fakeT{}
		s := newFakeSuite(ft)
		s.scope = &Scope{Namespaces: []string{"nri-test"}}

		p := NewTestPlugin("input", "00", 0, s.q, s.scope)

		_, _, err := p.CreateContainer(context.Background(),
			&api.PodSandbox{Id: "pod0", Name: "pod0", Namespace: "nri-test"},
			&api.Container{Id: "ctr0", PodSandboxId: "pod0", Name: "ctr0"})
		require.NoError(t, err)

		require.Panics(t, func() {
			s.CapturedAt(p, CreateContainer, &Container{ID: "ctr0", PodID: "pod0"})
		}, "fatalf must not return")

		kinds := map[Kind]int{}
		for _, f := range TestReport.Findings() {
			kinds[f.Kind]++
		}

		require.Zero(t, kinds[KindExpectation],
			"a harness bug must not be recorded as an expectation about the runtime")
		require.Positive(t, kinds[KindOperation])
	})
}
func TestSuiteInspectPod(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})
	useDriver(t, &fakeDriver{})

	t.Run("should return what the inspector observed", func(t *testing.T) {
		useInspector(t, &podInspector{pod: &ObservedPod{IPs: []string{"10.0.0.1"}, Pid: 99}})

		s := newFakeSuite(&fakeT{})

		got := s.InspectPod(&Pod{ID: "pod0"})
		require.Equal(t, []string{"10.0.0.1"}, got.IPs)
		require.Equal(t, uint32(99), got.Pid)
	})

	t.Run("should end the case when the pod cannot be observed", func(t *testing.T) {
		// A failed observation is a failed operation, not an unmet expectation:
		// nothing about the runtime's NRI behaviour was measured by it, and the
		// report counts the two separately on purpose.
		useInspector(t, &podInspector{err: errors.New("sandbox not found")})

		ft := &fakeT{}
		s := newFakeSuite(ft)

		require.Panics(t, func() { s.InspectPod(&Pod{ID: "pod0"}) },
			"fatalf must not return")
		require.Contains(t, ft.allErrors(), "pod0")
	})
}

// podInspector is an Inspector that answers only about pods.
type podInspector struct {
	fakeExecutor

	pod *ObservedPod
	err error
}

func (i *podInspector) Inspect(context.Context, string) (*Observed, error) {
	return nil, errors.New("podInspector does not observe containers")
}

func (i *podInspector) InspectPod(context.Context, string) (*ObservedPod, error) {
	if i.err != nil {
		return nil, i.err
	}

	return i.pod, nil
}

var _ Inspector = &podInspector{}

func TestSuiteCovered(t *testing.T) {
	useConfig(t, &Config{Timeout: time.Minute, Namespace: "nri-test"})

	t.Run("should record each field against its hook", func(t *testing.T) {
		saved := TestReport
		TestReport = &Report{}

		t.Cleanup(func() { TestReport = saved })

		s := newFakeSuite(&fakeT{})
		s.Covered(CreateContainer, "Container.CDI_devices", "LinuxContainer.sysctl")

		got := TestReport.CoverageOf()
		require.Len(t, got, 2)
		require.Equal(t, "Container.CDI_devices", got[0].Field)
		require.Equal(t, CreateContainer, got[0].Hook)
		require.Equal(t, "LinuxContainer.sysctl", got[1].Field)
	})

	t.Run("should reject a field the table does not name", func(t *testing.T) {
		// A typo here would be reported forever as a field no run exercises,
		// while the field it meant would also read as unexercised. Two wrong
		// lines from one mistake, and neither of them loud.
		saved := TestReport
		TestReport = &Report{}

		t.Cleanup(func() { TestReport = saved })

		ft := &fakeT{}
		s := newFakeSuite(ft)

		require.Panics(t, func() { s.Covered(CreateContainer, "Container.nonesuch") },
			"fatalf must not return")
		require.Contains(t, ft.allErrors(), "Container.nonesuch")
	})
}
