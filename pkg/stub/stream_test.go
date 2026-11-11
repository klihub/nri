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

// discardLogger drops every message.
type discardLogger struct{}

func (discardLogger) Debugf(context.Context, string, ...interface{}) {}
func (discardLogger) Infof(context.Context, string, ...interface{})  {}
func (discardLogger) Warnf(context.Context, string, ...interface{})  {}
func (discardLogger) Errorf(context.Context, string, ...interface{}) {}

// blockingStreamClient takes every message and answers none.
//
// A runtime that owes an answer for as long as the test wants one owed,
// which is not something a real runtime can be asked for.
type blockingStreamClient struct {
	sent chan *api.PluginMessage
	stop chan struct{}
}

func newBlockingStreamClient() *blockingStreamClient {
	return &blockingStreamClient{
		sent: make(chan *api.PluginMessage, 16),
		stop: make(chan struct{}),
	}
}

func (s *blockingStreamClient) Send(msg *api.PluginMessage) error {
	s.sent <- msg

	return nil
}

func (s *blockingStreamClient) Recv() (*api.RuntimeMessage, error) {
	<-s.stop

	return nil, errors.New("stream client stopped")
}

func (s *blockingStreamClient) CloseSend() error {
	return nil
}

func (s *blockingStreamClient) SendMsg(interface{}) error {
	return nil
}

func (s *blockingStreamClient) RecvMsg(interface{}) error {
	return nil
}

func TestADeadStreamOutranksARequestsTurn(t *testing.T) {
	// A stream that ends releases every request waiting for a turn, so the
	// turn and the end of the stream are ready together and the wait for
	// them cannot say which came first. The request goes unanswered, which
	// is the only answer a stream that is gone can carry.
	//
	// A hundred rounds, because one is a coin toss: a request answered on a
	// dead stream fails writing and is reported as a failure, so taking the
	// turn half the time would look like a flake rather than a policy.
	const rounds = 100

	stub := &stub{logger: discardLogger{}}

	for i := 0; i < rounds; i++ {
		rpc := newBlockingStreamClient()
		rs := newRuntimeStream(rpc, func() {})

		prev, done := make(chan struct{}), make(chan struct{})

		go stub.answerInTurn(context.Background(), rs, &api.RuntimeMessage{}, prev, done)

		rs.shutdown(errors.New("the stream ended"))
		close(prev)

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a request waiting for its turn never finished")
		}

		require.Empty(t, rpc.sent, "a request was answered on a stream that had ended")
	}
}

// owed is how many answers this stream is still waiting for.
func (s *runtimeStream) owed() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.pending)
}

func TestAnAbandonedStreamCallLeavesNothingBehind(t *testing.T) {
	// That entry is what a runtime's answer is handed over by. Left behind,
	// it is an answer kept for a caller that has gone, and the stream never
	// runs out of them.
	rpc := newBlockingStreamClient()
	defer close(rpc.stop)

	rs := newRuntimeStream(rpc, func() {})
	go rs.sender()

	defer rs.shutdown(errors.New("the test is over"))

	ctx, cancel := context.WithCancel(context.Background())

	errC := make(chan error, 1)

	go func() {
		_, err := rs.call(ctx, &api.PluginMessage{})
		errC <- err
	}()

	// The entry is made before the message goes on the wire, so one the
	// runtime has taken is one the stream is already tracking. Waiting for
	// it is what keeps the test from passing for the wrong reason, by
	// cancelling before there is anything to leave behind.
	select {
	case <-rpc.sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the wire")
	}

	require.Equal(t, 1, rs.owed(), "the stream is not tracking the call")

	cancel()

	select {
	case err := <-errC:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("the call outlived its context")
	}

	require.Zero(t, rs.owed(), "a call its caller gave up on left its entry behind")
}

func TestADrainedStreamTakesNoFurtherEntry(t *testing.T) {
	// shutdown closes the entries it finds, so one made after it would be
	// waited on for an answer that cannot arrive: the stream is gone and
	// nothing will close that entry a second time.
	//
	// The window is between a call handing itself to the sender and the
	// sender numbering it, which is not reachable from a caller: by the
	// time the stream has drained, the call that is in it is past the point
	// a test could hold it at.
	rpc := newBlockingStreamClient()
	defer close(rpc.stop)

	rs := newRuntimeStream(rpc, func() {})

	rs.shutdown(errors.New("the stream ended"))

	c := &streamCall{
		msg:   &api.PluginMessage{},
		reply: make(chan *api.RuntimeMessage, 1),
	}

	rs.number(c)

	require.Zero(t, rs.owed(), "a drained stream took an entry nothing can answer")
	require.NotZero(t, c.msg.GetSeq(), "the request went out without a number")
}

// unusedRuntimeClient is a unary client that reports every use of it.
type unusedRuntimeClient struct{}

func (unusedRuntimeClient) RegisterPlugin(context.Context, *api.RegisterPluginRequest) (*api.Empty, error) {
	return nil, errors.New("the unary client of a lost connection was used")
}

func (unusedRuntimeClient) UpdateContainers(context.Context, *api.UpdateContainersRequest) (*api.UpdateContainersResponse, error) {
	return nil, errors.New("the unary client of a lost connection was used")
}

func TestCloseForgetsTheTransportItHad(t *testing.T) {
	// Which transport a plugin gets is settled per connection, so a stub
	// that reconnects settles it again from scratch. One that kept either
	// of them reports, or uses, the transport of a connection that is
	// gone. Both are set here because a stub that reconnected onto the
	// other transport is how it ends up holding both.
	stub := &stub{
		logger:  discardLogger{},
		started: true,
		stream:  newRuntimeStream(nil, func() {}),
		runtime: unusedRuntimeClient{},
	}

	stub.close()

	require.False(t, stub.Streaming(),
		"a closed stub should not report the stream of the connection it lost")

	_, err := stub.UpdateContainers(context.Background(), nil)
	require.ErrorIs(t, err, ErrNoService,
		"a closed stub should not use the unary client of the connection it lost")
}

func TestEveryStreamedRequestHasAnAnswer(t *testing.T) {
	// A new request arm in the proto with no case in answerStream is a
	// request the plugin silently fails. The compiler cannot see it: the
	// arm is an interface implementation, not a switch the compiler checks.
	// Each request arm is sent in turn, and none may come back as an
	// unexpected message.
	stub := &stub{logger: discardLogger{}, cfgErrC: make(chan error, 1)}
	md := (&api.RuntimeMessage{}).ProtoReflect().Descriptor()
	arms := md.Oneofs().ByName("msg").Fields()

	checked := 0
	for i := 0; i < arms.Len(); i++ {
		arm := arms.Get(i)
		if !strings.HasSuffix(string(arm.Name()), "_request") {
			continue
		}

		req := &api.RuntimeMessage{}
		m := req.ProtoReflect()
		m.Set(arm, protoreflect.ValueOfMessage(m.NewField(arm).Message()))

		rpl, err := stub.answerStream(context.Background(), req)
		require.NotErrorIs(t, err, errUnexpectedMessage,
			"answerStream has no case for %s", arm.Name())

		// serveStream puts the request's number on what comes back, so an
		// arm answered with neither a reply nor an error takes the reader
		// down with it.
		require.NotNil(t, rpl, "answerStream answered %s with nothing", arm.Name())
		checked++
	}

	require.NotZero(t, checked, "RuntimeMessage has no request arms")
}
