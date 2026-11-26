package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
)

func runStdUIExploreLaw(t *testing.T, law string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "ergo")
	build := ergobuild.Command(bin, "./cmd/ergo", "-p", "4")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	run := exec.Command(bin, "test", "internal/sketch/std/ui", "--law", law, "--seed", "1")
	run.Dir = root
	run.Env = append(os.Environ(), "ERGO_FALLBACK=on")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("law %s failed: %v\n%s", law, err, out)
	}
	if strings.Contains(string(out), "\nskip ") || strings.HasPrefix(string(out), "skip ") {
		t.Fatalf("law %s skipped:\n%s", law, out)
	}
}

// TestStdUIExploreLawsRun runs all six autonomous UI exploration laws of std ui
// natively via ergo test and verifies every ui-explore row of runners.txt.
func TestStdUIExploreLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "ergo")
	build := ergobuild.Command(bin, "./cmd/ergo", "-p", "4")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		lawName := row.Law
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			run := exec.Command(bin, "test", "internal/sketch/std/ui", "--law", lawName, "--seed", "1")
			run.Dir = root
			run.Env = append(os.Environ(), "ERGO_FALLBACK=on")
			out, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("law %s failed: %v\n%s", lawName, err, out)
			}
			if strings.Contains(string(out), "\nskip ") || strings.HasPrefix(string(out), "skip ") {
				t.Fatalf("law %s skipped:\n%s", lawName, out)
			}
		}}
	}
	lawledger.Run(t, laws)
}

func TestStdUIExploreOffersArePlayable(t *testing.T) { runStdUIExploreLaw(t, "OffersArePlayable") }
func TestStdUIExploreSettlesBeforeChecking(t *testing.T) {
	runStdUIExploreLaw(t, "ExploreSettlesBeforeChecking")
}
func TestStdUIExploreEndsIdleWhenExhausted(t *testing.T) {
	runStdUIExploreLaw(t, "ExploreEndsIdleWhenExhausted")
}
func TestStdUIExploreFindingsReplay(t *testing.T) { runStdUIExploreLaw(t, "FindingsReplay") }
func TestStdUIExploreSwarmNeverLeavesNone(t *testing.T) {
	runStdUIExploreLaw(t, "SwarmNeverLeavesNone")
}
func TestStdUIExploreCoveredPairsNeverOvercount(t *testing.T) {
	runStdUIExploreLaw(t, "CoveredPairsNeverOvercount")
}
