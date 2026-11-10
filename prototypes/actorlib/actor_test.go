package actorlib

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// within runs body as the root task's body inside one owner scope, so every
// actor body starts is stopped when it returns (R5).
func within(t *testing.T, body func(task *rt.Task, s *rt.Scope) error) {
	t.Helper()
	_, err := rt.Run(rt.Root(t.Name()), func(task *rt.Task) (struct{}, error) {
		return rt.Scoped(task, nil, func(s *rt.Scope) (struct{}, error) {
			return struct{}{}, body(task, s)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func failureIs[E error](err error) bool {
	f, ok := rt.Failure(err)
	if !ok {
		return false
	}
	_, ok = f.(E)
	return ok
}

// probe is a test message: it may carry a reply, and it counts its releases.
type probe struct {
	id     int
	reply  Reply[int, Never]
	closed *[]atomic.Int32
}

func (m probe) Close() {
	m.reply.Close()
	if m.closed != nil {
		(*m.closed)[m.id].Add(1)
	}
}

// replyTo builds a request on c's control by hand, as Ask does, so a test can
// admit it without waiting for the answer.
func replyTo[M any](a Address[M]) (Reply[int, Never], chan outcome[int, Never]) {
	sl := newSlot[int, Never](&a.c.control)
	return Reply[int, Never]{sl}, sl.ch
}

func waitQueued[M any](t *testing.T, a Address[M], n int) {
	t.Helper()
	if !testwait.Until(t, func() bool { return len(a.c.mailbox) == n }) {
		t.Fatalf("mailbox holds %d, want %d", len(a.c.mailbox), n)
	}
}

func TestDroppedReplyAnswersNoReply(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "drop", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			defer m.Close() // the release R5 inserts: the handler never answers
			return Continue(n), nil
		})
		_, err := Ask(task, a.Address(), func(r Reply[int, Never]) probe { return probe{reply: r} })
		if !failureIs[NoReply](err) {
			t.Errorf("dropped reply: got %v, want NoReply", err)
		}
		return a.Stop()
	})
}

func TestFirstAnswerWins(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "twice", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			defer m.Close()
			m.reply.Send(ht, 1)
			m.reply.Send(ht, 2) // a use after consume (R20) in checked source
			return Continue(n), nil
		})
		for range 3 {
			v, err := Ask(task, a.Address(), func(r Reply[int, Never]) probe { return probe{reply: r} })
			if err != nil || v != 1 {
				t.Errorf("got %d, %v; want 1, nil", v, err)
			}
		}
		return a.Stop()
	})
}

// gated starts an actor whose handler blocks on the first message until gate
// closes or the actor is stopped, and reports when it holds that message.
func gated(s *rt.Scope, capacity int, gate <-chan struct{}, holding chan<- struct{}) Actor[probe] {
	first := true
	return Start(s, "gated", 0, capacity, func(t *rt.Task, n int, m probe) (Next[int], error) {
		defer m.Close()
		if first {
			first = false
			holding <- struct{}{}
			select {
			case <-gate:
			case <-t.Done():
				return Next[int]{}, rt.Interrupted
			}
		}
		m.reply.Send(t, n)
		return Continue(n + 1), nil
	})
}

func TestStopAnswersInFlightAndQueuedStopped(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding := make(chan struct{})
		a := gated(s, 8, make(chan struct{}), holding)
		addr := a.Address()
		r0, slot0 := replyTo(addr)
		if err := addr.Send(task, probe{reply: r0}); err != nil {
			return err
		}
		<-holding
		var slots []chan outcome[int, Never]
		for range 5 {
			r, slot := replyTo(addr)
			if err := addr.Send(task, probe{reply: r}); err != nil {
				return err
			}
			slots = append(slots, slot)
		}
		if err := a.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
		if o := <-slot0; !failureIs[Stopped](resultErr(o)) {
			t.Errorf("in-flight request: got %v, want Stopped", resultErr(o))
		}
		for i, slot := range slots {
			if o := <-slot; !failureIs[Stopped](resultErr(o)) {
				t.Errorf("queued request %d: got %v, want Stopped", i, resultErr(o))
			}
		}
		if err := addr.Send(task, probe{}); !failureIs[Stopped](err) {
			t.Errorf("Send after Stop: got %v, want Stopped", err)
		}
		if _, err := Ask(task, addr, func(r Reply[int, Never]) probe { return probe{reply: r} }); !failureIs[Stopped](err) {
			t.Errorf("Ask after Stop: got %v, want Stopped", err)
		}
		return nil
	})
}

func TestBlockedSenderWakesOnStopAndReleasesItsMessage(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		closed := make([]atomic.Int32, 3)
		holding := make(chan struct{})
		a := gated(s, 1, make(chan struct{}), holding)
		addr := a.Address()
		if err := addr.Send(task, probe{id: 0, closed: &closed}); err != nil {
			return err
		}
		<-holding
		if err := addr.Send(task, probe{id: 1, closed: &closed}); err != nil { // fills the mailbox
			return err
		}
		blocked := rt.Go(s, "sender", func(ct *rt.Task) (struct{}, error) {
			return struct{}{}, addr.Send(ct, probe{id: 2, closed: &closed})
		})
		time.Sleep(10 * time.Millisecond) // let the sender block; the result does not depend on it
		if err := a.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
		if _, err := blocked.Wait(); !failureIs[Stopped](err) {
			t.Errorf("blocked sender: got %v, want Stopped", err)
		}
		for i := range closed {
			if n := closed[i].Load(); n != 1 {
				t.Errorf("message %d released %d times, want 1", i, n)
			}
		}
		return nil
	})
}

// TestEveryMessageReleasedOnceUnderConcurrentStop admits messages from many
// tasks while the actor stops. Each message is handled, released by the
// drain or released by its failed Send: exactly one release each.
func TestEveryMessageReleasedOnceUnderConcurrentStop(t *testing.T) {
	const senders, each = 8, 200
	within(t, func(task *rt.Task, s *rt.Scope) error {
		closed := make([]atomic.Int32, senders*each)
		var handled, refused atomic.Int32
		a := Start(s, "busy", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			defer m.Close()
			handled.Add(1)
			return Continue(n + 1), nil
		})
		addr := a.Address()
		var start sync.WaitGroup
		start.Add(1)
		var hs []*rt.Handle[struct{}]
		for g := range senders {
			hs = append(hs, rt.Go(s, "sender", func(ct *rt.Task) (struct{}, error) {
				start.Wait()
				for i := range each {
					if err := addr.Send(ct, probe{id: g*each + i, closed: &closed}); err != nil {
						if !failureIs[Stopped](err) {
							return struct{}{}, err
						}
						refused.Add(1)
					}
				}
				return struct{}{}, nil
			}))
		}
		start.Done()
		time.Sleep(time.Millisecond)
		if err := a.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
		for _, h := range hs {
			if _, err := h.Wait(); err != nil {
				t.Errorf("sender: %v", err)
			}
		}
		for i := range closed {
			if n := closed[i].Load(); n != 1 {
				t.Fatalf("message %d released %d times, want 1", i, n)
			}
		}
		t.Logf("handled %d, refused %d, drained %d", handled.Load(), refused.Load(), senders*each-int(handled.Load())-int(refused.Load()))
		return nil
	})
}

func TestInterruptedAskerDetaches(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		gate, holding := make(chan struct{}), make(chan struct{})
		a := gated(s, 4, gate, holding)
		addr := a.Address()
		asker := rt.Go(s, "asker", func(ct *rt.Task) (int, error) {
			return Ask(ct, addr, func(r Reply[int, Never]) probe { return probe{reply: r} })
		})
		<-holding
		asker.Task().Interrupt()
		if _, err := asker.Wait(); !errors.Is(err, rt.Interrupted) {
			t.Errorf("interrupted asker: got %v, want an interruption", err)
		}
		close(gate) // the actor answers the detached caller without blocking
		v, err := Ask(task, addr, func(r Reply[int, Never]) probe { return probe{reply: r} })
		if err != nil || v != 1 {
			t.Errorf("next Ask: got %d, %v; want 1, nil", v, err)
		}
		return a.Stop()
	})
}

type boom struct{}

func (boom) Error() string { return "boom" }

func TestHandlerDefectAnswersNoReplyThenStopped(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		gate := make(chan struct{})
		a := Start(s, "defect", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			defer m.Close()
			<-gate
			panic("handler bug")
		})
		addr := a.Address()
		r0, slot0 := replyTo(addr)
		r1, slot1 := replyTo(addr)
		if err := addr.Send(task, probe{reply: r0}); err != nil {
			return err
		}
		if err := addr.Send(task, probe{reply: r1}); err != nil {
			return err
		}
		close(gate)
		if o := <-slot0; !failureIs[NoReply](resultErr(o)) {
			t.Errorf("in-flight request of a defect: got %v, want NoReply", resultErr(o))
		}
		if o := <-slot1; !failureIs[Stopped](resultErr(o)) {
			t.Errorf("queued request after a defect: got %v, want Stopped", resultErr(o))
		}
		err := a.Stop()
		var c *rt.Cause
		if !errors.As(err, &c) || len(c.Reasons) != 1 || c.Reasons[0].Kind != rt.KindDefect {
			t.Errorf("Stop after a defect: got %v, want the defect alone", err)
		}
		return nil
	})
}

func TestHandlerFailureEndsTheActor(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "fail", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			m.Close()
			return Next[int]{}, boom{}
		})
		if err := a.Send(task, probe{}); err != nil {
			return err
		}
		<-a.addr.c.stopping // the actor has ended by itself; Stop must not race it
		if err := a.Stop(); !failureIs[boom](err) {
			t.Errorf("Stop: got %v, want boom", err)
		}
		return nil
	})
}

// state is a resource the actor owns.
type state struct{ released *atomic.Int32 }

func (s state) Close() { s.released.Add(1) }

func TestOwnerScopeStopsTheActorAndReleasesItsState(t *testing.T) {
	var released atomic.Int32
	var addr Address[probe]
	within(t, func(task *rt.Task, s *rt.Scope) error {
		_, err := rt.Scoped(task, nil, func(inner *rt.Scope) (struct{}, error) {
			a := Start(inner, "scoped", state{&released}, 4, func(ht *rt.Task, st state, m probe) (Next[state], error) {
				defer m.Close()
				m.reply.Send(ht, 0)
				return Continue(st), nil
			})
			addr = a.Address()
			_, err := Ask(task, addr, func(r Reply[int, Never]) probe { return probe{reply: r} })
			return struct{}{}, err
		})
		if err != nil {
			return err
		}
		if err := addr.Send(task, probe{}); !failureIs[Stopped](err) {
			t.Errorf("Send after the owner scope ended: got %v, want Stopped", err)
		}
		return nil
	})
	if n := released.Load(); n != 1 {
		t.Errorf("state released %d times, want 1", n)
	}
}

func TestEndReleasesStateAndRefusesLaterSends(t *testing.T) {
	var released atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "end", state{&released}, 4, func(ht *rt.Task, st state, m probe) (Next[state], error) {
			defer m.Close()
			m.reply.Send(ht, 0)
			return End(st), nil
		})
		if _, err := Ask(task, a.Address(), func(r Reply[int, Never]) probe { return probe{reply: r} }); err != nil {
			return err
		}
		if err := a.Stop(); err != nil {
			t.Errorf("Stop after End: %v", err)
		}
		if err := a.Send(task, probe{}); !failureIs[Stopped](err) {
			t.Errorf("Send after End: got %v, want Stopped", err)
		}
		return nil
	})
	if n := released.Load(); n != 1 {
		t.Errorf("state released %d times, want 1", n)
	}
}

// get lowers `func(n *int) int { return *n }`.
func get(_ *rt.Task, n *int) (int, error) { return *n, nil }

func TestAgentCallsAreOrderedAndQueuedCallsStop(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "agent", 0, 8)
		addr := ag.Address()
		for i := range 100 {
			v, err := Call(task, addr, func(ht *rt.Task, n *int) (int, error) { old := *n; *n++; return old, nil })
			if err != nil || v != i {
				t.Fatalf("call %d: got %d, %v", i, v, err)
			}
		}
		full := func(ht *rt.Task, n *int) (int, error) {
			if *n >= 100 {
				return 0, boom{} // fails before it changes the state
			}
			*n++
			return *n, nil
		}
		if _, err := Call(task, addr, full); !failureIs[boom](err) {
			t.Errorf("failing call: got %v, want boom", err)
		}
		if v, _ := Call(task, addr, get); v != 100 {
			t.Errorf("state after the failing call: got %d, want 100", v)
		}
		holding := make(chan struct{})
		blocker := rt.Go(s, "blocker", func(ct *rt.Task) (int, error) {
			return Call(ct, addr, func(at *rt.Task, n *int) (int, error) {
				close(holding)
				<-at.Done() // a wait of the callback observes the agent's stop (I1)
				return 0, rt.Interrupted
			})
		})
		<-holding
		queued := rt.Go(s, "queued", func(ct *rt.Task) (int, error) { return Call(ct, addr, get) })
		waitQueued(t, addr, 1)
		if err := ag.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
		if _, err := blocker.Wait(); !failureIs[Stopped](err) {
			t.Errorf("in-flight call interrupted by the stop: got %v, want Stopped", err)
		}
		if _, err := queued.Wait(); !failureIs[Stopped](err) {
			t.Errorf("queued call: got %v, want Stopped", err)
		}
		if _, err := Call(task, addr, get); !failureIs[Stopped](err) {
			t.Errorf("Call after Stop: got %v, want Stopped", err)
		}
		return nil
	})
}

// A defect in a callback is a defect of the agent, not a refusal: the caller
// gets NoReply, a queued caller gets Stopped, and Stop returns the defect.
func TestAgentCallbackDefectIsADefect(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "agent-defect", 0, 8)
		addr := ag.Address()
		gate, holding := make(chan struct{}), make(chan struct{})
		first := rt.Go(s, "first", func(ct *rt.Task) (int, error) {
			return Call(ct, addr, func(ht *rt.Task, n *int) (int, error) {
				close(holding)
				<-gate
				panic("callback bug")
			})
		})
		<-holding
		second := rt.Go(s, "second", func(ct *rt.Task) (int, error) { return Call(ct, addr, get) })
		waitQueued(t, addr, 1)
		close(gate)
		if _, err := first.Wait(); !failureIs[NoReply](err) {
			t.Errorf("caller of a panicking callback: got %v, want NoReply", err)
		}
		if _, err := second.Wait(); !failureIs[Stopped](err) {
			t.Errorf("queued caller after a defect: got %v, want Stopped", err)
		}
		err := ag.Stop()
		var c *rt.Cause
		if !errors.As(err, &c) || len(c.Reasons) != 1 || c.Reasons[0].Kind != rt.KindDefect {
			t.Errorf("Stop after a callback defect: got %v, want the defect alone", err)
		}
		return nil
	})
}

// item carries a slice: a value with mutable storage that crosses the actor
// boundary.
type item struct {
	tags  []string
	reply Reply[[]string, Never]
}

func (m item) Close() { m.reply.Close() }

// TestBoundaryCopiesMessagesAndReplies checks the copy rule at each crossing:
// a message the sender changes after Send, a reply the asker changes, and an
// agent result the caller changes never reach the actor's state.
func TestBoundaryCopiesMessagesAndReplies(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "copies", []string(nil), 4, func(ht *rt.Task, st []string, m item) (Next[[]string], error) {
			defer m.Close()
			st = append(st, m.tags...)
			m.reply.Send(ht, st)
			return Continue(st), nil
		})
		tags := []string{"a"}
		got, err := Ask(task, a.Address(), func(r Reply[[]string, Never]) item { return item{tags: tags, reply: r} })
		if err != nil {
			return err
		}
		tags[0] = "sender changed"
		got[0] = "asker changed"
		again, err := Ask(task, a.Address(), func(r Reply[[]string, Never]) item { return item{reply: r} })
		if err != nil {
			return err
		}
		if len(again) != 1 || again[0] != "a" {
			t.Errorf("actor state after the caller changed its copies: got %q, want [a]", again)
		}
		if err := a.Stop(); err != nil {
			return err
		}

		ag := StartAgent(s, "agent-copies", []string{"x"}, 4)
		names := func(ht *rt.Task, st *[]string) ([]string, error) { return *st, nil }
		out, err := Call(task, ag.Address(), names)
		if err != nil {
			return err
		}
		out[0] = "caller changed"
		if out, _ := Call(task, ag.Address(), names); out[0] != "x" {
			t.Errorf("agent state after the caller changed the result: got %q, want [x]", out)
		}
		return ag.Stop()
	})
}

// resultErr is the exit an Ask would return for o.
func resultErr[T any, E error](o outcome[T, E]) error {
	_, err := o.result()
	return err
}
