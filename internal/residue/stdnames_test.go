package residue_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The identity-blind rule of the lowering (OPT4a; orchestrator ruling
// 2026-11-11): no lowering decision keys on a std package or function
// name, except the entries of testdata/std-names.txt. The inventory has two
// categories:
//
//   - abi: a backend binding. A runtime type representation, a prelude
//     collection wrapper or a std extern's ABI: the lowering must know
//     which Go form stands for the declaration, so the name is the binding.
//   - debt: an inherited optimizer that selects a faster form by a std
//     name. Unit OPT6 retires each one through a general fact (Ergo-side
//     inlining of an ordinary body, a result-alias summary, ownership
//     facts), so this category ends empty.
//
// A name-keyed decision is a function of a non-test file of the lowering
// that compares a .Name or .Path selector with a string literal (== or
// !=, or a switch on the selector with string cases). The literals "_" and
// "main" are the blank name and the program entry, which the language
// defines; they are not std names. Every such function must be listed,
// and every listed function must still hold one, so the inventory never
// keeps a stale line. The inventory may only shrink: stdNamesBaseline is
// the inventory as the rule landed, and a line outside it fails. A new
// optimization decides by checked facts, never by a name.
func TestLoweringKeysOnNoStdName(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "internal/sketch/lower/*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	found := map[string]bool{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || strings.Contains(strings.TrimPrefix(name, "internal/sketch/lower/"), "/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, fn := range nameKeyedFuncs(t, name, src) {
			found[filepath.Base(name)+":"+fn] = true
		}
	}
	listed := readStdNames(t)
	for key := range found {
		if _, ok := listed[key]; !ok {
			t.Errorf("%s decides by a std name and is not in testdata/std-names.txt: decide by a checked fact (OPT4a ruling)", key)
		}
	}
	for key, cat := range listed {
		if !found[key] {
			t.Errorf("testdata/std-names.txt lists %s (%s), which no longer compares a name: drop the line", key, cat)
		}
		if !slices.Contains(stdNamesBaseline, cat+" "+key) {
			t.Errorf("testdata/std-names.txt lists %s %s, which the inventory did not hold when the rule landed: the inventory may only shrink", cat, key)
		}
	}
}

// readStdNames reads the inventory: one `category<TAB>file:function<TAB>reason`
// line per entry; # starts a comment.
func readStdNames(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("testdata/std-names.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || (parts[0] != "abi" && parts[0] != "debt") || strings.TrimSpace(parts[2]) == "" {
			t.Errorf("testdata/std-names.txt: %q is not `abi|debt<TAB>file:function<TAB>reason`", line)
			continue
		}
		out[parts[1]] = parts[0]
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// nameKeyedFuncs are the functions of src (a method as Recv.Name) that
// compare a .Name or .Path selector with a string literal.
func nameKeyedFuncs(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		keyed := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BinaryExpr:
				if (n.Op == token.EQL || n.Op == token.NEQ) && (nameSel(n.X) && stdLit(n.Y) || nameSel(n.Y) && stdLit(n.X)) {
					keyed = true
				}
			case *ast.SwitchStmt:
				if n.Tag != nil && nameSel(n.Tag) {
					for _, c := range n.Body.List {
						for _, e := range c.(*ast.CaseClause).List {
							if stdLit(e) {
								keyed = true
							}
						}
					}
				}
			}
			return !keyed
		})
		if !keyed {
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
		out = append(out, fn)
	}
	return out
}

// nameSel reports x.Name or x.Path.
func nameSel(x ast.Expr) bool {
	s, ok := x.(*ast.SelectorExpr)
	return ok && (s.Sel.Name == "Name" || s.Sel.Name == "Path")
}

// stdLit reports a string literal that may name a std declaration: any
// but the blank name and the program entry.
func stdLit(x ast.Expr) bool {
	b, ok := x.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(b.Value)
	return err == nil && s != "_" && s != "main"
}

// stdNamesBaseline is testdata/std-names.txt as the rule landed (OPT4a):
// `category file:function`. A line may leave the inventory; none may join.
var stdNamesBaseline = []string{
	"abi calls.go:funcCtx.callee",
	"abi calls.go:lowerer.hostImportNames",
	"abi decls.go:isPreludeType",
	"abi decls.go:lowerer.intrinsic",
	"abi getmatch.go:getPattern",
	"abi getmatch.go:preludeGet",
	"abi gocall.go:funcCtx.goErrorCall",
	"abi gocall.go:isOptionType",
	"abi library.go:hostProvides",
	"abi module.go:lowerer.hostFields",
	"abi std.go:lowerer.runtimeType",
	"abi types.go:lowerer.foreignOwner",
	"abi values.go:preludeAppend",
	"abi values.go:preludeMapWrite",
	"debt machinerun.go:funcCtx.flatReturn",
	"debt machinerun.go:funcCtx.machineRun",
	"debt machinerun.go:lowerer.flatRunOf",
	"debt refown.go:isSyncModify",
	"debt refown.go:isSyncRef",
	"debt refown.go:valueWalk.refCall",
	"debt update.go:mapsUpdate",
	"debt values.go:valueWalk.fresh",
}
