package check_test

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestExternErrorRow is G4's checker property (gx-extern-error-row;
// decision O14): a user extern declares fails go.Error exactly when its Go
// target's last result is error. For every function of the allowlist
// whose last result is error, read from the toolchain's own Go sources
// (the oracle; the checker reads the embedded bindings, IG2), the members
// that reach the host aside (they are refused first), and a set of
// functions of the allowlist that return no error, with and without the
// row: a match is clean, and a call of a fallible one checks under try; a
// mismatch is exactly one S108 at the declaration, with the text pinned
// once here.
func TestExternErrorRow(t *testing.T) {
	type target struct {
		path, name string
		errs       bool
	}
	var targets []target
	for _, path := range v1.PureImports {
		for _, name := range goErrorFuncs(t, path) {
			if !slices.Contains(v1.PureImportsExcept[path], name) {
				targets = append(targets, target{path, name, true})
			}
		}
	}
	for _, q := range []string{"strings.ToUpper", "strconv.Itoa", "math.Abs", "encoding/hex.EncodeToString", "path/filepath.Base", "unicode/utf8.RuneLen"} {
		i := strings.LastIndex(q, ".")
		targets = append(targets, target{q[:i], q[i+1:], false})
	}
	for _, tg := range targets {
		for _, row := range []bool{false, true} {
			decl := "extern go " + tg.path + "." + tg.name + " func f(s string) int"
			use := "func Use(s string) int { f(s) }\n"
			if row {
				decl += " fails go.Error"
				use = "func Use(s string) int fails go.Error { try f(s) }\n"
			}
			t.Run(decl, func(t *testing.T) {
				_, diags := check.CheckSource(decl + "\n" + use)
				switch {
				case row == tg.errs:
					if len(diags) != 0 {
						t.Fatalf("want clean, got\n\t%s", describe(diags))
					}
					return
				case tg.errs:
					want := "extern go " + tg.path + "." + tg.name + " returns an error; declare it in the row: fails go.Error"
					if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 1 || diags[0].Message != want {
						t.Fatalf("want one S108 %q at line 1, got\n\t%s", want, describe(diags))
					}
				default:
					want := "extern go " + tg.path + "." + tg.name + " returns no error; a user extern fails go.Error only when its Go function's last result is error"
					if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 1 || diags[0].Message != want {
						t.Fatalf("want one S108 %q at line 1, got\n\t%s", want, describe(diags))
					}
				}
			})
		}
	}
}

// goErrorFuncs reads the exported top-level functions of the Go package
// path whose last result is error from the toolchain's sources for this
// GOOS and GOARCH: the oracle of TestExternErrorRow, independent of the
// bindings the checker reads.
func goErrorFuncs(t *testing.T, path string) []string {
	t.Helper()
	p, err := build.Default.Import(path, "", 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, name := range p.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil {
				continue
			}
			list := fd.Type.Results.List
			if id, ok := list[len(list)-1].Type.(*ast.Ident); ok && id.Name == "error" {
				out = append(out, fd.Name.Name)
			}
		}
	}
	return out
}
