package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckStrictRefusesUndecided: a law that check time does not decide
// (here its budget runs out) leaves plain check at 0, and check --strict
// exits 1 naming it; a law that is decided passes --strict. Kills
// check-strict-undecided-passes and check-strict-unwired (MODELCHECK T3b:
// an ergo whose check skipped every law exited 0, and orrery read it as
// proved). In process the compiler has no shared identity, so no answer
// is warm here.
func TestCheckStrictRefusesUndecided(t *testing.T) {
	t.Setenv("ERGO_DEV", "off")
	t.Setenv("ERGO_LAWCACHE", "off") // a stored verdict would decide the starved run
	path := filepath.Join(t.TempDir(), "doubles.ergo")
	source := "func double(n int) int { n + n }\n\nlaw Doubles(n int) { double(n) == n * 2 }\n"
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkCommand([]string{path, "--strict"}); err != nil {
		t.Fatalf("a decided law under --strict: %v", err)
	}
	starved := []string{path, "--eval-steps", "1"}
	if err := checkCommand(starved); err != nil {
		t.Fatalf("plain check of an undecided law: %v, want exit 0", err)
	}
	err := checkCommand(append(starved, "--strict"))
	var exit exitStatusError
	if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(exit.message, "law Doubles (incomplete") {
		t.Fatalf("check --strict of an undecided law: %v, want exit 1 naming law Doubles", err)
	}
}

// TestCheckStrictRefusesASampledMachineLaw: an always law of a machine
// whose signal carries an int (no finite list of signals) passes on 100
// sampled runs, evidence "tested"; it is false (forty Adds of 3 pass 100),
// so --strict must not exit 0 on it, while its sometimes law, proved by a
// witness run, is decided. The smallest repro of batch-mc1 T3b O1. Kills
// check-strict-sampled-passes.
func TestCheckStrictRefusesASampledMachineLaw(t *testing.T) {
	t.Setenv("ERGO_DEV", "off")
	t.Setenv("ERGO_LAWCACHE", "off")
	path := filepath.Join(t.TempDir(), "open.ergo")
	source := `type Signal = Add{N int} | Reset{}

type Count { N int }

func step(s Count, event Signal) Decision[Count, string] {
	let mut w = s
	match event {
	case Add as a:
		if a.N < 0 || a.N > 3 {
			return Ignore
		}
		w.N = w.N + a.N
	case Reset:
		w.N = 0
	}
	return Enter{w}
}

func small(s Count) bool { s.N < 100 }

func some(s Count) bool { s.N >= 1 }

type Counter machine[Count, Signal] {
	initial: Count{N: 0},
	step:    step,
	law Small always small,
	law Some sometimes some,
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	err := checkCommand([]string{path, "--strict"})
	var exit exitStatusError
	if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(exit.message, "law Counter.Small (tested, not proved") || strings.Contains(exit.message, "Counter.Some") {
		t.Fatalf("check --strict of a sampled always law: %v, want exit 1 naming law Counter.Small alone", err)
	}
}
