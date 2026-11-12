package residue_test

import (
	"bufio"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The identity-blind rule of the lowering (OPT4a; orchestrator ruling
// 2026-11-11): no lowering decision keys on a std package or function
// name, except the entries of testdata/std-names.txt. The inventory has one
// category, abi: a backend binding. A runtime type representation, a
// prelude collection wrapper or a std extern's ABI: the lowering must know
// which Go form stands for the declaration, so the name is the binding.
// Apart from the pre-existing json Text path sanctioned by owner q108
// until LR3 removes it, no optimizer selects a faster form by a std name:
// OPT6 retired
// maps.Update fusion, sync's owned handover and WithCapacity freshness,
// and OPT6b the machines Run/From flat run, through general facts
// (Ergo-side inlining of an ordinary body, a runtime body's form, copies
// for a known record and scalar locals). A line of another category
// fails.
//
// A name-keyed decision is a function of a non-test file of the lowering
// that compares a .Name or .Path selector with a constant string (== or
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
	fset := token.NewFileSet()
	var files []*ast.File
	names := map[*ast.File]string{}
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || strings.Contains(strings.TrimPrefix(name, "internal/sketch/lower/"), "/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files = append(files, f)
		names[f] = name
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Error: func(error) {}}
	_, _ = conf.Check("lower", fset, files, info)
	for _, f := range files {
		for _, fn := range nameKeyedFuncs(f, info) {
			found[filepath.Base(names[f])+":"+fn] = true
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
		if len(parts) != 3 || parts[0] != "abi" || strings.TrimSpace(parts[2]) == "" {
			t.Errorf("testdata/std-names.txt: %q is not `abi<TAB>file:function<TAB>reason`: the only category is a backend binding (OPT6b retired the last inherited optimizer, the machines flat run)", line)
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
// compare a .Name or .Path selector with a constant string. go/types
// evaluates aliases, concatenations and package-local constants across
// files too. Unresolved
// imports/types do not prevent it from evaluating independent constants.
func nameKeyedFuncs(f *ast.File, info *types.Info) []string {
	stdConst := func(x ast.Expr) bool {
		value := info.Types[x].Value
		if value == nil || value.Kind() != constant.String {
			return false
		}
		s := constant.StringVal(value)
		return s != "_" && s != "main" && types.Universe.Lookup(s) == nil
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
				if (n.Op == token.EQL || n.Op == token.NEQ) && (nameSel(n.X) && stdConst(n.Y) || nameSel(n.Y) && stdConst(n.X)) {
					keyed = true
				}
			case *ast.SwitchStmt:
				if n.Tag != nil && nameSel(n.Tag) {
					for _, c := range n.Body.List {
						for _, e := range c.(*ast.CaseClause).List {
							if stdConst(e) {
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

// stdNamesBaseline is testdata/std-names.txt as OPT6 left it (the rule
// landed with OPT4a): `category file:function`. A line may leave the
// inventory; none may join. CF1 corrects the old literal-only scanner's
// missed, owner-sanctioned Text site (q108); LR3 removes that site.
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
	"abi wirejson.go:isJSONText",
}
