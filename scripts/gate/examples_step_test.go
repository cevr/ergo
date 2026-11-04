package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// builtKey keys a step with the given inputs over a fresh view of root, as
// a gate run does after the step's Needs have passed.
func builtKey(t *testing.T, root string, step *Step) (string, error) {
	t.Helper()
	g := &gate{root: root, cacheDir: t.TempDir()}
	if err := g.loadFiles(); err != nil {
		t.Fatal(err)
	}
	return g.key(step)
}

// A built file (Inputs.Built) is a file a step in Needs writes: the key holds
// its content and keyed permission bits, read when the dependent step is
// keyed, after its Needs passed. A missing built file is a key error, never a
// replay; a FIFO cannot be keyed, so the step runs without its receipt.
func TestBuiltFilesAreInputs(t *testing.T) {
	root := fixture(t)
	write(t, root, ".gitignore", "/runs/\n/out/\n")
	write(t, root, "out/built", "build one\n")
	built := filepath.Join(root, "out", "built")
	step := &Step{Name: "s", Argv: []string{"true"}, Inputs: &Inputs{Paths: []string{"src/"}, Built: []string{"out/built"}}}
	key := func() string {
		t.Helper()
		k, err := builtKey(t, root, step)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	first := key()
	if key() != first {
		t.Fatal("built keys are not deterministic")
	}
	// The same bytes written again: a rebuild that changed nothing.
	write(t, root, "out/built", "build one\n")
	if key() != first {
		t.Fatal("rewriting the built file with equal bytes changed the key")
	}
	write(t, root, "out/built", "build two\n")
	if key() == first {
		t.Fatal("changing the built file's bytes kept the key")
	}
	write(t, root, "out/built", "build one\n")
	if key() != first {
		t.Fatal("restoring the built file's bytes did not restore the key")
	}
	for _, mode := range []os.FileMode{0o744, 0o645} {
		if err := os.Chmod(built, mode); err != nil {
			t.Fatal(err)
		}
		if key() == first {
			t.Errorf("mode %o, a keyed permission change of the built file, kept the key", mode)
		}
	}
	// Without owner-read the file is unreadable: a key error.
	if err := os.Chmod(built, 0o244); err != nil {
		t.Fatal(err)
	}
	if _, err := builtKey(t, root, step); err == nil {
		t.Error("an unreadable built file keyed")
	}
	// Group and other read and write bits decide no step's access.
	if err := os.Chmod(built, 0o664); err != nil {
		t.Fatal(err)
	}
	if key() != first {
		t.Error("a group-write bit on the built file changed the key")
	}
	if err := os.Chmod(built, 0o644); err != nil {
		t.Fatal(err)
	}

	// A missing built file is a key error, not an unkeyable input.
	if err := os.Remove(built); err != nil {
		t.Fatal(err)
	}
	_, err := builtKey(t, root, step)
	var unkeyed unkeyedInput
	if err == nil || errors.As(err, &unkeyed) || !strings.Contains(err.Error(), "out/built") {
		t.Fatalf("a missing built file: want a key error naming it, got %v", err)
	}
	// A FIFO cannot be keyed: the step runs without its receipt.
	if err := syscall.Mkfifo(built, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := builtKey(t, root, step); !errors.As(err, &unkeyed) {
		t.Fatalf("a FIFO built file: want an unkeyed input, got %v", err)
	}
	cache := t.TempDir()
	fifoSteps := []*Step{{Name: "fifo", Argv: count("fifo"), Inputs: step.Inputs}}
	for run := 1; run <= 2; run++ {
		code, out := runGate(t, root, cache, fifoSteps)
		if code != 0 || !strings.Contains(out, "gate: fifo: not cached: ") || strings.Contains(out, "ok, cached") || runs(t, root, "fifo") != run {
			t.Fatalf("run %d over a FIFO built file: want the step run, not cached; exit %d, runs %d\n%s", run, code, runs(t, root, "fifo"), out)
		}
	}
	if err := os.Remove(built); err != nil {
		t.Fatal(err)
	}
	missing := []*Step{{Name: "missing", Argv: count("missing"), Inputs: step.Inputs}}
	if code, out := runGate(t, root, cache, missing); code != 1 || !strings.Contains(out, "gate: hash inputs: ") || runs(t, root, "missing") != 0 {
		t.Fatalf("a missing built file: want the step failed by its key, not run; exit %d, runs %d\n%s", code, runs(t, root, "missing"), out)
	}

	// A step in Needs writes the built file. The dependent step keys it as
	// that step left it in this run: it reruns when the build changed the
	// file and replays while the file is unchanged. A digest taken before
	// the build ran would key the previous build and replay a stale pass.
	write(t, root, "src/version", "A\n")
	write(t, root, "out/built", "A\n")
	steps := []*Step{
		{Name: "build", Argv: []string{"sh", "-c", "mkdir -p out && cat src/version > out/built"}},
		{Name: "use", Needs: []string{"build"}, Argv: count("use"), Inputs: &Inputs{Paths: []string{"src/input.txt"}, Built: []string{"out/built"}}},
	}
	expect := func(executions int, cached bool) {
		t.Helper()
		code, out := runGate(t, root, cache, steps)
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if got := runs(t, root, "use"); got != executions || strings.Contains(out, "== use: ok, cached") != cached {
			t.Fatalf("use ran %d times (want %d), cached %t (want %t)\n%s", got, executions, !cached, cached, out)
		}
	}
	expect(1, false)
	expect(1, true)
	write(t, root, "src/version", "B\n")
	expect(2, false)
	expect(2, true)
	write(t, root, "src/version", "A\n")
	expect(2, true)
}

// The examples step runs the example programs with the bin/ergo its Need
// built. Its key holds every file under examples/ on disk (Installed), the
// runner's sources, go.mod, the bytes and keyed bits of bin/ergo, and the
// go and sh fingerprints; nothing else.
func TestExamplesStepKeysOnItsInputs(t *testing.T) {
	_, steps := repositorySteps(t)
	at := slices.IndexFunc(steps, func(s *Step) bool { return s.Name == "examples (default)" })
	if at < 0 {
		t.Fatal("no step 'examples (default)'")
	}
	step := steps[at]
	if at == 0 || steps[at-1].Name != "build bin/ergo" {
		t.Fatalf("'examples (default)' is not declared right after 'build bin/ergo'")
	}
	if !slices.Equal(step.Needs, []string{"build bin/ergo"}) {
		t.Fatalf("needs %q, want [build bin/ergo]", step.Needs)
	}
	if !slices.Equal(step.Argv, []string{"go", "run", "./scripts/examples"}) {
		t.Fatalf("argv %q, want go run ./scripts/examples", step.Argv)
	}
	if step.Inputs == nil {
		t.Fatal("the step declares no inputs, so it never replays")
	}

	root := fixture(t)
	write(t, root, ".gitignore", "/runs/\n/bin/\n*.ignored\n")
	write(t, root, "go.mod", "module fixture\n\ngo 1.27\n")
	write(t, root, "scripts/examples/main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "examples/README.md", "# examples\n")
	write(t, root, "examples/hello/main.ergo", "func main() needs Console {\n}\n")
	write(t, root, "examples/hello/tags", "default\n")
	write(t, root, "examples/hello/cases/basic/expected.out", "hi\n")
	write(t, root, "bin/ergo", "compiler one\n")
	if err := os.Chmod(filepath.Join(root, "bin/ergo"), 0o755); err != nil {
		t.Fatal(err)
	}
	key := func() string {
		t.Helper()
		k, err := builtKey(t, root, step)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	base := key()
	if key() != base {
		t.Fatal("the step's key is not deterministic")
	}
	// Each change is made, keyed and undone; each must change the key, and
	// undoing it must restore the key.
	type change struct {
		what     string
		do, undo func()
	}
	// edit writes a file; undo restores its bytes, or removes created, the
	// first path the edit created (a file or a new folder).
	edit := func(what, name, content, created string) change {
		path := filepath.Join(root, name)
		before, err := os.ReadFile(path)
		return change{what, func() { write(t, root, name, content) }, func() {
			if created == "" {
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				write(t, root, name, string(before))
				return
			}
			if err := os.RemoveAll(filepath.Join(root, created)); err != nil {
				t.Fatal(err)
			}
		}}
	}
	move := func(a, b string) func() {
		return func() {
			if err := os.Rename(filepath.Join(root, a), filepath.Join(root, b)); err != nil {
				t.Fatal(err)
			}
		}
	}
	chmod := func(name string, mode os.FileMode) func() {
		return func() {
			if err := os.Chmod(filepath.Join(root, name), mode); err != nil {
				t.Fatal(err)
			}
		}
	}
	changes := []change{
		edit("one byte of a golden", "examples/hello/cases/basic/expected.out", "ho\n", ""),
		edit("one byte of main.ergo", "examples/hello/main.ergo", "func main() needs Console {\n }\n", ""),
		edit("the tags", "examples/hello/tags", "slow\n", ""),
		edit("a new case folder", "examples/hello/cases/second/expected.out", "hi\n", "examples/hello/cases/second"),
		{"a renamed case", move("examples/hello/cases/basic", "examples/hello/cases/first"), move("examples/hello/cases/first", "examples/hello/cases/basic")},
		edit("a new file that Git ignores under examples/", "examples/hello/notes.ignored", "ignored\n", "examples/hello/notes.ignored"),
		edit("scripts/examples/main.go", "scripts/examples/main.go", "package main\n\nfunc main() { println() }\n", ""),
		edit("go.mod", "go.mod", "module fixture\n\ngo 1.28\n", ""),
		edit("the bytes of bin/ergo", "bin/ergo", "compiler two\n", ""),
		{"the executable bit of bin/ergo", chmod("bin/ergo", 0o644), chmod("bin/ergo", 0o755)},
	}
	for _, c := range changes {
		c.do()
		if key() == base {
			t.Errorf("%s left the key unchanged", c.what)
		}
		c.undo()
		if key() != base {
			t.Fatalf("undoing %s did not restore the key", c.what)
		}
	}
	kept := []change{
		edit("scripts/examples/x_test.go", "scripts/examples/x_test.go", "package main\n", "scripts/examples/x_test.go"),
		edit("a file under scripts/examples/testdata/", "scripts/examples/testdata/fixture.txt", "data\n", "scripts/examples/testdata"),
		edit("docs/x.md", "docs/x.md", "# x\n", "docs/x.md"),
	}
	for _, c := range kept {
		c.do()
		if key() != base {
			t.Errorf("%s, outside the step's inputs, changed the key", c.what)
		}
		c.undo()
	}

	// The tools: a go that reports another toolchain, and an sh that is
	// another program, each first on PATH (as TestToolsAreInputs keys an
	// absent tool, and TestGoFingerprintKeysTheReportedToolchain a stub go).
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	goroot := t.TempDir()
	write(t, goroot, "src/runtime/probe.go", "package runtime\n")
	executable(t, bin, "go", "#!/bin/sh\necho '{\"GOROOT\": \""+goroot+"\", \"GOVERSION\": \"go1.0.0\"}'\n")
	stubbed := key()
	if stubbed == base {
		t.Error("another go on PATH left the key unchanged")
	}
	if key() != stubbed {
		t.Fatal("the key over a stub go is not deterministic")
	}
	rewriteInPlace(t, filepath.Join(goroot, "src/runtime/probe.go"), 0, "P")
	if key() == stubbed {
		t.Error("a byte of the go toolchain left the key unchanged")
	}
	if err := os.Remove(filepath.Join(bin, "go")); err != nil {
		t.Fatal(err)
	}
	if key() != base {
		t.Fatal("removing the stub go did not restore the key")
	}
	executable(t, bin, "sh", "#!/nonexistent/loop-probe-x\n")
	if key() == base {
		t.Error("another sh on PATH left the key unchanged")
	}
}
