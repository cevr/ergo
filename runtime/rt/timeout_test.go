package rt

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

func TestVirtualClockDrivesTimeout(t *testing.T) {
	v := NewVirtual(epoch)
	root := Root("main")
	s := NewScope(root)
	start := time.Now()
	h := Go(&s, "caller", func(task *Task) (int, error) {
		return After(task, v, time.Hour, func(task *Task) (int, error) {
			return 1, Sleep(task, v, 24*time.Hour)
		})
	})
	v.AwaitTimers(2) // the deadline and the body's sleep
	v.Advance(time.Hour - time.Nanosecond)
	if v.Pending() != 2 {
		t.Fatal("a timer fired before its deadline")
	}
	v.Advance(time.Nanosecond)
	_, err := h.Wait()
	if f, ok := Failure(err); !ok || f != (Expired{}) {
		t.Fatalf("exit %v, want a lone Expired", err)
	}
	if v.Pending() != 0 {
		t.Fatalf("%d timers stayed armed after the child drained", v.Pending())
	}
	if got := v.Now(); !got.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("virtual now %v, want epoch+1h", got)
	}
	if elapsed := time.Since(start); elapsed > time.Minute {
		t.Fatalf("a one-hour virtual timeout took %v of wall time", elapsed)
	}
	_ = endScope(&s)
}

func TestVirtualFiresInDeadlineOrder(t *testing.T) {
	v := NewVirtual(epoch)
	late := v.NewTimer(3 * time.Second)
	first := v.NewTimer(time.Second)
	tie := v.NewTimer(time.Second)
	stopped := v.NewTimer(2 * time.Second)
	if !stopped.Stop() {
		t.Fatal("Stop on an armed timer returned false")
	}
	v.Advance(5 * time.Second)
	got := []time.Time{<-first.C(), <-tie.C(), <-late.C()}
	want := []time.Time{epoch.Add(time.Second), epoch.Add(time.Second), epoch.Add(3 * time.Second)}
	if !slices.Equal(got, want) {
		t.Fatalf("fired at %v, want %v", got, want)
	}
	select {
	case <-stopped.C():
		t.Fatal("a stopped timer fired")
	default:
	}
	if late.Stop() {
		t.Fatal("Stop on a fired timer returned true")
	}
}

func TestLiveSchedulerTimeout(t *testing.T) {
	root := Root("main")
	s := NewScope(root)
	h := Go(&s, "caller", func(task *Task) (int, error) {
		return After(task, Live{}, time.Millisecond, blockUntilStopped)
	})
	if _, err := h.Wait(); err != (Expired{}) {
		t.Fatalf("exit %v, want Expired", err)
	}
	if err := Sleep(root, Live{}, time.Microsecond); err != nil {
		t.Fatalf("Sleep returned %v", err)
	}
	_ = endScope(&s)
}

// afterCase runs After as the body of a task, so a test can stop the caller.
func afterCase[T any](s *Scope, v *Virtual, body func(*Task) (T, error)) *Handle[T] {
	return Go(s, "caller", func(task *Task) (T, error) {
		return After(task, v, time.Hour, body)
	})
}

func TestAfterStopsAndDrainsChild(t *testing.T) {
	t.Run("deadline drains the child before returning", func(t *testing.T) {
		v := NewVirtual(epoch)
		s := NewScope(Root("main"))
		var mu sync.Mutex
		var log []string
		record := func(s string) { mu.Lock(); log = append(log, s); mu.Unlock() }
		h := afterCase(&s, v, func(task *Task) (int, error) {
			return Scoped(task, nil, func(s *Scope) (int, error) {
				s.Defer(func() { record("child released") })
				<-task.Done()
				return 0, errBody // abandoned work: normalized
			})
		})
		v.AwaitTimers(1)
		v.Advance(time.Hour)
		_, err := h.Wait()
		record("After returned")
		if err != (Expired{}) {
			t.Fatalf("exit %v, want a lone Expired", err)
		}
		if !slices.Equal(log, []string{"child released", "After returned"}) {
			t.Fatalf("order %v", log)
		}
		_ = endScope(&s)
	})
	t.Run("deadline keeps a defect", func(t *testing.T) {
		v := NewVirtual(epoch)
		s := NewScope(Root("main"))
		h := afterCase(&s, v, func(task *Task) (int, error) {
			<-task.Done()
			panic("cleanup defect")
		})
		v.AwaitTimers(1)
		v.Advance(time.Hour)
		_, err := h.Wait()
		c, ok := err.(*Cause)
		if !ok || len(c.Reasons) != 2 || c.Reasons[0].Value != "cleanup defect" || c.Reasons[1].Err != (Expired{}) {
			t.Fatalf("exit %v, want the defect then Expired", err)
		}
		_ = endScope(&s)
	})
	t.Run("parent stop interrupts and keeps a defect", func(t *testing.T) {
		v := NewVirtual(epoch)
		s := NewScope(Root("main"))
		started := make(chan struct{})
		h := afterCase(&s, v, func(task *Task) (int, error) {
			close(started)
			<-task.Done()
			panic("cleanup defect")
		})
		<-started
		h.Task().Interrupt()
		_, err := h.Wait()
		c, ok := err.(*Cause)
		if !ok || len(c.Reasons) != 2 || c.Reasons[0].Value != "cleanup defect" || c.Reasons[1].Kind != KindInterruption {
			t.Fatalf("exit %v, want the defect then the interruption", err)
		}
		if errors.Is(err, Expired{}) {
			t.Fatal("a parent stop reported Expired")
		}
		_ = endScope(&s)
	})
	t.Run("ancestor stop after the body finished", func(t *testing.T) {
		v := NewVirtual(epoch)
		root := Root("main")
		s := NewScope(root)
		h := afterCase(&s, v, func(task *Task) (int, error) {
			root.Interrupt()
			return 42, nil
		})
		if _, err := h.Wait(); err != Interrupted {
			t.Fatalf("exit %v, want Interrupted", err)
		}
		_ = endScope(&s)
	})
	t.Run("body result", func(t *testing.T) {
		v := NewVirtual(epoch)
		s := NewScope(Root("main"))
		if got, err := afterCase(&s, v, func(*Task) (string, error) { return "ok", nil }).Wait(); got != "ok" || err != nil {
			t.Fatalf("got %q, %v", got, err)
		}
		if _, err := afterCase(&s, v, func(*Task) (string, error) { return "", errBody }).Wait(); err != errBody {
			t.Fatalf("got %v, want the body's failure", err)
		}
		if v.Pending() != 0 {
			t.Fatal("a finished After left its deadline armed")
		}
		_ = endScope(&s)
	})
}
