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
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/stretchr/testify/require"
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

	s := newPluginStream(nil, srv)
	go func() { _ = s.run() }()

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

// owed is how many answers this stream is still waiting for.
func (s *pluginStream) owed() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.pending)
}
