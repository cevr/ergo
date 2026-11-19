package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckWantReadsTheMarkers: `ergo check --want` passes the checker's
// own fixtures (internal/sketch/check/testdata/fixtures, which
// TestFixtures holds to the same rule), and fails, exit 1, each copy of
// one fixture whose marker no longer matches its diagnostic: the code
// changed, the marker moved to another line, the marker removed. Before
// TP-DIAG-WANT nothing read the comment, so a wrong code passed. In
// process; it builds nothing. Mutants: tpdiag-want-counts-only (the code
// changed, the marker moved) and tpdiag-want-unmarked-passes (the marker
// removed).
func TestCheckWantReadsTheMarkers(t *testing.T) {
	fixtures := filepath.Join("..", "..", "internal", "sketch", "check", "testdata", "fixtures")
	var dirs []string
	for _, d := range []string{"negative", "positive", "spec"} {
		dirs = append(dirs, filepath.Join(fixtures, d))
	}
	if err := checkCommand(append([]string{"--want"}, dirs...)); err != nil {
		t.Fatalf("ergo check --want over the checker's fixtures: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(fixtures, "negative", "argument-mismatch.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	const marker = ") // want S108"
	if strings.Count(src, marker) != 1 {
		t.Fatalf("argument-mismatch.ergo: want one %q", marker)
	}
	for name, wrong := range map[string]string{
		"code changed":   strings.Replace(src, marker, ") // want S109", 1),
		"marker moved":   strings.Replace(strings.Replace(src, marker, ")", 1), "func twice(n int64) int64 { return n + n }", "func twice(n int64) int64 { return n + n } // want S108", 1),
		"marker removed": strings.Replace(src, marker, ")", 1),
	} {
		file := filepath.Join(t.TempDir(), "fixture.ergo")
		if err := os.WriteFile(file, []byte(wrong), 0o644); err != nil {
			t.Fatal(err)
		}
		err := checkCommand([]string{"--want", file})
		var usage usageError
		if err == nil || errors.As(err, &usage) {
			t.Errorf("%s: ergo check --want gives %v; want the exit-1 failure that the fixture differs", name, err)
		}
	}
}
