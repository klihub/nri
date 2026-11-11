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
//
// A number can carry work that is not its request's: an apply attached
// below it, which runs when the order reaches that number. Nothing a
// caller can see says when that is -- the order arrives at a number
// through holes the gate walks on its own, and at a number whose caller is
// not there to be let go -- so a gate announces every number it reaches
// and the applier runs what is attached to it then.

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

// placeNext runs place for the number take would hand out next, and hands
// out no number while it runs.
func (s *sequencer) placeNext(place func(seqNo)) {
	// For an apply attached below a number nobody has taken yet. Reading
	// the number and attaching below it have to be one step: a request
	// numbered in between could reach its apply first, and the place would
	// then be one the order had passed.
	s.mu.Lock()
	defer s.mu.Unlock()

	place(s.last + 1)
}

// gate runs work in the order of the numbers it is given.
type gate struct {
	mu     sync.Mutex
	want   seqNo
	armed  bool
	closed bool
	waiter map[seqNo]chan struct{}
	hole   map[seqNo]bool
	done   chan struct{}

	// serving is the number the order is in the middle of reaching, and
	// zero when it is in the middle of none. It is announced and its work
	// handed on with g.mu released, so without this it would look like a
	// number whose turn has come and which nothing has handed on: a skip of
	// it would serve it a second time, and a wait for it would take a turn
	// the walk is about to hand over. Both leave two walks in one order.
	// The walk that set it is the one that clears it.
	serving seqNo

	// served announces a number the order has reached, which is the rule
	// an attachment runs by: it runs when the order reaches its number,
	// however the order got there. The applier sets the hook, and a nil
	// hook is a no-op.
	//
	// Called with g.mu released, and before the number's own work is let
	// go, so the attachment has the head of the number to itself.
	served func(seqNo)
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
		g.done = make(chan struct{})
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

// onServed sets the hook that announces the numbers this gate serves.
func (g *gate) onServed(served func(seqNo)) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.init(1)

	g.served = served
}

// position reports where seq stands in the order: head says it is seq's
// turn, and gone says the order has already passed it.
func (g *gate) position(seq seqNo) (head, gone bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// For an apply attached below seq, which has to be run by whoever
	// brings the order to seq and has nobody to do that once the order is
	// there.
	g.init(1)

	return seq == g.want && !g.closed, seq < g.want
}

// ended returns a channel closed once this gate is.
func (g *gate) ended() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()

	// For a waiter with no turn of its own to be woken from: an apply
	// attached below a number runs when the order reaches that number, and
	// a closed gate is one where it never will.
	g.init(1)

	return g.done
}

// skip serves seq without running anything, for a request that will never
// reach this consumer, or whose caller gave up before its turn.
func (g *gate) skip(seq seqNo) {
	g.mu.Lock()
	g.init(1)

	// A number already announced is left alone. Every request announces its
	// number once it is done with it whether it had a turn or not, so the
	// same number arrives here twice, and the second time can fall while
	// the order is in the middle of serving it.
	if g.closed || seq < g.want || g.hole[seq] {
		g.mu.Unlock()
		return
	}

	// A number the order is in the middle of reaching is left as a hole
	// rather than served here, because the walk that is on it may be about
	// to leave it behind as a number with work of its own: the request
	// announcing it has no turn of its own to be found waiting in.
	if seq != g.want || seq == g.serving {
		g.hole[seq] = true
		g.mu.Unlock()

		return
	}

	g.serving = seq
	served := g.served
	g.mu.Unlock()

	if served != nil {
		served(seq)
	}

	g.advance(seq)
}

// close releases everything parked and makes later calls return without
// running.
func (g *gate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.init(1)

	if !g.closed {
		g.closed = true
		close(g.done)
	}

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

	// The number the order is in the middle of reaching is the walk's to
	// hand on, so a caller that finds its own turn being reached waits for
	// the walk to hand it over rather than taking it here. A turn taken from
	// under a walk leaves two walks in the order, and the one that lags
	// hands the order back to a number the other has passed.
	if seq == g.want && seq != g.serving {
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
	// Every number the order reaches is announced as served, with g.mu
	// released and before that number's own work is let go, so an apply
	// attached below a number runs at the head of it.
	for next := seq + 1; ; next++ {
		g.mu.Lock()
		g.init(1)

		// One critical section per hole walked, with every part of
		// leaving a number behind in it: the hole it was, the number want
		// moves to, and the number this walk is now in the middle of.
		// Between any two of them a number would look neither announced
		// nor handed on, and a skip of it would serve it a second time.
		delete(g.hole, next-1)
		g.want = next
		g.serving = next
		hole := g.hole[next]
		served := g.served
		g.mu.Unlock()

		if served != nil {
			served(next)
		}

		if hole {
			continue
		}

		g.mu.Lock()

		ch, parked := g.waiter[next]

		// A skip of this number that arrived while it was being served is
		// a hole left here for the walk rather than a second serving of
		// it, and nothing waits at a number nobody will run.
		if !parked && g.hole[next] {
			g.mu.Unlock()

			continue
		}

		if parked {
			delete(g.waiter, next)
			close(ch)
		}

		g.serving = 0
		g.mu.Unlock()

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

// placedUpdateCtxKey carries an update's place from the stream reader that
// took it to the apply that honours it.
//
// Request-scoped like a request's own number, and for the same reason: it
// passes through a plugin method and a stream answer that have no use for
// it.
type placedUpdateCtxKey struct{}

// withPlacedUpdate returns ctx carrying placed.
func withPlacedUpdate(ctx context.Context, placed *placedUpdate) context.Context {
	return context.WithValue(ctx, placedUpdateCtxKey{}, placed)
}

// placedUpdateFrom returns the placed update ctx carries, and whether it
// carried one.
func placedUpdateFrom(ctx context.Context) (*placedUpdate, bool) {
	// Nothing carried means the update has no place in the order and
	// applies as it arrives. A plugin on unary calls gets that: an
	// out-of-band call carries no position, so there is no order to read
	// off it.
	placed, ok := ctx.Value(placedUpdateCtxKey{}).(*placedUpdate)

	return placed, ok
}
