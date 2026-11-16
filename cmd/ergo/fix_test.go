package main

import (
	"os"
	"path/filepath"
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
