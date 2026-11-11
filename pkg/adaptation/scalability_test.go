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

package adaptation_test

// A measurement rig for the cost of ordering NRI requests.
//
// Adaptation holds one mutex across a whole request, every plugin round
// trip included, so N concurrent requests against P plugins that each take
// d to answer retire in N*P*d: the last one in waits out everyone else.
// Nothing below Adaptation imposes that. ttrpc multiplexes concurrent
// calls, and the stub answers each in its own goroutine.
//
// What the lock buys is an order. The property a runtime needs is that if a
// plugin processed two requests in some order, their answers are applied in
// that same order -- otherwise the older decision lands last and wins.
// Serialising whole requests gets there by leaving only one request in
// flight, which is also why it costs N*P*d.
//
// The tests here measure that cost and pin the property, so a change that
// buys the time back can be told from one that drops the guarantee:
//
//   - TestScalabilityMatrix prints wall time over a grid of N and P. The
//     signature of request-level serialisation is time linear in both.
//   - TestScalabilityPathological is the case the lock is worst for.
//
// The ordering guarantee itself is checked in ordering_internal_test.go,
// not here: what has to hold is the order of the commit sections, and from
// outside NRI a request that committed second can still be written down
// first. A test built out here would be measuring the scheduler.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nri "github.com/containerd/nri/pkg/adaptation"
	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// delayPlugin answers CreateContainer and StopContainer after a delay, and
// records the order it saw them in.
//
// It keeps no per-container state beyond that record and does no locking
// of its own around a handler, so any overlap between handlers is the
// stub's doing.
type delayPlugin struct {
	name  string
	idx   string
	stub  stub.Stub
	delay func(ctr *api.Container) time.Duration

	synced chan struct{}
	once   sync.Once

	// failCreate makes CreateContainer refuse, so a request can be made to
	// fail before it reaches its apply.
	failCreate atomic.Bool
	// updateFromHandler asks for an unsolicited container update from
	// inside the CreateContainer handler, synchronously, while the request
	// that is being answered still holds its place in the order.
	updateFromHandler atomic.Bool
	// updateFromConfigure asks for an unsolicited container update from
	// inside the Configure handler, while the plugin is still starting and
	// has yet to be told it is configured.
	updateFromConfigure atomic.Bool
	// validates subscribes this plugin to container adjustment validation
	// as well as to the events it answers. Read by Configure, so a test
	// sets it before the plugin starts.
	validates bool

	mu   sync.Mutex
	seen []string
	// validated is the containers this plugin was asked to validate the
	// adjustment of, in order.
	validated []string
	// held holds a CreateContainer handler open until the test closes the
	// channel it was given for that container.
	held map[string]chan struct{}
	// inside is how many CreateContainer handlers are running right now,
	// and overlap counts the handlers that started while another ran.
	inside  int
	overlap int
}

func newDelayPlugin(idx int, delay func(*api.Container) time.Duration) *delayPlugin {
	return &delayPlugin{
		name:   fmt.Sprintf("delay%d", idx),
		idx:    fmt.Sprintf("%02d", idx),
		delay:  delay,
		synced: make(chan struct{}),
	}
}

func (p *delayPlugin) Configure(ctx context.Context, _, _, _ string) (stub.EventMask, error) {
	if p.updateFromConfigure.Load() {
		_, err := p.stub.UpdateContainers(ctx, []*api.ContainerUpdate{
			{ContainerId: "configuring"},
		})
		if err != nil {
			return 0, err
		}
	}

	events := []string{"CreateContainer", "StopContainer"}
	if p.validates {
		events = append(events, "ValidateContainerAdjustment")
	}

	return api.MustParseEventMask(events...), nil
}

func (p *delayPlugin) Synchronize(context.Context, []*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error) {
	p.once.Do(func() { close(p.synced) })
	return nil, nil
}

func (p *delayPlugin) CreateContainer(ctx context.Context, _ *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	if p.failCreate.Load() {
		return nil, nil, errors.New("refused by the plugin")
	}

	p.observe(ctr.Id)
	p.enter()
	defer p.leave()

	p.waitHeld(ctr.Id)

	time.Sleep(p.delay(ctr))

	if p.updateFromHandler.Load() {
		_, err := p.stub.UpdateContainers(ctx, []*api.ContainerUpdate{
			{ContainerId: "unsolicited"},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	// Adjust something, so the per-request merge across plugins runs. The
	// key carries the plugin name because two plugins setting one key is a
	// conflict Adaptation refuses.
	a := &api.ContainerAdjustment{}
	a.AddAnnotation("nri.scale/"+p.name, ctr.Id)

	return a, nil, nil
}

func (p *delayPlugin) StopContainer(_ context.Context, _ *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	p.observe(ctr.Id)
	time.Sleep(p.delay(ctr))
	return nil, nil
}

// ValidateContainerAdjustment approves every adjustment it is asked about,
// and records that it was asked.
func (p *delayPlugin) ValidateContainerAdjustment(_ context.Context, req *api.ValidateContainerAdjustmentRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.validated = append(p.validated, req.GetContainer().GetId())

	return nil
}

// Validated returns the container ids this plugin was asked to validate
// the adjustment of, in order.
func (p *delayPlugin) Validated() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.validated...)
}

// holdCreate returns a channel that keeps id's CreateContainer handler in
// the plugin until the channel is closed.
func (p *delayPlugin) holdCreate(id string) chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.held == nil {
		p.held = map[string]chan struct{}{}
	}

	held := make(chan struct{})
	p.held[id] = held

	return held
}

// waitHeld returns once nothing holds id's CreateContainer handler.
func (p *delayPlugin) waitHeld(id string) {
	p.mu.Lock()
	held := p.held[id]
	p.mu.Unlock()

	if held != nil {
		<-held
	}
}

// observe records that this plugin has taken ctr's request, before the
// delay rather than after, so seen is the order the plugin started them in.
func (p *delayPlugin) observe(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, id)
}

// enter records that a CreateContainer handler has started.
func (p *delayPlugin) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.inside > 0 {
		p.overlap++
	}

	p.inside++
}

// leave records that a CreateContainer handler has finished.
func (p *delayPlugin) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.inside--
}

// Overlap returns how many CreateContainer handlers started while another
// one was running.
func (p *delayPlugin) Overlap() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.overlap
}

// Seen returns the container ids this plugin took requests for, in order.
func (p *delayPlugin) Seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *delayPlugin) start(dir string) error {
	opts := []stub.Option{
		stub.WithPluginName(p.name),
		stub.WithPluginIdx(p.idx),
		stub.WithSocketPath(filepath.Join(dir, "nri.sock")),
	}

	s, err := stub.New(p, opts...)
	if err != nil {
		return err
	}

	p.stub = s

	return p.stub.Start(context.Background())
}

func (p *delayPlugin) stop() {
	if p.stub != nil {
		p.stub.Stop()
		p.stub.Wait()
	}
}

// scaleRig is a started Adaptation with plugins attached and synchronized.
type scaleRig struct {
	runtime *nri.Adaptation
	plugins []*delayPlugin

	// dir holds the runtime's socket, so a test can connect a plugin of
	// its own to it.
	dir string

	// update stands in for the runtime's UpdateFn when a test sets it, so a
	// plugin's unsolicited updates can be observed.
	update func(context.Context, []*nri.ContainerUpdate) ([]*nri.ContainerUpdate, error)
}

// rigOption prepares the i'th plugin of a rig before it starts.
type rigOption func(i int, p *delayPlugin)

// validatesAdjustments returns an option that subscribes plugin idx to
// container adjustment validation as well as to the events it answers.
func validatesAdjustments(idx int) rigOption {
	return func(i int, p *delayPlugin) {
		if i == idx {
			p.validates = true
		}
	}
}

// startRig brings up an Adaptation with count plugins, each answering after
// delay and prepared by every option given, and waits until every one of
// them is eligible for events.
func startRig(t *testing.T, count int, delay func(*api.Container) time.Duration, opts ...rigOption) *scaleRig {
	t.Helper()

	logrus.SetLevel(logrus.ErrorLevel)

	// Not t.TempDir(): its paths run past the platform's socket limit.
	dir, err := os.MkdirTemp("", "nri-scale-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "etc", "nri"), 0o755))

	rig := &scaleRig{dir: dir}

	sync := func(ctx context.Context, cb nri.SyncCB) error {
		_, err := cb(ctx, nil, nil)
		return err
	}
	update := func(ctx context.Context, u []*nri.ContainerUpdate) ([]*nri.ContainerUpdate, error) {
		if rig.update != nil {
			return rig.update(ctx, u)
		}

		return nil, nil
	}

	runtime, err := nri.New("scale-runtime", "0.0.1", sync, update,
		nri.WithPluginPath(filepath.Join(dir, "opt", "nri", "plugins")),
		nri.WithPluginConfigPath(filepath.Join(dir, "etc", "nri", "conf.d")),
		nri.WithSocketPath(filepath.Join(dir, "nri.sock")),
	)
	require.NoError(t, err)
	require.NoError(t, runtime.Start())
	t.Cleanup(runtime.Stop)

	rig.runtime = runtime

	for i := 0; i < count; i++ {
		p := newDelayPlugin(i, delay)

		for _, opt := range opts {
			opt(i, p)
		}

		require.NoError(t, p.start(dir))
		t.Cleanup(p.stop)
		rig.plugins = append(rig.plugins, p)
	}

	for _, p := range rig.plugins {
		select {
		case <-p.synced:
		case <-time.After(startupTimeout):
			t.Fatalf("plugin %s did not synchronize", p.name)
		}
	}

	rig.waitEligible()

	return rig
}

// waitEligible waits until every plugin that has been synchronized is
// eligible for events.
//
// Synchronize returning is not the same as being eligible: the runtime
// lists a plugin as live after its synchronization, and a request numbered
// in between takes the plugin list without that plugin in it and reaches
// nobody. Taking the sync block and dropping it again waits out that
// window, because the runtime holds it across both steps.
func (r *scaleRig) waitEligible() {
	r.runtime.BlockPluginSync().Unblock()
}

// pod returns the sandbox every request in these tests names.
func pod() *api.PodSandbox {
	return &api.PodSandbox{
		Id:        "scale-pod",
		Name:      "scale-pod",
		Namespace: "scale",
	}
}

// container returns the i'th container, with an id that sorts in creation
// order so a recorded order can be read at a glance.
func container(i int) *api.Container {
	id := fmt.Sprintf("ctr-%04d", i)
	return &api.Container{
		Id:    id,
		Name:  id,
		State: api.ContainerState_CONTAINER_CREATED,
	}
}

// fixedDelay answers every request after d.
func fixedDelay(d time.Duration) func(*api.Container) time.Duration {
	return func(*api.Container) time.Duration { return d }
}

// TestScalabilityMatrix reports what concurrent requests cost today.
//
// It asserts nothing. Request-level serialisation shows up as wall time
// that grows with both N and P -- the last request in waits out every other
// request's every plugin -- and the numbers are the baseline a change to
// the ordering mechanism has to beat.
func TestScalabilityMatrix(t *testing.T) {
	const delay = 10 * time.Millisecond

	counts := []int{1, 8, 32}
	plugins := []int{1, 2, 4}

	if testing.Short() {
		counts, plugins = []int{1, 8}, []int{1, 2}
	}

	type row struct {
		event     string
		n, p      int
		total     time.Duration
		slowest   time.Duration
		perRecord time.Duration
	}

	events := map[string]func(*scaleRig, int) error{
		"CreateContainer": func(rig *scaleRig, i int) error {
			return rig.runtime.CreateContainerAndApply(context.Background(),
				&api.CreateContainerRequest{Pod: pod(), Container: container(i)}, nil)
		},
		"StopContainer": func(rig *scaleRig, i int) error {
			return rig.runtime.StopContainerAndApply(context.Background(),
				&api.StopContainerRequest{Pod: pod(), Container: container(i)}, nil)
		},
	}
	order := []string{"CreateContainer", "StopContainer"}

	var rows []row

	for _, event := range order {
		issue := events[event]
		for _, p := range plugins {
			for _, n := range counts {
				rig := startRig(t, p, fixedDelay(delay))

				var (
					wg      sync.WaitGroup
					mu      sync.Mutex
					slowest time.Duration
				)

				start := time.Now()

				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						began := time.Now()
						err := issue(rig, i)
						took := time.Since(began)
						mu.Lock()
						defer mu.Unlock()
						if took > slowest {
							slowest = took
						}
						if err != nil {
							t.Errorf("%s: %v", event, err)
						}
					}(i)
				}

				wg.Wait()
				total := time.Since(start)

				rows = append(rows, row{event: event, n: n, p: p, total: total,
					slowest: slowest, perRecord: total / time.Duration(n)})
			}
		}
	}

	t.Logf("plugin delay %v per request", delay)
	t.Logf("%-16s %4s %4s %12s %12s %12s %10s", "event", "N", "P",
		"total", "slowest", "per-request", "vs N*P*d")
	for _, r := range rows {
		ideal := time.Duration(r.n*r.p) * delay
		t.Logf("%-16s %4d %4d %12v %12v %12v %10.2f", r.event, r.n, r.p,
			r.total.Round(time.Millisecond), r.slowest.Round(time.Millisecond),
			r.perRecord.Round(time.Millisecond), float64(r.total)/float64(ideal))
	}
}

// TestScalabilityPathological is the case the lock is worst for: one plugin
// that takes a long time, and a burst of creations and stops issued at
// once.
//
// Both are requests a plugin can answer with a change, so both are
// ordered, and under the lock all 100 of them queue behind each other for
// 100*100ms. There is no reason they should: the plugin answers them
// concurrently, and only applying the answers has to be kept in order.
func TestScalabilityPathological(t *testing.T) {
	const (
		creates = 50
		stops   = 50
		delay   = 100 * time.Millisecond
	)

	rig := startRig(t, 1, fixedDelay(delay))

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		slowest time.Duration
		failed  int
	)

	issue := func(name string, call func() error) {
		defer wg.Done()
		began := time.Now()
		err := call()
		took := time.Since(began)

		mu.Lock()
		defer mu.Unlock()
		if took > slowest {
			slowest = took
		}
		if err != nil {
			failed++
			t.Errorf("%s: %v", name, err)
		}
	}

	start := time.Now()

	// Creations and stops released together, so the two request kinds
	// contend with each other and not just with their own kind.
	for i := 0; i < creates || i < stops; i++ {
		if i < creates {
			wg.Add(1)
			go issue("CreateContainer", func() error {
				return rig.runtime.CreateContainerAndApply(context.Background(),
					&api.CreateContainerRequest{Pod: pod(), Container: container(i)}, nil)
			})
		}
		if i < stops {
			wg.Add(1)
			go issue("StopContainer", func() error {
				return rig.runtime.StopContainerAndApply(context.Background(),
					&api.StopContainerRequest{Pod: pod(), Container: container(1000 + i)}, nil)
			})
		}
	}

	wg.Wait()
	total := time.Since(start)

	n := creates + stops
	serial := time.Duration(n) * delay

	t.Logf("1 plugin, %v per request, %d creations and %d stops at once",
		delay, creates, stops)
	t.Logf("  total        %v", total.Round(time.Millisecond))
	t.Logf("  slowest      %v", slowest.Round(time.Millisecond))
	t.Logf("  one at a time %v (%d x %v)", serial, n, delay)
	t.Logf("  speedup      %.1fx", float64(serial)/float64(total))
	t.Logf("  plugin saw   %d requests, %d failed", len(rig.plugins[0].Seen()), failed)
}

// TestApplyRunsInArrivalOrder is the guarantee as a runtime sees it.
//
// Observable from out here only because apply runs inside the commit
// section: what it writes down is written while the order is held, so
// there is no window between committing and recording for the scheduler to
// reorder. That is what the old end-to-end check lacked, and why it had to
// measure the scheduler instead.
//
// Requests are issued one at a time, each handed to its own goroutine once
// the plugin has taken the previous one, so the arrival order is known. The
// plugin's delay falls as they arrive, so whichever it sees last it answers
// first: an implementation that applied answers as they came back would
// reverse them.
func TestApplyRunsInArrivalOrder(t *testing.T) {
	const (
		count = 8
		step  = 20 * time.Millisecond
	)

	delay := func(ctr *api.Container) time.Duration {
		i, err := strconv.Atoi(ctr.Id[len("ctr-"):])
		if err != nil {
			return step
		}

		return time.Duration(count-i) * step
	}

	rig := startRig(t, 1, delay)
	plugin := rig.plugins[0]

	var (
		mu      sync.Mutex
		applied []string
		inside  int
		overlap int
		wg      sync.WaitGroup
	)

	for i := 0; i < count; i++ {
		ctr := container(i)

		wg.Add(1)

		go func() {
			defer wg.Done()

			err := rig.runtime.CreateContainerAndApply(context.Background(),
				&api.CreateContainerRequest{Pod: pod(), Container: ctr},
				func(*api.CreateContainerResponse) error {
					mu.Lock()
					inside++
					if inside > overlap {
						overlap = inside
					}
					applied = append(applied, ctr.Id)
					mu.Unlock()

					// Held open, so an implementation that let the next
					// request apply early is caught rather than raced with.
					time.Sleep(time.Millisecond)

					mu.Lock()
					inside--
					mu.Unlock()

					return nil
				})
			if err != nil {
				t.Errorf("CreateContainerAndApply %s: %v", ctr.Id, err)
			}
		}()

		waitForObserved(t, plugin, i+1)
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, 1, overlap, "two requests must not apply at once")
	require.Equal(t, plugin.Seen(), applied,
		"answers must be applied in the order the requests arrived")
}

// waitForObserved blocks until plugin has taken at least want requests.
//
// How a test fixes the arrival order: the next request is not issued until
// the plugin has taken the previous one, so what arrives when is the test's
// choice rather than the scheduler's.
func waitForObserved(t *testing.T, plugin *delayPlugin, want int) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(plugin.Seen()) >= want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("plugin %s took %d requests, waited for %d",
		plugin.name, len(plugin.Seen()), want)
}

func TestUnorderedRequestDoesNotBlockAnApply(t *testing.T) {
	// Ten of the thirteen requests cannot be answered with a change, so they
	// take a number and announce a hole rather than an apply. A hole that is
	// never announced leaves the applier waiting on it, and every later
	// request's answer with it.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	require.NoError(t, rig.runtime.RunPodSandbox(context.Background(),
		&api.RunPodSandboxRequest{Pod: pod()}))

	applied := make(chan struct{})

	go func() {
		err := rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
			func(*api.CreateContainerResponse) error {
				close(applied)
				return nil
			})
		require.NoError(t, err)
	}()

	select {
	case <-applied:
	case <-time.After(10 * time.Second):
		t.Fatal("an apply never ran: an unordered request left a hole in the order")
	}
}

func TestApplyErrorDoesNotStallTheOrder(t *testing.T) {
	// A request whose apply fails still has to hand the order on, or every
	// later request waits for a turn that never comes.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	first := rig.runtime.CreateContainerAndApply(context.Background(),
		&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
		func(*api.CreateContainerResponse) error {
			return errors.New("apply refused")
		})
	require.Error(t, first, "an apply that returns an error must surface it")

	second := make(chan error, 1)

	go func() {
		second <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(1)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	select {
	case err := <-second:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the request after a failed apply never got its turn")
	}
}

func TestUnsolicitedUpdateFromHandlerDoesNotDeadlock(t *testing.T) {
	// A plugin may ask for container updates of its own while it is still
	// answering a request. That path takes no place in the apply order, and
	// both runtime integrations rely on it: one that gives it a turn behind
	// the request in flight deadlocks, because the request will not hand
	// the order on until the plugin has answered.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	applied := make(chan []*nri.ContainerUpdate, 1)
	rig.update = func(_ context.Context, u []*nri.ContainerUpdate) ([]*nri.ContainerUpdate, error) {
		applied <- u

		return nil, nil
	}

	rig.plugins[0].updateFromHandler.Store(true)

	done := make(chan error, 1)

	go func() {
		done <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a request deadlocked behind its own plugin's unsolicited update")
	}

	select {
	case u := <-applied:
		require.Len(t, u, 1)
		require.Equal(t, "unsolicited", u[0].GetContainerId())
	default:
		t.Fatal("the unsolicited update never reached the runtime")
	}
}

func TestAFailedRequestReleasesItsNumber(t *testing.T) {
	// A request takes its number before it talks to plugins, so one that
	// fails on the way -- a plugin refusing it, a validator rejecting it --
	// never reaches its apply. Its number still has to be handed on, or
	// every later request waits for a turn that never comes.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	rig.plugins[0].failCreate.Store(true)

	err := rig.runtime.CreateContainerAndApply(context.Background(),
		&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
		func(*api.CreateContainerResponse) error { return nil })
	require.Error(t, err, "the refusing plugin's error must surface")

	rig.plugins[0].failCreate.Store(false)

	applied := make(chan error, 1)

	go func() {
		applied <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(1)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	select {
	case err := <-applied:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the request after a failed one never got its turn")
	}
}
