package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFillsMatchSource is the seeded property over slices built by a
// count loop of one-value appends to an empty slice (B-FILL,
// GenerateFill): the IR makes them at their final length and fills them
// by index, the source path appends (the reference). Each program prints
// the same under both lowerings; the functions GenerateFill presizes hold
// v1.SliceOfLen in their IR bodies, and its look-alikes do not. A failing
// seed prints the Ergo source, both outcomes and its replay command.
//
// It builds Go because the values, lengths and arrays the two forms give
// show only when the compiled program reads them back.
func TestIRFillsMatchSource(t *testing.T) {
	f7Property(t, "TestIRFillsMatchSource", GenerateFill, append(append([]string{}, fillFuncs...), sameFuncs...)...)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateFill(seed)
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
		bodies := irBodies(t, files)
		for _, f := range fillFuncs {
			if !strings.Contains(bodies[f], `"SliceOfLen"`) {
				t.Errorf("seed %d: %s's slice is not presized\n%s", seed, f, text)
			}
		}
		for _, f := range sameFuncs {
			if strings.Contains(bodies[f], `"SliceOfLen"`) {
				t.Errorf("seed %d: %s's slice is presized, and the appends it needs are gone\n%s", seed, f, text)
			}
		}
	}
}

// irBodies is the JSON of each IR body of files' IR file, by the
// function's Go name.
func irBodies(t *testing.T, files []lower.File) map[string]string {
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
