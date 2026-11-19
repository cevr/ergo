package main

import (
	"os"
	"path/filepath"
	"strings"
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
	for _, kind := range []string{"api", "rpc"} {
		if err := genCommand([]string{kind, "api.API"}); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	for _, spelling := range []string{"mod/api.API", "./mod/api/.API", filepath.Join(mod, "api") + ".API"} {
		for _, kind := range []string{"api", "rpc"} {
			if err := genCommand([]string{kind, "--check", spelling}); err != nil {
				t.Errorf("%s --check %s: %v", kind, spelling, err)
			}
		}
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int, Note string }\ntype API { Read(key string) Item }\n")
	if err := genCommand([]string{"rpc", "--check", "mod/api.API"}); err == nil {
		t.Error("--check passed a contract that changed")
	}
}

// TestGenCliCheckIsRedOnAStaleFile catches ap1-cli-check-blind (ergo gen
// cli --check passes a cli.ergo the contract no longer generates),
// ap1-cli-stale-rpc (--check compares no file's text, so a changed
// contract passes), af-register-transport-skips-hub (ergo gen cli writes
// no hub, so the cli package names a missing one; and --check passes a
// hub edited by hand) and ap1-cli-own-request (the
// cli package is written over no hub, so a caller holds two types for one
// call). It runs in process: the question is which files the command
// compares, not what they do.
func TestGenCliCheckIsRedOnAStaleFile(t *testing.T) {
	mod := t.TempDir()
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
	// No hub yet: ergo gen cli writes the hub it is written over too, so
	// no order of gen commands leaves it naming a missing hub.
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mod, "api", "apiapi", "api.ergo")); err != nil {
		t.Errorf("ergo gen cli wrote no hub: %v", err)
	}
	if err := genCommand([]string{"cli", "--check", "api.API"}); err != nil {
		t.Fatalf("--check of a current cli package: %v", err)
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string, all bool) Item }\n")
	if err := genCommand([]string{"cli", "--check", "api.API"}); err == nil {
		t.Error("--check passed a cli package whose contract changed")
	}
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	// A hub edited by hand: the cli package's --check checks the hub too.
	hub := filepath.Join(mod, "api", "apiapi", "api.ergo")
	text, err := os.ReadFile(hub)
	if err != nil {
		t.Fatal(err)
	}
	write(hub, string(text)+"\n// edited\n")
	if err := genCommand([]string{"cli", "--check", "api.API"}); err == nil || !strings.Contains(err.Error(), "apiapi") {
		t.Errorf("--check of a cli package over a hub edited by hand: %v", err)
	}
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(filepath.Join(mod, "api", "apicli", "cli.ergo"))
	if err != nil || !strings.Contains(string(cli), "type Request = apiapi.Request") {
		t.Errorf("the cli package does not name the hub's Request: %v\n%s", err, cli)
	}
}
