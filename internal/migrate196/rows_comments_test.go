package migrate196

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/migrate196/oldsyntax"
	"ergo.local/prototype/internal/sketch/syntax"
)

// rowSite is a place an old capability row stands: the row's tokens go
// where the template says ROW.
type rowSite struct {
	name, template string
	keyword        string // needs or provides
	parens         bool   // the old parenthesised form `needs (A, B)`
}

var rowSites = []rowSite{
	{"function needs", "func F() ROW {}\n", "needs", false},
	{"function needs, parenthesised", "func F() ROW {}\n", "needs", true},
	{"parameter needs", "func F(f func() ROW) {}\n", "needs", false},
	{"parameter needs, parenthesised", "func F(f func() ROW) {}\n", "needs", true},
	{"context provides", "wire L ROW {\nENTRIES}\n", "provides", false},
}

var rowNames = []string{"A", "B", "C"}

// rowTokens is the old spelling of a row of n names, one token each.
func rowTokens(s rowSite, n int) []string {
	toks := []string{s.keyword}
	if s.parens {
		toks = append(toks, "(")
	}
	for i := range n {
		if i > 0 {
			toks = append(toks, ",")
		}
		toks = append(toks, rowNames[i])
	}
	if s.parens {
		toks = append(toks, ")")
	}
	return toks
}

// rowProgram declares the names, then puts the row's tokens in the site,
// with comments[i] (empty for none) after token i.
func rowProgram(s rowSite, n int, comments []string) string {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for _, name := range rowNames[:n] {
		fmt.Fprintf(&b, "type %s interface {\n\tM%s()\n}\n\n", name, name)
	}
	var row strings.Builder
	for i, tok := range rowTokens(s, n) {
		if i > 0 {
			row.WriteByte(' ')
		}
		row.WriteString(tok)
		if comments[i] != "" {
			row.WriteString(" " + comments[i])
		}
	}
	var entries strings.Builder
	for _, name := range rowNames[:n] {
		fmt.Fprintf(&entries, "\t%s = %s{}\n", name, strings.ToLower(name))
	}
	src := strings.Replace(s.template, "ROW", row.String(), 1)
	b.WriteString(strings.Replace(src, "ENTRIES", entries.String(), 1))
	return b.String()
}

var commentMarker = regexp.MustCompile(`/\*[^*]*\*/|//[^\n]*`)

// markers lists the comments of a source in order.
func markers(src string) []string {
	var out []string
	for _, c := range commentMarker.FindAllString(src, -1) {
		out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c, "//"), "/*"), "*/")))
	}
	return out
}

// isSubsequence reports whether want appears in got in order.
func isSubsequence(want, got []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// rowNamesOf lists the names of each capability row of a decided source.
func rowNamesOf(t *testing.T, src string) [][]string {
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var rows [][]string
	add := func(names []syntax.Expr) {
		var row []string
		for _, n := range names {
			row = append(row, src[n.Pos():n.End()])
		}
		rows = append(rows, row)
	}
	syntax.Inspect(file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.NeedsClause:
			if n != nil {
				add(n.Names)
			}
		case *syntax.ProvidesClause:
			if n != nil {
				add(n.Names)
			}
		}
		return true
	})
	return rows
}

// rowCommentCase is one generated program: a site, a name count and the
// comments placed after the row's tokens.
type rowCommentCase struct {
	label string
	site  rowSite
	n     int
	src   string
}

// rowCommentCases places a block or a line comment after each token of
// every row of one to three names, one comment at a time, and then a
// comment after every token at once.
func rowCommentCases() []rowCommentCase {
	var out []rowCommentCase
	for _, s := range rowSites {
		for n := 1; n <= 3; n++ {
			toks := rowTokens(s, n)
			for _, kind := range []string{"block", "line"} {
				// Each comment holds a comma, so a comma found inside a
				// comment instead of between the names changes its text.
				comment := func(k int) string {
					if kind == "block" {
						return fmt.Sprintf("/* c%d, kept */", k)
					}
					return fmt.Sprintf("// c%d, kept\n", k)
				}
				for at := range toks {
					cs := make([]string, len(toks))
					cs[at] = comment(at)
					out = append(out, rowCommentCase{fmt.Sprintf("%s, %d names, %s comment after %q (token %d)", s.name, n, kind, toks[at], at), s, n, rowProgram(s, n, cs)})
				}
				all := make([]string, len(toks))
				for k := range toks {
					all[k] = comment(k)
				}
				out = append(out, rowCommentCase{fmt.Sprintf("%s, %d names, a %s comment after every token", s.name, n, kind), s, n, rowProgram(s, n, all)})
			}
		}
	}
	return out
}

// TestRowCommentsKept is the property of 196n round 2 (M1): translating an
// old capability row keeps every comment the old parser admits in it. With
// no note the output parses, holds the same comments in the same order, and
// each row keeps its names. With a note the old text, comments included,
// stays in the output in order.
func TestRowCommentsKept(t *testing.T) {
	admitted := map[string]int{}
	for _, c := range rowCommentCases() {
		if _, err := oldsyntax.Parse(c.src); err != nil {
			continue // the old parser does not admit a comment there
		}
		kind := "block"
		if strings.Contains(c.label, "line comment") {
			kind = "line"
		}
		admitted[kind]++
		want := markers(c.src)
		out, notes, err := Translate(c.src)
		if err != nil {
			t.Errorf("%s: %v\n%s", c.label, err, c.src)
			continue
		}
		got := markers(out)
		if len(notes) > 0 {
			admitted["noted"]++
			if !isSubsequence(want, got) {
				t.Errorf("%s: notes %v, and the output lost a comment: want %q in order, got %q\n--- in\n%s--- out\n%s", c.label, notes, want, got, c.src, out)
			}
			continue
		}
		if !slices.Equal(want, got) {
			t.Errorf("%s: comments %q, want %q\n--- in\n%s--- out\n%s", c.label, got, want, c.src, out)
			continue
		}
		rows := rowNamesOf(t, out)
		if len(rows) != 1 || !slices.Equal(rows[0], rowNames[:c.n]) {
			t.Errorf("%s: rows %v, want one row %v\n--- out\n%s", c.label, rows, rowNames[:c.n], out)
		}
	}
	t.Logf("admitted programs: %d with block comments, %d with line comments; %d translated with a note", admitted["block"], admitted["line"], admitted["noted"])
	// The property is not vacuous: the old parser admits both kinds.
	if admitted["block"] == 0 || admitted["line"] == 0 {
		t.Fatalf("admitted programs %v: the old parser admits no comment of a kind; the generator is wrong", admitted)
	}
}

// TestRowCommentsVerifierCases are the round 1 verifier's probes (M1):
// a comment after a comma, after a removed parenthesis, and in provides.
func TestRowCommentsVerifierCases(t *testing.T) {
	for _, src := range []string{
		"package p\nfunc F() needs A, /* KEEP */ B {}\n",
		"package p\nfunc F(f func() needs (/* KEEP */ A, B)) {}\n",
		"package p\nwire C provides A, /* KEEP */ B { A = a\n B = b }\n",
	} {
		out, notes, err := Translate(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if !strings.Contains(out, "/* KEEP */") {
			t.Errorf("%q: the translation lost the comment (notes %v):\n%s", src, notes, out)
		}
	}
}
