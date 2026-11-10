package syntax

import "testing"

// TestPairLawDraws: the round-trip law of a by-name pair draws x itself for
// an open type, so it reaches every value, and a drawn b through ParseX
// for a type only its package builds (types.md, Codecs 7). It kills the
// mutant l11b-s1 (openType always false), which every law still passes:
// the b form over random names is vacuous for a union of four levels.
func TestPairLawDraws(t *testing.T) {
	for _, tc := range []struct{ src, param string }{
		{"type Level = Debug | Info\ntype Debug {}\ntype Info { Verbose bool }\ntype Bad { Name string }\nfunc ParseLevel(s string) Level fails Bad { Debug }\nfunc Wire(l Level) string { \"debug\" }\n", "Level"},
		{"type host { name string }\ntype bad { Text string }\nfunc parseHost(s string) host fails bad { host{name: s} }\nfunc Wire(h host) string { h.name }\n", "string"},
	} {
		f, err := Parse(tc.src)
		if err != nil {
			t.Fatal(err)
		}
		var law *LawDecl
		for _, d := range f.Decls {
			if l, ok := d.(*LawDecl); ok && l.Pair != nil {
				law = l
			}
		}
		if law == nil {
			t.Fatalf("no round-trip law derived from\n%s", tc.src)
		}
		if id, ok := law.Params.List[0].Type.(*Ident); !ok || id.Name != tc.param {
			t.Errorf("%s draws %T %v, want %s", law.Name.Name, law.Params.List[0].Type, law.Params.List[0].Type, tc.param)
		}
	}
}
