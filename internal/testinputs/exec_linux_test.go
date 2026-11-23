//go:build linux && (amd64 || arm64)

package testinputs

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestExecRunsTargetObserved needs a real process: only the kernel can
// show that the shell and the command it starts inherit the filter.
func TestExecRunsTargetObserved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input")
	const contents = "the target read this\n"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := Exec(ExecSpec{
		Argv: []string{"sh", "-c", `cat "$1"`, "sh", file},
		Env:  []string{"PATH=/usr/bin:/bin"},
		Dir:  dir, Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil || result.Exit != 0 || result.Reason != "" {
		t.Fatalf("Exec: exit %d, incomplete %q, error %v; stderr: %s", result.Exit, result.Reason, err, &stderr)
	}
	if got := stdout.String(); got != contents {
		t.Fatalf("target stdout = %q, want %q", got, contents)
	}
	if !slices.ContainsFunc(result.Inputs, func(in Input) bool { return in.Path == file && in.Kind == SnapRead }) {
		t.Fatalf("target read of %s missing from %v", file, result.Inputs)
	}
}
