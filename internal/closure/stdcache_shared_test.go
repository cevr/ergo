package closure

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The gate's sweep steps share one std fill (GATE2 GT4). Each sweep step
// has a TMPDIR of its own, which the gate empties before the step runs; with
// the store under TMPDIR every step filled it again, about 370 CPU-s each
// (measured). With stdStoreVariable naming one directory, as the gate sets
// it for every sweep step, steps with different TMPDIRs, and sweeps that
// fill at the same time, run the std builds once in all: a counted guard
// (the fake go logs each build). Mutants: gt4-store-under-tmpdir (the
// variable is ignored), gt4-no-recheck (a sweep that waited for another's
// fill fills again).
func TestSweepStepsShareOneStdFill(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin, log, goroot := t.TempDir(), filepath.Join(t.TempDir(), "go.log"), t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = env ]; then printf '{\"GOVERSION\":\"go0.0-fake\",\"GOROOT\":\"%s\"}' '" + goroot + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"mkdir -p \"$GOCACHE/ab\" && printf x > \"$GOCACHE/ab/$GOOS$GOARCH-d\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(stdStoreVariable, filepath.Join(t.TempDir(), "shared"))
	builds := func() int {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}
	var stores []string
	// Sweeps that fill at once, all in the first step's TMPDIR.
	t.Setenv("TMPDIR", t.TempDir())
	var mu sync.Mutex
	var wg, arrived sync.WaitGroup
	// Every sweep finds the store empty before any fills: they race.
	arrived.Add(4)
	stdStoreFoundEmpty = func() { arrived.Done(); arrived.Wait() }
	defer func() { stdStoreFoundEmpty = nil }()
	for range 4 {
		wg.Go(func() {
			store, release, err := fillStdStore(root)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			mu.Lock()
			stores = append(stores, store)
			mu.Unlock()
		})
	}
	wg.Wait()
	// Later steps, each with a TMPDIR of its own.
	for range 3 {
		t.Setenv("TMPDIR", t.TempDir())
		store, release, err := fillStdStore(root)
		if err != nil {
			t.Fatal(err)
		}
		release()
		stores = append(stores, store)
	}
	if n := builds(); n != len(stdBuilds()) {
		t.Errorf("%d std builds for 7 sweeps in 4 TMPDIRs, want one fill (%d builds)", n, len(stdBuilds()))
	}
	for _, s := range stores {
		if s != stores[0] {
			t.Errorf("the sweeps used the stores %q, want one", stores)
			break
		}
	}
}
