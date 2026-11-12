package residue_test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckerNamesNoStdPackage holds the inventory of remaining package
// identity comparisons exact. A new identity rule requires an explicit
// inventory edit; deleting one requires deleting its row too.
func TestCheckerNamesNoStdPackage(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	repo := strings.TrimSpace(string(root))
	allow := readAllow(t, "testdata/std-names-allow.txt")
	used := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(repo, "internal/sketch/check/*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("checker files: %v", err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		fs := token.NewFileSet()
		f, err := parser.ParseFile(fs, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (id.Name == "SealedStd" || id.Name == "exitCodeRange" || id.Name == "stdExternResultSet") {
				t.Errorf("%s: std semantics must use declarations, not %s", fs.Position(id.Pos()), id.Name)
			}
			b, ok := n.(*ast.BinaryExpr)
			if !ok || (b.Op != token.EQL && b.Op != token.NEQ) {
				return true
			}
			if id, ok := b.Y.(*ast.Ident); ok && id.Name == "nil" {
				return true // syntax import-path presence, not a package identity
			}
			isPath := func(x ast.Expr) bool { s, ok := x.(*ast.SelectorExpr); return ok && s.Sel.Name == "Path" }
			if !isPath(b.X) && !isPath(b.Y) {
				return true
			}
			var text bytes.Buffer
			if err := format.Node(&text, fs, b); err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(repo, path)
			if err != nil {
				t.Fatal(err)
			}
			key := filepath.ToSlash(rel) + "\t" + text.String()
			if _, ok := allow[key]; !ok {
				t.Errorf("%s: package identity rule not inventoried: %s", fs.Position(b.Pos()), key)
			} else {
				used[key] = true
			}
			return true
		})
	}
	for _, key := range staleRows(allow, used) {
		t.Errorf("std identity inventory: stale row %q", key)
	}
}
