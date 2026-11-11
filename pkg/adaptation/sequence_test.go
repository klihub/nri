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

import (
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateRunsInSequenceOrder(t *testing.T) {
	// Arriving out of order is the normal case: requests take their number
	// at entry and then reach a consumer in whatever order their goroutines
	// are scheduled. The gate is what turns that back into the number order.
	const n = 16

	var (
		g     = newGate(1)
		mu    sync.Mutex
		order []seqNo
		wg    sync.WaitGroup
	)

	seqs := make([]seqNo, 0, n)
	for i := 1; i <= n; i++ {
		seqs = append(seqs, seqNo(i))
	}
	rand.Shuffle(len(seqs), func(i, j int) { seqs[i], seqs[j] = seqs[j], seqs[i] })

	for _, s := range seqs {
		wg.Add(1)

		go func(s seqNo) {
			defer wg.Done()

			g.run(s, func() {
				mu.Lock()
				order = append(order, s)
				mu.Unlock()
			})
		}(s)
	}

	wg.Wait()

	require.Len(t, order, n)
	for i, s := range order {
		require.Equal(t, seqNo(i+1), s, "work ran out of order at index %d", i)
	}
}

func TestGateSkipsAHole(t *testing.T) {
	// A request that will never reach this consumer -- a plugin it is not
	// subscribed to, or one it aborted before -- has to be announced, or the
	// gate waits on a number that is never coming.
	var (
		g     = newGate(1)
		mu    sync.Mutex
		order []seqNo
		done  = make(chan struct{})
	)

	record := func(s seqNo) func() {
		return func() {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
		}
	}

	g.run(1, record(1))

	go func() {
		g.run(3, record(3))
		close(done)
	}()

	g.skip(2)

	<-done

	require.Equal(t, []seqNo{1, 3}, order)
}

func TestGateAbandonDoesNotStall(t *testing.T) {
	// A caller whose context ended before its turn must not leave the gate
	// waiting on it for good.
	var (
		g    = newGate(1)
		done = make(chan struct{})
	)

	g.run(1, func() {})

	go func() {
		g.run(3, func() {})
		close(done)
	}()

	g.abandon(2)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the number after an abandoned one never got its turn")
	}
}

func TestGateCloseReleasesParked(t *testing.T) {
	// A plugin that dies with requests parked in its writer must not strand
	// their callers.
	var (
		g    = newGate(1)
		done = make(chan struct{})
		ran  bool
	)

	go func() {
		g.run(2, func() { ran = true })
		close(done)
	}()

	g.close(errors.New("stream gone"))

	<-done

	require.False(t, ran, "work ran after the gate was closed")
}

func TestGateRunAfterCloseReturns(t *testing.T) {
	g := newGate(1)
	g.close(errors.New("stream gone"))

	ran := false
	g.run(1, func() { ran = true })

	require.False(t, ran, "work ran on a closed gate")
}

func TestSequencerAllocatesDistinctNumbers(t *testing.T) {
	const n = 100

	var (
		s    sequencer
		mu   sync.Mutex
		seen = map[seqNo]bool{}
		wg   sync.WaitGroup
	)

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			got := s.take()

			mu.Lock()
			seen[got] = true
			mu.Unlock()
		}()
	}

	wg.Wait()

	require.Len(t, seen, n, "take() handed out a number twice")
	for i := 1; i <= n; i++ {
		require.True(t, seen[seqNo(i)], "number %d was never handed out", i)
	}
}

func TestSequencerUpcomingDoesNotAllocate(t *testing.T) {
	// A stream created mid-burst starts its gate at the next number, so it
	// never waits for requests that predate it. Peeking must not consume.
	var s sequencer

	require.Equal(t, seqNo(1), s.upcoming())
	require.Equal(t, seqNo(1), s.upcoming())
	require.Equal(t, seqNo(1), s.take())
	require.Equal(t, seqNo(2), s.upcoming())
}

func TestListingAPluginArmsItsStream(t *testing.T) {
	// Every plugin is listed through addPlugins, so arming there is what
	// makes the guarantee hold for a pre-installed plugin as well as for one
	// that connects later. An unarmed gate is not a stalled plugin, which is
	// why this needs its own test: pluginStream.call skips the gate when the
	// gate is not armed, so the plugin takes requests in no order at all
	// while still reporting that it streams.
	r := &Adaptation{metrics: &noopMetrics{}}

	for i := 0; i < 3; i++ {
		r.seq.take()
	}

	p := &plugin{impl: &pluginType{streamingImpl: newPluginStream(nil, nil)}}

	r.addPlugins(p)

	require.True(t, p.impl.streamingImpl.order.isArmed(),
		"a plugin listed as live should have its stream armed")

	// The numbers taken above predate this plugin, so its gate owes them
	// nothing and the next one runs without waiting.
	done := make(chan struct{})
	go func() {
		p.impl.streamingImpl.order.run(4, func() {})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream waited for a number issued before its plugin was listed")
	}
}
