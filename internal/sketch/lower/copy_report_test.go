package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// copiesOf lowers src as a program and returns its main.go, its parsed
// file, every copy site the lowering recorded, one per copy it wrote
// (knownStats.Copies, before the report merges a site written twice), and
// the functions with owned parameters (CopyReport.Owned).
func copiesOf(t *testing.T, src string) (string, *syntax.File, []CopySite, []OwnedFunc) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("checker diagnostics %v\n%s", diags, src)
	}
	l := newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	code, err := l.program()
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return string(code), file, l.stats.Copies, l.ownedFuncs()
}

// goCopies counts the copy calls of the lowered Go mainGo by kind
// (copyKind): Go's slices.Clone, slices.Clip and maps.Clone and each
// generated ergoClone function, except inside a generated copy function (a
// deep copy is one copy at its site).
func goCopies(t *testing.T, mainGo string) map[string]int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", mainGo, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("the lowered Go does not parse: %v", err)
	}
	out := map[string]int{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fd.Name.Name, goHelper("ergoClone")) {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if kind := copyKind(goCallee(call.Fun) + "("); kind != "" {
					out[kind]++
				}
			}
			return true
		})
	}
	return out
}

// goCallee is the text of a callee: pkg.Name or Name.
func goCallee(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "." + x.Sel.Name
		}
	case *ast.IndexExpr:
		return goCallee(x.X)
	case *ast.IndexListExpr:
		return goCallee(x.X)
	}
	return ""
}

// exprStarts is the set of line:column positions of the expressions of
// file (their Pos, which the report locates).
func exprStarts(file *syntax.File) map[[2]int]bool {
	out := map[[2]int]bool{}
	syntax.Inspect(file, func(n syntax.Node) bool {
		if x, ok := n.(syntax.Expr); ok && x != nil {
			line, col := file.Position(x.Pos())
			out[[2]int{line, col}] = true
		}
		return true
	})
	return out
}

// TestCopyReportIsWhole: on the programs of the copy-count property, the
// copy report holds every copy of the lowered Go (REUSE RU0: not only the
// hand-over's Append sites), of the same kind, each at the start of an
// expression of the source. Mutants: reuse-report-drops-call-copies (the
// report keeps the handover's sites only), reuse-report-wrong-site (a
// copy is reported at its function).
func TestCopyReportIsWhole(t *testing.T) {
	for _, seed := range copySeeds() {
		src, _ := copyProgram(seed, map[string]bool{})
		mainGo, file, sites, _ := copiesOf(t, src)
		want := goCopies(t, mainGo)
		got := map[string]int{}
		starts := exprStarts(file)
		for _, s := range sites {
			if s.Kind == "" {
				continue
			}
			got[s.Kind]++
			if s.File == testSourcePath && !starts[[2]int{s.Line, s.Column}] {
				t.Errorf("seed %d: the copy at %d:%d (%s: %s) starts no expression\nReplay: go test -run '^TestCopyReportIsWhole$' ./internal/sketch/lower -args -copy.seed=%d\n%s", seed, s.Line, s.Column, s.Kind, s.Why, seed, numbered(src))
			}
		}
		if !maps.Equal(got, want) {
			t.Errorf("seed %d: the report has the copies %v, the lowered Go %v\nReplay: go test -run '^TestCopyReportIsWhole$' ./internal/sketch/lower -args -copy.seed=%d\n--- source\n%s\n--- report\n%s--- Go\n%s", seed, got, want, seed, numbered(src), reportText(sites), mainGo)
		}
	}
}

// TestOwnedCallsSayHandedOver: the report says which owned argument a call
// hands over and which it copies, and the functions that own a parameter
// (ergo inspect copies). A concrete row: the copy-count programs
// have no owned call whose argument is copied beside one handed over.
// Mutants: reuse-report-owned-call-handed (every owned argument reported
// handed over), reuse-report-owned-none (no function reported owning).
func TestOwnedCallsSayHandedOver(t *testing.T) {
	src := "type Set { xs []int }\n\n" +
		"func Insert(s Set, x int) Set {\n\tlet mut t = s\n\tt.xs = t.xs.Append(x)\n\tt\n}\n\n" +
		"func kept(s Set) int {\n\tlet u = Insert(s, 1)\n\tlen(u.xs) + len(s.xs)\n}\n\n" +
		"func main() needs Console {\n\tlet mut a = Set{xs: []int{}}\n\tfor i in 0..3 { a = Insert(a, i) }\n\tConsole.Println(\"\\(len(a.xs)) \\(kept(a))\")\n}\n"
	_, _, sites, owned := copiesOf(t, src)
	if len(owned) != 1 || owned[0].Func != "Set.Insert" || !slices.Equal(owned[0].Params, []string{"s"}) || owned[0].Line != 3 {
		t.Errorf("the owned functions are %+v, want Set.Insert at line 3 owning s", owned)
	}
	saw := map[string]string{}
	for _, s := range sites {
		if s.Form == "owned call" {
			saw[s.Func] = s.Why
		}
	}
	if !strings.HasSuffix(saw["main"], "s handed over") {
		t.Errorf("main's Insert(a, i) is not reported handing s over: %q", saw["main"])
	}
	if !strings.Contains(saw["Set.kept"], "s copied: ") {
		t.Errorf("kept's Insert(s, 1) is not reported copying s: %q\n%s", saw["Set.kept"], reportText(sites))
	}
}

func reportText(sites []CopySite) string {
	var b strings.Builder
	for _, s := range slices.Clone(sites) {
		fmt.Fprintf(&b, "%s:%d:%d %s: %s %s: %s\n", s.File, s.Line, s.Column, s.Func, s.Form, s.Kind, s.Why)
	}
	return b.String()
}
