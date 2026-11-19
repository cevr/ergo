package lower

import "testing"

// TestAdmitLevelTable holds admitLevel to the admission table of LAWFFI
// r2 §1 (facts.md, Evidence 2), over its whole domain: every verdict a
// ledger line may hold, with each run and none, for each trust class of
// the shortcut registry. Proved admits every class; validated admits the
// value class only, and only with the run that gave it named; tested,
// skipped and refuted admit nothing.
//
// Mutants it kills (TestLedgerGatesRewrites asks only for the value
// class): lfl-validated-admits-memory-class, and, at the function itself,
// lfl-tested-admitted and lfl-refuted-applies.
func TestAdmitLevelTable(t *testing.T) {
	model := func(verdict, by string, class trust) bool {
		switch verdict {
		case "proved":
			return true
		case "validated":
			return by != "" && class == valueClass
		}
		return false
	}
	verdicts := []string{""}
	for v := range ledgerVerdicts() {
		verdicts = append(verdicts, v)
	}
	for _, v := range verdicts {
		for _, by := range []string{"", "native", "eval", "guard"} {
			for _, class := range []trust{valueClass, memoryClass, detectorClass, checkedClass} {
				if got, want := admitLevel(v, by, class), model(v, by, class); got != want {
					t.Errorf("admitLevel(%q, %q, %s) = %v, want %v", v, by, class, got, want)
				}
			}
		}
	}
}
