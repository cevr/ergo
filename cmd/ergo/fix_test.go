package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixCheckWritesNothing runs ergo fix in process on a file whose
// helper misses its fails clause: --check exits 1 and leaves the file,
// fix writes the clause, and a second --check exits 0. The property in
// internal/sketch/fix covers what is written; this covers the command's
// modes. Mutant: write when --check (ledger ex1-check-writes).
func TestFixCheckWritesNothing(t *testing.T) {
	const src = "type Bad {}\n\nfunc leaf() int fails Bad {\n\t1\n}\n\nfunc helper() int {\n\ttry leaf()\n}\n\nfunc main() {}\n"
	const want = "type Bad {}\n\nfunc leaf() int fails Bad {\n\t1\n}\n\nfunc helper() int fails Bad {\n\ttry leaf()\n}\n\nfunc main() {}\n"
	path := filepath.Join(t.TempDir(), "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if err, ok := command([]string{"fix", path, "--check", "--json"}).(exitStatusError); !ok || err.code != 1 {
		t.Fatalf("fix --check: %v, want exit 1", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --check wrote:\n%s", got)
	}
	if err := command([]string{"fix", path, "--json"}); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if got := read(); got != want {
		t.Fatalf("fix wrote:\n%s\nwant:\n%s", got, want)
	}
	if err := command([]string{"fix", path, "--check", "--json"}); err != nil {
		t.Fatalf("fix --check after fix: %v", err)
	}
}

// TestFixWriteRefusesAChangedFile calls ergo fix's write (code.Rewrite,
// SEM4c) with the text a fix read, after another writer changed one of
// the two files: the write is refused stale and neither file changes.
// Mutant (cmd/ergo ledger sem4c-fix-write-reads-disk): a write that takes
// the text on disk as the text the fix read writes over the change.
func TestFixWriteRefusesAChangedFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.ergo"), filepath.Join(dir, "p", "b.ergo")
	if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("read\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(b, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{a: "read\n", b: "read\n"}
	after := map[string]string{a: "fixed\n", b: "fixed\n"}
	err := writeFixed(before, after)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("writeFixed over a changed file = %v, want a stale refusal", err)
	}
	for p, want := range map[string]string{a: "read\n", b: "changed\n"} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("after a refused write %s holds %q, want %q", p, got, want)
		}
	}
}
