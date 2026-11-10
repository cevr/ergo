package syntax

import (
	"errors"
	gotoken "go/token"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestIdentifierRunesMatchGo sweeps every non-ASCII code point, in the first
// and in a later position, and requires the scanner to admit exactly the
// identifiers go/token admits (ported from the prototype's lex_test.go).
func TestIdentifierRunesMatchGo(t *testing.T) {
	for r := rune(utf8.RuneSelf); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		for _, name := range []string{"x" + string(r), string(r) + "x"} {
			s := scan(name)
			admitted := s.err == nil && len(s.toks) == 3 && s.toks[0].tok == IDENT && s.toks[0].lit == name
			if admitted != gotoken.IsIdentifier(name) {
				t.Fatalf("%q (U+%04X): scanner admits %v, go/token admits %v (err %v)", name, r, admitted, !admitted, s.err)
			}
		}
	}
}

// TestIdentifierBytes rejects invalid UTF-8 in identifier position with
// S001 at the offending byte, wherever it falls in the name.
func TestIdentifierBytes(t *testing.T) {
	for _, name := range []string{"x\xff", "\xffx", "x\xe2\xaa", "x\xc0\x80"} {
		src := "func main() needs Console {\n  let " + name + " = \"ok\"\n}\n"
		_, err := Parse(src)
		var e *Error
		if !errors.As(err, &e) || e.Code != CodeSyntax || e.Line != 2 {
			t.Fatalf("%q: got %v, want S001 on line 2", name, err)
		}
		if bad := strings.IndexFunc(src[e.Offset:], func(r rune) bool { return r == utf8.RuneError }); bad != 0 {
			t.Fatalf("%q: the error at byte %d is not at the invalid byte", name, e.Offset)
		}
	}
}

// TestSemicolonInsertion: Go's rule, including after break, continue, ++,
// --, return, a closing brace and a block comment that spans lines.
func TestSemicolonInsertion(t *testing.T) {
	cases := map[string][]Token{
		"x\n":               {IDENT, SEMICOLON, EOF},
		"break\n":           {BREAK, SEMICOLON, EOF},
		"continue\n":        {CONTINUE, SEMICOLON, EOF},
		"return\n":          {RETURN, SEMICOLON, EOF},
		"i++\n":             {IDENT, INC, SEMICOLON, EOF},
		"i--\n":             {IDENT, DEC, SEMICOLON, EOF},
		"}\n":               {RBRACE, SEMICOLON, EOF},
		"x /* a\nb */ y":    {IDENT, SEMICOLON, IDENT, SEMICOLON, EOF},
		"x /* a */\n":       {IDENT, SEMICOLON, EOF},
		"try\n":             {TRY, EOF},
		"x // c\n":          {IDENT, SEMICOLON, EOF},
		"500ms 'r' \"s\"\n": {DURATION, RUNE, STRING, SEMICOLON, EOF},
		"a &^= b <<= c":     {IDENT, AND_NOT_ASSIGN, IDENT, SHL_ASSIGN, IDENT, SEMICOLON, EOF},
		"go.Error":          {GO, PERIOD, IDENT, SEMICOLON, EOF},
		"initial step":      {IDENT, IDENT, SEMICOLON, EOF},
		"1..n":              {INT, DOTDOT, IDENT, SEMICOLON, EOF},
		"2.5 1. 1e9\n":      {FLOAT, FLOAT, FLOAT, SEMICOLON, EOF},
		"x as y":            {IDENT, AS, IDENT, SEMICOLON, EOF},
		"x <-y":             {IDENT, ARROW, IDENT, SEMICOLON, EOF},
		"a...":              {IDENT, ELLIPSIS, EOF},
	}
	for src, want := range cases {
		s := scan(src)
		if s.err != nil {
			t.Fatalf("%q: %v", src, s.err)
		}
		var got []Token
		for _, tok := range s.toks {
			got = append(got, tok.tok)
		}
		if len(got) != len(want) {
			t.Fatalf("%q: tokens %v, want %v", src, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q: tokens %v, want %v", src, got, want)
			}
		}
	}
}

// TestNumberForms: decimal floats are literals (`1..n` is a range, never
// the float `1.`); hexadecimal, octal and binary integers and digit
// separators are literals with Go's rules (grammar.md, Literals 1);
// imaginary literals, hexadecimal floats, a digit outside the base and a
// misplaced separator stay S001 with a message that names them.
func TestNumberForms(t *testing.T) {
	for src, want := range map[string]string{
		"5kg":     `unknown number suffix "kg"`,
		"0.A":     `unknown number suffix "A"`,
		"2i":      "imaginary",
		"2.5i":    "imaginary",
		"0x1i":    "invalid digit 'i' in the hexadecimal literal",
		"0x1p4":   "hexadecimal float",
		"0x1.8":   "hexadecimal float",
		"0xff.A":  "hexadecimal float",
		"0x":      "the hexadecimal literal 0x has no digits",
		"0b_":     "the binary literal 0b_ has no digits",
		"0b102":   "invalid digit '2' in the binary literal 0b102",
		"0o8":     "invalid digit '8' in the octal literal",
		"0xG":     "invalid digit 'G'",
		"0x10ms":  "invalid digit 'm'",
		"0x1é":    "invalid digit 'é'",
		"1__000":  "'_' must separate successive digits in 1__000",
		"1_":      "'_' must separate successive digits",
		"1_.5":    "'_' must separate successive digits",
		"1._5":    "'_' must separate successive digits",
		"1e_5":    "'_' must separate successive digits",
		"1_e5":    "'_' must separate successive digits",
		"0x1__F":  "'_' must separate successive digits in 0x1__F",
		"0xF_":    "'_' must separate successive digits",
		"0b1_2":   "invalid digit '2'",
		"1_000kg": `unknown number suffix "kg"`,
	} {
		s := scan(src)
		if s.err == nil || !strings.Contains(s.err.Message, want) {
			t.Fatalf("%q: %v, want a message with %q", src, s.err, want)
		}
	}
	for src, want := range map[string][]Token{
		"1.5":    {FLOAT, SEMICOLON, EOF},
		"1.":     {FLOAT, SEMICOLON, EOF},
		"2.5e-3": {FLOAT, SEMICOLON, EOF},
		"1e9":    {FLOAT, SEMICOLON, EOF},
		"1..9":   {INT, DOTDOT, INT, SEMICOLON, EOF},
		"1.5..2": {FLOAT, DOTDOT, INT, SEMICOLON, EOF},
		"500ms":  {DURATION, SEMICOLON, EOF},
		// Go's integer forms and separators (L1).
		"0x1F":           {INT, SEMICOLON, EOF},
		"0X_1f":          {INT, SEMICOLON, EOF},
		"0o600 0O7":      {INT, INT, SEMICOLON, EOF},
		"0b1010 0B1":     {INT, INT, SEMICOLON, EOF},
		"1_000 0_7":      {INT, INT, SEMICOLON, EOF},
		"1_000.000_5":    {FLOAT, SEMICOLON, EOF},
		"1e1_0":          {FLOAT, SEMICOLON, EOF},
		"1_500ms":        {DURATION, SEMICOLON, EOF},
		"0x1..0xF":       {INT, DOTDOT, INT, SEMICOLON, EOF},
		"0b1 + 0o7*0x_A": {INT, ADD, INT, MUL, INT, SEMICOLON, EOF},
	} {
		s := scan(src)
		if s.err != nil {
			t.Fatalf("%q: %v", src, s.err)
		}
		var got []Token
		for _, tok := range s.toks {
			got = append(got, tok.tok)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%q: tokens %v, want %v", src, got, want)
		}
	}
}

// TestKeywords: the 28 decided keywords and `as` are keywords, the 15 Go
// keywords Ergo does not use are reserved, and none of them is a name.
func TestKeywords(t *testing.T) {
	want := strings.Fields("break case continue else for func if import return type try catch needs provides context machine let mut in match escaping fails test law assert extern with takes as")
	got := Keywords()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("keywords %v, want %v", got, want)
	}
	reserved := strings.Fields("chan const default defer fallthrough go goto interface map package range select struct switch var")
	got = Reserved()
	slices.Sort(got)
	if !slices.Equal(got, reserved) {
		t.Fatalf("reserved %v, want %v", got, reserved)
	}
	for _, word := range append(want, reserved...) {
		if !IsKeyword(word) {
			t.Fatalf("%s is not reserved", word)
		}
		if _, err := Parse("let " + word + " = 1\n"); err == nil {
			t.Fatalf("%s is a name", word)
		}
	}
}

// TestPositions: a CRLF source keeps byte offsets, a span at the end of the
// file is empty, and a file without a package clause starts at its first
// comment or declaration.
func TestPositions(t *testing.T) {
	_, err := Parse("// c\r\n\r\nfunc f() {\r\n\tg(\r\n")
	var e *Error
	if !errors.As(err, &e) || e.Line != 5 || e.Length != 0 {
		t.Fatalf("got %+v, want an empty span on line 5", err)
	}
	f, err := Parse("\r\n\r\n// é\r\nfunc f() {}\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if line, column := f.Position(f.Decls[0].Pos()); line != 4 || column != 1 {
		t.Fatalf("func at %d:%d, want 4:1", line, column)
	}
	if line, _ := f.Position(f.Pos()); line != 3 {
		t.Fatalf("file at line %d, want the comment's 3", line)
	}
	if text := f.Comments[0].List[0].Text; text != "// é" {
		t.Fatalf("comment text %q keeps its carriage return", text)
	}
	if doc := f.Decls[0].(*FuncDecl).Doc; doc == nil || doc.List[0].Text != "// é" {
		t.Fatalf("the func has no doc comment")
	}
}

// TestNodeSpans: every node of every positive input has a span inside its
// source, Pos <= End, nested inside its parent's span: no NoPos leaks from a
// desugared node (a TypeGuard without parentheses, a File without a package
// clause, an extern FuncDecl) into a span.
func TestNodeSpans(t *testing.T) {
	inputs, err := sketchtest.Positive()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inputs {
		f, err := Parse(in.Src)
		if err != nil {
			t.Errorf("%s: %v", in.Name, err)
			continue
		}
		// check visits the children of parent, each inside its parent's
		// span (the File's span may start after a leading comment).
		var check func(parent Node)
		check = func(parent Node) {
			Inspect(parent, func(n Node) bool {
				if n == parent {
					return true
				}
				pos, end := n.Pos(), n.End()
				if pos < 0 || end < pos || int(end) > len(in.Src) {
					t.Errorf("%s: %T spans %d..%d in a %d-byte source", in.Name, n, pos, end, len(in.Src))
					return false
				}
				if _, isFile := parent.(*File); !isFile && (pos < parent.Pos() || end > parent.End()) {
					t.Errorf("%s: %T %d..%d outside its parent %T %d..%d", in.Name, n, pos, end, parent, parent.Pos(), parent.End())
				}
				check(n)
				return false
			})
		}
		check(f)
	}
}
