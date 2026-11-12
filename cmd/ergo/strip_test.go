package main

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// TestBuildStripsUnlessDebug: `ergo build` links stripped, `--debug` keeps
// the symbols, and `ergo run` never strips (binsize BS4). It reads the Go
// build action in process; the sizes command of the full tier measures
// the files. Mutants bs4-default-unstripped and bs4-debug-stripped.
func TestBuildStripsUnlessDebug(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	for _, c := range []struct {
		command string
		args    []string
		strip   bool
	}{
		{"build", []string{"main.ergo"}, true},
		{"build", []string{"main.ergo", "--debug"}, false},
		{"run", []string{"main.ergo"}, false},
	} {
		opts, err := parseFileOptions(c.command, c.args)
		if err != nil {
			t.Fatalf("%s %v: %v", c.command, c.args, err)
		}
		command := toolchain(opts).Command(build.Module{Dir: "/nonexistent/loop-probe-module"}, "/nonexistent/loop-probe-out")
		stripped := slices.ContainsFunc(command.Env, func(kv string) bool {
			return strings.HasPrefix(kv, "GOFLAGS=") && strings.Contains(kv, "-ldflags=-s -w")
		})
		if stripped != c.strip {
			t.Errorf("ergo %s %v: the link strips = %v, want %v (env GOFLAGS of %v)", c.command, c.args, stripped, c.strip, command.Args)
		}
	}
	if _, err := parseFileOptions("run", []string{"--debug", "main.ergo"}); err == nil || !strings.Contains(err.Error(), "--debug is only supported by build") {
		t.Errorf("ergo run --debug: %v, want the refusal", err)
	}
}
