package format

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// check asserts the formatter's properties on one parseable input: the
// output parses, formatting it again changes nothing, comments and tokens
// are kept (modulo semicolons and a comma before a closer), and whitespace
// perturbations format to the same text. It returns the formatted text.
func check(t *testing.T, name, src string, perturbations uint64) string {
	t.Helper()
	out, err := Source(src)
	if err != nil {
		t.Errorf("%s: %v", name, err)
		return ""
	}
	again, err := Source(out)
	if err != nil {
		t.Errorf("%s: the output does not parse: %v\n%s", name, err, out)
		return out
	}
	if again != out {
		t.Errorf("%s: not idempotent at line %d\n--- first\n%s\n--- second\n%s", name, firstDiffLine(out, again), out, again)
	}
	if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") || strings.HasPrefix(out, "\n") {
		t.Errorf("%s: output does not end with exactly one newline or starts with a blank line", name)
	}
	for i, line := range strings.Split(out, "\n") {
		if strings.TrimRight(line, " \t") != line && !strings.Contains(line, "`") {
			t.Errorf("%s: trailing blanks on output line %d: %q", name, i+1, line)
			break
		}
	}
	if msg := contracts(src, out); msg != "" {
		t.Errorf("%s: %s\n--- output\n%s", name, msg, out)
	}
	_, ok1 := sketchtest.Comments(src)
	for seed := uint64(1); seed <= perturbations && ok1; seed++ {
		p, _ := sketchtest.Perturb(src, seed)
		got, err := Source(p)
		if err != nil {
			t.Errorf("%s: perturbation %d does not parse: %v", name, seed, err)
			break
		}
		if got != out {
			t.Errorf("%s: perturbation %d formats differently at line %d\n--- want\n%s\n--- got\n%s", name, seed, firstDiffLine(out, got), out, got)
			break
		}
	}
	return out
}

// contracts checks what formatting keeps between a parseable source and its
// formatted text, and describes the first broken one: the tree (without
// positions and comments) is the same, and every comment stays between the
// same two tokens, in order.
func contracts(src, out string) string {
	f1, err := syntax.Parse(src)
	if err != nil {
		return "the input does not parse: " + err.Error()
	}
	f2, err := syntax.Parse(out)
	if err != nil {
		return "the output does not parse: " + err.Error()
	}
	if a, b := treeShape(f1), treeShape(f2); a != b {
		i := 0
		for i < len(a) && i < len(b) && a[i] == b[i] {
			i++
		}
		lo := max(0, i-80)
		return fmt.Sprintf("the tree changed at\n  in:  …%s\n  out: …%s", a[lo:min(len(a), i+80)], b[lo:min(len(b), i+80)])
	}
	s1, ok1 := sketchtest.Interleaved(src)
	s2, ok2 := sketchtest.Interleaved(out)
	if ok1 && ok2 {
		if i, a, b := sketchtest.FirstDiff(s1, s2); i >= 0 {
			return fmt.Sprintf("token or comment %d changed: %q became %q", i+1, a, b)
		}
	}
	return ""
}

func firstDiffLine(a, b string) int {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		if i >= len(la) || i >= len(lb) || la[i] != lb[i] {
			return i + 1
		}
	}
	return 0
}

// TestFormatIdempotent (D7): over every positive input, the decided and
// edge fixtures, and whitespace perturbations of each, the output parses,
// keeps the tree, the tokens and the comments, and formats to itself.
func TestFormatIdempotent(t *testing.T) {
	inputs, err := sketchtest.Positive()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"decided", "edge"} {
		paths, _ := filepath.Glob(filepath.Join("..", "syntax", "testdata", dir, "*.ergo"))
		if len(paths) == 0 {
			t.Fatalf("no syntax/testdata/%s fixtures", dir)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			inputs = append(inputs, sketchtest.Input{Name: dir + "/" + filepath.Base(p), Src: string(data)})
		}
	}
	for _, in := range inputs {
		check(t, in.Name, in.Src, 5)
	}
}

func TestSupersededInputs(t *testing.T) {
	_, superseded, err := sketchtest.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range superseded {
		if _, err := syntax.Parse(in.Src); err == nil {
			check(t, in.Name, in.Src, 2)
		}
	}
}

// Comments retain their place at every import and extern token boundary.
func TestImportPathComments(t *testing.T) {
	for _, src := range []string{
		"import net/http // note\n",
		"import go net/http /* note */\n",
		"import alias /* note */ example.com/json\n",
		"import alias/* note */example.com/json\n",
		"import/* note */net/http\n",
		"import go strings// note\n",
		"import go alias net/http/* note */\n",
		"extern go net/http.Get/* note */ func Get()\n",
		"extern go net/http.Get /* note */ func Get()\n",
	} {
		check(t, "path comments", src, 3)
	}
}

// Assert the independent token oracle as well as the formatter: a path
// ends before either comment opener, with or without separating blanks.
func TestImportCommentSpacing(t *testing.T) {
	for _, c := range []struct{ src, want, comment string }{
		{"import strings// note", "import strings // note\n", "// note"},
		{"import strings/* c */", "import strings /* c */\n", "/* c */"},
		{"import strings /* c */", "import strings /* c */\n", "/* c */"},
	} {
		got, ok := sketchtest.Interleaved(c.src)
		if !ok || strings.Join(got, "\n") != "import\nstrings\ncomment "+c.comment {
			t.Errorf("%q: token oracle got %q (ok %v), want a path then a comment", c.src, got, ok)
		}
		if out := check(t, "import comment spacing", c.src, 3); out != c.want {
			t.Errorf("%q: formatted %q, want %q", c.src, out, c.want)
		}
	}
}

// TestCommentPositions: comments in every position keep their order, and
// the layout is idempotent and canonical (spec risk 4).
func TestCommentPositions(t *testing.T) {
	src := `// file doc
// after the clause

/* block
   comment */

// before a spec
import a // after a spec
/* inline */ import b
// before the paren

type T { // after the brace
	// before a field
	a int, /* mid */ // end
	b string,
	// before the brace
}

type U = A | B

// doc
extern go net/url.Parse func parse[E](raw string) url.URL fails go.Error | E needs go.Foreign

func f(a /* first */ int, // after a
	b int, /* after b */
) ( /* results */ int) {
	let x = g( // after the paren
		1, /* one */
		// own line in the arguments
		2,
	)
	if x { // after if
		y()
	} /* between */ else { // after else
		z()
	}
	match x { // after switch
	// before the first case
	case A:
		a()
	// still A

	// about B
	case B:
	// still B, empty
	case _:
		d()
		// end of default

	// before the closing brace
	}
	let r = try h() catch {
	case E1 as e: // after the case
		log(e)
		// end of the arm
	}
	let w = context { /* only */ A: X{} }
	return 1 /* after the result */
	// end of the body
}
// end of file
`
	// The layout is pinned: each comment stays between the same two tokens
	// (`A /* x */ | B` keeps its comment before the `|`), an own-line
	// comment takes its block's indentation, and a comment between a
	// clause's last statement and the next case belongs to the clause
	// above unless a blank line separates them.
	want := strings.Replace(strings.Replace(src,
		"\t\ta()\n\t// still A\n", "\t\ta()\n\t\t// still A\n", 1),
		"\tcase B:\n\t// still B, empty\n", "\tcase B:\n\t\t// still B, empty\n", 1)
	if out := check(t, "comments", src, 8); out != want {
		t.Errorf("layout at line %d\n--- got\n%s\n--- want\n%s", firstDiffLine(out, want), out, want)
	}
}

// TestOutputBound (review M2): one long field name among many short ones
// starts its own alignment section, so the output stays near the input's
// size; and a bound below the output is refused before the text is built,
// so the allocation stays near the input's size too.
func TestOutputBound(t *testing.T) {
	src := "type Big {\n\t" + strings.Repeat("L", 4096) + " int64,\n" + strings.Repeat("\tx int64,\n", 2000) + "}\n"
	out, err := SourceBounded(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > len(src)+len(src)/10 {
		t.Fatalf("output %d bytes for a %d-byte input: the long name pads its neighbours", len(out), len(src))
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = SourceBounded(src, 1024)
	runtime.ReadMemStats(&after)
	var limit *LimitError
	if !errors.As(err, &limit) || limit.Limit != 1024 {
		t.Fatalf("bound 1024 gave %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 200*uint64(len(src)) {
		t.Fatalf("a refused %d-byte input allocated %d bytes", len(src), allocated)
	}
	// An exact bound is not a refusal.
	if exact, err := SourceBounded(src, len(out)); err != nil || exact != out {
		t.Fatalf("bound equal to the output: %v", err)
	}
	// Review r2 M1: a short cell never inherits a long cell's width, for
	// every aligned column: record fields with trailing comments, keyed
	// literals, context entries, and trailing comments of consecutive lets.
	long := strings.Repeat("L", 4096)
	for _, g := range []struct{ name, src string }{
		{"trailing comments after a long field", "type T {\n\t" + long + " int, // c\n" + strings.Repeat("\tx int, // c\n", 2000) + "}\n"},
		{"keyed literal with a long key", "let m = M{\n\t" + long + ": 1,\n" + strings.Repeat("\tb: 2,\n", 2000) + "}\n"},
		{"context with a long key", "let W = context {\n\t" + long + ": X{},\n" + strings.Repeat("\tb: Y{},\n", 2000) + "}\n"},
		{"lets after a long value", "let a = " + long + "{} // c\n" + strings.Repeat("let b = 2 // c\n", 2000)},
		{"lets before a long value", strings.Repeat("let b = 2 // c\n", 2000) + "let a = " + long + "{} // c\n"},
	} {
		out, err := Source(g.src)
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		if len(out) > len(g.src)+len(g.src)/10 {
			t.Errorf("%s: output %d bytes for a %d-byte input", g.name, len(out), len(g.src))
		}
	}
	// Consecutive lets are separate statements: their values never align.
	got, _ := Source("let a int = 1\nlet b = 2\nlet ccccc string = \"x\"\n")
	if want := "let a int = 1\nlet b = 2\nlet ccccc string = \"x\"\n"; got != want {
		t.Fatalf("lets\n%s", got)
	}
	// Aligned columns of similar widths still align (gofmt).
	got, _ = Source("type T {\n\tshort int,\n\tsomewhatLongerName string,\n}\n")
	if want := "type T {\n\tshort              int,\n\tsomewhatLongerName string,\n}\n"; got != want {
		t.Fatalf("alignment\n%s", got)
	}
}

// TestMalformedIsRefused: a malformed input gives no text and its
// registered code.
func TestMalformedIsRefused(t *testing.T) {
	for _, m := range sketchtest.MalformedTable {
		src := m.Src
		if m.Generate != nil {
			src = m.Generate()
		}
		code := m.Code
		if code == "" {
			code = syntax.CodeSyntax
		}
		out, err := Source(src)
		var e *syntax.Error
		if out != "" || !errors.As(err, &e) || e.Code != code {
			t.Errorf("%s: got %q, %v; want no text and %s", m.Name, out, err, code)
		}
	}
}

// TestLayout pins the canonical layout of constructs and comment positions,
// and the decided normalisations (D4): the leading `|` of a union exactly
// when it spans several lines, `with (W)` as `with W`, `match (x)` as
// `match x`.
func TestLayout(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{
			"gofmt layout",
			"\n\n\nimport  a\nimport b   c\n  import go d\nfunc f( a,b int,\n) (int, string) fails A|B needs X | Y { return a*b+1 }\n",
			"import a\nimport b c\nimport go d\nfunc f(a, b int,\n) (int, string) fails A | B needs X | Y { return a*b + 1 }\n",
		},
		{
			"explicit semicolons become line breaks",
			"func main() {\n\tf(); g()\n}\n",
			"func main() {\n\tf()\n\tg()\n}\n",
		},
		{
			"alignment",
			"type T {\n\tn int64, // count\n\tlonger string,\n}\n\nlet first = \"a\"\nlet secondName = \"b\"\n\nlet W = context {\n\tA: X{},\n\tLonger: Y{},\n}\n\ntype M machine[S, E] {\n\tinitial: Idle{},\n\tstep: f,\n}\n\nlet routes = R{\n\t\"a\": x,\n\t\"long\": y,\n}\n",
			"type T {\n\tn      int64, // count\n\tlonger string,\n}\n\nlet first = \"a\"\nlet secondName = \"b\"\n\nlet W = context {\n\tA:      X{},\n\tLonger: Y{},\n}\n\ntype M machine[S, E] {\n\tinitial: Idle{},\n\tstep:    f,\n}\n\nlet routes = R{\n\t\"a\":    x,\n\t\"long\": y,\n}\n",
		},
		{
			"comments take the block's indentation",
			"func f() {\n        x()\n  // before the brace\n}\n",
			"func f() {\n\tx()\n\t// before the brace\n}\n",
		},
		{
			"comment before case: blank line decides the clause",
			"func f(v int64) {\n\tmatch v {\n\tcase 1:\n\t\ta()\n\t// still case 1\n\n\t// about case 2\n\tcase 2:\n\t\tb()\n\t\t// end of case 2\n\t}\n}\n",
			"func f(v int64) {\n\tmatch v {\n\tcase 1:\n\t\ta()\n\t\t// still case 1\n\n\t// about case 2\n\tcase 2:\n\t\tb()\n\t\t// end of case 2\n\t}\n}\n",
		},
		{
			"catch arms are clauses",
			"func f() {\n\tlet r = g() catch {\n\t\tcase A as e: log(e)\n\t\t\tStatus(1)\n\t}\n}\n",
			"func f() {\n\tlet r = g() catch {\n\tcase A as e:\n\t\tlog(e)\n\t\tStatus(1)\n\t}\n}\n",
		},
		{
			"short one-line blocks stay on one line",
			"func f() { if x { return } }\nfunc g() {}\nfunc h(c bool) int64 {\n\tif c { 1 } else { 2 }\n}\n",
			"func f() { if x { return } }\nfunc g() {}\nfunc h(c bool) int64 {\n\tif c { 1 } else { 2 }\n}\n",
		},
		{
			"continuation lines indent once",
			"func f() {\n\tlet x = a &&\nb ||\nc\n\tg(1,\n2)\n}\n",
			"func f() {\n\tlet x = a &&\n\t\tb ||\n\t\tc\n\tg(1,\n\t\t2)\n}\n",
		},
		{
			"raw strings stay",
			"func f() {\n\tlet s = `a\n  b`\n\tuse(s)\n}\n",
			"func f() {\n\tlet s = `a\n  b`\n\tuse(s)\n}\n",
		},
		{
			"CRLF input gives LF output",
			"// c\r\nfunc f() {}\r\n",
			"// c\nfunc f() {}\n",
		},
		{
			"extern stays on one line",
			"extern  go net/url.Parse   func parse(raw string) url.URL fails go.Error needs go.Foreign\n",
			"extern go net/url.Parse func parse(raw string) url.URL fails go.Error needs go.Foreign\n",
		},
		{
			// Review M1: a comment inside a delimiter stays before its
			// closer, so no semicolon is inserted after the closer.
			"comments stay inside delimiters",
			"let xs [ // c\n]int\nlet ys [ /* c\n d */ ]int\nlet s = a /* x */ .b\n",
			"let xs [ // c\n]int\nlet ys [ /* c\n d */ ]int\nlet s = a /* x */ .b\n",
		},
		{
			// go/printer: no blank between a block comment and `,`, nor `)`
			// unless the comment follows the opener.
			"block comment before a comma or closer",
			"let x = f(a /* x */, b)\nlet y = f( /* y */ )\nlet z = f(a /* z */)\n",
			"let x = f(a /* x */, b)\nlet y = f( /* y */ )\nlet z = f(a /* z */)\n",
		},
		{
			// Round-2 review O1: the `)` that closes an interpolation
			// follows the same rule.
			"block comment before an interpolation closer",
			"let s = \"\\(a /* c */) and \\(b /* d */)!\"\n",
			"let s = \"\\(a /* c */) and \\(b /* d */)!\"\n",
		},
		{
			"grouped names keep their line breaks",
			"func f(a,\nb int) {}\n\ntype T {\n\tx,\ny int,\n}\n",
			"func f(a,\n\tb int) {}\n\ntype T {\n\tx,\n\ty int,\n}\n",
		},
		{
			// Review r2 m1: grouped names take the enclosing list's or
			// block's continuation level, and one of their own only where
			// none exists.
			"grouped names take one continuation level",
			"func f(a,\nb int,\nc,\nd string) {}\nfunc g(\na,\nb int,\nc,\nd string,\n) {}\n",
			"func f(a,\n\tb int,\n\tc,\n\td string) {}\nfunc g(\n\ta,\n\tb int,\n\tc,\n\td string,\n) {}\n",
		},
		{
			// Review r3 m1: a list indents at its first line break, as
			// go/printer's parameters and exprList do, so a nested
			// parameter list or a multi-line item before that break keeps
			// the level the list started at.
			"lists indent at their first line break",
			"func f(a,\n\tb func(x,\n\ty int),\n\tc,\n\td int) {}\nfunc f4(\n\ta func(x,\n\t\ty int),\n) {}\nfunc f7(a,\n\tb func() func(x,\n\ty int),\n\tc int) {}\nfunc g() {\n\th(a,\n\t\tfunc(x,\n\t\t\ty int) {\n\t\t\tk(p,\n\t\t\t\tq)\n\t\t},\n\t\tb)\n\tf(T{\n\t\tA: 1,\n\t},\n\t\tb)\n}\n",
			"func f(a,\n\tb func(x,\n\ty int),\n\tc,\n\td int) {}\nfunc f4(\n\ta func(x,\n\t\ty int),\n) {}\nfunc f7(a,\n\tb func() func(x,\n\ty int),\n\tc int) {}\nfunc g() {\n\th(a,\n\t\tfunc(x,\n\t\t\ty int) {\n\t\t\tk(p,\n\t\t\t\tq)\n\t\t},\n\t\tb)\n\tf(T{\n\t\tA: 1,\n\t},\n\t\tb)\n}\n",
		},
		{
			"header parentheses go",
			"func f() {\n\tmatch (x) {\n\tcase _:\n\t}\n\twith (W) {\n\t\tg()\n\t}\n\tmatch (a, b) {\n\tcase (_, _):\n\t}\n}\n",
			"func f() {\n\tmatch x {\n\tcase _:\n\t}\n\twith W {\n\t\tg()\n\t}\n\tmatch (a, b) {\n\tcase (_, _):\n\t}\n}\n",
		},
		{
			"a union's leading bar exactly when it spans lines",
			"type U =\n\tA\n\t| B\n\ntype V = A |\n\tB\n\ntype W =\n\t| C\n\t| D\n\ntype X = E | F\n",
			"type U =\n\t| A\n\t| B\n\ntype V =\n\t| A\n\t| B\n\ntype W =\n\t| C\n\t| D\n\ntype X = E | F\n",
		},
		{
			"unary operators never merge",
			"let x = - -y\nlet z = a - -b\n",
			"let x = - -y\nlet z = a - -b\n",
		},
	}
	for _, c := range cases {
		got := check(t, c.name, c.src, 3)
		if got != c.want {
			t.Errorf("%s:\n--- got\n%s--- want\n%s", c.name, got, c.want)
		}
	}
}
