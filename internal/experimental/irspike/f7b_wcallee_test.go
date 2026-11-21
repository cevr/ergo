package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWitnessCalleesMatchSource is the seeded property over generic
// bodies that call operations through their witness parameters
// (B-WITNESS, GenerateWitnessCallee): each program prints the same under
// both lowerings, each site has an IR body, and that body calls a
// witness's field (a "vcall"); main's call of single goes to the copy
// for its known witness, as the emitter's does, never to single itself.
// A failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the witness locals, the uniform (value, error)
// results of their fields and the failures they carry show only when the
// compiled program runs them.
func TestIRWitnessCalleesMatchSource(t *testing.T) {
	f7Property(t, "TestIRWitnessCalleesMatchSource", GenerateWitnessCallee, wcalleeSites...)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateWitnessCallee(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatal(err)
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
		calls := map[string]bool{}
		for _, fn := range ir.Funcs {
			kinds := map[string]bool{}
			nodeKinds(fn.Body, kinds)
			calls[fn.Name] = kinds["vcall"]
			if fn.Name == "main_1" && callsNamed(fn.Body, "single") {
				t.Errorf("seed %d: main calls single itself, not the copy for its known witness\n%s", seed, text)
			}
		}
		for _, f := range wcalleeSites {
			if !calls[f] {
				t.Errorf("seed %d: %s calls no witness field\n%s", seed, f, text)
			}
		}
	}
}

// callsNamed reports whether a "call" node under v names the function name.
func callsNamed(v any, name string) bool {
	switch x := v.(type) {
	case map[string]any:
		if x["k"] == "call" && x["name"] == name {
			return true
		}
		for _, e := range x {
			if callsNamed(e, name) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if callsNamed(e, name) {
				return true
			}
		}
	}
	return false
}
