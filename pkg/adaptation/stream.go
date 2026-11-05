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

// PROTOTYPE. The runtime's end of a plugin's event stream.
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

// errStreamClosed is what a request waiting on a stream that went away gets
// when nothing more specific was recorded.
var errStreamClosed = errors.New("plugin event stream closed")

// streamCall is one request waiting for its reply.
type streamCall struct {
	msg   *api.RuntimeMessage
	reply chan *api.PluginMessage
}

// pluginStream is one plugin's event stream.
type pluginStream struct {
	srv   api.StreamingRuntime_StartEventStreamServer
	sends chan *streamCall

	mu      sync.Mutex
	pending map[uint64]*streamCall
	err     error

	once sync.Once
	done chan struct{}
}

var _ api.PluginService = &pluginStream{}

func newPluginStream(srv api.StreamingRuntime_StartEventStreamServer) *pluginStream {
	return &pluginStream{
		srv:     srv,
		sends:   make(chan *streamCall),
		pending: make(map[uint64]*streamCall),
		done:    make(chan struct{}),
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
// puts them on the wire.
//
// Numbering and writing happen here, in one goroutine, which is what makes
// the sequence the wire order rather than a wish about it.
func (s *pluginStream) write() {
	seq := uint64(0)

	for {
		select {
		case c := <-s.sends:
			seq++
			c.msg.Seq = seq

			// Registered before the write, so a reply that comes back
			// before this returns still finds its caller.
			s.mu.Lock()
			s.pending[seq] = c
			s.mu.Unlock()

			if err := s.srv.Send(c.msg); err != nil {
				s.shutdown(fmt.Errorf("failed to write plugin event stream: %w", err))
				return
			}

		case <-s.done:
			return
		}
	}
}

// read hands each reply to the request it answers.
func (s *pluginStream) read() {
	for {
		rpl, err := s.srv.Recv()
		if err != nil {
			s.shutdown(fmt.Errorf("failed to read plugin event stream: %w", err))
			return
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

// shutdown records why the stream ended and fails everything waiting on it.
func (s *pluginStream) shutdown(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		pending := s.pending
		s.pending = map[uint64]*streamCall{}
		s.mu.Unlock()

		close(s.done)

		// A closed reply channel is how a waiter learns the stream is gone.
		for _, c := range pending {
			close(c.reply)
		}
	})
}

// call sends one request and waits for its reply.
//
// A request whose context ends first leaves its entry behind, reaped when
// the plugin answers or the stream ends. Every caller here arrives with the
// plugin request timeout already on its context, so that wait is bounded.
func (s *pluginStream) call(ctx context.Context, msg *api.RuntimeMessage) (*api.PluginMessage, error) {
	c := &streamCall{msg: msg, reply: make(chan *api.PluginMessage, 1)}

	select {
	case s.sends <- c:
	case <-s.done:
		return nil, s.closed()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case rpl, ok := <-c.reply:
		if !ok {
			return nil, s.closed()
		}
		if e := rpl.GetError(); e != "" {
			return nil, errors.New(e)
		}

		return rpl, nil

	case <-ctx.Done():
		return nil, ctx.Err()
	}
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

// StateChange is not carried on a stream.
//
// The dedicated per-event messages replaced it before this protocol
// existed, and a plugin new enough to stream is new enough to use them, so
// arriving here is a bug worth reporting rather than something to tunnel.
func (s *pluginStream) StateChange(context.Context, *api.StateChangeEvent) (*api.Empty, error) {
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
