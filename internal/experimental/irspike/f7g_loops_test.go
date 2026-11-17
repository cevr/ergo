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
//
// GP-FOLD's folds run in it too. Mutant f7g-fold-source-empty: a fused
// fold ranges over the empty slice, not the source's part.
func TestIRLoopIdiomsMatchSource(t *testing.T) {
	funcs := append(append(append([]string{}, loopFuncs...), foldFuncs.fused...), foldFuncs.kept...)
	funcs = append(append(funcs, resliceFuncs.taken...), resliceFuncs.kept...)
	f7Property(t, "TestIRLoopIdiomsMatchSource", GenerateLoops, funcs...)
}

// TestIRCollectFoldsFuse checks the fact GP-FOLD promises, in process on
// LowerIR's IR: a collect whose slice the next loop only folds builds no
// slice when its guard holds (no growth: the fold ranges over the source's
// part),
// and one whose slice is read after, whose fold writes the source, or
// whose slice starts with elements still builds it (slices.Grow).
//
// Mutants: f7g-fold-dropped (no collect is fused), f7g-fold-used-after
// (a slice read after the fold is fused), f7g-fold-body-unchecked (a fold
// that writes the source is fused), f7g-fold-prefix (a slice that starts
// with elements is fused).
func TestIRCollectFoldsFuse(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		grows, text := collectGrowth(t, seed)
		for _, f := range foldFuncs.fused {
			if g, ok := grows[f]; !ok || g {
				t.Errorf("seed %d: %s builds its slice (IR body %v), want its fold over the source's part\n%s", seed, f, ok, text)
			}
		}
		for _, f := range foldFuncs.kept {
			if !grows[f] {
				t.Errorf("seed %d: %s builds no slice, want it kept\n%s", seed, f, text)
			}
		}
	}
}

// TestIRCollectsReslice checks the fact GP-RESLICE promises, in process on
// LowerIR's IR: a collect into an empty slice whose source is dead after
// the loop and unshared takes the source's part (no growth, no copy),
// and one whose source is read after, shares another value's array, or is
// read again by a loop around the collect still copies (slices.Grow).
//
// Mutants: f7g-reslice-dropped (no collect takes its part),
// f7g-reslice-live (a source read after the loop counts as dead),
// f7g-reslice-shared (a source sharing another value's array counts as
// unshared), f7g-reslice-outer (a source read in each pass of a loop
// around the collect counts as dead). In the property
// (TestIRLoopIdiomsMatchSource), each of the last three writes the
// source, and the output shows it.
//
// GP-RESLICE-FIELD's field sources run in it too:
//   - f7g-reslice-field-dropped: no field source is taken;
//   - f7g-reslice-field-any: every other use of the record counts as
//     disjoint, so resliceFieldLive and resliceFieldWhole are taken, and
//     the property's output shows the write.
//
// GP-RESLICE-EMPTY's gaps too: f7g-reslice-empty-adjacent (the empty let
// must be the statement just before; resliceGap copies) and
// f7g-reslice-empty-written (a write between does not count; resliceGapWritten
// is taken and drops the element it appended, which the property shows).
func TestIRCollectsReslice(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		grows, text := collectGrowth(t, seed)
		for _, f := range resliceFuncs.taken {
			if g, ok := grows[f]; !ok || g {
				t.Errorf("seed %d: %s copies its part (IR body %v), want the source's part taken\n%s", seed, f, ok, text)
			}
		}
		for _, f := range resliceFuncs.kept {
			if !grows[f] {
				t.Errorf("seed %d: %s takes its source's part, want it copied\n%s", seed, f, text)
			}
		}
	}
}

// collectGrowth is, for each function of GenerateLoops(seed) with an IR
// body, whether the body grows a slice with slices.Grow (GP-COLLECT's
// copy; the element loop of an else branch appends in every form), and
// the program.
func collectGrowth(t *testing.T, seed uint64) (map[string]bool, string) {
	t.Helper()
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
	grows := map[string]bool{}
	for _, fn := range ir.Funcs {
		kinds := map[string]bool{}
		nodeKinds(fn.Body, kinds)
		grows[fn.Name] = kinds["pcall Grow"]
	}
	return grows, text
}

// nodeKinds adds to into the kind of every node under v, and "pcall NAME"
// for a package call.
func nodeKinds(v any, into map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		if k, ok := x["k"].(string); ok {
			into[k] = true
			if n, ok := x["name"].(string); ok && k == "pcall" {
				into["pcall "+n] = true
			}
		}
		for _, e := range x {
			nodeKinds(e, into)
		}
	case []any:
		for _, e := range x {
			nodeKinds(e, into)
		}
	}
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
