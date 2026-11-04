package syntax_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/migrate196"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The decided grammar's own fixtures (#196, D7): testdata/decided holds the
// preview's Decided blocks, testdata/edge the edge forms, testdata/refuse
// one old spelling per file. They are copies of the acceptance inputs of
// the parse-v1 unit.
const previewPath = "../../../docs/previews/2026-10-28-without-go-syntax.md"

// previewRenames are the later renames of the preview's names: the preview
// is recorded history and keeps its spelling, so the tests read it through
// these (196w, docs/previews/2026-11-02-wire-to-context.md).
var previewRenames = map[string]string{
	"WireBody": "ContextBody", "WireEntry": "ContextEntry", "WireLit": "ContextLit",
	"wire": "context",
}

var previewWord = regexp.MustCompile(`[A-Za-z]+`)

// previewRow is a capability row in the preview's comma spelling. The
// preview keeps it as recorded; the tests read it in the pipe spelling
// (196n). Every row of the preview is a declaration's, so its commas are
// the row's.
var previewRow = regexp.MustCompile(`\b(needs|provides) [A-Za-z_][A-Za-z0-9_.]*(, [A-Za-z_][A-Za-z0-9_.]*)+`)

// readPreview returns the preview's text with previewRenames and the pipe
// spelling of rows applied.
func readPreview(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(previewPath)
	if err != nil {
		t.Fatal(err)
	}
	text := previewWord.ReplaceAllStringFunc(string(data), func(w string) string {
		if r, ok := previewRenames[w]; ok {
			return r
		}
		return w
	})
	return previewRow.ReplaceAllStringFunc(text, func(row string) string {
		return strings.ReplaceAll(row, ", ", " | ")
	})
}

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

// oldInputs is the old domain of TestOldCorpusRefusedOrSame: the frozen
// old inputs of the translator (the v0 corpus, the prototype fixtures and
// the repository inputs at 92fac236), the superseded corpus, and hand-made
// old files aimed at the spellings that the decided grammar also parses
// with another meaning (risk 1 of the parse-v1 spec).
func oldInputs(t *testing.T) []sketchtest.Input {
	t.Helper()
	var inputs []sketchtest.Input
	root := "../../migrate196/testdata/old"
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sketch") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		inputs = append(inputs, sketchtest.Input{Name: path, Src: string(data)})
		return nil
	})
	if err != nil || len(inputs) < 140 {
		t.Fatalf("frozen old inputs: %d, %v", len(inputs), err)
	}
	_, superseded, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, superseded...)
	for i, src := range handMadeOld {
		inputs = append(inputs, sketchtest.Input{Name: "hand-made/" + strconv.Itoa(i), Src: src})
	}
	return inputs
}

// handMadeOld are old files whose spellings come closest to a decided form:
// each must be refused, or parse to exactly its translation.
var handMadeOld = []string{
	"package p\n\nfunc f(x int64, y int64) bool {\n\treturn x <-y\n}\n",
	"package p\n\nfunc f(x int64) int64 {\n\tx--\n\treturn x\n}\n",
	"package p\n\nfunc f() (Order, error) {\n\treturn Order{}\n}\n",
	"package p\n\nfunc f() needs Console {\n\twith(W) {\n\t\tConsole.Println(\"x\")\n\t}\n}\n",
	"package p\n\nfunc f(xs []int64) {\n\tfor i := range xs {\n\t\tuse(i)\n\t}\n}\n",
	"package p\n\nfunc f() {\n\tif x := g(); x > 0 {\n\t}\n}\n",
	"package p\n\nfunc f(v Value) {\n\tswitch v.(type) {\n\tdefault:\n\t}\n}\n",
	"package p\n\nfunc f() {\n\tvar x = 1\n\tuse(x)\n}\n",
	"package p\n\nfunc f(x int64) int64 {\n\tswitch x {\n\tcase 1:\n\t\treturn 2\n\tdefault:\n\t\treturn 3\n\t}\n}\n",
	"package p\n\nfunc f() {\n\tx := 1\n\tuse(x)\n}\n",
	"package p\n\ntype T struct {\n\tA int64\n}\n",
	"package p\n\ntype S interface {\n\tGet() int64\n}\n",
	"package p\n\nfunc (t T) Get() int64 {\n\treturn 1\n}\n",
	"package p\n\nfunc f(m map[string]int64) {}\n",
	"package p\n\nfunc f() {\n\tgo work()\n}\n",
	"package p\n\nfunc f() {\n\tdefer done()\n}\n",
	"package p\n\nwire W {\n\tClock = Fixed{}\n}\n",
	"package p\n\nfunc f() {\n\tg() catch e {\n\tcase E:\n\t\tuse(e)\n\t}\n}\n",
	"package p\n\nimport \"strings\"\n",
	"package p\n\nimport (\n\t\"strings\"\n)\n",
	"package p\n\nfunc f() error[NotFound] {\n\treturn NotFound{}\n}\n",
	"package p\n\nfunc f() {\n\tfor i := 0; i < 3; i++ {\n\t}\n}\n",
	"package p\n\nfunc f() uses Clock {}\n",
	"package p\n\nfunc f(xs ...int64) {}\n",
	"package p\n\nfunc f() {\n\tx := [3]int64{1, 2, 3}\n\tuse(x)\n}\n",
	"package p\n\nfunc f(x int64) int64 {\n\treturn x\n}\n",
}

// dropBlankLines removes the blank lines of src: removing the package
// clause can join two comment groups that the translation keeps apart, and
// a blank line is never meaning.
func dropBlankLines(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// printlnOnly reports whether two texts of the same lines differ only on
// lines that call Console.Println, and which of a's Println lines differ,
// by their order among a's Println lines (0 for the first).
func printlnOnly(a, b string) (ordinals []int, ok bool) {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return nil, false
	}
	k := -1
	for i := range la {
		println := strings.Contains(la[i], "Console.Println(")
		if println {
			k++
		}
		if la[i] == lb[i] {
			continue
		}
		if !println || !strings.Contains(lb[i], "Console.Println(") {
			return nil, false
		}
		ordinals = append(ordinals, k)
	}
	return ordinals, true
}

// printlnLines returns the line of each Console.Println call of src, in
// source order.
func printlnLines(t *testing.T, src string) []int {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	syntax.Inspect(f, func(n syntax.Node) bool {
		if sel, ok := n.(*syntax.SelectorExpr); ok && sel.Sel.Name == "Println" {
			if id, ok := sel.X.(*syntax.Ident); ok && id.Name == "Console" {
				lines = append(lines, 1+strings.Count(src[:sel.Pos()], "\n"))
			}
		}
		return true
	})
	return lines
}

// oldContextLits returns the line of each composite literal of src whose
// type is the bare name wire, the old inline context, in source order.
func oldContextLits(t *testing.T, src string) []int {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var lines []int
	syntax.Inspect(f, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.CompositeLit); ok {
			if id, ok := lit.Type.(*syntax.Ident); ok && id.Name == "wire" {
				lines = append(lines, 1+strings.Count(src[:lit.Pos()], "\n"))
			}
		}
		return true
	})
	return lines
}

// oldContextOnly reports whether two texts of the same lines differ only on
// lines where a writes an old inline context and b its translation.
func oldContextOnly(a, b string) bool {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if la[i] != lb[i] && (!strings.Contains(la[i], "wire{") || !strings.Contains(lb[i], "context {")) {
			return false
		}
	}
	return true
}

// diagnosedLines reports whether the checker reports a diagnostic on each
// of the given lines of src.
func diagnosedLines(src string, lines []int) bool {
	_, diags := check.CheckSource(src)
	for _, line := range lines {
		found := false
		for _, d := range diags {
			if d.Line == line {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// packageClause is an old file's package clause line.
var packageClause = regexp.MustCompile(`(?m)^package [A-Za-z_][A-Za-z_0-9]*[ \t]*\n(\n)?`)

// TestOldCorpusRefusedOrSame: for every old input, the decided parser
// either refuses it with a located S001, or accepts it and its canonical
// form equals its translation: an old file that still parses means what its
// translation means (D7; Done rule 1, "nothing else"). Every old file
// starts with a package clause, which alone refuses it; so the property is
// checked a second time on the file without that line, where the rest of
// its spellings decide. One difference is admitted: the decided prelude's
// Console.Println takes one line string (round 6, A3), so the translator
// rewrites an old call with any other operands; the old call still parses,
// and the checker refuses it with a diagnostic on that call's line, so it
// never runs with another meaning. A second is the old inline context
// `wire{…}`: since 196w the old keyword is an identifier, so it parses as a
// composite literal of a type named wire, and the checker refuses it on its
// line.
func TestOldCorpusRefusedOrSame(t *testing.T) {
	refused, same, checked, oldLits := 0, 0, 0, 0
	for _, in := range oldInputs(t) {
		for _, src := range []string{in.Src, packageClause.ReplaceAllString(in.Src, "")} {
			_, err := syntax.Parse(src)
			if err != nil {
				var e *syntax.Error
				if !errors.As(err, &e) || e.Code != syntax.CodeSyntax || e.Line < 1 || e.Offset < 0 || e.Offset > len(src) {
					t.Errorf("%s: %v is not a located S001", in.Name, err)
				}
				refused++
				continue
			}
			got, err := format.Source(src)
			if err != nil {
				t.Errorf("%s: parses but does not format: %v", in.Name, err)
				continue
			}
			want, notes, err := migrate196.Translate(in.Src)
			if err != nil || len(notes) > 0 {
				t.Errorf("%s: parses as decided syntax, but the translator gives %v %v", in.Name, err, notes)
				continue
			}
			if dropBlankLines(got) != dropBlankLines(want) {
				if ordinals, ok := printlnOnly(dropBlankLines(got), dropBlankLines(want)); ok {
					// Each differing call is refused on its own line.
					calls := printlnLines(t, src)
					var lines []int
					for _, k := range ordinals {
						if k >= 0 && k < len(calls) {
							lines = append(lines, calls[k])
						}
					}
					if len(lines) == len(ordinals) && diagnosedLines(src, lines) {
						checked++
						continue
					}
				}
				if lines := oldContextLits(t, src); len(lines) > 0 && oldContextOnly(dropBlankLines(got), dropBlankLines(want)) && diagnosedLines(src, lines) {
					oldLits++
					continue
				}
				t.Errorf("%s: parses as decided syntax with another meaning:\nformatted:\n%s\ntranslated:\n%s", in.Name, got, want)
				continue
			}
			same++
		}
	}
	t.Logf("%d refused, %d parse to their translation, %d Println calls and %d old inline contexts refused by the checker", refused, same, checked, oldLits)
	if same == 0 {
		t.Error("no old input parsed: the property was never exercised")
	}
}

// previewDecidedBlocks extracts the go block that follows each **Decided.**
// paragraph of the preview, with the heading it is under.
func previewDecidedBlocks(t *testing.T) map[string]string {
	t.Helper()
	blocks := map[string]string{}
	heading := ""
	lines := strings.Split(readPreview(t), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "### ") {
			heading = strings.TrimPrefix(lines[i], "### ")
		}
		if !strings.HasPrefix(lines[i], "**Decided.**") {
			continue
		}
		for i++; i < len(lines) && lines[i] != "```go"; i++ {
			if strings.HasPrefix(lines[i], "### ") || strings.HasPrefix(lines[i], "**") {
				t.Fatalf("%s: a Decided paragraph without a go block", heading)
			}
		}
		var body []string
		for i++; i < len(lines) && lines[i] != "```"; i++ {
			body = append(body, lines[i])
		}
		blocks[heading] = strings.Join(body, "\n") + "\n"
	}
	return blocks
}

// commentGap is the run of blanks before a trailing line comment.
var commentGap = regexp.MustCompile(`[ \t]+//`)

// TestPreviewDecidedBlocksCanonical: every Decided block of the preview
// parses, and the formatter returns it unchanged up to the alignment of
// trailing comments (the preview misaligns one comment; D4). The signature
// block is a list of bodiless lines, not a program, and is checked through
// its adapted fixture testdata/decided/signatures.ergo.
func TestPreviewDecidedBlocksCanonical(t *testing.T) {
	blocks := previewDecidedBlocks(t)
	if len(blocks) != 13 {
		t.Fatalf("%d Decided blocks, want 13", len(blocks))
	}
	for heading, src := range blocks {
		if heading == "A signature with failures and services" {
			continue
		}
		out, err := format.Source(src)
		if err != nil {
			t.Errorf("%s: %v", heading, err)
			continue
		}
		if commentGap.ReplaceAllString(out, " //") != commentGap.ReplaceAllString(src, " //") {
			t.Errorf("%s: not canonical:\n%s", heading, out)
		}
	}
}

// TestGrammarStatesTheSketch: the syntactic productions of
// docs/spec/grammar.md are the 62 of the preview's decided sketch plus
// PatternCore (D1), so the specification and the sketch cannot drift.
func TestGrammarStatesTheSketch(t *testing.T) {
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
	sketch := append(sketchProductions(t), "PatternCore")
	for _, name := range sketch {
		if !spec[name] {
			t.Errorf("grammar.md has no production %s", name)
		}
		delete(spec, name)
	}
	for name := range spec {
		t.Errorf("grammar.md states %s, which the decided sketch does not have", name)
	}
}

// sketchProductions returns the production names of the preview's decided
// sketch, in order.
func sketchProductions(t *testing.T) []string {
	t.Helper()
	text := readPreview(t)
	start := strings.Index(text, "<!-- count: grammar/decided -->")
	if start < 0 {
		t.Fatal("no decided sketch in the preview")
	}
	text = text[start:]
	open := strings.Index(text, "```ebnf\n")
	close := strings.Index(text[open+8:], "\n```")
	production := regexp.MustCompile(`(?m)^([A-Za-z]+) *=`)
	var names []string
	for _, m := range production.FindAllStringSubmatch(text[open+8:open+8+close], -1) {
		names = append(names, m[1])
	}
	if len(names) != 62 {
		t.Fatalf("%d productions in the decided sketch, want 62", len(names))
	}
	return names
}

// TestGrammarFixtures: every production of the preview's decided sketch,
// and PatternCore, has a grammar fixture named after it; each fixture
// parses and is canonical.
func TestGrammarFixtures(t *testing.T) {
	names := append(sketchProductions(t), "PatternCore")
	have := map[string]bool{}
	for _, g := range sketchtest.GrammarFixtures {
		have[g.Name] = true
		out, err := format.Source(g.Src)
		if err != nil {
			t.Errorf("%s: %v", g.Name, err)
			continue
		}
		if out != g.Src {
			t.Errorf("%s: not canonical:\n%s", g.Name, out)
		}
	}
	for _, name := range names {
		if !have[name] {
			t.Errorf("production %s has no grammar fixture", name)
		}
	}
}
