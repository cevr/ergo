package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRIntMapsMatchSource is MI-2's property over Maps with an integer
// key (GenerateIntMap): the IR lowers them to the runtime's IntMap, the
// source path keeps Go's map (the reference), and each program prints the
// same under both. Each of intMapFuncs has an IR body, and that body
// calls the table's methods, so the property runs the table and not a
// lowering that fell back to Go's map. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because the table's entries, lengths and copies show only
// when the compiled program reads them back.
func TestIRIntMapsMatchSource(t *testing.T) {
	f7Property(t, "TestIRIntMapsMatchSource", GenerateIntMap, intMapFuncs...)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateIntMap(seed)
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
			t.Fatal(err)
		}
		bodies := intMapBodies(t, files)
		for _, f := range intMapFuncs {
			if !strings.Contains(bodies[f], `"k":"mcall"`) || !strings.Contains(bodies[f], `"name":"Put"`) {
				t.Errorf("seed %d: %s's Map is not an IntMap on the IR\n%s", seed, f, text)
			}
		}
		// walked's Map type conflicts: Go's map, iterated on the IR.
		if b, ok := bodies["walked"]; !ok || !strings.Contains(b, `"k":"range"`) || strings.Contains(b, `"name":"NewIntMap"`) {
			t.Errorf("seed %d: walked's Map is not Go's map iterated on the IR\n%s", seed, text)
		}
	}
}

// TestIntMapRoundsAreFew counts the int map choice's lowerings, the work
// it adds to a build (intmap.go): a GenerateIntMap program has one Map
// type that conflicts (walked's, a for-in over it), so it lowers twice,
// the first round finding that conflict by its cause and the second none.
// A choice that lowered again with nothing new to turn off would run all
// of intMapRounds (mutant mi2-rounds-always).
func TestIntMapRoundsAreFew(t *testing.T) {
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateIntMap(seed)
		file := filepath.Join(t.TempDir(), "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatal(err)
		}
		_, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatal(err)
		}
		if rep.MapRounds != 2 || rep.MapConflicts["body: for-in over an int map"] != 1 {
			t.Fatalf("seed %d: %d lowerings, conflicts %v; want 2, one for-in over an int map\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIntMapRoundsAreFew ./internal/experimental/irspike\n%s", seed, rep.MapRounds, rep.MapConflicts, seed, text)
		}
	}
}

// intMapBodies is each IR function's body, as JSON, by name.
func intMapBodies(t *testing.T, files []lower.File) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range files {
		if filepath.Base(f.Path) != lower.IRFileName {
			continue
		}
		var ir struct {
			Funcs []struct {
				Name string          `json:"name"`
				Body json.RawMessage `json:"body"`
			} `json:"funcs"`
		}
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			out[fn.Name] = string(fn.Body)
		}
	}
	return out
}
