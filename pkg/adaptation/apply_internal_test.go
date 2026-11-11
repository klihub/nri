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

// The applier's own tests.
//
// In this package rather than beside the others, because the property to
// check is what happens *inside* an applied section and that is not
// observable from outside NRI: a request that applied second can still be
// recorded first, if its goroutine is descheduled between the apply and
// whatever the test writes down. A test built on the outside measures the
// scheduler, which is a flake waiting for a quiet machine.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestApplierAppliesInTurn is the guarantee, checked directly: answers are
// applied in the order the requests arrived, one at a time.
//
// Numbers are taken up front, in a known order, and then applied at once.
// Each records itself from inside its applied section, so what the test
// compares is the order the sections ran in and not the order their
// goroutines happened to be scheduled afterwards.
func TestApplierAppliesInTurn(t *testing.T) {
	const count = 8

	var (
		s       sequencer
		a       applier
		mu      sync.Mutex
		applied []seqNo
		inside  int
		overlap int
		wg      sync.WaitGroup
		release = make(chan struct{})
	)

	seqs := make([]seqNo, 0, count)
	for i := 0; i < count; i++ {
		seqs = append(seqs, s.take())
	}

	// Released in reverse, so the arrival order and the scheduling order
	// disagree as sharply as the test can arrange.
	for i := len(seqs) - 1; i >= 0; i-- {
		seq := seqs[i]

		wg.Add(1)

		go func() {
			defer wg.Done()

			<-release

			_ = a.apply(context.Background(), seq, func() error {
				mu.Lock()
				inside++
				if inside > overlap {
					overlap = inside
				}
				applied = append(applied, seq)
				mu.Unlock()

				time.Sleep(time.Millisecond)

				mu.Lock()
				inside--
				mu.Unlock()

				return nil
			})
		}()
	}

	close(release)
	wg.Wait()

	require.Equal(t, seqs, applied, "answers were applied out of arrival order")
	require.Equal(t, 1, overlap, "two answers were applied at once")
}

// TestApplierSurvivesAReleasedNumber pins the fail-safe property: a request
// that gives up without ever applying must not strand the ones behind it.
//
// Every ordered request defers release, so an error, a cancelled context or
// a panic still hands the order on.
func TestApplierSurvivesAReleasedNumber(t *testing.T) {
	var s sequencer

	var a applier
	_ = s.upcoming()

	first, second := s.take(), s.take()

	a.release(first)

	done := make(chan error, 1)

	go func() {
		done <- a.apply(context.Background(), second, func() error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a released number stranded the request behind it")
	}
}

// TestApplierFirstNumberWaitsForNobody keeps the first turn honest: the
// first request through must not block on a predecessor that never existed.
func TestApplierFirstNumberWaitsForNobody(t *testing.T) {
	var s sequencer

	var a applier

	done := make(chan error, 1)

	go func() {
		done <- a.apply(context.Background(), s.take(), func() error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the first request waited for a predecessor")
	}
}

// TestApplierPassesTheApplyError back, since a refused apply is the
// runtime's answer to the caller and not the order's business.
func TestApplierPassesTheApplyError(t *testing.T) {
	var s sequencer

	var a applier
	refused := errors.New("apply refused")

	err := a.apply(context.Background(), s.take(), func() error { return refused })

	require.ErrorIs(t, err, refused)
}

// TestApplierContextEndedBeforeItsTurn checks that a caller which gave up
// neither applies nor keeps the order waiting.
func TestApplierContextEndedBeforeItsTurn(t *testing.T) {
	var s sequencer

	var a applier

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	err := a.apply(ctx, s.take(), func() error { ran = true; return nil })

	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ran, "a cancelled request applied anyway")

	done := make(chan error, 1)

	go func() {
		done <- a.apply(context.Background(), s.take(), func() error { return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled request stranded the one behind it")
	}
}
