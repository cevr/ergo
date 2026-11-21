package build_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// TestCoverCopyKeepsTheProgramsSignalHandler: a coverage copy's covdir
// flushes the counters on SIGINT and SIGTERM and then dies of the signal,
// which resets it. A module whose own Go registers a handler (a package of
// its own calls signal.Notify) keeps that handler: its copy's covdir makes
// no flush call. A module with none gets the flush. Mutants
// (testdata/mutants.json): gt7c-handler-ignored, gt7c-flush-never.
func TestCoverCopyKeepsTheProgramsSignalHandler(t *testing.T) {
	const mod = "module ergo.local/program\n\ngo 1.22\n"
	const main = "package main\n\nfunc main() {}\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		flush bool
	}{
		{"no handler", map[string]string{"go.mod": mod, "main.go": main}, true},
		{"a handler in its own package", map[string]string{"go.mod": mod, "main.go": main,
			"stop/stop.go": "package stop\n\nimport (\n\t\"os\"\n\t\"os/signal\"\n)\n\nfunc On(c chan os.Signal) { signal.Notify(c, os.Interrupt) }\n"}, false},
	} {
		module := t.TempDir()
		writeFiles(t, module, c.files)
		dir, _, err := build.CoverCopy(t.TempDir(), module)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		src, err := os.ReadFile(filepath.Join(dir, "covdir", "covdir.go"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(src), "\tflushOnSignal(dir)\n"); got != c.flush {
			t.Errorf("%s: covdir flushes on a signal: %v, want %v", c.name, got, c.flush)
		}
	}
}
