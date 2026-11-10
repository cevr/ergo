package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGenCheckReadsThePackage catches r3b-spell (the generated header
// records how the command spelled the package, so --check from another
// folder reports current files stale). Smallest repro of the X-rpc1 bug:
// `ergo gen rpc --check examples/limiter/limits.Limiter` from the repo root
// failed on files generated as `limits.Limiter` from the example's folder.
// No other test runs --check from a second spelling; it runs in process.
func TestGenCheckReadsThePackage(t *testing.T) {
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
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	if err := genCommand([]string{"rpc", "api.API"}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, spelling := range []string{"mod/api.API", "./mod/api/.API", filepath.Join(mod, "api") + ".API"} {
		if err := genCommand([]string{"rpc", "--check", spelling}); err != nil {
			t.Errorf("--check %s: %v", spelling, err)
		}
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int, Note string }\ntype API { Read(key string) Item }\n")
	if err := genCommand([]string{"rpc", "--check", "mod/api.API"}); err == nil {
		t.Error("--check passed a contract that changed")
	}
}
