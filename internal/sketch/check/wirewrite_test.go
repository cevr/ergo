package check_test

import (
	"strings"
	"testing"
)

// cellPen is a sealed writer of the program: Pen provides the prelude's
// Writer, and its one provider pen is a cell.
const cellPen = `type Pen {
	Null(),
	Number(text string),
	Bool(b bool),
	Int(n int64),
	Uint(n uint64),
	Float(f float64),
	String(s string),
	BeginList(n int),
	EndList(),
	BeginObject(n int),
	Tag(name string),
	Key(k string),
	EndObject(),
	seal(),
}

type pen { n int }

func Null(p *pen) { p.n = p.n + 1 }
func Number(p *pen, text string) { p.n = p.n + 1 }
func Bool(p *pen, b bool) { p.n = p.n + 1 }
func Int(p *pen, n int64) { p.n = p.n + 1 }
func Uint(p *pen, n uint64) { p.n = p.n + 1 }
func Float(p *pen, f float64) { p.n = p.n + 1 }
func String(p *pen, s string) { p.n = p.n + 1 }
func BeginList(p *pen, n int) { p.n = p.n + 1 }
func EndList(p *pen) { p.n = p.n + 1 }
func BeginObject(p *pen, n int) { p.n = p.n + 1 }
func Tag(p *pen, name string) { p.n = p.n + 1 }
func Key(p *pen, k string) { p.n = p.n + 1 }
func EndObject(p *pen) { p.n = p.n + 1 }
func seal(p pen) {}

func NewPen() Pen { pen{n: 0} }

type Mark { V int }
`

// TestWireWriteDiagnostics: the text and position of each refusal of a
// WireWrite (types.md, Codecs 9), and the form it admits. Rows:
// diagnostic text (mutants cx1c-foreign-write-admitted,
// cx1c-shape-unchecked, cx1c-provider-not-writer).
func TestWireWriteDiagnostics(t *testing.T) {
	for _, row := range []struct {
		name, decl string
		want       string // a part of the one diagnostic's message; "": none
	}{
		{name: "admitted", decl: "func WireWrite(m Mark, p *pen) { p.n = p.n + m.V }\n"},
		{name: "another package's type", decl: "func WireWrite(n int, p *pen) { p.n = p.n + n }\n", want: "WireWrite writes a type of its own package"},
		{name: "a result", decl: "func WireWrite(m Mark, p *pen) int { m.V }\n", want: "WireWrite of Mark is no write of its own: it takes the value and the writer's provider"},
		{name: "a provider of no Writer", decl: "func WireWrite(m Mark, s *src) { s.at = m.V }\n", want: "WireWrite of Mark is no write of its own: src is not the cell provider of a sealed operation type of package main that provides the prelude's Writer"},
	} {
		t.Run(row.name, func(t *testing.T) {
			src := cellPen + cellReader + row.decl
			ds := checkCellSrc(t, "main", src, nil)
			if row.want == "" {
				if len(ds) > 0 {
					t.Fatalf("want no diagnostic, got %v\n%s", ds, src)
				}
				return
			}
			line := 1 + strings.Count(src[:strings.Index(src, "func WireWrite")], "\n")
			if len(ds) != 1 || ds[0].Line != line || !strings.Contains(ds[0].Message, row.want) {
				t.Fatalf("want one diagnostic on line %d containing %q, got %v\n%s", line, row.want, ds, src)
			}
		})
	}
}
