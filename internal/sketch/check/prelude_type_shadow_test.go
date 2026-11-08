package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestPackageTypeHidesPreludeType (L6; grammar.md, Reserved names 2; owner
// q30, 2026-11-08): a package-level type declaration may take the name of
// a prelude or universe type, and the name means the package's type inside
// the package, as Go's package scope hides the universe. A local, a type
// parameter, a package function, let or context of such a name, and any
// declaration of a prelude function's name, stay D-D3's S108. Each case
// lists exactly its diagnostics, CODE@line:col.
func TestPackageTypeHidesPreludeType(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"option-union", "type Option[T] = Present[T]{ V T } | Absent{}\n\nfunc pick(o Option[int]) int {\n\tmatch o {\n\tcase Present[int]{V: v}:\n\t\tv\n\tcase Absent:\n\t\t0\n\t}\n}\n", nil},
		{"option-record", "type Option { Value int }\n\nfunc f(o Option) int { o.Value }\n", nil},
		{"variants", "type Opt = Some{ V int } | None{}\n\nfunc f(o Opt) int {\n\tmatch o {\n\tcase Some{V: v}:\n\t\tv\n\tcase None:\n\t\t0\n\t}\n}\n", nil},
		{"range-and-seq", "type Range { lo int, hi int }\ntype Seq = Cell{ v int } | Nil{}\n\nfunc f(r Range, s Seq) int { r.hi - r.lo }\n", nil},
		// A range literal still builds the prelude's Range.
		{"range-literal", "type Range { lo int }\n\nfunc f() int {\n\tlet mut s = 0\n\tfor i in 0..3 {\n\t\ts += i\n\t}\n\ts\n}\n", nil},
		// string, not int: TestDiagnosticsStayInTheirFile's neutral file
		// uses int, which a package int would hide in it too.
		{"basic-type", "type string { b []byte }\n\nfunc f(x string) int { len(x.b) }\n", nil},
		// A prelude constant or function is no type, so a type of its name
		// stays refused.
		{"type-named-like-constant", "type true { V int }\n", []string{"S108@1:6"}},
		{"type-named-like-function", "type len { V int }\n", []string{"S108@1:6"}},
		// Map[K, V] names the package's Map, in a type and a literal, and a
		// function whose first parameter is it is a function on that type.
		{"map", "type Map[K, V] { keys []K, vals []V }\n\nfunc size[K, V](m Map[K, V]) int { len(m.keys) }\n\nfunc f() int {\n\tlet m = Map[string, int]{keys: []string{\"a\"}, vals: []int{1}}\n\tm.size()\n}\n", nil},
		{"map-not-generic", "type Map { n int }\n\nfunc f(m Map) int { m.n }\n\nfunc g(m Map[string, int]) {}\n", []string{"S108@5:10"}},
		// Two declarations of one name are a duplicate as before.
		{"twice", "type Option { V int }\ntype Option { W int }\n", []string{"S108@2:6"}},
		// Every other declaration of a prelude name stays refused.
		{"function", "func Option() int { 1 }\n", []string{"S108@1:6"}},
		{"package-let", "let Option = 1\n", []string{"S108@1:5"}},
		{"prelude-function", "func len(xs []int) int { 0 }\n", []string{"S108@1:6"}},
		{"type-parameter", "func f[Option comparable](x Option) Option { x }\n", []string{"S108@1:8"}},
		{"local", "func f() int {\n\tlet Option = 1\n\tOption\n}\n", []string{"S108@2:6"}},
		{"local-beside-package-type", "type Option { V int }\n\nfunc f() int {\n\tlet Option = 1\n\tOption\n}\n", []string{"S108@4:6"}},
		{"parameter", "type Option { V int }\n\nfunc f(Option int) int { Option }\n", []string{"S108@3:8"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := rbCodes(c.src); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("want %v, got %v\n%s", c.want, got, c.src)
			}
			if p := check.RefWalkProblems(c.src); len(p) > 0 {
				t.Errorf("reference walk problems %v\n%s", p, c.src)
			}
		})
	}
	if d := check.PreludeDiagnostics(); len(d) > 0 {
		t.Fatalf("the prelude's own declarations are refused: %v", d)
	}
}
