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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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
// It keeps no per-container state beyond that record, so it models a plugin
// that can answer concurrently. A plugin that serialises internally would
// hide the thing being measured.
type delayPlugin struct {
	name  string
	idx   string
	stub  stub.Stub
	delay func(ctr *api.Container) time.Duration

	synced chan struct{}
	once   sync.Once

	mu   sync.Mutex
	seen []string
}

func newDelayPlugin(idx int, delay func(*api.Container) time.Duration) *delayPlugin {
	return &delayPlugin{
		name:   fmt.Sprintf("delay%d", idx),
		idx:    fmt.Sprintf("%02d", idx),
		delay:  delay,
		synced: make(chan struct{}),
	}
}

func (p *delayPlugin) Configure(context.Context, string, string, string) (stub.EventMask, error) {
	return api.MustParseEventMask("CreateContainer", "StopContainer"), nil
}

func (p *delayPlugin) Synchronize(context.Context, []*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error) {
	p.once.Do(func() { close(p.synced) })
	return nil, nil
}

func (p *delayPlugin) CreateContainer(_ context.Context, _ *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	p.observe(ctr.Id)
	time.Sleep(p.delay(ctr))

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

// observe records that this plugin has taken ctr's request, before the
// delay rather than after, so seen is the order the plugin started them in.
func (p *delayPlugin) observe(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, id)
}

// Seen returns the container ids this plugin took requests for, in order.
func (p *delayPlugin) Seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

func (p *delayPlugin) start(dir string) error {
	s, err := stub.New(p,
		stub.WithPluginName(p.name),
		stub.WithPluginIdx(p.idx),
		stub.WithSocketPath(filepath.Join(dir, "nri.sock")),
	)
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
}

// startRig brings up an Adaptation with count plugins, each answering after
// delay, and waits until every one of them is eligible for events.
func startRig(t *testing.T, count int, delay func(*api.Container) time.Duration) *scaleRig {
	t.Helper()

	logrus.SetLevel(logrus.ErrorLevel)

	// Not t.TempDir(): its paths run past the platform's socket limit.
	dir, err := os.MkdirTemp("", "nri-scale-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })

	require.NoError(t, os.MkdirAll(filepath.Join(dir, "etc", "nri"), 0o755))

	sync := func(ctx context.Context, cb nri.SyncCB) error {
		_, err := cb(ctx, nil, nil)
		return err
	}
	update := func(context.Context, []*nri.ContainerUpdate) ([]*nri.ContainerUpdate, error) {
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

	rig := &scaleRig{runtime: runtime}

	for i := 0; i < count; i++ {
		p := newDelayPlugin(i, delay)
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

	// Synchronize returning is not the same as being eligible for events;
	// taking the sync block and dropping it again waits out that window.
	runtime.BlockPluginSync().Unblock()

	return rig
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
			_, err := rig.runtime.CreateContainer(context.Background(),
				&api.CreateContainerRequest{Pod: pod(), Container: container(i)})
			return err
		},
		"StopContainer": func(rig *scaleRig, i int) error {
			_, err := rig.runtime.StopContainer(context.Background(),
				&api.StopContainerRequest{Pod: pod(), Container: container(i)})
			return err
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
				_, err := rig.runtime.CreateContainer(context.Background(),
					&api.CreateContainerRequest{Pod: pod(), Container: container(i)})
				return err
			})
		}
		if i < stops {
			wg.Add(1)
			go issue("StopContainer", func() error {
				_, err := rig.runtime.StopContainer(context.Background(),
					&api.StopContainerRequest{Pod: pod(), Container: container(1000 + i)})
				return err
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
