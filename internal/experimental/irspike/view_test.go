package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// viewSites are GenerateView's functions that match on a view call.
var viewSites = []string{"walk", "pick", "code"}

// TestIRViewsMatchSource is VW1's agreement property: for generated view
// programs (GenerateView), the IR path, which builds a match on a view call
// as one switch, prints, exits and fails as the source path, whose match
// builds the view's variant and then switches on it (the two-step form).
// view, walk, pick and code get IR bodies in every seed, and every site is
// one switch (its IR calls no view), shared arms included. Over the run,
// some seed panics in a view's literal. A failing seed prints the Ergo
// source, both outcomes and its replay command. It builds Go for the
// reason needsBuild gives.
func TestIRViewsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 16)
	panics := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateView(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRViewsMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		for _, f := range append([]string{"view"}, viewSites...) {
			if !slices.Contains(rep.IR, f) {
				t.Fatalf("seed %d: no IR body for %s (fallback %v)\n%s\n%s", seed, f, rep.Fallback, replay, text)
			}
		}
		calls := viewCalls(t, filepath.Join(dir, "ir", lower.IRFileName))
		for _, f := range viewSites {
			if calls[f] > 0 {
				t.Fatalf("seed %d: %s's match calls view: it is not one switch\n%s\n%s", seed, f, replay, text)
			}
		}
		if strings.Contains(text, "100 / x") && a.exit != 0 {
			panics++
		}
	}
	if count < 16 {
		return // a replay: the coverage needs the whole run
	}
	if panics == 0 {
		t.Errorf("no seed panicked in a view's literal")
	}
}

// viewCalls counts, in each function of an IR file, the calls of view.
func viewCalls(t *testing.T, file string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Funcs []struct {
			Name string `json:"name"`
			Body any    `json:"body"`
		} `json:"funcs"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, f := range v.Funcs {
		var walk func(any)
		walk = func(x any) {
			switch x := x.(type) {
			case map[string]any:
				if x["k"] == "call" && x["name"] == "view" {
					out[f.Name]++
				}
				for _, y := range x {
					walk(y)
				}
			case []any:
				for _, y := range x {
					walk(y)
				}
			}
		}
		walk(f.Body)
	}
	return out
}
