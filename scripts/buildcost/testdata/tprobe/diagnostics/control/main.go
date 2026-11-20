// Command control is the hand Go control of TPROBE's diagnostics kind: the
// least Go that gives TestLexicalErrorInLookahead's verdicts on the same
// subject (compiler/syntax's scanner and parser, through the linked Go of
// internal/linked/compiler). For each source it checks the token stream's
// invariant and the parse's S001, offset and message, and prints one FAIL
// line per case that breaks it (exit 1), or "ok N cases" (exit 0). It
// reads the parse's failure from linked.Failure, with no decode into Go
// nodes, since the verdict reads only the failure.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	linked "ergo.local/prototype/internal/linked/compiler"
)

// cases are lexerror_test.go's lexErrorCases: a source, the offset of the
// first S001 and its message (any message when empty).
var cases = []struct {
	src    string
	offset int
	msg    string
}{
	{`func A(){match A{"`, 17, "string literal not terminated"},
	{`func A(){with A{"0`, 16, "string literal not terminated"},
	{"func A(){with A{`0", 16, "raw string literal not terminated"},
	{`func A(){match A{'`, 17, ""},
	{"func A(){match A{\xff", 17, ""},
	{`func A(){match A{"\(b`, 17, "string literal not terminated"},
	{"func A(){match A{\"\\(b\n", 17, "newline in string"},
	{`func A(){match A{"\(b)c`, 17, "string literal not terminated"},
	{`type A{F["`, 9, "string literal not terminated"},
	{"type A{Items [\xff", 14, "invalid UTF-8 byte 0xff"},
	{`type A{F["\(x`, 9, "string literal not terminated"},
	{`type A{F[T]"`, 9, "expected \"]\" or an integer length, found \"T\""},
	{`func A(){for "`, 13, "string literal not terminated"},
	{`func A(){for x in "\(y`, 18, "string literal not terminated"},
	{"func A(xs []int){for i, x\u00a0in xs {\n}}", 25, "unexpected character U+00A0 '\\u00a0'"},
	{"func A(xs []int){for i, x@ in xs {\n}}", 25, "unexpected character '@'"},
	{`func A(){for i, x "`, 18, "string literal not terminated"},
	{`func A(){let y = f[escaping func("`, 33, "string literal not terminated"},
	{`func A(){with context{A, "`, 25, "string literal not terminated"},
}

// The kinds of compiler/syntax/token.ergo the stream reads.
const (
	illegal = 0
	eof     = 1
)

func main() {
	failed := 0
	for _, c := range cases {
		if why := check(c.src, c.offset, c.msg); why != "" {
			failed++
			fmt.Printf("FAIL %q: %s\n", c.src, why)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
	fmt.Printf("ok %d cases\n", len(cases))
}

// check is "" when src keeps the stream's invariant and fails to parse
// with S001 at offset with msg, else the first break. A panic of the
// subject is a break.
func check(src string, offset int, msg string) (why string) {
	defer func() {
		if r := recover(); r != nil {
			why = fmt.Sprintf("panic: %v", r)
		}
	}()
	ctx := context.Background()
	words, err := linked.Tokens(ctx, src)
	if err != nil {
		return "tokens: " + err.Error()
	}
	dump, err := linked.Scan(ctx, src)
	if err != nil {
		return "scan: " + err.Error()
	}
	n := len(words) / 3
	kinds, starts, ends := make([]int, n, n+2), make([]int, n, n+2), make([]int, n, n+2)
	for j := range n {
		kinds[j], starts[j], ends[j] = int(words[3*j]), int(words[3*j+1]), int(words[3*j+2])
	}
	lines := strings.Split(strings.TrimSuffix(dump, "\n"), "\n")
	var code string
	var at, length int
	if k, _ := fmt.Sscanf(lines[len(lines)-1], "E %s %d %d", &code, &at, &length); k == 3 {
		end := at + length
		if n > 0 && ends[n-1] > at {
			at, end = ends[n-1], ends[n-1]
		}
		kinds, starts, ends = append(kinds, illegal, eof), append(starts, at, end), append(ends, end, end)
	}
	m := len(kinds)
	if m == 0 || kinds[m-1] != eof {
		return "the stream does not end with EOF"
	}
	prev := 0
	for i := range m {
		switch {
		case kinds[i] == eof && i != m-1:
			return fmt.Sprintf("EOF at token %d of %d", i, m)
		case starts[i] < prev:
			return fmt.Sprintf("token %d starts at %d, before the previous end %d", i, starts[i], prev)
		case ends[i] < starts[i] || ends[i] > len(src):
			return fmt.Sprintf("token %d spans %d..%d in a %d-byte source", i, starts[i], ends[i], len(src))
		}
		prev = ends[i]
	}
	failure, err := linked.Failure(ctx, src)
	if err != nil {
		return "parse: " + err.Error()
	}
	// code offset length line column fix message (compiler/syntax/dump).
	f := strings.SplitN(failure, " ", 7)
	if len(f) < 7 {
		return fmt.Sprintf("got %q, want S001", failure)
	}
	got, _ := strconv.Atoi(f[1])
	if f[0] != "S001" || got != offset || msg != "" && f[6] != msg {
		return fmt.Sprintf("got %s %d %q, want S001 %d %q", f[0], got, f[6], offset, msg)
	}
	return ""
}
