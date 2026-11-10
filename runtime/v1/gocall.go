package v1

import (
	"context"

	"ergo.local/prototype/runtime/rt"
)

// Go calls Ergo (IG11; docs/spec/interop.md, Go calls Ergo). An Ergo
// library exports each exported function of its root package as a Go
// function whose first parameter is a context.Context; the exported
// function runs the Ergo body through Call.

// Call runs body as the body of a new root task named span, on the calling
// goroutine, for a Go caller: the Ergo code runs on the caller's goroutine
// (and on its OS thread, when the caller locked one: a GUI host's main
// thread), and returns before Call does, with every task it started ended.
//
// ctx cancels the call: when ctx is done, the task is interrupted (the same
// stop a timeout or a parent's interruption makes, runtime.md I1). The Ergo
// code observes it at its next interruption point, unwinds, and runs its
// releases, masked (I2); the call then returns ctx.Err(). A ctx that is
// never done (context.Background) costs nothing: no goroutine and no
// registration. A registration (context.AfterFunc) is removed before Call
// returns, so no goroutine is left behind.
//
// A typed failure returns as an error whose value is the Ergo failure; a
// defect returns as the defect error that names the Ergo call sites, as the
// program entry reports it; an interruption that ctx did not cause returns
// as rt.Interrupted.
//
// Prior art: a Go net/http handler gets the request's context and must
// watch it by hand; Kotlin's runBlocking and Swift's Task bridge a host
// call into structured concurrency with cancellation propagated inward, as
// here; Effect.runPromise takes an AbortSignal that interrupts the fiber.
func Call[T any](ctx context.Context, span string, body func(*rt.Task) (T, error)) (T, error) {
	t := rt.Root(span)
	if ctx.Done() != nil {
		stop := context.AfterFunc(ctx, t.Interrupt)
		defer stop()
	}
	// runOn is the root task's run under the defect owner (guarded), as
	// main's: v is written by the body before runOn returns.
	var v T
	err := runOn(t, func(t *rt.Task) error {
		var err error
		v, err = body(t)
		return err
	})
	if err != nil && rt.OnlyInterruption(err) && ctx.Err() != nil {
		return v, ctx.Err()
	}
	return v, err
}
