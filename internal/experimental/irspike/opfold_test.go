package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRConstFoldsMatchSource is PATCH-MIN M6's property over constant
// folding before gc (GenerateOpFold): each program prints the same,
// exits the same and fails with the same defect line under the source
// lowering and the IR; in the IR, every folds* function holds no
// operation or conversion whose operands are all constants (the fold the
// derivative's reader did is now the builder's), and the stays* functions
// keep theirs (a division by zero and an int8 overflow are the run's). A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because a fold that rounds, truncates or fits differently
// from Go shows only in the compiled program's output.
func TestIRConstFoldsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	folded := 0
	for seed := first; seed < first+count; seed++ {
		text := GenerateOpFold(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRConstFoldsMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay, text)
		}
		folded += rep.ConstFolds
		left := constOps(t, filepath.Join(dir, "ir", lower.IRFileName))
		for name, n := range left {
			if strings.HasPrefix(name, "folds") && n > 0 && lower.OpFoldOn() {
				t.Errorf("seed %d: %s keeps %d operations of constants\n%s\n%s", seed, name, n, replay, text)
			}
		}
		for _, line := range strings.Split(text, "\n") {
			if name, ok := strings.CutPrefix(line, "func folds"); ok {
				name = "folds" + name[:strings.Index(name, "(")]
				if _, built := left[name]; !built {
					t.Errorf("seed %d: %s has no IR body (fallback %v)\n%s", seed, name, rep.Fallback, replay)
				}
			}
		}
		for _, name := range []string{"staysWrap", "staysDiv"} {
			if left[name] == 0 {
				t.Errorf("seed %d: %s has no IR body or folded what Go keeps (operations of constants %v)\n%s", seed, name, left, replay)
			}
		}
	}
	if lower.OpFoldOn() && folded == 0 {
		t.Errorf("no constant folded")
	}
}

// constOps counts, per IR function, the bin, un, conv and len nodes whose
// operands are all constants.
func constOps(t *testing.T, file string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Funcs []struct {
			Name string
			Body any
		}
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	isConst := func(v any) bool {
		c, _ := v.(map[string]any)
		return c != nil && c["k"] == "const"
	}
	out := map[string]int{}
	for _, f := range m.Funcs {
		out[f.Name] += 0
		var walk func(any)
		walk = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				switch v["k"] {
				case "bin":
					if isConst(v["x"]) && isConst(v["y"]) {
						out[f.Name]++
					}
				case "un", "conv", "len":
					if isConst(v["x"]) {
						out[f.Name]++
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
		walk(f.Body)
	}
	return out
}
