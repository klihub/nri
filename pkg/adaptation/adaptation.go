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

package adaptation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/containerd/nri/pkg/adaptation/builtin"
	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/log"
	nriver "github.com/containerd/nri/pkg/version"
	validator "github.com/containerd/nri/plugins/default-validator/builtin"
	"github.com/containerd/ttrpc"

	"google.golang.org/protobuf/proto"
)

const (
	// DefaultPluginPath is the default path to search for NRI plugins.
	DefaultPluginPath = "/opt/nri/plugins"
	// DefaultSocketPath is the default socket path for external plugins.
	DefaultSocketPath = api.DefaultSocketPath
	// DefaultPluginConfigPath is the drop-in directory for NRI-launched plugin configuration.
	DefaultPluginConfigPath = "/etc/nri/conf.d"
)

// SyncFn is a container runtime function for state synchronization.
type SyncFn func(context.Context, SyncCB) error

// SyncCB is an NRI function used to synchronize plugins with the runtime.
type SyncCB func(context.Context, []*PodSandbox, []*Container) ([]*ContainerUpdate, error)

// UpdateFn is a container runtime function for unsolicited container
// updates.
//
// An update from a plugin on an event stream applies in the place in the
// order its plugin decided it, so this runs wherever the order reaches that
// place: on another request's goroutine where that request brings the order
// to the place, and on the goroutine the stream reader answers the update
// on where the order is at the place already. Two rules it is held to,
// because the order runs across it:
//
//   - it must not call back into Adaptation. A nested request takes a later
//     number and waits for a turn that cannot come until this returns, and
//     that wait honours no deadline.
//   - it must not block on a lock a CRI caller can hold while that caller
//     is inside NRI. Neither side has a deadline, so each waits out the
//     other for good.
//
// Stop waits behind this on the first of those two goroutines: it runs
// there inside the order with Adaptation's lock held for reading, and the
// write lock Stop takes queues behind that read lock. On the other it holds
// no lock of Adaptation's, and what holds Stop off is the request whose
// number the update was placed below -- which for an otherwise idle
// plugin's update is a number nobody has taken, so there is no such
// request. A runtime that tears state down in Stop has to exclude this
// itself.
type UpdateFn func(context.Context, []*ContainerUpdate) ([]*ContainerUpdate, error)

// Adaptation is the NRI abstraction for container runtime NRI adaptation/integration.
type Adaptation struct {
	// Held for writing by startup, shutdown and plugin registration, and
	// for reading by every request, so requests run alongside each other.
	// The order they are applied in is seq's and applies' job.
	sync.RWMutex
	seq         sequencer
	applies     applier
	name        string
	version     string
	nriVersion  string
	dropinPath  string
	pluginPath  string
	socketPath  string
	dontListen  bool
	syncFn      SyncFn
	updateFn    UpdateFn
	clientOpts  []ttrpc.ClientOpts
	serverOpts  []ttrpc.ServerOpt
	listener    net.Listener
	plugins     []*plugin
	validators  []*plugin
	builtin     []*builtin.BuiltinPlugin
	syncLock    sync.RWMutex
	wasmService *api.PluginPlugin
	metrics     Metrics
	deprecation DeprecationRecorder
}

var (
	// Used instead of nil Context in logging.
	noCtx = context.TODO()

	// ErrWasmDisabled is returned for WASM initialization if WASM support is disabled.
	ErrWasmDisabled = errors.New("WASM support is disabled (at build time)")
)

// Option to apply to the NRI runtime.
type Option func(*Adaptation) error

// WithPluginPath returns an option to override the default NRI plugin path.
func WithPluginPath(path string) Option {
	return func(r *Adaptation) error {
		r.pluginPath = path
		return nil
	}
}

// WithPluginConfigPath returns an option to override the default NRI plugin config path.
func WithPluginConfigPath(path string) Option {
	return func(r *Adaptation) error {
		r.dropinPath = path
		return nil
	}
}

// WithSocketPath returns an option to override the default NRI socket path.
func WithSocketPath(path string) Option {
	return func(r *Adaptation) error {
		r.socketPath = path
		return nil
	}
}

// WithDisabledExternalConnections returns an options to disable accepting plugin connections.
func WithDisabledExternalConnections() Option {
	return func(r *Adaptation) error {
		r.dontListen = true
		return nil
	}
}

// WithTTRPCOptions sets extra client and server options to use for ttrpc.
func WithTTRPCOptions(clientOpts []ttrpc.ClientOpts, serverOpts []ttrpc.ServerOpt) Option {
	return func(r *Adaptation) error {
		r.clientOpts = append(r.clientOpts, clientOpts...)
		r.serverOpts = append(r.serverOpts, serverOpts...)
		return nil
	}
}

// WithBuiltinPlugins sets extra builtin plugins to register.
func WithBuiltinPlugins(plugins ...*builtin.BuiltinPlugin) Option {
	return func(r *Adaptation) error {
		r.builtin = append(r.builtin, plugins...)
		return nil
	}
}

// WithMetrics allows consumers to register an implementation of the Metrics interface.
func WithMetrics(m Metrics) Option {
	return func(r *Adaptation) error {
		if m != nil {
			r.metrics = m
		}
		return nil
	}
}

// WithDefaultValidator sets up builtin validator plugin if it is configured.
func WithDefaultValidator(cfg *validator.DefaultValidatorConfig) Option {
	return func(r *Adaptation) error {
		if plugin := validator.GetDefaultValidator(cfg); plugin != nil {
			r.builtin = append([]*builtin.BuiltinPlugin{plugin}, r.builtin...)
		}
		return nil
	}
}

// WithDeprecationRecorder sets up functions to record usage of deprecated
// NRI interfaces or features.
func WithDeprecationRecorder(d DeprecationRecorder) Option {
	return func(r *Adaptation) error {
		r.deprecation = d
		return nil
	}
}

// New creates a new NRI Runtime.
func New(name, version string, syncFn SyncFn, updateFn UpdateFn, opts ...Option) (*Adaptation, error) {
	var err error

	if syncFn == nil {
		return nil, fmt.Errorf("failed to create NRI adaptation, nil SyncFn")
	}
	if updateFn == nil {
		return nil, fmt.Errorf("failed to create NRI adaptation, nil UpdateFn")
	}

	wasmService, err := getWasmService()
	if err != nil {
		log.Errorf(noCtx, "failed to initialize WASM support: %v", err)
		if !errors.Is(err, ErrWasmDisabled) {
			return nil, err
		}
	}

	r := &Adaptation{
		name:        name,
		version:     version,
		nriVersion:  nriver.GetFromBuildInfo(),
		syncFn:      syncFn,
		updateFn:    updateFn,
		pluginPath:  DefaultPluginPath,
		dropinPath:  DefaultPluginConfigPath,
		socketPath:  DefaultSocketPath,
		syncLock:    sync.RWMutex{},
		wasmService: wasmService,
		metrics:     &noopMetrics{},
	}

	for _, o := range opts {
		if err = o(r); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	log.Infof(noCtx, "runtime interface created")

	return r, nil
}

// Start up the NRI runtime.
func (r *Adaptation) Start() error {
	log.Infof(noCtx, "runtime interface starting up...")

	r.Lock()
	defer r.Unlock()

	if err := r.startPlugins(); err != nil {
		return err
	}

	if err := r.startListener(); err != nil {
		return err
	}

	return nil
}

// Stop the NRI runtime.
func (r *Adaptation) Stop() {
	log.Infof(noCtx, "runtime interface shutting down...")

	r.Lock()
	defer r.Unlock()

	// The order ends before what it orders does. An apply still waiting
	// for a turn does not get one: it would change containers the runtime
	// no longer looks after, and an update waiting here is answered with
	// why rather than dropped, because its plugin is waiting for that
	// answer. Closed first, so nothing applies while the plugins are
	// being stopped. The write lock keeps every request out of the order
	// for the duration, but not a placed update the order had already
	// reached: that one drains on its plugin's stream holding no lock of
	// this runtime, so it can still be inside its UpdateFn here.
	r.applies.close()

	r.stopListener()
	r.stopPlugins()
}

// RunPodSandbox relays the corresponding CRI request to plugins.
func (r *Adaptation) RunPodSandbox(ctx context.Context, req *RunPodSandboxRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		if _, err := plugin.runPodSandbox(ctx, req); err != nil {
			return err
		}
	}

	return nil
}

// UpdatePodSandbox relays the corresponding CRI request to plugins.
func (r *Adaptation) UpdatePodSandbox(ctx context.Context, req *UpdatePodSandboxRequest) (*UpdatePodSandboxResponse, error) {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.updatePodSandbox(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	return &UpdatePodSandboxResponse{}, nil
}

// PostUpdatePodSandbox relays the corresponding CRI request to plugins.
func (r *Adaptation) PostUpdatePodSandbox(ctx context.Context, req *PostUpdatePodSandboxRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.postUpdatePodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StopPodSandbox relays the corresponding CRI request to plugins.
func (r *Adaptation) StopPodSandbox(ctx context.Context, req *StopPodSandboxRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.stopPodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// RemovePodSandbox relays the corresponding CRI request to plugins.
func (r *Adaptation) RemovePodSandbox(ctx context.Context, req *RemovePodSandboxRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.removePodSandbox(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// enter gives a request its place in the order and returns a context
// carrying it, for the consumers of that order to read.
func (r *Adaptation) enter(ctx context.Context) (context.Context, seqNo) {
	seq := r.seq.take()

	return withSeq(ctx, seq), seq
}

// releaseOrderLocked hands seq on wherever it did not arrive.
//
// Deferred by every request. A plugin this request was not dispatched to --
// one not subscribed to the event, or one past the plugin that failed it --
// would otherwise leave its writer waiting on a number that never comes,
// and a request that applies nothing would leave the applier waiting. A
// number a consumer has already served is ignored, so this needs to know
// nothing about where the request actually got to.
//
// The caller holds the lock for reading, so the plugin list is stable, and
// nothing here takes it again: a second RLock blocks while a writer is
// waiting for the first one, and the writer waits for a reader that is
// blocked on it.
//
// The plugin list is the whole of it. Every validator is on it as well:
// r.validators is rebuilt from r.plugins whenever either changes, so it
// holds nothing the sweep above has not already announced to.
func (r *Adaptation) releaseOrderLocked(seq seqNo) {
	r.applies.release(seq)

	for _, p := range r.plugins {
		p.impl.releaseOrder(seq)
	}
}

// CreateContainerAndApply relays the request to plugins and calls apply
// with the answer, in the order the requests arrived.
//
// apply is where a runtime puts the answer into practice: the container
// updates and evictions it carries reach containers other than the one this
// request is about, so two requests applying them out of order is exactly
// what the ordering guarantee forbids. The next request's apply does not
// start until this one has returned, so there is one order and the runtime
// is inside it.
//
// A callback rather than a handle to release, because a handle a caller
// forgets does not weaken the order, it stops it: every later request would
// wait for a turn that never comes. A callback cannot be forgotten.
//
// An adjustment needs none of this. It describes the container the request
// is about, which no other request touches.
//
// Two rules apply is held to, because the order runs across it:
//
//   - apply must not call back into Adaptation. A nested request takes a
//     later number and waits for a turn that cannot come until apply
//     returns, and that wait honours no deadline. On one goroutine that is
//     a deadlock which also blocks Stop, since it takes the write lock
//     behind the parked reader. A runtime whose apply evicts or updates
//     containers has to reach its own state directly, not through NRI.
//
//   - UpdateContainers from a plugin on an event stream applies in the
//     order that plugin decided it. A plugin's successive decisions take
//     effect in the order it made them, so a plugin that answers a request
//     and then asks for a further update of the same container has the
//     answer applied first. An update asked for from inside a handler
//     cannot deadlock: it is placed below everything its plugin owes an
//     answer on, so it never waits on the handler that is waiting for it.
//     A request counts as owed from the moment it takes its number, so one
//     the plugin has not been handed yet counts too.
func (r *Adaptation) CreateContainerAndApply(ctx context.Context, req *CreateContainerRequest,
	apply func(*CreateContainerResponse) error) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	var (
		result   = collectCreateContainerResult(req)
		validate *ValidateContainerAdjustmentRequest
	)

	if r.hasValidators() {
		validate = &ValidateContainerAdjustmentRequest{
			Pod:       req.Pod,
			Container: proto.Clone(req.Container).(*Container),
		}
	}

	for _, plugin := range r.plugins {
		if validate != nil {
			validate.AddPlugin(plugin.base, plugin.idx)
		}
		rpl, err := plugin.createContainer(ctx, req)
		if err != nil {
			return err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return err
		}
	}

	// Validators only approve or refuse, so they are not a decision to be
	// ordered and run alongside other requests' validators.
	rpl, err := r.validateContainerAdjustment(ctx, validate, result)
	if err != nil {
		return err
	}

	if apply == nil {
		r.applies.release(seq)

		return nil
	}

	return r.applies.apply(ctx, seq, func() error { return apply(rpl) })
}

// PostCreateContainer relays the corresponding CRI request to plugins.
func (r *Adaptation) PostCreateContainer(ctx context.Context, req *PostCreateContainerRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.postCreateContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StartContainer relays the corresponding CRI request to plugins.
func (r *Adaptation) StartContainer(ctx context.Context, req *StartContainerRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.startContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// PostStartContainer relays the corresponding CRI request to plugins.
func (r *Adaptation) PostStartContainer(ctx context.Context, req *PostStartContainerRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.postStartContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// UpdateContainerAndApply relays the request to plugins and calls apply
// with the answer, in the order the requests arrived. See
// CreateContainerAndApply.
func (r *Adaptation) UpdateContainerAndApply(ctx context.Context, req *UpdateContainerRequest,
	apply func(*UpdateContainerResponse) error) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	result := collectUpdateContainerResult(req)
	for _, plugin := range r.plugins {
		rpl, err := plugin.updateContainer(ctx, req)
		if err != nil {
			return err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return err
		}
	}

	if apply == nil {
		r.applies.release(seq)

		return nil
	}

	return r.applies.apply(ctx, seq, func() error {
		return apply(result.updateContainerResponse())
	})
}

// PostUpdateContainer relays the corresponding CRI request to plugins.
func (r *Adaptation) PostUpdateContainer(ctx context.Context, req *PostUpdateContainerRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.postUpdateContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// StopContainerAndApply relays the request to plugins and calls apply with
// the answer, in the order the requests arrived. See
// CreateContainerAndApply.
func (r *Adaptation) StopContainerAndApply(ctx context.Context, req *StopContainerRequest,
	apply func(*StopContainerResponse) error) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	result := collectStopContainerResult()
	for _, plugin := range r.plugins {
		rpl, err := plugin.stopContainer(ctx, req)
		if err != nil {
			return err
		}
		err = result.apply(rpl, plugin.name())
		if err != nil {
			return err
		}
	}

	if apply == nil {
		r.applies.release(seq)

		return nil
	}

	return r.applies.apply(ctx, seq, func() error {
		return apply(result.stopContainerResponse())
	})
}

// RemoveContainer relays the corresponding CRI request to plugins.
func (r *Adaptation) RemoveContainer(ctx context.Context, req *RemoveContainerRequest) error {
	defer r.removeClosed()

	r.RLock()
	defer r.RUnlock()

	ctx, seq := r.enter(ctx)
	defer r.releaseOrderLocked(seq)

	for _, plugin := range r.plugins {
		_, err := plugin.removeContainer(ctx, req)
		if err != nil {
			return err
		}
	}

	return nil
}

// Perform a set of unsolicited container updates requested by a plugin, in
// the place in the order its plugin decided them.
//
// A plugin on an event stream says everything it has to say on one ordered
// channel, so its decisions are in an order the runtime can read rather
// than infer, and this is where that order is put into practice. A plugin
// on unary calls gets no place and applies as it arrives: an out-of-band
// call carries no position, and there is nothing to read.
func (r *Adaptation) updateContainers(ctx context.Context, req []*ContainerUpdate) ([]*ContainerUpdate, error) {
	// The place carries the update it was given to, so a plugin that has
	// one is answered from there rather than from req.
	if placed, ok := placedUpdateFrom(ctx); ok {
		return placed.apply(ctx)
	}

	return r.updateFn(ctx, req)
}

// placedUpdate is a plugin's unsolicited update with its place in the order
// taken.
//
// Taking the place and applying there are two steps because they happen in
// two places: the stream reader takes the place, since the set of numbers
// the place is read from is the reader's own to move, and the apply waits
// for its turn, which the reader must not do.
type placedUpdate struct {
	r   *Adaptation
	req []*ContainerUpdate

	// at is the number the apply is attached below, and held is the
	// attachment. The update takes no number of its own either way, so it
	// announces nothing to anyone.
	at   seqNo
	held *attachment

	// failed is what the runtime could not apply, written by the apply and
	// read once it has run.
	failed []*ContainerUpdate
}

// placeUpdate gives req the place its plugin's decisions put it in: below
// the lowest number its plugin owes an answer on, and below the number
// nobody has taken yet when it owes none.
//
// Called by the stream reader, and waits for nothing.
func (r *Adaptation) placeUpdate(ctx context.Context, req []*ContainerUpdate, at seqNo, owed bool) *placedUpdate {
	placed := &placedUpdate{r: r, req: req, at: at}

	// A number its plugin owes an answer on cannot have been served,
	// because that number's apply needs the answer this plugin has not
	// sent.
	if owed {
		placed.held = r.applies.placeAt(at, placed.update(ctx))

		return placed
	}

	// The number nobody has taken yet is after every request already
	// numbered -- so after every decision of this plugin that is in the
	// order -- and before every request numbered later, because the
	// request that takes it runs what is attached below it first.
	//
	// Attached with the sequencer held, so that no request can take that
	// number in between and reach its apply before this is attached.
	r.seq.placeNext(func(next seqNo) {
		placed.at = next
		placed.held = r.applies.placeAt(next, placed.update(ctx))
	})

	return placed
}

// update is the apply itself, which is the runtime's own update function.
func (u *placedUpdate) update(ctx context.Context) func() error {
	return func() error {
		var err error

		u.failed, err = u.r.updateFn(ctx, u.req)

		return err
	}
}

// apply puts the update into practice in the place it was given, and
// returns what the runtime could not apply.
func (u *placedUpdate) apply(ctx context.Context) ([]*ContainerUpdate, error) {
	err := u.r.applies.awaitAt(ctx, u.at, u.held)

	return u.failed, err
}

// Validate requested container adjustments.
func (r *Adaptation) validateContainerAdjustment(ctx context.Context, req *ValidateContainerAdjustmentRequest, result *result) (*CreateContainerResponse, error) {
	// Not part of the request's turn: a validator is asked about the
	// request it was just dispatched, and one number cannot order a plugin
	// against itself.
	ctx = withoutSeq(ctx)

	rpl := result.createContainerResponse()

	if req == nil || len(r.validators) == 0 {
		return rpl, nil
	}

	req.AddResponse(rpl)
	req.AddOwners(result.owners)

	errors := make(chan error, len(r.validators))
	wg := sync.WaitGroup{}

	for _, p := range r.validators {
		wg.Add(1)
		go func(p *plugin) {
			defer wg.Done()
			errors <- p.ValidateContainerAdjustment(ctx, req)
		}(p)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		if err != nil {
			return nil, err
		}
	}

	return rpl, nil
}

// Start up pre-installed plugins.
func (r *Adaptation) startPlugins() (retErr error) {
	var plugins []*plugin

	log.Infof(noCtx, "starting plugins...")

	ids, names, configs, err := r.discoverPlugins()
	if err != nil {
		return err
	}

	defer func() {
		if retErr != nil {
			for _, p := range plugins {
				p.stop()
			}
		}
	}()

	for _, b := range r.builtin {
		log.Infof(noCtx, "starting builtin NRI plugin %q...", b.Index+"-"+b.Base)
		p, err := r.newBuiltinPlugin(b)
		if err != nil {
			return fmt.Errorf("failed to initialize builtin NRI plugin %q: %v", b.Base, err)
		}

		if err := p.start(r.name, r.version); err != nil {
			return fmt.Errorf("failed to start builtin NRI plugin %q: %v", b.Base, err)
		}

		plugins = append(plugins, p)
	}

	for i, name := range names {
		log.Infof(noCtx, "starting pre-installed NRI plugin %q...", name)

		id := ids[i]
		p, err := r.newLaunchedPlugin(r.pluginPath, id, name, configs[i])
		if err != nil {
			log.Warnf(noCtx, "failed to initialize pre-installed NRI plugin %q: %v", name, err)
			continue
		}

		if err := p.start(r.name, r.version); err != nil {
			log.Warnf(noCtx, "failed to start pre-installed NRI plugin %q: %v", name, err)
			continue
		}

		plugins = append(plugins, p)
	}

	// Although the error returned by syncPlugins may not be nil, r.syncFn could still ignores this error and returns a nil error.
	// We need to make sure that the plugins are successfully synchronized in the `plugins`
	syncPlugins := func(ctx context.Context, pods []*PodSandbox, containers []*Container) (updates []*ContainerUpdate, err error) {
		startedPlugins := plugins
		plugins = make([]*plugin, 0, len(plugins))
		for _, plugin := range startedPlugins {
			us, err := plugin.synchronize(ctx, pods, containers)
			if err != nil {
				plugin.stop()
				log.Warnf(noCtx, "failed to synchronize pre-installed NRI plugin %q: %v", plugin.name(), err)
				continue
			}

			plugins = append(plugins, plugin)
			updates = append(updates, us...)
			log.Infof(noCtx, "pre-installed NRI plugin %q synchronization success", plugin.name())
		}
		return updates, nil
	}
	if err := r.syncFn(noCtx, syncPlugins); err != nil {
		return fmt.Errorf("failed to synchronize pre-installed NRI Plugins: %w", err)
	}

	r.addPlugins(plugins...)

	return nil
}

// Stop plugins.
func (r *Adaptation) stopPlugins() {
	log.Infof(noCtx, "stopping plugins...")

	for _, p := range r.plugins {
		p.stop()
	}
	r.plugins = nil
}

// removeClosed drops plugins that closed while a request was running.
//
// The write lock is taken only when there is something to drop: taking it
// on every request would wait out every request running alongside this
// one, which is the serialisation one order per request replaces.
func (r *Adaptation) removeClosed() {
	// A read lock of its own: every request registers this defer ahead of
	// the one that releases the lock it held, so this runs after that
	// release rather than under it.
	r.RLock()

	closed := false

	for _, p := range r.plugins {
		if p.isClosed() {
			closed = true

			break
		}
	}

	r.RUnlock()

	if !closed {
		return
	}

	r.Lock()
	defer r.Unlock()

	r.removeClosedPlugins()
}

// addPlugins lists plugins as live, arming the stream of each as it does.
//
// Arming belongs here because this is where a plugin becomes reachable. A
// request numbered between the arming and the listing reaches neither the
// plugin, which is not listed yet, nor the sweep that would announce its
// hole, so the two have to happen under the one lock. Every plugin is
// listed here: the pre-installed ones through startPlugins and the rest as
// they connect.
//
// The caller holds r.Lock().
func (r *Adaptation) addPlugins(plugins ...*plugin) {
	first := r.seq.upcoming()
	for _, p := range plugins {
		p.impl.armStream(first)
	}

	r.plugins = append(r.plugins, plugins...)
	r.sortPlugins()
}

func (r *Adaptation) removeClosedPlugins() {
	var active, closed, validators []*plugin
	for _, p := range r.plugins {
		if p.isClosed() {
			closed = append(closed, p)
		} else {
			active = append(active, p)
			if p.isContainerAdjustmentValidator() {
				validators = append(validators, p)
			}
		}
	}

	if len(closed) != 0 {
		go func() {
			for _, plugin := range closed {
				plugin.stop()
			}
		}()
	}

	r.plugins = active
	r.validators = validators
	r.metrics.UpdatePluginCount(len(r.plugins))
}

func (r *Adaptation) startListener() error {
	if r.dontListen {
		log.Infof(noCtx, "connection from external plugins disabled")
		return nil
	}

	os.Remove(r.socketPath)
	if err := os.MkdirAll(filepath.Dir(r.socketPath), 0700); err != nil {
		return fmt.Errorf("failed to create socket %q: %w", r.socketPath, err)
	}

	l, err := net.ListenUnix("unix", &net.UnixAddr{
		Name: r.socketPath,
		Net:  "unix",
	})
	if err != nil {
		return fmt.Errorf("failed to create socket %q: %w", r.socketPath, err)
	}

	r.acceptPluginConnections(l)

	return nil
}

func (r *Adaptation) stopListener() {
	if r.listener != nil {
		r.listener.Close()
	}
}

func (r *Adaptation) acceptPluginConnections(l net.Listener) error {
	r.listener = l

	ctx := context.Background()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				log.Infof(ctx, "stopped accepting plugin connections (%v)", err)
				return
			}

			p, err := r.newExternalPlugin(conn)
			if err != nil {
				log.Errorf(ctx, "failed to create external plugin: %v", err)
				continue
			}

			if err := p.start(r.name, r.version); err != nil {
				log.Errorf(ctx, "failed to start external plugin: %v", err)
				continue
			}

			r.requestPluginSync()
			err = r.syncFn(ctx, p.synchronize)
			if err != nil {
				log.Errorf(ctx, "failed to synchronize plugin: %v", err)
				p.close()
			} else {
				r.Lock()
				if p.isContainerAdjustmentValidator() {
					r.validators = append(r.validators, p)
				}
				r.addPlugins(p)
				count := len(r.plugins)
				r.Unlock()
				r.metrics.UpdatePluginCount(count)
				log.Infof(ctx, "plugin %q connected and synchronized", p.name())
			}
			r.finishedPluginSync()
		}
	}()

	return nil
}

func (r *Adaptation) discoverPlugins() ([]string, []string, []string, error) {
	var (
		plugins []string
		indices []string
		configs []string
		entries []os.DirEntry
		info    fs.FileInfo
		err     error
	)

	if entries, err = os.ReadDir(r.pluginPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("failed to discover plugins in %s: %w",
			r.pluginPath, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err = e.Info(); err != nil {
			continue
		}
		if info.Mode()&fs.FileMode(0o111) == 0 {
			continue
		}

		name := e.Name()
		idx, base, err := api.ParsePluginName(name)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to discover plugins in %s: %w",
				r.pluginPath, err)
		}

		cfg, err := r.getPluginConfig(idx, base)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to discover plugins in %s: %w",
				r.pluginPath, err)
		}

		log.Infof(noCtx, "discovered plugin %s", name)

		indices = append(indices, idx)
		plugins = append(plugins, base)
		configs = append(configs, cfg)
	}

	return indices, plugins, configs, nil
}

func (r *Adaptation) sortPlugins() {
	r.removeClosedPlugins()
	sort.Slice(r.plugins, func(i, j int) bool {
		return r.plugins[i].idx < r.plugins[j].idx
	})
	sort.Slice(r.validators, func(i, j int) bool {
		return r.validators[i].idx < r.validators[j].idx
	})
	if len(r.plugins) > 0 {
		log.Infof(noCtx, "plugin invocation order")
		for i, p := range r.plugins {
			log.Infof(noCtx, "  #%d: %q (%s)", i+1, p.name(), p.qualifiedName())
		}
	}
	if len(r.validators) > 0 {
		log.Infof(noCtx, "validator plugins")
		for _, p := range r.validators {
			log.Infof(noCtx, "  %q (%s)", p.name(), p.qualifiedName())
		}
	}
}

func (r *Adaptation) hasValidators() bool {
	return len(r.validators) > 0
}

func (r *Adaptation) requestPluginSync() {
	r.syncLock.Lock()
}

func (r *Adaptation) finishedPluginSync() {
	r.syncLock.Unlock()
}

// PluginSyncBlock is a handle for blocking plugin synchronization.
type PluginSyncBlock struct {
	r *Adaptation
}

// BlockPluginSync blocks plugins from being synchronized/fully registered.
func (r *Adaptation) BlockPluginSync() *PluginSyncBlock {
	r.syncLock.RLock()
	return &PluginSyncBlock{r: r}
}

// Unblock a plugin sync. block put in place by BlockPluginSync. Safe to call
// multiple times but only from a single goroutine.
func (b *PluginSyncBlock) Unblock() {
	if b != nil && b.r != nil {
		b.r.syncLock.RUnlock()
		b.r = nil
	}
}
