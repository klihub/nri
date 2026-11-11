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

// The stream's own tests, for the part of it that is not observable from
// outside NRI. What a plugin still owes an answer on is internal state, and
// a test driven through a real plugin cannot hold a request open for as
// long as the check needs it held.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// blockingStreamServer takes every message and never answers one.
//
// A plugin that owes an answer for as long as the test wants one owed,
// which is not something a real stub can be asked for.
type blockingStreamServer struct {
	sent chan *api.RuntimeMessage
	stop chan struct{}
}

func newBlockingStreamServer() *blockingStreamServer {
	return &blockingStreamServer{
		sent: make(chan *api.RuntimeMessage, 16),
		stop: make(chan struct{}),
	}
}

func (s *blockingStreamServer) Send(msg *api.RuntimeMessage) error {
	s.sent <- msg

	return nil
}

func (s *blockingStreamServer) Recv() (*api.PluginMessage, error) {
	<-s.stop

	return nil, errors.New("stream server stopped")
}

func (s *blockingStreamServer) SendMsg(interface{}) error {
	return nil
}

func (s *blockingStreamServer) RecvMsg(interface{}) error {
	return nil
}

// TestAnAbandonedCallLeavesNothingBehind pins that a caller which gives up
// takes its pending entry with it.
//
// That entry is what says the plugin still owes an answer. Left behind, it
// says so about a request whose place in the order has already been handed
// on, and the order is then free to pass a number that still looks owed.
func TestAnAbandonedCallLeavesNothingBehind(t *testing.T) {
	srv := newBlockingStreamServer()
	defer close(srv.stop)

	s := newPluginStream(&plugin{}, srv)
	go func() { _ = s.run(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())

	errC := make(chan error, 1)

	go func() {
		_, err := s.call(ctx, &api.RuntimeMessage{})
		errC <- err
	}()

	// The entry is written before the message goes on the wire, so one the
	// server has taken is one the stream is already tracking. Waiting for
	// it is what keeps the test from passing for the wrong reason, by
	// cancelling before there is anything to leave behind.
	select {
	case <-srv.sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the wire")
	}

	require.Equal(t, 1, s.owed(), "the stream is not tracking the call")

	cancel()

	select {
	case err := <-errC:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("the call outlived its context")
	}

	require.Zero(t, s.owed(), "a call its caller gave up on left its entry behind")
}

// TestADrainedPluginStreamTakesNoFurtherEntry and the test after it are
// the one window a plugin can die in: between a call handing itself to the
// writer and the writer numbering it. It is not reachable from a caller --
// by then the call is past the point a test could hold it at -- so the test
// plays the writer itself.
func TestADrainedPluginStreamTakesNoFurtherEntry(t *testing.T) {
	// shutdown closes the entries it finds, so one made after it would be
	// waited on for a reply that cannot arrive: the plugin is gone and
	// nothing will close that entry a second time.
	srv := newBlockingStreamServer()
	defer close(srv.stop)

	s := newPluginStream(&plugin{}, srv)

	s.shutdown(errors.New("the plugin died"))

	c := &streamCall{
		msg:   &api.RuntimeMessage{},
		reply: make(chan *api.PluginMessage, 1),
	}

	s.number(c)

	require.Zero(t, s.owed(), "a drained stream took an entry nothing can answer")
	require.NotZero(t, c.msg.GetSeq(), "the request went out without a number")
}

func TestACallNumberedAfterTheStreamDiedFailsAtOnce(t *testing.T) {
	// Such a call has no entry, so a closed reply channel is not how it
	// learns the plugin is gone. Left to its context, it waits out the
	// whole plugin request timeout before failing, which is a plugin's
	// death made slower than the plugin's own crash.
	srv := newBlockingStreamServer()
	defer close(srv.stop)

	s := newPluginStream(&plugin{}, srv)

	// No writer: the test is the writer, which is how the call is held
	// between handing itself over and being numbered.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errC := make(chan error, 1)

	go func() {
		_, err := s.call(ctx, &api.RuntimeMessage{})
		errC <- err
	}()

	var c *streamCall

	select {
	case c = <-s.sends:
	case <-time.After(10 * time.Second):
		t.Fatal("the call never reached the writer")
	}

	s.shutdown(errors.New("the plugin died"))
	s.number(c)

	select {
	case err := <-errC:
		require.ErrorContains(t, err, "the plugin died",
			"the call did not report why the stream ended")
	case <-time.After(5 * time.Second):
		t.Fatal("the call waited for a reply the dead stream cannot carry")
	}
}

func TestEveryPluginRequestHasAnAnswer(t *testing.T) {
	// A new request arm in the proto with no case in answer is a request
	// the runtime silently refuses. The compiler cannot see it: the arm is
	// an interface implementation, not a switch the compiler checks. Each
	// request arm is answered in turn, and none may come back as an
	// unexpected message.
	//
	// The plugin is given what answering a request takes rather than a
	// stand-in for it: a registration reports itself on regC, and an update
	// reaches the runtime's UpdateFn.
	p := &plugin{
		regC: make(chan error, 1),
		r: &Adaptation{
			updateFn: func(context.Context, []*ContainerUpdate) ([]*ContainerUpdate, error) {
				return nil, nil
			},
		},
	}

	// Nothing an answer is built from goes near the stream itself.
	s := newPluginStream(p, nil)

	md := (&api.PluginMessage{}).ProtoReflect().Descriptor()
	arms := md.Oneofs().ByName("msg").Fields()

	checked := 0
	for i := 0; i < arms.Len(); i++ {
		arm := arms.Get(i)
		if !strings.HasSuffix(string(arm.Name()), "_request") {
			continue
		}

		msg := &api.PluginMessage{}
		m := msg.ProtoReflect()
		m.Set(arm, protoreflect.ValueOfMessage(m.NewField(arm).Message()))

		// Both halves of the routing: a request the reader takes for a
		// reply is looked up among the requests waiting for one and
		// reported as an answer to a request nobody made.
		require.True(t, isPluginRequest(msg),
			"isPluginRequest does not know %s", arm.Name())

		rpl := s.answer(context.Background(), msg)
		require.NotContains(t, rpl.GetError(), errUnexpectedMessage.Error(),
			"answer has no case for %s", arm.Name())
		checked++
	}

	require.NotZero(t, checked, "PluginMessage has no request arms")
}

// owed is how many answers this stream is still waiting for.
func (s *pluginStream) owed() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.pending)
}
