package rt

import (
	"context"
	"errors"
	"testing"
)

// TestGoContextModel holds the task's context.Context (GoContext, IG10) to
// context.Context's contract, as a model gives it: for every sequence of
// up to five steps over {interrupt the task, read Done, read Err}, each
// read from a goroutine other than the task's (Go calls them from its
// own), Done is closed exactly when a stop was requested before the read,
// Err is context.Canceled exactly then and nil otherwise, and the channel
// Done returns is the same at every read. A child of the task sees its
// parent's stop the same way. A failing sequence is printed.
//
// Mutants it alone catches: Err ignores the stop; Done made after the stop
// is left open; Done makes a new channel at each read. (The acceptance,
// cmd/ergo TestGoContextInterrupts, interrupts only after Go read Done.)
func TestGoContextModel(t *testing.T) {
	const steps = "IDE" // interrupt, Done, Err
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
			for i, c := range seq {
				fail := func(format string, args ...any) {
					t.Helper()
					t.Fatalf("child %v, sequence %s, step %d: "+format, append([]any{child, seq, i + 1}, args...)...)
				}
				switch c {
				case 'I':
					root.Interrupt()
					stopped = true
				case 'D':
					done := onOtherGoroutine(func() any { return ctx.Done() }).(<-chan struct{})
					if first == nil {
						first = done
					} else if done != first {
						fail("Done gave another channel")
					}
					select {
					case <-done:
						if !stopped {
							fail("Done is closed with no stop")
						}
					default:
						if stopped {
							fail("Done is open after the stop")
						}
					}
				case 'E':
					err, _ := onOtherGoroutine(func() any { return ctx.Err() }).(error)
					if want := error(nil); stopped {
						if !errors.Is(err, context.Canceled) {
							fail("Err is %v after the stop, want context.Canceled", err)
						}
					} else if err != want {
						fail("Err is %v with no stop, want nil", err)
					}
				}
			}
		}
	}
}

// onOtherGoroutine runs f on a new goroutine and returns its result.
func onOtherGoroutine(f func() any) any {
	out := make(chan any, 1)
	go func() { out <- f() }()
	return <-out
}
