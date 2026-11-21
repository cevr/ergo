package lower

import (
	"fmt"
	"strings"
	"testing"
)

// stepAllocSource is a machine whose step neither inliner takes (its
// body is past both budgets) and whose state has a field, so its
// Decision cannot be a value Go boxes from its static table: before MSB1
// each Enter and Stay it returned was runtime.convT, one heap allocation
// per signal (corpus-wave PA, machine-step-boxing.md).
const stepAllocSource = `type Start {}
type Digits { n int }
type Point { n int }
type Frac { n int, k int }
type Mark { n int }
type Part = Start | Digits | Point | Frac | Mark

type Digit { v int }
type Dot {}
type Exp {}
type Other {}
type Class = Digit | Dot | Exp | Other

type Ended {}
type Broken {}
type Verdict = Ended | Broken

func step(p Part, c Class) Decision[Part, Verdict] {
	match (p, c) {
	case (Start, Digit as d):
		Enter[Part]{Next: Digits{n: d.v}}
	case (Start, _):
		Done[Verdict]{Output: Broken}
	case (Digits as w, Digit as d):
		if w.n > 1000000 {
			Stay[Part]{Next: Digits{n: w.n % 7 + d.v}}
		} else {
			Stay[Part]{Next: Digits{n: w.n * 10 + d.v}}
		}
	case (Digits as w, Dot):
		Enter[Part]{Next: Point{n: w.n}}
	case (Digits as w, Exp):
		Enter[Part]{Next: Mark{n: w.n}}
	case (Digits, Other):
		Done[Verdict]{Output: Ended}
	case (Point as q, Digit as d):
		Enter[Part]{Next: Frac{n: q.n * 10 + d.v, k: 1}}
	case (Point, _):
		Done[Verdict]{Output: Broken}
	case (Frac as f, Digit as d):
		if f.k > 6 {
			Stay[Part]{Next: Frac{n: f.n, k: f.k}}
		} else {
			Stay[Part]{Next: Frac{n: f.n * 10 + d.v, k: f.k + 1}}
		}
	case (Frac as f, Exp):
		Enter[Part]{Next: Mark{n: f.n + f.k}}
	case (Frac, _):
		Done[Verdict]{Output: Ended}
	case (Mark as m, Digit as d):
		Stay[Part]{Next: Mark{n: m.n + d.v * 3 - 1}}
	case (Mark, _):
		Done[Verdict]{Output: Ended}
	}
}

type Numeral machine[Part, Class] {
	initial: Start,
	step:    step,
}

func Scan(cs []Class) int {
	let mut p Part = Start
	let mut i = 0
	for i < len(cs) {
		match step(p, cs[i]) {
		case Enter[Part] as e:
			p = e.Next
		case Stay[Part] as s:
			p = s.Next
		case Done[Verdict], Emit[Part, Verdict], Tell[Part, Verdict], Ignore, Reject:
			return i
		}
		i += 1
	}
	i
}

func main() needs Console {
	Console.Println("\(Scan([]Class{Digit{v: 1}, Dot, Digit{v: 2}, Other}))")
}
`

const stepAllocDriver = `package main

import "testing"

func TestStepAllocs(t *testing.T) {
	env := &Env{}
	_ = env
	classes := func(n int) []Class {
		xs := make([]Class, n)
		for i := range xs {
			xs[i] = Class{}.of0(Digit{v: i % 10})
		}
		return xs
	}
	short, long := classes(10), classes(1000)
	a := testing.AllocsPerRun(50, func() { _ = Scan(short) })
	b := testing.AllocsPerRun(50, func() { _ = Scan(long) })
	t.Logf("ALLOCS %v %v", a, b)
}
`

// TestMachineStepAllocates is the counted growth guard of MSB1: a scan of
// 1000 signals through a step that returns its Decision from a call
// allocates what a scan of 10 does, since the Decision is a struct
// returned by value (genericunion.go) and the state a flat union. It
// builds the emitted code with a Go test beside it, because allocation is
// a fact of Go's escape analysis of the lowered code, which no in-process
// check sees. It kills the mutant that keeps the old lowering
// (msb1-generic-off) and the step's call (the step is
// a Go call, not inlined: the guard reads the lowered text).
func TestMachineStepAllocates(t *testing.T) {
	needsBuild(t)
	prog := lowerMachineMode(t, "stepallocs", stepAllocSource, "", "")
	if !strings.Contains(prog.mainGo(), ":= step(p") {
		t.Fatalf("the scan does not call step (an inliner took it):\n%s", prog.mainGo())
	}
	out := goTestEmittedVerbose(t, prog, stepAllocDriver)
	_, rest, ok := strings.Cut(out, "ALLOCS ")
	if !ok {
		t.Fatalf("no ALLOCS line in\n%s", out)
	}
	var short, long float64
	if _, err := fmt.Sscan(rest, &short, &long); err != nil {
		t.Fatalf("the ALLOCS line: %v\n%s", err, out)
	}
	if short != long {
		t.Errorf("a scan of 10 signals allocates %v times, of 1000 %v times: the step's Decision allocates", short, long)
	}
	t.Logf("allocations per scan: %v (10 signals), %v (1000 signals)", short, long)
}
