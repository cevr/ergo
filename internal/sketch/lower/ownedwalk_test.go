package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOwnedParamsAgreeWithEveryWalk (FLAT-IR-ARENA): ownedParams, which
// walks a function's body only when one of its parameters holds a slice or
// Map, decides the owned parameters of every function of the program and
// of every library it reaches as the reference does (ownedParamsWalkAll:
// a value walk of every function); and it walks exactly the functions
// with such a parameter (ownedWalks), where the reference walked all.
//
// Mutants (lower mutants.json): flat-ir-arena-owned-prefilter-none,
// flat-ir-arena-owned-prefilter-all.
func TestOwnedParamsAgreeWithEveryWalk(t *testing.T) {
	src := "import json\nimport strings\nimport tasks\n\nfunc grow(xs []int, n int) []int {\n\tlet mut ys = xs\n\tys = ys.Append(n)\n\tys\n}\n\nfunc size(m Map[string, int]) int {\n\tm.Len()\n}\n\nfunc uses(a json.Text, b tasks.FailFast) string {\n\tstrings.ToUpper(\"a\")\n}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("the probe does not check: %v", diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	funcsOf := func(l *lowerer) []*check.Func {
		var out []*check.Func
		for _, p := range append([]*check.Package{l.pkg}, l.libraries()...) {
			out = append(out, l.canon.funcs(p)...)
		}
		return out
	}
	ownedParamsWalkAll = true
	defer func() { ownedParamsWalkAll = false }()
	ref := newLowerer(u)
	want := map[*check.Func][]ownedParam{}
	for _, f := range funcsOf(ref) {
		want[f] = ref.ownedParams(f)
	}
	ownedParamsWalkAll = false
	l := newLowerer(u)
	fns := funcsOf(l)
	owned, candidates := 0, 0
	for _, f := range fns {
		got := l.ownedParams(f)
		w := want[f]
		if len(got) != len(w) {
			t.Fatalf("ownedParams(%s.%s) = %d owned parameters, want %d", f.Pkg.Path, f.Name, len(got), len(w))
		}
		for i := range got {
			if got[i] != w[i] {
				t.Fatalf("ownedParams(%s.%s)[%d] = %+v, want %+v", f.Pkg.Path, f.Name, i, got[i], w[i])
			}
		}
		if len(got) > 0 {
			owned++
		}
		if d := f.Decl; d != nil && d.Body != nil && d.Extern == nil && f.Pkg != nil && f.Pkg.Info != nil && d.Type != nil && d.Type.Params != nil {
			if def, ok := f.Pkg.Info.Defs[d.Name].(*check.Func); ok && def.Sig != nil {
				holds := false
				for _, field := range d.Type.Params.List {
					for _, id := range field.Names {
						if v, ok := f.Pkg.Info.Defs[id].(*check.Var); ok && l.holdsValue(v.Type) {
							holds = true
						}
					}
				}
				if holds {
					candidates++
				}
			}
		}
	}
	if owned == 0 || candidates == len(fns) {
		t.Fatalf("the probe reaches %d functions, %d with an owned parameter and %d with a parameter that holds a value; the property needs some of each and some with none", len(fns), owned, candidates)
	}
	t.Logf("%d functions, %d with a parameter that holds a value, %d with an owned parameter; walks %d (reference %d)", len(fns), candidates, owned, l.ownedWalks, ref.ownedWalks)
	if l.ownedWalks != candidates {
		t.Errorf("%d value walks for %d functions with a parameter that holds a value (of %d functions; the reference walked %d)", l.ownedWalks, candidates, len(fns), ref.ownedWalks)
	}
}
