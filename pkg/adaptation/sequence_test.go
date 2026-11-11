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
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newGate returns a gate whose first number is first.
//
// A gate in production is a field of what orders by it, armed with the
// number its plugin was listed at or left to start at the sequencer's
// first; a test says which number it starts at instead.
func newGate(first seqNo) *gate {
	g := &gate{}
	g.init(first)

	return g
}

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

func TestGateSkipsANumberNobodyWaitsFor(t *testing.T) {
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

	g.skip(2)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the number after an abandoned one never got its turn")
	}
}

func TestGateServesEachNumberOnce(t *testing.T) {
	// A walk announces every number it reaches with g.mu released, and while
	// it is there that number looks like one whose turn has come and which
	// nothing has handed on. A skip of it then would announce it a second
	// time and hand the order on from under the walk: two walks from one
	// position, and the one that lags hands the order to a number the other
	// has already passed, which is want going backwards.
	//
	// A second skip of one number is ordinary rather than hypothetical:
	// every request announces its number once it is done with it, whether it
	// had a turn or not.
	var (
		g        = newGate(1)
		mu       sync.Mutex
		order    []seqNo
		ran      int
		released bool
		start    = make(chan struct{})
		ack      = make(chan struct{})
		wg       sync.WaitGroup
	)

	g.onServed(func(s seqNo) {
		mu.Lock()
		order = append(order, s)
		release := s == 2 && !released
		released = released || release
		mu.Unlock()

		if !release {
			return
		}

		// The second skip of 2 runs here, which is the gate serving that
		// number with its lock released.
		close(start)
		<-ack
	})

	// Parked, so 2 is a number with work of its own: the walk announces it
	// and only then lets that work go.
	wg.Add(1)

	go func() {
		defer wg.Done()

		g.run(2, func() {
			mu.Lock()
			ran++
			mu.Unlock()
		})
	}()

	require.Eventually(t, func() bool { return g.parked(2) }, 10*time.Second,
		time.Millisecond, "nothing ever waited for the second number")

	wg.Add(1)

	go func() {
		defer wg.Done()

		<-start
		g.skip(2)
		close(ack)
	}()

	g.skip(1)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []seqNo{1, 2, 3}, order, "the gate served a number twice")
	require.Equal(t, 1, ran, "the work of one number ran more than once")
}

func TestGateNeverTakesTheOrderBackwards(t *testing.T) {
	// A number is announced with g.mu released, and the walk that is on it
	// comes back to hand it on. A turn taken at that number in between
	// leaves the walk stale: it then hands the order on from a number the
	// turn's own walk has already passed, so the number the gate wants moves
	// backwards and the numbers in between are served a second time. One
	// number is one walk's to hand on.
	var (
		g      = newGate(1)
		mu     sync.Mutex
		order  []seqNo
		wanted []seqNo
		taken  bool
		inWork = make(chan struct{})
		walked = make(chan struct{})
		wg     sync.WaitGroup
	)

	// Holes at the third and the fourth number, so the walk that leaves the
	// second behind reaches the fifth -- above where a turn taken at the
	// second leaves the order.
	g.skip(3)
	g.skip(4)

	g.onServed(func(s seqNo) {
		want := g.wanted()

		mu.Lock()
		order = append(order, s)
		wanted = append(wanted, want)
		take := s == 2 && !taken
		taken = taken || take
		mu.Unlock()

		if !take {
			return
		}

		// Both of these fall while the gate is serving the second number:
		// the skip is left as a hole for the walk, and the turn is one the
		// walk has yet to hand on.
		g.skip(2)

		wg.Add(1)

		go func() {
			defer wg.Done()

			g.run(2, func() {
				close(inWork)
				<-walked
			})
		}()

		// The turn either waits for the walk to hand it on, or is taken
		// from under the walk.
		require.Eventually(t, func() bool {
			select {
			case <-inWork:
				return true
			default:
			}

			return g.parked(2)
		}, 10*time.Second, time.Millisecond,
			"nothing ever asked for the second number")
	})

	g.skip(1)

	close(walked)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	for i := 1; i < len(wanted); i++ {
		require.LessOrEqual(t, wanted[i-1], wanted[i],
			"the number the gate wants went backwards")
	}

	require.Equal(t, []seqNo{1, 2, 3, 4, 5}, order, "the gate served a number twice")
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

	g.close()

	<-done

	require.False(t, ran, "work ran after the gate was closed")
}

func TestGateRunAfterCloseReturns(t *testing.T) {
	g := newGate(1)
	g.close()

	ran := false
	g.run(1, func() { ran = true })

	require.False(t, ran, "work ran on a closed gate")
}

func TestGateHandsOnANumberSkippedWhileServed(t *testing.T) {
	// The number a walk is in the middle of is left for the walk to hand
	// on rather than served again, so the skip of it has to be remembered:
	// the request announcing a number it will never reach has no turn of
	// its own to be found waiting in, and announces that number once. A
	// number nothing announces again is one the order stops dead at.
	var (
		g        = newGate(1)
		mu       sync.Mutex
		order    []seqNo
		released bool
		start    = make(chan struct{})
		ack      = make(chan struct{})
		third    = make(chan struct{})
		wg       sync.WaitGroup
	)

	g.onServed(func(s seqNo) {
		mu.Lock()
		order = append(order, s)
		release := s == 2 && !released
		released = released || release
		mu.Unlock()

		if !release {
			return
		}

		close(start)
		<-ack
	})

	// Parked at 3 rather than at 2, so 2 is a number the walk reaches with
	// nothing waiting at it, which is what the skip of it races.
	wg.Add(1)

	go func() {
		defer wg.Done()

		g.run(3, func() { close(third) })
	}()

	require.Eventually(t, func() bool { return g.parked(3) }, 10*time.Second,
		time.Millisecond, "nothing ever waited for the third number")

	wg.Add(1)

	go func() {
		defer wg.Done()

		<-start
		g.skip(2)
		close(ack)
	}()

	g.skip(1)

	select {
	case <-third:
	case <-time.After(10 * time.Second):
		t.Fatal("the order stopped at a number skipped while it was served")
	}

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	// The fourth number is the one the third's own work hands the order to,
	// and nothing waits there.
	require.Equal(t, []seqNo{1, 2, 3, 4}, order,
		"the gate served a number twice")
}

// wanted returns the number whose turn it is.
func (g *gate) wanted() seqNo {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.want
}

// parked reports whether anything is waiting for seq's turn.
func (g *gate) parked(seq seqNo) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	_, ok := g.waiter[seq]

	return ok
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

	p := &plugin{}
	p.impl = &pluginType{streamingImpl: newPluginStream(p, nil)}

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
