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
	"context"
	"sync"
)

// The order NRI does things in, as one number per request and a gate for
// each consumer of that order.
//
// A request takes a number when it enters NRI. That number is the whole of
// the ordering: a plugin's writer uses it to decide what goes on the wire
// next, and the applier uses it to decide whose answer is put into practice
// next. One number with two consumers rather than a number each, because
// two numbers can disagree about the same pair of requests.
//
// A consumer never sees every number. A plugin is not subscribed to every
// event, and a request that fails partway reaches only some plugins. So a
// request that will not reach a consumer announces a hole to it. A gate
// that is only ever told about the numbers it will serve waits for the rest
// for good, which is the failure this design has to avoid rather than
// discover.

// seqNo is a request's place in the order.
type seqNo uint64

// sequencer hands out the numbers.
type sequencer struct {
	mu   sync.Mutex
	last seqNo
}

// take returns the next number, which is this request's own.
func (s *sequencer) take() seqNo {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.last++

	return s.last
}

// upcoming returns the number take would hand out next, without taking it.
func (s *sequencer) upcoming() seqNo {
	// A gate armed at this number waits for nothing that came before it.
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.last + 1
}

// gate runs work in the order of the numbers it is given.
type gate struct {
	mu     sync.Mutex
	want   seqNo
	armed  bool
	closed bool
	waiter map[seqNo]chan struct{}
	hole   map[seqNo]bool
}

// newGate returns a gate whose first number is first.
func newGate(first seqNo) *gate {
	g := &gate{}
	g.init(first)

	return g
}

// arm sets the gate's first number, discarding anything it was told
// before.
func (g *gate) arm(first seqNo) {
	// A plugin's stream opens when it connects, but the plugin only joins
	// the live list once it has been configured and synchronized. A request
	// numbered in between reaches neither: not the plugin, which is not yet
	// listed, and so not the sweep that would announce the hole. Arming
	// when the plugin joins, under the same lock that lists it, closes that
	// window -- every number after it either reaches the plugin or is
	// announced, and every number before it is one this gate ignores.
	g.mu.Lock()
	defer g.mu.Unlock()

	g.init(first)
	g.want = first
	g.armed = true

	for seq, ch := range g.waiter {
		if seq < first {
			close(ch)
			delete(g.waiter, seq)
		}
	}
}

// isArmed reports whether this gate orders anything yet.
func (g *gate) isArmed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.armed
}

// init prepares a gate whose first number is first.
func (g *gate) init(first seqNo) {
	// Taken by every entry point, so that a zero gate works: an Adaptation
	// built as a literal rather than by New would otherwise take requests
	// against a nil map, and the order is not something a caller should
	// have to remember to switch on.
	if g.waiter == nil {
		g.waiter = map[seqNo]chan struct{}{}
		g.hole = map[seqNo]bool{}
	}

	if g.want == 0 {
		g.want = first
	}
}

// run calls work once every number before seq has been served, and returns
// once it has.
func (g *gate) run(seq seqNo, work func()) {
	// A closed gate has no order left to run in, so work is not called.
	if !g.await(seq) {
		return
	}

	work()

	g.advance(seq)
}

// skip serves seq without running anything, for a request that will never
// reach this consumer.
func (g *gate) skip(seq seqNo) {
	g.mu.Lock()
	g.init(1)

	if g.closed || seq < g.want {
		g.mu.Unlock()
		return
	}

	if seq != g.want {
		g.hole[seq] = true
		g.mu.Unlock()

		return
	}

	g.mu.Unlock()
	g.advance(seq)
}

// abandon serves seq for a caller that gave up before its turn, so the
// order does not wait on it.
func (g *gate) abandon(seq seqNo) {
	g.skip(seq)
}

// close releases everything parked and makes later calls return without
// running.
func (g *gate) close(error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.init(1)

	g.closed = true

	for seq, ch := range g.waiter {
		close(ch)
		delete(g.waiter, seq)
	}
}

// await reports whether it is seq's turn to run, blocking until it is.
//
// False means the gate closed while waiting, so the caller runs nothing.
func (g *gate) await(seq seqNo) bool {
	g.mu.Lock()
	g.init(1)

	if g.closed {
		g.mu.Unlock()
		return false
	}

	if seq == g.want {
		g.mu.Unlock()
		return true
	}

	ch := make(chan struct{})
	g.waiter[seq] = ch
	g.mu.Unlock()

	<-ch

	g.mu.Lock()
	defer g.mu.Unlock()

	return !g.closed
}

// advance hands the order to seq's successor, and to as many holes as
// follow it.
func (g *gate) advance(seq seqNo) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.init(1)

	g.want = seq + 1

	for {
		if g.hole[g.want] {
			delete(g.hole, g.want)
			g.want++

			continue
		}

		if ch, ok := g.waiter[g.want]; ok {
			delete(g.waiter, g.want)
			close(ch)
		}

		return
	}
}

// seqCtxKey carries a request's number to the consumers that need it.
//
// Request-scoped and nothing else: the alternative is a parameter on all
// sixteen pluginType methods and the plugin wrappers under them, for a
// value no implementation of them reads.
type seqCtxKey struct{}

// withSeq returns ctx carrying seq.
func withSeq(ctx context.Context, seq seqNo) context.Context {
	return context.WithValue(ctx, seqCtxKey{}, seq)
}

// withoutSeq returns ctx carrying no number.
func withoutSeq(ctx context.Context) context.Context {
	// For the calls a request makes that are not the request: a validator
	// is asked to approve an adjustment for the request it was just
	// dispatched, and the two cannot share a turn. Validators only approve
	// or refuse, so they are not a decision to be ordered at all.
	return context.WithValue(ctx, seqCtxKey{}, nil)
}

// seqFrom returns the number ctx carries, and whether it carried one.
func seqFrom(ctx context.Context) (seqNo, bool) {
	seq, ok := ctx.Value(seqCtxKey{}).(seqNo)

	return seq, ok
}
