package lower

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/types"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"
)

// TestSharedInstancesNameTheirUses: law SharedListsMatchTheCode, in
// process (MONO-SHARED, pkginst.go). On the generated programs of several
// Ergo packages (genSplitProgram), split and cut into a shard per
// component, in the run build and the test build, the IR path's module
// type-checks with its instance package, and, by go/types rather than by
// the split's own reading of the syntax:
//
//   - each package's shared list is exactly the instances it names outside
//     a generic declaration that another package names too (any package,
//     with shards; never the root alone) and that the instance package can
//     name (every type
//     argument a predeclared type, a non-alias type of a package other than
//     the root, or a pointer, slice or map of such), each spelled as Go's
//     compiler spells it (types.LinkString), so the patched compile meets
//     each one;
//   - the instance package names exactly the union of the lists, and only
//     the root imports it, blank;
//   - some program shares an instance (the law is not vacuous).
//
// A spelling the compiler does not meet is a compile error, and a
// missing instance a link error, in every build of a multi-package
// example; this law finds both in process. Mutants: mono-key-dir,
// mono-list-dropped, mono-generic-body-listed, mono-root-shared,
// mono-root-import-dropped.
func TestSharedInstancesNameTheirUses(t *testing.T) {
	shared := 0
	for _, seed := range []uint64{1, 2, 3, 4, 5, 6} {
		u := genSplitProgram(seed).check(t)
		for _, tests := range []bool{false, true} {
			for _, sh := range []*shardOpts{nil, everyShard} {
				files, _, rep, err := lowerIRSplit(u, tests, programModule, sh)
				if err != nil || rep.Reason != "" {
					t.Fatalf("seed %d tests=%v: %v %+v", seed, tests, err, rep)
				}
				where := fmt.Sprintf("seed %d tests=%v shards=%v", seed, tests, sh != nil)
				shared += checkSharedLists(t, where, files)
			}
		}
	}
	if shared == 0 {
		t.Fatal("no program shares an instance: the law is vacuous")
	}
}

// checkSharedLists checks a lowered module's shared lists against the
// instances its packages name, and returns the number of instances the
// instance package names.
func checkSharedLists(t *testing.T, where string, files []File) int {
	t.Helper()
	errs, pkgs := typecheckModuleInfo(t, programModule, files)
	if len(errs) > 0 {
		t.Fatalf("%s: the module does not type-check:\n%s", where, strings.Join(errs, "\n"))
	}
	lists := map[string][]string{}
	for _, f := range files {
		if path.Base(f.Path) != IRFileName {
			continue
		}
		var ir struct {
			Shared []string `json:"shared"`
		}
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		lists[path.Dir(f.Path)] = ir.Shared
	}
	named := map[string]map[string]bool{}
	users := map[string]int{}
	for dir, p := range pkgs {
		if dir == instDir {
			continue
		}
		named[dir] = namedInstances(p, programModule)
		for k := range named[dir] {
			users[k]++
		}
	}
	// Two packages must name an instance in a split, one with shards (the
	// edit loop); the root alone never shares one.
	least := 2
	for dir := range named {
		if strings.HasPrefix(dir, shardDir+"/") {
			least = 1
		}
	}
	union := map[string]bool{}
	for dir := range named {
		var want []string
		for k := range named[dir] {
			if users[k] >= least && !(users[k] == 1 && named["."][k]) {
				want = append(want, k)
			}
		}
		slices.Sort(want)
		got := slices.Clone(lists[dir])
		if !slices.Equal(got, want) {
			t.Errorf("%s: package %s lists the shared instances\n%v\nand names\n%v", where, dir, got, want)
		}
		for _, k := range got {
			union[k] = true
		}
	}
	inst, ok := pkgs[instDir]
	if len(union) == 0 {
		if ok {
			t.Errorf("%s: an instance package and no shared instance", where)
		}
		return 0
	}
	if !ok {
		t.Fatalf("%s: shared instances and no instance package", where)
	}
	names := map[string]bool{}
	for id, in := range inst.info.Instances {
		names[linkInstance(inst.info.Uses[id], in.TypeArgs)] = true
	}
	// A nested instance (Edge[int] in Append[Edge[int]]) is named too; the
	// union holds both.
	if got, want := slices.Sorted(maps.Keys(names)), slices.Sorted(maps.Keys(union)); !slices.Equal(got, want) {
		t.Errorf("%s: the instance package names\n%v\nand the packages list\n%v", where, got, want)
	}
	for dir, p := range pkgs {
		for _, f := range p.files {
			for _, s := range f.Imports {
				if s.Path.Value != `"`+programModule+"/"+instDir+`"` {
					continue
				}
				if dir != "." || s.Name == nil || s.Name.Name != "_" {
					t.Errorf("%s: package %s imports the instance package (as %v); only the root does, blank", where, dir, s.Name)
				}
			}
		}
	}
	rootImports := false
	for _, f := range pkgs["."].files {
		for _, s := range f.Imports {
			rootImports = rootImports || s.Path.Value == `"`+programModule+"/"+instDir+`"`
		}
	}
	if !rootImports {
		t.Errorf("%s: the root does not import the instance package, so no binary links it", where)
	}
	return len(names)
}

// namedInstances are the instances p names outside generic declarations
// that an instance package of the module base can name, by link string:
// go/types' reading of the rule pkginst.go applies to the syntax.
func namedInstances(p checkedPkg, base string) map[string]bool {
	out := map[string]bool{}
	for _, f := range p.files {
		for _, d := range f.Decls {
			if genericDecl(d) {
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				var gen ast.Expr
				switch x := n.(type) {
				case *ast.IndexExpr:
					gen = x.X
				case *ast.IndexListExpr:
					gen = x.X
				}
				if s, ok := gen.(*ast.SelectorExpr); ok {
					gen = s.Sel // a name of another package of the module
				}
				id, _ := gen.(*ast.Ident)
				if id == nil {
					return true
				}
				in, ok := p.info.Instances[id]
				obj := p.info.Uses[id]
				if !ok || obj == nil || !instNameable(obj, base) {
					return true
				}
				for i := range in.TypeArgs.Len() {
					if !typeNameable(in.TypeArgs.At(i), base) {
						return true
					}
				}
				out[linkInstance(obj, in.TypeArgs)] = true
				return true
			})
		}
	}
	return out
}

// genericDecl reports a declaration of a generic function or type, or a
// method of a generic type.
func genericDecl(d ast.Decl) bool {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Type.TypeParams != nil {
			return true
		}
		if d.Recv != nil && len(d.Recv.List) > 0 {
			t := d.Recv.List[0].Type
			if s, ok := t.(*ast.StarExpr); ok {
				t = s.X
			}
			switch t.(type) {
			case *ast.IndexExpr, *ast.IndexListExpr:
				return true
			}
		}
	case *ast.GenDecl:
		for _, s := range d.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && ts.TypeParams != nil {
				return true
			}
		}
	}
	return false
}

// instNameable reports an object of the module outside the root.
func instNameable(obj types.Object, base string) bool {
	return obj.Pkg() != nil && strings.HasPrefix(obj.Pkg().Path(), base+"/")
}

// typeNameable reports a type the instance package can name.
func typeNameable(t types.Type, base string) bool {
	switch t := t.(type) {
	case *types.Basic:
		return true
	case *types.Alias:
		return false
	case *types.Named:
		if t.Obj().Pkg() == nil {
			return t.Obj().Name() == "error"
		}
		if !instNameable(t.Obj(), base) {
			return false
		}
		for i := range t.TypeArgs().Len() {
			if !typeNameable(t.TypeArgs().At(i), base) {
				return false
			}
		}
		return true
	case *types.Pointer:
		return typeNameable(t.Elem(), base)
	case *types.Slice:
		return typeNameable(t.Elem(), base)
	case *types.Map:
		return typeNameable(t.Key(), base) && typeNameable(t.Elem(), base)
	case *types.Interface:
		return t.Empty()
	}
	return false
}

// linkInstance is obj[args] as Go's compiler spells an instance's link
// name.
func linkInstance(obj types.Object, args *types.TypeList) string {
	parts := make([]string, args.Len())
	for i := range parts {
		parts[i] = linkString(args.At(i))
	}
	return obj.Pkg().Path() + "." + obj.Name() + "[" + strings.Join(parts, ",") + "]"
}

// linkString is a type's link string (types.LinkString in cmd/compile):
// a predeclared type by its kind's name, an alias as its target.
func linkString(t types.Type) string {
	switch t := t.(type) {
	case *types.Basic:
		return types.Typ[t.Kind()].Name()
	case *types.Alias:
		return linkString(types.Unalias(t))
	case *types.Named:
		name := t.Obj().Name()
		if t.Obj().Pkg() != nil {
			name = t.Obj().Pkg().Path() + "." + name
		}
		if t.TypeArgs().Len() == 0 {
			return name
		}
		parts := make([]string, t.TypeArgs().Len())
		for i := range parts {
			parts[i] = linkString(t.TypeArgs().At(i))
		}
		return name + "[" + strings.Join(parts, ",") + "]"
	case *types.Pointer:
		return "*" + linkString(t.Elem())
	case *types.Slice:
		return "[]" + linkString(t.Elem())
	case *types.Map:
		return "map[" + linkString(t.Key()) + "]" + linkString(t.Elem())
	case *types.Interface:
		if t.Empty() {
			return "interface {}"
		}
	}
	return t.String()
}
