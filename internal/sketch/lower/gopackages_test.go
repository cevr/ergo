package lower

import (
	"bytes"
	"fmt"
	"go/types"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestGoPackagesPlaceEachPackageOnce: the law of LowerGoPackages, in
// process. On the generated programs of several Ergo packages
// (genSplitProgram: base, imported by left and right, which top imports;
// left and right export functions and types of the same names), lowered
// as the Go packages of the module gen:
//
//   - the module type-checks package by package with go/types: no import
//     cycle, no name declared twice in a package;
//   - each Ergo package's Go package is at its folder, and declares each
//     export Go can call as its facade under its Ergo name, and each
//     exported Ergo type the lowering emits under its Ergo name, naming the
//     Go type declared at the Ergo type's line (one declaration, which
//     every importer names);
//   - every Go func init of the one Go file is in a Go package that each
//     Ergo package's imports reach: none stays in the split's root, which
//     no package imports.
//
// Mutants: golib-respell-none (no plain names), golib-clash-kept (a
// declaration spelled as a plain name keeps it), golib-foreign-own-name (a
// package's declaration of another's name keeps its spelling),
// golib-init-in-root (an init starts in the root), golib-shared-unimported
// (no blank import of the shared package).
func TestGoPackagesPlaceEachPackageOnce(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			g := genSplitProgram(seed)
			replay := fmt.Sprintf("go test ./internal/sketch/lower -run 'TestGoPackagesPlaceEachPackageOnce/%d'", seed)
			imp := pkgImporter{}
			var units []Unit
			names := map[string]string{}
			for _, lib := range g.libs {
				u := checkPackage(t, lib.path, []pkgSource{{"/split/" + lib.path + "/a.ergo", lib.text}}, imp)
				imp[lib.path] = u.Package
				units = append(units, u)
				names[lib.path] = path.Base(lib.path)
			}
			root := units[len(units)-1]
			root.Imports = units[:len(units)-1]
			files, _, err := LowerGoPackages(root, "gen", names)
			if err != nil {
				t.Fatalf("%s: %v", replay, err)
			}
			errs, checked := typecheckModuleInfo(t, "gen", files)
			if len(errs) > 0 {
				t.Fatalf("%s: the Go packages do not type-check:\n%s", replay, strings.Join(errs[:min(len(errs), 10)], "\n"))
			}
			// The inits of the one Go file.
			u := root
			u.goPackages = true
			one, _, _, _, err := lowerIRModuleReaching(u, nil, false, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			inits := 0
			for _, f := range one {
				if isMainGo(f.Path) {
					inits = bytes.Count(f.Data, []byte("\nfunc init() {"))
				}
			}
			placed := 0
			var initDirs []string
			for _, f := range files {
				if n := bytes.Count(f.Data, []byte("\nfunc init() {")); n > 0 && strings.HasSuffix(f.Path, ".go") {
					placed += n
					initDirs = append(initDirs, path.Dir(f.Path))
				}
			}
			if inits == 0 || placed != inits {
				t.Fatalf("%s: the one Go file has %d func init, the Go packages %d (in %v): want them all placed, and some", replay, inits, placed, initDirs)
			}
			for _, un := range units {
				dir := strings.TrimPrefix(un.Package.Path, "gen/")
				home, ok := checked[dir]
				if !ok {
					t.Fatalf("%s: no Go package at %s for the Ergo package %s", replay, dir, un.Package.Path)
				}
				reach := importClosure(home.pkg)
				for _, d := range initDirs {
					if p := checked[d].pkg; p != home.pkg && !reach[p] {
						t.Errorf("%s: the Go package of %s does not import %s, whose func init registers what it may use", replay, un.Package.Path, d)
					}
				}
				goFacing(t, replay, un, home, checked)
			}
		})
	}
}

// goFacing checks the Go-facing names of the Ergo package of un in its Go
// package home: each export's facade, and each exported type the
// lowering emitted (declared in some package at its line).
func goFacing(t *testing.T, replay string, un Unit, home checkedPkg, all map[string]checkedPkg) {
	t.Helper()
	scope := home.pkg.Scope()
	for _, f := range un.Files {
		for _, d := range f.Decls {
			sd, ok := d.(*syntax.FuncDecl)
			if !ok {
				continue
			}
			fd, ok := un.Package.Info.Defs[sd.Name].(*check.Func)
			if !ok || !exported(fd.Name) || fd.Decl == nil || fd.Decl.Body == nil || exportReason(fd) != "" {
				continue
			}
			obj, ok := scope.Lookup(fd.Name).(*types.Func)
			if !ok {
				t.Errorf("%s: package %s has no facade %s: %v", replay, home.pkg.Path(), fd.Name, scope.Lookup(fd.Name))
				continue
			}
			if p := obj.Type().(*types.Signature).Params(); p.Len() == 0 || p.At(0).Type().String() != "context.Context" {
				t.Errorf("%s: %s.%s is %s, not a facade", replay, home.pkg.Path(), fd.Name, obj.Type())
			}
		}
	}
	src := un.Sources[0]
	for _, m := range typeLine.FindAllStringSubmatchIndex(src.Text, -1) {
		name := src.Text[m[2]:m[3]]
		if !exported(name) {
			continue
		}
		line := strings.Count(src.Text[:m[0]], "\n") + 1
		declared := false
		for _, c := range all {
			for _, n := range c.pkg.Scope().Names() {
				if tn, ok := c.pkg.Scope().Lookup(n).(*types.TypeName); ok && !tn.IsAlias() {
					if pos := c.fset.Position(tn.Pos()); pos.Filename == src.Path && pos.Line == line {
						declared = true
					}
				}
			}
		}
		if !declared {
			continue // not emitted: no Go value of it exists
		}
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			t.Errorf("%s: package %s does not name the Ergo type %s: %v", replay, home.pkg.Path(), name, scope.Lookup(name))
			continue
		}
		named, ok := types.Unalias(tn.Type()).(*types.Named)
		if !ok {
			t.Errorf("%s: %s.%s is %s, not a named type", replay, home.pkg.Path(), name, tn.Type())
			continue
		}
		at := home.fset.Position(named.Obj().Pos())
		if named.Obj().Pkg() != nil {
			for _, c := range all {
				if c.pkg == named.Obj().Pkg() {
					at = c.fset.Position(named.Obj().Pos())
				}
			}
		}
		if at.Filename != src.Path || at.Line != line {
			t.Errorf("%s: %s.%s names the Go type declared at %s, not the Ergo type's %s:%d", replay, home.pkg.Path(), name, at, src.Path, line)
		}
	}
}

// TestGoPackagesSpellTheirOwnNames: two packages that export the same
// names, where b's IR uses a's DivZero and declares its own: b spells a's
// type by a name of its own, so its DivZero and Div are its Ergo names;
// and c exports a type Env, the name of the lowering's own environment
// type, which c's functions use. Each module type-checks, and each name
// is its Ergo name in its own package (goFacing).
//
// Mutants: golib-foreign-own-name (b declares a's DivZero under the name
// DivZero), golib-clash-kept (the environment type keeps the name Env in
// c).
func TestGoPackagesSpellTheirOwnNames(t *testing.T) {
	srcs := []pkgSource{
		{"/m/a/a.ergo", "// DivZero is a division by zero.\ntype DivZero { Of int }\n\n// Div is a / b.\nfunc Div(a int, b int) int fails DivZero {\n\tif b == 0 {\n\t\treturn DivZero{Of: a}\n\t}\n\ta / b\n}\n"},
		{"/m/b/b.ergo", "import m/a\n\n// DivZero is b's own.\ntype DivZero { At int }\n\n// Half is n / d through a.\nfunc Half(n int, d int) int fails a.DivZero { a.Div(n, d) }\n\n// Div is b's: n / d, or b's DivZero.\nfunc Div(n int, d int) int fails DivZero {\n\tif d == 0 {\n\t\treturn DivZero{At: n}\n\t}\n\tn / d\n}\n"},
		{"/m/c/c.ergo", "// Env is c's.\ntype Env { N int }\n\n// Twice is 2n through an Env.\nfunc Twice(n int) int { (Env{N: n}).N * 2 }\n\n// Greet prints hello.\nfunc Greet() needs Console { Console.Println(\"hello\") }\n"},
	}
	imp := pkgImporter{}
	var units []Unit
	names := map[string]string{}
	for _, s := range srcs {
		p := "m/" + path.Base(path.Dir(s.path))
		u := checkPackage(t, p, []pkgSource{s}, imp)
		imp[p] = u.Package
		units = append(units, u)
		names[p] = path.Base(p)
	}
	root := units[len(units)-1]
	root.Imports = units[:len(units)-1]
	files, _, err := LowerGoPackages(root, "m", names)
	if err != nil {
		t.Fatal(err)
	}
	errs, checked := typecheckModuleInfo(t, "m", files)
	if len(errs) > 0 {
		for _, f := range files {
			if strings.HasSuffix(f.Path, ".go") {
				t.Logf("%s:\n%s", f.Path, f.Data)
			}
		}
		t.Fatalf("the Go packages do not type-check:\n%s", strings.Join(errs, "\n"))
	}
	for _, un := range units {
		goFacing(t, "TestGoPackagesSpellTheirOwnNames", un, checked[strings.TrimPrefix(un.Package.Path, "m/")], checked)
	}
}

// typeLine finds a top-level type declaration's name.
var typeLine = regexp.MustCompile(`(?m)^type ([A-Za-z_][A-Za-z0-9_]*)`)

// importClosure are the packages p imports, transitively.
func importClosure(p *types.Package) map[*types.Package]bool {
	out := map[*types.Package]bool{}
	var walk func(q *types.Package)
	walk = func(q *types.Package) {
		for _, i := range q.Imports() {
			if !out[i] {
				out[i] = true
				walk(i)
			}
		}
	}
	walk(p)
	return out
}

// TestGoPackagesRefuseAPackageOutsideTheModule: an Ergo package whose path
// is not the module's or below it has no folder in the module, and one in
// the module's _ergo folder would be one of the packages the lowering
// adds; both are refused, and a Go package name is checked.
//
// Mutant: golib-outside-placed (the module prefix check is dropped).
func TestGoPackagesRefuseAPackageOutsideTheModule(t *testing.T) {
	src := "// Add is a + b.\nfunc Add(a int, b int) int { a + b }\n"
	for _, c := range []struct{ path, module, name, want string }{
		{"other/a", "gen", "a", "is not a package of the Go module gen"},
		{"genx/a", "gen", "a", "is not a package of the Go module gen"},
		{"gen/_ergo/a", "gen", "a", "the folder of the packages ergo adds"},
		{"gen/a", "gen", "main", "is not a Go package name"},
	} {
		u := checkPackage(t, c.path, []pkgSource{{"/x/" + c.path + "/a.ergo", src}}, pkgImporter{})
		_, _, err := LowerGoPackages(u, c.module, map[string]string{c.path: c.name})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("LowerGoPackages(%s in %s as %s) = %v, want %q", c.path, c.module, c.name, err, c.want)
		}
	}
	u := checkPackage(t, "gen", []pkgSource{{"/x/gen/a.ergo", src}}, pkgImporter{})
	files, _, err := LowerGoPackages(u, "gen", map[string]string{"gen": "gen"})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["gen.go"] || !paths[IRFileName] {
		t.Fatalf("the module root's package is at %v, want gen.go and %s at the root", slices.Sorted(maps.Keys(paths)), IRFileName)
	}
}
