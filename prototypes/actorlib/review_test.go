package actorlib

// The round-1 review probes (research note, Q2, Q5, Q6, Q7 and Q8), kept as
// regression tests. They are written against the round-2 API: an agent
// callback takes the agent's task and a lent *S.

import (
	"errors"
	"runtime"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// lease is a resource a reply carries.
type lease struct{ closed *atomic.Int32 }

func (v lease) Close() { v.closed.Add(1) }

type leaseRequest struct{ reply Reply[lease, Never] }

func (m leaseRequest) Close() { m.reply.Close() }

// M1: a reply nobody can receive is released, exactly once.
func TestDetachedResourceReplyIsReleased(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate, answered := make(chan struct{}), make(chan struct{}), make(chan struct{})
		a := Start(s, "resource-reply", 0, 4, func(ht *rt.Task, n int, m leaseRequest) (Next[int], error) {
			defer m.Close()
			close(holding)
			<-gate
			m.reply.Send(ht, lease{&closed})
			close(answered)
			return Continue(n), nil
		})
		asker := rt.Go(s, "asker", func(ct *rt.Task) (lease, error) {
			return Ask(ct, a.Address(), func(r Reply[lease, Never]) leaseRequest { return leaseRequest{r} })
		})
		<-holding
		asker.Task().Interrupt()
		if _, err := asker.Wait(); !errors.Is(err, rt.Interrupted) {
			t.Fatalf("asker: %v", err)
		}
		close(gate)
		<-answered
		return a.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("late resource reply released %d times; want 1", got)
	}
}

// M1: an answer that lands before the asker is interrupted, and is not yet
// received, is released by the asker's detach.
func TestAnsweredButUnreceivedReplyIsReleased(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "early-reply", 0, 4, func(ht *rt.Task, n int, m leaseRequest) (Next[int], error) {
			defer m.Close()
			return Continue(n), nil
		})
		sl := newSlot[lease, Never](&a.addr.c.control)
		Reply[lease, Never]{sl}.Send(task, lease{&closed})
		var err error
		sl.abandon(task, &err)
		if err != nil {
			return err
		}
		Reply[lease, Never]{sl}.Send(task, lease{&closed}) // a second answer is released too
		return a.Stop()
	})
	if got := closed.Load(); got != 2 {
		t.Errorf("released %d unreceived replies; want 2", got)
	}
}

func TestDetachedAgentResultIsReleased(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate := make(chan struct{}), make(chan struct{})
		a := StartAgent(s, "resource-agent-result", 0, 4)
		asker := rt.Go(s, "asker", func(ct *rt.Task) (lease, error) {
			return Call(ct, a.Address(), func(ht *rt.Task, n *int) (lease, error) {
				close(holding)
				<-gate
				return lease{&closed}, nil
			})
		})
		<-holding
		asker.Task().Interrupt()
		if _, err := asker.Wait(); !errors.Is(err, rt.Interrupted) {
			t.Fatalf("asker: %v", err)
		}
		close(gate)
		return a.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("late agent result released %d times; want 1", got)
	}
}

// M5: a callback may block, and its wait observes the agent's stop, because
// it runs on the agent's task and receives it.
func TestAgentChannelWaitObservesStop(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate := make(chan struct{}), make(chan struct{})
		var forced atomic.Bool
		a := StartAgent(s, "channel-agent", 0, 4)
		asker := rt.Go(s, "asker", func(ct *rt.Task) (int, error) {
			return Call(ct, a.Address(), func(at *rt.Task, n *int) (int, error) {
				close(holding)
				select { // the lowering of a channel receive: an interruption point
				case <-gate:
					return *n, nil
				case <-at.Done():
					return 0, rt.Interrupted
				}
			})
		})
		<-holding
		stopWatchdog := testwait.AfterFunc(t, func() {
			forced.Store(true)
			close(gate)
		})
		if err := a.Stop(); err != nil {
			return err
		}
		stopWatchdog()
		if forced.Load() {
			t.Error("Stop could not interrupt the callback's channel wait")
		}
		if _, err := asker.Wait(); !failureIs[Stopped](err) {
			t.Errorf("caller of an interrupted callback: got %v, want Stopped", err)
		}
		return nil
	})
}

// M2: after the stop is requested, no Send succeeds, even while the handler
// still runs.
func TestStopRefusesAdmissionBeforeHandlerFinishes(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate := make(chan struct{}), make(chan struct{})
		a := Start(s, "stopping", 0, 4, func(ht *rt.Task, n int, m probe) (Next[int], error) {
			defer m.Close()
			close(holding)
			<-gate
			return Continue(n), nil
		})
		addr := a.Address()
		if err := addr.Send(task, probe{}); err != nil {
			return err
		}
		<-holding
		sender := rt.Go(s, "sender-after-stop-request", func(ct *rt.Task) (struct{}, error) {
			for !addr.c.task.StopRequested() {
				runtime.Gosched()
			}
			err := addr.Send(ct, probe{})
			close(gate)
			return struct{}{}, err
		})
		if err := a.Stop(); err != nil {
			return err
		}
		if _, err := sender.Wait(); !failureIs[Stopped](err) {
			t.Errorf("Send after stop request returned %v; want Stopped", err)
		}
		return nil
	})
}

// M3: a state query returns a copy, never an alias of the state.
func TestMachineStateIsASnapshot(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		m := Machine[[]int64, int64, struct{}]{
			Initial: []int64{1},
			Step: func(ht *rt.Task, _ []int64, _ int64) (Decision[[]int64, struct{}], error) {
				return Ignore[[]int64, struct{}]{}, nil
			},
		}
		a := StartMachine(s, "snapshot", m, 4)
		first, err := a.State(task)
		if err != nil {
			return err
		}
		first[0] = 99
		second, err := a.State(task)
		if err != nil {
			return err
		}
		if second[0] != 1 {
			t.Errorf("snapshot mutation changed actor state to %d; want 1", second[0])
		}
		return a.Stop()
	})
}

type cleanupMessage struct {
	panicValue string
	closed     *atomic.Int32
}

func (m cleanupMessage) Close() {
	m.closed.Add(1)
	if m.panicValue != "" {
		panic(m.panicValue)
	}
}

// M4: the drain keeps every release defect, in order, and still releases the
// state.
func TestDrainKeepsAllDefectsAndReleasesState(t *testing.T) {
	var stateClosed, messagesClosed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding := make(chan struct{})
		a := Start(s, "drain-defects", lease{&stateClosed}, 4, func(ct *rt.Task, st lease, m cleanupMessage) (Next[lease], error) {
			defer m.Close()
			close(holding)
			<-ct.Done()
			return Continue(st), nil
		})
		if err := a.Send(task, cleanupMessage{closed: &messagesClosed}); err != nil {
			return err
		}
		<-holding
		order := []string{"first release defect", "second release defect"}
		for _, p := range order {
			if err := a.Send(task, cleanupMessage{panicValue: p, closed: &messagesClosed}); err != nil {
				return err
			}
		}
		err := a.Stop()
		var cause *rt.Cause
		var defects []string
		if errors.As(err, &cause) {
			for _, r := range cause.Reasons {
				if r.Kind == rt.KindDefect {
					if !r.InRelease {
						t.Errorf("defect %v is not marked as a release defect", r.Value)
					}
					defects = append(defects, r.Value.(string))
				}
			}
		}
		if len(defects) != 2 || defects[0] != order[0] || defects[1] != order[1] {
			t.Errorf("Stop kept defects %q; want %q in order (exit %v)", defects, order, err)
		}
		if got := messagesClosed.Load(); got != 3 {
			t.Errorf("released %d messages; want 3", got)
		}
		return nil
	})
	if got := stateClosed.Load(); got != 1 {
		t.Errorf("state released %d times; want 1", got)
	}
}
