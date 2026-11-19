package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkRunsErgoWorkFromPath: `ergo work ARGS` runs the work found
// on PATH with ARGS as they are, and exits with its status; with no
// work on PATH it says how to build one. The work here is a
// shell script, so the test builds nothing. Mutants ws0-work-drops-args,
// ws0-work-status-lost (cmd/ergo/testdata/mutants.json).
func TestWorkRunsErgoWorkFromPath(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ERGO_WORK_SEEN\"\nexit 3\n"
	if err := os.WriteFile(filepath.Join(dir, workProgram), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("ERGO_WORK_SEEN", seen)
	err := command([]string{"work", "new", "-copy", "wf10/a b"})
	var status exitStatusError
	if !errors.As(err, &status) || status.code != 3 {
		t.Fatalf("ergo work: %v; want work's status 3", err)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n-copy\nwf10/a b\n" {
		t.Fatalf("work saw %q; want its arguments as given", got)
	}
	t.Setenv("PATH", t.TempDir())
	err = command([]string{"work", "list"})
	if !errors.As(err, &status) || status.code != 1 || !strings.Contains(status.message, "work is not on PATH") {
		t.Fatalf("ergo work with no work: %v", err)
	}
}
