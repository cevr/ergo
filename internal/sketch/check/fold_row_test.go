package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A machine's work row is its own parameter W, apart from the step's R
// (fumb-e, lane MB F1): a fold (machines.Run) runs no work, so it needs R
// only, and the runner (actor.StartMachine) needs R | W. Each row kills a
// mutant no other test catches: fold-needs-step-row-only (the work's row
// is unioned into R again, so the law is S102), runner-needs-work-row
// (StartMachine leaves W out of its row, so a missing Codes is quiet).
func TestFoldRow(t *testing.T) {
	const prelude = `import machines

type Codes { Check(c int) bool }

type S = Idle{} | Busy{N int}

type Sig = Go{N int} | Finish{}

func step(s S, e Sig) Decision[S, int] {
	match e {
	case Go as g:
		Enter[S]{Next: Busy{N: g.N}}
	case Finish:
		Done[int]{Output: 1}
	}
}

func work(s S) Option[machines.Work[Sig, Codes]] {
	match s {
	case Busy as b:
		Some{machines.Work{Run: func() Sig needs Codes {
			if Codes.Check(b.N) { Finish } else { Finish }
		}}}
	case Idle:
		None
	}
}

type M machine[S, Sig] { initial: Idle, step: step, work: work }
`
	cases := []struct{ name, src string }{
		{"fold-needs-step-row-only", prelude + `
law FoldNeedsNoCodes(n int) {
	let o = machines.Run(M{}, []Sig{Go{N: n}, Finish}) catch {
	case machines.Rejected:
		return false
	}
	match o {
	case machines.Finished[S, int]:
		true
	case machines.Running[S, int]:
		false
	}
}
`},
		{"runner-needs-work-row", "import actor\n" + prelude + `
func run() needs Scheduler {
	let a = actor.StartMachine(M{}, 1, func(o int) {}) // want S102
	_ = a
}
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
