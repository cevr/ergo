package syntax

import (
	"testing"
)

// TestPairLawDraws: the round-trip law of a by-name pair draws x of X, in
// both forms of the pair (types.md, Codecs 7; GX11), and calls Wire with
// one argument, the empty buffer, in the append form, ParseX(b []byte)
// with Wire(x X, into []byte) []byte. Whether X has its own generator, so
// that the law runs, is the checker's (check TestPairLawDrawsOwnValues).
//
// It kills gx11-pair-append-no-law (the append form derives no law, as it
// did in GX10) and gx11-pair-append-no-into (x.Wire() with no buffer).
func TestPairLawDraws(t *testing.T) {
	for _, form := range []struct {
		parse, wire string
		args        int
	}{
		{"func ParseX(s string) X fails Bad { return Bad{Name: s} }", "func Wire(x X) string { \"x\" }", 0},
		{"func ParseX(b []byte) X fails Bad { return Bad{Name: \"x\"} }", "func Wire(x X, into []byte) []byte { into }", 1},
	} {
		src := "type X { f int }\ntype Bad { Name string }\n" + form.parse + "\n" + form.wire + "\n"
		f, err := Parse(src)
		if err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		var law *LawDecl
		for _, d := range f.Decls {
			if l, ok := d.(*LawDecl); ok && l.Pair != nil {
				law = l
			}
		}
		if law == nil {
			t.Fatalf("no round-trip law derived from\n%s", src)
		}
		p := law.Params.List[0]
		if id, ok := p.Type.(*Ident); !ok || id.Name != "X" || p.Names[0].Name != "x" {
			t.Fatalf("%s draws %v %v, want x X\n%s", law.Name.Name, p.Names[0].Name, p.Type, src)
		}
		Inspect(law.Body, func(n Node) bool {
			if c, ok := n.(*CallExpr); ok {
				if s, ok := c.Fun.(*SelectorExpr); ok && s.Sel.Name == "Wire" && len(c.Args) != form.args {
					t.Fatalf("%s calls Wire with %d arguments, want %d\n%s", law.Name.Name, len(c.Args), form.args, src)
				}
			}
			return true
		})
	}
}
