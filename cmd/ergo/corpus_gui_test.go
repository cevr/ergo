package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// The helpers of the GUI corpus programs' agreement properties
// (examples/stars-gui, editor-gui, draw-gui, dash-gui): each links its
// program with `ergo build`, links its hand Go control from
// testdata/<program>/control with the emitted module's go line, and runs
// both, each under proclimit's kernel memory bound, waiting for the exit.

// buildCorpusProgram links examples/<name> with `ergo build` and returns
// the binary and the emitted module's directory.
func buildCorpusProgram(t *testing.T, name string) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/"+name), "examples", name, "cmd", name)
	bin = filepath.Join(t.TempDir(), name)
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildCorpusControl links testdata/<name>/control in a module with the go
// line of the program's emitted module in work, so one toolchain compiles
// both.
func buildCorpusControl(t *testing.T, work, name string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("testdata", name, "control", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module control\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, "control")
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of the %s control: %v\n%s", name, err, out.Bytes())
	}
	return bin
}

// runCorpus runs bin in dir with args and stdin, and is its stdout,
// stderr and exit status as one text.
func runCorpus(t *testing.T, bin, dir string, args []string, stdin string) string {
	t.Helper()
	command := exec.Command(bin, args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", bin, err)
		}
		code = exit.ExitCode()
	}
	return corpusOutput(stdout.String(), stderr.String(), code)
}

// corpusOutput is a run's output as runCorpus writes it.
func corpusOutput(stdout, stderr string, code int) string {
	return fmt.Sprintf("stdout:\n%s\nstderr:\n%s\nstatus %d\n", stdout, stderr, code)
}

// firstDiff is the first line where got and want differ, with a few lines
// of context, so a failing seed shows the step that went wrong.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl || i >= len(g) || i >= len(w) {
			from := max(i-3, 0)
			return fmt.Sprintf("line %d differs\nwant (context):\n%s\ngot (context):\n%s",
				i+1, strings.Join(w[from:min(i+2, len(w))], "\n"), strings.Join(g[from:min(i+2, len(g))], "\n"))
		}
	}
	return "no difference"
}
