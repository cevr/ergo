package ergo

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

func waitSchedulerSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-testwait.After(t):
		t.Fatal("managed operation did not complete")
	}
}

func adjustWithin(t *testing.T, scheduler *TestScheduler, milliseconds int64) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- scheduler.Adjust(milliseconds) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-testwait.After(t):
		t.Fatalf("adjust(%d) did not return", milliseconds)
	}
}

func TestTestSchedulerControlsSleepAndRegistration(t *testing.T) {
	scheduler := NewTestScheduler()
	started := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			close(started)
			return Invoke(fc, Sleep(100))
		})
	}()
	waitSchedulerSignal(t, started)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("sleep registration: %v", err)
	}
	if err := scheduler.Advance(99); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("sleep completed before its deadline")
	default:
	}
	if err := scheduler.Advance(1); err != nil {
		t.Fatal(err)
	}
	if out := <-done; out.IsFailure() {
		t.Fatalf("sleep failed at deadline: %+v", out)
	}
}

func TestTestSchedulerRejectsInvalidAdjustment(t *testing.T) {
	scheduler := NewTestScheduler()
	for _, duration := range []int64{-1, maxMilliseconds + 1} {
		if err := scheduler.Adjust(duration); err == nil {
			t.Fatalf("adjust accepted invalid duration %d", duration)
		}
	}
}

func TestTestSchedulerAdvancesSequentialSleepAtIntermediateDeadlines(t *testing.T) {
	scheduler := NewTestScheduler()
	points := make(chan int64, 2)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			if out := Invoke(fc, Sleep(20)); out.IsFailure() {
				return out
			}
			points <- scheduler.Now()
			if out := Invoke(fc, Sleep(30)); out.IsFailure() {
				return out
			}
			points <- scheduler.Now()
			return Succeed(Unit{})
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := scheduler.Adjust(50); err != nil {
		t.Fatal(err)
	}
	for expected, want := range []int64{20, 50} {
		select {
		case point := <-points:
			if point != want {
				t.Fatalf("sleep %d resumed at %d", expected+1, point)
			}
		case <-testwait.After(t):
			t.Fatalf("sleep %d did not resume", expected+1)
		}
	}
	if out := <-done; out.IsFailure() {
		t.Fatalf("sequential sleeps failed: %+v", out)
	}
}

// A woken managed continuation that holds its turn without waiting keeps
// Adjust from selecting the next deadline. The hold is a raw channel receive,
// so the active window stays open until the test releases it rather than for
// however long the continuation happens to run.
func TestTestSchedulerWaitsForHeldManagedTurnBeforeAdvancing(t *testing.T) {
	scheduler := NewTestScheduler()
	woke := make(chan struct{})
	release := make(chan struct{})
	second := make(chan int64, 1)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			if out := Invoke(fc, Sleep(20)); out.IsFailure() {
				return out
			}
			close(woke)
			<-release
			out := Invoke(fc, Sleep(30))
			second <- scheduler.Now()
			return out
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	adjusted := make(chan error, 1)
	go func() { adjusted <- scheduler.Adjust(50) }()
	<-woke
	select {
	case err := <-adjusted:
		t.Fatalf("adjust committed while a managed fiber held its turn: err=%v now=%d", err, scheduler.Now())
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-adjusted:
		if err != nil {
			t.Fatal(err)
		}
	case <-testwait.After(t):
		t.Fatal("adjust did not return after the held turn was released")
	}
	select {
	case point := <-second:
		if point != 50 {
			t.Fatalf("second sleep resumed at %d, want 50", point)
		}
	case <-testwait.After(t):
		t.Fatal("second sleep did not resume")
	}
	if out := <-done; out.IsFailure() {
		t.Fatalf("held continuation failed: %+v", out)
	}
}

func TestTestSchedulerWaitsForLatchContinuationBeforeAdvancing(t *testing.T) {
	scheduler := NewTestScheduler()
	latch := NewLatch()
	points := make(chan int64, 2)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			first := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				if out := Invoke(child, Sleep(20)); out.IsFailure() {
					return out
				}
				return Invoke(child, SignalLatch(latch))
			}))
			if first.IsFailure() {
				return Propagate[Unit](first)
			}
			second := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				if out := Invoke(child, AwaitLatch(latch)); out.IsFailure() {
					return out
				}
				points <- scheduler.Now()
				if out := Invoke(child, Sleep(30)); out.IsFailure() {
					return out
				}
				points <- scheduler.Now()
				return Succeed(Unit{})
			}))
			if second.IsFailure() {
				return Propagate[Unit](second)
			}
			if out := Invoke(fc, first.Value.Join()); out.IsFailure() {
				return Propagate[Unit](out)
			}
			return Invoke(fc, second.Value.Join())
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := scheduler.Adjust(50); err != nil {
		t.Fatal(err)
	}
	for expected, want := range []int64{20, 50} {
		select {
		case point := <-points:
			if point != want {
				t.Fatalf("latch continuation %d resumed at %d, want %d", expected+1, point, want)
			}
		case <-testwait.After(t):
			t.Fatalf("latch continuation %d did not resume", expected+1)
		}
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("latch sequence failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("latch sequence did not finish")
	}
}

func TestTestSchedulerWaitsForJoinContinuationBeforeAdvancing(t *testing.T) {
	scheduler := NewTestScheduler()
	resumed := make(chan int64, 1)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			first := Invoke(fc, Fork(Sleep(20)))
			if first.IsFailure() {
				return Propagate[Unit](first)
			}
			second := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				if out := Invoke(child, first.Value.Join()); out.IsFailure() {
					return out
				}
				resumed <- scheduler.Now()
				return Invoke(child, Sleep(30))
			}))
			if second.IsFailure() {
				return Propagate[Unit](second)
			}
			return Invoke(fc, second.Value.Join())
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := scheduler.Adjust(50); err != nil {
		t.Fatal(err)
	}
	select {
	case point := <-resumed:
		if point != 20 {
			t.Fatalf("join continuation resumed at %d, want 20", point)
		}
	case <-testwait.After(t):
		t.Fatal("join continuation did not resume")
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("join sequence failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("join sequence did not finish")
	}
}

func TestTestSchedulerStrongAdjustPreservesCausalIntermediateTimes(t *testing.T) {
	for iteration := 0; iteration < 300; iteration++ {
		scheduler := NewTestScheduler()
		joinedAt := make(chan int64, 1)
		done := make(chan Exit[Unit], 1)
		go func() {
			done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
				first := Invoke(fc, Fork(Sleep(20)))
				if first.IsFailure() {
					return Propagate[Unit](first)
				}
				joiner := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
					if out := Invoke(child, first.Value.Join()); out.IsFailure() {
						return out
					}
					joinedAt <- scheduler.Now()
					return Invoke(child, Sleep(30))
				}))
				if joiner.IsFailure() {
					return Propagate[Unit](joiner)
				}
				return Invoke(fc, joiner.Value.Join())
			})
		}()
		if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
			t.Fatalf("iteration %d first registration: %v", iteration, err)
		}
		if err := scheduler.Adjust(50); err != nil {
			t.Fatalf("iteration %d adjust: %v", iteration, err)
		}
		select {
		case point := <-joinedAt:
			if point != 20 {
				t.Fatalf("iteration %d joined at %d, want 20", iteration, point)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d join continuation did not resume", iteration)
		}
		select {
		case out := <-done:
			if out.IsFailure() {
				t.Fatalf("iteration %d join sequence failed: %+v", iteration, out)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d join sequence did not finish", iteration)
		}
	}
}

func TestTestSchedulerDeadlineCleanupPreservesIntermediateTime(t *testing.T) {
	for iteration := 0; iteration < 300; iteration++ {
		scheduler := NewTestScheduler()
		resumedAt := make(chan int64, 1)
		done := make(chan Exit[Unit], 1)
		go func() {
			done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
				out := Invoke(fc, Timeout(Sleep(100), 20))
				if out.Failure == nil || out.Failure.Tag != "Timeout" {
					return out
				}
				resumedAt <- scheduler.Now()
				return Invoke(fc, Sleep(30))
			})
		}()
		if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
			t.Fatalf("iteration %d first registration: %v", iteration, err)
		}
		if err := scheduler.Adjust(50); err != nil {
			t.Fatalf("iteration %d adjust: %v", iteration, err)
		}
		select {
		case point := <-resumedAt:
			if point != 20 {
				t.Fatalf("iteration %d timeout resumed at %d, want 20", iteration, point)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d timeout continuation did not resume", iteration)
		}
		select {
		case out := <-done:
			if out.IsFailure() {
				t.Fatalf("iteration %d timeout sequence failed: %+v", iteration, out)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d timeout sequence did not finish", iteration)
		}
	}
}

func TestTestSchedulerDoesNotObserveIndependentTimerBeforeJoinPublication(t *testing.T) {
	for iteration := 0; iteration < 300; iteration++ {
		scheduler := NewTestScheduler()
		joinedAt := make(chan int64, 1)
		done := make(chan Exit[Unit], 1)
		go func() {
			done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
				first := Invoke(fc, Fork(Sleep(20)))
				if first.IsFailure() {
					return Propagate[Unit](first)
				}
				second := Invoke(fc, Fork(Sleep(25)))
				if second.IsFailure() {
					return Propagate[Unit](second)
				}
				joiner := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
					if out := Invoke(child, first.Value.Join()); out.IsFailure() {
						return out
					}
					joinedAt <- scheduler.Now()
					return Invoke(child, Sleep(30))
				}))
				if joiner.IsFailure() {
					return Propagate[Unit](joiner)
				}
				if out := Invoke(fc, second.Value.Join()); out.IsFailure() {
					return out
				}
				return Invoke(fc, joiner.Value.Join())
			})
		}()
		if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
			t.Fatalf("iteration %d first registration: %v", iteration, err)
		}
		if err := scheduler.Adjust(100); err != nil {
			t.Fatalf("iteration %d adjust: %v", iteration, err)
		}
		select {
		case point := <-joinedAt:
			if point != 20 {
				t.Fatalf("iteration %d observed join at %d, want 20", iteration, point)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d joiner did not resume", iteration)
		}
		select {
		case out := <-done:
			if out.IsFailure() {
				t.Fatalf("iteration %d sequence failed: %+v", iteration, out)
			}
		case <-testwait.After(t):
			t.Fatalf("iteration %d sequence did not finish", iteration)
		}
	}
}

func TestTestSchedulerWaitsForScopeCleanupContinuationBeforeAdvancing(t *testing.T) {
	scheduler := NewTestScheduler()
	resumed := make(chan int64, 1)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			out := Invoke(fc, Scoped(func(child *FiberContext) Exit[Unit] {
				return Invoke(child, AcquireRelease("cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, cleanup context.Context) error {
					result := RunContextWithScheduler(cleanup, scheduler, Sleep(20))
					if result.IsFailure() {
						return result.Cause()
					}
					return nil
				}))
			}))
			if out.IsFailure() {
				return out
			}
			resumed <- scheduler.Now()
			return Invoke(fc, Sleep(30))
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("cleanup registration: %v", err)
	}
	if err := scheduler.Adjust(50); err != nil {
		t.Fatal(err)
	}
	select {
	case point := <-resumed:
		if point != 20 {
			t.Fatalf("scope continuation resumed at %d, want 20", point)
		}
	case <-testwait.After(t):
		t.Fatal("scope continuation did not resume")
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("scope sequence failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("scope sequence did not finish")
	}
}

func TestTestSchedulerPartialScopeCleanupCanAdvanceAndThenDrain(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, Scoped(func(child *FiberContext) Exit[Unit] {
				return Invoke(child, AcquireRelease("partial-cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, cleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(cleanup, scheduler, Sleep(20))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				}))
			}))
		})
	}()
	waitSchedulerSignal(t, releaseStarted)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("cleanup registration: %v", err)
	}
	adjustWithin(t, scheduler, 0)
	if scheduler.Now() != 0 {
		t.Fatalf("zero adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("cleanup completed during zero adjustment: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("partial adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("cleanup completed before its deadline: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("cleanup drain failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("cleanup did not complete after its deadline")
	}
}

func TestTestSchedulerNestedResourceCleanupCanAdvanceAndThenDrain(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, AcquireRelease("outer", func(context.Context) (Unit, error) {
				return Unit{}, nil
			}, func(_ Unit, cleanup context.Context) error {
				out := RunContextWithScheduler(cleanup, scheduler, AcquireRelease("inner", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, innerCleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(innerCleanup, scheduler, Sleep(20))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				}))
				if out.IsFailure() {
					return out.Cause()
				}
				return nil
			}))
		})
	}()
	waitSchedulerSignal(t, releaseStarted)
	adjustWithin(t, scheduler, 0)
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("nested cleanup adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("nested cleanup completed before its deadline: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("nested cleanup failed after its deadline: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("nested cleanup did not complete after its deadline")
	}
}

func TestTestSchedulerScopedNestedCleanupCanAdvanceAndThenDrain(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, AcquireRelease("scoped-outer", func(context.Context) (Unit, error) {
				return Unit{}, nil
			}, func(_ Unit, cleanup context.Context) error {
				close(releaseStarted)
				out := RunContextWithScheduler(cleanup, scheduler, Scoped(Sleep(20)))
				if out.IsFailure() {
					return out.Cause()
				}
				return nil
			}))
		})
	}()
	waitSchedulerSignal(t, releaseStarted)
	adjustWithin(t, scheduler, 0)
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("scoped nested cleanup adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("scoped nested cleanup completed before its deadline: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("scoped nested cleanup failed after its deadline: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("scoped nested cleanup did not complete after its deadline")
	}
}

func TestTestSchedulerPartialLatchCleanupCanBeSignaled(t *testing.T) {
	scheduler := NewTestScheduler()
	latch := NewLatch()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, Scoped(func(child *FiberContext) Exit[Unit] {
				return Invoke(child, AcquireRelease("latch-cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, cleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(cleanup, scheduler, AwaitLatch(latch))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				}))
			}))
		})
	}()
	waitSchedulerSignal(t, releaseStarted)
	adjusted := make(chan error, 1)
	go func() { adjusted <- scheduler.Adjust(10) }()
	select {
	case err := <-adjusted:
		if err != nil {
			t.Fatal(err)
		}
	case <-testwait.After(t):
		t.Fatal("partial adjustment blocked on a managed latch cleanup")
	}
	if scheduler.Now() != 10 {
		t.Fatalf("partial latch adjustment moved time to %d", scheduler.Now())
	}
	latch.Signal()
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("latch cleanup failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("latch signal did not complete cleanup")
	}
}

func TestTestSchedulerWaitsForTimeoutContinuationBeforeAdvancing(t *testing.T) {
	scheduler := NewTestScheduler()
	resumed := make(chan int64, 1)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			out := Invoke(fc, Timeout(Sleep(20), 100))
			if out.IsFailure() && (out.Failure == nil || out.Failure.Tag != "Timeout") {
				return out
			}
			resumed <- scheduler.Now()
			return Invoke(fc, Sleep(30))
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("timeout registration: %v", err)
	}
	if err := scheduler.Adjust(50); err != nil {
		t.Fatal(err)
	}
	select {
	case point := <-resumed:
		if point != 20 {
			t.Fatalf("timeout continuation resumed at %d, want 20", point)
		}
	case <-testwait.After(t):
		t.Fatal("timeout continuation did not resume")
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("timeout sequence failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("timeout sequence did not finish")
	}
}

func TestTimeoutUsesSchedulerAndWaitsForCancelledCleanup(t *testing.T) {
	scheduler := NewTestScheduler()
	cleanup := make(chan struct{})
	started := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, Timeout(func(child *FiberContext) Exit[Unit] {
				close(started)
				out := Invoke(child, AcquireRelease("cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(Unit, context.Context) error {
					close(cleanup)
					return nil
				}))
				if out.IsFailure() {
					return out
				}
				return Invoke(child, Sleep(100))
			}, 50))
		})
	}()
	waitSchedulerSignal(t, started)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("timeout registration: %v", err)
	}
	if err := scheduler.Advance(49); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		t.Fatal("timeout completed before deadline")
	default:
	}
	if err := scheduler.Advance(1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleanup:
	case <-testwait.After(t):
		t.Fatal("timeout did not release child resource")
	}
	out := <-done
	if out.Failure == nil || out.Failure.Tag != "Timeout" || len(out.Cause()) != 1 {
		t.Fatalf("wrong timeout result: %+v", out)
	}
}

func TestTestSchedulerPartialTimeoutCleanupCanAdvance(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			return Invoke(fc, Timeout(func(child *FiberContext) Exit[Unit] {
				acquired := Invoke(child, AcquireRelease("slow-timeout-cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, cleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(cleanup, scheduler, Sleep(50))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				}))
				if acquired.IsFailure() {
					return acquired
				}
				return Invoke(child, Sleep(100))
			}, 10))
		})
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("timeout registration: %v", err)
	}
	adjustWithin(t, scheduler, 10)
	waitSchedulerSignal(t, releaseStarted)
	if scheduler.Now() != 10 {
		t.Fatalf("partial timeout adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("timeout completed before slow cleanup: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 50)
	select {
	case out := <-done:
		if out.Failure == nil || out.Failure.Tag != "Timeout" || len(out.Cause()) != 1 {
			t.Fatalf("wrong timeout result after cleanup: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("timeout did not complete after slow cleanup")
	}
}

func TestTestSchedulerForkedCleanupUsesIndependentContinuations(t *testing.T) {
	tests := []struct {
		name string
		body Effect[Unit]
	}{
		{
			name: "join slow child first",
			body: func(fc *FiberContext) Exit[Unit] {
				first := Invoke(fc, Fork(Sleep(10)))
				if first.IsFailure() {
					return Propagate[Unit](first)
				}
				second := Invoke(fc, Fork(Sleep(30)))
				if second.IsFailure() {
					return Propagate[Unit](second)
				}
				if out := Invoke(fc, second.Value.Join()); out.IsFailure() {
					return out
				}
				return Invoke(fc, first.Value.Join())
			},
		},
		{
			name: "join fast child first",
			body: func(fc *FiberContext) Exit[Unit] {
				first := Invoke(fc, Fork(Sleep(10)))
				if first.IsFailure() {
					return Propagate[Unit](first)
				}
				second := Invoke(fc, Fork(Sleep(30)))
				if second.IsFailure() {
					return Propagate[Unit](second)
				}
				if out := Invoke(fc, first.Value.Join()); out.IsFailure() {
					return out
				}
				return Invoke(fc, second.Value.Join())
			},
		},
		{
			name: "caller sleeps while child completes",
			body: func(fc *FiberContext) Exit[Unit] {
				child := Invoke(fc, Fork(Sleep(10)))
				if child.IsFailure() {
					return Propagate[Unit](child)
				}
				if out := Invoke(fc, Sleep(30)); out.IsFailure() {
					return out
				}
				return Invoke(fc, child.Value.Join())
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheduler := NewTestScheduler()
			releaseStarted := make(chan struct{})
			done := make(chan Exit[Unit], 1)
			go func() {
				done <- RunContextWithScheduler(context.Background(), scheduler, AcquireRelease(
					"forked-cleanup",
					func(context.Context) (Unit, error) { return Unit{}, nil },
					func(_ Unit, cleanup context.Context) error {
						close(releaseStarted)
						out := RunContextWithScheduler(cleanup, scheduler, test.body)
						if out.IsFailure() {
							return out.Cause()
						}
						return nil
					},
				))
			}()

			waitSchedulerSignal(t, releaseStarted)
			if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
				t.Fatalf("forked cleanup registration: %v", err)
			}
			adjustWithin(t, scheduler, 10)
			if scheduler.Now() != 10 {
				t.Fatalf("forked cleanup first adjustment moved time to %d", scheduler.Now())
			}
			select {
			case out := <-done:
				t.Fatalf("forked cleanup completed before slow work: %+v", out)
			default:
			}
			adjustWithin(t, scheduler, 20)
			select {
			case out := <-done:
				if out.IsFailure() {
					t.Fatalf("forked cleanup failed after both deadlines: %+v", out)
				}
			case <-testwait.After(t):
				t.Fatal("forked cleanup did not complete after both deadlines")
			}
		})
	}
}

func TestTestSchedulerTimeoutInsideCleanupCanAdvanceToDeadline(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, AcquireRelease(
			"timeout-cleanup",
			func(context.Context) (Unit, error) { return Unit{}, nil },
			func(_ Unit, cleanup context.Context) error {
				close(releaseStarted)
				out := RunContextWithScheduler(cleanup, scheduler, Timeout(Sleep(30), 10))
				if out.Failure == nil || out.Failure.Tag != "Timeout" || len(out.Cause()) != 1 {
					return errors.New("nested cleanup timeout did not win")
				}
				return nil
			},
		))
	}()

	waitSchedulerSignal(t, releaseStarted)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("nested timeout registration: %v", err)
	}
	adjustWithin(t, scheduler, 5)
	if scheduler.Now() != 5 {
		t.Fatalf("nested timeout partial adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("nested timeout completed before its deadline: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 5)
	if scheduler.Now() != 10 {
		t.Fatalf("nested timeout deadline adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("outer cleanup failed after nested timeout: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("outer cleanup did not complete after nested timeout")
	}
}

func TestTestSchedulerPartialWorkWinnerCleanupCanAdvance(t *testing.T) {
	scheduler := NewTestScheduler()
	deadlineReady := make(chan struct{})
	releaseStarted := make(chan struct{})
	done := make(chan Exit[string], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[string] {
			return Invoke(fc, TimeoutWithEffect(
				func(child *FiberContext) Exit[string] {
					out := Invoke(child, Sleep(10))
					if out.IsFailure() {
						return Propagate[string](out)
					}
					return Succeed("work")
				},
				func(deadline *FiberContext) Exit[Unit] {
					acquired := Invoke(deadline, AcquireRelease("slow-deadline-cleanup", func(context.Context) (Unit, error) {
						return Unit{}, nil
					}, func(_ Unit, cleanup context.Context) error {
						close(releaseStarted)
						out := RunContextWithScheduler(cleanup, scheduler, Sleep(50))
						if out.IsFailure() {
							return out.Cause()
						}
						return nil
					}))
					if acquired.IsFailure() {
						return acquired
					}
					close(deadlineReady)
					return Invoke(deadline, Sleep(100))
				},
			))
		})
	}()
	waitSchedulerSignal(t, deadlineReady)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("work/deadline registration: %v", err)
	}
	adjustWithin(t, scheduler, 10)
	waitSchedulerSignal(t, releaseStarted)
	if scheduler.Now() != 10 {
		t.Fatalf("partial work-winner adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("work winner completed before slow cleanup: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 50)
	select {
	case out := <-done:
		if out.IsFailure() || out.Value != "work" {
			t.Fatalf("work winner failed after cleanup: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("work winner did not complete after slow cleanup")
	}
}

func TestTestSchedulerWorkWinnerWithAlreadyClosingDeadlineCleanupCanAdvance(t *testing.T) {
	scheduler := NewTestScheduler()
	deadlineReady := make(chan struct{})
	releaseStarted := make(chan struct{})
	done := make(chan Exit[string], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[string] {
			return Invoke(fc, TimeoutWithEffect(
				func(child *FiberContext) Exit[string] {
					out := Invoke(child, Sleep(10))
					if out.IsFailure() {
						return Propagate[string](out)
					}
					return Succeed("work")
				},
				func(deadline *FiberContext) Exit[Unit] {
					acquired := Invoke(deadline, AcquireRelease("closing-deadline", func(context.Context) (Unit, error) {
						return Unit{}, nil
					}, func(_ Unit, cleanup context.Context) error {
						close(releaseStarted)
						out := RunContextWithScheduler(cleanup, scheduler, Sleep(20))
						if out.IsFailure() {
							return out.Cause()
						}
						return nil
					}))
					if acquired.IsFailure() {
						return acquired
					}
					close(deadlineReady)
					return Succeed(Unit{})
				},
			))
		})
	}()
	waitSchedulerSignal(t, deadlineReady)
	waitSchedulerSignal(t, releaseStarted)
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("work winner adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("work winner completed before deadline cleanup: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.IsFailure() || out.Value != "work" {
			t.Fatalf("work winner failed after already-closing cleanup: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("work winner did not complete after already-closing cleanup")
	}
}

func TestTestSchedulerDeadlineWinnerWithAlreadyClosingWorkCleanupCanAdvance(t *testing.T) {
	scheduler := NewTestScheduler()
	releaseStarted := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, Timeout(
			AcquireRelease(
				"closing-work",
				func(context.Context) (Unit, error) { return Unit{}, nil },
				func(_ Unit, cleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(cleanup, scheduler, Sleep(20))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				},
			),
			10,
		))
	}()

	waitSchedulerSignal(t, releaseStarted)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("deadline/cleanup registration: %v", err)
	}
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("deadline adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("deadline winner completed before closing work cleanup: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.Failure == nil || out.Failure.Tag != "Timeout" || len(out.Cause()) != 1 {
			t.Fatalf("wrong deadline-winner result after cleanup: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("deadline winner did not complete after closing work cleanup")
	}
}

func TestTestSchedulerParentInterruptConsumesCleanupInCompletionOrder(t *testing.T) {
	for _, test := range []struct {
		name          string
		childCleanup  int64
		timerCleanup  int64
		firstDeadline int64
	}{
		{name: "child first", childCleanup: 20, timerCleanup: 30, firstDeadline: 20},
		{name: "timer first", childCleanup: 30, timerCleanup: 20, firstDeadline: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduler := NewTestScheduler()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := make(chan struct{}, 2)
			started := make(chan struct{}, 2)
			done := make(chan Exit[Unit], 1)
			branch := func(delay int64) Effect[Unit] {
				return func(fc *FiberContext) Exit[Unit] {
					acquired := Invoke(fc, AcquireRelease("parent-cleanup", func(context.Context) (Unit, error) {
						return Unit{}, nil
					}, func(_ Unit, cleanup context.Context) error {
						started <- struct{}{}
						out := RunContextWithScheduler(cleanup, scheduler, Sleep(delay))
						if out.IsFailure() {
							return out.Cause()
						}
						return nil
					}))
					if acquired.IsFailure() {
						return acquired
					}
					ready <- struct{}{}
					return Invoke(fc, Sleep(100))
				}
			}
			go func() {
				done <- RunContextWithScheduler(ctx, scheduler, TimeoutWithEffect(
					branch(test.childCleanup),
					branch(test.timerCleanup),
				))
			}()
			for i := 0; i < 2; i++ {
				select {
				case <-ready:
				case <-testwait.After(t):
					t.Fatal("parent branches did not become ready")
				}
			}
			cancel()
			for i := 0; i < 2; i++ {
				select {
				case <-started:
				case <-testwait.After(t):
					t.Fatal("parent cleanup did not start")
				}
			}
			adjustWithin(t, scheduler, 0)
			adjustWithin(t, scheduler, 10)
			if scheduler.Now() != 10 {
				t.Fatalf("partial parent adjustment moved time to %d", scheduler.Now())
			}
			select {
			case out := <-done:
				t.Fatalf("parent interruption completed before cleanup deadlines: %+v", out)
			default:
			}
			adjustWithin(t, scheduler, test.firstDeadline-10)
			if scheduler.Now() != test.firstDeadline {
				t.Fatalf("first cleanup adjustment moved time to %d", scheduler.Now())
			}
			select {
			case out := <-done:
				t.Fatalf("parent interruption completed before the remaining cleanup: %+v", out)
			default:
			}
			adjustWithin(t, scheduler, 10)
			select {
			case out := <-done:
				if !out.Interrupted {
					t.Fatalf("parent interruption lost its interrupt cause: %+v", out)
				}
			case <-testwait.After(t):
				t.Fatal("parent interruption did not complete after both cleanups")
			}
		})
	}
}

func TestTestSchedulerManagedLatchSignalerReleasesCleanup(t *testing.T) {
	scheduler := NewTestScheduler()
	latch := NewLatch()
	releaseStarted := make(chan struct{}, 1)
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			signaler := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				if out := Invoke(child, Sleep(20)); out.IsFailure() {
					return out
				}
				return Invoke(child, SignalLatch(latch))
			}))
			if signaler.IsFailure() {
				return Propagate[Unit](signaler)
			}
			cleanup := Invoke(fc, Scoped(func(child *FiberContext) Exit[Unit] {
				return Invoke(child, AcquireRelease("managed-latch-cleanup", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(_ Unit, cleanup context.Context) error {
					close(releaseStarted)
					out := RunContextWithScheduler(cleanup, scheduler, AwaitLatch(latch))
					if out.IsFailure() {
						return out.Cause()
					}
					return nil
				}))
			}))
			if cleanup.IsFailure() {
				return cleanup
			}
			return Invoke(fc, signaler.Value.Join())
		})
	}()
	waitSchedulerSignal(t, releaseStarted)
	adjustWithin(t, scheduler, 10)
	if scheduler.Now() != 10 {
		t.Fatalf("managed signaler adjustment moved time to %d", scheduler.Now())
	}
	select {
	case out := <-done:
		t.Fatalf("latch cleanup completed before its signaler deadline: %+v", out)
	default:
	}
	adjustWithin(t, scheduler, 10)
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("managed latch signaler failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("managed latch signaler did not release cleanup")
	}
}

func TestTimeoutWithExplicitDeadlinePreservesChildValue(t *testing.T) {
	scheduler := NewTestScheduler()
	out := RunContextWithScheduler(context.Background(), scheduler, TimeoutWithEffect(
		func(*FiberContext) Exit[string] { return Succeed("completed") },
		Sleep(100),
	))
	if out.IsFailure() || out.Value != "completed" {
		t.Fatalf("child result was lost: %+v", out)
	}
}

func TestTimeoutWithWorkWinnerRetainsAcquireReleaseCleanupDefect(t *testing.T) {
	deadlineResourceReady := make(chan struct{})
	timerDefect := errors.New("timer cleanup defect")
	out := Run(TimeoutWithEffect(
		func(*FiberContext) Exit[string] {
			<-deadlineResourceReady
			return Succeed("completed")
		},
		func(fc *FiberContext) Exit[Unit] {
			acquired := Invoke(fc, AcquireRelease("deadline", func(context.Context) (Unit, error) {
				return Unit{}, nil
			}, func(Unit, context.Context) error {
				return timerDefect
			}))
			if acquired.IsFailure() {
				return acquired
			}
			close(deadlineResourceReady)
			return Invoke(fc, Sleep(100))
		},
	))
	if !out.IsFailure() || out.Defect == nil || !errors.Is(out.Defect, timerDefect) {
		t.Fatalf("timer cleanup defect was discarded after work won: %+v", out)
	}
	if out.Failure != nil || out.Interrupted || len(out.Cause()) != 1 {
		t.Fatalf("work winner was rewritten or duplicated: %+v", out)
	}
}

func TestTimeoutParentInterruptRetainsOneAcquireReleaseCleanupDefect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deadlineResourceReady := make(chan struct{})
	timerDefect := errors.New("parent deadline cleanup defect")
	done := make(chan Exit[string], 1)
	go func() {
		done <- RunContext(ctx, TimeoutWithEffect(
			func(fc *FiberContext) Exit[string] {
				<-deadlineResourceReady
				out := Invoke(fc, Sleep(100))
				if out.IsFailure() {
					return Propagate[string](out)
				}
				return Succeed("work")
			},
			func(fc *FiberContext) Exit[Unit] {
				acquired := Invoke(fc, AcquireRelease("deadline", func(context.Context) (Unit, error) {
					return Unit{}, nil
				}, func(Unit, context.Context) error {
					return timerDefect
				}))
				if acquired.IsFailure() {
					return acquired
				}
				close(deadlineResourceReady)
				return Invoke(fc, Sleep(100))
			},
		))
	}()
	waitSchedulerSignal(t, deadlineResourceReady)
	cancel()
	select {
	case out := <-done:
		cause := out.Cause()
		if !out.Interrupted || len(cause) != 2 || cause[0].Kind != "interrupt" || cause[1].Kind != "defect" || !errors.Is(cause[1].Err, timerDefect) {
			t.Fatalf("parent interruption cause was lost or duplicated: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("parent interruption did not complete cleanup")
	}
}

func TestTimeoutDeadlineCauseIsNotRewrittenAsTimeout(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause Cause
	}{
		{name: "failure", cause: Cause{{Kind: "failure", Failure: &Failure{Tag: "DeadlineFailed"}}}},
		{name: "interrupt", cause: Cause{{Kind: "interrupt", Err: context.Canceled}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := Run(TimeoutWithEffect(Sleep(100), func(*FiberContext) Exit[Unit] {
				return FromCause[Unit](test.cause)
			}))
			if len(out.Cause()) != 1 || out.Cause()[0].Kind != test.cause[0].Kind {
				t.Fatalf("deadline cause was rewritten or duplicated: %+v", out)
			}
			if out.Failure != nil && out.Failure.Tag == "Timeout" {
				t.Fatalf("deadline cause became synthetic timeout: %+v", out)
			}
		})
	}
}

func TestCancelledSleepUnregistersItsTimer(t *testing.T) {
	scheduler := NewTestScheduler()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(ctx, scheduler, func(fc *FiberContext) Exit[Unit] {
			close(started)
			return Invoke(fc, Sleep(100))
		})
	}()
	waitSchedulerSignal(t, started)
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatalf("sleep registration: %v", err)
	}
	cancel()
	if out := <-done; !out.Interrupted {
		t.Fatalf("cancelled sleep was not interrupted: %+v", out)
	}
	if err := scheduler.Advance(100); err != nil {
		t.Fatal(err)
	}
	barrierCtx, barrierCancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer barrierCancel()
	if err := scheduler.AwaitNextRegistration(barrierCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale timer remained registered: %v", err)
	}
}

// The managed barrier behind Scheduler.awaitRegistration returns once no
// managed fiber is runnable, whether or not a timer was registered since the
// last barrier. Before it returns, forked work that completes immediately, a
// timeout whose already-signalled work cancels its timer, and a nested fork
// have all run, and a sleeping fork has registered its timer, which is then
// the only pending one.
func TestManagedRegistrationBarrierReturnsAtIdle(t *testing.T) {
	scheduler := NewTestScheduler()
	latch := NewLatch()
	type observation struct {
		settled []bool
		timers  int
	}
	observed := make(chan observation, 1)
	done := make(chan Exit[Unit], 1)
	settle := func(ran chan struct{}) Effect[Unit] {
		return func(*FiberContext) Exit[Unit] {
			close(ran)
			return Succeed(Unit{})
		}
	}
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			if out := Invoke(fc, SignalLatch(latch)); out.IsFailure() {
				return out
			}
			immediate, timed, nested := make(chan struct{}), make(chan struct{}), make(chan struct{})
			forks := []Effect[*Fiber[Unit]]{
				Fork(settle(immediate)),
				Fork(Timeout(func(child *FiberContext) Exit[Unit] {
					if out := Invoke(child, AwaitLatch(latch)); out.IsFailure() {
						return out
					}
					return Invoke(child, settle(timed))
				}, 10)),
				Fork(func(child *FiberContext) Exit[Unit] {
					inner := Invoke(child, Fork(settle(nested)))
					if inner.IsFailure() {
						return Propagate[Unit](inner)
					}
					return Invoke(child, inner.Value.Join())
				}),
				Fork(Sleep(20)),
			}
			fibers := []*Fiber[Unit]{}
			for _, fork := range forks {
				out := Invoke(fc, fork)
				if out.IsFailure() {
					return Propagate[Unit](out)
				}
				fibers = append(fibers, out.Value)
			}
			if out := AwaitTestSchedulerRegistration(fc, scheduler); out.IsFailure() {
				return out
			}
			settled := []bool{}
			for _, ran := range []chan struct{}{immediate, timed, nested} {
				select {
				case <-ran:
					settled = append(settled, true)
				default:
					settled = append(settled, false)
				}
			}
			scheduler.mu.Lock()
			timers := len(scheduler.timers)
			scheduler.mu.Unlock()
			observed <- observation{settled: settled, timers: timers}
			if out := AdjustTestScheduler(fc, scheduler, 20); out.IsFailure() {
				return out
			}
			for _, fiber := range fibers {
				if out := Invoke(fc, fiber.Join()); out.IsFailure() {
					return out
				}
			}
			return Succeed(Unit{})
		})
	}()
	select {
	case got := <-observed:
		if !slices.Equal(got.settled, []bool{true, true, true}) || got.timers != 1 {
			t.Fatalf("barrier returned before idle: settled (immediate, timed, nested) = %v, pending timers = %d, want the sleep's alone", got.settled, got.timers)
		}
	case <-testwait.After(t):
		t.Fatal("managed barrier did not return at idle")
	}
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("barrier root failed: %+v", out)
		}
	case <-testwait.After(t):
		t.Fatal("barrier root did not finish")
	}
}

// A cancelled context wakes a parked sleeper or latch waiter with no wake
// reservation, and the canceller then parks on the woken fiber's completion.
// Until the woken fiber has its turn back it is runnable, so the managed
// barrier must not return in that window. Each case forks a worker that parks
// a victim, waits at its own barrier until the victim is parked, releases the
// root, cancels the victim and then marks completion. The root's barrier,
// entered after that release, must observe the mark.
//
// The window is held open, not raced for: the woken victim waits in the
// cancelledResume hook until a barrier has decided once inside the window,
// every counter idle while the victim's context is done. Without the
// cancellation rule that decision is idle and the root's barrier returns
// before the mark, every run.
func TestManagedBarrierCountsCancellationWakes(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel func(fc *FiberContext, park func(*FiberContext, Effect[Unit]) (*Fiber[Unit], Exit[Unit])) Exit[Unit]
		victim func(latch *Latch) Effect[Unit]
	}{
		{"interrupt sleeper", nil, func(*Latch) Effect[Unit] { return Sleep(1000) }},
		{"interrupt latch waiter", nil, func(latch *Latch) Effect[Unit] { return AwaitLatch(latch) }},
		{"scope close", func(fc *FiberContext, park func(*FiberContext, Effect[Unit]) (*Fiber[Unit], Exit[Unit])) Exit[Unit] {
			return Invoke(fc, Scoped(func(inner *FiberContext) Exit[Unit] {
				_, out := park(inner, Sleep(1000))
				return out
			}))
		}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduler := NewTestScheduler()
			release := make(chan struct{})
			verdict := make(chan bool, 1)
			var decided sync.Once
			scheduler.hooks = &schedulerHooks{
				cancelledResume: func() { <-release },
				idleChecked: func(window, idle bool) {
					if window {
						decided.Do(func() {
							verdict <- idle
							close(release)
						})
					}
				},
			}
			latch := NewLatch()
			ready := NewLatch()
			// park forks the victim and returns once it is parked: the worker's
			// own barrier is the ordering point. It then releases the root.
			park := func(fc *FiberContext, victim Effect[Unit]) (*Fiber[Unit], Exit[Unit]) {
				parked := Invoke(fc, Fork(victim))
				if parked.IsFailure() {
					return nil, Propagate[Unit](parked)
				}
				if out := AwaitTestSchedulerRegistration(fc, scheduler); out.IsFailure() {
					return nil, out
				}
				return parked.Value, Invoke(fc, SignalLatch(ready))
			}
			cancel := test.cancel
			if cancel == nil {
				cancel = func(fc *FiberContext, park func(*FiberContext, Effect[Unit]) (*Fiber[Unit], Exit[Unit])) Exit[Unit] {
					victim, out := park(fc, test.victim(latch))
					if out.IsFailure() {
						return out
					}
					return Invoke(fc, victim.Interrupt())
				}
			}
			marked := make(chan struct{})
			observed := make(chan bool, 1)
			done := make(chan Exit[Unit], 1)
			go func() {
				done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
					worker := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
						if out := cancel(child, park); out.IsFailure() {
							return out
						}
						close(marked)
						return Succeed(Unit{})
					}))
					if worker.IsFailure() {
						return Propagate[Unit](worker)
					}
					if out := Invoke(fc, AwaitLatch(ready)); out.IsFailure() {
						return out
					}
					if out := AwaitTestSchedulerRegistration(fc, scheduler); out.IsFailure() {
						return out
					}
					select {
					case <-marked:
						observed <- true
					default:
						observed <- false
					}
					return Invoke(fc, worker.Value.Join())
				})
			}()
			select {
			case idle := <-verdict:
				if idle {
					t.Fatal("barrier judged idle while a fiber woken by cancellation had not run again")
				}
			case <-testwait.After(t):
				t.Fatal("no barrier decided inside the cancellation window")
			}
			select {
			case settled := <-observed:
				if !settled {
					t.Fatal("barrier returned while the cancelled fiber was still runnable")
				}
			case <-testwait.After(t):
				t.Fatal("managed barrier did not return")
			}
			select {
			case out := <-done:
				if out.IsFailure() {
					t.Fatalf("cancellation root failed: %+v", out)
				}
			case <-testwait.After(t):
				t.Fatal("cancellation root did not finish")
			}
		})
	}
}

// Interrupting a fiber parked in the managed barrier is an ordinary
// interruption: the barrier's cancelled wait ends in an interrupt cause, not a
// defect, so Interrupt succeeds. The waiter signals before it enters the
// barrier and keeps the turn until it parks there, so the interrupt always
// reaches it parked.
func TestInterruptingABarrierWaiterIsAnInterruption(t *testing.T) {
	scheduler := NewTestScheduler()
	entered := NewLatch()
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
			waiter := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				if out := Invoke(child, SignalLatch(entered)); out.IsFailure() {
					return out
				}
				return AwaitTestSchedulerRegistration(child, scheduler)
			}))
			if waiter.IsFailure() {
				return Propagate[Unit](waiter)
			}
			if out := Invoke(fc, AwaitLatch(entered)); out.IsFailure() {
				return out
			}
			return Invoke(fc, waiter.Value.Interrupt())
		})
	}()
	select {
	case out := <-done:
		if out.IsFailure() {
			t.Fatalf("interrupting a barrier waiter failed: %+v", out.Cause())
		}
	case <-testwait.After(t):
		t.Fatal("interrupting a barrier waiter did not finish")
	}
}
