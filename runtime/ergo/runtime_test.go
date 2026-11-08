package ergo

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

func wait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-testwait.After(t):
		t.Fatal("managed shutdown did not finish")
		var zero T
		return zero
	}
}
func TestCancellationWaitsForChildrenBeforeReleasingParentResources(t *testing.T) {
	started, cleanupStarted, finishCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan Exit[Unit], 1)
	var parentReleased atomic.Bool
	go func() {
		done <- Run(func(fc *FiberContext) Exit[Unit] {
			acquired := Invoke(fc, AcquireRelease("parent", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { parentReleased.Store(true); return nil }))
			if acquired.IsFailure() {
				return acquired
			}
			child := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				acquired := Invoke(child, AcquireRelease("child", func(context.Context) (Unit, error) { return Unit{}, nil }, func(_ Unit, ctx context.Context) error {
					if ctx.Err() != nil {
						t.Error("cleanup was interruptible")
					}
					close(cleanupStarted)
					<-finishCleanup
					return nil
				}))
				if acquired.IsFailure() {
					return acquired
				}
				close(started)
				<-child.Context().Done()
				return Interrupt[Unit](child.Context().Err())
			}))
			if child.IsFailure() {
				return Propagate[Unit](child)
			}
			wait(t, started)
			return Succeed(Unit{})
		})
	}()
	wait(t, cleanupStarted)
	select {
	case <-done:
		t.Fatal("owner completed before child cleanup")
	default:
	}
	if parentReleased.Load() {
		t.Fatal("parent resource released while child was alive")
	}
	close(finishCleanup)
	out := wait(t, done)
	if out.IsFailure() || !parentReleased.Load() {
		t.Fatalf("shutdown: %+v", out)
	}
}
func TestLateAcquisitionIsReleasedBeforeConcurrentCloseCompletes(t *testing.T) {
	scope := newScope(context.Background(), nil)
	fc := &FiberContext{ctx: scope.ctx, scope: scope, driver: scope.driver}
	started, finishAcquire, releaseStarted, finishRelease := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var released atomic.Int32
	result := make(chan Exit[Unit], 1)
	go func() {
		result <- Invoke(fc, AcquireRelease("late", func(ctx context.Context) (Unit, error) {
			close(started)
			<-finishAcquire
			if ctx.Err() != nil {
				t.Error("acquisition was interrupted before registration")
			}
			return Unit{}, nil
		}, func(Unit, context.Context) error {
			released.Add(1)
			close(releaseStarted)
			<-finishRelease
			return errors.New("late cleanup defect")
		}))
	}()
	wait(t, started)
	closed := make(chan Cause, 2)
	go func() { closed <- scope.Close() }()
	wait(t, scope.ctx.Done())
	go func() { closed <- scope.Close() }()
	close(finishAcquire)
	wait(t, releaseStarted)
	select {
	case <-closed:
		t.Fatal("close completed while late resource was still releasing")
	default:
	}
	close(finishRelease)
	a, b := wait(t, closed), wait(t, closed)
	if released.Load() != 1 || len(a) != 1 || !reflect.DeepEqual(a, b) {
		t.Fatalf("idempotent close lost cleanup: %v %v", a, b)
	}
	if !wait(t, result).Interrupted || scope.Snapshot().State != "Closed" {
		t.Fatal("late admission succeeded")
	}
}
func TestFailureAndCleanupDefectsArePreservedInReverseOrder(t *testing.T) {
	order := []string{}
	out := Run(func(fc *FiberContext) Exit[string] {
		for _, name := range []string{"first", "second"} {
			acquired := Invoke(fc, AcquireRelease(name, func(context.Context) (string, error) { return name, nil }, func(value string, _ context.Context) error { order = append(order, value); return errors.New(value) }))
			if acquired.IsFailure() {
				return Propagate[string](acquired)
			}
		}
		return Fail[string]("DomainError", nil)
	})
	if !reflect.DeepEqual(order, []string{"second", "first"}) || out.Failure == nil || len(out.Cause()) != 3 {
		t.Fatalf("lost failure/finalizers: %+v %v", out, order)
	}
	recovered := Run(Catch(func(*FiberContext) Exit[string] { return out }, "DomainError", func() string { return "hidden" }))
	if !recovered.IsFailure() {
		t.Fatal("recovery erased cleanup defects")
	}
}
func TestCancellationHookUnblocksForeignWorkAndTimeoutWaitsForCleanup(t *testing.T) {
	var released atomic.Bool
	out := Run(Timeout(func(fc *FiberContext) Exit[Unit] {
		acquired := Invoke(fc, AcquireRelease("owned", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { released.Store(true); return nil }))
		if acquired.IsFailure() {
			return acquired
		}
		blocked := make(chan struct{})
		if err := fc.Scope().OnCancel(func() error { close(blocked); return nil }); err != nil {
			return Die[Unit](err)
		}
		<-blocked // A simulated foreign wait observes the registered abort hook, not context itself.
		return Interrupt[Unit](context.Canceled)
	}, 5))
	if out.Failure == nil || out.Failure.Tag != "Timeout" || !released.Load() {
		t.Fatalf("timeout returned before cleanup: %+v", out)
	}
}
func TestOwnedFailuresPanicDefectsAndObservedJoin(t *testing.T) {
	for _, observed := range []bool{false, true} {
		out := Run(func(fc *FiberContext) Exit[string] {
			fiber := Invoke(fc, Fork(func(*FiberContext) Exit[string] { return Fail[string]("ChildError", nil) }))
			if fiber.IsFailure() {
				return Propagate[string](fiber)
			}
			if observed {
				return Invoke(fc, fiber.Value.Join())
			}
			<-fiber.Value.done
			return Succeed("parent")
		})
		if out.Failure == nil || out.Failure.Tag != "ChildError" || len(out.Cause()) != 1 {
			t.Fatalf("child failure lost or duplicated: %+v", out)
		}
	}
	var released atomic.Bool
	panicExit := Run(func(fc *FiberContext) Exit[Unit] {
		Invoke(fc, AcquireRelease("panic", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { released.Store(true); return nil }))
		panic("foreign defect")
	})
	if panicExit.Defect == nil || !released.Load() {
		t.Fatal("panic bypassed managed cleanup")
	}
}
func TestConcurrentAdmissionAndClosure(t *testing.T) {
	for range 30 {
		scope := newScope(context.Background(), nil)
		fc := &FiberContext{ctx: scope.ctx, scope: scope, driver: scope.driver}
		var opened, released atomic.Int64
		var workers sync.WaitGroup
		for range 12 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				Invoke(fc, AcquireRelease("race", func(context.Context) (Unit, error) { opened.Add(1); return Unit{}, nil }, func(Unit, context.Context) error { released.Add(1); return nil }))
			}()
		}
		scope.Close()
		workers.Wait()
		if opened.Load() != released.Load() {
			t.Fatalf("unowned acquisition: %d/%d", opened.Load(), released.Load())
		}
	}
}
