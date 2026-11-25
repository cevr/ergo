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

// TestFixLeavesTheFolderAsItWas runs ergo fix on a program in a folder of
// its own, its file private (0o600): the fix writes the clause, the file
// keeps its mode, and the folder holds the same names as before: the
// apply lock and journal are in the user's cache folder (SEM4c round 2).
// Mutants (cmd/ergo ledger): sem4c-fix-state-in-folder (the lock in the
// folder's .ergo), sem4c-exchange-drops-mode (Exchange without keepMode).
func TestFixLeavesTheFolderAsItWas(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "main.ergo")
	const src = "type Bad {}\n\nfunc leaf() int fails Bad {\n\t1\n}\n\nfunc helper() int {\n\ttry leaf()\n}\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		var out []string
		err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
			out = append(out, p)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	was := names()
	if err := command([]string{"fix", path, "--json"}); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "func helper() int fails Bad {") {
		t.Fatalf("fix wrote:\n%s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("after fix the file's mode is %v, want 0600", info.Mode().Perm())
	}
	if now := names(); strings.Join(now, "\n") != strings.Join(was, "\n") {
		t.Errorf("after fix the folder holds\n%s\nwant\n%s", strings.Join(now, "\n"), strings.Join(was, "\n"))
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

// TestFixRespellsAStdThatDoesNotCheck runs ergo fix --std on a std folder
// of one package whose borrow is written *T, the old spelling, so it does
// not check: the respell is syntactic and needs no check, so the borrow
// is written mut T (RESPELL-SYNTAX; before it, ergo fix --std refused a
// std that did not check and a script respelled std). The checked passes
// after it may still refuse this partial std: only the respell is read.
// Mutant (cmd/ergo ledger fix-respell-skips-std): the respell walks only
// the PATHs, so --std alone respells nothing.
func TestFixRespellsAStdThatDoesNotCheck(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	std := t.TempDir()
	path := filepath.Join(std, "ansi", "ansi.ergo")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "// Package ansi is std-shaped.\n\ntype Box { N int }\n\nfunc Bump(b *Box) {\n\tb.N = b.N + 1\n}\n"
	const want = "// Package ansi is std-shaped.\n\ntype Box { N int }\n\nfunc Bump(b mut Box) {\n\tb.N = b.N + 1\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := command([]string{"fix", "--std", std})
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("ergo fix --std (err %v) left:\n%s\nwant:\n%s", err, got, want)
	}
}

// TestFixLenCommand exercises `ergo fix --len` modes (--check, --dry-run,
// --json, write) and verifies that default `ergo fix --check` (without --len)
// leaves un-migrated len(e) files passing during the 4a->4b window.
func TestFixLenCommand(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	const src = "func count(xs []int, p mut []int) int {\n\tlen(xs) + len(*p)\n}\n"
	const want = "func count(xs []int, p mut []int) int {\n\txs.Len() + (*p).Len()\n}\n"
	path := filepath.Join(t.TempDir(), "count.ergo")
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
	// Default `ergo fix --check` (without --len) stays green on un-migrated len.
	if err := command([]string{"fix", "--check", "--json", path}); err != nil {
		t.Fatalf("default fix --check without --len: %v", err)
	}
	// `ergo fix --len --check` reports changes (exit 1) and writes nothing.
	if err, ok := command([]string{"fix", "--len", "--check", "--json", path}).(exitStatusError); !ok || err.code != 1 {
		t.Fatalf("fix --len --check: %v, want exit 1", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --len --check wrote:\n%s", got)
	}
	// `ergo fix --len --dry-run` exits 0 and writes nothing.
	if err := command([]string{"fix", "--len", "--dry-run", "--json", path}); err != nil {
		t.Fatalf("fix --len --dry-run: %v", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --len --dry-run wrote:\n%s", got)
	}
	// `ergo fix --len` writes the rewritten file.
	if err := command([]string{"fix", "--len", "--json", path}); err != nil {
		t.Fatalf("fix --len: %v", err)
	}
	if got := read(); got != want {
		t.Fatalf("fix --len wrote:\n%s\nwant:\n%s", got, want)
	}
	// `ergo fix --len --check` is now clean (exit 0).
	if err := command([]string{"fix", "--len", "--check", "--json", path}); err != nil {
		t.Fatalf("fix --len --check after fix: %v", err)
	}
}

// TestFixGlobalFuncsCommand exercises `ergo fix --global-funcs` modes (--check,
// --dry-run, --json, write) and verifies that default `ergo fix --check`
// (without --global-funcs) stays green during the 5a->5b window.
func TestFixGlobalFuncsCommand(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	const src = "func hashed(s string) uint64 {\n\thash(s)\n}\n"
	const want = "import maps\n\nfunc hashed(s string) uint64 {\n\tmaps.Hash(s)\n}\n"
	path := filepath.Join(t.TempDir(), "hashed.ergo")
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
	if err := command([]string{"fix", "--check", "--json", path}); err != nil {
		t.Fatalf("default fix --check without --global-funcs: %v", err)
	}
	if err, ok := command([]string{"fix", "--global-funcs", "--check", "--json", path}).(exitStatusError); !ok || err.code != 1 {
		t.Fatalf("fix --global-funcs --check: %v, want exit 1", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --global-funcs --check wrote:\n%s", got)
	}
	if err := command([]string{"fix", "--global-funcs", "--dry-run", "--json", path}); err != nil {
		t.Fatalf("fix --global-funcs --dry-run: %v", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --global-funcs --dry-run wrote:\n%s", got)
	}
	if err := command([]string{"fix", "--global-funcs", "--json", path}); err != nil {
		t.Fatalf("fix --global-funcs: %v", err)
	}
	if got := read(); got != want {
		t.Fatalf("fix --global-funcs wrote:\n%s\nwant:\n%s", got, want)
	}
	if err := command([]string{"fix", "--global-funcs", "--check", "--json", path}); err != nil {
		t.Fatalf("fix --global-funcs --check after fix: %v", err)
	}
}

func TestFixStdTypesCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.ergo")
	src := "func main() needs Console {\n\tConsole.Println(\"hi\")\n}\n"
	want := "import io\n\nfunc main() needs io.Console {\n\tio.Console.Println(\"hi\")\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if err := command([]string{"fix", "--check", "--json", path}); err != nil {
		t.Fatalf("default fix --check without --std-types: %v", err)
	}
	if err, ok := command([]string{"fix", "--global-funcs", "--std-types", path}).(usageError); !ok {
		t.Fatalf("fix --global-funcs --std-types: %v, want usageError", err)
	}
	if err, ok := command([]string{"fix", "--std-types", "--check", "--json", path}).(exitStatusError); !ok || err.code != 1 {
		t.Fatalf("fix --std-types --check: %v, want exit 1", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --std-types --check wrote:\n%s", got)
	}
	if err := command([]string{"fix", "--std-types", "--dry-run", "--json", path}); err != nil {
		t.Fatalf("fix --std-types --dry-run: %v", err)
	}
	if got := read(); got != src {
		t.Fatalf("fix --std-types --dry-run wrote:\n%s", got)
	}
	if err := command([]string{"fix", "--std-types", "--json", path}); err != nil {
		t.Fatalf("fix --std-types: %v", err)
	}
	if got := read(); got != want {
		t.Fatalf("fix --std-types wrote:\n%s\nwant:\n%s", got, want)
	}
	if err := command([]string{"fix", "--std-types", "--check", "--json", path}); err != nil {
		t.Fatalf("fix --std-types --check after fix: %v", err)
	}
}
