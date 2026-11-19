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
// It is ERGO_GO=system with that go first on PATH, so a test's go stub put
// before it on PATH still runs (I19 full r3).
//
// Mutant: no default (tc-gate-managed-go) leaves PATH's go, or ergo's
// managed download, in the step.
func TestStepsBuildWithTheModuleGo(t *testing.T) {
	if realTools["go"] == "" {
		t.Fatal("go is required")
	}
	saved := moduleGoCommand
	moduleGoCommand = realTools["go"]
	defer func() { moduleGoCommand = saved }()
	t.Setenv(ergoGo, "")
	os.Unsetenv(ergoGo)
	environment := keyedEnvironment()
	if value, _ := environmentVariable(environment, ergoGo); value != "system" {
		t.Fatalf("with ERGO_GO unset a step gets ERGO_GO=%q, want system", value)
	}
	path, _ := environmentVariable(environment, "PATH")
	value := filepath.Join(filepath.SplitList(path)[0], "go")
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
		t.Errorf("the go first on a step's PATH, %s, is %q (%v), want go.mod's toolchain %s", value, got, err, line[1])
	}
	t.Setenv(ergoGo, "managed")
	if value, _ := environmentVariable(keyedEnvironment(), ergoGo); value != "managed" {
		t.Errorf("with ERGO_GO=managed a step gets ERGO_GO=%q, want the caller's", value)
	}
}
