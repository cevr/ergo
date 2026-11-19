package eval

import "testing"

// TestRefutationOutlivesItsShrink: a refuted case stays refuted when its
// shrink spends the step budget (EVAL-SHRINK-REFUTED; found by STD
// TOTP-FLOOR r3, where totp's early-exit mutant was reported incomplete
// at 1,000,000 steps after r2 showed it refuted at case 1). Each case of
// Slow does about a quarter of the budget's work, so the drawn case is
// refuted within it and the first shrink replays spend the rest. The law
// is refuted, at the drawn case, with the drawn arguments; an obligation
// a refutation already decided is never downgraded to incomplete.
//
// Mutant esr-shrink-stop-wins (a replay that runs out of steps is the
// result again) is its kill.
func TestRefutationOutlivesItsShrink(t *testing.T) {
	pkg := checked(t, `test Slow(n int) {
	let mut s = 0
	for i in 0..60000 {
		s = s + i % 3
	}
	assert n == 123456789 && s >= 0
}`)
	for seed := uint64(1); seed <= 4; seed++ {
		b := DefaultBudget()
		b.Seed = seed
		rep, err := Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		r := rep.Results[0]
		if r.Status != Refuted || r.Case != 1 || len(r.Arguments) != 1 || r.Arguments[0].Name != "n" || r.Arguments[0].Value == "" {
			t.Fatalf("seed %d: Slow is %s (%s) at case %d with %v, want refuted at case 1 with its drawn n", seed, r.Status, r.Reason, r.Case, r.Arguments)
		}
	}
}
