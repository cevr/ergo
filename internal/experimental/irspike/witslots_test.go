package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWitnessSlotsMatchSource is the seeded property over an inlined
// callee in a body with witness parameters (GenerateWitnessSlots; vt's
// `def: string is not error`): each program builds and prints the same on
// both lowerings, walk has an IR body, and the inliner copied note into
// it (walk's IR calls no note). A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the fault it guards is the IR checker's (ergoir)
// refusal of a def onto a local of another type, which only the patched
// Go compiler reports, and a local numbered onto one of its own type
// shows only when the program runs.
func TestIRWitnessSlotsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateWitnessSlots(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRWitnessSlotsMatchSource$' ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		data, err := os.ReadFile(filepath.Join(dir, "ir", lower.IRFileName))
		if err != nil {
			t.Fatal(err)
		}
		var mod struct {
			Funcs []struct {
				Name string `json:"name"`
				Body any    `json:"body"`
			} `json:"funcs"`
		}
		if err := json.Unmarshal(data, &mod); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, fn := range mod.Funcs {
			if fn.Name != "walk" {
				continue
			}
			found = true
			if callsNoteIn(fn.Body) {
				t.Errorf("seed %d: walk still calls note: the inliner copied nothing into the body with witness parameters (IR bodies: %v)\n%s\n%s", seed, rep.IR, replay, text)
			}
		}
		if !found {
			t.Errorf("seed %d: walk has no IR body (IR bodies: %v)\n%s\n%s", seed, rep.IR, replay, text)
		}
	}
}

// callsNoteIn reports a call or method call under v whose name ends in note.
func callsNoteIn(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if k, _ := x["k"].(string); k == "call" || k == "mcall" {
			if name, _ := x["name"].(string); strings.HasSuffix(name, "note") {
				return true
			}
		}
		for _, e := range x {
			if callsNoteIn(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if callsNoteIn(e) {
				return true
			}
		}
	}
	return false
}
