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
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// A minimal OTLP trace receiver, so a runtime's own OpenTelemetry spans can be
// the instrument of a scalability run instead of a recorder plugin.
//
// It stores what it is sent and nothing else: no batching, no sampling, no
// forwarding. The runtime is pointed at it by the runtime's own configuration,
// which this suite does not touch.

// TraceProtocol selects the OTLP transport a TraceReceiver speaks.
const (
	// TraceHTTP is OTLP over HTTP with protobuf bodies. containerd's default.
	TraceHTTP = "http"
	// TraceGRPC is OTLP over gRPC. CRI-O's only transport.
	TraceGRPC = "grpc"
)

// TraceSpan is one received span, flattened to what the analysis uses.
type TraceSpan struct {
	// Trace, ID and Parent are the hex ids. Parent is empty for a root.
	Trace, ID, Parent string
	// Service is the exporting process's service.name.
	Service string
	// Name is the span's name.
	Name string
	// Start and End bracket the span on the exporter's clock.
	Start, End time.Time
	// Attrs holds the span's attributes rendered as strings.
	Attrs map[string]string
}

// TraceReceiver collects spans exported to it.
type TraceReceiver struct {
	mu    sync.Mutex
	spans []*TraceSpan

	ln   net.Listener
	http *http.Server
	grpc *grpc.Server
	done chan struct{}
}

// NewTraceReceiver listens on addr with the given protocol and starts
// receiving. Close it when done.
func NewTraceReceiver(protocol, addr string) (*TraceReceiver, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("trace receiver cannot listen on %s: %w", addr, err)
	}

	r := &TraceReceiver{ln: ln, done: make(chan struct{})}

	switch protocol {
	case TraceHTTP:
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/traces", r.serveHTTP)
		r.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

		go func() {
			defer close(r.done)
			_ = r.http.Serve(ln)
		}()
	case TraceGRPC:
		r.grpc = grpc.NewServer()
		coltrace.RegisterTraceServiceServer(r.grpc, grpcTraces{r: r})

		go func() {
			defer close(r.done)
			_ = r.grpc.Serve(ln)
		}()
	default:
		_ = ln.Close()

		return nil, fmt.Errorf("unknown trace protocol %q: use %q or %q",
			protocol, TraceHTTP, TraceGRPC)
	}

	return r, nil
}

// Addr returns the address being listened on.
func (r *TraceReceiver) Addr() string { return r.ln.Addr().String() }

// Close stops the receiver.
func (r *TraceReceiver) Close() {
	switch {
	case r.http != nil:
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_ = r.http.Shutdown(ctx)
	case r.grpc != nil:
		r.grpc.Stop()
	}

	<-r.done
}

// Spans returns a snapshot of everything received so far.
func (r *TraceReceiver) Spans() []*TraceSpan {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*TraceSpan, len(r.spans))
	copy(out, r.spans)

	return out
}

func (r *TraceReceiver) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)

		return
	}

	var body io.Reader = http.MaxBytesReader(w, req.Body, 64<<20)

	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		defer gz.Close()

		body = gz
	}

	blob, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	msg := &coltrace.ExportTraceServiceRequest{}
	if err := proto.Unmarshal(blob, msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	r.add(msg)

	resp, _ := proto.Marshal(&coltrace.ExportTraceServiceResponse{})

	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(resp)
}

type grpcTraces struct {
	coltrace.UnimplementedTraceServiceServer

	r *TraceReceiver
}

func (g grpcTraces) Export(_ context.Context, req *coltrace.ExportTraceServiceRequest,
) (*coltrace.ExportTraceServiceResponse, error) {
	g.r.add(req)

	return &coltrace.ExportTraceServiceResponse{}, nil
}

func (r *TraceReceiver) add(req *coltrace.ExportTraceServiceRequest) {
	spans := FlattenSpans(req)

	r.mu.Lock()
	r.spans = append(r.spans, spans...)
	r.mu.Unlock()
}

// FlattenSpans converts an export request to TraceSpans.
func FlattenSpans(req *coltrace.ExportTraceServiceRequest) []*TraceSpan {
	var out []*TraceSpan

	for _, rs := range req.GetResourceSpans() {
		service := ""

		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				service = anyString(kv.GetValue())
			}
		}

		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				s := &TraceSpan{
					Trace:   fmt.Sprintf("%x", sp.GetTraceId()),
					ID:      fmt.Sprintf("%x", sp.GetSpanId()),
					Service: service,
					Name:    sp.GetName(),
					Start:   time.Unix(0, int64(sp.GetStartTimeUnixNano())),
					End:     time.Unix(0, int64(sp.GetEndTimeUnixNano())),
					Attrs:   map[string]string{},
				}

				if len(sp.GetParentSpanId()) > 0 {
					s.Parent = fmt.Sprintf("%x", sp.GetParentSpanId())
				}

				for _, kv := range sp.GetAttributes() {
					s.Attrs[kv.GetKey()] = anyString(kv.GetValue())
				}

				out = append(out, s)
			}
		}
	}

	return out
}

func anyString(v *common.AnyValue) string {
	switch x := v.GetValue().(type) {
	case *common.AnyValue_StringValue:
		return x.StringValue
	case *common.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *common.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *common.AnyValue_DoubleValue:
		return strconv.FormatFloat(x.DoubleValue, 'g', -1, 64)
	default:
		return ""
	}
}

// WriteSpans writes everything received so far to path, one JSON span per
// line, so a run's spans can be inspected afterwards.
func (r *TraceReceiver) WriteSpans(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)

	for _, s := range r.Spans() {
		if err := enc.Encode(s); err != nil {
			_ = f.Close()

			return err
		}
	}

	return f.Close()
}

var errNoSpans = errors.New("no spans received")

// WaitSpans waits until at least one span has been received, and returns an
// error if none arrives within the timeout.
func (r *TraceReceiver) WaitSpans(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		if len(r.Spans()) > 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return errNoSpans
		}

		time.Sleep(100 * time.Millisecond)
	}
}
