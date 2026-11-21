package lower

import (
	"bytes"
	"flag"
	"fmt"
	"go/scanner"
	"go/token"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of anchorLines (anchor.go) against anchorLinesScan,
// the go/scanner pass it replaced, as the reference model. Each seed writes
// a Go-like text of lines: indentation, then identifiers, operators,
// interpreted strings and runes whose escapes hide a quote, raw strings and
// block comments that hold the other kinds' openers and may span lines,
// line comments, //line directives at a line's start, after code on a line
// and with no text, a line that starts with a semicolon or a comment, and
// blank lines. Both passes must give the same bytes. A failing seed prints
// the text, both outputs and its replay command. The fixed seeds reach every
// path of anchorPaths.

var anchorSeed = flag.Uint64("anchor.seed", 0, "run TestAnchorLinesAgree on this seed alone")

// anchorPaths are the generator's branches; the fixed seeds reach each.
var anchorPaths = []string{"ident", "op", "string", "escape", "rune", "raw", "rawlines", "block", "blocklines", "comment", "directive", "middirective", "emptydirective", "semicolon", "blank", "afterclose"}

type anchorGen struct {
	r       *rand.Rand
	b       strings.Builder
	reached map[string]bool
	dir     int
}

func (g *anchorGen) path(p string) { g.reached[p] = true }

// inner is text a quoted or commented token may hold: the openers of the
// other kinds, and of its own kind where except allows.
func (g *anchorGen) inner(except string) string {
	parts := []string{"x", " ", "//", "/*", "*/", "\"", "'", "`", "\\\\", ";", "//line z.ergo:9:9"}
	var s strings.Builder
	for range g.r.IntN(4) {
		p := parts[g.r.IntN(len(parts))]
		if strings.Contains(except, p) || (except != "" && strings.ContainsAny(p, except)) {
			continue
		}
		s.WriteString(p)
	}
	return s.String()
}

// token writes one token or comment; it reports whether the line must end
// after it (a line comment). A block comment spans lines only when it leads
// its line (lead): after a token that ends a statement, go/scanner gives
// the implied semicolon after the comment, on its first line, so the
// reference anchors the code after the comment's end, which anchorLines
// leaves as it is. The lowering writes no block comment that spans lines.
func (g *anchorGen) token(lead bool) bool {
	switch g.r.IntN(12) {
	case 0, 1:
		g.path("ident")
		g.b.WriteString([]string{"x", "v_1", "env", "return", "if", "0x1f", "3.5"}[g.r.IntN(7)])
	case 2:
		g.path("op")
		g.b.WriteString([]string{"=", ":=", "{", "}", "(", ")", ".", ",", "+", "/", "*"}[g.r.IntN(11)])
	case 3:
		g.path("string")
		s := g.inner("\"\\\n")
		if g.r.IntN(2) == 0 {
			g.path("escape")
			s += "\\\"" + g.inner("\"\\\n")
		}
		g.b.WriteString("\"" + s + "\"")
	case 4:
		g.path("rune")
		g.b.WriteString([]string{"'a'", "'`'", "'\"'", "'\\''", "'\\\\'", "'/'"}[g.r.IntN(6)])
	case 5:
		g.path("raw")
		s := g.inner("`")
		if g.r.IntN(2) == 0 {
			g.path("rawlines")
			s += "\n" + g.inner("`") + "\n" + g.inner("`")
		}
		g.b.WriteString("`" + s + "`")
	case 6:
		g.path("block")
		s := strings.ReplaceAll(g.inner(""), "*/", "")
		if lead && g.r.IntN(2) == 0 {
			g.path("blocklines")
			s += "\n" + strings.ReplaceAll(g.inner(""), "*/", "") + "\n\t"
		}
		if strings.HasSuffix(s, "*") || strings.HasSuffix(s, "/") {
			s += "x"
		}
		g.b.WriteString("/*" + s + "*/")
	case 7:
		g.path("comment")
		g.b.WriteString("// " + strings.ReplaceAll(g.inner(""), "\n", ""))
		return true
	case 8:
		g.path("middirective")
		g.dir++
		fmt.Fprintf(&g.b, "//line m.ergo:%d:2", g.dir)
		return true
	default:
		g.b.WriteString("y")
	}
	return false
}

func anchorText(seed uint64, reached map[string]bool) []byte {
	g := &anchorGen{r: rand.New(rand.NewPCG(seed, 37)), reached: reached}
	for range 20 + g.r.IntN(40) {
		switch g.r.IntN(10) {
		case 0:
			g.path("directive")
			g.dir++
			fmt.Fprintf(&g.b, "//line a.ergo:%d:%d\n", g.dir, 1+g.r.IntN(4))
			continue
		case 1:
			if g.r.IntN(4) == 0 {
				g.path("emptydirective")
				g.b.WriteString("//line \n")
				continue
			}
			g.path("blank")
			g.b.WriteString([]string{"\n", "\t\n", "  \n"}[g.r.IntN(3)])
			continue
		}
		semi := g.r.IntN(8) == 0
		if semi {
			g.path("semicolon")
			g.b.WriteString("\t; ")
		}
		g.b.WriteString(strings.Repeat("\t", g.r.IntN(3)))
		for k := range 1 + g.r.IntN(5) {
			if k > 0 {
				g.b.WriteString(" ")
			}
			if g.token(k == 0 && !semi) {
				break
			}
		}
		g.b.WriteString("\n")
	}
	src := g.b.String()
	if strings.Contains(src, "*/ y") || strings.Contains(src, "` y") || strings.Contains(src, "*/ x") {
		reached["afterclose"] = true // code after a multi-line token closes
	}
	return []byte(src)
}

// TestAnchorLinesAgree is the seeded property (see above).
func TestAnchorLinesAgree(t *testing.T) {
	seeds := make([]uint64, 300)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if *anchorSeed != 0 {
		seeds = []uint64{*anchorSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src := anchorText(seed, reached)
		got, want := anchorLines(bytes.Clone(src)), anchorLinesScan(bytes.Clone(src))
		if !bytes.Equal(got, want) {
			t.Fatalf("seed %d: anchorLines differs from the go/scanner pass\nReplay: go test -run '^TestAnchorLinesAgree$' ./internal/sketch/lower -args -anchor.seed=%d\n--- text\n%s\n--- anchorLines\n%s\n--- go/scanner\n%s", seed, seed, src, got, want)
		}
	}
	if *anchorSeed != 0 {
		return
	}
	for _, p := range anchorPaths {
		if !reached[p] {
			t.Errorf("the fixed seeds never reach %q", p)
		}
	}
}

// anchorLinesScan is anchorLines as it was before it read the text itself:
// a go/scanner pass over every token (the reference model).
func anchorLinesScan(src []byte) []byte {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	type insert struct {
		at   int
		text string
	}
	var inserts []insert
	cur, curLine, lastLine := "", 0, 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		p := file.PositionFor(pos, false)
		first := p.Line != lastLine
		lastLine = p.Line
		if tok == token.STRING || tok == token.COMMENT {
			lastLine += strings.Count(lit, "\n")
		}
		switch {
		case tok == token.COMMENT && strings.HasPrefix(lit, "//line "):
			cur, curLine = lit[len("//line "):], p.Line
		case !first || tok == token.COMMENT || tok == token.SEMICOLON || cur == "" || p.Line == curLine+1:
		default:
			inserts = append(inserts, insert{p.Offset, "/*line " + cur + "*/"})
		}
	}
	var b []byte
	prev := 0
	for _, in := range inserts {
		b = append(b, src[prev:in.at]...)
		b = append(b, in.text...)
		prev = in.at
	}
	return append(b, src[prev:]...)
}
