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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// applyAt runs fn at the head of seq, before whatever seq applies, and
// returns what fn returned.
//
// The two halves of it belong to two goroutines in production -- the
// stream reader attaches and the goroutine answering the update waits --
// and to one here.
func (a *applier) applyAt(ctx context.Context, seq seqNo, fn func() error) error {
	return a.awaitAt(ctx, seq, a.placeAt(seq, fn))
}

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

// TestApplierRunsAnAttachedApplyBeforeItsNumber is the placement: an apply
// attached below a number runs after everything before that number and
// before what the number itself applies.
//
// Each section records itself from inside, and the overlap count is what
// says the attached apply took the head rather than sharing it.
func TestApplierRunsAnAttachedApplyBeforeItsNumber(t *testing.T) {
	var (
		s       sequencer
		a       applier
		mu      sync.Mutex
		applied []string
		inside  int
		overlap int
	)

	section := func(what string) error {
		mu.Lock()
		inside++
		if inside > overlap {
			overlap = inside
		}
		applied = append(applied, what)
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		inside--
		mu.Unlock()

		return nil
	}

	first, second := s.take(), s.take()

	attached := make(chan error, 1)

	go func() {
		attached <- a.applyAt(context.Background(), second,
			func() error { return section("attached") })
	}()

	waitForAttachment(t, &a, second)

	require.NoError(t, a.apply(context.Background(), first,
		func() error { return section("first") }))

	require.NoError(t, a.apply(context.Background(), second,
		func() error { return section("second") }))

	select {
	case err := <-attached:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an attached apply never ran")
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"first", "attached", "second"}, applied,
		"an attached apply ran out of its place in the order")
	require.Equal(t, 1, overlap, "two applies ran at once")
}

// TestApplierRunsTwoAttachedAppliesInTurn pins what two applies attached to
// one number do: one at a time, in the order they were attached.
//
// A plugin's background updates overlap as a matter of course, and two of
// them placed while it owes one answer go below the same number. The second
// attaches while the first is running, which is the case a drainer that
// looked past a running attachment would run alongside it -- two applies at
// once, and the later decision able to land first.
func TestApplierRunsTwoAttachedAppliesInTurn(t *testing.T) {
	var (
		s       sequencer
		a       applier
		mu      sync.Mutex
		applied []string
		inside  int
		overlap int
	)

	section := func(what string) error {
		mu.Lock()
		inside++
		if inside > overlap {
			overlap = inside
		}
		applied = append(applied, what)
		mu.Unlock()

		time.Sleep(time.Millisecond)

		mu.Lock()
		inside--
		mu.Unlock()

		return nil
	}

	first, second := s.take(), s.take()

	var (
		running = make(chan struct{})
		release = make(chan struct{})
		one     = make(chan error, 1)
		two     = make(chan error, 1)
	)

	go func() {
		one <- a.applyAt(context.Background(), second, func() error {
			close(running)
			<-release

			return section("one")
		})
	}()

	waitForAttachment(t, &a, second)

	// Brings the order to the number both applies are attached below, and
	// so runs the first of them -- which holds it there until the second
	// has attached.
	handed := make(chan error, 1)

	go func() {
		handed <- a.apply(context.Background(), first, func() error { return nil })
	}()

	select {
	case <-running:
	case <-time.After(5 * time.Second):
		t.Fatal("the first attached apply never ran")
	}

	go func() {
		two <- a.applyAt(context.Background(), second,
			func() error { return section("two") })
	}()

	require.Eventually(t, func() bool { return a.attachments(second) == 2 },
		5*time.Second, time.Millisecond, "the second apply never attached")

	close(release)

	for what, done := range map[string]chan error{"one": one, "two": two, "first": handed} {
		select {
		case err := <-done:
			require.NoError(t, err, what)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never finished", what)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"one", "two"}, applied,
		"two applies attached to one number ran out of turn")
	require.Equal(t, 1, overlap, "two applies ran at once")
}

// TestApplierRunsAnAttachedApplyOnASkippedNumber pins the case the gate has
// to announce: a number that applies nothing still has a turn, and what is
// attached below it runs then.
func TestApplierRunsAnAttachedApplyOnASkippedNumber(t *testing.T) {
	var (
		s sequencer
		a applier
	)

	first, second := s.take(), s.take()

	attached := make(chan error, 1)

	go func() {
		attached <- a.applyAt(context.Background(), second,
			func() error { return nil })
	}()

	waitForAttachment(t, &a, second)

	a.release(first)
	a.release(second)

	select {
	case err := <-attached:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an attached apply never ran on a skipped number")
	}
}

// TestApplierRunsAnAttachedApplyAtTheHeadItself is the in-handler case: the
// number the apply is attached below is the one the order is already at,
// and the request that owns it is waiting for the answer the plugin is
// asking from inside.
//
// Nobody else can bring the order there, so the attaching caller runs it.
func TestApplierRunsAnAttachedApplyAtTheHeadItself(t *testing.T) {
	var (
		s   sequencer
		a   applier
		ran atomic.Bool
	)

	done := make(chan error, 1)

	go func() {
		done <- a.applyAt(context.Background(), s.take(),
			func() error { ran.Store(true); return nil })
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
		require.True(t, ran.Load(), "the apply at the head of the order never ran")
	case <-time.After(5 * time.Second):
		t.Fatal("an apply attached to the head of the order waited for nobody")
	}
}

// TestApplierRefusesAnAttachedApplyTheOrderHasPassed covers the one way the
// place can be stale: the request the update was placed below had its
// caller give up, so its number was handed on before the update got here.
func TestApplierRefusesAnAttachedApplyTheOrderHasPassed(t *testing.T) {
	var (
		s   sequencer
		a   applier
		ran atomic.Bool
	)

	first := s.take()
	a.release(first)

	err := a.applyAt(context.Background(), first,
		func() error { ran.Store(true); return nil })

	require.ErrorIs(t, err, errOrderPassed)
	require.False(t, ran.Load(), "an update applied below a number that was served")
}

// TestAnUpdateFromAnIdlePluginGoesBelowTheNextNumber pins where an update
// goes when its plugin owes no answer and there is nothing to place it
// below: below the number nobody has taken yet.
//
// That is after every request already numbered, so after every decision of
// this plugin that is in the order, and before every request numbered
// later, because the request that takes that number runs what is attached
// below it first. Taking a number for the update instead would put the
// taking after the reading of the set it was placed against, and a request
// numbered in between would then apply before an update decided before it.
func TestAnUpdateFromAnIdlePluginGoesBelowTheNextNumber(t *testing.T) {
	r := &Adaptation{
		updateFn: func(context.Context, []*ContainerUpdate) ([]*ContainerUpdate, error) {
			return nil, nil
		},
	}

	numbered := r.seq.take()

	placed := r.placeUpdate(context.Background(),
		[]*ContainerUpdate{{ContainerId: "idle"}}, 0, false)

	require.Equal(t, numbered+1, placed.at,
		"an idle plugin's update was not placed below the next number")
	require.Equal(t, 1, r.applies.attachments(placed.at),
		"an idle plugin's update left no apply attached")
	require.Equal(t, placed.at, r.seq.take(),
		"the next request took a number other than the one the update went below")
}

// TestStoppingTheRuntimeClosesTheApplyOrder pins what closes the order in
// production, which is what gives an update waiting for a turn something
// to be answered with.
//
// Nothing else does: a plugin dying closes that plugin's stream, not the
// order every plugin's answers are applied in.
func TestStoppingTheRuntimeClosesTheApplyOrder(t *testing.T) {
	var ran atomic.Bool

	r := &Adaptation{
		updateFn: func(context.Context, []*ContainerUpdate) ([]*ContainerUpdate, error) {
			ran.Store(true)

			return nil, nil
		},
	}

	// Numbered but never applied, so the order is still waiting on it when
	// the runtime stops and the update below it has had no turn.
	placed := r.placeUpdate(context.Background(),
		[]*ContainerUpdate{{ContainerId: "idle"}}, 0, false)

	_ = r.seq.take()

	r.Stop()

	failed, err := placed.apply(context.Background())

	require.ErrorIs(t, err, ErrOrderClosed)
	require.Nil(t, failed)
	require.False(t, ran.Load(), "an update applied after the runtime stopped")
}

// TestAnUpdateWhoseGateClosesReportsIt pins what a placed update answers
// with when the order closes under it, which is what Stop does.
//
// The reply to an update is synchronous, so there is a caller to tell. A
// dropped update would have the plugin believe a decision took effect that
// never will.
//
// A plugin dying is the other way an update loses its turn, and it is
// answered elsewhere: the update is applied under the stream handler's
// context, so the death cancels it, the attachment is dropped and the
// update applies nothing. TestAnUpdateAbandonedByItsContextIsNotApplied
// pins that half.
func TestAnUpdateWhoseGateClosesReportsIt(t *testing.T) {
	var (
		s   sequencer
		a   applier
		ran atomic.Bool
	)

	_, second := s.take(), s.take()

	done := make(chan error, 1)

	go func() {
		done <- a.applyAt(context.Background(), second,
			func() error { ran.Store(true); return nil })
	}()

	// The first number keeps its turn, so the update is waiting for one
	// that has not come when the order closes under it.
	waitForAttachment(t, &a, second)

	a.gate.close()

	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrOrderClosed)
		require.False(t, ran.Load(), "an update applied after the order closed")
	case <-time.After(5 * time.Second):
		t.Fatal("an update outlived the order it was waiting for")
	}
}

// TestAnUpdateAbandonedByItsContextIsNotApplied checks that the context
// bounding the wait for a turn also bounds what the update does: a caller
// that gave up gets its error, and the apply it gave up on does not run.
func TestAnUpdateAbandonedByItsContextIsNotApplied(t *testing.T) {
	var (
		s   sequencer
		a   applier
		ran atomic.Bool
	)

	first, second := s.take(), s.take()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		done <- a.applyAt(ctx, second, func() error { ran.Store(true); return nil })
	}()

	// The first number keeps its turn, so the update is waiting for one
	// that has not come when its caller gives up.
	waitForAttachment(t, &a, second)

	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("an update outlived its context")
	}

	// The number the update was attached below still has its own turn to
	// come, and what the abandoned update left behind must not run then.
	require.NoError(t, a.apply(context.Background(), first,
		func() error { return nil }))
	require.NoError(t, a.apply(context.Background(), second,
		func() error { return nil }))

	require.False(t, ran.Load(), "an abandoned update applied anyway")
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

// attachments is how many applies are attached below seq.
func (a *applier) attachments(seq seqNo) int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return len(a.attached[seq])
}

// waitForAttachment blocks until an apply is attached below seq.
//
// How a test fixes the order of events: an attachment is made before the
// caller waits for it and nothing outside says when, so a test that went on
// without waiting would be arranging a different case than it meant to.
func waitForAttachment(t *testing.T, a *applier, seq seqNo) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.attachments(seq) > 0 {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("no apply was attached below %d", seq)
}
