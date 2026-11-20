package check_test

import (
	"strings"
	"testing"
)

// directTok is a direct reader of the program: tok provides the prelude's
// Reader by its own functions, each taking tok by value (types.md,
// Codecs 11). Its reads are placeholders; these rows check, not run.
const directTok = `type tok { at int } implements Reader

func TakeNull(t tok) bool { false }
func Peek(t tok) WireKind { WireNumber }
func Number(t tok) string fails WireWrongKind { "1" }
func Path(t tok) string { "/" }
func Bool(t tok) bool fails WireWrongKind { true }
func Int(t tok) int64 fails WireWrongKind { int64(t.at) }
func Uint(t tok) uint64 fails WireWrongKind { 1 }
func Float(t tok) float64 fails WireWrongKind { 1.0 }
func String(t tok) string fails WireWrongKind { "" }
func StringBytes(t tok, into []byte) []byte fails WireWrongKind { into }
func BeginList(t tok) fails WireWrongKind {}
func BeginObject(t tok) fails WireWrongKind {}
func Tag(t tok) string fails WireMissingField { "" }
func More(t tok) bool { false }
func Key(t tok) string { "" }
func Skip(t tok) {}
func End(t tok) {}

type Mark { V int }
type Bad {}
`

// markLaw is the law rule 11 requires beside Mark's WireRead.
const markLaw = `law MarkReadsOneValue(x Mark) {
	let m = decode[Mark](tok{at: x.V}) catch {
	case _:
		return false
	}
	m == x
}
`

// TestWireReadDiagnostics: the text and position of each refusal of a
// WireRead (types.md, Codecs 11), the form it admits, and its row joining
// a decode's. Rows: diagnostic text (mutants wr-foreign-read-admitted,
// wr-shape-unchecked, wr-reader-not-direct, wr-law-unrequired,
// wr-law-unnamed, wr-row-dropped).
func TestWireReadDiagnostics(t *testing.T) {
	read := "func WireRead(m mut Mark, t tok) fails WireWrongKind { m.V = int(try t.Int()) }\n"
	for _, row := range []struct {
		name, decl string
		at         string // the text the diagnostic's line starts with
		want       string // a part of the one diagnostic's message; "": none
	}{
		{name: "admitted", decl: read + markLaw},
		{name: "another package's type", decl: "func WireRead(n mut int, t tok) fails WireWrongKind {}\n", at: "func WireRead", want: "WireRead reads a type of its own package"},
		{name: "a result", decl: "func WireRead(m mut Mark, t tok) int fails WireWrongKind { 1 }\n" + markLaw, at: "func WireRead", want: "WireRead of Mark is no read of its own: it takes the value to read into and the reader"},
		{name: "by value", decl: "func WireRead(m Mark, t tok) fails WireWrongKind {}\n" + markLaw, at: "func WireRead", want: "WireRead of Mark is no read of its own: it takes the value to read into and the reader"},
		{name: "a reader of no direct form", decl: cellReader + "func WireRead(m mut Mark, s src) fails WireWrongKind {}\n", at: "func WireRead", want: "WireRead of Mark is no read of its own: src does not provide the prelude's Reader by its own functions"},
		{name: "no law", decl: read, at: "func WireRead", want: "WireRead of Mark states no law MarkReadsOneValue(x Mark) beside it"},
		{name: "a law that names no reader", decl: read + "law MarkReadsOneValue(x Mark) {\n\tMark{V: x.V} == x\n}\n", at: "func WireRead", want: "WireRead of Mark states no law MarkReadsOneValue(x Mark) beside it"},
		{name: "its row joins the decode's", decl: "func WireRead(m mut Mark, t tok) fails Bad {}\n" + markLaw + "func f(t tok) Mark fails WireWrongKind | WireMissingField {\n\ttry decode[Mark](t)\n}\n", at: "\ttry decode", want: "Bad"},
	} {
		t.Run(row.name, func(t *testing.T) {
			src := directTok + row.decl
			ds := checkCellSrc(t, "main", src, nil)
			if row.want == "" {
				if len(ds) > 0 {
					t.Fatalf("want no diagnostic, got %v\n%s", ds, src)
				}
				return
			}
			line := 1 + strings.Count(src[:strings.Index(src, row.at)], "\n")
			if len(ds) != 1 || ds[0].Line != line || !strings.Contains(ds[0].Message, row.want) {
				t.Fatalf("want one diagnostic on line %d containing %q, got %v\n%s", line, row.want, ds, src)
			}
		})
	}
}
