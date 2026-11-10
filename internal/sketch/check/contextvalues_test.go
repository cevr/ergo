package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestContextValueDiagnostics: the text and line of each refusal of a
// conversion to a Context value (rows.md, Context values). Mutants: the
// missing-capability check dropped (value-missing-cap-unchecked), the
// row-parameter refusal dropped (value-row-param-built), a Context made
// by a composite literal (context-literal-admitted), and a provider with
// a Close admitted (value-owns-close). What a value installs is
// TestContextValuesAgree's (lower).
func TestContextValueDiagnostics(t *testing.T) {
	const head = "type A { Get() int }\ntype B { Get() int }\ntype Box { n int }\nfunc Get(b Box) int { b.n }\n"
	rows := []struct{ name, src, want string }{
		{"missing capability", head + "func f() {\n\tlet v Context[A | B] = context { A: Box{n: 1} }\n\t_ = v\n}\n", "S102:6 this context binds no B, which Context[A | B] provides"},
		{"row parameter", head + "func f[R]() Context[R] {\n\tcontext { A: Box{n: 1} }\n}\n", "S108:6 a context converts only to a Context of concrete capabilities, and Context[R] names the row parameter R"},
		{"composite literal", head + "func f() {\n\tlet v = Context[A]{}\n\t_ = v\n}\n", "S108:6 a Context value is made from a context"},
		{"provider with a Close", head + "type Conn { n int }\nfunc Get(c Conn) int { c.n }\nfunc Close(c Conn) {}\nfunc open() Conn { Conn{n: 1} }\nfunc f() {\n\tlet v Context[A] = context { A: open() }\n\t_ = v\n}\n", "S109:10 a Context value cannot hold a provider with a Close"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
