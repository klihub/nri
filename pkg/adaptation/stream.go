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

// The runtime's end of a plugin's event stream.
//
// One writer goroutine and one reader goroutine per plugin. The writer is
// the point of the whole thing: it takes the next sequence number and
// writes the message in the same step, so nothing can come between them and
// the order of the sequence numbers is the order of the bytes on the wire.
// A unary call per request cannot promise that at any price -- the send is
// welded to waiting for the reply, so a caller cannot hold a plugin's turn
// until its bytes are out.
//
// The reader matches each reply to its request by that sequence number,
// which is the job a unary call was doing for us invisibly and the first
// thing we have to do ourselves here.
//
// A plugin reaches this only if it opened a stream. One that did not --
// because it was built against an older NRI, or was told not to -- is
// talked to exactly as before, over unary calls. See pluginType, where a
// stream is simply one more implementation and its absence selects the old
// one.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/log"
)

// errStreamClosed is what a request waiting on a stream that went away gets.
//
// Every way a stream can end wraps it, so that isFatalError recognises a
// dead stream the way it recognises a dead ttrpc connection: the plugin is
// closed and the request carries on without it. A plugin crash costs the
// same either way, rather than failing the request on a stream and being
// survivable on a unary call.
var errStreamClosed = errors.New("plugin event stream closed")

// streamCall is one request waiting for its reply.
type streamCall struct {
	msg   *api.RuntimeMessage
	reply chan *api.PluginMessage

	// What the writer numbered this call, and whether its caller is still
	// waiting. Both are read and written under pluginStream.mu, which is
	// what lets a caller that gives up before the writer gets there stop
	// the entry from being made at all.
	id        uint64
	abandoned bool
}

// pluginStream is one plugin's event stream.
type pluginStream struct {
	p     *plugin
	srv   api.StreamingRuntime_StartEventStreamServer
	sends chan *streamCall

	// responses carries the replies to the requests the plugin originates.
	// They take no number and no pending entry: the number is the
	// plugin's, echoed back, and nothing here waits for an answer.
	responses chan *api.RuntimeMessage

	// order is what makes the sequence the wire order. A request waits here
	// for its turn and only then hands the message to the writer, so the
	// writer receives in number order and has nothing to reorder.
	order *gate

	mu      sync.Mutex
	id      uint64
	pending map[uint64]*streamCall
	err     error

	// closing says the stream has ended and its pending entries have been
	// closed, so no further entry is made for anything to wait on.
	closing bool

	once sync.Once
	done chan struct{}
}

var _ api.PluginService = &pluginStream{}

// newPluginStream returns a stream for p whose order is not yet armed.
func newPluginStream(p *plugin, srv api.StreamingRuntime_StartEventStreamServer) *pluginStream {
	// Until the plugin joins the live list, nothing is dispatched to it and
	// there is no order to keep.
	return &pluginStream{
		p:         p,
		srv:       srv,
		sends:     make(chan *streamCall),
		responses: make(chan *api.RuntimeMessage),
		order:     &gate{},
		pending:   make(map[uint64]*streamCall),
		done:      make(chan struct{}),
	}
}

// run pumps the stream until either end stops, and reports why.
//
// The caller is the StartEventStream handler, which must not return before
// the stream is finished with.
func (s *pluginStream) run() error {
	go s.read()
	s.write()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

// write takes requests in the order they were handed over, numbers them and
// puts them on the wire, and puts the replies to the plugin's own requests
// on it as they are answered.
func (s *pluginStream) write() {
	// Numbering and writing both happen in this one goroutine, which is
	// what makes the sequence the wire order rather than a wish about it.
	for {
		select {
		case msg := <-s.responses:
			// A reply carries the number the plugin minted for the request
			// it answers, so there is nothing here to number.
			//
			// A reply and a request reach the plugin in whichever order
			// this select takes them, so a request that the plugin's own
			// request unblocked can arrive ahead of the reply to it and a
			// plugin takes both orders. Nothing here rests on one of them:
			// the runtime drives a plugin whose registration succeeded, so
			// a request that overtakes a reply is never one for a plugin
			// that was refused.
			if err := s.srv.Send(msg); err != nil {
				s.shutdown(fmt.Errorf("%w while writing: %w", errStreamClosed, err))
				return
			}

		case c := <-s.sends:
			s.number(c)

			if err := s.srv.Send(c.msg); err != nil {
				s.shutdown(fmt.Errorf("%w while writing: %w", errStreamClosed, err))
				return
			}

		case <-s.done:
			return
		}
	}
}

// number gives c the next correlation id and makes the entry its reply is
// looked up in.
func (s *pluginStream) number(c *streamCall) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A correlation id and not an order. The order is the request's number,
	// which the gate in call has already applied by the time anything gets
	// here; this only has to tell one reply from another, and a request can
	// send a plugin more than one message -- a validator is asked to
	// validate the adjustment for the request it was just dispatched.
	//
	// The id is taken whatever happens, because the message still goes
	// out: the plugin has been told about every request before this one and
	// there is no way to tell it to forget one.
	s.id++
	c.msg.Seq = s.id
	c.id = s.id

	// A caller that gave up while c was on its way to the writer is owed
	// nothing, so it gets no entry, and neither does one whose stream has
	// already ended: shutdown closes the entries it finds, and one made
	// after that would be waited on for a reply that cannot arrive.
	if !c.abandoned && !s.closing {
		s.pending[c.id] = c
	}
}

// read hands each reply to the request it answers and each request the
// plugin originates to handle.
func (s *pluginStream) read() {
	for {
		rpl, err := s.srv.Recv()
		if err != nil {
			s.shutdown(fmt.Errorf("%w while reading: %w", errStreamClosed, err))
			return
		}

		// A request of the plugin's own is numbered in the plugin's
		// sequence namespace, so it answers nothing of ours and is not
		// looked for among the requests waiting for a reply.
		if isPluginRequest(rpl) {
			s.handle(rpl)
			continue
		}

		s.mu.Lock()
		c, ok := s.pending[rpl.GetSeq()]
		delete(s.pending, rpl.GetSeq())
		s.mu.Unlock()

		if !ok {
			// A reply to a request whose caller gave up, or one we never
			// sent. Neither is worth tearing the stream down for.
			log.Warnf(noCtx, "plugin answered unknown stream request %d", rpl.GetSeq())
			continue
		}

		// Buffered, so a caller that has given up cannot block the reader.
		c.reply <- rpl
	}
}

// arm starts ordering this stream at first.
func (s *pluginStream) arm(first seqNo) {
	s.order.arm(first)
}

// skip announces that seq will never reach this plugin, so the order is
// not left waiting on it.
func (s *pluginStream) skip(seq seqNo) {
	// A plugin is not subscribed to every event, and a request that fails
	// partway reaches only some plugins; both leave gaps that have to be
	// announced rather than discovered.
	s.order.skip(seq)
}

// shutdown records why the stream ended and fails everything waiting on it.
func (s *pluginStream) shutdown(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.closing = true
		pending := s.pending
		s.pending = map[uint64]*streamCall{}
		s.mu.Unlock()

		close(s.done)

		// Releases whatever is still waiting for a turn, so a plugin that
		// dies mid-burst does not strand the requests behind it.
		s.order.close(err)

		// A closed reply channel is how a waiter learns the stream is gone.
		for _, c := range pending {
			close(c.reply)
		}
	})
}

// call sends one request and waits for its reply.
func (s *pluginStream) call(ctx context.Context, msg *api.RuntimeMessage) (*api.PluginMessage, error) {
	// A request whose context ends first takes its entry with it, so what
	// the stream is still waiting for is what someone is still waiting on.
	// Every caller here arrives with the plugin request timeout already on
	// its context, so that wait is bounded.
	c := &streamCall{msg: msg, reply: make(chan *api.PluginMessage, 1)}

	var handed bool

	hand := func() {
		select {
		case s.sends <- c:
			handed = true
		case <-s.done:
		case <-ctx.Done():
		}
	}

	seq, ordered := seqFrom(ctx)

	// The turn is over once the message is handed to the writer, not once
	// the plugin answers: a stream write does not wait for a reply, which
	// is the whole reason a stream can carry an order a unary call cannot.
	if ordered && s.order.isArmed() {
		s.order.run(seq, hand)
	} else {
		hand()
	}

	if !handed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		return nil, s.closed()
	}

	select {
	case rpl, ok := <-c.reply:
		return s.answered(rpl, ok)

	case <-s.done:
		// A reply already handed over outranks the end of the stream, and
		// both are ready here once a plugin answers and then dies. The
		// reply is the plugin's decision and nothing else will report it:
		// read drops the entry as it hands the reply over, so the stream's
		// end does not close the channel it came on. The end of the stream
		// is reported to every call that has no answer yet.
		select {
		case rpl, ok := <-c.reply:
			return s.answered(rpl, ok)
		default:
		}

		// For the call the writer numbered after the stream had ended,
		// which has no entry and so no closed reply channel to learn it
		// from.
		return nil, s.closed()

	case <-ctx.Done():
		s.abandon(c)

		return nil, ctx.Err()
	}
}

// answered returns what a request was answered with: the reply itself, the
// error the plugin answered with, or why the stream ended for a reply
// channel that was closed rather than written to.
func (s *pluginStream) answered(rpl *api.PluginMessage, ok bool) (*api.PluginMessage, error) {
	if !ok {
		return nil, s.closed()
	}

	if e := rpl.GetError(); e != "" {
		return nil, errors.New(e)
	}

	return rpl, nil
}

// abandon drops what c was waiting for, for a caller that no longer is.
func (s *pluginStream) abandon(c *streamCall) {
	// Both halves matter: the entry goes if the writer has made one, and
	// the flag stops one being made if the writer has not got there yet. A
	// plugin that answers afterwards is answering a request nobody is
	// waiting for, which read already reports and does not treat as fatal.
	s.mu.Lock()
	defer s.mu.Unlock()

	c.abandoned = true
	delete(s.pending, c.id)
}

// closed reports why the stream ended.
func (s *pluginStream) closed() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return s.err
	}

	return errStreamClosed
}

// isPluginRequest reports whether msg carries a request the plugin
// originated rather than a reply to a request of the runtime's.
func isPluginRequest(msg *api.PluginMessage) bool {
	switch msg.GetMsg().(type) {
	case *api.PluginMessage_RegisterPluginRequest:
		return true
	}

	return false
}

// handle answers one request the plugin originated.
//
// Registration is the one such request and the one answer to it reaches
// a plugin once, so the reader answers it itself. The answer waits for
// the writer to take it, which makes a plugin that asks for more than it
// reads the plugin whose reader stalls: its own, and nothing beyond it.
func (s *pluginStream) handle(msg *api.PluginMessage) {
	rpl := s.answer(msg)
	rpl.Seq = msg.GetSeq()

	if err := s.respond(rpl); err != nil {
		log.Warnf(noCtx, "failed to answer request %d of plugin %q: %v",
			msg.GetSeq(), s.p.name(), err)
	}
}

// answer hands a request the plugin originated to the same runtime method
// the unary path uses and wraps what comes back.
//
// A case per request, so a request cannot be quietly missing from the
// stream path.
func (s *pluginStream) answer(msg *api.PluginMessage) *api.RuntimeMessage {
	switch m := msg.GetMsg().(type) {
	case *api.PluginMessage_RegisterPluginRequest:
		if err := s.p.claimRegistration(m.RegisterPluginRequest.GetPluginName()); err != nil {
			return &api.RuntimeMessage{Error: err.Error()}
		}

		if _, err := s.p.register(noCtx, m.RegisterPluginRequest); err != nil {
			return &api.RuntimeMessage{Error: err.Error()}
		}

		return &api.RuntimeMessage{
			Msg: &api.RuntimeMessage_RegisterPluginResponse{
				RegisterPluginResponse: &api.Empty{},
			},
		}
	}

	return &api.RuntimeMessage{
		Error: fmt.Sprintf("unexpected %T on the event stream", msg.GetMsg()),
	}
}

// respond hands a reply to one of the plugin's requests to the writer.
func (s *pluginStream) respond(msg *api.RuntimeMessage) error {
	select {
	case s.responses <- msg:
		return nil
	case <-s.done:
		return s.closed()
	}
}

// StateChange returns the error that it is not carried on a stream.
func (s *pluginStream) StateChange(context.Context, *api.StateChangeEvent) (*api.Empty, error) {
	// The dedicated per-event messages replaced it before this protocol
	// existed, and a plugin new enough to stream is new enough to use
	// them, so arriving here is a bug worth reporting rather than
	// something to tunnel.
	return nil, errors.New("StateChange is not carried on a plugin event stream")
}

//
// Relaying each of service Plugin's requests over the stream. One per
// request, so an event cannot be quietly missing from the stream path.
//

func (s *pluginStream) Configure(ctx context.Context, req *api.ConfigureRequest) (*api.ConfigureResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_ConfigureRequest{ConfigureRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetConfigureResponse(), nil
}

func (s *pluginStream) Synchronize(ctx context.Context, req *api.SynchronizeRequest) (*api.SynchronizeResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_SynchronizeRequest{SynchronizeRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetSynchronizeResponse(), nil
}

func (s *pluginStream) Shutdown(ctx context.Context, req *api.Empty) (*api.Empty, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_ShutdownRequest{ShutdownRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetShutdownResponse(), nil
}

func (s *pluginStream) RunPodSandbox(ctx context.Context, req *api.RunPodSandboxRequest) (*api.RunPodSandboxResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_RunPodSandboxRequest{RunPodSandboxRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetRunPodSandboxResponse(), nil
}

func (s *pluginStream) UpdatePodSandbox(ctx context.Context, req *api.UpdatePodSandboxRequest) (*api.UpdatePodSandboxResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_UpdatePodSandboxRequest{UpdatePodSandboxRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetUpdatePodSandboxResponse(), nil
}

func (s *pluginStream) PostUpdatePodSandbox(ctx context.Context, req *api.PostUpdatePodSandboxRequest) (*api.PostUpdatePodSandboxResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_PostUpdatePodSandboxRequest{PostUpdatePodSandboxRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetPostUpdatePodSandboxResponse(), nil
}

func (s *pluginStream) StopPodSandbox(ctx context.Context, req *api.StopPodSandboxRequest) (*api.StopPodSandboxResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_StopPodSandboxRequest{StopPodSandboxRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetStopPodSandboxResponse(), nil
}

func (s *pluginStream) RemovePodSandbox(ctx context.Context, req *api.RemovePodSandboxRequest) (*api.RemovePodSandboxResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_RemovePodSandboxRequest{RemovePodSandboxRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetRemovePodSandboxResponse(), nil
}

func (s *pluginStream) CreateContainer(ctx context.Context, req *api.CreateContainerRequest) (*api.CreateContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_CreateContainerRequest{CreateContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetCreateContainerResponse(), nil
}

func (s *pluginStream) PostCreateContainer(ctx context.Context, req *api.PostCreateContainerRequest) (*api.PostCreateContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_PostCreateContainerRequest{PostCreateContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetPostCreateContainerResponse(), nil
}

func (s *pluginStream) StartContainer(ctx context.Context, req *api.StartContainerRequest) (*api.StartContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_StartContainerRequest{StartContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetStartContainerResponse(), nil
}

func (s *pluginStream) PostStartContainer(ctx context.Context, req *api.PostStartContainerRequest) (*api.PostStartContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_PostStartContainerRequest{PostStartContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetPostStartContainerResponse(), nil
}

func (s *pluginStream) UpdateContainer(ctx context.Context, req *api.UpdateContainerRequest) (*api.UpdateContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_UpdateContainerRequest{UpdateContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetUpdateContainerResponse(), nil
}

func (s *pluginStream) PostUpdateContainer(ctx context.Context, req *api.PostUpdateContainerRequest) (*api.PostUpdateContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_PostUpdateContainerRequest{PostUpdateContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetPostUpdateContainerResponse(), nil
}

func (s *pluginStream) StopContainer(ctx context.Context, req *api.StopContainerRequest) (*api.StopContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_StopContainerRequest{StopContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetStopContainerResponse(), nil
}

func (s *pluginStream) RemoveContainer(ctx context.Context, req *api.RemoveContainerRequest) (*api.RemoveContainerResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_RemoveContainerRequest{RemoveContainerRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetRemoveContainerResponse(), nil
}

func (s *pluginStream) ValidateContainerAdjustment(ctx context.Context, req *api.ValidateContainerAdjustmentRequest) (*api.ValidateContainerAdjustmentResponse, error) {
	rpl, err := s.call(ctx, &api.RuntimeMessage{
		Msg: &api.RuntimeMessage_ValidateContainerAdjustmentRequest{ValidateContainerAdjustmentRequest: req},
	})
	if err != nil {
		return nil, err
	}

	return rpl.GetValidateContainerAdjustmentResponse(), nil
}
