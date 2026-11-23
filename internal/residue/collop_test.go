package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// collOpBinds are the functions of the lowering that may ask which
// operation of the slice or Map type a call is (check.CollOpOf, or a
// check.Coll* constant): each picks the operation's Go form, its binding
// (PRIM, owner 2026-11-16: the operations belong to the language, as +
// belongs to int, so the lowering must know which Go form stands for
// each). The list only shrinks: a line is never added without a ruling.
var collOpBinds = map[string]string{
	"calls.go:funcCtx.callText":            "an operation's receiver is its body's first operand",
	"decls.go:lowerer.intrinsic":           "the Go bodies of Append, Put, Get and Take",
	"decls.go:intrinsicTakes":              "Take's intrinsic takes its entry in place: the intrinsic's own fact, read beside it",
	"getmatch.go:preludeGet":               "Get's Go form is Go's comma-ok read",
	"ir_intmap.go:irBuilder.intMapPrelude": "the int-keyed table's Go form of each Map operation",
	"ir_pkg.go:irBuilder.builtinAppend":    "the IR writes Append as Go's builtin append",
	"ir_pkg.go:irBuilder.stdCall":          "an operation's receiver is its body's first operand",
}

// TestValueRuleReadsNoOperation is RU6's residue rule (docs/spec/types.md,
// Bindings 2): no decision of the lowering keys on which operation of the
// slice or Map type a call is. The value rule (values.go) and every
// optimizer that reads it (the hand-over, the reserve, the seeded
// capacity, the last read, loops and flat unions) read how a call treats
// its first operand from the callee's Go form: Go's builtin append or
// delete, a runtime/v1 appender or Map writer (lower/gobody.go), or an
// intrinsic's own fact. So the prelude's Append, Put, Delete and Take are
// users of the rule, as a user's extern of Go's append is. Only the
// bindings in collOpBinds ask, and each listed function must still ask, so
// the list never keeps a stale line.
//
// Mutant (testdata/mutants.json): ru6-value-rule-names-put.
func TestValueRuleReadsNoOperation(t *testing.T) {
	top, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(top))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "internal/sketch/lower/*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	files := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || filepath.Dir(name) != "internal/sketch/lower" {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(repo, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files++
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !asksCollOp(fd.Body) {
				continue
			}
			fn := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) == 1 {
				typ := fd.Recv.List[0].Type
				if s, ok := typ.(*ast.StarExpr); ok {
					typ = s.X
				}
				if id, ok := typ.(*ast.Ident); ok {
					fn = id.Name + "." + fn
				}
			}
			found[filepath.Base(name)+":"+fn] = true
		}
	}
	if files == 0 {
		t.Fatal("no lowering file read")
	}
	for key := range found {
		if _, ok := collOpBinds[key]; !ok {
			t.Errorf("%s asks which slice or Map operation a call is (check.CollOpOf): decide by the callee's Go form (values.go, appendsSource, writesMapSource, takesMapSource), never by the operation (RU6)", key)
		}
	}
	for key := range collOpBinds {
		if !found[key] {
			t.Errorf("collOpBinds lists %s, which no longer asks for the operation: drop the line", key)
		}
	}
}

// asksCollOp reports a body that names check.CollOpOf or a check.Coll*
// constant.
func asksCollOp(body *ast.BlockStmt) bool {
	asks := false
	ast.Inspect(body, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Name == "check" && strings.HasPrefix(s.Sel.Name, "Coll") {
				asks = true
			}
		}
		return !asks
	})
	return asks
}
