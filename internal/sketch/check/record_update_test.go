package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A record update `T{..base, F: v}` is a composite literal of T (grammar.md,
// Expressions 11; types.md, Structs 3): its base is a value of exactly T,
// typed once and first; it names at least one field, by the keyed-literal
// rules; a field it omits is the base's. The cases hold the decision record's
// refusals with their positions, the generic and union risks, the routes
// that refuse the literal (each must keep the base's own diagnostics and add
// none), and the reference walk, which must read the base as a value.

// TestRecordUpdateBaseRefs: a package function or value named in a record
// update's base is a reference edge for typing and for the walk alike, on
// every route the checker takes: an accepted update, and each refusal of
// the literal or of the base (each types the base, as a value or as an
// orphan, and the walk reads it as a value). The bases are a name, a call
// and a nested update; the CompositeLit.Base row of the literal guards
// (TestLiteralRefsAgree, litNoShape) points here.
func TestRecordUpdateBaseRefs(t *testing.T) {
	const decls = "type Rec { V int }\ntype Other { V int }\ntype Box[T] { V T }\ntype Ints []int\nfunc mkRec(f func()) Rec { Rec{V: 1} }\nfunc fx() {}\nlet pv = Rec{V: 5}\n\n"
	bases := []string{"$N", "mkRec($N)", "Rec{..$N, V: 2}"}
	names := []string{"fx", "pv"}
	routes := []struct{ name, lit string }{
		{"accepted", "Rec{..BASE, V: 1}"},
		{"no field", "Rec{..BASE}"},
		{"unknown type", "Nope{..BASE, V: 1}"},
		{"a slice type", "Ints{..BASE, V: 1}"},
		{"another record type", "Other{..BASE, V: 1}"},
		{"an unknown field", "Rec{..BASE, W: 1}"},
		{"a generic record from its base", "Box{..BASE, V: 1}"},
	}
	n := 0
	for _, r := range routes {
		for _, b := range bases {
			for _, nm := range names {
				base := b
				if nm == "fx" && b != "mkRec($N)" {
					continue // fx is a function value, never a Rec
				}
				if nm == "pv" && b == "mkRec($N)" {
					continue
				}
				body := "func host() {\n\tlet _ = " + strings.Replace(r.lit, "BASE", base, 1) + "\n}\n"
				at := len(decls) + strings.Index(body, "$N")
				src := decls + strings.Replace(body, "$N", nm, 1)
				info, walk, found := check.RefEdge(src, at)
				if !found {
					t.Errorf("%s, %s: no identifier at the name\n%s", r.name, base, src)
					continue
				}
				n++
				if info != "edge" || walk != "edge" {
					t.Errorf("%s, %s, %s: want Info edge and the walk edge, got Info %s and the walk %s\n%s", r.name, base, nm, info, walk, src)
				}
				if p := check.RefWalkProblems(src); len(p) != 0 {
					t.Errorf("%s, %s, %s: the walk and typing disagree: %v\n%s", r.name, base, nm, p, src)
				}
			}
		}
	}
	if n != len(routes)*3 {
		t.Errorf("%d programs, want %d", n, len(routes)*3)
	}
}
