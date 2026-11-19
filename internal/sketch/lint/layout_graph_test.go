package lint_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// graphConf is every rule off but the graph rules: layer with its layers,
// domain-imports-implementation, hidden-dependency and file-cycle
// (opt-in).
func graphConf(t *testing.T, layers []string) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	raw, _ := json.Marshal(map[string][]string{"layers": layers})
	settings["layer"] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: raw}
	for _, name := range []string{"file-cycle", "domain-imports-implementation", "hidden-dependency"} {
		settings[name] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestGraphLayoutAgreesWithReference writes a module (GRAPH-LINT, the
// acceptance of CI-SELECT §12.8): an operation type and a package that
// implements it, a library and a package that re-exports two of its
// functions as lets, and one to four packages p0..pN. Each may declare
// an operation type of its own (a domain package), imports earlier ones
// (a layering lists them in a random order, so some imports go up), and
// maybe the implementation (domain-imports-implementation only in a
// domain package), a re-export used with or without its own import
// (hidden-dependency only without), two files that use each other
// (file-cycle) or one file that uses another one way (none). It checks
// that the graph rules report exactly the planted findings, and that
// after fix --lint the module checks, the hidden-dependency findings are
// gone and every other finding stays. A domain package may hold a file
// of the test build's constructors (a row-free NewBox and a helper):
// test code, so no finding, unless the package's code uses the helper.
// Also no finding: an import of
// both, a domain package that also implements store's type, or the
// implementation imported by a file of tests. A test-only package cycle
// cannot be planted: the loader refuses every import cycle (S108).
// Mutants: gl-layer-order, gl-entry, gl-reexport-imported,
// gl-file-reach, gl-fix-qualifier, gl-fix-import, gl-drop-once,
// gl-domain-only, gl-own-optype, gl-testfile, gl-ctor-file,
// gl-ctor-file-used.
func TestGraphLayoutAgreesWithReference(t *testing.T) {
	for _, seed := range propertySeeds(10) {
		r := rand.New(rand.NewPCG(seed, 0x6a1))
		files := map[string]*ownerFile{
			"ergo.mod":           {lines: []string{"module app"}},
			"store/store.ergo":   {lines: strings.Split("// Store keeps a count.\ntype Store {\n\tGet() int,\n}", "\n")},
			"memory/memory.ergo": {lines: strings.Split("import app/store\n\n// Fixed is a Store of one count.\ntype Fixed { N int } implements store.Store\n\nfunc Get(f Fixed) int { f.N }", "\n")},
			"lib/lib.ergo":       {lines: strings.Split("func Base() int { 7 }\n\nfunc Twice() int { 14 }", "\n")},
			"re/re.ergo":         {lines: strings.Split("import app/lib\n\nlet Base = lib.Base\n\nlet Twice = lib.Twice", "\n")},
			// both declares an operation type and implements store's: a
			// domain package, which a domain package may import.
			"both/both.ergo": {lines: strings.Split("import app/store\n\n// Counter counts.\ntype Counter {\n\tCount() int,\n}\n\n// Plain is a Store of one count.\ntype Plain { N int } implements store.Store\n\nfunc Get(p Plain) int { p.N }", "\n")},
		}
		n := 1 + r.IntN(4)
		order := r.Perm(n) // order[i] is p_i's place in the layering, from the top
		layers := make([]string, n+1)
		layers[0] = "app/cmd/..."
		for i, o := range order {
			layers[1+o] = fmt.Sprintf("app/p%d", i)
		}
		// The entry chooses the implementation: no finding.
		mainFile := &ownerFile{}
		mainFile.add("import app/memory", "")
		sum := []string{"memory.Fixed{N: 5}.N"}
		for i := range n {
			f := &ownerFile{}
			name := fmt.Sprintf("p%d", i)
			files[name+"/"+name+".ergo"] = f
			mainFile.add("import app/"+name, "")
			sum = append(sum, name+".F()")
			var deps []string
			for j := range i {
				if r.IntN(2) == 0 {
					rule := ""
					if order[j] < order[i] {
						rule = "layer"
					}
					f.add(fmt.Sprintf("import app/p%d", j), rule)
					deps = append(deps, fmt.Sprintf("p%d.F()", j))
				}
			}
			domain, impl, reuse, direct := r.IntN(2) == 0, r.IntN(3) == 0, r.IntN(2) == 0, r.IntN(3) == 0
			if impl {
				// Only a domain package is held to it.
				f.add("import app/memory", map[bool]string{true: "domain-imports-implementation"}[domain])
				deps = append(deps, "memory.Fixed{N: 1}.N")
			}
			if r.IntN(3) == 0 {
				f.add("import app/both", "")
				deps = append(deps, "both.Plain{N: 3}.N")
			}
			if r.IntN(3) == 0 {
				// A file of tests may choose an implementation.
				files[name+"/tests.ergo"] = &ownerFile{lines: []string{"import app/memory", "", "test FixedHolds {", "\tassert memory.Fixed{N: 1}.N == 1", "}"}}
			}
			if reuse {
				f.add("import app/re", "")
			}
			if direct {
				f.add("import app/lib", "")
				deps = append(deps, "lib.Twice()")
			}
			f.add("", "")
			if domain {
				f.add("// Op is the package's contract.", "")
				f.add("type Op {", "")
				f.add("\tRun() int,", "")
				f.add("}", "")
				f.add("", "")
				if r.IntN(2) == 0 {
					// A file of the test build's constructors: the row-free
					// NewBox and a helper. Test code, unless code outside
					// it uses the helper.
					used := r.IntN(2) == 0
					files[name+"/box.ergo"] = &ownerFile{lines: []string{"type Box { N int }"}}
					files[name+"/world.ergo"] = &ownerFile{lines: []string{"import app/memory", "", "func NewBox() Box { Box{N: memory.Fixed{N: 1}.N} }", "", "func Spare() int { memory.Fixed{N: 2}.N }"}}
					if used {
						files[name+"/world.ergo"].want = []string{"1 domain-imports-implementation"}
						deps = append(deps, "Spare()")
					}
				}
			}
			body := strings.Join(append(deps, "1"), " + ")
			f.add("func F() int { "+body+" }", "")
			if reuse {
				f.add("", "")
				rule := "hidden-dependency"
				if direct {
					rule = ""
				}
				f.add("func G() int { re.Base() }", rule)
				sum = append(sum, name+".G()")
			}
			if r.IntN(3) == 0 {
				g := &ownerFile{}
				files[name+"/half.ergo"] = g
				f.add("", "")
				f.add("func H() int { half() }", "file-cycle")
				f.add("", "")
				f.add("func whole() int { 2 }", "")
				g.add("func half() int { whole() }", "file-cycle")
				sum = append(sum, name+".H()")
			} else if r.IntN(2) == 0 {
				// A use one way between two files is no cycle.
				files[name+"/side.ergo"] = &ownerFile{lines: []string{"func side() int { 4 }"}}
				f.add("", "")
				f.add("func K() int { side() }", "")
				sum = append(sum, name+".K()")
			}
		}
		// An entry may declare a contract and still choose.
		mainFile.add("", "")
		mainFile.add("// Op is the entry's contract.", "")
		mainFile.add("type Op {", "")
		mainFile.add("\tRun() int,", "")
		mainFile.add("}", "")
		mainFile.add("", "")
		mainFile.add("func main() needs Console {", "")
		mainFile.add("\tConsole.Println(\"\\("+strings.Join(sum, " + ")+")\")", "")
		mainFile.add("}", "")
		files["cmd/app/main.ergo"] = mainFile
		dir := t.TempDir()
		var want []string
		for name, f := range files {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Join(f.lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, w := range f.want {
				line, rule, _ := strings.Cut(w, " ")
				want = append(want, name+":"+line+" "+rule)
			}
		}
		slices.Sort(want)
		conf := graphConf(t, layers)
		src := func() string {
			var b strings.Builder
			for _, name := range slices.Sorted(func(yield func(string) bool) {
				for k := range files {
					if !yield(k) {
						return
					}
				}
			}) {
				data, _ := os.ReadFile(filepath.Join(dir, name))
				fmt.Fprintf(&b, "--- %s\n%s", name, data)
			}
			return b.String()
		}
		replay := fmt.Sprintf("replay: go test -run TestGraphLayoutAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if got := layoutFindings(t, dir, conf); !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s): layers %v: got %v, want %v\n%s", seed, replay, layers, got, want, src())
		}
		entry := filepath.Join(dir, "cmd", "app")
		texts, chk, err := fix.Load(entry)
		if err != nil {
			t.Fatal(err)
		}
		lintr, err := fix.LintOf(entry, conf)
		if err != nil {
			t.Fatal(err)
		}
		res, err := fix.RunLint(texts, chk, lintr)
		if err != nil {
			t.Fatalf("seed %d (%s): fix --lint: %v\n%s", seed, replay, err, src())
		}
		for path, text := range res.Texts {
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The fix moves import lines, so the findings compare by file and
		// rule.
		unlined := func(keys []string) []string {
			var out []string
			for _, k := range keys {
				file, rest, _ := strings.Cut(k, ":")
				_, rule, _ := strings.Cut(rest, " ")
				out = append(out, file+" "+rule)
			}
			slices.Sort(out)
			return out
		}
		var rest []string
		for _, w := range unlined(want) {
			if !strings.HasSuffix(w, " hidden-dependency") {
				rest = append(rest, w)
			}
		}
		if got := unlined(layoutFindings(t, dir, conf)); !slices.Equal(got, rest) {
			t.Fatalf("seed %d (%s): after fix --lint got %v, want %v\n%s", seed, replay, got, rest, src())
		}
	}
}

// TestGraphLayoutTexts is the diagnostic text of each graph rule, one row
// each (GRAPH-LINT acceptance), linted with the importing package as
// the run's root, whose checker path is "main". Mutants: gl-text,
// gl-unit-path.
func TestGraphLayoutTexts(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ergo.mod":           "module app\n",
		"store/store.ergo":   "// Store keeps a count.\ntype Store {\n\tGet() int,\n}\n",
		"memory/memory.ergo": "import app/store\n\n// Fixed is a Store of one count.\ntype Fixed { N int } implements store.Store\n\nfunc Get(f Fixed) int { f.N }\n",
		"lib/lib.ergo":       "func Base() int { 7 }\n",
		"re/re.ergo":         "import app/lib\n\nlet Base = lib.Base\n",
		"low/low.ergo":       "import app/memory\nimport app/re\nimport app/top\n\n// Op is low's contract.\ntype Op {\n\tRun() int,\n}\n\nfunc F() int { memory.Fixed{N: 1}.N + re.Base() + top.T() + half() }\n\nfunc whole() int { 2 }\n",
		"low/half.ergo":      "func half() int { whole() }\n",
		"top/top.ergo":       "func T() int { 3 }\n",
		"cmd/app/main.ergo":  "import app/low\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(low.F())\")\n}\n",
	}
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := sketchfront.CheckDir(filepath.Join(dir, "low"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range graphConf(t, []string{"app/top", "app/low"}).Run(lint.InputOf(a)).Findings {
		got = append(got, fmt.Sprintf("%s:%d:%d %s %s: %s; %s", f.File, f.Line, f.Column, f.Code, f.Rule, f.Message, f.Instead))
	}
	slices.Sort(got)
	want := []string{
		"low/half.ergo:1:19 L155 file-cycle: this use reaches low/low.ergo, whose uses come back to this file; move the declarations the two files share into one of them, or into a file both use",
		"low/low.ergo:10:42 L158 hidden-dependency: this name reaches app/lib through a re-export, and this package does not import it; name the declaration itself and import its package",
		"low/low.ergo:10:61 L155 file-cycle: this use reaches low/half.ergo, whose uses come back to this file; move the declarations the two files share into one of them, or into a file both use",
		"low/low.ergo:1:8 L157 domain-imports-implementation: this package declares an operation type and imports app/memory, an implementation of another package's operation type; need the operation type, and bind the implementation in the binary's main",
		"low/low.ergo:3:8 L156 layer: this import goes up the layering: app/top is above app/low; move what the lower package needs from the upper one down, or pass it in",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
