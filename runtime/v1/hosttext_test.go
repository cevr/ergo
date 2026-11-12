package v1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"testing"
)

// TestHostTextUsersAreListed: hostTextUsers is the set of exported
// functions of runtime/v1 whose calls reach rt.TypedText, which writes a
// machine state with fmt only when the gofmt leaf is linked (binsize BS2).
// The lowering imports that leaf into a program that names one of them
// (NeedsHostText), so a missing name would write a program's machine
// state as the core's placeholder. The call graph joins functions and
// methods by name, so it errs toward a longer list, never a shorter one.
// Mutant: bs2-host-text-user-missing (MachineStart is left out of the list).
func TestHostTextUsersAreListed(t *testing.T) {
	calls := map[string]map[string]bool{} // a function or method name -> the names it calls
	exported := map[string]bool{}         // top-level exported functions
	err := fs.WalkDir(Source, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(Source, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, data, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv == nil && ast.IsExported(name) {
				exported[name] = true
			}
			if calls[name] == nil {
				calls[name] = map[string]bool{}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					calls[name][x.Name] = true
				case *ast.SelectorExpr:
					calls[name][x.Sel.Name] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reaches := map[string]bool{}
	var reach func(name string, seen map[string]bool) bool
	reach = func(name string, seen map[string]bool) bool {
		if done, ok := reaches[name]; ok {
			return done
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for callee := range calls[name] {
			if callee == "TypedText" || calls[callee] != nil && reach(callee, seen) {
				reaches[name] = true
				return true
			}
		}
		return false
	}
	var want []string
	for name := range exported {
		if reach(name, map[string]bool{}) {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatal("no function of runtime/v1 reaches rt.TypedText; the scan no longer sees it")
	}
	if !slices.Equal(want, hostTextUsers) {
		t.Errorf("the exported functions that reach rt.TypedText are %v; hostTextUsers lists %v", want, hostTextUsers)
	}
}
