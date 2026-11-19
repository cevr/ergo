package eval

import (
	"strings"
	"testing"
)

// TestInvariantLawsRunAtCheckTime: an always or a sometimes law (X2) is
// decided at check time as ergo test decides it (lower machinelaws.go):
// always p never reaches a state where p is false, sometimes p reaches one
// where p holds, each on the whole graph when its states and signals are
// listed. A true law of each kind is proved, and a false one is refuted
// with ergo test's text. Before MX-EVSKIPS each was skipped. Mutants only
// it kills (testdata/mutants.json): evskips-invariant-skipped,
// evskips-always-reads-holds, evskips-sometimes-is-never (the examples'
// invariant laws hold, so the examples runner sees no refutation that
// a mutant should give).
func TestInvariantLawsRunAtCheckTime(t *testing.T) {
	pkg := checked(t, `type Lamp = Off{} | On{} | Broken{}

type Press = Flip{} | Smash{}

func Step(s Lamp, e Press) Decision[Lamp, Never] {
	match (s, e) {
	case (Off, Flip):
		Enter[Lamp]{Next: On}
	case (On, Flip):
		Enter[Lamp]{Next: Off}
	case (Off | On, Smash):
		Enter[Lamp]{Next: Broken}
	case (Broken, Flip | Smash):
		Reject
	}
}

type Switch machine[Lamp, Press] {
	initial: Off,
	step:    Step,
	law Known always known,
	law Whole always whole,
	law Lit sometimes lit,
	law Fixed sometimes fixed,
}

func known(s Lamp) bool { true }

func whole(s Lamp) bool {
	match s {
	case Broken:
		false
	case Off, On:
		true
	}
}

func lit(s Lamp) bool {
	match s {
	case On:
		true
	case Off, Broken:
		false
	}
}

func fixed(s Lamp) bool { false }
`)
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		status Status
		reason string
	}{
		"law Switch.Known": {Passed, "proved: 3 states"},
		"law Switch.Whole": {Refuted, "the run from Off with []Press{Smash} reaches Broken, where whole is false"},
		"law Switch.Lit":   {Passed, "proved by the run from Off with []Press{Flip}"},
		"law Switch.Fixed": {Refuted, "no run reaches a state where fixed holds"},
	}
	seen := 0
	for _, r := range rep.Results {
		w, ok := want[r.Name]
		if !ok {
			continue
		}
		seen++
		if r.Status != w.status || !strings.Contains(r.Reason, w.reason) {
			t.Errorf("%s: %s (%s), want %s with %q", r.Name, r.Status, r.Reason, w.status, w.reason)
		}
	}
	if seen != len(want) {
		t.Errorf("%d of the %d laws reported: %+v", seen, len(want), rep.Results)
	}
}
