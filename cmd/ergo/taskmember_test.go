package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTaskGenRegeneratesMemberGen is a member's `ergo gen` packages as a
// derived task (docs/spec/build.md, Generators): with nothing changed
// `ergo task gen --check` holds and every node is a hit; an edit of the
// contract makes `--check` fail naming the hub and write nothing;
// `ergo task gen` then writes what `ergo gen` writes, every kind (the rpc
// package passes `ergo gen rpc --check`), and a hand edit of a generated
// file is stale again. It catches task-member-inputs-skip-member (the
// node's inputs leave out the member's files: the contract edit is a
// hit) and task-member-kinds-hub-only (only the hub is written again: rpc
// --check fails), cmd/ergo/testdata/mutants.json. It runs in process.
func TestTaskGenRegeneratesMemberGen(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	t.Setenv("ERGO_BUDGET", "")
	root := t.TempDir()
	mod := filepath.Join(root, "mod")
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mod, "ergo.mod"), "module mod\n")
	contract := filepath.Join(mod, "api", "api.ergo")
	write(contract, "type Item { Count int }\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	if err := genCommand([]string{"rpc", "api.API"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := taskMain(context.Background(), args, &out)
		return out.String(), err
	}
	if out, err := run("gen"); err != nil {
		t.Fatalf("first gen: %v\n%s", err, out)
	}
	if out, err := run("gen", "--check"); err != nil || !strings.Contains(out, "mod:gen: hit") {
		t.Fatalf("check with nothing changed: %v\n%s", err, out)
	}
	write(contract, "type Item { Count int }\ntype API {\n\tRead(key string) Item,\n\tWrite(key string, n int) Item,\n}\n")
	hub := filepath.Join(mod, "api", "apiapi")
	before := readTree(t, filepath.Join(mod, "api"))
	if out, err := run("gen", "--check"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("check after a contract edit: %v\n%s", err, out)
	}
	if after := readTree(t, filepath.Join(mod, "api")); !mapsEqual(before, after) {
		t.Fatal("gen --check wrote the tree")
	}
	if out, err := run("gen"); err != nil {
		t.Fatalf("gen after a contract edit: %v\n%s", err, out)
	}
	t.Chdir(mod)
	for _, kind := range []string{"api", "rpc"} {
		if err := genCommand([]string{kind, "--check", "api.API"}); err != nil {
			t.Fatalf("ergo gen %s --check after ergo task gen: %v", kind, err)
		}
	}
	t.Chdir(root)
	entries, err := os.ReadDir(hub)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the hub %s: %v", hub, err)
	}
	write(filepath.Join(hub, entries[0].Name()), "// a hand edit\n")
	if out, err := run("gen", "--check"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("check after a hand edit of the hub: %v\n%s", err, out)
	}
}

// readTree is every file below dir by its path relative to dir.
func readTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
