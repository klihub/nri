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

package framework

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

var t0 = time.Unix(1000, 0)

func span(id, parent, name string, startMs, endMs int, attrs ...string) *TraceSpan {
	s := &TraceSpan{
		Trace: "t" + id[:1], ID: id, Parent: parent, Name: name,
		Start: t0.Add(time.Duration(startMs) * time.Millisecond),
		End:   t0.Add(time.Duration(endMs) * time.Millisecond),
		Attrs: map[string]string{},
	}

	for i := 0; i+1 < len(attrs); i += 2 {
		s.Attrs[attrs[i]] = attrs[i+1]
	}

	return s
}

func hookSpan(id, parent, hook string, startMs, endMs int) *TraceSpan {
	return span(id, parent, nriPluginService+"/"+hook, startMs, endMs,
		"rpc.service", nriPluginService, "rpc.method", hook)
}

func TestHookTraceAttributesByClientTrace(t *testing.T) {
	s := hookSpan("h1", "", "StopContainer", 10, 20)
	s.Trace = "t1"
	other := hookSpan("h2", "", "StopContainer", 30, 40)
	other.Trace = "t2"

	ht := NewHookTrace([]*TraceSpan{s, other}, nil, map[string]string{"t1": "pod-1"})

	require.Len(t, ht.Calls, 1)
	require.Equal(t, "pod-1", ht.Calls[0].PodName)
	require.Len(t, ht.Orphans, 1)
}

func TestHookTraceAttributesThroughAncestors(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 300, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_uid_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 290, 291),
		span("a3", "", "runtime.v1.RuntimeService/CreateContainer", 400, 500,
			"sandbox.id", "sb1", "container.id", "c1"),
		span("a4", "a3", "client.NewContainer", 410, 490, "container.id", "c1"),
		hookSpan("a5", "a4", "CreateContainer", 420, 430),
	}

	ht := NewHookTrace(spans, nil, nil)

	require.Len(t, ht.Calls, 2)
	require.Empty(t, ht.Orphans)
	require.Len(t, ht.Events, 2)

	require.Equal(t, RunPodSandbox, ht.Events[0].Type)
	require.Equal(t, "pod-1", ht.Events[0].PodName)
	require.Equal(t, "sb1", ht.Events[0].PodID)
	require.Equal(t, t0.Add(290*time.Millisecond), ht.Events[0].At)

	require.Equal(t, CreateContainer, ht.Events[1].Type)
	require.Equal(t, "pod-1", ht.Events[1].PodName)
	require.Equal(t, "c1", ht.Events[1].CtrID)
}

func TestHookTraceDoesNotTakeAPodHooksSandboxContainerForAContainer(t *testing.T) {
	// A sandbox is a container to the runtime, so a pod hook's ancestors can
	// carry a container.id that is the sandbox's own.
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 300, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_uid_0"),
		span("a2", "a1", "client.NewContainer", 10, 290, "container.id", "sb1"),
		hookSpan("a3", "a2", "RunPodSandbox", 290, 291),
	}

	ht := NewHookTrace(spans, nil, nil)

	require.Len(t, ht.Calls, 1)
	require.Empty(t, ht.Calls[0].CtrID)
}

func TestHookTraceKeepsOrphansApart(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 300, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_uid_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 290, 291),
		hookSpan("b1", "", "StopContainer", 290, 291),
		hookSpan("b2", "", "RemoveContainer", 500, 520),
	}

	ht := NewHookTrace(spans, nil, nil)

	require.Len(t, ht.Calls, 1)
	require.Len(t, ht.Orphans, 2)
	require.Equal(t, StopContainer, ht.Orphans[0].Hook)
	require.Len(t, ht.Events, 1, "an orphan has no pod to be an event of")

	stats := ht.HookStats([]string{"pod-1"}, hookOrder)
	require.Len(t, stats, 2)
	require.Equal(t, StopContainer, stats[1].Hook, "RemoveContainer is outside the pods' window")
	require.Equal(t, 1, stats[1].Orphan.N)
	require.Equal(t, 0, stats[1].Call.N)
}

func TestHookTraceIgnoresOtherSpans(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", nriPluginService+"/Configure", 0, 1, "rpc.service", nriPluginService, "rpc.method", "Configure"),
		span("a2", "", nriPluginService+"/Synchronize", 0, 1, "rpc.service", nriPluginService, "rpc.method", "Synchronize"),
		span("a3", "", "runtime.v1.RuntimeService/Version", 0, 1),
	}

	ht := NewHookTrace(spans, nil, nil)

	require.Empty(t, ht.Calls)
	require.Empty(t, ht.Orphans)
}

func TestHookTraceUsesKnownNamesAndSandboxOfContainer(t *testing.T) {
	// No span names the sandbox, but the caller does, and the container's
	// sandbox is learned from another span that has both.
	spans := []*TraceSpan{
		span("a1", "", "runtime.v1.RuntimeService/CreateContainer", 0, 100,
			"sandbox.id", "sb1", "container.id", "c1"),
		hookSpan("b1", "", "PostCreateContainer", 50, 60),
		span("c1", "", "x", 0, 1, "container.id", "c1"),
		hookSpan("c2", "c1", "PostStartContainer", 70, 80),
	}

	ht := NewHookTrace(spans, map[string]string{"sb1": "pod-1"}, nil)

	require.Len(t, ht.Calls, 1)
	require.Equal(t, "pod-1", ht.Calls[0].PodName)
	require.Equal(t, PostStartContainer, ht.Calls[0].Hook)
	require.Len(t, ht.Orphans, 1)
}

func TestHookTraceOrdersPluginsAndTakesTheFirstCallAsTheEvent(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 400, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_uid_0"),
		hookSpan("a3", "a1", "RunPodSandbox", 160, 260),
		hookSpan("a2", "a1", "RunPodSandbox", 100, 150),
		hookSpan("a4", "a1", "RunPodSandbox", 270, 390),
	}

	ht := NewHookTrace(spans, nil, nil)

	require.Len(t, ht.Calls, 3)
	require.Equal(t, []int{0, 1, 2}, []int{ht.Calls[0].Seq, ht.Calls[1].Seq, ht.Calls[2].Seq})
	require.Equal(t, 100*time.Millisecond, ht.Calls[1].Duration())

	require.Len(t, ht.Events, 1)
	require.Equal(t, t0.Add(100*time.Millisecond), ht.Events[0].At)

	st := ht.HookStats([]string{"pod-1"}, hookOrder)
	require.Len(t, st, 1)
	require.Equal(t, 3, st[0].Call.N)
	require.Equal(t, 1, st[0].Chain.N)
	require.Equal(t, 290*time.Millisecond, st[0].Chain.P50, "first start to last end")
}

func TestHookStatsOnlyCountsTheNamedPods(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 10, "sandbox.id", "sb1", "sandbox.name", "mine_ns_u_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 1, 2),
		span("b1", "", "cri.sandbox.run", 0, 10, "sandbox.id", "sb2", "sandbox.name", "other_ns_u_0"),
		hookSpan("b2", "b1", "RunPodSandbox", 1, 9),
	}

	st := NewHookTrace(spans, nil, nil).HookStats([]string{"mine"}, hookOrder)

	require.Len(t, st, 1)
	require.Equal(t, 1, st[0].Call.N)
	require.Equal(t, time.Millisecond, st[0].Call.Max)
}

func TestTimelineFromTraceEvents(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 300, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_uid_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 290, 291),
		span("a3", "", "runtime.v1.RuntimeService/CreateContainer", 400, 500,
			"sandbox.id", "sb1", "container.id", "c1"),
		hookSpan("a4", "a3", "CreateContainer", 420, 430),
		hookSpan("a5", "a3", "PostCreateContainer", 480, 481),
		span("a6", "", "runtime.v1.RuntimeService/StartContainer", 600, 700,
			"sandbox.id", "sb1", "container.id", "c1"),
		hookSpan("a7", "a6", "StartContainer", 610, 611),
		hookSpan("a8", "a6", "PostStartContainer", 690, 691),
	}

	tl := NewTimeline(NewHookTrace(spans, nil, nil).Events, []string{"pod-1"}, StartupPhase(), "")
	rep := NewScaleReport(1, 1, tl)

	require.Equal(t, 1, rep.Started)
	require.Equal(t, 1, rep.Finished)
	require.Equal(t, 400*time.Millisecond, rep.Burst, "RunPodSandbox at 290ms to PostStartContainer at 690ms")
}

func exportRequest() *coltrace.ExportTraceServiceRequest {
	str := func(k, v string) *common.KeyValue {
		return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
	}

	return &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{str("service.name", "rt")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId:           []byte{1, 2},
			SpanId:            []byte{3},
			ParentSpanId:      []byte{4},
			Name:              "n",
			StartTimeUnixNano: 1_000_000_000,
			EndTimeUnixNano:   2_000_000_000,
			Attributes: []*common.KeyValue{
				str("a", "b"),
				{Key: "i", Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 7}}},
			},
		}}}},
	}}}
}

func requireExported(t *testing.T, r *TraceReceiver) {
	t.Helper()

	require.NoError(t, r.WaitSpans(5*time.Second))

	spans := r.Spans()
	require.Len(t, spans, 1)
	require.Equal(t, "0102", spans[0].Trace)
	require.Equal(t, "03", spans[0].ID)
	require.Equal(t, "04", spans[0].Parent)
	require.Equal(t, "rt", spans[0].Service)
	require.Equal(t, "n", spans[0].Name)
	require.Equal(t, time.Second, spans[0].End.Sub(spans[0].Start))
	require.Equal(t, "b", spans[0].Attrs["a"])
	require.Equal(t, "7", spans[0].Attrs["i"])
}

func TestTraceReceiverHTTP(t *testing.T) {
	r, err := NewTraceReceiver(TraceHTTP, "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(r.Close)

	blob, err := proto.Marshal(exportRequest())
	require.NoError(t, err)

	resp, err := http.Post("http://"+r.Addr()+"/v1/traces", "application/x-protobuf",
		strings.NewReader(string(blob)))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	requireExported(t, r)
}

func TestTraceReceiverHTTPRejectsGarbage(t *testing.T) {
	r, err := NewTraceReceiver(TraceHTTP, "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(r.Close)

	resp, err := http.Post("http://"+r.Addr()+"/v1/traces", "application/x-protobuf",
		strings.NewReader("\xff\xff not protobuf"))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Empty(t, r.Spans())
}

func TestTraceReceiverGRPC(t *testing.T) {
	r, err := NewTraceReceiver(TraceGRPC, "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(r.Close)

	conn, err := grpc.NewClient(r.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = coltrace.NewTraceServiceClient(conn).Export(ctx, exportRequest())
	require.NoError(t, err)

	requireExported(t, r)
}

func TestTraceReceiverRejectsAnUnknownProtocol(t *testing.T) {
	_, err := NewTraceReceiver("carrier-pigeon", "127.0.0.1:0")
	require.Error(t, err)
}

func TestWaitSpansTimesOut(t *testing.T) {
	r, err := NewTraceReceiver(TraceHTTP, "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(r.Close)

	require.Error(t, r.WaitSpans(200*time.Millisecond))
}

func TestHookStatsOrphanWindowSpansAllOfThePodsHooks(t *testing.T) {
	// The StopContainer root span starts before the pod's first teardown hook
	// that can be attributed, but after its startup, so it is inside the
	// window of the pod's calls even though not of its teardown calls.
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 10, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_u_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 1, 2),
		hookSpan("a5", "", "StopContainer", 21, 22),
		span("a3", "", "runtime.v1.RuntimeService/StopPodSandbox", 20, 30, "sandbox.id", "sb1"),
		hookSpan("a4", "a3", "StopPodSandbox", 25, 26),
		hookSpan("a6", "", "StopContainer", 90, 91),
	}

	st := NewHookTrace(spans, nil, nil).HookStats([]string{"pod-1"}, TeardownPhase().Stages)

	require.Len(t, st, 2)
	require.Equal(t, StopContainer, st[0].Hook)
	require.Equal(t, 1, st[0].Orphan.N, "the one after the pod's last call is outside the window")
}

func TestHookStatsOnlyCountsTheGivenHooks(t *testing.T) {
	spans := []*TraceSpan{
		span("a1", "", "cri.sandbox.run", 0, 10, "sandbox.id", "sb1", "sandbox.name", "pod-1_ns_u_0"),
		hookSpan("a2", "a1", "RunPodSandbox", 1, 2),
		span("a3", "", "runtime.v1.RuntimeService/StopPodSandbox", 20, 30, "sandbox.id", "sb1"),
		hookSpan("a4", "a3", "StopPodSandbox", 21, 22),
		hookSpan("a5", "", "StopContainer", 21, 22),
	}

	st := NewHookTrace(spans, nil, nil).HookStats([]string{"pod-1"}, TeardownPhase().Stages)

	require.Len(t, st, 2)
	require.Equal(t, StopContainer, st[0].Hook)
	require.Equal(t, StopPodSandbox, st[1].Hook)
}
