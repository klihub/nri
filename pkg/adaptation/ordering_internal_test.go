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

// The order's own tests.
//
// In this package rather than beside the others, because the property to
// check is what happens *inside* a commit section and that is not
// observable from outside NRI: a request that committed second can still be
// recorded first, if its goroutine is descheduled between the commit and
// whatever the test writes down. A test built on the outside measures the
// scheduler, which is a flake waiting for a quiet machine.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRequestOrderAppliesInTurn is the guarantee, checked directly: answers
// are applied in the order the requests arrived, one at a time.
//
// Every turn is taken up front, in a known order, and then released to run
// at once. Each records itself from inside its commit section, so what the
// test compares is the order the sections ran in and not the order their
// goroutines happened to get the lock afterwards.
func TestRequestOrderAppliesInTurn(t *testing.T) {
	const n = 32

	var o requestOrder

	turns := make([]*turn, 0, n)
	for i := 0; i < n; i++ {
		turns = append(turns, o.take())
	}

	var (
		mu        sync.Mutex
		order     []int
		inside    int
		overlap   int
		wg        sync.WaitGroup
		startLine = make(chan struct{})
	)

	for i, tn := range turns {
		wg.Add(1)

		go func(i int, tn *turn) {
			defer wg.Done()
			defer tn.release()

			<-startLine

			if err := tn.commitTurn(context.Background()); err != nil {
				t.Errorf("turn %d: %v", i, err)

				return
			}

			mu.Lock()
			inside++
			if inside > overlap {
				overlap = inside
			}
			order = append(order, i)
			mu.Unlock()

			// Held open, so an implementation that let the next turn in
			// early is caught rather than merely raced with.
			time.Sleep(time.Millisecond)

			mu.Lock()
			inside--
			mu.Unlock()
		}(i, tn)
	}

	close(startLine)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, 1, overlap, "commit sections must not overlap")

	want := make([]int, 0, n)
	for i := 0; i < n; i++ {
		want = append(want, i)
	}

	require.Equal(t, want, order, "answers must be applied in the order taken")
}

// TestRequestOrderSurvivesAnAbandonedTurn pins the fail-safe property: a
// request that gives up without ever committing must not strand the ones
// behind it.
//
// This is why a turn is a one-shot channel its holder closes rather than a
// counter someone has to remember to advance. Every request defers release,
// so an error, a cancelled context or a panic still hands the order on.
func TestRequestOrderSurvivesAnAbandonedTurn(t *testing.T) {
	var o requestOrder

	first := o.take()
	abandoned := o.take()
	last := o.take()

	first.release()

	// Nobody will ever commit this one: it errored out on the way in.
	abandoned.release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, last.commitTurn(ctx),
		"a turn nobody committed must still pass the order on")
}

// TestRequestOrderFirstTurnWaitsForNobody keeps the zero baton honest: the
// first request through must not block on a predecessor that never existed.
func TestRequestOrderFirstTurnWaitsForNobody(t *testing.T) {
	var o requestOrder

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, o.take().commitTurn(ctx))
}

// TestRequestOrderHonoursACancelledContext checks a request whose context
// ends while it waits gives up rather than hanging, and that giving up
// still releases the order.
func TestRequestOrderHonoursACancelledContext(t *testing.T) {
	var o requestOrder

	blocking := o.take()
	waiting := o.take()
	behind := o.take()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, waiting.commitTurn(ctx), context.Canceled)

	waiting.release()
	blocking.release()

	live, cancelLive := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLive()

	require.NoError(t, behind.commitTurn(live),
		"the order must go on after a request gave up waiting")
}
