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

// The order NRI puts plugin decisions into practice in, kept by handing a
// baton along rather than by holding a lock across a request.
//
// Only requests a plugin can answer with a change need an order:
// CreateContainer, UpdateContainer and StopContainer. For the rest a plugin
// says proceed or fail, so there is no decision to apply and no way for two
// of them to interact. Those run with no ordering at all. Extending NRI to
// take changes in answer to another request would add it to the ordered
// set; it would not change what follows.
//
// One ordering point: a request's answer is applied only once its
// predecessor's has been. That is the guarantee -- decisions are put into
// practice in the order the requests arrived in -- and it is the whole of
// what this file does.
//
// What it deliberately does not do is order the calls to each plugin.
// Ordering those would be worth the code only if it made a plugin *process*
// them in that order, and over a unary ttrpc call nothing here can: the
// send is welded to waiting for the reply, so a caller cannot hold a
// plugin's turn until its bytes are out, and the stub answers each call in
// its own goroutine besides. A plugin's processing order is settled on the
// wire or not at all, so this promises only what it can keep and carries
// only the code for that.
//
// A baton is a channel its holder closes, so a holder that gives up -- an
// error, a cancelled context, a panic -- still passes it on and the chain
// never stalls. That is why this is a chain of one-shot channels rather
// than a counter and a condition variable.

import (
	"context"
	"sync"
)

// baton is one request's turn.
//
// The zero baton is a turn nobody else is waiting for: waiting on it
// returns at once and passing it does nothing.
type baton struct {
	prev <-chan struct{}
	mine chan struct{}
}

// wait blocks until the previous holder passed the baton, or ctx ends.
//
// A request that gives up here must still pass its own baton, which the
// caller's deferred release does.
func (b baton) wait(ctx context.Context) error {
	if b.prev == nil {
		return nil
	}

	select {
	case <-b.prev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pass hands the baton to the next holder. Safe to call more than once.
func (b baton) pass() {
	if b.mine == nil {
		return
	}

	select {
	case <-b.mine:
	default:
		close(b.mine)
	}
}

// requestOrder is the order NRI applies the answers to change-capable
// requests in.
type requestOrder struct {
	mu   sync.Mutex
	last chan struct{}
}

// turn is one request's place in that order.
type turn struct {
	commit baton
}

// take puts a request at the end of the order.
func (o *requestOrder) take() *turn {
	o.mu.Lock()
	defer o.mu.Unlock()

	b := baton{prev: o.last, mine: make(chan struct{})}
	o.last = b.mine

	return &turn{commit: b}
}

// release passes the baton this request holds.
//
// Deferred by each request, so a request that returns early for any reason
// leaves nobody waiting on it.
func (t *turn) release() {
	t.commit.pass()
}

// commitTurn waits until every earlier request has applied its answer.
//
// Note it does not pass the baton here. Applying an answer wants to be
// ordered and *not* overlapping, since the next answer must land after this
// one has, so the baton is passed by release, on the way out, once this
// request is done with it.
func (t *turn) commitTurn(ctx context.Context) error {
	return t.commit.wait(ctx)
}

// removeClosed drops plugins that closed while a request was running.
//
// A request holds the lock for reading, so it cannot do this itself. The
// check runs under the read lock it already had and the write lock is taken
// only when there is something to drop: taking it on every request would
// wait out every request running alongside this one, which is the
// serialisation this change is removing.
func (r *Adaptation) removeClosed() {
	r.RLock()

	closed := false

	for _, p := range r.plugins {
		if p.isClosed() {
			closed = true

			break
		}
	}

	r.RUnlock()

	if !closed {
		return
	}

	r.Lock()
	defer r.Unlock()

	r.removeClosedPlugins()
}
