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
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
)

// recordingLogger collects the messages logged at each level.
type recordingLogger struct {
	info []string
	warn []string
}

func (l *recordingLogger) Debugf(_ context.Context, _ string, _ ...interface{}) {}
func (l *recordingLogger) Errorf(_ context.Context, _ string, _ ...interface{}) {}

func (l *recordingLogger) Infof(_ context.Context, format string, args ...interface{}) {
	l.info = append(l.info, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Warnf(_ context.Context, format string, args ...interface{}) {
	l.warn = append(l.warn, fmt.Sprintf(format, args...))
}

// joined returns every warning as one string, for substring assertions.
func (l *recordingLogger) joined() string {
	return strings.Join(l.warn, "\n")
}

func TestOrderedEventsOnUnaryIsReported(t *testing.T) {
	// A plugin that asked for ordered delivery and landed on unary calls got
	// a different contract from the one it requested, not a slower
	// transport, so the option has to be named rather than left for the
	// reader to infer from "using unary calls".
	t.Setenv(streamingEnv, "0")

	logger := &recordingLogger{}
	stub := &stub{logger: logger, orderedEvents: true}

	stub.startStream(context.Background(), nil)

	if stub.streaming {
		t.Fatal("the stub streams with " + streamingEnv + "=0")
	}
	if !strings.Contains(logger.joined(), "WithOrderedEvents") {
		t.Errorf("no warning names WithOrderedEvents; warnings were %q", logger.joined())
	}
}

func TestOrderedEventsIsReportedOnEveryFallback(t *testing.T) {
	// The NRI_STREAMING exit is the one a test reaches cheaply, and it is not
	// the one production meets: against a runtime that takes no stream the
	// refusal comes from the stream call or from the first message. Drive one
	// of those too, so the warning is pinned to the fallback rather than to a
	// single branch.
	t.Setenv(streamingEnv, "1")

	logger := &recordingLogger{}
	stub := &stub{logger: logger, orderedEvents: true}

	here, there := net.Pipe()
	require.NoError(t, there.Close())
	require.NoError(t, here.Close())

	stub.startStream(context.Background(), ttrpc.NewClient(here))

	if stub.streaming {
		t.Fatal("the stub streams over a closed connection")
	}
	if !strings.Contains(logger.joined(), "WithOrderedEvents") {
		t.Errorf("no warning names WithOrderedEvents; warnings were %q", logger.joined())
	}
}

func TestUnorderedEventsOnUnaryIsNotReported(t *testing.T) {
	// The warning is for a plugin that asked. A plugin that did not must not
	// be told an option it never set was dropped.
	t.Setenv(streamingEnv, "0")

	logger := &recordingLogger{}
	stub := &stub{logger: logger}

	stub.startStream(context.Background(), nil)

	if strings.Contains(logger.joined(), "WithOrderedEvents") {
		t.Errorf("a plugin that did not ask is warned: %q", logger.joined())
	}
}
