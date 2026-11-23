package eval

import "testing"

// TestAdmissionLawsWaitForTheTestBinary is the smallest repro of a fixed
// bug: check time ran a law with a where over earlier parameters as an
// ordinary law, drawing b without the where, so Hashing's EqualKeysHashEqual
// for a case-folding provider was refuted at a = "a", b = "" (examples/
// triage: ergo check exited 1, and ergo test answered from the evaluator).
// Such a law is skipped at check time (types.md, Laws 15), which sends
// ergo test to the test binary's near-variant search. Mutant:
// eval-admission-law-run.
func TestAdmissionLawsWaitForTheTestBinary(t *testing.T) {
	pkg := checked(t, `type Parity {} implements Hashing[int]

func Hash(_ Parity, k int) uint64 { hash(k % 2 == 0) }

func Equal(_ Parity, a int, b int) bool { a % 2 == b % 2 }
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, res := range rep.Results {
		switch res.Name {
		case "law Hashing.EqualKeysHashEqual for Parity", "law Hashing.EqualIsTransitive for Parity", "law Hashing.EqualKeysNormalEqual for Parity":
			seen++
			if res.Status != Skipped || res.Reason != AdmissionVerdict {
				t.Errorf("%s is %s (%s), want skipped (%s)", res.Name, res.Status, res.Reason, AdmissionVerdict)
			}
		}
	}
	if seen != 3 {
		t.Errorf("%d of Hashing's 3 laws with a where over earlier parameters were run for Parity: %+v", seen, rep.Results)
	}
}
