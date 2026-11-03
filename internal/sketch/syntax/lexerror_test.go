package syntax

import (
	"errors"
	"fmt"
	"testing"
)

// lexErrorCases are sources that end in a lexical error after a parser
// lookahead has started: the lookahead runs into the scanner's error token
// and must stop there (#226). Each wants the first S001 in the source, the
// scanner's unless a parser error comes before it, at offset with msg (any
// message when msg is empty). Before #226 each panicked on a slice of the
// source or scanned without end.
var lexErrorCases = []struct {
	name   string
	src    string
	offset int
	msg    string
}{
	// The two FuzzCheck crashers: positionalElement scanned past the
	// unterminated string of a header literal's body.
	{"match header, unterminated string", `func A(){match A{"`, 17, "string literal not terminated"},
	{"with header, unterminated string", `func A(){with A{"0`, 16, "string literal not terminated"},
	{"with header, unterminated raw string", "func A(){with A{`0", 16, "raw string literal not terminated"},
	{"match header, unterminated rune", `func A(){match A{'`, 17, ""},
	{"match header, invalid byte", "func A(){match A{\xff", 17, ""},
	// The scanner reports an open interpolation at its string's opening
	// quote, behind the interpolation's tokens.
	{"match header, open interpolation", `func A(){match A{"\(b`, 17, "string literal not terminated"},
	{"match header, newline in interpolation", "func A(){match A{\"\\(b\n", 17, "newline in string"},
	{"match header, unterminated string after interpolation", `func A(){match A{"\(b)c`, 17, "string literal not terminated"},
	// bracketsThenParen and the for header's habit scan stopped only at
	// EOF or a closing token. A member's `[` right before the error may
	// open `[]T`, so the lexical error is the first; a member's `[` before
	// anything but `]` is a fixed array or an operation's type parameters,
	// an error whatever follows, so it comes first in the source.
	{"member brackets, unterminated string", `type A{F["`, 9, "string literal not terminated"},
	{"member brackets, invalid byte", "type A{Items [\xff", 14, "invalid UTF-8 byte 0xff"},
	{"member brackets, open interpolation", `type A{F["\(x`, 8, "Ergo has no fixed arrays; use a slice `[]T`"},
	{"member brackets closed before the error", `type A{F[T]"`, 8, "Ergo has no fixed arrays; use a slice `[]T`"},
	{"for header, unterminated string", `func A(){for "`, 13, "string literal not terminated"},
	{"for header, open interpolation", `func A(){for x in "\(y`, 18, "string literal not terminated"},
	// A speculative reading (attempt) that reaches the lexical error read
	// the source without fault up to it; the fallback reading must not
	// report an earlier error (round-1 review m1). The range-pattern
	// reading of a for header, and the type reading of an index.
	{"for header, no-break space before in", "func A(xs []int){for i, x in xs {\n}}", 25, "unexpected character U+00A0 '\\u00a0'"},
	{"for header, stray character before in", "func A(xs []int){for i, x@ in xs {\n}}", 25, "unexpected character '@'"},
	{"for header, unterminated string after patterns", `func A(){for i, x "`, 18, "string literal not terminated"},
	// `escaping func(` reads only as a type: with no repair, the fallback
	// expression reading reports "a type is not a value" at the `escaping`.
	{"index, type reading reaches the error", `func A(){let y = f[escaping func("`, 33, "string literal not terminated"},
	// keysContinue reads a context body's key list.
	{"context entry keys, unterminated string", `func A(){with context{A, "`, 25, "string literal not terminated"},
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

// TestLexicalErrorInLookahead: a lexical error inside a lookahead gives
// the scanner's located S001, with no panic and no endless scan, and the
// token stream keeps its invariant.
func TestLexicalErrorInLookahead(t *testing.T) {
	for _, c := range lexErrorCases {
		t.Run(c.name, func(t *testing.T) {
			if broken := checkStream(c.src, scan(c.src).toks); broken != "" {
				t.Fatal(broken)
			}
			file, err := Parse(c.src)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeSyntax || file != nil {
				t.Fatalf("got %v (%T), want S001", err, err)
			}
			if e.Offset != c.offset || c.msg != "" && e.Message != c.msg {
				t.Fatalf("got %d %q, want %d %q", e.Offset, e.Message, c.offset, c.msg)
			}
		})
	}
}
