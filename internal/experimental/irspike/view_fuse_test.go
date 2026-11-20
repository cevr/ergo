package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// fusedViews is IRF-1's part of TestIRViewsMatchSource: for generated
// fused-view programs (GenerateFuse), whose view lives in another package,
// is generic, is called as a method, or has results in ifs, nested matches
// and an early return, the IR path prints, exits and fails as the source
// path. walk, pick and code get IR bodies in every seed and each is one
// switch, unless Read is past the fusion budget (Big), where each keeps
// the two-step form: it calls no Read and switches on no variant's tag (match fusion
// jumps from each of Read's results to the arm its variant reaches). Over
// the run, every form is reached and some seed panics in a literal.
func fusedViews(t *testing.T, src, ir Toolchain, first, count uint64) {
	t.Helper()
	forms := map[string]int{}
	panics := 0
	for seed := first; seed < first+count; seed++ {
		files, form := GenerateFuse(seed)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRViewsMatchSource$' ./internal/experimental/irspike"
		dir := t.TempDir()
		mod := filepath.Join(dir, "p")
		var text strings.Builder
		for _, name := range []string{"rows/rows.ergo", "cmd/p/main.ergo", "ergo.mod"} {
			body, ok := files[name]
			if !ok {
				continue
			}
			text.WriteString("// " + name + "\n" + body + "\n")
			path := filepath.Join(mod, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		u, err := Load(filepath.Join(mod, "cmd", "p"))
		if err != nil {
			t.Fatalf("fuse seed %d: the program does not check: %v\n%s", seed, err, text.String())
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("fuse seed %d (%+v): the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, form, a, b, replay, text.String())
		}
		calls, tags := fuseSites(t, filepath.Join(dir, "ir", lower.IRFileName))
		for _, f := range []string{"walk", "pick", "code"} {
			if _, ok := calls[f]; !ok {
				t.Fatalf("fuse seed %d (%+v): no IR body for %s (IR %v, fallback %v)\n%s\n%s", seed, form, f, rep.IR, rep.Fallback, replay, text.String())
			}
			if form.Big {
				// Past the budget: the site calls Read and switches on its tag.
				if calls[f] == 0 || tags[f] == 0 {
					t.Fatalf("fuse seed %d (%+v): %s's Read is past the fusion budget, yet it is fused (%d calls, %d tag switches)\n%s\n%s", seed, form, f, calls[f], tags[f], replay, text.String())
				}
				continue
			}
			if calls[f] > 0 || tags[f] > 0 {
				t.Fatalf("fuse seed %d (%+v): %s calls Read %d times and switches on a tag %d times: it is not one switch\n%s\n%s", seed, form, f, calls[f], tags[f], replay, text.String())
			}
		}
		for name, on := range map[string]bool{"cross": form.Cross, "generic": form.Generic, "method": form.Method, "tails": form.Tails, "big": form.Big} {
			if on {
				forms[name]++
			}
		}
		if a.exit != 0 {
			panics++
		}
	}
	if count < 16 {
		return // a replay: the coverage needs the whole run
	}
	if forms["cross"] == 0 || forms["generic"] == 0 || forms["method"] == 0 || forms["tails"] == 0 || forms["big"] == 0 || panics == 0 {
		t.Errorf("the fused run did not reach every form (%v) and a panic in a literal (%d seeds)", forms, panics)
	}
}

// readName matches Read and its monomorphic copies (mono.go: ReadMono,
// ReadMono_i1 …): since b9f4df7cf the IR calls a generic Read's copy at
// its known type arguments, so a Generic seed past the budget calls
// ReadMono_i1, not Read (fuse seed 16).
var readName = regexp.MustCompile(`^Read(Mono(_i[0-9]+)?)?$`)

// fuseSites counts, in each function of an IR file (by its name without a
// receiver), the calls of Read (or its monomorphic copy) and the switches
// on a field named tag.
func fuseSites(t *testing.T, file string) (calls, tags map[string]int) {
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
	calls, tags = map[string]int{}, map[string]int{}
	for _, f := range v.Funcs {
		name := f.Name[strings.LastIndex(f.Name, ".")+1:]
		calls[name] += 0
		var walk func(any)
		walk = func(x any) {
			switch x := x.(type) {
			case map[string]any:
				if (x["k"] == "call" || x["k"] == "mcall") && readName.MatchString(fmt.Sprint(x["name"])) {
					calls[name]++
				}
				if s, ok := x["x"].(map[string]any); ok && x["k"] == "switch" && s["k"] == "field" && s["name"] == "tag" {
					tags[name]++
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
	return calls, tags
}
