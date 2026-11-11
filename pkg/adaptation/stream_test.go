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

import (
	"context"
	"errors"
	"fmt"
	stdnet "net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
)

// streamReadyWait is how long a plugin waits for the runtime to accept
// its event stream before deciding it will not.
//
// It mirrors pkg/stub's streamReadyTimeout, which is not exported. Raising
// that one without raising this one leaves handshakeTimeout below what a
// handshake is allowed to take.
const streamReadyWait = 2 * time.Second

// handshakeTimeout is how long a test waits for a plugin to get through
// the handshake: every wait the handshake is allowed to make, and as much
// again as slack.
//
// Derived rather than chosen, because a deadline close to the sum of the
// timeouts under it fails a handshake that is merely slow -- a machine
// running several of these tests at once is enough -- and reads as a
// number nothing explains. The waits are the plugin's for its event
// stream, its registration timeout, and the request timeout of the
// Configure and Synchronize that follow it.
const handshakeTimeout = 2 * (streamReadyWait +
	api.DefaultPluginRegistrationTimeout +
	2*api.DefaultPluginRequestTimeout)

// TestEventStreamNegotiation pins which transport a plugin ends up on.
//
// Without it the rest of the streaming measurements prove nothing: a run
// that quietly fell back to unary calls looks exactly like one that
// streamed, and the numbers would be the same either way for the wrong
// reason.
func TestEventStreamNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want bool
	}{
		{
			name: "streams when nothing says otherwise",
			env:  "",
			want: true,
		},
		{
			name: "uses unary calls when the plugin is told not to stream",
			env:  "0",
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NRI_STREAMING", tc.env)

			rig := startRig(t, 1, fixedDelay(0))

			require.Equal(t, tc.want, rig.plugins[0].stub.Streaming(),
				"plugin's view of whether it is streaming")
		})
	}
}

// rawPlugin is a plugin that speaks to the runtime itself, with no stub in
// between, so a test can choose which transport it registers over.
type rawPlugin struct {
	conn   stdnet.Conn
	mux    multiplex.Mux
	client *ttrpc.Client
	stream api.StreamingRuntime_StartEventStreamClient
	cancel context.CancelFunc
}

// connectRawPlugin returns a plugin connected to the runtime in dir, with
// an event stream the runtime has accepted.
func connectRawPlugin(t *testing.T, dir string) *rawPlugin {
	t.Helper()

	p := dialRawPlugin(t, dir)

	// The runtime says it took the stream before the plugin registers, so
	// waiting for that is waiting for the transport to be settled.
	msg, err := p.stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, msg.GetStreamReady(), "the runtime did not accept the event stream")

	return p
}

// dialRawPlugin returns a plugin connected to the runtime in dir, with an
// event stream it has offered and the runtime has yet to answer.
func dialRawPlugin(t *testing.T, dir string) *rawPlugin {
	t.Helper()

	conn, err := stdnet.Dial("unix", filepath.Join(dir, "nri.sock"))
	require.NoError(t, err)

	mux := multiplex.Multiplex(conn)

	rconn, err := mux.Open(multiplex.RuntimeServiceConn)
	require.NoError(t, err)

	client := ttrpc.NewClient(rconn)

	ctx, cancel := context.WithCancel(context.Background())

	stream, err := api.NewStreamingRuntimeClient(client).StartEventStream(ctx)
	require.NoError(t, err)

	p := &rawPlugin{
		conn:   conn,
		mux:    mux,
		client: client,
		stream: stream,
		cancel: cancel,
	}
	t.Cleanup(p.close)

	return p
}

// close drops this plugin's connection to the runtime.
func (p *rawPlugin) close() {
	p.cancel()
	p.client.Close()
	p.mux.Close()
	p.conn.Close()
}

// registration returns what this plugin registers as.
func (p *rawPlugin) registration() *api.RegisterPluginRequest {
	return &api.RegisterPluginRequest{
		PluginName: "raw",
		PluginIdx:  "10",
		NRIVersion: api.DevelVersion,
	}
}

// register puts this plugin's registration on the event stream, numbered
// seq.
func (p *rawPlugin) register(t *testing.T, seq uint64) {
	t.Helper()

	require.NoError(t, p.stream.Send(&api.PluginMessage{
		Seq: seq,
		Msg: &api.PluginMessage_RegisterPluginRequest{
			RegisterPluginRequest: p.registration(),
		},
	}))
}

// await fails the test with what if fn reports an error or does not
// finish in time.
func (p *rawPlugin) await(t *testing.T, what string, fn func() error) {
	t.Helper()

	done := make(chan error, 1)

	go func() { done <- fn() }()

	select {
	case err := <-done:
		require.NoError(t, err, what)
	case <-time.After(handshakeTimeout):
		t.Fatal(what)
	}
}

// handshake answers the runtime on the event stream until this plugin has
// been synchronized, and reports what went wrong if it is not.
func (p *rawPlugin) handshake() error {
	var registered bool

	for {
		msg, err := p.stream.Recv()
		if err != nil {
			return fmt.Errorf("event stream ended: %w", err)
		}

		if e := msg.GetError(); e != "" {
			return fmt.Errorf("the runtime refused a request: %s", e)
		}

		switch {
		case msg.GetRegisterPluginResponse() != nil:
			registered = true

		case msg.GetConfigureRequest() != nil:
			err = p.reply(msg, &api.PluginMessage{
				Msg: &api.PluginMessage_ConfigureResponse{
					ConfigureResponse: &api.ConfigureResponse{},
				},
			})
			if err != nil {
				return err
			}

		case msg.GetSynchronizeRequest() != nil:
			err = p.reply(msg, &api.PluginMessage{
				Msg: &api.PluginMessage_SynchronizeResponse{
					SynchronizeResponse: &api.SynchronizeResponse{},
				},
			})
			if err != nil {
				return err
			}

			if !registered {
				return errors.New("the runtime answered no registration on the stream")
			}

			return nil

		default:
			return fmt.Errorf("unexpected %T on the event stream", msg.GetMsg())
		}
	}
}

// servePods answers the runtime's pod requests on the event stream until
// the runtime has refused the given number of this plugin's requests and
// has driven one pod request through, and reports what went wrong if it
// does not.
func (p *rawPlugin) servePods(refusals int) error {
	var refused, served int

	for refused < refusals || served == 0 {
		msg, err := p.stream.Recv()
		if err != nil {
			return fmt.Errorf("event stream ended: %w", err)
		}

		switch {
		case msg.GetError() != "":
			refused++

		case msg.GetRunPodSandboxRequest() != nil:
			err = p.reply(msg, &api.PluginMessage{
				Msg: &api.PluginMessage_RunPodSandboxResponse{
					RunPodSandboxResponse: &api.RunPodSandboxResponse{},
				},
			})
			if err != nil {
				return err
			}

			served++

		default:
			return fmt.Errorf("unexpected %T on the event stream", msg.GetMsg())
		}
	}

	return nil
}

// registrationTaken reports whether the runtime took the registration
// this plugin put on its event stream.
//
// A stream that ends before any answer arrives reports the registration
// not taken: a refusal on the other transport takes the plugin down with
// it, which is an answer by other means.
func (p *rawPlugin) registrationTaken() bool {
	for {
		msg, err := p.stream.Recv()
		if err != nil {
			return false
		}

		switch {
		case msg.GetRegisterPluginResponse() != nil:
			return true

		case msg.GetMsg() == nil && msg.GetError() != "":
			return false
		}
	}
}

// registerUnary registers this plugin with a unary call, with no deadline
// of its own, and reports what the runtime answered.
func (p *rawPlugin) registerUnary() error {
	_, err := api.NewRuntimeClient(p.client).RegisterPlugin(context.Background(),
		p.registration())

	return err
}

// reply answers one of the runtime's requests, echoing its sequence number.
func (p *rawPlugin) reply(req *api.RuntimeMessage, rpl *api.PluginMessage) error {
	rpl.Seq = req.GetSeq()

	return p.stream.Send(rpl)
}

func TestAStreamingPluginRegistersOnItsStream(t *testing.T) {
	// The whole handshake over one transport. The plugin reaching
	// Synchronize is the proof: registration is what unblocks it.
	rig := startRig(t, 0, fixedDelay(0))
	plugin := connectRawPlugin(t, rig.dir)

	plugin.register(t, 1)
	plugin.await(t, "a plugin that registered on its stream was never synchronized",
		plugin.handshake)
}

func TestFurtherRegistrationsOnAStreamAreRefused(t *testing.T) {
	// A plugin may be as stupid as it likes and still must not be able to
	// stop the runtime. A registration is reported on a channel with one
	// slot, so a second registration that reached it would fill the slot
	// and a third would block the reader that answers everything else
	// this plugin asks for.
	//
	// The refusals are half the claim; the other half is that the runtime
	// still drives the plugin afterwards.
	const refusals = 3

	rig := startRig(t, 0, fixedDelay(0))
	plugin := connectRawPlugin(t, rig.dir)

	plugin.register(t, 1)
	plugin.await(t, "the plugin did not get through the handshake", plugin.handshake)

	// The pod request below has to reach this plugin for the second half of
	// the claim to say anything, and a request numbered before the plugin
	// is eligible for events reaches nobody at all.
	rig.waitEligible()

	for seq := uint64(2); seq < 2+refusals; seq++ {
		plugin.register(t, seq)
	}

	pods := make(chan error, 1)

	go func() {
		pods <- rig.runtime.RunPodSandbox(context.Background(),
			&api.RunPodSandboxRequest{Pod: pod()})
	}()

	// That an error came back, and not what it says: the wording of a
	// refusal is nothing the plugin is promised. Both halves are named,
	// because servePods waits for both and a message naming one of them
	// reads as a claim about which half failed.
	plugin.await(t,
		"the runtime did not refuse a plugin that registered again and ask it for a pod",
		func() error { return plugin.servePods(refusals) })

	select {
	case err := <-pods:
		require.NoError(t, err, "a request to a plugin that registered again")
	case <-time.After(handshakeTimeout):
		t.Fatal("a plugin that registered again stopped the runtime driving it")
	}
}

func TestAUnaryRegistrationIsRefusedOnceAStreamExists(t *testing.T) {
	// A plugin's traffic is all of it on a stream or all of it on unary
	// calls. The two ends can still disagree about which this plugin got:
	// it gives up waiting for the runtime to accept the stream after the
	// runtime has started using it, and registers the only way it believes
	// it can. The refusal is the runtime's to make.
	rig := startRig(t, 0, fixedDelay(0))
	plugin := connectRawPlugin(t, rig.dir)

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()

	_, err := api.NewRuntimeClient(plugin.client).RegisterPlugin(ctx, plugin.registration())

	// Not what the error says: a refusal that reports a status races the
	// teardown that the refusal itself starts, so what reaches the plugin
	// is as often a closed connection as the status.
	require.Error(t, err,
		"a plugin holding an event stream registered with a unary call")
}

func TestFurtherUnaryRegistrationsAreRefused(t *testing.T) {
	// The registration channel has one slot and one reader, and that
	// reader takes its value when the plugin registers. A second
	// registration that reported itself on that channel would never
	// return, so what this test watches for is a call that comes back at
	// all.
	rig := startRig(t, 0, fixedDelay(0))
	plugin := connectRawPlugin(t, rig.dir)

	plugin.register(t, 1)
	plugin.await(t, "the plugin did not get through the handshake", plugin.handshake)

	for i := 0; i < 2; i++ {
		errC := make(chan error, 1)

		go func() { errC <- plugin.registerUnary() }()

		select {
		case err := <-errC:
			require.Error(t, err, "a registered plugin registered again")
		case <-time.After(handshakeTimeout):
			t.Fatalf("unary registration %d never came back", i+1)
		}
	}
}

func TestRegistrationsOnBothTransportsTakeOneRegistration(t *testing.T) {
	// A plugin registering on both transports at once offers two
	// registrations to one plugin, each on its own goroutine inside the
	// runtime. One of them is taken, the other is refused, and both come
	// back.
	rig := startRig(t, 0, fixedDelay(0))
	plugin := connectRawPlugin(t, rig.dir)

	unary := make(chan error, 1)

	go func() { unary <- plugin.registerUnary() }()

	plugin.register(t, 1)

	taken := make(chan bool, 1)

	go func() { taken <- plugin.registrationTaken() }()

	var onStream bool

	select {
	case onStream = <-taken:
	case <-time.After(handshakeTimeout):
		t.Fatal("the registration on the event stream was never answered")
	}

	select {
	case err := <-unary:
		// Both taken is the failure. Which one was taken is the race, and
		// the runtime answers the other with an error either way.
		require.False(t, onStream && err == nil,
			"the plugin registered twice, once on each transport")
	case <-time.After(handshakeTimeout):
		t.Fatal("the unary registration never came back")
	}
}

func TestPluginObservesRequestsInDispatchOrder(t *testing.T) {
	// The regression the sequence exists to prevent. Two numbers -- one for
	// the applies, one minted by the writer -- could disagree about the same
	// pair of requests, so a plugin could be shown them in one order while
	// their answers were applied in another. One number cannot disagree with
	// itself.
	//
	// The delays fall as the requests arrive, so the first request is the
	// slowest: an implementation that lets a later request reach the wire
	// first is caught rather than raced with.
	const (
		count = 16
		step  = 5 * time.Millisecond
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

	require.True(t, plugin.stub.Streaming(),
		"this test is about the stream; the plugin is not on one")

	var (
		mu      sync.Mutex
		applied []string
		wg      sync.WaitGroup
		release = make(chan struct{})
	)

	want := make([]string, 0, count)

	for i := 0; i < count; i++ {
		ctr := container(i)
		want = append(want, ctr.Id)

		wg.Add(1)

		go func() {
			defer wg.Done()

			<-release

			err := rig.runtime.CreateContainerAndApply(context.Background(),
				&api.CreateContainerRequest{Pod: pod(), Container: ctr},
				func(*api.CreateContainerResponse) error {
					mu.Lock()
					applied = append(applied, ctr.Id)
					mu.Unlock()

					return nil
				})
			require.NoError(t, err)
		}()
	}

	for i := 0; i < count; i++ {
		release <- struct{}{}
	}

	wg.Wait()

	// Which request is first is not the claim and is not observable from
	// out here: a request takes its number inside the call, so releasing a
	// goroutine does not decide when it gets there. The claim is that the
	// two consumers of that number agree -- the order the plugin was shown
	// requests in is the order their answers were applied in -- which is
	// exactly what two separate numberings could not promise.
	seen := plugin.Seen()

	require.ElementsMatch(t, want, seen, "the plugin did not see every request")
	require.Equal(t, seen, applied,
		"what the plugin took and what was applied are two different orders")
}

func TestAnUnsubscribedPluginGetsAHole(t *testing.T) {
	// Review focus 1. The test plugin subscribes to CreateContainer and
	// StopContainer only, so a RunPodSandbox takes a number its writer will
	// never see. Unannounced, that number is one the writer waits on for
	// good and the plugin stops seeing traffic entirely.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	require.NoError(t, rig.runtime.RunPodSandbox(context.Background(),
		&api.RunPodSandboxRequest{Pod: pod()}))

	done := make(chan error, 1)

	go func() {
		done <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("an event the plugin is not subscribed to stalled its stream")
	}

	require.Equal(t, []string{container(0).Id}, rig.plugins[0].Seen())
}

func TestAFailedRequestHolesTheRemainingPlugins(t *testing.T) {
	// Review focus 2. Plugin 0 refuses, so plugin 1 is never dispatched to
	// and its writer never sees that number.
	rig := startRig(t, 2, func(*api.Container) time.Duration { return 0 })

	rig.plugins[0].failCreate.Store(true)

	require.Error(t, rig.runtime.CreateContainerAndApply(context.Background(),
		&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
		func(*api.CreateContainerResponse) error { return nil }))

	rig.plugins[0].failCreate.Store(false)

	done := make(chan error, 1)

	go func() {
		done <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(1)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("a request that failed at one plugin stalled the next plugin's stream")
	}

	require.Equal(t, []string{container(1).Id}, rig.plugins[1].Seen(),
		"the second plugin should have seen only the request that succeeded")
}

func TestAPluginDyingReleasesItsWriter(t *testing.T) {
	// Review focus 4. A plugin that goes away with requests waiting for a
	// turn on its stream must not strand them.
	rig := startRig(t, 1, func(*api.Container) time.Duration { return 0 })

	require.NoError(t, rig.runtime.CreateContainerAndApply(context.Background(),
		&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
		func(*api.CreateContainerResponse) error { return nil }))

	rig.plugins[0].stop()

	done := make(chan struct{})

	go func() {
		// Whether this succeeds depends on whether the plugin has been
		// reaped yet, which is a race this test is not about. That it
		// returns at all is the claim.
		_ = rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(1)},
			func(*api.CreateContainerResponse) error { return nil })
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("a request was stranded by a plugin that went away")
	}
}

func TestAPluginDyingMidRequestDoesNotFailIt(t *testing.T) {
	// A plugin that dies owing an answer costs the same on either transport:
	// it is closed and the request carries on without it. On a unary call
	// ttrpc.ErrClosed is fatal and the relay reports no error, so a stream
	// that went away has to be fatal in the same way or the same plugin
	// crash fails a CreateContainer that unary mode survives.
	//
	// The delay stays under the plugin request timeout, so what this
	// exercises is the stream dying rather than the deadline expiring --
	// context.DeadlineExceeded is already fatal and would pass either way.
	rig := startRig(t, 1, func(*api.Container) time.Duration {
		return 1500 * time.Millisecond
	})

	errC := make(chan error, 1)

	go func() {
		errC <- rig.runtime.CreateContainerAndApply(context.Background(),
			&api.CreateContainerRequest{Pod: pod(), Container: container(0)},
			func(*api.CreateContainerResponse) error { return nil })
	}()

	// The plugin records the container before it sleeps, so this says the
	// request is in flight rather than merely dispatched.
	require.Eventually(t, func() bool {
		return len(rig.plugins[0].Seen()) == 1
	}, 10*time.Second, 10*time.Millisecond, "the plugin was never asked")

	// In a goroutine: stopping the stub waits for its server to finish, and
	// this test is about the caller, not about who returns first.
	go rig.plugins[0].stop()

	select {
	case err := <-errC:
		require.NoError(t, err,
			"a plugin dying mid-request should not fail the request")
	case <-time.After(30 * time.Second):
		t.Fatal("a request outlived the plugin that was answering it")
	}
}

func TestAPluginAnswersOneRequestAtATime(t *testing.T) {
	// No option asks for ordered processing. Overlap is counted from inside
	// the handler, so what this measures is whether two handlers ran at once
	// rather than what order they finished in.
	const count = 16

	rig := startRig(t, 1, fixedDelay(5*time.Millisecond))
	plugin := rig.plugins[0]

	require.True(t, plugin.stub.Streaming(),
		"this test is about the stream; the plugin is not on one")

	var (
		mu      sync.Mutex
		applied []string
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
					applied = append(applied, ctr.Id)
					mu.Unlock()

					return nil
				})
			require.NoError(t, err)
		}()
	}

	wg.Wait()

	require.Zero(t, plugin.Overlap(), "two handlers ran at once")
	require.Equal(t, plugin.Seen(), applied,
		"what the plugin took and what was applied are two different orders")
}

func TestAPluginAnswersAValidationAheadOfQueuedRequests(t *testing.T) {
	// A validator is asked about the request it was just dispatched, so the
	// runtime sends that question with no number and the plugin answers it
	// without waiting for a turn. Answered in turn, it would queue behind
	// the handler of a request numbered after the one it is about, and the
	// request waiting for it -- the lower number, whose apply the order
	// cannot pass -- would wait out the plugin request timeout and fail.
	//
	// The first plugin is subscribed to CreateContainer and to validation,
	// which nothing forbids. The second plugin is what fixes the order of
	// the three messages the first one sees: it holds request A's create
	// open until the first plugin is inside request B's handler, so A's
	// validation is written behind B's create.
	rig := startRig(t, 2, fixedDelay(0), validatesAdjustments(0))

	validator, slow := rig.plugins[0], rig.plugins[1]

	require.True(t, validator.stub.Streaming(),
		"this test is about the stream; the plugin is not on one")

	// B's handler is held until A has finished, so a validation queued
	// behind it is a validation A waits out its timeout for.
	heldA := slow.holdCreate(container(0).Id)
	heldB := validator.holdCreate(container(1).Id)

	create := func(ctr *api.Container) chan error {
		errC := make(chan error, 1)

		go func() {
			errC <- rig.runtime.CreateContainerAndApply(context.Background(),
				&api.CreateContainerRequest{Pod: pod(), Container: ctr},
				func(*api.CreateContainerResponse) error { return nil })
		}()

		return errC
	}

	errA := create(container(0))

	// A reaches both plugins before B is issued, so A is the lower number
	// of the two and B's create is written behind A's.
	waitForObserved(t, validator, 1)
	waitForObserved(t, slow, 1)

	errB := create(container(1))

	waitForObserved(t, validator, 2)

	// A's create is answered by every plugin, which is what makes the
	// runtime ask the validator about it.
	close(heldA)

	select {
	case err := <-errA:
		require.NoError(t, err, "the request a validation was about failed")
	case <-time.After(30 * time.Second):
		t.Fatal("the request a validation was about never finished")
	}

	require.Equal(t, []string{container(0).Id}, validator.Validated(),
		"the plugin was not asked to validate the adjustment of the request")

	close(heldB)

	select {
	case err := <-errB:
		require.NoError(t, err, "the request whose handler was held failed")
	case <-time.After(30 * time.Second):
		t.Fatal("the request whose handler was held never finished")
	}
}
