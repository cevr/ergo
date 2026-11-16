package lower

import "testing"

// neverProgram uses Never where the checker leaves arms out (FKG2;
// types.md, Never): a match over a machine's decisions and a run's
// outcome with no Emit, Done or Finished arm, a match of no arm on a Never,
// Absurd, and an Option of Never.
const neverProgram = `import machines

type S = A{N int} | B{N int}
type E = Go{} | Halt{}

func step(s S, e E) Decision[S, Never] {
	match (s, e) {
	case (A as a, Go):
		Enter[S]{Next: B{N: a.N + 1}}
	case (B as b, Go):
		Stay[S]{Next: A{N: b.N + 1}}
	case (_, Halt):
		Ignore
	}
}

type M machine[S, E] { initial: A{N: 0}, step: step }

func kind(d Decision[S, Never]) string {
	match d {
	case Enter[S]:
		"enter"
	case Stay[S]:
		"stay"
	case Ignore, Reject:
		"keep"
	}
}

func last(o machines.Outcome[S, Never]) S {
	match o {
	case machines.Running[S, Never] as r:
		r.State
	}
}

func none(n Never) int {
	match n {}
}

func absurd(n Never) string { n.Absurd[string]() }

func count(o Option[Never]) int {
	match o {
	case None:
		0
	}
}

func main() needs Console {
	let o = machines.Run(M{}, []E{Go, Go, Halt}) catch {
	case machines.Rejected:
		return
	}
	match last(o) {
	case A as a:
		Console.Println("\(a.N) \(kind(step(a, Go))) \(count(None))")
	case B as b:
		Console.Println("\(b.N)")
	}
}
`

// TestNeverLowers: a program that leaves out the arms of what holds a
// Never lowers, with the match tree and flat, and the Go type-checks.
// Mutants: fkg2-empty-match-ice (a match of no arm reaches the tree, whose
// root fails) and fkg2-never-record-body (Never lowers as a record, which
// has no struct body).
func TestNeverLowers(t *testing.T) {
	var programs []*lowered
	for _, flat := range []bool{false, true} {
		name := "tree"
		if flat {
			name = "flat"
		}
		o := lowerSourceMode(name, neverProgram, flat).lowered
		if o.err != nil || len(o.diags) > 0 || o.panicked != nil || len(o.refused) > 0 {
			t.Fatalf("%s: err %v, diags %v, panic %v, refused %v", name, o.err, firstN(o.diags, 5), o.panicked, o.refused)
		}
		programs = append(programs, o)
	}
	typecheckGo(t, programs)
}
