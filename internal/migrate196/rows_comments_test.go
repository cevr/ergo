package migrate196

import (
	"fmt"
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
// with gaps[i] (empty for none) after token i.
func rowProgram(s rowSite, n int, gaps []string) string {
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
		if gaps[i] != "" {
			row.WriteString(" " + gaps[i])
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

// rowTrivia are the comment sequences a gap of a row can hold, as format
// strings of one tag (%[1]s): block and line comments alone, adjacent and
// spaced, mixed, and comments that hold the row's punctuation (, | ( )) or
// the other comment kind's delimiters. Adjacent block comments put `*//*`
// in the source, which reads as a line comment to a byte scanner.
var rowTrivia = []struct{ name, format string }{
	{"block", "/* %[1]s */"},
	{"adjacent blocks", "/* %[1]sa *//* %[1]sb */"},
	{"adjacent blocks, no spaces, comma", "/*%[1]sa*//*%[1]sb,*/"},
	{"spaced blocks", "/* %[1]sa */ /* %[1]sb */"},
	{"block then line", "/* %[1]sa */ // %[1]sb\n"},
	{"line", "// %[1]s\n"},
	{"line then block", "// %[1]sa\n/* %[1]sb */"},
	{"block with punctuation", "/* %[1]s, | ( ) */"},
	{"line with punctuation and a block opener", "// %[1]s, | ( ) /* x\n"},
	{"block with delimiters inside", "/* %[1]s /* // , */"},
	{"block of stars", "/** %[1]s, **/"},
}

// rowCommentCase is one generated program: a site, a name count and the
// comment sequences placed after the row's tokens.
type rowCommentCase struct {
	label  string
	trivia int // index in rowTrivia
	site   rowSite
	n      int
	src    string
}

// rowCommentCases places each comment sequence after each token of every
// row of one to three names, one gap at a time, and then after every token
// at once.
func rowCommentCases() []rowCommentCase {
	var out []rowCommentCase
	for _, s := range rowSites {
		for n := 1; n <= 3; n++ {
			toks := rowTokens(s, n)
			for ti, tr := range rowTrivia {
				for at := range toks {
					gaps := make([]string, len(toks))
					gaps[at] = fmt.Sprintf(tr.format, fmt.Sprintf("c%d", at))
					out = append(out, rowCommentCase{fmt.Sprintf("%s, %d names, %s after %q (token %d)", s.name, n, tr.name, toks[at], at), ti, s, n, rowProgram(s, n, gaps)})
				}
				all := make([]string, len(toks))
				for k := range toks {
					all[k] = fmt.Sprintf(tr.format, fmt.Sprintf("c%d", k))
				}
				out = append(out, rowCommentCase{fmt.Sprintf("%s, %d names, %s after every token", s.name, n, tr.name), ti, s, n, rowProgram(s, n, all)})
			}
		}
	}
	return out
}

// oldComments and newComments list a source's comments in order, as the
// parser of each syntax reports them.
func oldComments(f *oldsyntax.File) []string {
	var out []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			out = append(out, c.Text)
		}
	}
	return out
}

func newComments(f *syntax.File) []string {
	var out []string
	for _, g := range f.Comments {
		for _, c := range g.List {
			out = append(out, c.Text)
		}
	}
	return out
}

// rowNamesOf lists the names of each capability row of a decided file.
func rowNamesOf(f *syntax.File, src string) [][]string {
	var rows [][]string
	add := func(names []syntax.Expr) {
		var row []string
		for _, n := range names {
			row = append(row, src[n.Pos():n.End()])
		}
		rows = append(rows, row)
	}
	syntax.Inspect(f, func(n syntax.Node) bool {
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

// TestRowCommentsKept is the property of 196n rounds 2 and 3 (M1, M2):
// every old capability row the frozen parser admits, with any of the
// comment sequences of rowTrivia in any gap, translates with no note, and
// the output parses, holds the same comments in the same order, and keeps
// each row's names.
func TestRowCommentsKept(t *testing.T) {
	admitted := make([]int, len(rowTrivia))
	total := 0
	for _, c := range rowCommentCases() {
		old, err := oldsyntax.Parse(c.src)
		if err != nil {
			continue // the old parser does not admit this sequence there
		}
		admitted[c.trivia]++
		total++
		out, notes, err := Translate(c.src)
		if err != nil || len(notes) > 0 {
			t.Errorf("%s: err %v, notes %v\n--- in\n%s--- out\n%s", c.label, err, notes, c.src, out)
			continue
		}
		f, err := syntax.Parse(out)
		if err != nil {
			t.Errorf("%s: the output does not parse: %v\n--- out\n%s", c.label, err, out)
			continue
		}
		if want, got := oldComments(old), newComments(f); !slices.Equal(want, got) {
			t.Errorf("%s: comments %q, want %q\n--- in\n%s--- out\n%s", c.label, got, want, c.src, out)
			continue
		}
		if rows := rowNamesOf(f, out); len(rows) != 1 || !slices.Equal(rows[0], rowNames[:c.n]) {
			t.Errorf("%s: rows %v, want one row %v\n--- out\n%s", c.label, rows, rowNames[:c.n], out)
		}
	}
	t.Logf("admitted programs: %d; by comment sequence %v", total, admitted)
	// The property is not vacuous: the old parser admits each sequence somewhere.
	for i, n := range admitted {
		if n == 0 {
			t.Errorf("the old parser admits no program with %q; the generator is wrong", rowTrivia[i].name)
		}
	}
}

// TestRowCommentsVerifierCases are the verifiers' probes: round 1's comment
// after a comma, after a removed parenthesis, and in provides (M1), and
// round 2's adjacent block comments before the comma (M2).
func TestRowCommentsVerifierCases(t *testing.T) {
	for _, c := range []struct {
		src  string
		keep []string
	}{
		{"package p\nfunc F() needs A, /* KEEP */ B {}\n", []string{"/* KEEP */"}},
		{"package p\nfunc F(f func() needs (/* KEEP */ A, B)) {}\n", []string{"/* KEEP */"}},
		{"package p\nwire C provides A, /* KEEP */ B { A = a\n B = b }\n", []string{"/* KEEP */"}},
		{"package p\ntype A interface { M() }\ntype B interface { N() }\nfunc F() needs A /*one*//*two,*/, B {}\n", []string{"/*one*/", "/*two,*/"}},
	} {
		out, notes, err := Translate(c.src)
		if err != nil || len(notes) > 0 {
			t.Errorf("%q: err %v, notes %v\n%s", c.src, err, notes, out)
			continue
		}
		f, err := syntax.Parse(out)
		if err != nil {
			t.Errorf("%q: the output does not parse: %v\n%s", c.src, err, out)
			continue
		}
		rows, comments := rowNamesOf(f, out), newComments(f)
		if !slices.Equal(comments, c.keep) || len(rows) != 1 || !slices.Equal(rows[0], []string{"A", "B"}) {
			t.Errorf("%q: rows %v and comments %q, want one row [A B] and comments %q:\n%s", c.src, rows, comments, c.keep, out)
		}
	}
}

// TestSeparateAdjacentComments is the regression case of the members
// rewrite (separate), the other reader of comment spans: adjacent block
// comments before a struct's semicolon must not hide it.
func TestSeparateAdjacentComments(t *testing.T) {
	src := "package p\n\ntype T struct {\n\tA int64 /*one*//*two;*/; B string\n}\n"
	out, notes, err := Translate(src)
	if err != nil || len(notes) > 0 {
		t.Fatalf("err %v, notes %v\n%s", err, notes, out)
	}
	f, err := syntax.Parse(out)
	if err != nil {
		t.Fatalf("the output does not parse: %v\n%s", err, out)
	}
	if got := newComments(f); !slices.Equal(got, []string{"/*one*/", "/*two;*/"}) {
		t.Errorf("comments %q, want both kept\n%s", got, out)
	}
	if strings.Contains(strings.ReplaceAll(out, "/*two;*/", ""), ";") {
		t.Errorf("the old semicolon is left in the output:\n%s", out)
	}
}
