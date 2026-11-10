package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D1 round 4: the round 3 review's findings (stage-D1-round-3-
// findings.md), each as the program that showed it. runD1 compares the
// exact diagnostics, as CODE:line.

// machineDecls declares a machine type, Breaker, whose value has no fields.
const machineDecls = "type Closed {}\ntype Open {}\ntype State = Closed | Open\ntype Trip {}\ntype Signal = Trip\n" +
	"func step(s State, sig Signal) Decision[State, string] {\n\tmatch s {\n\tcase Closed:\n\t\treturn Enter{Open}\n\tcase Open:\n\t\treturn Done{\"open\"}\n\t}\n}\n" +
	"type Breaker machine[State, Signal] {\n\tinitial: Closed,\n\tstep:    step,\n}\n"

// M4: every route that refuses a literal's element skips its value as an
// orphan, so the callback frames in it are recorded: the reference walk
// agrees, and a let mut local the frame reassigns draws no false S110
// beside the literal's diagnostic. The first six are the review's; the
// first is its fuzz seed (testdata/fuzz/FuzzCheck/3da3f8287b8175fd).
func TestEveryLiteralRefusalRecordsItsFrames(t *testing.T) {
	for i, src := range []string{
		"func e(A)A{A{{{0(){e}}}}}",
		"func e() { int{func() { e() }} }",
		"func e() { int{0() { e() }} }",
		"func e() { Map[int,int]{0() { e() }} }",
		"func e() int { let mut n = 0; int{0() { n = 1 }}; n }",
		"func e() int { let mut n = 0; Map[int,int]{0() { n = 1 }}; n }",
		"type P { X func() }\nfunc e() int { let mut n = 0; _ = P{X: func() {}, X: func() { n = 1 }}; n }",
		machineDecls + "func e() int { let mut n = 0; _ = Breaker{func() { n = 1 }}; n }",
		machineDecls + "func e() { _ = Breaker{0() { e() }} }",
	} {
		_, ds := check.CheckSource(src)
		if len(ds) == 0 {
			t.Errorf("%d: want the literal's diagnostic\n%s", i, src)
		}
		if p := check.RefWalkProblems(src); len(p) > 0 {
			t.Errorf("%d: reference walk: %v\n%s", i, p, src)
		}
		for _, d := range ds {
			if strings.Contains(d.Message, "never reassigned") {
				t.Errorf("%d: false advice from a skipped reassignment: %s\n%s", i, describe(ds), src)
			}
		}
	}
}
