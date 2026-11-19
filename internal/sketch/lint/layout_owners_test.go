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

	"ergo.local/prototype/internal/sketch/lint"
)

// ownersConf is the base preset off and the four owner rules of the
// layout preset (LH2d) on, with capability-outside-owner's owners.
func ownersConf(t *testing.T, owners map[string][]string) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	for _, name := range []string{"foreign-outside-adapter", "console-outside-entry", "test-reads-unexported"} {
		settings[name] = lint.Setting{Severity: lint.Warning, HasSeverity: true}
	}
	raw, _ := json.Marshal(map[string]any{"owners": owners})
	settings["capability-outside-owner"] = lint.Setting{Severity: lint.Warning, HasSeverity: true, Options: raw}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// ownerFile is a file the generator writes and the findings it plants,
// as "line rule".
type ownerFile struct {
	lines []string
	want  []string
}

func (f *ownerFile) add(line string, rule string) {
	f.lines = append(f.lines, line)
	if rule != "" {
		f.want = append(f.want, fmt.Sprintf("%d %s", len(f.lines), rule))
	}
}

// TestLayoutOwnersAgreeWithReference writes a module of one to three
// packages, each choosing whether it declares an operation type, imports
// a pure Go package or an impure one, runs the store's capability, writes
// Console, and holds tests and a law that call an unexported function of
// its code, an exported one, one only tests call or a helper beside the
// tests. The owners
// option gives store.Store to p0; the entry file, whose package declares
// an operation type, and the file of tests write Console too. The
// reference names each finding from those choices:
// foreign-outside-adapter on an impure import go in a package with an
// operation type, capability-outside-owner on the store's operation
// outside p0, console-outside-entry on a Console write in a package with
// an operation type and no main, outside its tests, and
// test-reads-unexported on a test's call of the code's unexported
// function. Mutants: lh2d-adapter-any-package, lh2d-adapter-pure-import,
// lh2d-console-entry, lh2d-console-contract, lh2d-console-test-file,
// lh2d-owner-ignored, lh2d-unexported-helper, lh2d-unexported-support,
// lh2d-unexported-exported,
// lh2d-unexported-law.
func TestLayoutOwnersAgreeWithReference(t *testing.T) {
	conf := ownersConf(t, map[string][]string{"store.Store": {"p0"}})
	for _, seed := range propertySeeds(12) {
		r := rand.New(rand.NewPCG(seed, 0xd4))
		files := map[string]*ownerFile{
			"ergo.mod":          {lines: []string{"module app"}},
			"store/store.ergo":  {lines: strings.Split("// Store keeps a count.\ntype Store {\n\tGet() int,\n}\n\n// Fixed is a Store of one count.\ntype Fixed { N int } implements Store\n\nfunc Get(f Fixed) int { f.N }", "\n")},
			"cmd/app/main.ergo": {lines: strings.Split("// Op runs.\ntype Op {\n\tRun() int,\n}\n\nfunc show() needs Console {\n\tConsole.Println(\"m\")\n}\n\nfunc main() needs Console {\n\tshow()\n}", "\n")},
		}
		for i := range 1 + r.IntN(3) {
			name := fmt.Sprintf("p%d", i)
			op, goimp, capab, console, tests := r.IntN(2) == 0, r.IntN(3), r.IntN(2) == 0, r.IntN(2) == 0, r.IntN(2) == 0
			f := &ownerFile{}
			files[name+"/"+name+".ergo"] = f
			switch goimp {
			case 1:
				f.add("import go unicode/utf8", "")
			case 2:
				f.add("import go os", map[bool]string{true: "foreign-outside-adapter"}[op])
			}
			if capab {
				f.add("import app/store", "")
			}
			f.add("", "")
			if op {
				f.add("// Op runs.", "")
				f.add("type Op {", "")
				f.add("\tRun() int,", "")
				f.add("}", "")
				f.add("", "")
			}
			switch goimp {
			case 1:
				f.add("func width() int { utf8.RuneLen(65) }", "")
				f.add("", "")
			case 2:
				f.add("func pid() int needs go.Foreign { os.Getpid() }", "")
				f.add("", "")
			}
			if console {
				f.add("func say() needs Console {", "")
				f.add("\tConsole.Println(\"x\")", map[bool]string{true: "console-outside-entry"}[op])
				f.add("}", "")
				f.add("", "")
			}
			if capab {
				f.add("func count() int needs store.Store {", "")
				f.add("\tstore.Store.Get()", map[bool]string{true: "capability-outside-owner"}[i != 0])
				f.add("}", "")
				f.add("", "")
			}
			f.add("func helper() int { 1 }", "")
			f.add("", "")
			f.add("func support() int { 4 }", "")
			f.add("", "")
			f.add("func Use() int { helper() + Help() }", "")
			f.add("", "")
			f.add("func Help() int { 2 }", "")
			if !tests {
				continue
			}
			tf := &ownerFile{}
			files[name+"/tests.ergo"] = tf
			tf.add("func local() int { 3 }", "")
			tf.add("", "")
			tf.add("func both() int { local() }", "")
			tf.add("", "")
			tf.add("func dbg() needs Console {", "")
			tf.add("\tConsole.Println(\"t\")", "")
			tf.add("}", "")
			for j := range 1 + r.IntN(4) {
				tf.add("", "")
				switch r.IntN(5) {
				case 0:
					tf.add(fmt.Sprintf("test T%d {", j), "")
					tf.add("\tassert helper() == 1", "test-reads-unexported")
				case 1:
					tf.add(fmt.Sprintf("test T%d {", j), "")
					tf.add("\tassert Help() == 2", "")
				case 2:
					tf.add(fmt.Sprintf("test T%d {", j), "")
					tf.add("\tassert local() == 3", "")
				case 4:
					tf.add(fmt.Sprintf("test T%d {", j), "")
					tf.add("\tassert support() == 4", "")
				case 3:
					tf.add(fmt.Sprintf("law L%d(n int) {", j), "")
					tf.add("\thelper() + n == 1 + n", "")
				}
				tf.add("}", "")
			}
		}
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
		if got := layoutFindings(t, dir, conf); !slices.Equal(got, want) {
			var src strings.Builder
			for _, name := range slices.Sorted(func(yield func(string) bool) {
				for k := range files {
					if !yield(k) {
						return
					}
				}
			}) {
				fmt.Fprintf(&src, "--- %s\n%s\n", name, strings.Join(files[name].lines, "\n"))
			}
			t.Fatalf("seed %d (replay: go test -run TestLayoutOwnersAgreeWithReference ./internal/sketch/lint -lint.seed=%d): got %v, want %v\n%s", seed, seed, got, want, src.String())
		}
	}
}
