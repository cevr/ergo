package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketchfront"
)

// TestCheckAdviceFollowsLedger drives `ergo check` in process over a
// package folder with a top-level rewrite law (LF6c): with no ergo.laws
// the law's obligation carries the advice; once ergo test's writer has
// licensed it, a second check of the unchanged sources gives none, also
// through the warm answer the first check recorded (its key holds the
// ledger it read). In process: the check and the writer run no Go build
// (a library's licence run is the evaluator's).
//
// Mutants: lf6c-check-advice-unwired (check prints no advice),
// lf6c-advice-ledger-unrecorded (the ledger is read past the warm
// recorder, so the warm answer keeps stale advice).
func TestCheckAdviceFollowsLedger(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("ERGO_LAWCACHE", "off")
	work := t.TempDir()
	folder := filepath.Join(work, "pkg")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(work, "ergo.mod"), "module example.com/m\n")
	writeFile(t, filepath.Join(folder, "a.ergo"), "func Id(s string) string { s }\n\nlaw IdOnce(s string) { Id(Id(s)) } == { Id(s) }\n")
	advice := func(step string) string {
		t.Helper()
		stdout, code := checkInProcess(t, folder)
		var env report.CheckEnvelope
		if err := json.Unmarshal(stdout, &env); err != nil || code != 0 || env.Evaluation == nil {
			t.Fatalf("%s: exit %d, %v\n%s", step, code, err, stdout)
		}
		for _, o := range env.Evaluation.Obligations {
			if o.Name == "law IdOnce" {
				return o.Advice
			}
		}
		t.Fatalf("%s: no obligation of law IdOnce\n%s", step, stdout)
		return ""
	}
	if got := advice("no ledger"); !strings.Contains(got, "has no entry for it") {
		t.Fatalf("no ledger: advice %q, want one that says it has no entry", got)
	}
	l, err := loadThrough(nil, folder, "", gobind.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := sketchfront.UpdateLedger(l.analysis, sketchfront.BuildRequest{Source: l.source.Admitted(), Ledger: ledgerPath(folder)}); err != nil || n != 1 {
		t.Fatalf("ergo test's writer: %d entries changed, %v; want 1", n, err)
	}
	if got := advice("licensed"); got != "" {
		t.Fatalf("licensed: advice %q, want none", got)
	}
}
