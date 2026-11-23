package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/mutants/suitename"
)

// TestLedgerRefusesAmbiguousSuite catches mut-ambiguous-alias-taken: a
// legacy spelling must not silently select either suite when both exist.
func TestLedgerRefusesAmbiguousSuite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "check/check_test.go"), "package check\n")
	path := filepath.Join(root, "p/testdata/mutants.json")
	ledger := `{"mutants":[{"name":"x","file":"p.go","from":"a","to":"b","killers":["SUITE:TestX"]}]}`
	for _, suite := range []string{"./check", "internal/sketch/check", "runtime", "lint", "examples", "command"} {
		writeFile(t, path, strings.ReplaceAll(ledger, "SUITE", suite))
		l, err := readLedger(root, path, nil)
		if err != nil {
			t.Fatalf("%s: %v", suite, err)
		}
		want := strings.TrimPrefix(suite, "./") + ":TestX"
		if got := killerIDs(l.Mutants[0]); len(got) != 1 || got[0] != want {
			t.Errorf("%s: got %v, want %s", suite, got, want)
		}
	}
	writeFile(t, path, strings.ReplaceAll(ledger, "SUITE", "check"))
	_, err := readLedger(root, path, nil)
	if err == nil || !strings.Contains(err.Error(), "./check:TestX") ||
		!strings.Contains(err.Error(), "internal/sketch/check:TestX") {
		t.Fatalf("want both candidates for check:TestX, got %v", err)
	}
}

// TestCanonicalKillerMigration catches mut-migration-skips-anchors: the
// rewrite changes anchor IDs, not identical-looking source or command text.
func TestCanonicalKillerMigration(t *testing.T) {
	root := t.TempDir()
	src := []byte(`{"commands":{"run":["check:TestX"]},"mutants":[
  {"name":"x","killers":["std:TestX (note)"],"from":"lower:TestY",
   "anchor":{"tests":["lower:TestY","command:run"]}}
]}`)
	want := []byte(`{"commands":{"run":["check:TestX"]},"mutants":[
  {"name":"x","killers":["internal/sketch/stdlib:TestX (note)"],"from":"lower:TestY",
   "anchor":{"tests":["internal/sketch/lower:TestY","command:run"]}}
]}`)
	names := suitename.At(root)
	got, n, err := rewriteKillerIDs(names, src)
	if err != nil || n != 2 || !bytes.Equal(got, want) {
		t.Fatalf("rewrite %d IDs, err %v:\ngot  %s\nwant %s", n, err, got, want)
	}
	again, n, err := rewriteKillerIDs(names, got)
	if err != nil || n != 0 || !bytes.Equal(again, want) {
		t.Fatalf("second rewrite %d IDs, err %v:\n%s", n, err, again)
	}
}
