package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRLoopIdiomsMatchSource is the seeded property over the count loops
// DT4 writes as calls (FLIP F7 GP-LOOPS, GenerateLoops): fills and moves
// at every offset form, overlaps both ways and empty ranges print the same
// under both lowerings, and their functions have IR bodies. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the guarded copy and clear reach Go's OCOPY and
// OCLEAR only in the compiler that reads the IR.
//
// Mutants (testdata/mutants.json): f7g-loops-forward-unguarded (a move
// whose target starts inside its source is a copy too), f7g-loops-zero-
// always (every fill is a clear), f7g-collect-offset-dropped (a collect
// reads its part at the loop's bounds, not shifted by its offset).
func TestIRLoopIdiomsMatchSource(t *testing.T) {
	f7Property(t, "TestIRLoopIdiomsMatchSource", GenerateLoops, loopFuncs...)
}

// TestIRLoopIdiomsAreCalls checks the fact GP-LOOPS promises, in process on
// LowerIR's IR: each fill and move loop of GenerateLoops is written with
// Go's copy (and a zero fill with clear), as the text path's loopIdiomHead
// writes it, not as the element loop alone.
//
// Mutants: f7g-loops-idiom-dropped (the IR builder keeps every element
// loop), f7g-collect-dropped (it keeps every collect's loop).
func TestIRLoopIdiomsAreCalls(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateLoops(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		files, _, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, text)
		}
		var ir struct {
			Funcs []struct {
				Name string `json:"name"`
				Body any    `json:"body"`
			} `json:"funcs"`
		}
		for _, f := range files {
			if f.Path == lower.IRFileName {
				if err := json.Unmarshal(f.Data, &ir); err != nil {
					t.Fatal(err)
				}
			}
		}
		calls := map[string]map[string]bool{}
		for _, fn := range ir.Funcs {
			calls[fn.Name] = map[string]bool{}
			builtins(fn.Body, calls[fn.Name])
		}
		for _, f := range loopFuncs {
			if c, ok := calls[f]; !ok {
				t.Errorf("seed %d: %s has no IR body\n%s", seed, f, text)
			} else if !c["copy"] {
				t.Errorf("seed %d: %s's loop is no copy in its IR body (builtins %v)\n%s", seed, f, c, text)
			}
		}
		for _, f := range []string{"fillInts", "fillWords", "fillMarks"} {
			if !calls[f]["clear"] {
				t.Errorf("seed %d: %s's zero fill is no clear in its IR body\n%s", seed, f, text)
			}
		}
	}
}

// builtins adds to into the name of every "builtin" node under v.
func builtins(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if x["k"] == "builtin" {
			if n, ok := x["name"].(string); ok {
				into[n] = true
			}
		}
		for _, e := range x {
			builtins(e, into)
		}
	case []any:
		for _, e := range x {
			builtins(e, into)
		}
	}
}
