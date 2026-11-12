package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintRulesReadNoSpelling holds lint to checked facts (plan LINT0;
// docs/specs/custom-lint.md: "a rule must not reconstruct import aliases,
// lexical shadowing or effect contracts from identifier spelling"). It
// parses every non-test Go file of internal/sketch/lint and fails on:
//
//   - anywhere: a comparison (==, !=, a switch case, or a strings test such
//     as HasPrefix or EqualFold) of a spelling, a .Name, .Text, .Src,
//     .Value or .Path selector or a .String() call, with a string literal
//     or a string constant of the package;
//   - in a rule file (rule_*.go): any such comparison of anything with a
//     string literal or constant, any call of package strings, and any
//     comparison of two spellings (a.Name == b.Name);
//   - a rule (a &Rule{…} literal) declared outside a rule file.
//
// A rule matches the resolved object a fact names (check.Info's Uses,
// Calls, Patterns, a type's identity), so a same-spelled local, an alias or
// a type of another package cannot satisfy it.
func TestLintRulesReadNoSpelling(t *testing.T) {
	dir := filepath.Join("..", "sketch", "lint")
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no lint sources under %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	constants := map[string]bool{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(name)] = f
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) && stringLit(vs.Values[i]) {
						constants[n.Name] = true
					}
				}
			}
		}
	}
	for base, f := range files {
		ruleFile := strings.HasPrefix(base, "rule_")
		text := func(e ast.Expr) bool {
			if stringLit(e) {
				return true
			}
			id, ok := e.(*ast.Ident)
			return ok && constants[id.Name]
		}
		compared := func(at token.Pos, a, b ast.Expr) {
			switch {
			case spelling(a) && text(b), spelling(b) && text(a):
				t.Errorf("%s: compares a spelling with a string; match the resolved object a checked fact names", fset.Position(at))
			case ruleFile && (text(a) || text(b)):
				t.Errorf("%s: a rule compares with a string; a rule reads checked facts only", fset.Position(at))
			case ruleFile && spelling(a) && spelling(b):
				t.Errorf("%s: a rule compares two spellings; compare the resolved objects", fset.Position(at))
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if n.Op == token.EQL || n.Op == token.NEQ {
					compared(n.OpPos, n.X, n.Y)
				}
			case *ast.SwitchStmt:
				if n.Tag != nil {
					for _, c := range n.Body.List {
						for _, e := range c.(*ast.CaseClause).List {
							compared(e.Pos(), n.Tag, e)
						}
					}
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					break
				}
				if pkg, isPkg := sel.X.(*ast.Ident); isPkg && pkg.Name == "strings" {
					if ruleFile {
						t.Errorf("%s: a rule calls strings.%s; a rule reads checked facts only", fset.Position(n.Pos()), sel.Sel.Name)
					} else if stringTests[sel.Sel.Name] && len(n.Args) >= 2 {
						compared(n.Pos(), n.Args[0], n.Args[1])
					}
				}
			case *ast.CompositeLit:
				if id, ok := n.Type.(*ast.Ident); ok && id.Name == "Rule" && !ruleFile {
					t.Errorf("%s: a rule is declared outside a rule_*.go file, where the spelling guard is strict", fset.Position(n.Pos()))
				}
			}
			return true
		})
	}
}

// stringTests are the strings functions that compare their first two
// arguments.
var stringTests = map[string]bool{
	"HasPrefix": true, "HasSuffix": true, "Contains": true, "EqualFold": true, "Index": true,
	"Cut": true, "CutPrefix": true, "CutSuffix": true, "TrimPrefix": true, "TrimSuffix": true, "Compare": true,
}

func stringLit(e ast.Expr) bool {
	b, ok := e.(*ast.BasicLit)
	return ok && b.Kind == token.STRING
}

// spelling reports an expression that reads a name's text: x.Name, x.Text,
// x.Src, x.Value, x.Path or x.String().
func spelling(e ast.Expr) bool {
	if c, ok := e.(*ast.CallExpr); ok {
		if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "String" {
			return true
		}
		return false
	}
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch s.Sel.Name {
	case "Name", "Text", "Src", "Value", "Path":
		return true
	}
	return false
}
