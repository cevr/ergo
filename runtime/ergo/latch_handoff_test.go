package ergo

import (
	"context"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// A signalled waiter that has not run yet is cancelled by its owner's close
// before it can take the turn (docs/runtime.md, latch handoff rule 4). The
// closer signals a waiter it owns and then closes its scope, by leaving a
// scope block or by exiting the fiber that forked the waiter. The third case
// has the shape of layer shutdown (layerBuild.close): the owner is a separate
// node scope and the closer is another fiber context sharing the turn. The
// layer API itself cannot reach this precondition, because every path from a
// signal to a node close passes a turn handoff that legitimately lets the
// unowned waiter run first.
//
// The window is held open, not raced for: the closeHandoff hook keeps the
// closer, which has just handed off the turn, from going on until the waiter
// has either reported or finished. A close that hands off the turn before it
// cancels lets the woken waiter take that turn uncancelled and report, every
// run; a close that cancels first leaves the waiter interrupted at its next
// checkpoint, so it never reports.
func TestScopeCloseCancelsASignalledWaiterBeforeItRuns(t *testing.T) {
	for _, test := range []struct {
		name string
		// close runs owned(scope) inside an owner whose close ends the case.
		close func(fc *FiberContext, owned func(*FiberContext) Exit[Unit]) Exit[Unit]
	}{
		{"scope block", func(fc *FiberContext, owned func(*FiberContext) Exit[Unit]) Exit[Unit] {
			return Invoke(fc, Scoped(func(inner *FiberContext) Exit[Unit] { return owned(inner) }))
		}},
		{"fiber exit", func(fc *FiberContext, owned func(*FiberContext) Exit[Unit]) Exit[Unit] {
			closer := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] { return owned(child) }))
			if closer.IsFailure() {
				return Propagate[Unit](closer)
			}
			return Invoke(fc, closer.Value.Join())
		}},
		{"foreign closer (layer shutdown shape)", func(fc *FiberContext, owned func(*FiberContext) Exit[Unit]) Exit[Unit] {
			base := withoutSchedulerContinuation(context.WithoutCancel(fc.ctx))
			owner := newScopeWithDriver(base, fc.scope, fc.timerDriver())
			node := &FiberContext{ctx: owner.ctx, scope: owner, driver: owner.driver, turn: fc.turn, continuation: fc.continuation}
			out := owned(node)
			return withCleanup(out, owner.closeWithContext(fc, false))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduler := NewTestScheduler()
			latch := NewLatch()
			reported := make(chan struct{})
			finished := make(chan struct{})
			held := make(chan struct{})
			var target atomic.Pointer[Scope]
			scheduler.hooks = &schedulerHooks{
				closeHandoff: func(scope *Scope) {
					if scope != target.Load() {
						return
					}
					close(held)
					select {
					case <-reported:
					case <-finished:
					case <-testwait.After(t):
						t.Errorf("close handoff timed out: the waiter neither reported nor finished")
					}
				},
			}
			owned := func(fc *FiberContext) Exit[Unit] {
				target.Store(fc.Scope())
				waiter := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
					defer close(finished)
					if out := Invoke(child, AwaitLatch(latch)); out.IsFailure() {
						return out
					}
					return Invoke(child, func(*FiberContext) Exit[Unit] {
						close(reported)
						return Succeed(Unit{})
					})
				}))
				if waiter.IsFailure() {
					return Propagate[Unit](waiter)
				}
				if out := AwaitTestSchedulerRegistration(fc, scheduler); out.IsFailure() {
					return out
				}
				return Invoke(fc, SignalLatch(latch))
			}
			done := make(chan Exit[Unit], 1)
			go func() {
				done <- RunContextWithScheduler(context.Background(), scheduler, func(fc *FiberContext) Exit[Unit] {
					return test.close(fc, owned)
				})
			}()
			var out Exit[Unit]
			select {
			case out = <-done:
			case <-testwait.After(t):
				t.Fatal("close did not complete")
			}
			select {
			case <-held:
			default:
				t.Fatal("the close never reached its handoff")
			}
			select {
			case <-reported:
				t.Fatal("a signalled waiter took the turn and reported after its owner began closing")
			default:
			}
			if out.IsFailure() {
				t.Fatalf("close over an interrupted waiter failed: %v", out.Cause())
			}
		})
	}
}
