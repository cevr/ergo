package syntax

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRuneLiteralsAgreeWithStrconv: a rune literal scans as one RUNE token
// exactly when Go's strconv.Unquote reads it as one rune (grammar.md takes
// Go's rune literals). The sweep holds every octal escape \000 to \777,
// every \x escape, the one-letter escapes and seeded \u and \U escapes
// around the surrogates and the last code point. A row prints the literal
// and both verdicts. It kills scan-rune-octal-bound (`'\400'` admitted),
// which no corpus file reaches.
func TestRuneLiteralsAgreeWithStrconv(t *testing.T) {
	var lits []string
	for v := range 0o1000 {
		lits = append(lits, fmt.Sprintf(`'\%03o'`, v))
	}
	for v := range 0x100 {
		lits = append(lits, fmt.Sprintf(`'\x%02x'`, v))
	}
	for _, c := range "abfnrtv\\'\"0q8" {
		lits = append(lits, `'\`+string(c)+`'`)
	}
	for _, v := range []int{0, 0x7f, 0xd7ff, 0xd800, 0xdfff, 0xe000, 0xfffd, 0xffff, 0x10000, 0x10ffff, 0x110000, 0xffffffff} {
		lits = append(lits, fmt.Sprintf(`'\u%04x'`, v&0xffff), fmt.Sprintf(`'\U%08x'`, v))
	}
	lits = append(lits, "'a'", "'é'", "'ab'", "''", "'\xff'")
	for _, lit := range lits {
		s := scan(lit + "\n")
		scanned := s.err == nil && len(s.toks) > 0 && s.toks[0].tok == RUNE && s.toks[0].lit == lit
		u, err := strconv.Unquote(lit)
		gos := err == nil && utf8.RuneCountInString(u) == 1
		if scanned != gos {
			t.Errorf("%s: the scanner reads one rune: %v; strconv.Unquote: %v (%v)", lit, scanned, gos, err)
		}
	}
}

// TestCommentGroupsEndAtBlankLines: two comments with a blank line, or a
// token, between them are never one group (go/parser's grouping, which the
// Doc of a declaration reads). On every input of TestDecodeAgreesWithView
// that parses. It kills comments-blank-line-joins-groups, which neither the
// view (which prints groups as the parser made them) nor the formatter
// (which keeps blank lines either way) sees.
func TestCommentGroupsEndAtBlankLines(t *testing.T) {
	corpus := corpusInputs(t)
	inputs := agreeSeeds(t, func(s int64) string { return pickDecode(s, corpus) })
	if os.Getenv("ERGO_AGREE_SEED") == "" {
		inputs = append(corpus, inputs...)
	}
	failures := 0
	for _, in := range inputs {
		f, err := Parse(in.src)
		if err != nil {
			continue
		}
		for _, g := range f.Comments {
			for i := 1; i < len(g.List); i++ {
				gap := in.src[g.List[i-1].End():g.List[i].Pos()]
				if strings.TrimSpace(gap) == "" && strings.Count(gap, "\n") < 2 {
					continue
				}
				failures++
				if failures <= 3 {
					line, _ := f.Position(g.List[i].Pos())
					t.Errorf("%s:%d: a comment joins the group above it across %q%s", in.name, line, gap, replay(in, "TestCommentGroupsEndAtBlankLines"))
				}
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d comments join a group across a blank line or a token", failures)
	}
}
