package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The mapping step keys on the directories of the evidence tests the
// mapping names, wherever they live, each once and sorted; a mapping that
// does not parse adds none.
func TestEvidenceDirectoriesFollowTheMapping(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(content string) {
		t.Helper()
		path := filepath.Join(root, "conformance", "effect-cases.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := evidenceDirectories(root); got != nil {
		t.Fatalf("no mapping gave %v", got)
	}
	write(`{"cases": [
		{"evidence": [{"file": "runtime/rt/scope_test.go"}, {"file": "cmd/ergo/a_test.go"}]},
		{"evidence": []},
		{"evidence": [{"file": "runtime/rt/other_test.go"}, {"file": "top_test.go"}]}
	]}`)
	want := []string{"cmd/ergo/", "runtime/rt/", "top_test.go"}
	if got := evidenceDirectories(root); !slices.Equal(got, want) {
		t.Fatalf("evidence directories %v, want %v", got, want)
	}
	write(`{"cases": [`)
	if got := evidenceDirectories(root); got != nil {
		t.Fatalf("a malformed mapping gave %v", got)
	}
}
