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

import "context"

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
type applier struct {
	gate *gate
}

// newApplier returns an applier whose first number is first.
func newApplier(first seqNo) *applier {
	return &applier{gate: newGate(first)}
}

// apply runs fn once every earlier request has applied, and returns what fn
// returned.
//
// A context that ends before fn's turn abandons the turn, so the order is
// handed on rather than left waiting, and fn does not run.
func (a *applier) apply(ctx context.Context, seq seqNo, fn func() error) error {
	if err := ctx.Err(); err != nil {
		a.gate.abandon(seq)

		return err
	}

	var err error

	// The gate takes func() rather than func() error, so that the stream
	// writer can use the same type. The error comes back by capture.
	a.gate.run(seq, func() { err = fn() })

	return err
}

// skip announces that seq applies nothing.
func (a *applier) skip(seq seqNo) {
	a.gate.skip(seq)
}

// release hands seq on if nothing else has.
//
// Every ordered request defers this: it takes its number before it talks to
// any plugin, so a request that fails on the way never reaches its apply,
// and the number would otherwise be one the order waits on for good. It is
// a no-op once seq has had its turn.
func (a *applier) release(seq seqNo) {
	a.gate.skip(seq)
}
