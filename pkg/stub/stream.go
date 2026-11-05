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

// PROTOTYPE. The plugin's end of the event stream.
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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/ttrpc"
)

// streamingEnv turns the event stream off, so one build can be measured
// both ways.
const streamingEnv = "NRI_STREAMING"

// streamReadyTimeout is how long to wait for a runtime to accept a stream
// before deciding it will not.
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
// Called before registering, so that which transport this plugin gets is
// settled before the runtime starts driving it, once and for the plugin's
// whole life. It never fails: a runtime that will not stream is not an
// error, it is the other half of the compatibility story.
func (stub *stub) startStream(ctx context.Context, rpcc *ttrpc.Client) {
	// Checked on the way out so that every exit below is covered by it,
	// including one added later: each already says which transport was
	// settled on, and none of them says what that costs a plugin that asked
	// for ordered delivery.
	defer func() {
		if stub.streaming || !stub.orderedEvents {
			return
		}

		stub.logger.Warnf(ctx, "WithOrderedEvents() was requested but this "+
			"plugin's requests arrive on unary calls, which cannot order "+
			"them: its handlers are invoked concurrently and in no "+
			"particular order")
	}()

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

	// Start() holds the lock, and nothing reads this until it returns.
	stub.streaming = true

	go stub.serveStream(stream, cancel)
}

// serveStream answers the runtime's requests off the stream.
//
// Concurrently by default, because that is what a plugin already does
// today: ttrpc answers each unary call in its own goroutine, so answering
// a stream one request at a time would make streaming slower than the
// thing it replaces. It would also make the plugin the bottleneck -- a
// burst of N requests taking d each would need N*d of plugin time no
// matter how little the runtime serializes.
//
// WithOrderedEvents asks for the other behaviour. A stream delivers in the
// order the runtime wrote, so answering serially is what turns the
// runtime's dispatch order into this plugin's processing order, and that is
// the ordering guarantee. It is opt-in because it is a trade, not an
// improvement.
func (stub *stub) serveStream(stream api.StreamingRuntime_StartEventStreamClient, cancel context.CancelFunc) {
	defer cancel()

	ctx := context.Background()

	// A stream is not safe for two senders, and concurrent answers mean
	// two senders.
	var sendLock sync.Mutex

	answer := func(req *api.RuntimeMessage) error {
		rpl := stub.answerStream(ctx, req)
		rpl.Seq = req.GetSeq()

		sendLock.Lock()
		defer sendLock.Unlock()

		return stream.Send(rpl)
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			stub.logger.Infof(ctx, "event stream ended: %v", err)

			return
		}

		if stub.orderedEvents {
			if err := answer(req); err != nil {
				stub.logger.Errorf(ctx, "failed to answer on the event stream: %v", err)

				return
			}

			continue
		}

		go func(req *api.RuntimeMessage) {
			if err := answer(req); err != nil {
				stub.logger.Errorf(ctx, "failed to answer on the event stream: %v", err)
			}
		}(req)
	}
}

// streamError is the reply to a request this plugin could not answer. It
// stands in for the error a unary call would have returned.
func streamError(err error) *api.PluginMessage {
	return &api.PluginMessage{Error: err.Error()}
}

// answerStream hands a request to the same stub method the unary path uses
// and wraps what comes back.
//
// A case per request, so an event cannot be quietly missing from the stream
// path; TestStreamAnswersEveryRequest fails if one is.
func (stub *stub) answerStream(ctx context.Context, req *api.RuntimeMessage) *api.PluginMessage {
	switch m := req.GetMsg().(type) {
	case *api.RuntimeMessage_ConfigureRequest:
		rpl, err := stub.Configure(ctx, m.ConfigureRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ConfigureResponse{ConfigureResponse: rpl},
		}

	case *api.RuntimeMessage_SynchronizeRequest:
		rpl, err := stub.Synchronize(ctx, m.SynchronizeRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_SynchronizeResponse{SynchronizeResponse: rpl},
		}

	case *api.RuntimeMessage_ShutdownRequest:
		rpl, err := stub.Shutdown(ctx, m.ShutdownRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ShutdownResponse{ShutdownResponse: rpl},
		}

	case *api.RuntimeMessage_RunPodSandboxRequest:
		rpl, err := stub.RunPodSandbox(ctx, m.RunPodSandboxRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RunPodSandboxResponse{RunPodSandboxResponse: rpl},
		}

	case *api.RuntimeMessage_UpdatePodSandboxRequest:
		rpl, err := stub.UpdatePodSandbox(ctx, m.UpdatePodSandboxRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_UpdatePodSandboxResponse{UpdatePodSandboxResponse: rpl},
		}

	case *api.RuntimeMessage_PostUpdatePodSandboxRequest:
		rpl, err := stub.PostUpdatePodSandbox(ctx, m.PostUpdatePodSandboxRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostUpdatePodSandboxResponse{PostUpdatePodSandboxResponse: rpl},
		}

	case *api.RuntimeMessage_StopPodSandboxRequest:
		rpl, err := stub.StopPodSandbox(ctx, m.StopPodSandboxRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StopPodSandboxResponse{StopPodSandboxResponse: rpl},
		}

	case *api.RuntimeMessage_RemovePodSandboxRequest:
		rpl, err := stub.RemovePodSandbox(ctx, m.RemovePodSandboxRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RemovePodSandboxResponse{RemovePodSandboxResponse: rpl},
		}

	case *api.RuntimeMessage_CreateContainerRequest:
		rpl, err := stub.CreateContainer(ctx, m.CreateContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_CreateContainerResponse{CreateContainerResponse: rpl},
		}

	case *api.RuntimeMessage_PostCreateContainerRequest:
		rpl, err := stub.PostCreateContainer(ctx, m.PostCreateContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostCreateContainerResponse{PostCreateContainerResponse: rpl},
		}

	case *api.RuntimeMessage_StartContainerRequest:
		rpl, err := stub.StartContainer(ctx, m.StartContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StartContainerResponse{StartContainerResponse: rpl},
		}

	case *api.RuntimeMessage_PostStartContainerRequest:
		rpl, err := stub.PostStartContainer(ctx, m.PostStartContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostStartContainerResponse{PostStartContainerResponse: rpl},
		}

	case *api.RuntimeMessage_UpdateContainerRequest:
		rpl, err := stub.UpdateContainer(ctx, m.UpdateContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_UpdateContainerResponse{UpdateContainerResponse: rpl},
		}

	case *api.RuntimeMessage_PostUpdateContainerRequest:
		rpl, err := stub.PostUpdateContainer(ctx, m.PostUpdateContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_PostUpdateContainerResponse{PostUpdateContainerResponse: rpl},
		}

	case *api.RuntimeMessage_StopContainerRequest:
		rpl, err := stub.StopContainer(ctx, m.StopContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_StopContainerResponse{StopContainerResponse: rpl},
		}

	case *api.RuntimeMessage_RemoveContainerRequest:
		rpl, err := stub.RemoveContainer(ctx, m.RemoveContainerRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_RemoveContainerResponse{RemoveContainerResponse: rpl},
		}

	case *api.RuntimeMessage_ValidateContainerAdjustmentRequest:
		rpl, err := stub.ValidateContainerAdjustment(ctx, m.ValidateContainerAdjustmentRequest)
		if err != nil {
			return streamError(err)
		}

		return &api.PluginMessage{
			Msg: &api.PluginMessage_ValidateContainerAdjustmentResponse{ValidateContainerAdjustmentResponse: rpl},
		}

	}

	return streamError(fmt.Errorf("unexpected %T on the event stream", req.GetMsg()))
}

// Streaming reports whether this plugin's requests arrive on an event
// stream. See the Stub interface for what it implies.
func (stub *stub) Streaming() bool {
	stub.Lock()
	defer stub.Unlock()

	return stub.streaming
}
