package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// declaredInputs are the module paths, relative to the module root, that
// this package's tests may read, besides the authored examples
// (scripts/authored_examples.txt names them; the step tests read them). Go's
// test cache keys the package's result on every module path the tests read
// (testinputs records the reads of the processes they start too), so a path
// here is an input: changing it reruns the package, the output-storage
// property included. A path ending in "/" stands for the directory and
// everything below it. The list is the module paths of the test log of a
// full run (open, stat and chdir lines): the package's own files and the
// testinputs packages it imports; go.mod, and the go.mod, go.work and
// vendor lookups of the go build a test runs; the cmd/ergo test files the
// duration tests read, with the listing of cmd/ergo; and the files the step
// tests read. A lookup (stat or chdir) of a directory on the way to a
// declared path is declared too; a listing of it is not. Any other read,
// such as a listing of the module root, would make the result depend on
// directories the gate itself rewrites (dist/), and the package would never
// replay; checkDeclaredInputs refuses such a run.
var declaredInputs = []string{
	"scripts/gate/",
	"internal/testinputs/",
	"internal/go.mod",
	"go.mod",
	"go.work",
	"vendor",
	"scripts/go.mod",
	"scripts/go.work",
	"cmd/ergo/",
	"conformance/effect-cases.json",
	"scripts/authored_examples.txt",
}

// undeclaredInputs returns the module paths, relative to root, that a test
// log reads outside inputs (as declaredInputs), in log order without
// repeats. It reads the log as cmd/go does
// (cmd/go/internal/test.computeTestInputsID): an operation, a space and a
// name; relative names are joined to the working directory, which starts at
// dir and follows chdir.
func undeclaredInputs(log, root, dir string, inputs []string) []string {
	var found []string
	for _, line := range strings.Split(log, "\n") {
		op, name, ok := strings.Cut(line, " ")
		if !ok || (op != "open" && op != "stat" && op != "chdir") {
			continue
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(dir, name)
		}
		if op == "chdir" {
			dir = name
		}
		relative, err := filepath.Rel(root, name)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		relative = filepath.ToSlash(relative)
		if !declared(relative, op != "open", inputs) && !slices.Contains(found, relative) {
			found = append(found, relative)
		}
	}
	return found
}

// declared reports whether a module path is among inputs, or, for a lookup,
// a directory on the way to one of them.
func declared(relative string, lookup bool, inputs []string) bool {
	for _, input := range inputs {
		if directory, ok := strings.CutSuffix(input, "/"); ok {
			if relative == directory || strings.HasPrefix(relative, input) {
				return true
			}
		} else if relative == input {
			return true
		}
		if lookup && (relative == "." || strings.HasPrefix(input, relative+"/")) {
			return true
		}
	}
	return false
}

// checkDeclaredInputs turns code into a failure if log, the complete test
// log of the tests run in dir (finishTestLog), reads a module path outside
// declaredInputs.
func checkDeclaredInputs(code int, log, dir string) int {
	root, err := moduleRootAbove(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate tests: check the declared inputs:", err)
		return 1
	}
	examples, err := authoredExamples(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate tests: check the declared inputs:", err)
		return 1
	}
	found := undeclaredInputs(log, root, dir, append(slices.Clone(declaredInputs), examples...))
	if len(found) == 0 {
		return code
	}
	const shown = 20
	fmt.Fprintf(os.Stderr, "gate tests: the tests read %d module paths outside declaredInputs (inputs_test.go), so Go's test cache keys this package on them:\n", len(found))
	for _, path := range found[:min(len(found), shown)] {
		fmt.Fprintln(os.Stderr, "  "+path)
	}
	if len(found) > shown {
		fmt.Fprintf(os.Stderr, "  and %d more\n", len(found)-shown)
	}
	return 1
}

// moduleRootAbove returns the directory holding the go.mod at or above dir.
func moduleRootAbove(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// TestUndeclaredInputs checks the reading of a test log: a listing of the
// module root or of a directory on the way to an input, a file read by a
// relative name after a chdir, and a lookup outside the declared inputs are
// found once each; declared paths, lookups on the way to them and paths
// outside the module are not.
func TestUndeclaredInputs(t *testing.T) {
	root := filepath.FromSlash("/m")
	dir := filepath.Join(root, "scripts", "gate")
	inputs := []string{"scripts/gate/", "internal/testinputs/", "go.mod", "cmd/ergo/a_test.go"}
	log := strings.Join([]string{
		"# test log",
		"getenv HOME",
		"open " + filepath.Join(root, "scripts", "gate", "durations.json"),
		"stat " + root,
		"open " + root,
		"stat " + filepath.Join(root, "go.mod"),
		"open steps.go",
		"stat " + filepath.Join(root, "internal", "testinputs"),
		"stat " + filepath.Join(root, "scripts"),
		"open " + filepath.Join(root, "scripts"),
		"stat " + filepath.FromSlash("/usr/bin/git"),
		"chdir " + filepath.Join(root, "cmd"),
		"open ergo/a_test.go",
		"open ergo/main.go",
		"open " + root,
		"stat " + filepath.Join(root, "scripts", "gate.sh"),
		"stat " + filepath.Join(root, "dist"),
		"",
	}, "\n")
	got := undeclaredInputs(log, root, dir, inputs)
	want := []string{".", "scripts", "cmd/ergo/main.go", "scripts/gate.sh", "dist"}
	if !slices.Equal(got, want) {
		t.Errorf("undeclared inputs %q, want %q", got, want)
	}
}
