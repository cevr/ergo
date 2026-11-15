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

// TestGenCliCheckIsRedOnAStaleFile catches ap1-cli-check-blind (ergo gen
// cli --check passes a cli.ergo the contract no longer generates),
// ap1-cli-stale-rpc (the cli package names the Request of an rpc package
// that is stale, so the two packages disagree) and ap1-cli-own-request (a
// cli package beside an rpc package declares a Request of its own, so a
// caller holds two types for one call). It runs in process: the
// question is which files the command compares, not what they do.
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
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if err := genCommand([]string{"cli", "--check", "api.API"}); err != nil {
		t.Fatalf("--check of a current cli package: %v", err)
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string, all bool) Item }\n")
	if err := genCommand([]string{"cli", "--check", "api.API"}); err == nil {
		t.Error("--check passed a cli package whose contract changed")
	}
	// An rpc package beside it: the cli package names its Request, so a
	// stale one is refused, and a current one is used.
	if err := genCommand([]string{"rpc", "api.API"}); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(mod, "api", "api.ergo"), "type Item { Count int }\ntype API { Read(key string) Item }\n")
	if err := genCommand([]string{"cli", "api.API"}); err == nil || !strings.Contains(err.Error(), "run ergo gen rpc first") {
		t.Errorf("ergo gen cli over a stale rpc package: %v", err)
	}
	if err := genCommand([]string{"rpc", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if err := genCommand([]string{"cli", "api.API"}); err != nil {
		t.Fatal(err)
	}
	cli, err := os.ReadFile(filepath.Join(mod, "api", "apicli", "cli.ergo"))
	if err != nil || !strings.Contains(string(cli), "type Request = apirpc.Request") {
		t.Errorf("the cli package does not name the rpc package's Request: %v\n%s", err, cli)
	}
}
