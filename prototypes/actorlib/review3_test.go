package actorlib

import (
	"errors"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// Round-3 review probes (round-3-findings.md), kept as tests, and the class
// tests for the one release path (drop): every release site runs masked,
// records a panic as an in-release defect, dispatches on the value it holds
// and releases by outcome kind.

// A resource union lowers to an interface that includes its drop method.
// *r3State has no Close method, while the value held by r3State does.
type r3State interface {
	Close()
	r3State()
}

func (lease) r3State() {}

// M2: the agent releases the S its storage holds on each of its five exits.
func TestR3AgentUnionStateReleasedOnEveryExit(t *testing.T) {
	for _, mode := range []string{"stop", "callback-panic", "callback-cause", "typed-failure", "owner-scope"} {
		t.Run(mode, func(t *testing.T) {
			var closed atomic.Int32
			within(t, func(task *rt.Task, s *rt.Scope) error {
				var initial r3State = lease{&closed}
				ag := StartAgent(s, "union-state", initial, 4)
				if mode == "owner-scope" {
					return nil
				}
				if mode != "stop" {
					_, err := Call(task, ag.Address(), func(at *rt.Task, st *r3State) (int, error) {
						switch mode {
						case "callback-panic":
							panic("union callback panic")
						case "callback-cause":
							var c rt.Cause
							c.AddDefect(at, "union callback cause")
							return 0, c.Err()
						default:
							return 0, boom{}
						}
					})
					if mode == "typed-failure" && !failureIs[boom](err) {
						t.Errorf("typed failure: %v", err)
					}
					if mode != "typed-failure" && !failureIs[NoReply](err) {
						t.Errorf("defect refusal: %v", err)
					}
				}
				err := ag.Stop()
				if (mode == "callback-panic" || mode == "callback-cause") && err == nil {
					t.Error("lost callback defect")
				}
				return nil
			})
			if got := closed.Load(); got != 1 {
				t.Errorf("%s released union resource state %d times; want 1", mode, got)
			}
		})
	}
}

// A Close uses the same poll an emitted wait or loop would use. Its release
// must run masked even when cancellation is what causes the release.
type r3PollingLease struct {
	task   *rt.Task
	closed *atomic.Int32
}

func (l r3PollingLease) Close() {
	l.task.Check()
	l.closed.Add(1)
}

// M1: the receiver's release of a ready answer runs masked.
func TestR3CanceledReplyReleaseRunsMasked(t *testing.T) {
	var closed atomic.Int32
	task := rt.Root("canceled-release-mask")
	sl := newSlot[r3PollingLease, Never](&control{task: task})
	sl.answer(task, outcome[r3PollingLease, Never]{v: r3PollingLease{task, &closed}})
	task.Interrupt()
	_, err := rt.Run(task, func(ct *rt.Task) (r3PollingLease, error) { return sl.receive(ct) })
	if !errors.Is(err, rt.Interrupted) {
		t.Errorf("exit: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Errorf("canceled reply release completed %d times; want 1 (release must be masked)", got)
	}
}

// A release defect must retain its InRelease bit beside the interruption.
type r3PanickingLease struct{}

func (r3PanickingLease) Close() { panic("answer release") }

// M1: a panic in the receiver's release is an in-release defect, before the
// interruption.
func TestR3CanceledReplyDefectIsInRelease(t *testing.T) {
	task := rt.Root("canceled-release-kind")
	sl := newSlot[r3PanickingLease, Never](&control{task: task})
	sl.answer(task, outcome[r3PanickingLease, Never]{v: r3PanickingLease{}})
	task.Interrupt()
	_, err := rt.Run(task, func(ct *rt.Task) (r3PanickingLease, error) { return sl.receive(ct) })
	var c *rt.Cause
	if !errors.As(err, &c) || len(c.Reasons) != 2 {
		t.Fatalf("exit: %v", err)
	}
	if c.Reasons[0].Kind != rt.KindDefect || !c.Reasons[0].InRelease || c.Reasons[1].Kind != rt.KindInterruption {
		t.Errorf("got %+v; want release defect then interruption", c.Reasons)
	}
}

type r3DefectiveState struct{ closed *atomic.Int32 }

func (st r3DefectiveState) Close() {
	st.closed.Add(1)
	panic("state release defect")
}

// Positive control for M4/M5: a callback's typed failure and nested release
// defects precede the state release, with kinds and release marks intact.
func TestR3CanonicalAgentExitKeepsCauseOrder(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "canonical-agent-exit", r3DefectiveState{&closed}, 4)
		_, err := Call(task, ag.Address(), func(at *rt.Task, _ *r3DefectiveState) (v int, err error) {
			outer := rt.NewScope(at)
			defer outer.End(&err)
			outer.Defer(func() {
				inner := rt.NewScope(at)
				defer inner.End(nil)
				inner.Defer(func() { panic("inner release one") })
				inner.Defer(func() { panic("inner release two") })
			})
			return 0, boom{}
		})
		if !failureIs[NoReply](err) {
			t.Errorf("caller: %v", err)
		}
		err = ag.Stop()
		var c *rt.Cause
		if !errors.As(err, &c) || len(c.Reasons) != 4 {
			t.Fatalf("exit: %v", err)
		}
		if c.Reasons[0].Kind != rt.KindFailure {
			t.Errorf("first cause is %+v; want typed failure", c.Reasons[0])
		}
		for i, want := range []string{"inner release two", "inner release one", "state release defect"} {
			r := c.Reasons[i+1]
			if r.Kind != rt.KindDefect || !r.InRelease || r.Value != want {
				t.Errorf("cause %d: %+v; want release defect %q", i+1, r, want)
			}
		}
		return nil
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("state released %d times; want 1", got)
	}
}

// The same resource-result input as the matched-baseline probe succeeds on
// the library path: its eventual refusal carries no value to close.
func TestR3LibraryResourceResultControl(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "resource-result-control", 0, 1)
		v, err := Call(task, ag.Address(), func(_ *rt.Task, _ *int) (lease, error) {
			return lease{&closed}, nil
		})
		if err != nil {
			return err
		}
		v.Close()
		return ag.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("resource result closed %d times; want 1", got)
	}
}

// These Gap 47 regressions use Scoped's explicit result release. A raw
// rt.Run or deferred Scope.End has no generic result release transport;
// callers with owned results must use the result-aware boundary.

// m1: rt.Run zeros a taken answer when the asker is stopped before its
// boundary.
func TestR3Gap47TaskBoundaryDropsTakenAnswer(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "gap47", 0, 1, func(ht *rt.Task, n int, m leaseRequest) (Next[int], error) {
			defer m.Close()
			m.reply.Send(ht, lease{&closed})
			return Continue(n), nil
		})
		asker := rt.Go(s, "taken-answer", func(ct *rt.Task) (lease, error) {
			return rt.Scoped(ct, lease.Close, func(*rt.Scope) (lease, error) {
				v, err := Ask(ct, a.Address(), func(r Reply[lease, Never]) leaseRequest { return leaseRequest{r} })
				if err != nil {
					return v, err
				}
				ct.Interrupt()
				return v, nil
			})
		})
		if _, err := asker.Wait(); !errors.Is(err, rt.Interrupted) {
			t.Errorf("asker exit: %v", err)
		}
		return a.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("Gap 47: scoped task body released the taken answer %d times; want 1", got)
	}
}

// m1: use Scoped instead of a bare deferred End so Call never receives an
// owned candidate beside an interruption. Call must not close failed values.
func TestR3Gap47AgentInterruptedCallbackResult(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "interrupted-result", 0, 1)
		_, err := Call(task, ag.Address(), func(at *rt.Task, _ *int) (lease, error) {
			return rt.Scoped(at, lease.Close, func(local *rt.Scope) (lease, error) {
				local.Defer(func() { at.Interrupt() })
				return lease{&closed}, nil
			})
		})
		if !failureIs[Stopped](err) {
			t.Errorf("caller: %v", err)
		}
		return ag.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("Gap 47: interrupted callback result released %d times; want 1", got)
	}
}

// m1: rt.Scoped zeros a candidate result when a release requests a stop.
func TestR3Gap47CanonicalScopedDropsResultOnStop(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		ag := StartAgent(s, "scoped-result", 0, 1)
		_, err := Call(task, ag.Address(), func(at *rt.Task, _ *int) (lease, error) {
			return rt.Scoped(at, lease.Close, func(local *rt.Scope) (lease, error) {
				local.Defer(func() { at.Interrupt() })
				return lease{&closed}, nil
			})
		})
		if !failureIs[Stopped](err) {
			t.Errorf("caller: %v", err)
		}
		return ag.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("Gap 47: rt.Scoped discarded result released %d times; want 1", got)
	}
}

// selfStopping stops the task that releases it, then polls. A release that
// is not masked unwinds at the poll and never counts; a masked one counts.
type selfStopping struct {
	closed *atomic.Int32
	task   *atomic.Pointer[rt.Task] // the releasing task, set by the test
}

func (l selfStopping) Close() {
	t := l.task.Load()
	t.Interrupt()
	t.Check()
	l.closed.Add(1)
}

type selfStoppingMsg struct{ l selfStopping }

func (m selfStoppingMsg) Close() { m.l.Close() }

type selfStoppingFailure struct{ selfStopping }

func (selfStoppingFailure) Error() string { return "owned failure" }

// The class test for decision 1: each library release site, given a
// resource that stops its releasing task and polls, completes the release
// once. Sites whose releasing task is the test's own task run it inside
// rt.Run on a fresh root.
func TestEveryReleaseSiteRunsMasked(t *testing.T) {
	sites := []struct {
		name string
		run  func(t *testing.T, task *rt.Task, l selfStopping)
	}{
		{"refused Send of a stopped sender", func(t *testing.T, task *rt.Task, l selfStopping) {
			task.Interrupt()
			a := Address[selfStoppingMsg]{&cell[selfStoppingMsg]{control: control{task: task}, mailbox: make(chan selfStoppingMsg, 1)}}
			if err := a.Send(task, selfStoppingMsg{l}); !errors.Is(err, rt.Interrupted) {
				t.Errorf("Send: %v", err)
			}
		}},
		{"Send refused by a stopped actor", func(t *testing.T, task *rt.Task, l selfStopping) {
			c := &cell[selfStoppingMsg]{control: control{task: task}, mailbox: make(chan selfStoppingMsg, 1)}
			c.stopping = make(chan struct{})
			c.closeAdmission()
			if err := (Address[selfStoppingMsg]{c}).Send(task, selfStoppingMsg{l}); !failureIs[Stopped](err) {
				t.Errorf("Send: %v", err)
			}
		}},
		{"late answer to a detached asker", func(t *testing.T, task *rt.Task, l selfStopping) {
			sl := newSlot[selfStopping, Never](&control{task: task})
			var err error
			sl.abandon(task, &err)
			Reply[selfStopping, Never]{sl}.Send(task, l)
		}},
		{"second answer", func(t *testing.T, task *rt.Task, l selfStopping) {
			sl := newSlot[selfStopping, Never](&control{task: task})
			Reply[selfStopping, Never]{sl}.Send(task, selfStopping{closed: new(atomic.Int32), task: l.task})
			Reply[selfStopping, Never]{sl}.Send(task, l)
			var err error
			sl.abandon(task, &err) // releases the first: it counts on its own counter
		}},
		{"abandon of an answered slot", func(t *testing.T, task *rt.Task, l selfStopping) {
			sl := newSlot[selfStopping, Never](&control{task: task})
			Reply[selfStopping, Never]{sl}.Send(task, l)
			var err error
			sl.abandon(task, &err)
		}},
		{"failure payload of a stopped receiver", func(t *testing.T, task *rt.Task, l selfStopping) {
			sl := newSlot[int, selfStoppingFailure](&control{task: task})
			Reply[int, selfStoppingFailure]{sl}.Fail(task, selfStoppingFailure{l})
			task.Interrupt()
			if _, err := sl.receive(task); !errors.Is(err, rt.Interrupted) {
				t.Errorf("receive: %v", err)
			}
		}},
		{"Send on a zero reply", func(t *testing.T, task *rt.Task, l selfStopping) {
			Reply[selfStopping, Never]{}.Send(task, l)
		}},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			var closed atomic.Int32
			var releaser atomic.Pointer[rt.Task]
			task := rt.Root(site.name)
			releaser.Store(task)
			_, _ = rt.Run(task, func(task *rt.Task) (struct{}, error) {
				site.run(t, task, selfStopping{&closed, &releaser})
				return struct{}{}, nil
			})
			if got := closed.Load(); got != 1 {
				t.Errorf("release completed %d times; want 1 (masked, to completion)", got)
			}
		})
	}
}

// The drain and the agent's state release run on the actor's task, masked,
// and a queued message's release that stops the actor's task completes.
func TestDrainReleasesRunMasked(t *testing.T) {
	var closed atomic.Int32
	var releaser atomic.Pointer[rt.Task]
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate := make(chan struct{}), make(chan struct{})
		first := true
		a := Start(s, "drain-mask", 0, 4, func(ht *rt.Task, n int, m selfStoppingMsg) (Next[int], error) {
			if first {
				first = false
				close(holding)
				select {
				case <-gate:
				case <-ht.Done():
				}
			}
			m.Close()
			return Continue(n), nil
		})
		releaser.Store(a.h.Task())
		if err := a.Send(task, selfStoppingMsg{selfStopping{new(atomic.Int32), &releaser}}); err != nil {
			return err
		}
		<-holding
		if err := a.Send(task, selfStoppingMsg{selfStopping{&closed, &releaser}}); err != nil {
			return err
		}
		return a.Stop()
	})
	if got := closed.Load(); got != 1 {
		t.Errorf("queued message released %d times; want 1", got)
	}
}

// A panic in the release of a late answer is an in-release defect of the
// answering task: the answerer unwinds, and rt records the defect with its
// kind and InRelease mark.
func TestLateAnswerReleaseDefectIsInRelease(t *testing.T) {
	task := rt.Root("late-answer-defect")
	sl := newSlot[r3PanickingLease, Never](&control{task: task})
	var err error
	sl.abandon(task, &err)
	_, err = rt.Run(task, func(ct *rt.Task) (struct{}, error) {
		Reply[r3PanickingLease, Never]{sl}.Send(ct, r3PanickingLease{})
		return struct{}{}, nil
	})
	var c *rt.Cause
	if !errors.As(err, &c) || len(c.Reasons) != 1 || c.Reasons[0].Kind != rt.KindDefect || !c.Reasons[0].InRelease {
		t.Errorf("exit: %+v; want one in-release defect", err)
	}
}

// A refused Send releases its message on the sender's task: the release
// defect follows the refusal in the sender's exit.
func TestRefusedSendReleaseDefectFollowsRefusal(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		a := Start(s, "refused-defect", 0, 1, func(_ *rt.Task, n int, m r3PanickingLease) (Next[int], error) {
			return Continue(n), nil
		})
		if err := a.Stop(); err != nil {
			return err
		}
		err := a.Send(task, r3PanickingLease{})
		var c *rt.Cause
		if !errors.As(err, &c) || len(c.Reasons) != 2 {
			t.Fatalf("Send: %v", err)
		}
		if !failureIs[Stopped](c.Reasons[0].Err) || c.Reasons[1].Kind != rt.KindDefect || !c.Reasons[1].InRelease {
			t.Errorf("got %+v; want Stopped then an in-release defect", c.Reasons)
		}
		return nil
	})
}

// A refusal owns nothing: a successful resource answer followed by the
// handler's automatic refusal closes nothing more, and a zero T is never
// closed (a zero lease would panic through its nil counter).
func TestRefusalReleasesNothing(t *testing.T) {
	task := rt.Root("refusal-owns-nothing")
	sl := newSlot[lease, Never](&control{task: task})
	sl.refuse()
	sl.refuse()
	Reply[lease, Never]{sl}.Close()
	var err error
	sl.abandon(task, &err)
	if err != nil {
		t.Errorf("abandon of a refused slot: %v", err)
	}
}

// A machine's queued event is released as its variant when the machine
// stops; an event the step received is the step's.
func TestMachineQueuedEventIsReleased(t *testing.T) {
	var closed atomic.Int32
	within(t, func(task *rt.Task, s *rt.Scope) error {
		holding, gate := make(chan struct{}), make(chan struct{})
		first := true
		m := Machine[int, lease, int]{Step: func(st *rt.Task, n int, e lease) (Decision[int, int], error) {
			defer e.Close() // the step owns the event
			if first {
				first = false
				close(holding)
				select {
				case <-gate:
				case <-st.Done():
				}
			}
			return Ignore[int, int]{}, nil
		}}
		b := StartMachine(s, "event-release", m, 4)
		if err := b.Send(task, lease{&closed}); err != nil {
			return err
		}
		<-holding
		if err := b.Send(task, lease{&closed}); err != nil {
			return err
		}
		return b.Stop()
	})
	if got := closed.Load(); got != 2 {
		t.Errorf("events released %d times; want 2 (one by the step, one by the drain)", got)
	}
}
