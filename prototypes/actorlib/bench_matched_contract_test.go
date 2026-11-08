package actorlib_test

// The matched baseline's contract (Q8, findings M6 of rounds 1 and 2): each
// test checks one clause that the library's tests check, so the matched
// ratios compare the same work and the same guarantees.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// Round-2 review probe: a sender cancelled before it sends is refused.
func TestMatchedSendRejectsCanceledCaller(t *testing.T) {
	a := startMatchedCounter(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.send(ctx, mInc{1})
	if stopErr := a.stop(); stopErr != nil {
		t.Errorf("stop: %v", stopErr)
	}
	if err == nil {
		t.Error("matched send admitted a message from an already-canceled caller")
	}
}

// Round-2 review probe, with the callback written against the baseline's
// contract: it waits on the actor's context, as a library callback waits on
// the agent's task. Stop must not need the gate.
func TestMatchedAgentCallbackObservesStop(t *testing.T) {
	a := startMatchedAgent(4)
	holding, gate, completed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := matchedCall(context.Background(), a, func(ctx context.Context, n *int64) (int64, error) {
			close(holding)
			select {
			case <-gate:
				return *n, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
		completed <- err
	}()
	<-holding
	var forced atomic.Bool
	stopWatchdog := testwait.AfterFunc(t, func() { forced.Store(true); close(gate) })
	if err := a.stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
	stopWatchdog()
	if forced.Load() {
		t.Error("matched stop needed an external gate release")
	}
	if err := <-completed; !errors.Is(err, errStopped) {
		t.Errorf("caller of an interrupted callback: got %v, want stopped", err)
	}
}

// A reply dropped while the actor runs completes "no reply", not "stopped".
func TestMatchedRunningDroppedReplyIsNoReply(t *testing.T) {
	a := startFenced(4, func(_ context.Context, m mGet) error {
		m.release() // the handler drops the reply
		return nil
	})
	r := newMSlot[int64](&a.closing)
	if err := a.send(context.Background(), mGet{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.receive(context.Background()); !errors.Is(err, errNoReply) {
		t.Errorf("dropped reply: got %v, want no reply", err)
	}
	if err := a.stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}

type mPanicky struct {
	value  string
	closed *atomic.Int32
}

func (m mPanicky) release() {
	m.closed.Add(1)
	if m.value != "" {
		panic(m.value)
	}
}

// The drain releases every queued message and keeps each release panic, in
// order.
func TestMatchedDrainKeepsEveryReleasePanic(t *testing.T) {
	var closed atomic.Int32
	holding := make(chan struct{})
	a := startFenced(4, func(ctx context.Context, m mPanicky) error {
		close(holding)
		<-ctx.Done()
		return nil
	})
	if err := a.send(context.Background(), mPanicky{closed: &closed}); err != nil {
		t.Fatal(err)
	}
	<-holding
	for _, v := range []string{"first", "second"} {
		if err := a.send(context.Background(), mPanicky{value: v, closed: &closed}); err != nil {
			t.Fatal(err)
		}
	}
	err := a.stop()
	if err == nil || err.Error() != "panic: first\npanic: second" {
		t.Errorf("stop: got %v, want both release panics in order", err)
	}
	if n := closed.Load(); n != 2 {
		t.Errorf("released %d queued messages; want 2", n)
	}
}

type mLease struct{ closed *atomic.Int32 }

func (l mLease) Close() { l.closed.Add(1) }

// A ready answer taken for a cancelled receiver is released.
func TestMatchedCanceledReadyReplyIsReleased(t *testing.T) {
	var closing atomic.Bool
	for range 1024 {
		var closed atomic.Int32
		s := newMSlot[mLease](&closing)
		s.answer(mOutcome[mLease]{v: mLease{&closed}})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.receive(ctx); err == nil {
			t.Fatal("cancelled receiver got the answer")
		}
		if closed.Load() != 1 {
			t.Fatalf("answer released %d times; want 1", closed.Load())
		}
	}
}

// Round-3 review probe (M3): a successful call transfers its resource to the
// caller. The handler's automatic refusal then has no effect: a refusal owns
// nothing, so its zero value is never closed.
func TestR3MatchedResourceResultDoesNotPanicOnRefusal(t *testing.T) {
	var closed atomic.Int32
	a := startMatchedAgent(4)
	v, err := matchedCall(context.Background(), a, func(_ context.Context, _ *int64) (mLease, error) {
		return mLease{&closed}, nil
	})
	if err != nil {
		t.Errorf("call: %v", err)
	} else {
		v.Close()
	}
	if err := a.stop(); err != nil {
		t.Errorf("successful resource result ended matched agent: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("answer released %d times; want 1", got)
	}
}

type r3OwnedFailure struct{ closed *atomic.Int32 }

func (r3OwnedFailure) Error() string { return "owned failure" }
func (e r3OwnedFailure) Close()      { e.closed.Add(1) }

// Round-3 review probe (M3): failures are owned reply payloads too, so
// cancellation releases the failure exactly as the library's failed outcome
// does.
func TestR3MatchedCanceledFailureIsReleased(t *testing.T) {
	var closed atomic.Int32
	var closing atomic.Bool
	s := newMSlot[struct{}](&closing)
	s.answer(mOutcome[struct{}]{err: r3OwnedFailure{&closed}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = s.receive(ctx)
	if got := closed.Load(); got != 1 {
		t.Errorf("abandoned error payload released %d times; want 1", got)
	}
}

// A late answer to a detached receiver is released by kind: a failure's
// payload, not its zero value, and nothing for a refusal.
func TestMatchedLateAnswersReleaseByKind(t *testing.T) {
	var closing atomic.Bool
	var closed atomic.Int32
	s := newMSlot[mLease](&closing)
	if err := s.abandon(); err != nil {
		t.Fatal(err)
	}
	s.answer(mOutcome[mLease]{err: r3OwnedFailure{&closed}}) // releases the failure, not the zero mLease
	s.refuse()                                               // releases nothing
	if got := closed.Load(); got != 1 {
		t.Errorf("late failure released %d times; want 1", got)
	}
}

// A release panic on the receiving side comes back as an error after the
// cancellation, as the library's in-release defect does.
func TestMatchedReceiverReleasePanicIsKept(t *testing.T) {
	var closing atomic.Bool
	s := newMSlot[mPanickyLease](&closing)
	s.answer(mOutcome[mPanickyLease]{v: mPanickyLease{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.receive(ctx)
	if !errors.Is(err, context.Canceled) || err.Error() == context.Canceled.Error() {
		t.Errorf("receive: %v; want the release panic and the cancellation", err)
	}
}

type mPanickyLease struct{}

func (mPanickyLease) Close() { panic("answer release") }
