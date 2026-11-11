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

import "sync"

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
	closed bool
	waiter map[seqNo]chan struct{}
	hole   map[seqNo]bool
}

// newGate returns a gate whose first number is first.
func newGate(first seqNo) *gate {
	return &gate{
		want:   first,
		waiter: map[seqNo]chan struct{}{},
		hole:   map[seqNo]bool{},
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
