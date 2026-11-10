package rt

import (
	"context"
	"errors"
	"testing"
)

// TestGoContextModel holds the task's context.Context (GoContext, IG10) to
// context.Context's contract, as a model gives it: for every sequence of
// up to five steps over {interrupt the task, read Done, read Err, the
// task's own wait}, each Done and Err read from a goroutine other than the
// task's (Go calls them from its own), Done is closed exactly when a stop
// was requested before the read, Err is context.Canceled exactly then and
// nil otherwise, and the channel Done returns is the same at every read,
// and is the task's own (Task.Done). A child of the task sees its
// parent's stop the same way. A failing sequence is printed.
//
// A Done read is a goroutine that Go started and does not join (`go
// func() { <-ctx.Done() }()`): the test joins it only before the next
// interrupt and at the end, so the task's own wait (W, on this goroutine,
// the task's) runs with no happens-before edge to a channel that Go made.
// Under -race, a W that reads the channel without that edge fails
// (batch-14 M1: Task.Done read stop unlocked while Go wrote it).
//
// Mutants it alone catches: Err ignores the stop; Done made after the stop
// is left open; Done makes a new channel at each read; Task.Done reads stop
// without the made flag (-race). (The acceptance, cmd/ergo
// TestGoContextInterrupts, interrupts only after Go read Done.)
func TestGoContextModel(t *testing.T) {
	const steps = "IDEW" // interrupt, Done, Err, the task's wait
	var seqs []string
	var grow func(s string)
	grow = func(s string) {
		seqs = append(seqs, s)
		if len(s) < 5 {
			for _, c := range steps {
				grow(s + string(c))
			}
		}
	}
	grow("")
	for _, child := range []bool{false, true} {
		for _, seq := range seqs {
			root := Root("model")
			task := root
			if child {
				task = &Task{}
				task.init(root, "child", false, false)
			}
			ctx := GoContext(task)
			stopped := false
			var first <-chan struct{}
			// pending are the Done reads not joined yet, each with the
			// stop state it was issued under and its step.
			type read struct {
				done    chan (<-chan struct{})
				stopped bool
				step    int
			}
			var pending []read
			fail := func(step int, format string, args ...any) {
				t.Helper()
				t.Fatalf("child %v, sequence %s, step %d: "+format, append([]any{child, seq, step}, args...)...)
			}
			checkDone := func(step int, done <-chan struct{}, stopped bool) {
				t.Helper()
				if first == nil {
					first = done
				} else if done != first {
					fail(step, "Done gave another channel")
				}
				select {
				case <-done:
					if !stopped {
						fail(step, "Done is closed with no stop")
					}
				default:
					if stopped {
						fail(step, "Done is open after the stop")
					}
				}
			}
			join := func() {
				t.Helper()
				for _, r := range pending {
					checkDone(r.step, <-r.done, r.stopped)
				}
				pending = pending[:0]
			}
			for i, c := range seq {
				step := i + 1
				switch c {
				case 'I':
					join()
					root.Interrupt()
					stopped = true
				case 'D':
					r := read{done: make(chan (<-chan struct{}), 1), stopped: stopped, step: step}
					go func() { r.done <- ctx.Done() }()
					pending = append(pending, r)
				case 'W':
					checkDone(step, task.Done(), stopped)
				case 'E':
					err, _ := onOtherGoroutine(func() any { return ctx.Err() }).(error)
					if want := error(nil); stopped {
						if !errors.Is(err, context.Canceled) {
							fail(step, "Err is %v after the stop, want context.Canceled", err)
						}
					} else if err != want {
						fail(step, "Err is %v with no stop, want nil", err)
					}
				}
			}
			join()
		}
	}
}

// onOtherGoroutine runs f on a new goroutine and returns its result.
func onOtherGoroutine(f func() any) any {
	out := make(chan any, 1)
	go func() { out <- f() }()
	return <-out
}
