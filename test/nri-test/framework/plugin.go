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
	"fmt"
	stdnet "net"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
)

// TestPlugin is a programmable NRI plugin. Assign the callback fields to give a
// test control over what the plugin does; unassigned callbacks do nothing.
type TestPlugin struct {
	// SynchronizeFn is invoked for the Synchronize handshake. Install it before
	// Start.
	//
	// It receives only the in-scope pods, and only the containers whose pod is one
	// of those: the runtime sends the handshake every pod and container on the
	// node, and a callback that maps over what it is handed would otherwise emit
	// updates for the whole node. Filtering the input makes that impossible rather
	// than merely caught, and keeps Synchronize consistent with every other
	// callback, all of which are scope filtered on input.
	//
	// SyncedPods and SyncedCtrs still report the unfiltered ids, and
	// RawSyncedPods and RawSyncedCtrs the unfiltered protos, for a test that needs
	// to know what the runtime actually sent -- measuring the fidelity of the very
	// fields the filter keys on has to be done there rather than here.
	SynchronizeFn func(context.Context, []*api.PodSandbox, []*api.Container) ([]*api.ContainerUpdate, error)

	RunPodSandboxFn        func(context.Context, *api.PodSandbox) error
	UpdatePodSandboxFn     func(context.Context, *api.PodSandbox, *api.LinuxResources, *api.LinuxResources) error
	PostUpdatePodSandboxFn func(context.Context, *api.PodSandbox) error
	StopPodSandboxFn       func(context.Context, *api.PodSandbox) error
	RemovePodSandboxFn     func(context.Context, *api.PodSandbox) error

	CreateContainerFn     func(context.Context, *api.PodSandbox, *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error)
	PostCreateContainerFn func(context.Context, *api.PodSandbox, *api.Container) error
	StartContainerFn      func(context.Context, *api.PodSandbox, *api.Container) error
	PostStartContainerFn  func(context.Context, *api.PodSandbox, *api.Container) error
	UpdateContainerFn     func(context.Context, *api.PodSandbox, *api.Container, *api.LinuxResources) ([]*api.ContainerUpdate, error)
	PostUpdateContainerFn func(context.Context, *api.PodSandbox, *api.Container) error
	StopContainerFn       func(context.Context, *api.PodSandbox, *api.Container) ([]*api.ContainerUpdate, error)
	RemoveContainerFn     func(context.Context, *api.PodSandbox, *api.Container) error
	// ValidateAdjustmentFn is read by Configure from the runtime's goroutine to
	// decide the subscription, so like SynchronizeFn it must be installed before
	// Start. Assigning it afterwards is both a data race and too late to matter.
	ValidateAdjustmentFn func(context.Context, *api.ValidateContainerAdjustmentRequest) error

	name  string
	idx   string
	mask  stub.EventMask
	q     *EventQ
	scope *Scope

	stub stub.Stub

	mu             sync.Mutex
	started        bool
	stopped        bool
	runtimeName    string
	runtimeVersion string
	syncedPods     []string
	syncedCtrs     []string
	// rawSyncedPods and rawSyncedCtrs are clones of the protos the most recent
	// Synchronize was sent, before scope filtering. See RawSyncedPods.
	rawSyncedPods []*api.PodSandbox
	rawSyncedCtrs []*api.Container
	// ours holds the ids of containers this plugin has seen through an in-scope
	// callback. It is the allowlist for filterUpdates: the scope filter guards
	// callback inputs, and this guards their outputs.
	ours      map[string]struct{}
	ready     chan struct{}
	readyOnce sync.Once
	// capture says whether record retains hook payloads, and captured holds
	// them. See Capture for why the default is off.
	capture  bool
	captured []*Captured
}

// Captured is the payload one NRI hook was given.
//
// Ctr is nil for a pod hook. The Event the same hook recorded carries only ids
// and names, which is all a test asserting about ordering or arrival needs;
// this is for a test asserting about the data itself.
type Captured struct {
	// Hook is the callback this payload arrived in.
	Hook EventType
	// Pod and Ctr are clones of what the hook was given. See record.
	Pod *api.PodSandbox
	Ctr *api.Container
}

// Capture turns payload retention on or off. It is off by default, and a test
// that wants to read a hook's PodSandbox or Container has to ask.
//
// Off by default because this type is also the scalability recorder: the scale
// tests time a burst of pods by the timestamps their hooks produce, and cloning
// a PodSandbox and a Container on every one of several hundred callbacks would
// be paid inside the interval being measured. EventQ.TrackCost is off by
// default for the same reason.
//
// Call it before Start. Reading p.capture happens on the runtime's goroutine,
// so flipping it mid-run is a data race on top of being unanswerable about
// which hooks it applied to.
func (p *TestPlugin) Capture(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.capture = on
}

// Captured returns the payload the given hook was given for the given pod and
// container, or nil if it was not captured.
//
// Pass an empty ctrID for a pod hook. The most recent matching capture wins: a
// hook can fire more than once for the same workload -- UpdateContainer most
// obviously -- and the latest is what the runtime most recently believed.
func (p *TestPlugin) Captured(hook EventType, podID, ctrID string) *Captured {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := len(p.captured) - 1; i >= 0; i-- {
		c := p.captured[i]
		if c.Hook == hook && c.Pod.GetId() == podID && c.Ctr.GetId() == ctrID {
			return c
		}
	}

	return nil
}

// AllCaptured returns every captured payload, in arrival order.
func (p *TestPlugin) AllCaptured() []*Captured {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]*Captured{}, p.captured...)
}

// record adds a hook's event to the queue and, when capture is on, retains the
// payload the hook was given.
//
// One function for both, so that a hook added later cannot record its event and
// silently skip the capture. That is the same hazard filterUpdates carries its
// warning comment for, and the input conformance tests would report such a hook
// as a runtime that populated nothing rather than as a plugin that looked
// nowhere.
//
// The payloads are cloned, not referenced: they belong to the stub's decoding
// of the request and nothing in its contract says they outlive the callback, so
// a capture that aliased them would report whatever they later became.
//
// Called after the scope check, from every scope-filtered callback, as
// noteContainer is. Capturing before it would retain a workload we do not own.
func (p *TestPlugin) record(hook EventType, pod *api.PodSandbox, ctr *api.Container) {
	// Retained first, published second, and the order is load-bearing. The
	// event is the signal Suite.CapturedAt waits on, and EventQ.Add releases
	// those waiters before it returns -- so retaining the payload afterwards
	// leaves a window in which a woken test reads p.captured before this
	// goroutine has appended to it. CapturedAt reports a payload it cannot find
	// as a finding against the runtime, so that window's symptom is an
	// intermittent conformance failure blamed on a runtime that behaved
	// correctly. Publishing last closes it: anything released by the event
	// necessarily sees everything done before the event existed.
	p.retain(hook, pod, ctr)

	if ctr != nil {
		p.q.Add(CtrEvent(p.FullName(), pod, ctr, hook))
	} else {
		p.q.Add(PodEvent(p.FullName(), pod, hook))
	}
}

// retain stores a clone of a hook's payload, when capture is on.
//
// Separate from record only so that record's two steps cannot be reordered by
// accident: the mutex is released before the event is published, which a single
// function body with a deferred unlock could not express.
func (p *TestPlugin) retain(hook EventType, pod *api.PodSandbox, ctr *api.Container) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.capture {
		return
	}

	c := &Captured{
		Hook: hook,
		Pod:  proto.Clone(pod).(*api.PodSandbox),
	}
	if ctr != nil {
		c.Ctr = proto.Clone(ctr).(*api.Container)
	}

	p.captured = append(p.captured, c)
}

// NewTestPlugin returns a plugin named idx-name, subscribed to mask, recording
// into q and filtered by scope.
func NewTestPlugin(name, idx string, mask stub.EventMask, q *EventQ, scope *Scope) *TestPlugin {
	return &TestPlugin{
		name:  name,
		idx:   idx,
		mask:  mask,
		q:     q,
		scope: scope,
		ours:  map[string]struct{}{},
		ready: make(chan struct{}),
	}
}

// FullName returns the plugin's full name, index first.
func (p *TestPlugin) FullName() string {
	return p.idx + "-" + p.name
}

// RuntimeName returns the runtime name the runtime sent during Configure.
func (p *TestPlugin) RuntimeName() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.runtimeName
}

// RuntimeVersion returns the runtime version the runtime sent during Configure.
func (p *TestPlugin) RuntimeVersion() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.runtimeVersion
}

// RuntimeNRIVersion returns the NRI version the runtime advertised.
func (p *TestPlugin) RuntimeNRIVersion() string {
	if p.stub == nil {
		return ""
	}

	return p.stub.RuntimeNRIVersion()
}

// RegistrationTimeout returns the registration timeout the runtime advertised.
func (p *TestPlugin) RegistrationTimeout() time.Duration {
	if p.stub == nil {
		return 0
	}

	return p.stub.RegistrationTimeout()
}

// RequestTimeout returns the request timeout the runtime advertised.
func (p *TestPlugin) RequestTimeout() time.Duration {
	if p.stub == nil {
		return 0
	}

	return p.stub.RequestTimeout()
}

// SyncedPods returns the pod ids of the most recent Synchronize.
//
// These are the ids the runtime sent, unfiltered by scope, unlike the list
// SynchronizeFn is handed. The asymmetry is deliberate: this is a diagnostic that
// answers "what did the runtime tell us about", which is itself a conformance
// question, while what a test's callback may act on must be limited to this run.
// RawSyncedPods gives the same unfiltered view of the whole proto.
func (p *TestPlugin) SyncedPods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string{}, p.syncedPods...)
}

// SyncedCtrs returns the container ids of the most recent Synchronize.
//
// Unfiltered, like SyncedPods and unlike what SynchronizeFn receives. See
// SyncedPods for why.
func (p *TestPlugin) SyncedCtrs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string{}, p.syncedCtrs...)
}

// RawSyncedPods returns the pods the most recent Synchronize was sent, before
// scope filtering, as clones of the protos the runtime delivered.
//
// This exists for fidelity measurement -- "did the runtime describe this pod
// correctly" -- and only for that. The pod fields a fidelity test measures are the
// very fields the scope filter keys on, so measuring them through the filtered
// SynchronizeFn is circular: a runtime that reported our pod with the wrong
// namespace would be filtered out, and the test would report "the runtime did not
// tell us about this pod" when the truth is "it told us with a wrong namespace".
// Those are different conformance findings, and this is how to report the right
// one.
//
// Every other test must use the pods SynchronizeFn is handed instead, which are
// scope filtered.
//
// Reading foreign workloads here does not reopen the hazard the input filter
// closed: filterUpdates still guards every exit, so a test can look at a workload
// outside this run but cannot act on one -- an update naming a container we have
// not seen through an in-scope callback is dropped and recorded. That separation,
// prevention on the way in and enforcement on the way out, is why both layers
// exist.
func (p *TestPlugin) RawSyncedPods() []*api.PodSandbox {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]*api.PodSandbox{}, p.rawSyncedPods...)
}

// RawSyncedCtrs returns the containers the most recent Synchronize was sent,
// before scope filtering, as clones of the protos the runtime delivered. For
// fidelity measurement only; see RawSyncedPods for the rule and the reasoning.
func (p *TestPlugin) RawSyncedCtrs() []*api.Container {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]*api.Container{}, p.rawSyncedCtrs...)
}

// noteContainer records a container as belonging to this run. Called from every
// scope-filtered container callback, after the scope check has passed.
func (p *TestPlugin) noteContainer(ctr *api.Container) {
	if ctr.GetId() == "" {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.ours[ctr.GetId()] = struct{}{}
}

// filterUpdates drops any update naming a container outside this run.
//
// The scope filter decides whether a callback runs at all, but whatever a callback
// returns reaches the runtime verbatim, and ContainerUpdate.ContainerId is a
// free-form string the runtime applies to that container. So a mistyped id in a
// test -- or a SynchronizeFn mapping over the node-wide list it was handed --
// would mutate a workload we do not own. Every drop is recorded, because a silent
// drop would hide a test bug just as effectively.
func (p *TestPlugin) filterUpdates(updates []*api.ContainerUpdate) []*api.ContainerUpdate {
	if len(updates) == 0 {
		return nil
	}

	// Copy the allowlist under the lock: it is written from callbacks running on
	// ttrpc goroutines, and iterating the live map would race with them.
	p.mu.Lock()
	ours := make(map[string]struct{}, len(p.ours))
	for id := range p.ours {
		ours[id] = struct{}{}
	}
	p.mu.Unlock()

	kept := make([]*api.ContainerUpdate, 0, len(updates))
	for _, u := range updates {
		if _, ok := ours[u.GetContainerId()]; ok {
			kept = append(kept, u)

			continue
		}

		p.q.Add(&Event{
			Type:   UpdateDropped,
			Plugin: p.FullName(),
			CtrID:  u.GetContainerId(),
			At:     time.Now(),
		})
	}

	if len(kept) == 0 {
		return nil
	}

	return kept
}

// UpdateContainers requests unsolicited container updates from the runtime.
//
// Returns stub.ErrNoService on a plugin that has not been started, which is what
// pkg/stub itself returns for a request made without a connection.
//
// The list is filtered before it reaches the stub: this entry point is exported
// and takes an arbitrary list, so it is the one update path reachable with no
// callback, and therefore no scope check, involved at all.
func (p *TestPlugin) UpdateContainers(updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	if p.stub == nil {
		return nil, stub.ErrNoService
	}

	return p.stub.UpdateContainers(p.filterUpdates(updates))
}

// Start connects the plugin to the runtime and waits for it to be synchronized.
//
// A TestPlugin is single-use: it may be started at most once -- successfully or
// not -- and a second Start returns an error rather than appearing to succeed.
// Readiness is signalled by closing a channel that Synchronize closes once and
// never reopens, so a restarted plugin would return from Start before the new
// Synchronize had happened, and a reconnect test would pass vacuously. Tests
// that need a reconnect construct a fresh plugin with the same name and index
// instead.
func (p *TestPlugin) Start(ctx context.Context, socket string, timeout time.Duration) error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()

		return fmt.Errorf("plugin %s has already been started; "+
			"a TestPlugin is single-use, construct a fresh one to reconnect", p.FullName())
	}
	p.started = true
	p.mu.Unlock()

	// Retain the connection so that a wedged startup can be broken. stub.Start
	// can block on an internal channel that does not observe context
	// cancellation, so closing the connection underneath it is the only way out.
	var (
		conn   stdnet.Conn
		connMu sync.Mutex
	)

	dialer := func(path string) (stdnet.Conn, error) {
		c, err := (&stdnet.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			return nil, err
		}

		connMu.Lock()
		conn = c
		connMu.Unlock()

		return c, nil
	}

	s, err := stub.New(p,
		stub.WithPluginName(p.name),
		stub.WithPluginIdx(p.idx),
		stub.WithSocketPath(socket),
		stub.WithDialer(dialer),
		stub.WithOnClose(p.recordDisconnect),
	)
	if err != nil {
		return fmt.Errorf("failed to create stub for plugin %s: %w", p.FullName(), err)
	}

	p.stub = s

	runErr := make(chan error, 1)
	go func() {
		runErr <- s.Run(ctx)
	}()

	select {
	case err := <-runErr:
		return fmt.Errorf("plugin %s exited during startup: %w", p.FullName(), err)
	case <-p.ready:
		return nil
	case <-time.After(timeout):
		connMu.Lock()
		if conn != nil {
			conn.Close()
		}
		connMu.Unlock()

		s.Stop()

		return fmt.Errorf("plugin %s did not synchronize within %v", p.FullName(), timeout)
	}
}

// recordDisconnect records that the plugin's connection to the runtime went
// down. It is wired into the stub as its onClose notification by Start.
//
// Without it a runtime-initiated disconnect is invisible: Shutdown records a
// graceful request the runtime sent, and Stopped records that we disconnected
// ourselves, so neither fires when the connection simply goes away.
func (p *TestPlugin) recordDisconnect() {
	p.q.Add(&Event{Type: Disconnected, Plugin: p.FullName(), At: time.Now()})
}

// Stop disconnects the plugin from the runtime. It is idempotent: calling it
// multiple times records only one Stopped event.
func (p *TestPlugin) Stop() {
	p.mu.Lock()
	if p.stopped || p.stub == nil {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()

	p.stub.Stop()
	p.stub.Wait()
	p.q.Add(&Event{Type: Stopped, Plugin: p.FullName(), At: time.Now()})
}

// minimalEventMask is the smallest subscription a TestPlugin may return from
// Configure.
//
// Subscribing to no events at all is meaningless -- a plugin that wants nothing
// has no reason to be connected -- so Configure must always return a deliberate,
// non-zero mask. Returning the mask explicitly is also what lets TestPlugin
// implement every handler interface, so tests can use any callback, without
// being auto-registered for every event: pkg/stub honours any non-zero mask
// verbatim, and only substitutes its own full set when the mask is zero
// ("if events == 0 { events = stub.events }" in pkg/stub/stub.go).
//
// That substitution is why zero must never escape. TestPlugin implements all the
// handler interfaces, so its "everything" is the full event set, including
// VALIDATE_CONTAINER_ADJUSTMENT -- and a registered validator makes the runtime
// build and proto.Clone a validation request for every container it creates,
// changing its behaviour for the very workloads we must not touch.
//
// POST_UPDATE_POD_SANDBOX is the floor because it is a pure notification that
// cannot adjust, update or reject anything, and it fires only on pod resource
// updates, which no runtime is currently known to implement.
//
// Do not "fix" this in pkg/stub. That is the code under conformance test.
func minimalEventMask() stub.EventMask {
	var m stub.EventMask

	m.Set(api.Event_POST_UPDATE_POD_SANDBOX)

	return m
}

// Configure implements stub.ConfigureInterface.
func (p *TestPlugin) Configure(_ context.Context, _, runtime, version string) (stub.EventMask, error) {
	p.mu.Lock()
	p.runtimeName = runtime
	p.runtimeVersion = version
	p.mu.Unlock()

	p.q.Add(&Event{Type: Configured, Plugin: p.FullName(), At: time.Now()})

	events := p.mask
	if p.ValidateAdjustmentFn == nil {
		events.Clear(api.Event_VALIDATE_CONTAINER_ADJUSTMENT)
	} else if !p.mask.IsSet(api.Event_VALIDATE_CONTAINER_ADJUSTMENT) {
		// Be loud instead of silently never consulting the validator. We
		// deliberately do not set the bit ourselves: registering a validator makes
		// the runtime build a validation request for every container it creates,
		// changing its behaviour for the very workloads we must not touch, so
		// subscribing to it must always be an explicit choice by the test.
		return 0, fmt.Errorf("plugin %s has a ValidateAdjustmentFn but its event "+
			"mask does not include VALIDATE_CONTAINER_ADJUSTMENT; add the event to "+
			"the mask or drop the function", p.FullName())
	}

	// Never return a zero mask. See minimalEventMask.
	if events == 0 {
		events = minimalEventMask()
	}

	return events, nil
}

// Synchronize implements stub.SynchronizeInterface.
func (p *TestPlugin) Synchronize(ctx context.Context, pods []*api.PodSandbox, ctrs []*api.Container) ([]*api.ContainerUpdate, error) {
	// Capture what the runtime sent before any filtering, ids and protos both. The
	// protos are cloned: they are request scoped, and retaining live request
	// protos past the call is a trap even though nothing mutates them today.
	p.mu.Lock()
	p.syncedPods = p.syncedPods[:0]
	p.rawSyncedPods = make([]*api.PodSandbox, 0, len(pods))
	for _, pod := range pods {
		p.syncedPods = append(p.syncedPods, pod.GetId())
		p.rawSyncedPods = append(p.rawSyncedPods, proto.Clone(pod).(*api.PodSandbox))
	}

	p.syncedCtrs = p.syncedCtrs[:0]
	p.rawSyncedCtrs = make([]*api.Container, 0, len(ctrs))
	for _, ctr := range ctrs {
		p.syncedCtrs = append(p.syncedCtrs, ctr.GetId())
		p.rawSyncedCtrs = append(p.rawSyncedCtrs, proto.Clone(ctr).(*api.Container))
	}
	p.mu.Unlock()

	// The runtime hands the handshake its whole world: every pod and container on
	// the node. Every other callback is scope filtered on its input, and
	// Synchronize used to be the documented exception -- which is exactly the kind
	// of exception that gets forgotten. So the in-scope set is derived here, from
	// the pods themselves, rather than inherited from a scope check that never
	// ran, and only the in-scope part of it is passed on. A SynchronizeFn that
	// maps over the lists it is handed then cannot emit an update for a foreign
	// container even by accident, because it never sees one; and a legitimate
	// synchronization-time update for one of our own containers still goes
	// through.
	inScope := make(map[string]struct{}, len(pods))
	ourPods := make([]*api.PodSandbox, 0, len(pods))
	for _, pod := range pods {
		// An empty pod id would make every container with an empty PodSandboxId
		// look in scope, so it is never a key.
		if pod.GetId() != "" && p.scope.InScope(pod) {
			inScope[pod.GetId()] = struct{}{}
			ourPods = append(ourPods, pod)
		}
	}

	// A container is ours only if its pod is one of ours. A container whose
	// PodSandboxId matches no in-scope pod -- including one whose pod is absent
	// from the list entirely -- stays foreign.
	ourCtrs := make([]*api.Container, 0, len(ctrs))
	for _, ctr := range ctrs {
		if _, ok := inScope[ctr.GetPodSandboxId()]; ok {
			p.noteContainer(ctr)
			ourCtrs = append(ourCtrs, ctr)
		}
	}

	var (
		updates []*api.ContainerUpdate
		err     error
	)

	// Invoked while still inside Synchronize, so a test can block here to hold
	// the synchronization window open. It is handed the filtered lists: input
	// filtering as prevention, filterUpdates below as defence in depth.
	if p.SynchronizeFn != nil {
		updates, err = p.SynchronizeFn(ctx, ourPods, ourCtrs)
	}

	p.q.Add(&Event{Type: Synchronized, Plugin: p.FullName(), At: time.Now()})
	p.readyOnce.Do(func() { close(p.ready) })

	return p.filterUpdates(updates), err
}

// Shutdown implements stub.ShutdownInterface.
func (p *TestPlugin) Shutdown(context.Context) {
	p.q.Add(&Event{Type: Shutdown, Plugin: p.FullName(), At: time.Now()})
}

// RunPodSandbox implements stub.RunPodInterface.
func (p *TestPlugin) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.record(RunPodSandbox, pod, nil)

	if p.RunPodSandboxFn != nil {
		return p.RunPodSandboxFn(ctx, pod)
	}

	return nil
}

// UpdatePodSandbox implements stub.UpdatePodInterface.
func (p *TestPlugin) UpdatePodSandbox(ctx context.Context, pod *api.PodSandbox, overhead, res *api.LinuxResources) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.record(UpdatePodSandbox, pod, nil)

	if p.UpdatePodSandboxFn != nil {
		return p.UpdatePodSandboxFn(ctx, pod, overhead, res)
	}

	return nil
}

// PostUpdatePodSandbox implements stub.PostUpdatePodInterface.
func (p *TestPlugin) PostUpdatePodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.record(PostUpdatePodSandbox, pod, nil)

	if p.PostUpdatePodSandboxFn != nil {
		return p.PostUpdatePodSandboxFn(ctx, pod)
	}

	return nil
}

// StopPodSandbox implements stub.StopPodInterface.
func (p *TestPlugin) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.record(StopPodSandbox, pod, nil)

	if p.StopPodSandboxFn != nil {
		return p.StopPodSandboxFn(ctx, pod)
	}

	return nil
}

// RemovePodSandbox implements stub.RemovePodInterface.
func (p *TestPlugin) RemovePodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.record(RemovePodSandbox, pod, nil)

	if p.RemovePodSandboxFn != nil {
		return p.RemovePodSandboxFn(ctx, pod)
	}

	return nil
}

// Every callback that can return []*api.ContainerUpdate must pass it through
// filterUpdates. If you add another, add it there too.

// CreateContainer implements stub.CreateContainerInterface.
func (p *TestPlugin) CreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	if !p.scope.InScope(pod) {
		return nil, nil, nil
	}

	p.noteContainer(ctr)
	p.record(CreateContainer, pod, ctr)

	if p.CreateContainerFn != nil {
		adjust, updates, err := p.CreateContainerFn(ctx, pod, ctr)

		return adjust, p.filterUpdates(updates), err
	}

	return nil, nil, nil
}

// PostCreateContainer implements stub.PostCreateContainerInterface.
func (p *TestPlugin) PostCreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.noteContainer(ctr)
	p.record(PostCreateContainer, pod, ctr)

	if p.PostCreateContainerFn != nil {
		return p.PostCreateContainerFn(ctx, pod, ctr)
	}

	return nil
}

// StartContainer implements stub.StartContainerInterface.
func (p *TestPlugin) StartContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.noteContainer(ctr)
	p.record(StartContainer, pod, ctr)

	if p.StartContainerFn != nil {
		return p.StartContainerFn(ctx, pod, ctr)
	}

	return nil
}

// PostStartContainer implements stub.PostStartContainerInterface.
func (p *TestPlugin) PostStartContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.noteContainer(ctr)
	p.record(PostStartContainer, pod, ctr)

	if p.PostStartContainerFn != nil {
		return p.PostStartContainerFn(ctx, pod, ctr)
	}

	return nil
}

// UpdateContainer implements stub.UpdateContainerInterface.
func (p *TestPlugin) UpdateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container, res *api.LinuxResources) ([]*api.ContainerUpdate, error) {
	if !p.scope.InScope(pod) {
		return nil, nil
	}

	p.noteContainer(ctr)
	p.record(UpdateContainer, pod, ctr)

	if p.UpdateContainerFn != nil {
		updates, err := p.UpdateContainerFn(ctx, pod, ctr, res)

		return p.filterUpdates(updates), err
	}

	return nil, nil
}

// PostUpdateContainer implements stub.PostUpdateContainerInterface.
func (p *TestPlugin) PostUpdateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	p.noteContainer(ctr)
	p.record(PostUpdateContainer, pod, ctr)

	if p.PostUpdateContainerFn != nil {
		return p.PostUpdateContainerFn(ctx, pod, ctr)
	}

	return nil
}

// StopContainer implements stub.StopContainerInterface.
func (p *TestPlugin) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	if !p.scope.InScope(pod) {
		return nil, nil
	}

	p.noteContainer(ctr)
	p.record(StopContainer, pod, ctr)

	if p.StopContainerFn != nil {
		updates, err := p.StopContainerFn(ctx, pod, ctr)

		return p.filterUpdates(updates), err
	}

	return nil, nil
}

// RemoveContainer implements stub.RemoveContainerInterface.
func (p *TestPlugin) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if !p.scope.InScope(pod) {
		return nil
	}

	// Noted here too, and never forgotten: a plugin may legitimately return an
	// update for a container it is being told about at removal time.
	p.noteContainer(ctr)
	p.record(RemoveContainer, pod, ctr)

	if p.RemoveContainerFn != nil {
		return p.RemoveContainerFn(ctx, pod, ctr)
	}

	return nil
}

// ValidateContainerAdjustment implements stub.ValidateContainerAdjustmentInterface.
func (p *TestPlugin) ValidateContainerAdjustment(ctx context.Context, req *api.ValidateContainerAdjustmentRequest) error {
	if !p.scope.InScope(req.GetPod()) {
		return nil
	}

	p.noteContainer(req.GetContainer())
	p.q.Add(CtrEvent(p.FullName(), req.GetPod(), req.GetContainer(), ValidateContainerAdjustment))

	if p.ValidateAdjustmentFn != nil {
		return p.ValidateAdjustmentFn(ctx, req)
	}

	return nil
}

var (
	_ = stub.ConfigureInterface(&TestPlugin{})
	_ = stub.SynchronizeInterface(&TestPlugin{})
	_ = stub.ShutdownInterface(&TestPlugin{})
	_ = stub.RunPodInterface(&TestPlugin{})
	_ = stub.UpdatePodInterface(&TestPlugin{})
	_ = stub.PostUpdatePodInterface(&TestPlugin{})
	_ = stub.StopPodInterface(&TestPlugin{})
	_ = stub.RemovePodInterface(&TestPlugin{})
	_ = stub.CreateContainerInterface(&TestPlugin{})
	_ = stub.PostCreateContainerInterface(&TestPlugin{})
	_ = stub.StartContainerInterface(&TestPlugin{})
	_ = stub.PostStartContainerInterface(&TestPlugin{})
	_ = stub.UpdateContainerInterface(&TestPlugin{})
	_ = stub.PostUpdateContainerInterface(&TestPlugin{})
	_ = stub.StopContainerInterface(&TestPlugin{})
	_ = stub.RemoveContainerInterface(&TestPlugin{})
	_ = stub.ValidateContainerAdjustmentInterface(&TestPlugin{})
)
