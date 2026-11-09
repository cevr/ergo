package v1

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// The laws of docs/research/lawful-runtime-contract.md (#124) that apply to
// task.Go, Task.Wait, Task.Stop and scope.OnExit, as executable tests over
// frames written the way the lowering writes them (frame, frameValue).
// docs/spec/runtime.md, "Runtime laws for task, scope and queue", maps each
// law to its test.

// bad is a typed failure.
type bad struct{ at string }

func (b bad) Error() string { return "bad " + b.at }

// frame is a lowered frame with an error row: its scope's End is its first
// defer and reads its named error result.
func frame(t *rt.Task, body func(s *Scope) error) (err error) {
	s := NewScope(t)
	defer s.End(&err)
	return body(&s)
}

// frameValue is a lowered frame with a value and an error row.
func frameValue[T any](t *rt.Task, body func(s *Scope) (T, error)) (v T, err error) {
	s := NewScope(t)
	defer s.End(&err)
	return body(&s)
}

// frameNoRow is a lowered frame with no error row.
func frameNoRow(t *rt.Task, body func(s *Scope)) {
	s := NewScope(t)
	defer s.End(nil)
	body(&s)
}

// explode is a Go defect at a known frame: an index out of range.
//
//go:noinline
func explode() int {
	var xs []int
	i := 3
	return xs[i]
}

// sleepUntilStopped waits on t until a stop reaches it, then raises the
// interruption, as a lowered Sleep does.
func sleepUntilStopped(t *rt.Task) {
	if err := rt.Sleep(t, rt.Live{}, time.Hour); err != nil {
		raise(err)
	}
}

// recorder is a trace that several goroutines append to.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.events, " ")
}

// recovered runs f and returns its panic value, or nil.
func recovered(f func()) (p any) {
	defer func() { p = recover() }()
	f()
	return nil
}

// reasons is err's reasons, a lone exit as one reason.
func reasons(err error) []rt.Reason {
	var c rt.Cause
	c.Add(err)
	return c.Reasons
}

// The operations interfaces the lowering emits for Task[int, E] and
// queue.Queue[int]: the runtime values must be values of them.
var (
	_ interface {
		Wait() (int, error)
		Stop()
	} = (*Task[int])(nil)
)

// TestWait: Wait returns the value, and a lone typed failure as the error a
// catch dispatches on (Tasks 4; law R6).
func TestWait(t *testing.T) {
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			a := Go(s, "a", func(*rt.Task) (int, error) { return 7, nil })
			b := Go(s, "b", func(*rt.Task) (int, error) { return 0, bad{"b"} })
			if v, err := a.Wait(); v != 7 || err != nil {
				t.Errorf("a.Wait() = %d, %v; want 7, nil", v, err)
			}
			if v, err := b.Wait(); v != 0 || err != (bad{"b"}) {
				t.Errorf("b.Wait() = %d, %v; want 0, bad b", v, err)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("exit %v", err)
	}
}

// TestWaitRaisesDefect: a defect in a task re-raises in its waiter (Tasks
// 4), after the task's exit actions ran (law R15), with the chain of tasks
// (G2) and the stack at the panic, which names the panicking function.
func TestWaitRaisesDefect(t *testing.T) {
	var cleaned atomic.Bool
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			c := Go(s, "child at x.ergo:3:2", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(s *Scope) (int, error) {
					OnExit(s, func() { cleaned.Store(true) })
					return explode(), nil
				})
			})
			v, err := c.Wait()
			t.Errorf("Wait returned %d, %v; want it to raise the defect", v, err)
			return nil
		})
	})
	if !cleaned.Load() {
		t.Error("the task's exit action did not run")
	}
	rs := reasons(err)
	if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
		t.Fatalf("exit %v; want one defect", err)
	}
	p, ok := rs[0].Value.(*Panic)
	if !ok {
		t.Fatalf("defect value %T; want *Panic", rs[0].Value)
	}
	if _, ok := p.Value.(runtime.Error); !ok {
		t.Errorf("panic value %T; want a runtime.Error", p.Value)
	}
	if !strings.Contains(string(p.Stack), "v1.explode") || !strings.Contains(string(p.Stack), "task_test.go") {
		t.Errorf("stack does not name the panicking frame:\n%s", p.Stack)
	}
	if len(rs[0].Chain) != 2 || rs[0].Chain[0].Span != "child at x.ergo:3:2" || rs[0].Chain[1].Span != "main" {
		t.Errorf("chain %+v; want the child, then main", rs[0].Chain)
	}
}

// TestDefectChainAcrossTasks: a defect in a grandchild crosses two Waits as
// one defect, with the whole chain and the grandchild's stack.
func TestDefectChainAcrossTasks(t *testing.T) {
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			c := Go(s, "child", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(s *Scope) (int, error) {
					g := Go(s, "grandchild", func(*rt.Task) (int, error) { return explode(), nil })
					return g.Wait()
				})
			})
			_, err := c.Wait()
			return err
		})
	})
	rs := reasons(err)
	if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
		t.Fatalf("exit %v; want one defect", err)
	}
	var spans []string
	for _, f := range rs[0].Chain {
		spans = append(spans, f.Span)
	}
	if got := strings.Join(spans, " "); got != "grandchild child main" {
		t.Errorf("chain %q; want grandchild child main", got)
	}
	if p, ok := rs[0].Value.(*Panic); !ok || !strings.Contains(string(p.Stack), "v1.explode") {
		t.Errorf("defect value %#v does not hold the grandchild's stack", rs[0].Value)
	}
}

// TestWaitInterrupted: a waiter stopped while it waits stops and drains the
// task, raises the interruption, and delivers neither the task's value nor
// its typed failure (laws R4, R5).
func TestWaitInterrupted(t *testing.T) {
	var cleaned atomic.Bool
	started := make(chan struct{})
	err := runMain(func(root *rt.Task) error {
		go func() {
			<-started
			root.Interrupt()
		}()
		return frame(root, func(s *Scope) error {
			c := Go(s, "child", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(s *Scope) (int, error) {
					OnExit(s, func() {
						time.Sleep(5 * time.Millisecond)
						cleaned.Store(true)
					})
					close(started)
					if err := rt.Sleep(ct, rt.Live{}, time.Hour); err != nil {
						return 0, bad{"abandoned"} // an abandoned typed failure
					}
					return 1, nil
				})
			})
			p := recovered(func() {
				v, err := c.Wait()
				t.Errorf("Wait returned %d, %v; want it to raise the interruption", v, err)
			})
			if p != rt.Interrupted {
				t.Errorf("Wait raised %v; want rt.Interrupted", p)
			}
			if !cleaned.Load() {
				t.Error("Wait returned before the task's exit action ended")
			}
			panic(p)
		})
	})
	if err != rt.Interrupted {
		t.Errorf("exit %v; want the interruption alone", err)
	}
}

// TestStop: Stop interrupts the task and returns only after its exit
// actions ran (law R5); the task's interruption and abandoned typed failure
// are dropped (R3, R6), and its defect is raised in the caller.
func TestStop(t *testing.T) {
	var cleaned atomic.Bool
	started := make(chan struct{})
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			c := Go(s, "child", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(s *Scope) (int, error) {
					OnExit(s, func() {
						time.Sleep(5 * time.Millisecond)
						cleaned.Store(true)
					})
					close(started)
					if err := rt.Sleep(ct, rt.Live{}, time.Hour); err != nil {
						return 0, bad{"abandoned"}
					}
					return 1, nil
				})
			})
			<-started
			c.Stop()
			if !cleaned.Load() {
				t.Error("Stop returned before the task's exit action ended")
			}
			c.Stop() // a second Stop does nothing
			d := Go(s, "defect", func(dt *rt.Task) (int, error) {
				_ = rt.Sleep(dt, rt.Live{}, time.Hour)
				return explode(), nil
			})
			p := recovered(d.Stop)
			x, ok := p.(*exit)
			if !ok || len(reasons(x.err)) != 1 || reasons(x.err)[0].Kind != rt.KindDefect {
				t.Errorf("Stop of a task that panics raised %#v; want its defect", p)
			}
			return nil
		})
	})
	if err != nil {
		t.Errorf("exit %v; want success", err)
	}
}

// TestFrameReleaseOrder: a frame's exit actions run last registered first,
// once per call, a loop's once per iteration (law R1; Exit actions 1, 2),
// and a task the frame did not consume is stopped and drained, its own
// exit actions included, before an action registered before it (law R2).
func TestFrameReleaseOrder(t *testing.T) {
	var r recorder
	started := make(chan struct{})
	err := runMain(func(root *rt.Task) error {
		err := frame(root, func(s *Scope) error {
			OnExit(s, func() { r.add("A") })
			Go(s, "T", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(s *Scope) (int, error) {
					OnExit(s, func() {
						time.Sleep(5 * time.Millisecond)
						r.add("T")
					})
					close(started)
					sleepUntilStopped(ct)
					return 0, nil
				})
			})
			for i := range 3 {
				OnExit(s, func() { r.add(fmt.Sprint(i)) })
			}
			OnExit(s, func() { r.add("B") })
			<-started
			return nil
		})
		r.add("returned")
		return err
	})
	if err != nil {
		t.Fatalf("exit %v", err)
	}
	if got, want := r.String(), "B 2 1 0 T A returned"; got != want {
		t.Errorf("order %q; want %q", got, want)
	}
}

// TestExitActionReadsAtRun: an exit action reads its variables when it
// runs, not when it is registered (Exit actions 4).
func TestExitActionReadsAtRun(t *testing.T) {
	var got int
	frameNoRow(rt.Root("main"), func(s *Scope) {
		x := 1
		OnExit(s, func() { got = x })
		x = 2
		_ = x
	})
	if got != 2 {
		t.Errorf("the action read %d; want 2", got)
	}
}

// TestExitActionMasked: an exit action runs masked (I2): a stop requested
// before the frame ends is not observed inside it, and a wait in it runs
// to its end; the stop is observed after, and beats the frame's success
// (Exit causes 5).
func TestExitActionMasked(t *testing.T) {
	var stoppedInside, slept atomic.Bool
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			OnExit(s, func() {
				stoppedInside.Store(root.Stopped())
				slept.Store(rt.Sleep(root, rt.Live{}, 5*time.Millisecond) == nil)
			})
			root.Interrupt()
			return nil
		})
	})
	if stoppedInside.Load() || !slept.Load() {
		t.Errorf("inside the action: stopped %v, slept to the end %v; want false, true", stoppedInside.Load(), slept.Load())
	}
	if err != rt.Interrupted {
		t.Errorf("exit %v; want the interruption", err)
	}
}

// TestExitCauses: the frame keeps every reason (Exit causes 4; law R6): a
// typed failure with a defect of an exit action, a body defect with an
// action's defect, each defect with its own stack; and a lone typed failure
// stays one, for a catch.
func TestExitCauses(t *testing.T) {
	t.Run("failure and action defect", func(t *testing.T) {
		var err error
		p := recovered(func() {
			err = frame(rt.Root("main"), func(s *Scope) error {
				OnExit(s, func() { explode() })
				return bad{"body"}
			})
		})
		x, ok := p.(*exit)
		if !ok {
			t.Fatalf("frame returned %v and raised %#v; want it to raise the combined exit", err, p)
		}
		rs := reasons(x.err)
		if len(rs) != 2 || rs[0].Kind != rt.KindFailure || rs[0].Err != (bad{"body"}) || rs[1].Kind != rt.KindDefect || !rs[1].InRelease {
			t.Errorf("reasons %+v; want the failure, then the action's defect", rs)
		}
	})
	t.Run("body defect and action defect", func(t *testing.T) {
		p := recovered(func() {
			frameNoRow(rt.Root("main"), func(s *Scope) {
				OnExit(s, func() { panic("in the action") })
				explode()
			})
		})
		x, ok := p.(*exit)
		if !ok {
			t.Fatalf("raised %#v; want the combined exit", p)
		}
		rs := reasons(x.err)
		if len(rs) != 2 || rs[0].Kind != rt.KindDefect || rs[0].InRelease || rs[1].Kind != rt.KindDefect || !rs[1].InRelease {
			t.Fatalf("reasons %+v; want the body's defect, then the action's", rs)
		}
		body, _ := rs[0].Value.(*Panic)
		action, _ := rs[1].Value.(*Panic)
		if body == nil || !strings.Contains(string(body.Stack), "v1.explode") {
			t.Errorf("body defect %#v does not hold its stack", rs[0].Value)
		}
		if action == nil || action.Value != "in the action" || !strings.Contains(string(action.Stack), "TestExitCauses") {
			t.Errorf("action defect %#v does not hold its stack", rs[1].Value)
		}
	})
	t.Run("lone failure", func(t *testing.T) {
		var ran bool
		err := frame(rt.Root("main"), func(s *Scope) error {
			OnExit(s, func() { ran = true })
			return bad{"lone"}
		})
		if err != (bad{"lone"}) || !ran {
			t.Errorf("err %v, action ran %v; want bad lone, true", err, ran)
		}
	})
	t.Run("action re-raises a task's defect", func(t *testing.T) {
		p := recovered(func() {
			frameNoRow(rt.Root("main"), func(s *Scope) {
				c := Go(s, "child", func(*rt.Task) (int, error) { return explode(), nil })
				OnExit(s, func() { _, _ = c.Wait() })
			})
		})
		x, ok := p.(*exit)
		if !ok {
			t.Fatalf("raised %#v; want the exit", p)
		}
		rs := reasons(x.err)
		if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
			t.Fatalf("reasons %+v; want the child's defect, flat", rs)
		}
		if _, ok := rs[0].Value.(*Panic); !ok {
			t.Errorf("defect value %T; want the child's *Panic, not a nested exit", rs[0].Value)
		}
	})
}

// TestAbandonedChild: when a frame ends with a failure, a task it did not
// consume is stopped and drained; the task's typed failure is abandoned
// (law R3, the E2 policy), its defect is kept.
func TestAbandonedChild(t *testing.T) {
	started := make(chan struct{})
	err := frame(rt.Root("main"), func(s *Scope) error {
		Go(s, "child", func(ct *rt.Task) (int, error) {
			close(started)
			if err := rt.Sleep(ct, rt.Live{}, time.Hour); err != nil {
				return 0, bad{"child"}
			}
			return 0, nil
		})
		<-started
		return bad{"owner"}
	})
	if err != (bad{"owner"}) {
		t.Errorf("exit %v; want the owner's failure alone", err)
	}
	p := recovered(func() {
		started := make(chan struct{})
		_ = frame(rt.Root("main"), func(s *Scope) error {
			Go(s, "child", func(ct *rt.Task) (int, error) {
				close(started)
				_ = rt.Sleep(ct, rt.Live{}, time.Hour)
				return explode(), nil
			})
			<-started
			return bad{"owner"}
		})
	})
	x, ok := p.(*exit)
	if !ok {
		t.Fatalf("raised %#v; want the owner's failure with the child's defect", p)
	}
	rs := reasons(x.err)
	if len(rs) != 2 || rs[0].Err != (bad{"owner"}) || rs[1].Kind != rt.KindDefect {
		t.Errorf("reasons %+v; want the owner's failure, then the child's defect", rs)
	}
}

// TestGapR3NormalClose pins a named gap: at a normal frame end, a task the
// frame never consumed that already failed is dropped with its typed
// failure. Law R3 keeps that failure; rt drops it (#198 O1). #197 makes a
// dropped Task a compile error, which removes this case from checked
// programs. Change this test when either lands.
//
// The frame returns only after rt has finished the task (rt detaches a
// finished task from its parent), so the frame's release meets a task that
// already failed: the test fails if the frame keeps the failure (the gap
// closed), and it cannot pass through a stop of a task that still runs.
func TestGapR3NormalClose(t *testing.T) {
	err := frame(rt.Root("main"), func(s *Scope) error {
		c := Go(s, "child", func(*rt.Task) (int, error) { return 0, bad{"unobserved"} })
		if !testwait.Until(t, func() bool { return c.h.Task().Parent() == nil }) {
			t.Fatal("the child did not finish")
		}
		return nil
	})
	if err != nil {
		t.Errorf("exit %v; the gap drops the unobserved failure (update this test with #197/#124)", err)
	}
}

// start is a lowered function whose result is a Task: dest is the hidden
// destination argument, its caller's frame, and its return moves the Task
// there in one step. A nil dest stands for a lowering that forgot the move.
func start(t *rt.Task, dest *Scope, body func(*rt.Task) (int, error)) *Task[int] {
	c, _ := frameValue(t, func(s *Scope) (*Task[int], error) {
		c := Go(s, "returned", body)
		if dest == nil {
			return c, nil
		}
		return c.Move(dest), nil
	})
	return c
}

// TestTaskReturn is the regression test of the stage 3a round 1 review, M1,
// in the form of the round 2 ruling (Tasks rule 8): a Task leaves its frame
// by a direct return, which moves it straight into the caller's frame, so
// it stays live, the caller can Wait for it, and the caller's frame drains
// it. A Task returned without the move was drained by its frame, and a Wait
// on it is a named defect: the guard of the lowering's calling convention.
func TestTaskReturn(t *testing.T) {
	t.Run("Wait after the frame returned", func(t *testing.T) {
		var v int
		var werr error
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				v, werr = start(root, s, func(*rt.Task) (int, error) { return 7, nil }).Wait()
				return nil
			})
		})
		if err != nil || werr != nil || v != 7 {
			t.Errorf("Wait = %d, %v (exit %v); want 7", v, werr, err)
		}
	})
	t.Run("the caller's frame owns it", func(t *testing.T) {
		var ended atomic.Bool
		var endedAtReturn, endedAtEnd bool
		err := runMain(func(root *rt.Task) error {
			err := frame(root, func(s *Scope) error {
				start(root, s, func(ct *rt.Task) (int, error) {
					defer ended.Store(true)
					sleepUntilStopped(ct)
					return 0, nil
				})
				endedAtReturn = ended.Load()
				return nil
			})
			endedAtEnd = ended.Load()
			return err
		})
		if err != nil || endedAtReturn || !endedAtEnd {
			t.Errorf("exit %v, ended when start returned %v, when the caller's frame ended %v; want nil, false, true", err, endedAtReturn, endedAtEnd)
		}
	})
	t.Run("into an exit action's frame", func(t *testing.T) {
		var r recorder
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() { r.add("earlier") })
				OnExit(s, func() {
					start(root, s, func(ct *rt.Task) (int, error) {
						defer r.add("child-ended")
						_ = rt.Sleep(ct, rt.Live{}, time.Hour)
						return 0, nil
					})
					r.add("action")
				})
				return nil
			})
		})
		if want := "action child-ended earlier"; err != nil || r.String() != want {
			t.Errorf("exit %v, order %q; want nil, %q", err, r.String(), want)
		}
	})
	t.Run("without the move, Wait is a named defect", func(t *testing.T) {
		var p any
		_ = runMain(func(root *rt.Task) error {
			c := start(root, nil, func(*rt.Task) (int, error) { return 7, nil })
			p = recovered(func() { _, _ = c.Wait() })
			return nil
		})
		if s, _ := p.(string); !strings.Contains(s, "its owner stopped when it ended") {
			t.Errorf("Wait raised %#v; want the named defect", p)
		}
	})
	t.Run("into a returned frame, another task or no frame", func(t *testing.T) {
		var saved *Scope
		var pEnded, pOther, pNil any
		_ = runMain(func(root *rt.Task) error {
			frameNoRow(root, func(s *Scope) { saved = s })
			frameNoRow(root, func(s *Scope) {
				c := Go(s, "c", func(*rt.Task) (int, error) { return 0, nil })
				pEnded = recovered(func() { c.Move(saved) })
				pNil = recovered(func() { c.Move(nil) })
				other := Go(s, "other", func(ct *rt.Task) (int, error) {
					frameNoRow(ct, func(os *Scope) { pOther = recovered(func() { c.Move(os) }) })
					return 0, nil
				})
				_, _ = other.Wait()
			})
			return nil
		})
		if s, _ := pEnded.(string); !strings.Contains(s, "has returned (law R9)") {
			t.Errorf("Move into a returned frame raised %#v; want the R9 defect", pEnded)
		}
		if s, _ := pOther.(string); !strings.Contains(s, "another task") {
			t.Errorf("Move into another task's frame raised %#v; want the R9 defect", pOther)
		}
		if s, _ := pNil.(string); !strings.Contains(s, "no destination") {
			t.Errorf("Move to no frame raised %#v; want the defect", pNil)
		}
	})
}

// TestTaskRegistrationsBounded is the regression test of the stage 3a
// round 2 review, M1: a frame's list holds only the tasks it still owns.
// Inside one live task, a long-lived frame receives, waits for and drops
// many returned tasks with 1 MiB results, directly and through fresh
// receiving frames; its list never holds more than the one live task, and
// the heap does not keep the dropped results.
func TestTaskRegistrationsBounded(t *testing.T) {
	const rounds, size = 200, 1 << 20
	big := func(*rt.Task) ([]byte, error) { return make([]byte, size), nil }
	startBig := func(rt0 *rt.Task, dest *Scope) *Task[[]byte] {
		c, _ := frameValue(rt0, func(s *Scope) (*Task[[]byte], error) {
			return Go(s, "big", big).Move(dest), nil
		})
		return c
	}
	var most, left int
	var grew int64
	err := runMain(func(root *rt.Task) error {
		return frame(root, func(s *Scope) error {
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := range rounds {
				if i%2 == 0 {
					c := startBig(root, s)
					most = max(most, s.n)
					_, _ = c.Wait()
				} else {
					_ = frame(root, func(r *Scope) error {
						c := startBig(root, r)
						most = max(most, s.n+r.n)
						_, err := c.Wait()
						return err
					})
				}
			}
			left = s.n
			runtime.GC()
			runtime.ReadMemStats(&after)
			grew = int64(after.HeapAlloc) - int64(before.HeapAlloc)
			return nil
		})
	})
	t.Logf("%d rounds: most entries %d, left %d, heap grew by %d bytes", rounds, most, left, grew)
	if err != nil || most != 1 || left != 0 {
		t.Errorf("exit %v, most entries %d, left %d; want nil, 1, 0", err, most, left)
	}
	if grew > 32*size {
		t.Errorf("the heap grew by %d bytes over %d dropped results of %d bytes; want far less than all of them", grew, rounds, size)
	}
}

// TestGoInExitAction is the regression test of the stage 3a round 1 review,
// M2 (Tasks rule 7; Exit actions 5): the block of an exit action is a frame
// for the tasks it starts and the exit actions it registers, so task.Go in
// it runs, a Wait in it works, a task it leaves is drained when the block
// ends, before the next exit action runs, and an exit action it registers
// runs when the block ends.
func TestGoInExitAction(t *testing.T) {
	t.Run("Go and Wait", func(t *testing.T) {
		ran := false
		var v int
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() {
					v, _ = Go(s, "cleanup", func(*rt.Task) (int, error) { ran = true; return 1, nil }).Wait()
				})
				return nil
			})
		})
		if err != nil || !ran || v != 1 {
			t.Errorf("exit %v, ran %v, Wait %d; want nil, true, 1", err, ran, v)
		}
	})
	t.Run("a task it leaves ends with the block", func(t *testing.T) {
		var r recorder
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() { r.add("earlier action") })
				OnExit(s, func() {
					started := make(chan struct{})
					Go(s, "cleanup", func(ct *rt.Task) (int, error) {
						defer r.add("child ended")
						close(started)
						// Inside an exit action the child is shielded from
						// the ambient stop, but its owner's drain stops it.
						if err := rt.Sleep(ct, rt.Live{}, time.Hour); err != nil {
							return 0, nil
						}
						return 0, nil
					})
					<-started
				})
				return nil
			})
		})
		if err != nil || r.String() != "child ended earlier action" {
			t.Errorf("exit %v, order %q; want nil, %q", err, r.String(), "child ended earlier action")
		}
	})
	t.Run("an exit action it registers", func(t *testing.T) {
		var r recorder
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() { r.add("earlier action") })
				OnExit(s, func() {
					r.add("outer")
					OnExit(s, func() { r.add("inner") })
				})
				return nil
			})
		})
		if err != nil || r.String() != "outer inner earlier action" {
			t.Errorf("exit %v, order %q; want nil, %q", err, r.String(), "outer inner earlier action")
		}
	})
	t.Run("a defect in its task joins the frame's exit", func(t *testing.T) {
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() {
					_, _ = Go(s, "cleanup", func(*rt.Task) (int, error) { return explode(), nil }).Wait()
				})
				return nil
			})
		})
		rs := reasons(err)
		if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
			t.Errorf("reasons %+v; want the cleanup task's defect", rs)
		}
	})
	// The stage 3a round 2 review, M2: the route to an exit action's frame
	// lasts through the block and its frame's release, where a nested exit
	// action runs, so a task the nested action starts belongs to the nested
	// action's frame, not to the frame that has returned.
	t.Run("a nested exit action starts and waits for a task", func(t *testing.T) {
		v := 0
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() {
					OnExit(s, func() {
						v, _ = Go(s, "inner", func(*rt.Task) (int, error) { return 1, nil }).Wait()
					})
				})
				return nil
			})
		})
		if err != nil || v != 1 {
			t.Errorf("exit %v, Wait %d; want nil, 1", err, v)
		}
	})
	t.Run("a nested exit action leaves a task", func(t *testing.T) {
		var r recorder
		err := runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				OnExit(s, func() { r.add("earlier") })
				OnExit(s, func() {
					OnExit(s, func() {
						started := make(chan struct{})
						Go(s, "inner", func(ct *rt.Task) (int, error) {
							defer r.add("inner-child-ended")
							close(started)
							_ = rt.Sleep(ct, rt.Live{}, time.Hour)
							return 0, nil
						})
						<-started
						r.add("inner")
					})
					r.add("outer")
				})
				return nil
			})
		})
		want := "outer inner inner-child-ended earlier"
		if err != nil || r.String() != want {
			t.Errorf("exit %v, order %q; want nil, %q", err, r.String(), want)
		}
	})
}

// TestGoOnEndedFrame: a frame that has returned admits no task (law R9).
func TestGoOnEndedFrame(t *testing.T) {
	var saved *Scope
	frameNoRow(rt.Root("main"), func(s *Scope) { saved = s })
	p := recovered(func() { Go(saved, "late", func(*rt.Task) (int, error) { return 0, nil }) })
	if s, _ := p.(string); !strings.Contains(s, "law R9") {
		t.Errorf("raised %#v; want the R9 defect", p)
	}
}

// TestSecondWait: a task is waited for once; a second Wait is a defect.
func TestSecondWait(t *testing.T) {
	frameNoRow(rt.Root("main"), func(s *Scope) {
		c := Go(s, "child", func(*rt.Task) (int, error) { return 1, nil })
		_, _ = c.Wait()
		p := recovered(func() { _, _ = c.Wait() })
		if s, _ := p.(string); !strings.Contains(s, "already waited") {
			t.Errorf("second Wait raised %#v; want the defect", p)
		}
	})
}

// TestNoTaskOutlivesItsFrame is the leak check: random trees of tasks that
// return, fail, panic, block until stopped, are waited for, stopped or
// left to their frame. Every task has ended when the root returns, and no
// goroutine is left.
func TestNoTaskOutlivesItsFrame(t *testing.T) {
	base := runtime.NumGoroutine()
	var live, started atomic.Int64
	for seed := uint64(1); seed <= 100; seed++ {
		rng := rand.New(rand.NewPCG(seed, 0))
		// Each task draws from its own generator, seeded by its parent, so
		// the tree is a function of the seed and no generator is shared.
		var tree func(ct *rt.Task, depth int, seed uint64) (int, error)
		tree = func(ct *rt.Task, depth int, seed uint64) (int, error) {
			started.Add(1)
			live.Add(1)
			defer live.Add(-1)
			rng := rand.New(rand.NewPCG(seed, uint64(depth)))
			return frameValue(ct, func(s *Scope) (int, error) {
				OnExit(s, func() {})
				n := 0
				if depth < 3 {
					n = rng.IntN(4)
				}
				kids := make([]*Task[int], n)
				for i := range kids {
					kidSeed := rng.Uint64()
					kids[i] = Go(s, Site(fmt.Sprintf("d%d.%d", depth, i)), func(t *rt.Task) (int, error) { return tree(t, depth+1, kidSeed) })
				}
				for _, k := range kids {
					switch rng.IntN(4) {
					case 0:
						recovered(func() { _, _ = k.Wait() })
					case 1:
						recovered(k.Stop)
					}
				}
				switch rng.IntN(5) {
				case 0:
					return 0, bad{"tree"}
				case 1:
					explode()
				case 2:
					sleepUntilStopped(ct)
				}
				return 1, nil
			})
		}
		// The root never waits for its child: it stops it, or leaves it to
		// its frame, so a task blocked until stopped always ends.
		rootSeed := rng.Uint64()
		_ = runMain(func(root *rt.Task) error {
			return frame(root, func(s *Scope) error {
				c := Go(s, "root child", func(t *rt.Task) (int, error) { return tree(t, 0, rootSeed) })
				if rng.IntN(2) == 0 {
					return bad{"leave it to the frame"}
				}
				recovered(c.Stop)
				return nil
			})
		})
		if n := live.Load(); n != 0 {
			t.Fatalf("seed %d: %d tasks still running after the root returned", seed, n)
		}
	}
	if started.Load() < 100 {
		t.Fatalf("only %d tasks started", started.Load())
	}
	waitForGoroutines(t, base)
}

// waitForGoroutines waits until at most base goroutines run: a task's
// goroutine exits just after it publishes its exit.
func waitForGoroutines(t *testing.T, base int) {
	t.Helper()
	if !testwait.Until(t, func() bool { return runtime.NumGoroutine() <= base }) {
		buf := make([]byte, 1<<16)
		t.Fatalf("%d goroutines; want at most %d:\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
	}
}

// TestReport: the entry's report and exit status (Exit causes 6): one line
// per reason, defects with their chain and stack; status 2 when any reason
// is a defect, 130 when all are interruptions, 1 otherwise.
func TestReport(t *testing.T) {
	defect := rt.Reason{Kind: rt.KindDefect, Value: &Panic{Value: "boom", Stack: []byte("STACK")}, Chain: []rt.Frame{{Task: 2, Span: "worker"}, {Task: 1, Span: "main"}}}
	cases := []struct {
		err  error
		code int
		text string
	}{
		{nil, 0, ""},
		{bad{"x"}, 1, "error: bad x\n"},
		{rt.Interrupted, 130, "interrupted\n"},
		{&rt.Cause{Reasons: []rt.Reason{{Kind: rt.KindFailure, Err: bad{"x"}}, defect}}, 2, "error: bad x\npanic: boom\n\ttask #2 worker\n\ttask #1 main\n\nSTACK\n"},
		{&rt.Cause{Reasons: []rt.Reason{{Kind: rt.KindFailure, Err: bad{"x"}}, {Kind: rt.KindInterruption}}}, 1, "error: bad x\ninterrupted\n"},
	}
	for i, c := range cases {
		var b strings.Builder
		if code := report(&b, c.err); code != c.code || b.String() != c.text {
			t.Errorf("case %d: status %d, text %q; want %d, %q", i, code, b.String(), c.code, c.text)
		}
	}
}
