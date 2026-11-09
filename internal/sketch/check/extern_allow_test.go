package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
)

// allowlist is the pure allowlist as G9 decides it (std-1 plan, section 3),
// with path/filepath from std-align S3a, math/bits from std-text-runes and
// unicode and unicode/utf16 from G2 (lane G), and crypto/sha256,
// encoding/base64, encoding/hex and hash/crc32 from G3: the oracle for
// check.PureGoPackages, written out independently of it.
var allowlist = []string{"crypto/sha256", "encoding/base64", "encoding/hex", "hash/crc32", "math", "math/bits", "path/filepath", "slices", "sort", "strconv", "strings", "unicode", "unicode/utf16", "unicode/utf8"}

// TestPureGoPackages pins the shared list: sorted, the plans' fourteen packages,
// and a copy (a caller that changes it does not change the rule).
func TestPureGoPackages(t *testing.T) {
	got := check.PureGoPackages()
	if !slices.Equal(got, allowlist) {
		t.Fatalf("the pure allowlist is %v, want %v", got, allowlist)
	}
	got[0] = "os"
	if check.PureGoPackage("os") || !check.PureGoPackage("math") {
		t.Fatal("PureGoPackages returned the list itself, not a copy")
	}
}

// TestNoCheckerPackageTable is G9's lint (round 2 M1): the pure allowlist
// has one owner, runtime/v1's PureImports (G5b), and the checker declares
// no package table of its own. In the checker's non-test sources, no
// composite literal (a slice, array or map, by element or key) and no
// switch (over its case lists) names two or more allowlisted packages as
// string literals. A second table in any of those shapes fails here.
func TestNoCheckerPackageTable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		ast.Inspect(f, func(n ast.Node) bool {
			var exprs []ast.Expr
			switch n := n.(type) {
			case *ast.CompositeLit:
				for _, e := range n.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						exprs = append(exprs, kv.Key, kv.Value)
					} else {
						exprs = append(exprs, e)
					}
				}
			case *ast.SwitchStmt:
				for _, s := range n.Body.List {
					exprs = append(exprs, s.(*ast.CaseClause).List...)
				}
			default:
				return true
			}
			var named []string
			for _, e := range exprs {
				b, ok := e.(*ast.BasicLit)
				if !ok || b.Kind != token.STRING {
					continue
				}
				if s, err := strconv.Unquote(b.Value); err == nil && slices.Contains(allowlist, s) {
					named = append(named, s)
				}
			}
			if len(named) >= 2 {
				t.Errorf("%s: a package table %v in the checker; read v1.PureImports instead", fset.Position(n.Pos()), named)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("no checker source scanned")
	}
}

// TestExternAllowlistStdExempt: std's externs bind runtime/v1, which no
// allowlist names, and every std package and the prelude still check
// clean: the allowlist is a rule for user externs only.
func TestExternAllowlistStdExempt(t *testing.T) {
	if ds := check.PreludeDiagnostics(); len(ds) != 0 {
		t.Fatalf("the prelude: %s", describe(ds))
	}
	externs := 0
	for _, path := range std.Packages() {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, f := range pkg.Funcs {
			if f.Decl != nil && f.Decl.Extern != nil {
				externs++
				if p, _ := f.Decl.Extern.Split(); check.PureGoPackage(p) {
					t.Errorf("%s.%s binds %s, an allowlisted package: the exemption is not tested by it", path, f.Name, p)
				}
			}
		}
	}
	if externs == 0 {
		t.Fatal("no std extern: the exemption is not tested")
	}
}
