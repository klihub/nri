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

package stub

// The plugin's end of the event stream.
//
// The plugin offers the runtime a stream and the runtime sends this
// plugin's requests down the reply half of it. Offering it is also the
// whole of the version negotiation: a runtime too old to know the service
// fails the call, and this falls back to the unary calls every runtime
// understands. Nothing is compared and no version is consulted.
//
// The unary path is not a fallback that had to be written. It is what was
// already here -- the ttrpc server this stub registers either way -- and
// the requests arrive through the same stub methods whichever transport
// carried them. That is why talking to an older runtime costs almost
// nothing: this file is additive.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/ttrpc"
)

// errUnexpectedMessage is what a message answerStream has no case for is
// answered with.
//
// TestEveryStreamedRequestHasAnAnswer matches it, so the guard and the
// error it guards are one thing and the wording of the error is free to
// change without the guard quietly stopping guarding.
var errUnexpectedMessage = errors.New("unexpected message on the event stream")

// errStreamClosed is what a request waiting on a stream that went away
// gets.
//
// Every way a stream can end wraps it, so that a request of this plugin's
// own fails the way a unary call to a runtime that went away fails.
var errStreamClosed = errors.New("runtime event stream closed")

// streamingEnv turns the event stream off, so one build can be measured
// both ways.
const streamingEnv = "NRI_STREAMING"

// streamReadyTimeout is how long to wait for a runtime to accept a stream
// before deciding it will not: 2s. It gates registration as well as the
// choice of transport, since the plugin does not register until it is
// settled.
const streamReadyTimeout = 2 * time.Second

// streamingEnabled reports whether to offer the runtime an event stream.
func streamingEnabled() bool {
	switch strings.ToLower(os.Getenv(streamingEnv)) {
	case "0", "false", "no", "off", "disabled":
		return false
	}

	return true
}

// startStream offers the runtime an event stream and, if it takes it,
// starts answering on it.
//
// Must be called before registering, so that which transport this plugin
// gets is settled before the runtime starts driving it, once and for the
// plugin's whole life.
func (stub *stub) startStream(ctx context.Context, rpcc *ttrpc.Client) {
	// No way out of here is an error: a runtime that will not stream is
	// the other half of the compatibility story, and a plugin that does not
	// get a stream carries on with unary calls.
	if !streamingEnabled() {
		stub.logger.Infof(ctx, "%s is off, using unary calls", streamingEnv)
		return
	}

	// Not the caller's context: the stream outlives this call.
	sctx, cancel := context.WithCancel(context.Background())

	stream, err := api.NewStreamingRuntimeClient(rpcc).StartEventStream(sctx)
	if err != nil {
		cancel()
		stub.logger.Infof(ctx, "runtime takes no event stream (%v), using unary calls", err)

		return
	}

	// ttrpc need not have put anything on the wire yet, so wait to be told
	// the runtime accepted the stream rather than assume it did.
	type first struct {
		msg *api.RuntimeMessage
		err error
	}

	firstC := make(chan first, 1)

	go func() {
		msg, err := stream.Recv()
		firstC <- first{msg: msg, err: err}
	}()

	select {
	case f := <-firstC:
		switch {
		case f.err != nil:
			cancel()
			stub.logger.Infof(ctx, "runtime takes no event stream (%v), using unary calls", f.err)

			return

		case f.msg.GetStreamReady() == nil:
			cancel()
			stub.logger.Warnf(ctx, "runtime opened an event stream with %T, using unary calls",
				f.msg.GetMsg())

			return
		}

	case <-time.After(streamReadyTimeout):
		cancel()
		stub.logger.Warnf(ctx, "runtime did not accept an event stream in %v, using unary calls",
			streamReadyTimeout)

		return
	}

	stub.logger.Infof(ctx, "runtime accepted an event stream for %s", stub.Name())

	rs := newRuntimeStream(stream, cancel)

	stub.transport.Lock()
	stub.stream = rs
	stub.transport.Unlock()

	go rs.sender()
	go stub.serveStream(rs)
}

// serveStream answers the runtime's requests off the stream, one at a time
// and in the order the runtime wrote them, or several at once for a plugin
// that asked for parallel events, answers a validation as it arrives, and
// hands each answer to one of this plugin's own requests to its caller.
func (stub *stub) serveStream(rs *runtimeStream) {
	ctx := context.Background()

	// A stream delivers in the order the runtime wrote, and each request
	// waits for the one before it to be answered, which is what turns that
	// order into the order this plugin processes them in. Reading is what
	// that wait does not hold up: an answer this plugin is itself waiting
	// for arrives on the same stream as the requests it is answering, so a
	// request answered by the reader could not wait for one.
	//
	// So a request waits for its turn in a goroutine of its own, rather
	// than in the socket. How many there can be is how many requests the
	// runtime has in flight for this plugin, which is the same count it is
	// already spending a goroutine of its own on at the other end.
	//
	// Each request is answered once the one before it has been, and nothing
	// came before the first.
	answered := make(chan struct{})
	close(answered)

	for {
		msg, err := rs.stream.Recv()
		if err != nil {
			stub.logger.Infof(ctx, "event stream ended: %v", err)
			rs.shutdown(fmt.Errorf("%w while reading: %w", errStreamClosed, err))

			return
		}

		// An answer to one of this plugin's own requests carries the number
		// this plugin minted for it, in this plugin's own namespace, so it
		// is handed to whoever is waiting for it rather than answered.
		if isRuntimeReply(msg) {
			if !rs.reply(msg) {
				stub.logger.Warnf(ctx, "runtime answered unknown stream request %d",
					msg.GetSeq())
			}

			continue
		}

		// A validation takes no turn. The runtime asks a validator about
		// the request it has just dispatched to it, and sends that
		// question with no sequence number so that it overtakes the
		// ordered requests on the wire: the request it is about is inside
		// the runtime waiting for the answer, and its apply holds the
		// order at a number below every request written after it. A
		// validation answered in turn would therefore wait for the
		// handler of a request numbered above the one the runtime is
		// waiting on, which is the one dependency the order excludes.
		if isValidation(msg) {
			go stub.answerNow(ctx, rs, msg)

			continue
		}

		// A plugin that asked for parallel events waits for no turn either,
		// so its handlers run as the requests arrive and several at once.
		if stub.parallelEvents {
			go stub.answerNow(ctx, rs, msg)

			continue
		}

		prev, done := answered, make(chan struct{})
		answered = done

		go stub.answerInTurn(ctx, rs, msg, prev, done)
	}
}

// answerInTurn answers one of the runtime's requests once prev has been
// answered, and reports it answered on done.
func (stub *stub) answerInTurn(ctx context.Context, rs *runtimeStream, req *api.RuntimeMessage, prev, done chan struct{}) {
	defer close(done)

	// A stream that ends while a request is waiting for its turn leaves
	// that request unanswered, which is what the runtime's end of a stream
	// that went away does with everything else waiting on it.
	select {
	case <-prev:
	case <-rs.done:
		return
	}

	// The stream ending releases every request waiting for a turn, so by
	// the time one gets here both of the above can be ready at once and
	// which the select took says nothing. Asking again, after the turn, is
	// what makes the ended stream the answer either way.
	select {
	case <-rs.done:
		return
	default:
	}

	stub.answerNow(ctx, rs, req)
}

// answerNow answers one of the runtime's requests, and ends the stream if
// the answer cannot be put on it.
func (stub *stub) answerNow(ctx context.Context, rs *runtimeStream, req *api.RuntimeMessage) {
	if err := stub.answer(ctx, rs, req); err != nil {
		stub.logger.Errorf(ctx, "failed to answer on the event stream: %v", err)
		rs.shutdown(fmt.Errorf("%w while writing: %w", errStreamClosed, err))
	}
}

// answer hands one of the runtime's requests to the plugin and puts the
// reply on the stream.
func (stub *stub) answer(ctx context.Context, rs *runtimeStream, req *api.RuntimeMessage) error {
	rpl, err := stub.answerStream(ctx, req)
	if err != nil {
		rpl = streamError(err)
	}
	rpl.Seq = req.GetSeq()

	return rs.write(rpl)
}

// isRuntimeReply reports whether msg answers a request this plugin
// originated rather than carrying a request of the runtime's.
func isRuntimeReply(msg *api.RuntimeMessage) bool {
	switch msg.GetMsg().(type) {
	case *api.RuntimeMessage_RegisterPluginResponse,
		*api.RuntimeMessage_UpdateContainersResponse:
		return true
	}

	// A runtime that refused a request answers with nothing but the error
	// it refused with, which is an answer too. That rests on an error with
	// no message being the runtime's way of refusing a request and nothing
	// else, which the message itself does not say: RuntimeMessage carries
	// one error field for both directions, so a runtime-originated message
	// that carried an error alone would be taken for an answer here.
	return msg.GetMsg() == nil && msg.GetError() != ""
}

// isValidation reports whether msg carries a request to validate a
// container adjustment.
func isValidation(msg *api.RuntimeMessage) bool {
	_, ok := msg.GetMsg().(*api.RuntimeMessage_ValidateContainerAdjustmentRequest)

	return ok
}

// streamError is the reply to a request this plugin could not answer. It
// stands in for the error a unary call would have returned.
func streamError(err error) *api.PluginMessage {
	return &api.PluginMessage{Error: err.Error()}
}

// streamCall is one request of this plugin's waiting for its answer.
type streamCall struct {
	msg   *api.PluginMessage
	reply chan *api.RuntimeMessage

	// What the sender numbered this call, and whether its caller is still
	// waiting. Both are read and written under runtimeStream.mu, which is
	// what lets a caller that gives up before the sender gets there stop
	// the entry from being made at all.
	id        uint64
	abandoned bool
}

// runtimeStream is this plugin's end of its event stream.
type runtimeStream struct {
	stream api.StreamingRuntime_StartEventStreamClient
	sends  chan *streamCall

	// cancel ends the stream itself. Every way the stream can end runs it,
	// so the reader is woken by whatever noticed, rather than waiting for a
	// message on a stream nothing is going to write to again.
	cancel context.CancelFunc

	// sendLock keeps the two senders apart: a stream is not safe for two,
	// and the answers to the runtime's requests travel on it alongside
	// this plugin's own requests.
	sendLock sync.Mutex

	mu      sync.Mutex
	seq     uint64
	pending map[uint64]*streamCall
	err     error

	// closing says the stream has ended and its pending entries have been
	// closed, so no further entry is made for anything to wait on.
	closing bool

	once sync.Once
	done chan struct{}
}

// newRuntimeStream returns this plugin's end of stream, which cancel ends.
func newRuntimeStream(stream api.StreamingRuntime_StartEventStreamClient, cancel context.CancelFunc) *runtimeStream {
	return &runtimeStream{
		stream:  stream,
		cancel:  cancel,
		sends:   make(chan *streamCall),
		pending: make(map[uint64]*streamCall),
		done:    make(chan struct{}),
	}
}

// sender numbers this plugin's requests in the order they were handed over
// and puts them on the wire.
func (s *runtimeStream) sender() {
	// Numbering and writing both happen in this one goroutine, so the
	// numbers this plugin mints count up without gaps in the order its
	// requests reach the wire.
	for {
		select {
		case c := <-s.sends:
			s.number(c)

			if err := s.write(c.msg); err != nil {
				s.shutdown(fmt.Errorf("%w while writing: %w", errStreamClosed, err))

				return
			}

		case <-s.done:
			return
		}
	}
}

// number gives c the next number this plugin mints and makes the entry its
// answer is looked up in.
func (s *runtimeStream) number(c *streamCall) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The number is taken whatever happens, because leaving one out of the
	// wire would look to the runtime like a request it had missed.
	s.seq++
	c.msg.Seq = s.seq
	c.id = s.seq

	// A caller that gave up while c was on its way to the sender is owed
	// nothing, so it gets no entry, and neither does one whose stream has
	// already ended: shutdown closes the entries it finds, and one made
	// after that would be waited on for an answer that cannot arrive.
	if !c.abandoned && !s.closing {
		s.pending[c.id] = c
	}
}

// write puts one message on the wire.
func (s *runtimeStream) write(msg *api.PluginMessage) error {
	s.sendLock.Lock()
	defer s.sendLock.Unlock()

	return s.stream.Send(msg)
}

// call sends one request of this plugin's and returns the runtime's answer.
func (s *runtimeStream) call(ctx context.Context, msg *api.PluginMessage) (*api.RuntimeMessage, error) {
	// A caller whose context ends first takes its entry with it, so what
	// the stream is still waiting for is what someone is still waiting on.
	c := &streamCall{msg: msg, reply: make(chan *api.RuntimeMessage, 1)}

	// Straight to the sender: this plugin originates no ordered requests,
	// so there is no turn to wait for.
	select {
	case s.sends <- c:
	case <-s.done:
		return nil, s.closed()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case rpl, ok := <-c.reply:
		return s.answered(rpl, ok)

	case <-s.done:
		// A reply already handed over outranks the end of the stream, and
		// both are ready here once the runtime answers and the stream then
		// dies. The reply is the runtime's answer and nothing else will
		// report it: reply drops the entry as it hands the answer over, so
		// the stream's end does not close the channel it came on. The end
		// of the stream is reported to every call that has no answer yet.
		select {
		case rpl, ok := <-c.reply:
			return s.answered(rpl, ok)
		default:
		}

		// For the call the sender numbered after the stream had ended,
		// which has no entry and so no closed reply channel to learn it
		// from.
		return nil, s.closed()

	case <-ctx.Done():
		s.abandon(c)

		return nil, ctx.Err()
	}
}

// answered returns what a request was answered with: the answer itself, the
// error the runtime answered with, or why the stream ended for a reply
// channel that was closed rather than written to.
func (s *runtimeStream) answered(rpl *api.RuntimeMessage, ok bool) (*api.RuntimeMessage, error) {
	if !ok {
		return nil, s.closed()
	}

	if e := rpl.GetError(); e != "" {
		return nil, errors.New(e)
	}

	return rpl, nil
}

// reply hands an answer to the caller waiting for it, and reports whether
// anyone was.
func (s *runtimeStream) reply(msg *api.RuntimeMessage) bool {
	s.mu.Lock()
	c, ok := s.pending[msg.GetSeq()]
	delete(s.pending, msg.GetSeq())
	s.mu.Unlock()

	if !ok {
		return false
	}

	// Buffered, so a caller that has given up cannot block the reader.
	c.reply <- msg

	return true
}

// abandon drops what c was waiting for, for a caller that no longer is.
func (s *runtimeStream) abandon(c *streamCall) {
	// Both halves matter: the entry goes if the sender has made one, and
	// the flag stops one being made if the sender has not got there yet. An
	// answer that arrives afterwards answers a request nobody is waiting
	// for, which serveStream reports and does not treat as fatal.
	s.mu.Lock()
	defer s.mu.Unlock()

	c.abandoned = true
	delete(s.pending, c.id)
}

// shutdown records why the stream ended and fails everything waiting on it.
func (s *runtimeStream) shutdown(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.closing = true
		pending := s.pending
		s.pending = map[uint64]*streamCall{}
		s.mu.Unlock()

		close(s.done)
		s.cancel()

		// A closed reply channel is how a waiter learns the stream is gone.
		for _, c := range pending {
			close(c.reply)
		}
	})
}

// closed reports why the stream ended.
func (s *runtimeStream) closed() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.err != nil {
		return s.err
	}

	return errStreamClosed
}

// answerStream hands a request to the same stub method the unary path uses
// and returns what comes back, wrapped in the reply that carries it.
func (stub *stub) answerStream(ctx context.Context, req *api.RuntimeMessage) (*api.PluginMessage, error) {
	// A case per request, so an event cannot be quietly missing from the
	// stream path. TestEveryStreamedRequestHasAnAnswer fails if one is,
	// because a message with no case is answered with
	// errUnexpectedMessage.
	switch m := req.GetMsg().(type) {
	case *api.RuntimeMessage_ConfigureRequest:
		rpl, err := stub.Configure(ctx, m.ConfigureRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ConfigureResponse{ConfigureResponse: rpl},
		}, nil

	case *api.RuntimeMessage_SynchronizeRequest:
		rpl, err := stub.Synchronize(ctx, m.SynchronizeRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_SynchronizeResponse{SynchronizeResponse: rpl},
		}, nil

	case *api.RuntimeMessage_ShutdownRequest:
		rpl, err := stub.Shutdown(ctx, m.ShutdownRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ShutdownResponse{ShutdownResponse: rpl},
		}, nil

	case *api.RuntimeMessage_RunPodSandboxRequest:
		rpl, err := stub.RunPodSandbox(ctx, m.RunPodSandboxRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RunPodSandboxResponse{RunPodSandboxResponse: rpl},
		}, nil

	case *api.RuntimeMessage_UpdatePodSandboxRequest:
		rpl, err := stub.UpdatePodSandbox(ctx, m.UpdatePodSandboxRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_UpdatePodSandboxResponse{UpdatePodSandboxResponse: rpl},
		}, nil

	case *api.RuntimeMessage_PostUpdatePodSandboxRequest:
		rpl, err := stub.PostUpdatePodSandbox(ctx, m.PostUpdatePodSandboxRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostUpdatePodSandboxResponse{PostUpdatePodSandboxResponse: rpl},
		}, nil

	case *api.RuntimeMessage_StopPodSandboxRequest:
		rpl, err := stub.StopPodSandbox(ctx, m.StopPodSandboxRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StopPodSandboxResponse{StopPodSandboxResponse: rpl},
		}, nil

	case *api.RuntimeMessage_RemovePodSandboxRequest:
		rpl, err := stub.RemovePodSandbox(ctx, m.RemovePodSandboxRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RemovePodSandboxResponse{RemovePodSandboxResponse: rpl},
		}, nil

	case *api.RuntimeMessage_CreateContainerRequest:
		rpl, err := stub.CreateContainer(ctx, m.CreateContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_CreateContainerResponse{CreateContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_PostCreateContainerRequest:
		rpl, err := stub.PostCreateContainer(ctx, m.PostCreateContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostCreateContainerResponse{PostCreateContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_StartContainerRequest:
		rpl, err := stub.StartContainer(ctx, m.StartContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StartContainerResponse{StartContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_PostStartContainerRequest:
		rpl, err := stub.PostStartContainer(ctx, m.PostStartContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostStartContainerResponse{PostStartContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_UpdateContainerRequest:
		rpl, err := stub.UpdateContainer(ctx, m.UpdateContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_UpdateContainerResponse{UpdateContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_PostUpdateContainerRequest:
		rpl, err := stub.PostUpdateContainer(ctx, m.PostUpdateContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostUpdateContainerResponse{PostUpdateContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_StopContainerRequest:
		rpl, err := stub.StopContainer(ctx, m.StopContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StopContainerResponse{StopContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_RemoveContainerRequest:
		rpl, err := stub.RemoveContainer(ctx, m.RemoveContainerRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RemoveContainerResponse{RemoveContainerResponse: rpl},
		}, nil

	case *api.RuntimeMessage_ValidateContainerAdjustmentRequest:
		rpl, err := stub.ValidateContainerAdjustment(ctx, m.ValidateContainerAdjustmentRequest)
		if err != nil {
			return nil, err
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ValidateContainerAdjustmentResponse{ValidateContainerAdjustmentResponse: rpl},
		}, nil

	}

	return nil, fmt.Errorf("%w: %T", errUnexpectedMessage, req.GetMsg())
}

// Streaming reports whether this plugin's requests arrive on an event
// stream. See the Stub interface for what it implies.
func (stub *stub) Streaming() bool {
	stream, _ := stub.transports()

	return stream != nil
}
