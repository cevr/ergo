package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs/testenv"
)

// GODEBUG=inittrace=1 leaves a Go program's bytes unchanged but adds lines to
// its standard error, which a test that asserts a program's output reads.
// Every gate step that runs Go programs therefore keys on the Go runtime
// environment: cached steps through their key, Go test steps through the tool
// digest their tests read.
func TestGoRuntimeEnvironmentKeysEveryStepThatRunsGoPrograms(t *testing.T) {
	_, steps := repositorySteps(t)
	// The steps' declared tools and environment are evaluated over a fixture:
	// the repository's own files, which the emission step rewrites every run,
	// would keep Go's test cache from ever replaying this test.
	g := &gate{root: fixture(t), cacheDir: t.TempDir()}
	if err := g.loadFiles(); err != nil {
		t.Fatal(err)
	}
	type fingerprint struct{ keys, digests map[string]string }
	observe := func() fingerprint {
		t.Helper()
		g.tools = nil
		observed := fingerprint{map[string]string{}, map[string]string{}}
		for _, step := range steps {
			if step.Inputs != nil && step.Argv[0] == "go" {
				declared := *step.Inputs
				declared.Paths, declared.Exclude, declared.Installed = []string{"src/"}, nil, nil
				declared.Built = nil
				key, err := g.key(&Step{Name: step.Name, Argv: step.Argv, Inputs: &declared})
				if err != nil {
					t.Fatal(err)
				}
				observed.keys[step.Name] = key
			}
			if len(step.ToolEnv) > 0 {
				observed.digests[step.Name] = g.toolDigest(step)
			}
		}
		return observed
	}
	for _, name := range goRuntimeEnvironment {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}
	unset := observe()
	if _, ok := unset.keys["foldkit corpus self-check"]; !ok {
		t.Fatal("no foldkit corpus self-check step")
	}
	if len(unset.keys) < 3 || len(unset.digests) < 2 {
		t.Fatalf("too few Go-running steps observed: %d keyed, %d with a tool digest", len(unset.keys), len(unset.digests))
	}
	for _, change := range []struct{ name, value string }{{"GODEBUG", "inittrace=1"}, {"GOTRACEBACK", "none"}, {"GOMAXPROCS", "1"}} {
		t.Setenv(change.name, change.value)
		changed := observe()
		for step, key := range unset.keys {
			if changed.keys[step] == key {
				t.Errorf("%s=%s left the key of %q unchanged", change.name, change.value, step)
			}
		}
		for step, digest := range unset.digests {
			if changed.digests[step] == digest {
				t.Errorf("%s=%s left the tool digest of %q unchanged", change.name, change.value, step)
			}
		}
		os.Unsetenv(change.name)
	}
}

// A variable the gate does not key must not reach a cached Go test: a program
// the test runs may read it, and if it was unset when the result was cached
// nothing names it, so exporting it later would replay a stale pass. A Go
// test step receives only testenv's variables, whose values the tool digest
// keys whether they are set or not, and the gate log names them. Every other
// step receives the same declared set (spec decision 3).
func TestGoTestStepsReceiveOnlyKeyedVariables(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatal("go is required")
	}
	root := fixture(t)
	write(t, root, "probe/go.mod", "module probe\n\ngo 1.24\n")
	write(t, root, "probe/probe_test.go", `package probe

import (
	"os"
	"testing"
)

func TestProbe(t *testing.T) {
	t.Logf("unknown=%s path=%t", os.Getenv("ERGO_GATE_UNKEYED_PROBE"), os.Getenv("PATH") != "")
}
`)
	t.Setenv("ERGO_GATE_UNKEYED_PROBE", "leaked")
	steps := []*Step{
		{Name: "go test probe", Argv: []string{"go", "test", "-C", "probe", "-count=1", "-v", "."}, ToolEnv: []string{"sh"}},
		{Name: "plain step", Argv: []string{"sh", "-c", `echo "plain=${ERGO_GATE_UNKEYED_PROBE-unset}"`}},
	}
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 || !strings.Contains(out, "unknown= path=true") {
		t.Fatalf("exit %d; the go test step saw an unkeyed variable, or no PATH:\n%s", code, out)
	}
	if !strings.Contains(out, "gate: steps receive only "+strings.Join(testenv.Names, " ")+"\n") {
		t.Fatalf("the gate log does not name the variables steps receive:\n%s", out)
	}
	if !strings.Contains(out, "plain=unset") {
		t.Fatalf("a step that runs no go test received a variable the gate does not declare:\n%s", out)
	}
	for _, entry := range goTestEnvironment() {
		name, _, _ := strings.Cut(entry, "=")
		if !slices.Contains(testenv.Names, name) {
			t.Errorf("go test steps receive %s", name)
		}
	}

	// Setting a variable a Go test step receives changes the tool digest,
	// though no result named it.
	step := &Step{Name: "go test probe", ToolEnv: []string{"sh"}}
	g := &gate{root: root}
	for _, name := range []string{"XDG_CONFIG_HOME", "GOEXPERIMENT", "TMPDIR"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
		unset := g.toolDigest(step)
		t.Setenv(name, "/nonexistent/loop-probe-x")
		if g.toolDigest(step) == unset {
			t.Errorf("setting %s left the tool digest unchanged", name)
		}
	}
}

// A submodule configured with ignore=all still makes the checkout unclean
// when its contents change.
func TestCheckoutIdentitySeesIgnoredSubmoduleChanges(t *testing.T) {
	t.Parallel()
	root := fixture(t)
	module := fixture(t)
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"--no-optional-locks", "-c", "user.name=gate", "-c", "user.email=gate@invalid", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	git("submodule", "add", "-q", module, "vendor/module")
	git("commit", "-qm", "submodule")
	git("config", "submodule.vendor/module.ignore", "all")
	clean := func() bool {
		t.Helper()
		identity, err := checkoutIdentity(root)
		if err != nil {
			t.Fatal(err)
		}
		return strings.HasSuffix(identity, "clean=true")
	}
	if !clean() {
		t.Fatal("a committed submodule reported a dirty checkout")
	}
	write(t, root, "vendor/module/src/input.txt", "modified\n")
	if clean() {
		t.Fatal("a modified submodule with ignore=all reported clean=true")
	}
}
