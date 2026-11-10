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
	"errors"
	"sync"
)

// errOrderPassed is what an update placed below a number the order has
// already served is answered with.
//
// The number it was placed below is one its plugin owed an answer on, and a
// number still owed cannot have been served -- unless its caller gave up on
// it in between, which is this.
var errOrderPassed = errors.New("the apply order passed the place this update was given")

// ErrOrderClosed is what an update still waiting for its turn when the
// order closes is answered with.
//
// The reply to an update is synchronous, so there is a caller to tell. A
// dropped update would have the plugin believe a decision took effect that
// never will. Exported because it is the one failure of an apply that is
// NRI shutting down rather than a plugin or a runtime refusing anything.
var ErrOrderClosed = errors.New("the apply order closed before this update's turn")

// The order plugin decisions are put into practice in.
//
// A request's answer is applied only once its predecessor's has been. That
// is the guarantee -- decisions take effect in the order the requests
// arrived in -- and it holds whatever transport a plugin is on, because it
// serializes no send: a request reaches every plugin it has to reach, and
// only the applying waits.
//
// Only CreateContainer, UpdateContainer and StopContainer can be answered
// with a change, so only those three apply anything. The other ten take a
// number like everyone else and announce a hole, which is what keeps one
// number authoritative for every request rather than one numbering for the
// ordered requests and another for the rest.

// applier runs applies in the order their requests arrived.
//
// Usable as a zero value: the first number is 1, which is where the
// sequencer starts.
type applier struct {
	gate gate
	once sync.Once

	// attached is what runs at the head of a number, before whatever that
	// number applies. mu guards it, and is held for bookkeeping only: no
	// apply runs under it.
	mu       sync.Mutex
	attached map[seqNo][]*attachment
}

// attachment is one apply waiting for the head of a number.
type attachment struct {
	fn   func() error
	done chan struct{}

	// claimed says somebody is running it, and is guarded by applier.mu.
	// err is written by whoever claimed it and read once done is closed.
	claimed bool
	err     error
}

// run applies what is attached, and keeps what the apply returned for the
// caller waiting on it.
func (h *attachment) run() {
	h.err = h.fn()
}

// apply runs fn once every earlier request has applied, and returns what fn
// returned.
func (a *applier) apply(ctx context.Context, seq seqNo, fn func() error) error {
	a.hook()

	// A context that ends before fn's turn abandons the turn, so the order
	// is handed on rather than left waiting, and fn does not run.
	if err := ctx.Err(); err != nil {
		a.gate.skip(seq)

		return err
	}

	var (
		err error
		ran bool
	)

	// The gate takes func() rather than func() error, so that the stream
	// writer can use the same type. The error comes back by capture.
	a.gate.run(seq, func() {
		ran = true

		a.drain(seq)

		err = fn()
	})

	// A turn that never came is not an answer that was put into practice.
	// The order ends with the runtime, and a caller told nothing would
	// take this for an apply that ran.
	if !ran {
		return ErrOrderClosed
	}

	return err
}

// placeAt attaches fn at the head of seq, to run there before whatever seq
// applies, and returns the attachment awaitAt waits on.
//
// Must be called by the stream reader, and the waiting left to awaitAt.
func (a *applier) placeAt(seq seqNo, fn func() error) *attachment {
	a.hook()

	// A plugin's unsolicited update needs no number of its own: it is
	// placed below the lowest number its plugin still owes an answer on,
	// and that place is always one the order has yet to reach, because the
	// apply of a number a plugin owes an answer on needs that answer.
	//
	// The place is sound only while the set of numbers it was read from
	// cannot move, and the stream reader is the only thing that moves that
	// set. Waiting is a second call because a reader waiting for an apply
	// stops reading the replies that apply waits for.
	h := &attachment{fn: fn, done: make(chan struct{})}

	a.attach(seq, h)

	return h
}

// awaitAt waits for what placeAt attached at seq to have run, and returns
// what it returned.
func (a *applier) awaitAt(ctx context.Context, seq seqNo, h *attachment) error {
	// The request that owns seq is waiting for a reply this plugin has not
	// sent, so it will not bring the order to seq, and the apply is run
	// here instead. What this does wait for is a number below seq that
	// another plugin is still answering, which is the wait every apply
	// has.
	//
	// A caller that has already given up applies nothing. The wait below
	// takes the attachment back for it.
	if ctx.Err() == nil {
		switch head, gone := a.gate.position(seq); {
		case head:
			a.drain(seq)

		case gone:
			if a.forget(seq, h) {
				return errOrderPassed
			}
		}
	}

	select {
	case <-h.done:
		return h.err
	case <-a.gate.ended():
	case <-ctx.Done():
	}

	// Asked again after the wake, because the order can close or the
	// caller give up at the same moment the apply runs, and what the apply
	// did outranks why the wait ended.
	if !a.forget(seq, h) {
		<-h.done

		return h.err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	return ErrOrderClosed
}

// hook makes the gate announce the numbers it serves, so an apply attached
// below one of them runs when the order reaches it.
func (a *applier) hook() {
	// Taken by every entry point, like the gate's own init and for the same
	// reason: a zero applier works, and the order is not something a
	// caller has to remember to switch on.
	a.once.Do(func() { a.gate.onServed(a.drain) })
}

// attach records that h runs at the head of seq.
func (a *applier) attach(seq seqNo, h *attachment) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.attached == nil {
		a.attached = map[seqNo][]*attachment{}
	}

	a.attached[seq] = append(a.attached[seq], h)
}

// forget drops h from seq's attachments, and reports whether it was still
// there to drop. False means it is being run or has been run, so what it
// did is the answer rather than why its caller stopped waiting.
func (a *applier) forget(seq seqNo, h *attachment) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	if h.claimed {
		return false
	}

	a.drop(seq, h)

	return true
}

// drop removes h from seq's attachments. The caller holds a.mu.
func (a *applier) drop(seq seqNo, h *attachment) {
	kept := make([]*attachment, 0, len(a.attached[seq]))

	for _, other := range a.attached[seq] {
		if other != h {
			kept = append(kept, other)
		}
	}

	if len(kept) == 0 {
		delete(a.attached, seq)

		return
	}

	a.attached[seq] = kept
}

// drain runs what is attached to seq and returns once all of it has run.
func (a *applier) drain(seq seqNo) {
	// Whoever brings the order to seq drains it: the gate, for every number
	// it reaches, and the caller that attaches to the number the order is
	// already at. Both can arrive at once, and a further apply can attach
	// while one is running, so only the attachment at the head of the
	// number is ever claimed, and a drainer that finds the head claimed
	// waits for it instead of looking past it. That is one apply at a
	// time, in the order they were attached, and seq's own apply after all
	// of them.
	//
	// What keeps a plugin from attaching here for good is the plugin
	// request timeout: a plugin is placed at seq only while it owes an
	// answer on it, and getPluginRequestTimeout() bounds every request
	// from the moment it reaches the plugin, after which the request's
	// call drops its entry and the lowest number the plugin owes an answer
	// on moves above seq.
	for {
		a.mu.Lock()

		var next *attachment

		if attached := a.attached[seq]; len(attached) > 0 {
			next = attached[0]
		}

		mine := next != nil && !next.claimed
		if mine {
			next.claimed = true
		}

		a.mu.Unlock()

		switch {
		case next == nil:
			return

		case !mine:
			<-next.done

		default:
			next.run()

			// Dropped before its caller is let go, so a drainer woken by
			// that finds the attachment behind it at the head.
			a.mu.Lock()
			a.drop(seq, next)
			a.mu.Unlock()

			close(next.done)
		}
	}
}

// close ends the order, so an apply that has not had its turn does not get
// one.
func (a *applier) close() {
	a.gate.close()
}

// release announces that seq applies nothing, and is a no-op once seq has
// had its turn.
func (a *applier) release(seq seqNo) {
	// The one name for every way a number reaches no apply: a request that
	// answers with no change, one that fails before its apply, and one
	// whose caller gave up. A number the order is never told about is one
	// it waits on for good.
	a.hook()

	a.gate.skip(seq)
}
