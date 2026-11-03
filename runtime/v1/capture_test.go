package v1

import (
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// Capture and Raise (E3a M3): code outside the lowered frames, such as the
// test build's case runner, recovers a panic to classify it and raises what
// it does not keep. Capture then Raise must be lossless: the program's
// report is the one it writes when nothing recovered the panic, every
// reason, each defect's Ergo frames and chain included.

// through runs f; when recovering, a deferred recover classifies the panic
// with Capture and raises it again with Raise, as the case runner does with
// a defect. Both modes leave the same frames below the panic.
//
//go:noinline
func through(t *rt.Task, recovering bool, f func()) {
	defer func() {
		if recovering {
			Raise(Capture(t, recover()))
		}
	}()
	f()
}

// TestCaptureRaiseLossless: for a fresh defect, a carried exit (a body
// defect beside an exit action's defect, joined at a frame's end), a typed
// failure carried beside a defect and an interruption, the report and the
// status after Capture and Raise equal those of the unrecovered panic.
func TestCaptureRaiseLossless(t *testing.T) {
	file := withSource(t)
	panics := []struct {
		name string
		f    func(t *rt.Task)
		want string // a line the report holds
	}{
		{"fresh defect", func(*rt.Task) { explode() }, "panic: runtime error: index out of range [3] with length 0\n"},
		{"carried exit", func(t *rt.Task) {
			frameNoRow(t, func(s *Scope) {
				OnExit(s, func() { boom() })
				explode()
			})
		}, "(in an exit action)\n"},
		{"failure beside a defect", func(t *rt.Task) {
			_ = frame(t, func(s *Scope) error {
				OnExit(s, func() { boom() })
				return bad{"body"}
			})
		}, "error: bad body\n"},
		{"interruption", func(*rt.Task) { panic(rt.Interrupted) }, "interrupted\n"},
	}
	for _, p := range panics {
		t.Run(p.name, func(t *testing.T) {
			run := func(recovering bool) (string, int) {
				var b strings.Builder
				status := report(&b, runMain(func(task *rt.Task) error {
					through(task, recovering, func() { p.f(task) })
					return nil
				}))
				return strings.ReplaceAll(b.String(), file, "F"), status
			}
			plain, plainStatus := run(false)
			got, status := run(true)
			if !strings.Contains(plain, p.want) {
				t.Fatalf("the unrecovered report lacks %q:\n%s", p.want, plain)
			}
			if got != plain || status != plainStatus {
				t.Fatalf("after Capture and Raise: status %d, report:\n%s\nunrecovered: status %d, report:\n%s", status, got, plainStatus, plain)
			}
		})
	}
}

// TestCaptureClassifies: Capture of nil is no exit, and Raise of no exit
// raises nothing; a fresh panic is one defect holding its value and the
// stack at the panic; a carried exit comes back with its reasons, nothing
// recorded twice; an interruption stays one.
func TestCaptureClassifies(t *testing.T) {
	root := rt.Root("main")
	if err := Capture(root, nil); err != nil {
		t.Fatalf("Capture(nil) = %v", err)
	}
	if p := recovered(func() { Raise(nil) }); p != nil {
		t.Fatalf("Raise(nil) panicked with %v", p)
	}
	var fresh error
	func() {
		defer func() { fresh = Capture(root, recover()) }()
		explode()
	}()
	rs := reasons(fresh)
	if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
		t.Fatalf("a fresh panic: %v; want one defect", fresh)
	}
	if p, ok := rs[0].Value.(*Panic); !ok || !strings.Contains(string(p.Stack), "explode") {
		t.Fatalf("a fresh defect's value %#v holds no stack at the panic", rs[0].Value)
	}
	carried := Capture(root, recovered(func() { Raise(fresh) }))
	if got := reasons(carried); len(got) != 1 || got[0].Value != rs[0].Value {
		t.Fatalf("a carried exit came back as %v; want the same defect once", carried)
	}
	if err := Capture(root, rt.Interrupted); err != rt.Interrupted {
		t.Fatalf("an interruption came back as %v", err)
	}
}
