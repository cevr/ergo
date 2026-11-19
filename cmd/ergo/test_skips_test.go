package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// skipModule writes a module whose package prints law Runner.Runs's skip
// (its operation type has a row parameter, which a run does not bind) and,
// when listed, an expected-skips file naming that line. A law no case can
// feed is a check error, not a skip (LAW-UNFED), so it cannot serve here.
func skipModule(t *testing.T, listed bool) (dir string) {
	t.Helper()
	root := t.TempDir()
	smokeWrite(t, root, "ergo.mod", "module skips\n")
	smokeWrite(t, root, "lib/lib.ergo", "func twice(f func(int) int, n int) int { f(f(n)) }\n\ntype Runner[E] {\n\tRun() fails E,\n\tlaw Runs() { true },\n}\n\ntest Plain {\n\tassert twice(func(n int) int { n }, 3) == 3\n}\n")
	if listed {
		smokeWrite(t, root, expectedSkipsFile, "# a run binds no row parameter\n"+twiceSkip+"\n")
	}
	return filepath.Join(root, "lib")
}

const twiceSkip = "skip law Runner.Runs: its operation type Runner has the row parameter E, and a run binds only type parameters"

func unexpectedSkipExit(t *testing.T, err error) {
	t.Helper()
	var exit exitStatusError
	if !errors.As(err, &exit) || exit.code != 1 || !strings.Contains(exit.message, "1 unexpected skips") || !strings.Contains(exit.message, twiceSkip) {
		t.Fatalf("got %v, want exit 1 naming the skip", err)
	}
}

// TestUnexpectedSkipFails: a skip line that the module's expected-skips
// does not list fails a test run, live or replayed; a listed one and a
// --skip-law one pass (runtime.md, Tests 4; MODELCHECK T3c). In process:
// the judgment reads the run's stdout and the file. Kills
// test-skip-unlisted-passes and test-skip-replay-unjudged.
func TestUnexpectedSkipFails(t *testing.T) {
	for _, listed := range []bool{false, true} {
		dir := skipModule(t, listed)
		out := []byte("ok Plain\n" + twiceSkip + "\nskip law Other: --skip-law\nPASS (1 tests, 2 skipped)\n")
		err := skipsExit(out, dir)
		stored, _ := json.Marshal(stored{Stdout: out})
		replayed := replayTestResult(stored, dir, io.Discard, io.Discard)
		if listed {
			if err != nil || replayed != nil {
				t.Fatalf("a listed skip: live %v, replayed %v", err, replayed)
			}
			continue
		}
		unexpectedSkipExit(t, err)
		unexpectedSkipExit(t, replayed)
	}
}

// TestTestRunFailsAnUnexpectedSkip: ergo test of a package whose law no
// generator can run exits 1 naming the skip, and 0 once expected-skips
// lists it. Kills test-skip-live-unjudged. It builds Go because the skip
// line is the test binary's output.
func TestTestRunFailsAnUnexpectedSkip(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	t.Setenv("ERGO_DEV", "off")
	t.Setenv("ERGO_LAWCACHE", "off")
	for _, listed := range []bool{false, true} {
		err := testCommand([]string{skipModule(t, listed), "--count", "1"}) // no stored result replays
		if listed {
			if err != nil {
				t.Fatalf("a listed skip: %v", err)
			}
			continue
		}
		unexpectedSkipExit(t, err)
	}
}
