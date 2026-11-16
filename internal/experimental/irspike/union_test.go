package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// unionFuncs are GenerateUnion's functions: each gets an IR body in every
// seed.
var unionFuncs = append([]string{"mk", "flip", "weight", "name", "pick", "count", "firstB", "all", "build", "sum", "depth", "classify", "code", "stretch", "meters"}, treeFuncs...)

// TestIRUnionsMatchSource is IRG3's agreement property: for generated
// union programs (GenerateUnion), the IR path and the source path print
// the same, exit the same and fail with the same message. Every function
// but main gets an IR body in every seed, and the run reaches both union
// representations the lowering decides: the flat form (a tag switch, a
// variant put in by its of method) and the sealed interface (a type
// switch, a conversion to it). A failing seed prints the Ergo source,
// both outcomes and its replay command. It builds Go for the reason
// needsBuild gives.
func TestIRUnionsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 12)
	seen := map[string]bool{}
	flat, iface := 0, 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateUnion(seed)
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
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRUnionsMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var missing []string
		for _, f := range unionFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
		irFile := filepath.Join(dir, "ir", lower.IRFileName)
		irKinds(t, irFile, seen)
		of, tag := unionForms(t, irFile, seen)
		if of > 0 && tag > 0 {
			flat++
		}
	}
	if seen["tswitch"] && seen["toiface"] {
		iface++
	}
	if count < 12 {
		return // a replay: the coverage needs the whole run
	}
	var missing []string
	for _, k := range []string{"tswitch", "switch", "toiface", "assert", "zero", "mcall", "var", "goto", "block", "labeled block", "labeled range", "labeled break"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || flat == 0 || iface == 0 {
		t.Fatalf("the run did not reach: kinds %v, seeds with a flat union %d, with an interface union %d", missing, flat, iface)
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// unionForms counts an IR file's flat-union forms: calls of a union's of
// method, and switches on a tag field. It marks in seen each kind of
// statement it finds with a label ("labeled range": a loop a break inside
// a match leaves; "labeled block": a match tree's shared arm).
func unionForms(t *testing.T, file string, seen map[string]bool) (of, tag int) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			name, _ := v["name"].(string)
			if v["k"] == "mcall" && strings.HasPrefix(name, "of") {
				of++
			}
			if x, _ := v["x"].(map[string]any); v["k"] == "switch" && x["k"] == "field" && x["name"] == "tag" {
				tag++
			}
			if k, _ := v["k"].(string); v["label"] != nil {
				seen["labeled "+k] = true
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	return of, tag
}
