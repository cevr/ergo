package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A machine whose after gives a machines.Every charges time.Clock to its
// runner's row W, never to its step's R (FKG1): the runner reads the clock
// when the timer fires, and a fold fires no timer. Each row kills a mutant
// no other test catches: fkg1-every-runner-no-clock (the conversion leaves
// time.Clock out of W, so an actor without a clock is quiet and its timer
// reads a nil clock), fkg1-every-clock-in-step (the clock goes into R, so
// the fold's law is S102), fkg1-every-after-row-admitted (an Every after
// with a capability row is not refused).
func TestEveryRow(t *testing.T) {
	const prelude = `import machines
import time

type S = Idle{} | Busy{}

type Sig = Go{} | Tick{At time.Time}

func step(s S, e Sig) Decision[S, Never] {
	match e {
	case Go:
		Enter[S]{Next: Busy}
	case Tick:
		Enter[S]{Next: Idle}
	}
}

func tickAt(at time.Time) Sig { Tick{At: at} }
`
	const every = `
func every(s S) Option[machines.Every[Sig]] { Some{machines.Every[Sig]{In: 1ms, Signal: tickAt}} }

type M machine[S, Sig] { initial: Idle, step: step, after: every }
`
	cases := []struct{ name, src string }{
		{"fold-needs-no-clock", prelude + every + `
law FoldNeedsNoClock(n int) {
	let o = machines.Run(M{}, []Sig{Go}) catch {
	case machines.Rejected:
		return false
	}
	machines.LastState(o) == Busy
}
`},
		{"runner-needs-clock", "import actor\n" + prelude + every + `
func run() needs Scheduler {
	let a = actor.StartMachine(M{}, 1, func(o Never) {}) // want S102
	_ = a
}
`},
		{"every-after-row-refused", prelude + `
type Codes { Check(c int) bool }

func every(s S) Option[machines.Every[Sig]] needs Codes {
	if Codes.Check(1) { None } else { None }
}

type M machine[S, Sig] { initial: Idle, step: step, after: every } // want S108
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			want, have := expected(tc.src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Fatalf("want %v, got %v\n\t%s\n%s", want, have, describe(diags), tc.src)
			}
		})
	}
}
