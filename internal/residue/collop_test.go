package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// collOpBinds was the shrinking allow-list of lowering functions that asked
// check.CollOpOf. NOPRE-COLLOPS-OUT retired the closed CollOp table completely,
// so the list is permanently empty.
var collOpBinds = map[string]string{}

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

// asksCollOp reports a body that names check.CollOpOf, a check.Coll*
// constant, or any CollOp / CollOpOf identifier.
func asksCollOp(body *ast.BlockStmt) bool {
	asks := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && id.Name == "check" && strings.HasPrefix(x.Sel.Name, "Coll") {
				asks = true
			}
		case *ast.Ident:
			if x.Name == "CollOp" || x.Name == "CollOpOf" {
				asks = true
			}
		}
		return !asks
	})
	return asks
}

// TestNoCollOpTable holds NO-PRELUDE Unit 7 (COLLOPS-OUT): collop.go is
// deleted and no non-test Go file under internal/sketch/{check,lower,eval}
// references CollOp, CollOpOf, or collop.go.
func TestNoCollOpTable(t *testing.T) {
	top, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(top))
	if _, err := os.Stat(filepath.Join(repo, "internal/sketch/check/collop.go")); !os.IsNotExist(err) {
		t.Fatalf("internal/sketch/check/collop.go must not exist (NO-PRELUDE COLLOPS-OUT)")
	}
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--",
		"internal/sketch/check", "internal/sketch/lower", "internal/sketch/eval")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	scanned := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		src := string(data)
		for _, banned := range []string{"CollOp", "CollOpOf", "collop.go"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s still references %q (NO-PRELUDE COLLOPS-OUT)", name, banned)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned 0 Go files")
	}
}
