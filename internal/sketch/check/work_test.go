package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestWorkIsNotAPreludeName pins where the work type lives: std
// machines.Work, so a program may declare a function or a type named Work
// (a prelude name could not be declared, D-D3), beside machines.Work.
func TestWorkIsNotAPreludeName(t *testing.T) {
	for name, src := range map[string]string{
		"func":                 "func Work(x int) int { x }\n\nfunc main() { let _ = Work(1) }\n",
		"type":                 "type Work { Run int }\n\nfunc main() { let _ = Work{Run: 1} }\n",
		"beside machines.Work": "import machines\n\ntype Work { Run int }\n\ntype Sig = A | B\ntype A {}\ntype B {}\n\nfunc w() machines.Work[Sig] { machines.Do(func() Sig { A }) }\n\nfunc main() { let _ = Work{Run: 1}\n\tlet _ = w() }\n",
	} {
		if _, diags := check.CheckSource(src); len(diags) != 0 {
			t.Errorf("%s: %v\n%s", name, diags, src)
		}
	}
}

// TestFailureMapInfers is the smallest repro of a fixed inference bug
// (NP3b): a function-literal argument for a parameter whose type is the
// value type of a row parameter (machines.Attempt's onFailure func(f F)
// E) did not unify, since the unifier read the row value as a union of
// no variants: "the argument has type func(U) Sig, which does not match
// func(U) Sig". The row binds from the argument as a row after fails does.
// Mutant unify-row-value-param (the row-value case of unify removed): the
// call is refused.
func TestFailureMapInfers(t *testing.T) {
	src := `import machines

type U {}
type V {}
type Got { N int }
type Sig = Got | U | V

func r(n int) int fails U | V {
	if n > 1 {
		return V
	}
	if n > 0 {
		return U
	}
	n
}

func w(n int) machines.Work[Sig] {
	machines.Attempt(func() int fails U | V { try r(n) }, func(m int) Sig { Got{N: m} }, func(f) {
		let s Sig = f
		s
	})
}

func main() { let _ = w(1) }
`
	if _, diags := check.CheckSource(src); len(diags) != 0 {
		t.Errorf("%v\n%s", diags, src)
	}
}
