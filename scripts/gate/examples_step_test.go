package main

import (
	"errors"
	"os"
	"path/filepath"
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
	// file and replays while the file is unchanged.
	write(t, root, "src/version", "A\n")
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
