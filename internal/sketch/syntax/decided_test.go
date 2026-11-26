package syntax_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// readDir reads one directory of the decided grammar's own fixtures (D7):
// testdata/decided holds one program per decided form, testdata/edge
// the edge forms, testdata/refuse one old spelling per file.
func readDir(t *testing.T, dir string) []sketchtest.Input {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.ergo"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no testdata/%s: %v", dir, err)
	}
	var out []sketchtest.Input
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sketchtest.Input{Name: dir + "/" + filepath.Base(p), Src: string(data)})
	}
	return out
}

// decidedInputs is every input that must parse under the decided grammar:
// sketchtest.Positive (the translated v0 corpus, the repository inputs, the
// spec fences and the grammar fixtures), the decided and edge fixtures, and
// the translated prototype fixtures that are not syntax refusals.
func decidedInputs(t *testing.T) []sketchtest.Input {
	t.Helper()
	inputs, err := sketchtest.Positive()
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, readDir(t, "decided")...)
	inputs = append(inputs, readDir(t, "edge")...)
	protos, _ := filepath.Glob("../testdata/*.ergo")
	more, _ := filepath.Glob("../testdata/*/*.ergo")
	// The shared corpus has 113 sources (stage 5): a wrong path reads none.
	if len(protos)+len(more) < 100 {
		t.Fatalf("found %d sources in the shared corpus ../testdata", len(protos)+len(more))
	}
	for _, p := range append(protos, more...) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(string(data), "// want S001") {
			continue
		}
		inputs = append(inputs, sketchtest.Input{Name: p, Src: string(data)})
	}
	return inputs
}

// TestDecidedFixtures: every decided and edge fixture parses and formats to
// a fixpoint; a decided fixture is already canonical, and an edge fixture's
// `// expect:` lines hold in its formatted output.
func TestDecidedFixtures(t *testing.T) {
	for _, dir := range []string{"decided", "edge"} {
		for _, in := range readDir(t, dir) {
			out, err := format.Source(in.Src)
			if err != nil {
				t.Errorf("%s: %v", in.Name, err)
				continue
			}
			if dir == "decided" && out != in.Src {
				t.Errorf("%s: not canonical:\n%s", in.Name, out)
			}
			if again, err := format.Source(out); err != nil || again != out {
				t.Errorf("%s: the output is not a fixpoint (%v)", in.Name, err)
			}
			for _, line := range strings.Split(in.Src, "\n") {
				if want, ok := strings.CutPrefix(line, "// expect: "); ok {
					want, err := strconv.Unquote(want)
					if err != nil {
						t.Fatalf("%s: %q: %v", in.Name, line, err)
					}
					if !strings.Contains(out, want) {
						t.Errorf("%s: output lacks %q", in.Name, want)
					}
				}
			}
		}
	}
}

// TestRefusedFixtures: each old spelling is refused with the code and line
// of its first line (`// want: CODE LINE TEXT`), and a Go habit's message
// holds TEXT (D2). A spelling the parser reads as a current form (the old
// `let W = wire {…}` is a composite literal of a type named wire since
// 196w) is refused by the checker instead, with that one diagnostic.
func TestRefusedFixtures(t *testing.T) {
	for _, in := range readDir(t, "refuse") {
		head, _, _ := strings.Cut(in.Src, "\n")
		fields := strings.SplitN(strings.TrimPrefix(head, "// want: "), " ", 3)
		if len(fields) != 3 {
			t.Fatalf("%s: bad header %q", in.Name, head)
		}
		var got []check.Diagnostic
		if _, err := syntax.Parse(in.Src); err == nil {
			_, got = check.CheckSource(in.Src)
		} else {
			var e *syntax.Error
			if !errors.As(err, &e) {
				t.Errorf("%s: got %v, want %s", in.Name, err, head)
				continue
			}
			got = []check.Diagnostic{{Code: e.Code, Message: e.Message, Line: e.Line}}
		}
		if len(got) != 1 {
			t.Errorf("%s: got %v, want only %s", in.Name, got, head)
			continue
		}
		if d := got[0]; d.Code != fields[0] || strconv.Itoa(d.Line) != fields[1] || fields[2] != "-" && !strings.Contains(d.Message, fields[2]) {
			t.Errorf("%s: got %v, want %s", in.Name, d, head)
		}
	}
}

// TestDecidedOnly: no positive input produces a shape that no decided form
// produces (D3, D7). The nodes and fields of retired forms are deleted
// (lower-v1 stage 5, D10), so the type system rules them out; this test
// keeps the shapes the tree can still express: a grouped import or let,
// a dot import, a := or several-sided assignment, and a bodiless function
// with no extern (D-S5-3).
func TestDecidedOnly(t *testing.T) {
	for _, in := range decidedInputs(t) {
		f, err := syntax.Parse(in.Src)
		if err != nil {
			t.Errorf("%s: %v", in.Name, err)
			continue
		}
		syntax.Inspect(f, func(n syntax.Node) bool {
			if why := undecided(n); why != "" {
				line, col := f.Position(n.Pos())
				t.Errorf("%s:%d:%d: %s", in.Name, line, col, why)
			}
			return true
		})
	}
}

// undecided names the undecided node or field n holds, or returns "".
func undecided(n syntax.Node) string {
	switch n := n.(type) {
	case *syntax.FuncDecl:
		if n.Body == nil && n.Extern == nil {
			return "a bodiless FuncDecl without extern"
		}
	case *syntax.ImportDecl:
		if len(n.Specs) != 1 {
			return "a grouped ImportDecl"
		}
		if n.Specs[0].Kind == syntax.ImportDot {
			return "ImportDot"
		}
	case *syntax.VarDecl:
		if len(n.Specs) != 1 {
			return "a grouped VarDecl"
		}
	case *syntax.AssignStmt:
		if n.Tok == syntax.DEFINE || len(n.Lhs) != 1 || len(n.Rhs) != 1 {
			return "an AssignStmt with := or several sides"
		}
	case *syntax.RangeStmt:
		if n.Tok != syntax.DEFINE {
			return "a RangeStmt whose token is not :="
		}
	case *syntax.UnaryExpr:
		if n.Op == syntax.ARROW {
			return "a receive"
		}
	}
	return ""
}

// TestGrammarStatesTheFixtures: the syntactic productions of
// docs/spec/grammar.md are exactly the names of the grammar fixtures
// (sketchtest.GrammarFixtures, one or more per production; a lowercase
// name is a lexical form, not a production), so the specification and the
// parser's inputs cannot drift.
func TestGrammarStatesTheFixtures(t *testing.T) {
	data, err := os.ReadFile("../../../docs/spec/grammar.md")
	if err != nil {
		t.Fatal(err)
	}
	ebnf := regexp.MustCompile("(?s)```ebnf\n(.*?)\n```")
	production := regexp.MustCompile(`(?m)^([A-Z][A-Za-z]*) *=`)
	spec := map[string]bool{}
	for _, block := range ebnf.FindAllStringSubmatch(string(data), -1) {
		for _, m := range production.FindAllStringSubmatch(block[1], -1) {
			if spec[m[1]] {
				t.Errorf("grammar.md states %s twice", m[1])
			}
			spec[m[1]] = true
		}
	}
	fixtures := map[string]bool{}
	for _, g := range sketchtest.GrammarFixtures {
		if g.Name[0] >= 'A' && g.Name[0] <= 'Z' {
			fixtures[g.Name] = true
		}
	}
	for name := range fixtures {
		if !spec[name] {
			t.Errorf("a grammar fixture is named %s, which grammar.md does not state", name)
		}
	}
	for name := range spec {
		if !fixtures[name] {
			t.Errorf("grammar.md states %s, which has no grammar fixture", name)
		}
	}
}

// TestGrammarFixtures: every grammar fixture parses and is canonical.
func TestGrammarFixtures(t *testing.T) {
	for _, g := range sketchtest.GrammarFixtures {
		out, err := format.Source(g.Src)
		if err != nil {
			t.Errorf("%s: %v", g.Name, err)
			continue
		}
		if out != g.Src {
			t.Errorf("%s: not canonical:\n%s", g.Name, out)
		}
	}
}
