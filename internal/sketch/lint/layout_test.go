package lint_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// layoutConf is the base preset off and the layout preset on, with
// with-at-entry's options.
func layoutConf(t *testing.T, functions []string) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	for _, name := range []string{"with-at-entry", "concept-file-names"} {
		settings[name] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	}
	if functions != nil {
		raw, _ := json.Marshal(map[string][]string{"functions": functions})
		settings["with-at-entry"] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: raw}
	}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// layoutFindings lints every package below dir (a module) under conf and
// returns its findings as "file:line rule", once each.
func layoutFindings(t *testing.T, dir string, conf *lint.Configuration) []string {
	t.Helper()
	targets, err := sketchfront.DiscoverPackages(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, target := range targets {
		a, err := sketchfront.CheckDir(target.Dir)
		if err != nil {
			t.Fatalf("%s: %v", target.Dir, err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			continue // the examples' own gates own their diagnostics
		}
		for _, f := range conf.Run(in).Findings {
			key := fmt.Sprintf("%s:%d %s", f.File, f.Line, f.Rule)
			if !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	slices.Sort(out)
	return out
}

// examplePicks are the functions of examples/ outside an entry file that
// pick an implementation, each by the layout preset's functions option:
// the in-process loopback servers, which serve a fresh memory provider.
var examplePicks = []string{"loopback.NewLoopback"}

// TestLayoutPresetOnExamples holds the examples to the layout their
// AGENTS.md rule states (owner, 2026-11-04): each example module is clean
// under the layout preset, with examplePicks listed. A planted module then
// shows each rule firing where the layout is broken, and only there: a
// kind-suffixed file and a with that picks a program implementation
// outside the entry file; not the same with in a function the options
// list, in a file of tests, or in cmd/NAME/main.ergo, nor a with of a
// capability value passed down.
func TestLayoutPresetOnExamples(t *testing.T) {
	conf := layoutConf(t, examplePicks)
	modules, err := filepath.Glob(filepath.Join("..", "..", "..", "examples", "*", "ergo.mod"))
	if err != nil || len(modules) == 0 {
		t.Fatalf("no example modules: %v", err)
	}
	for _, mod := range modules {
		if got := layoutFindings(t, filepath.Dir(mod), conf); len(got) > 0 {
			t.Errorf("example %s breaks the layout preset: %v", filepath.Base(filepath.Dir(mod)), got)
		}
	}

	dir := t.TempDir()
	files := map[string]string{
		"ergo.mod": "module app\n",
		"store/store.ergo": `// Store keeps counts.
type Store {
	Get() int,
}

// Fixed is a Store of one count.
type Fixed { N int }

func Get(f Fixed) int { f.N }
`,
		"store/store_service.ergo": "func zero() int {\n\t0\n}\n",
		"run/run.ergo": `import app/store

func Count() int needs store.Store {
	store.Store.Get()
}

func Wired() int {
	with context { store.Store: store.Fixed{N: 1} } {
		Count()
	}
}

func Listed() int {
	with context { store.Store: store.Fixed{N: 2} } {
		Count()
	}
}

func Passed(s store.Store) int {
	with context { store.Store: s } {
		Count()
	}
}
`,
		"run/tests.ergo": `import app/store

func fixed(n int) int {
	with context { store.Store: store.Fixed{N: n} } {
		Count()
	}
}

test WithInATest {
	assert fixed(3) == 3
}
`,
		"cmd/app/main.ergo": `import app/run
import app/store

func main() needs Console {
	let n = with context { store.Store: store.Fixed{N: 4} } {
		run.Count()
	}
	Console.Println("\(n) \(run.Wired()) \(run.Listed()) \(run.Passed(store.Fixed{N: 5}))")
}
`,
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
	want := []string{"run/run.ergo:8 with-at-entry", "store/store_service.ergo:1 concept-file-names"}
	if got := layoutFindings(t, dir, layoutConf(t, []string{"run.Listed"})); !slices.Equal(got, want) {
		t.Fatalf("the planted module gave %v, want %v", got, want)
	}
}
