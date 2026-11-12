package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRFailuresMatchSource is IRG2's agreement property: for generated
// failure programs (GenerateFail), the IR path and the source path print
// the same, exit the same and fail with the same message. Each run must
// reach every failure-ABI IR kind and give IR bodies to fallible
// functions. A failing seed prints the Ergo source, both outcomes and its
// replay command. It builds Go for the reason needsBuild gives.
func TestIRFailuresMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 16)
	seen := map[string]bool{}
	fallibleIR, defects, polled := 0, 0, 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateFail(seed)
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
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\nreplay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run TestIRFailuresMatchSource ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
		if a.exit != 0 {
			defects++
		}
		irKinds(t, filepath.Join(dir, "ir", lower.IRFileName), seen)
		polled += backEdgePolls(t, filepath.Join(dir, "ir", lower.IRFileName))
		for _, name := range rep.IR {
			if strings.Contains(text, "func "+name+"(") && fallibleHead(text, name) {
				fallibleIR++
			}
		}
	}
	if count < 16 {
		return // a replay: the coverage needs the whole run
	}
	var missing []string
	for _, k := range []string{"fail", "def2", "nil", "zero", "tswitch", "assert", "panic", "mcall", "while", "range"} {
		if !seen[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || fallibleIR == 0 || defects == 0 || polled == 0 {
		t.Fatalf("the run did not reach: kinds %v, fallible IR bodies %d, failing seeds %d, loops ending in their poll %d", missing, fallibleIR, defects, polled)
	}
}

// backEdgePolls counts the loops of an IR file whose body ends in the
// task's Check: the back-edge poll of a polling frame (runtime.md, I1).
// No output shows a poll (it acts only on a stop), so the property counts
// the one promised fact of its placement.
func backEdgePolls(t *testing.T, file string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	n := 0
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k := v["k"]; k == "while" || k == "range" {
				if body, _ := v["body"].([]any); len(body) > 0 {
					last, _ := body[len(body)-1].(map[string]any)
					x, _ := last["x"].(map[string]any)
					if last["k"] == "expr" && x["k"] == "mcall" && x["name"] == "Check" {
						n++
					}
				}
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
	return n
}

// fallibleHead reports that name's declaration in text has a fails row.
func fallibleHead(text, name string) bool {
	_, rest, ok := strings.Cut(text, "func "+name+"(")
	head, _, _ := strings.Cut(rest, "{")
	return ok && strings.Contains(head, " fails ")
}
