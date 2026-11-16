package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestStepsBuildWithTheModuleGo: a step's ergo builds with the go command
// of the toolchain go.mod selects (moduleGo) unless the caller chose
// another, so a step neither takes an older go from PATH, which the IR
// patch is not ported to (I19 full r2 B10), nor downloads ergo's managed
// release (internal/toolchain) from the network.
//
// Mutant: an ERGO_GO default of "system" (the old rule) puts PATH's go,
// not go.mod's release, in the step.
func TestStepsBuildWithTheModuleGo(t *testing.T) {
	if realTools["go"] == "" {
		t.Fatal("go is required")
	}
	saved := moduleGoCommand
	moduleGoCommand = realTools["go"]
	defer func() { moduleGoCommand = saved }()
	t.Setenv(ergoGo, "")
	os.Unsetenv(ergoGo)
	value, _ := environmentVariable(keyedEnvironment(), ergoGo)
	if filepath.Base(value) != "go" || !filepath.IsAbs(value) {
		t.Fatalf("with ERGO_GO unset a step gets ERGO_GO=%q, want the go command of go.mod's toolchain", value)
	}
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^toolchain (\S+)$`).FindSubmatch(mod)
	if line == nil {
		t.Fatal("go.mod has no toolchain line")
	}
	command := exec.Command(value, "env", "GOVERSION")
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	out, err := command.Output()
	if got := strings.TrimSpace(string(out)); err != nil || got != string(line[1]) {
		t.Errorf("ERGO_GO=%s is %q (%v), want go.mod's toolchain %s", value, got, err, line[1])
	}
	t.Setenv(ergoGo, "managed")
	if value, _ := environmentVariable(keyedEnvironment(), ergoGo); value != "managed" {
		t.Errorf("with ERGO_GO=managed a step gets ERGO_GO=%q, want the caller's", value)
	}
}
