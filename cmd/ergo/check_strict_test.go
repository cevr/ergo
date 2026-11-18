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
