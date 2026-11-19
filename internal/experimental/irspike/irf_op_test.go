package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIROpCallsMatchSource is the seeded property over capability calls
// (IR-FORMS IRF-5, GenerateOps): a provider known through every with that
// reaches a function, two providers for one capability, a with that
// rebinds it inside a function, a function a literal calls, Plains of
// different fields: the IR path prints what the source path prints. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because a wrong adapter shows only when the assertion at
// the function's entry runs, and a wrong provider only in the output.
//
// Mutants (testdata/mutants.json): irf-op-two-providers-specialised (a
// function two providers reach reads one), irf-op-inner-with-ignored (a
// call inside its function's own with reads the function's binding),
// irf-op-literal-call-explained (a call in a function literal counts as
// a site of its function's binding).
func TestIROpCallsMatchSource(t *testing.T) {
	f7Property(t, "TestIROpCallsMatchSource", GenerateOps, append(append([]string{}, opFuncs...), opDynamic...)...)
}

// TestIROpCallsKnownOnTheIR reads the promised form on LowerIR's IR, in
// process: each function of opFuncs reads Meter's field once at its entry
// as the adapter (an assertion), and each of opDynamic reads none.
//
// Mutant irf-op-never-known: no call takes the known adapter.
func TestIROpCallsKnownOnTheIR(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateOps(seed)
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
		known := map[string]bool{}
		for _, fn := range ir.Funcs {
			known[fn.Name] = entryAssert(fn.Body, "Meter")
		}
		for _, f := range opFuncs {
			if !known[f] {
				t.Errorf("seed %d: %s reads no known Meter at its entry\n%s", seed, f, text)
			}
		}
		for _, f := range opDynamic {
			if known[f] {
				t.Errorf("seed %d: %s reads a known Meter, though another provider reaches it\n%s", seed, f, text)
			}
		}
	}
}

// entryAssert reports a statement of body that defines a local as an
// assertion of the environment's field.
func entryAssert(body any, field string) bool {
	list, _ := body.([]any)
	for _, s := range list {
		m, _ := s.(map[string]any)
		if m["k"] != "def" {
			continue
		}
		x, _ := m["x"].(map[string]any)
		if x["k"] != "assert" {
			continue
		}
		if f, _ := x["x"].(map[string]any); f["k"] == "field" && f["name"] == field {
			return true
		}
	}
	return false
}
