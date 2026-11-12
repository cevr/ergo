package lower

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// lowerContextProgram checks the packages of p in order, each against the
// ones before it, and returns its diagnostics, or else the build and test
// lowerings of its root, file by file in the order they come.
func lowerContextProgram(p sketchtest.ContextProgram) string {
	var b strings.Builder
	imports := cxImporter{}
	for i, q := range p.Packages {
		f, err := syntax.Parse(q.Text)
		if err != nil {
			return fmt.Sprintf("%s: %v", q.File, err)
		}
		path := q.Path
		if i == len(p.Packages)-1 {
			path = "main"
		}
		pkg, ds := check.Check(path, []*syntax.File{f}, imports)
		if len(ds) > 0 {
			return fmt.Sprintf("%s: %v", q.File, ds)
		}
		u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: "/ergo-test/gen/" + q.File, Text: q.Text}}}
		if i < len(p.Packages)-1 {
			imports[q.Path] = u
			continue
		}
		u.Imports = imports.units()
		for _, lower := range []struct {
			name string
			run  func(Unit) ([]File, error)
		}{{"Lower", Lower}, {"LowerTests", LowerTests}} {
			files, err := lower.run(u)
			fmt.Fprintf(&b, "== %s %v\n", lower.name, err)
			for _, f := range files {
				fmt.Fprintf(&b, "-- %s\n%s\n", f.Path, f.Data)
			}
		}
	}
	return b.String()
}

// TestLoweringIsAFunctionOfInput: checking and lowering a generated program
// of packages whose contexts embed imported contexts beside local providers
// gives the same Go every time in one process (wf10 NONDET1; the examples
// and other processes: sketchfront's TestExamplesOutputIsAFunctionOfInput).
// Mutant check/nondet1-entry-order: the checker orders a with's
// acquisitions by seq alone, which ties across packages, so an unstable sort
// of a map's keys installs the providers, and emits their adapters, in
// either order. ERGO_DETERMINISM_SEED=<n> replays one seed.
func TestLoweringIsAFunctionOfInput(t *testing.T) {
	seeds := make([]int64, 16)
	for i := range seeds {
		seeds[i] = int64(9100 + i)
	}
	if s := os.Getenv("ERGO_DETERMINISM_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{n}
	}
	for _, seed := range seeds {
		p := sketchtest.GenerateContextProgram(seed)
		first := lowerContextProgram(p)
		if !strings.HasPrefix(first, "== Lower <nil>\n") {
			t.Errorf("seed %d does not lower:\n%s\n%s", seed, firstLines(first, 10), p)
			continue
		}
		for run := 1; run < 4; run++ {
			if got := lowerContextProgram(p); got != first {
				t.Errorf("seed %d: run %d differs from run 0 (replay: ERGO_DETERMINISM_SEED=%d go test ./internal/sketch/lower -run TestLoweringIsAFunctionOfInput):\n%s\n%s", seed, run, seed, lineDiff(first, got), p)
				break
			}
		}
	}
}

// lineDiff shows the lines around the first line where want and got
// differ.
func lineDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] != gl[i] {
			lo := max(0, i-3)
			return fmt.Sprintf("line %d\nwant:\n%s\ngot:\n%s", i+1, strings.Join(wl[lo:min(i+4, len(wl))], "\n"), strings.Join(gl[lo:min(i+4, len(gl))], "\n"))
		}
	}
	return fmt.Sprintf("one is a prefix of the other: %d and %d lines", len(wl), len(gl))
}
