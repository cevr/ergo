package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkerFixtures is the folder of the checker's diagnostic fixtures, a
// folder per family.
var checkerFixtures = filepath.Join("..", "..", "internal", "sketch", "check", "testdata", "fixtures")

// TestCheckerFixtures runs `ergo check --want` over every family folder of
// the checker's fixtures (internal/sketch/check/testdata/fixtures/*): the
// checker's diagnostics tests, which were Go tables of `// want` rows
// until TP-DIAG moved them into files. In process; it builds nothing. It
// is the bridge that runs them in the gate and for the mutant ledgers
// until the gate runs the verb itself (ST-0); each family's README.md
// names the mutants it kills.
func TestCheckerFixtures(t *testing.T) {
	entries, err := os.ReadDir(checkerFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(checkerFixtures, e.Name()))
		}
	}
	if len(dirs) < 3 {
		t.Fatalf("%s holds %d family folders", checkerFixtures, len(dirs))
	}
	if err := checkCommand(append([]string{"--want"}, dirs...)); err != nil {
		t.Fatalf("ergo check --want over the checker's fixtures: %v", err)
	}
}

// TestCheckWantReadsTheMarkers: `ergo check --want` fails, exit 1, each
// copy of one fixture whose marker no longer matches its diagnostic: the
// code changed, the marker moved to another line, the marker removed, a
// quoted text that no message on the line holds; and it passes a marker
// whose text a message holds. Before TP-DIAG-WANT nothing read the
// comment, so a wrong code passed. In process; it builds nothing.
// Mutants: tpdiag-want-counts-only (the code changed, the marker moved),
// tpdiag-want-unmarked-passes (the marker removed), tpdiag-want-text-unread
// (the text).
func TestCheckWantReadsTheMarkers(t *testing.T) {
	fixtures := checkerFixtures
	// A marker's quoted text is a part of a message on its line.
	quoted := filepath.Join(t.TempDir(), "quoted.ergo")
	if err := os.WriteFile(quoted, []byte("func f() int { \"a\" } // want S108 \"takes only a string type\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkCommand([]string{"--want", quoted}); err != nil {
		t.Fatalf("ergo check --want with a marker's text: %v", err)
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
		"code changed":            strings.Replace(src, marker, ") // want S109", 1),
		"marker moved":            strings.Replace(strings.Replace(src, marker, ")", 1), "func twice(n int64) int64 { return n + n }", "func twice(n int64) int64 { return n + n } // want S108", 1),
		"marker removed":          strings.Replace(src, marker, ")", 1),
		"text not in the message": strings.Replace(src, marker, ") // want S108 \"no such words\"", 1),
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
