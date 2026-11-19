package eval

import (
	"strings"
	"testing"
)

// TestRefutationOutlivesItsShrink: a refuted case stays refuted when its
// shrink spends the step budget (EVAL-SHRINK-REFUTED; found by STD
// TOTP-FLOOR r3, where totp's early-exit mutant was reported incomplete
// at 1,000,000 steps after r2 showed it refuted at case 1). The budget is
// one and a half cases of Slow's work, measured on Twin, which does the
// same work and passes: the drawn case is refuted within it, and the
// shrink's first replay runs out. The law is refuted, at the drawn case,
// with the drawn argument; an obligation a refutation already decided is
// never downgraded to incomplete.
//
// Mutant esr-shrink-stop-wins (a replay that runs out of steps is the
// result again) is its kill.
func TestRefutationOutlivesItsShrink(t *testing.T) {
	const body = `(n int) {
	let mut s = 0
	for i in 0..2000 {
		s = s + i % 3
	}
	assert HOLDS && s >= 0
}`
	program := func(name, holds string) string {
		return "test " + name + strings.Replace(body, "HOLDS", holds, 1)
	}
	twin := checked(t, program("Twin", "n == n"))
	slow := checked(t, program("Slow", "n == 123456789"))
	for seed := uint64(1); seed <= 4; seed++ {
		b := DefaultBudget()
		b.Seed = seed
		rep, err := Run(twin, b)
		if err != nil || rep.Results[0].Status != Passed || rep.Results[0].Cases < 2 {
			t.Fatalf("seed %d: Twin %+v %v", seed, rep.Results, err)
		}
		perCase := rep.Steps / int64(rep.Results[0].Cases)
		b.Steps = perCase * 3 / 2
		rep, err = Run(slow, b)
		if err != nil {
			t.Fatal(err)
		}
		r := rep.Results[0]
		if r.Status != Refuted || r.Case != 1 || len(r.Arguments) != 1 || r.Arguments[0].Name != "n" || r.Arguments[0].Value == "" {
			t.Fatalf("seed %d: with %d steps (a case is %d), Slow is %s (%s) at case %d with %v, want refuted at case 1 with its drawn n", seed, b.Steps, perCase, r.Status, r.Reason, r.Case, r.Arguments)
		}
	}
}
