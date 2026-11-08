package check_test

import (
	"strings"
	"testing"
)

// TestLocalShadowsPackageType (L5; types.md, Scope 1; decision O16): a
// local in a body may take the name of a package type, as in Go. In its
// scope the name is the local in value and pattern positions and the type
// in type positions; outside its scope the name is the type again. A
// package-level let, a universe type that is no prelude name, a prelude
// name and a match pattern keep their rules. Each case lists exactly its
// diagnostics, CODE@line:col.
func TestLocalShadowsPackageType(t *testing.T) {
	const decls = "type rec { n int }\ntype alt = va{ n int } | vb{}\ntype Bang {}\n\nfunc risky(n int) int fails Bang {\n\tif n < 0 {\n\t\treturn Bang\n\t}\n\tn\n}\n\n"
	for _, c := range []struct {
		name, src string
		want      []string
	}{
		{"let-field", "func f() int {\n\tlet rec = rec{n: 1}\n\trec.n\n}\n", nil},
		{"let-type-position", "func f() int {\n\tlet rec = rec{n: 1}\n\tlet w rec = rec\n\tlet v = rec{n: w.n}\n\tv.n\n}\n", nil},
		{"let-literal-beside-read", "func f() bool {\n\tlet rec = rec{n: 7}\n\trec == rec{n: 7}\n}\n", nil},
		{"tuple-pattern", "func f() int {\n\tlet (rec, m) = (rec{n: 1}, 2)\n\trec.n + m\n}\n", nil},
		{"record-pattern", "func f(r rec) int {\n\tlet rec{n: rec} = r\n\trec\n}\n", nil},
		{"for-second-name", "func f(xs []rec) int {\n\tlet mut s = 0\n\tfor i, rec in xs {\n\t\ts += i + rec.n\n\t}\n\ts\n}\n", nil},
		{"parameter", "func f(rec rec) int {\n\trec.n\n}\n", nil},
		{"literal-parameter", "func f() int {\n\tlet g = func(rec rec) int { rec.n }\n\tg(rec{n: 2})\n}\n", nil},
		{"literal-captures", "func f() int {\n\tlet rec = rec{n: 1}\n\tlet g = func() int { rec.n }\n\tg()\n}\n", nil},
		// After the block, vb is the variant again, a value by its name.
		{"scope-ends", "func f(b bool) alt {\n\tif b {\n\t\tlet vb = 3\n\t\tlet _ = vb\n\t}\n\tvb\n}\n", nil},
		{"scope-ends-in-literal", "func f() alt {\n\tlet g = func() int {\n\t\tlet vb = 3\n\t\tvb\n\t}\n\tlet _ = g()\n\tvb\n}\n", nil},
		// A local named like the type is unread when only type positions
		// name it.
		{"type-position-only", "func f() int {\n\tlet rec = 1\n\tlet w rec = rec{n: 2}\n\tw.n\n}\n", []string{"S110@13:6"}},
		// A package-level let names the package scope, which holds the
		// type: its single name is a duplicate, and a type name in its
		// pattern stays a type test.
		{"package-let", "let rec = rec{n: 1}\n", []string{"S108@12:5"}},
		{"package-let-pattern", "let (rec, x) = (rec{n: 1}, 2)\n\nfunc f() int { x }\n", nil},
		// `error` is a universe type and no prelude name: a let of it still
		// tests the type.
		{"universe-error", "func f() int {\n\tlet error = 1\n\t0\n}\n", []string{"S108@13:6"}},
		// A prelude type stays protected (D-D3; L6 widens only package
		// declarations).
		{"prelude-type", "func f() int {\n\tlet Option = 1\n\tOption\n}\n", []string{"S108@13:6"}},
		// In a catch arm, a lone name is the local in scope, not the error
		// type: it binds the rest, which D-D2 refuses, unread.
		{"catch-rest", "func f(v int) int {\n\tlet Bang = 1\n\tlet n = risky(v) catch {\n\tcase Bang:\n\t\t0\n\t}\n\tn + Bang\n}\n", []string{"S108@15:7", "S110@15:7"}},
		// A match pattern names the type (Totality 1): a field binder named
		// like a type is a type test, refused here.
		{"match-pattern", "func f(a alt) int {\n\tmatch a {\n\tcase va{n: rec}:\n\t\t1\n\tcase vb:\n\t\t0\n\t}\n}\n", []string{"S108@14:13"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := decls + c.src
			if got := rbCodes(src); strings.Join(got, " ") != strings.Join(c.want, " ") {
				t.Errorf("want %v, got %v\n%s", c.want, got, src)
			}
		})
	}
}
