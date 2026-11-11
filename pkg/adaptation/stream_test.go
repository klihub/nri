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

// handshakeTimeout is how long a test waits for a plugin to get through
// the handshake.
const handshakeTimeout = 10 * time.Second

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

	// The runtime says it took the stream before the plugin registers, so
	// waiting for that is waiting for the transport to be settled.
	msg, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, msg.GetStreamReady(), "the runtime did not accept the event stream")

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

	require.NoError(t, plugin.stream.Send(&api.PluginMessage{
		Seq: 1,
		Msg: &api.PluginMessage_RegisterPluginRequest{
			RegisterPluginRequest: plugin.registration(),
		},
	}))

	done := make(chan error, 1)

	go func() { done <- plugin.handshake() }()

	select {
	case err := <-done:
		require.NoError(t, err, "the plugin did not get through the handshake")
	case <-time.After(handshakeTimeout):
		t.Fatal("a plugin that registered on its stream was never synchronized")
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
