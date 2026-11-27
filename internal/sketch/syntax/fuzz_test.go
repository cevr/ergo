package syntax

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// FuzzParse: the token stream keeps its invariant (checkStream), Parse
// never panics, and every error is a located S001 (or the parser's S108
// for a type that mixes fields and operations) whose line and column agree
// with its byte offset. The corpus, the spec fences, the fixture tables
// and the lookahead cases of the unterminated-string fix are the seeds; testdata/fuzz/FuzzParse
// holds the FuzzCheck crashers as regressions.
func FuzzParse(f *testing.F) {
	positive, err := sketchtest.Positive()
	if err != nil {
		f.Fatal(err)
	}
	_, superseded, err := sketchtest.Corpus()
	if err != nil {
		f.Fatal(err)
	}
	for _, in := range append(positive, superseded...) {
		f.Add(in.Src)
	}
	for _, m := range sketchtest.MalformedTable {
		if m.Generate == nil {
			f.Add(m.Src)
		}
	}
	for _, src := range lookaheadSeeds {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if broken := checkStream(src, scan(src).toks); broken != "" {
			t.Fatal(broken)
		}
		file, err := Parse(src)
		if err == nil {
			if file == nil {
				t.Fatal("no file and no error")
			}
			return
		}
		var e *Error
		if !errors.As(err, &e) || (e.Code != CodeSyntax && e.Code != CodeMixedMembers) || file != nil {
			t.Fatalf("error %v (%T) is not a lone S001 or S108", err, err)
		}
		if e.Offset < 0 || e.Length < 0 || e.Offset+e.Length > len(src) {
			t.Fatalf("span %d+%d outside the %d-byte source", e.Offset, e.Length, len(src))
		}
		line := strings.Count(src[:e.Offset], "\n") + 1
		column := e.Offset - (strings.LastIndexByte(src[:e.Offset], '\n') + 1) + 1
		if line != e.Line || column != e.Column {
			t.Fatalf("%d:%d, want %d:%d", e.Line, e.Column, line, column)
		}
	})
}

// lookaheadSeeds are sources that end in a lexical error after a parser
// lookahead has started. Their verdicts are compiler/syntax's
// tests in lexerror.ergo (TestCompilerSyntaxTestsPass); here they seed
// the fuzzer.
var lookaheadSeeds = []string{
	`func A(){match A{"`, `func A(){with A{"0`, "func A(){with A{`0", `func A(){match A{'`,
	"func A(){match A{\xff", `func A(){match A{"\(b`, "func A(){match A{\"\\(b\n", `func A(){match A{"\(b)c`,
	`type A{F["`, "type A{Items [\xff", `type A{F["\(x`, `type A{F[T]"`,
	`func A(){for "`, `func A(){for x in "\(y`, "func A(xs []int){for i, x\u00a0in xs {\n}}",
	"func A(xs []int){for i, x@ in xs {\n}}", `func A(){for i, x "`,
	`func A(){let y = f[func("`, `func A(){with context{A, "`,
}

// checkStream reports the first break of the token stream's invariant, or
// "" when it holds: the stream ends with its one EOF, each token's span
// lies in the source with pos <= end, and each token starts at or after the
// end of the token before it. The parser's lookaheads rely on it: past the
// last token peek returns the EOF, so a scan that stops at EOF stops, and
// the source between any two tokens it reads is a valid slice.
func checkStream(src string, toks []token) string {
	if len(toks) == 0 || toks[len(toks)-1].tok != EOF {
		return "the stream does not end with EOF"
	}
	prev := Pos(0)
	for i, t := range toks {
		switch {
		case t.tok == EOF && i != len(toks)-1:
			return fmt.Sprintf("EOF at token %d of %d", i, len(toks))
		case t.pos < prev:
			return fmt.Sprintf("token %d (%s) starts at %d, before the previous end %d", i, t.tok, t.pos, prev)
		case t.end < t.pos || int(t.end) > len(src):
			return fmt.Sprintf("token %d (%s) spans %d..%d in a %d-byte source", i, t.tok, t.pos, t.end, len(src))
		}
		prev = t.end
	}
	return ""
}
