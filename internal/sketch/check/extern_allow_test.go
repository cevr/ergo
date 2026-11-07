package check_test

import (
	"fmt"
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
// with path/filepath from std-align S3a: the oracle for
// check.PureGoPackages, written out independently of it.
var allowlist = []string{"math", "path/filepath", "slices", "sort", "strconv", "strings", "unicode/utf8"}

// TestPureGoPackages pins the shared list: sorted, the plans' seven packages,
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

// externAllowPaths are the Go import paths of the property: each package
// of the allowlist, host packages, pure packages outside the list, and the
// near misses of an allowlisted path (a subpackage, a longer name, a
// prefix, a vendored or module copy, a case or separator variant).
func externAllowPaths() []string {
	paths := append([]string(nil), allowlist...)
	paths = append(paths, "os", "os/exec", "io", "io/fs", "fmt", "time", "net/http",
		"math/rand", "math/rand/v2", "crypto/rand", "runtime", "syscall", "log",
		"sync", "sync/atomic", "path/filepath", "embed", "plugin", "reflect",
		"math/bits", "math/cmplx", "unicode", "cmp", "text/template",
		"bufio", "errors", "maps", "bytes", "context")
	for _, p := range allowlist {
		paths = append(paths, p+"/x", p+"x", "x"+p, p[:len(p)-1], "vendor/"+p,
			"internal/"+p, "std/"+p, "./"+p, p+"/", "/"+p, strings.ToUpper(p),
			"golang.org/x/"+p, "example.com/"+p)
	}
	paths = append(paths, "unicode/utf16", "unicode/utf8/x", "utf8", "unicode\\utf8", "")
	return paths
}

// TestExternAllowlist is G9's property: a user extern whose Go package is
// in the pure allowlist checks clean; any other package is exactly one
// S109 at the declaration. Among those, a module path (a dot in its first
// element) keeps its #202 text and every other path gets the host text,
// which names the target and the std route.
func TestExternAllowlist(t *testing.T) {
	for _, path := range externAllowPaths() {
		target := "F"
		if path != "" {
			target = path + ".F"
		}
		src := "extern go " + target + " func f(s string) string\nfunc Use() { _ = f(\"x\") }\n"
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			if check.PureGoPackage(path) != slices.Contains(allowlist, path) {
				t.Fatalf("PureGoPackage(%q) = %v", path, check.PureGoPackage(path))
			}
			_, diags := check.CheckSource(src)
			if slices.Contains(allowlist, path) {
				if len(diags) != 0 {
					t.Fatalf("want clean, got\n\t%s", describe(diags))
				}
				return
			}
			if malformed(path) {
				// The parser refuses the target before the checker sees it.
				if len(diags) != 1 || diags[0].Code != "S001" {
					t.Fatalf("want one S001 for a malformed target, got\n\t%s", describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S109" || diags[0].Line != 1 {
				t.Fatalf("want one S109 on line 1, got\n\t%s", describe(diags))
			}
			msg := diags[0].Message
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				if !strings.Contains(msg, "#202") || strings.Contains(msg, "reaches the host") {
					t.Fatalf("a module path keeps its #202 text: %s", msg)
				}
				return
			}
			want := "extern go " + target + " reaches the host; wait for "
			if !strings.HasPrefix(msg, want) || !strings.Contains(msg, " or #202 ") {
				t.Fatalf("want the host text %q…, got %s", want, msg)
			}
		})
	}
}

// TestExternAllowlistExcept (std-align S3a): path/filepath is allowed, but
// its members that reach the host are refused as a host package is; a
// lexical member checks clean.
func TestExternAllowlistExcept(t *testing.T) {
	for _, name := range []string{"Abs", "EvalSymlinks", "Glob", "Walk", "WalkDir"} {
		target := "path/filepath." + name
		_, diags := check.CheckSource("extern go " + target + " func f(s string) string\nfunc Use() { _ = f(\"x\") }\n")
		if len(diags) != 1 || diags[0].Code != "S109" || !strings.HasPrefix(diags[0].Message, "extern go "+target+" reaches the host") {
			t.Errorf("%s: want one S109 with the host text, got\n\t%s", target, describe(diags))
		}
	}
	if _, diags := check.CheckSource("extern go path/filepath.Clean func f(s string) string\nfunc Use() { _ = f(\"x\") }\n"); len(diags) != 0 {
		t.Errorf("path/filepath.Clean: want clean, got\n\t%s", describe(diags))
	}
}

// malformed reports a path the parser refuses as an extern target (an
// empty path, an empty or relative element, a backslash).
func malformed(path string) bool {
	return path == "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, ".") ||
		strings.HasSuffix(path, "/") || strings.Contains(path, "\\")
}

// TestExternAllowlistHostText pins F7's message for os (std-1 plan, G9;
// round 2: std os.Process is G2's and not on main yet, so the text waits
// for it).
func TestExternAllowlistHostText(t *testing.T) {
	_, diags := check.CheckSource("extern go os.Getenv func Getenv(k string) string\n")
	const want = "extern go os.Getenv reaches the host; wait for std os.Process or #202"
	if len(diags) != 1 || diags[0].Code != "S109" || !strings.HasPrefix(diags[0].Message, want) {
		t.Fatalf("want one S109 starting %q, got\n\t%s", want, describe(diags))
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
