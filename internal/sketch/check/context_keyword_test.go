package check_test

import (
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The rules of 196w (the construct that binds services is spelled
// context): wire is an ordinary identifier, context is a keyword, and an
// import whose path ends in a keyword needs an alias.

// TestWireIsAnIdentifier: wire is not a keyword and has no compatibility
// path, so it names a let, a parameter and a type like any identifier.
func TestWireIsAnIdentifier(t *testing.T) {
	if syntax.IsKeyword("wire") || slices.Contains(syntax.Keywords(), "wire") {
		t.Fatal("wire is a keyword")
	}
	for _, src := range []string{
		"let wire = 1\n",
		"func f(wire int) int { wire }\n",
		"type wire { N int }\nfunc f(w wire) int { w.N }\n",
	} {
		if _, d := check.CheckSource(src); len(d) != 0 {
			t.Errorf("%q: %s", src, describe(d))
		}
	}
}

// keywordPathImporter adds a package whose path ends in a keyword to the
// standard library.
type keywordPathImporter struct{ pkg *check.Package }

func (k keywordPathImporter) Import(path string) (*check.Package, error) {
	if path == "x/match" {
		return k.pkg, nil
	}
	return check.Std.Import(path)
}

// TestImportGoKeywordPath: an import path whose last element is a keyword
// binds no name, so it needs an alias (grammar.md, Declarations rule 1).
// Go's context package is the common case, and the message gives the fix.
// The aliased Go import reads its binding; unused, it is S110.
func TestImportGoKeywordPath(t *testing.T) {
	for _, tc := range []struct {
		name, src, code, msg string
		line, col            int
	}{
		{"go-context", "import go context\nfunc main() {}\n", check.CodeType,
			"context is a keyword; import the package under another name: import go gocontext context", 1, 11},
		{"go-deep-path", "import go golang.org/x/net/context\nfunc main() {}\n", check.CodeType,
			"context is a keyword; import the package under another name: import go gocontext golang.org/x/net/context", 1, 11},
		{"go-aliased", "import go gocontext context\nfunc main() {}\n", check.CodeUnused,
			"package gocontext is imported and not used", 1, 8},
		{"plain-keyword-path", "import x/match\nfunc main() {}\n", check.CodeType,
			"match is a keyword; import the package under another name: import matchpkg x/match", 1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 || d[0].Code != tc.code || d[0].Message != tc.msg || d[0].Line != tc.line || d[0].Column != tc.col {
				t.Fatalf("want one %s at %d:%d %q, got %s", tc.code, tc.line, tc.col, tc.msg, describe(d))
			}
		})
	}

	// The alias of a keyword path resolves a clash, so the alias rule
	// (D-D7) admits it.
	f, err := syntax.Parse("type T { N int }\n")
	if err != nil {
		t.Fatal(err)
	}
	pkg, d := check.Check("x/match", []*syntax.File{f}, nil)
	if len(d) != 0 {
		t.Fatalf("x/match: %s", describe(d))
	}
	if d := checkWith(t, keywordPathImporter{pkg}, "import matchpkg x/match\nfunc f() matchpkg.T { matchpkg.T{N: 1} }\n"); len(d) != 0 {
		t.Errorf("aliased keyword path: %s", describe(d))
	}
}
