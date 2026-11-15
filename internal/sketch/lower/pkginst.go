package lower

import (
	"go/ast"
	"maps"
	"slices"
	"strings"
)

// Shared instances (MONO-SHARED; docs/tooling.md, "Shared instances").
// Go compiles a generic instance in every package that uses it: each
// package that names Graph[string] compiles Graph[string]'s methods, and
// every generic body they instantiate, again (its symbols are DUPOK and
// the linker keeps one). Once the split cuts a program into packages and
// shards, that is most of a shard's compile (test-speed FT9, round 3: a
// 6.4 KB shard of tests cost 486 M instructions).
//
// On the IR path, the split emits one more Go package, inst, that names
// each closed instance the program's packages name once, and that only
// the root imports (blank), so every binary links it. The IR file of each
// other package lists, under "shared", the instances it names; ergo's
// patched compile still reads, checks, escape-analyses and inlines such
// an instance and every generic body read for it, but does not compile
// it: inst does, once, and the linker takes its symbols from there
// (toolchain/ir: ergoshared.go). Go's generics still instantiate every
// body (design.md: no frontend monomorphization, no copied bodies); the
// split only chooses which package compiles each instance.
//
// An instance is shared when inst can name it: a generic type or function
// of main.go with every type argument written out, each a predeclared
// type or a type of main.go (no alias, no type parameter) or a pointer,
// slice or map of such, and none of them placed in the root, which no
// package imports. An instance in a generic declaration's body is not
// listed: Go compiles that body only where it is instantiated, which the
// list of the body's own instance covers. A listed instance its package
// never names is a compile error, so the list cannot drift from the code.
//
// The reference is instShared off: every package compiles its own
// instances, as Go does (TestSharedInstancesAgree).
//
// Prior art: Rust's share-generics (-Zshare-generics, on in debug
// builds): a crate reuses the monomorphizations an upstream crate
// already made and exports, instead of making its own. C++ explicit
// instantiation (extern template, one translation unit holding the
// instantiation definitions) is the same choice made by hand. Gleam,
// ReScript and Elixir target hosts without generic instantiation and
// never meet it.

// instShared selects shared instances on the IR path; off is the
// reference, Go's own rule.
var instShared = true

// instDir is the folder of the instance package, and instName its Go
// package name.
const (
	instDir  = "inst"
	instName = "ergoinst"
)

// splitInst is a closed instance of main.go: its expression and the
// package of the declaration that names it.
type splitInst struct {
	x   ast.Expr
	pkg int
}

// splitInsts finds the instances inst can name.
type splitInsts struct {
	scope   *ast.Scope
	groups  []splitGroup
	groupOf map[string]int
	params  map[string]int // the type parameter count of each generic
	alias   map[string]bool
}

// newSplitInsts reads the generics and aliases of f.
func newSplitInsts(f *ast.File, groups []splitGroup, groupOf map[string]int) *splitInsts {
	s := &splitInsts{scope: f.Scope, groups: groups, groupOf: groupOf, params: map[string]int{}, alias: map[string]bool{}}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Type.TypeParams != nil {
				s.params[d.Name.Name] = d.Type.TypeParams.NumFields()
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					if ts.Assign != 0 {
						s.alias[ts.Name.Name] = true
					} else if ts.TypeParams != nil {
						s.params[ts.Name.Name] = ts.TypeParams.NumFields()
					}
				}
			}
		}
	}
	return s
}

// global is the group of id when it names a package-scope declaration of
// main.go, else -1.
func (s *splitInsts) global(id *ast.Ident) int {
	obj := s.scope.Lookup(id.Name)
	if obj == nil || (id.Obj != nil && id.Obj != obj) {
		return -1
	}
	if g, ok := s.groupOf[id.Name]; ok {
		return g
	}
	return -1
}

// instParts are the generic and type arguments of x, an instance
// expression, or nil.
func instParts(x ast.Expr) (*ast.Ident, []ast.Expr) {
	switch x := x.(type) {
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id, []ast.Expr{x.Index}
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id, x.Indices
		}
	}
	return nil, nil
}

// instance reports whether x is an instance inst can name.
func (s *splitInsts) instance(x ast.Expr) bool {
	gen, args := instParts(x)
	if gen == nil {
		return false
	}
	g := s.global(gen)
	if g < 0 || s.groups[g].pkg == 1 || s.params[gen.Name] != len(args) {
		return false
	}
	for _, a := range args {
		if !s.closed(a) {
			return false
		}
	}
	return true
}

// closed reports whether t is a type inst can name.
func (s *splitInsts) closed(t ast.Expr) bool {
	switch t := t.(type) {
	case *ast.Ident:
		if g := s.global(t); g >= 0 {
			return !s.alias[t.Name] && s.params[t.Name] == 0 && s.groups[g].pkg != 1
		}
		return t.Obj == nil && s.scope.Lookup(t.Name) == nil && predeclaredLink[t.Name] != ""
	case *ast.IndexExpr, *ast.IndexListExpr:
		return s.instance(t)
	case *ast.StarExpr:
		return s.closed(t.X)
	case *ast.ArrayType:
		return t.Len == nil && s.closed(t.Elt)
	case *ast.MapType:
		return s.closed(t.Key) && s.closed(t.Value)
	case *ast.ParenExpr:
		return s.closed(t.X)
	}
	return false
}

// predeclaredLink is each predeclared type's name in a link string, the
// spelling of Go's compiler (types.LinkString): an alias is its target.
var predeclaredLink = map[string]string{
	"bool": "bool", "string": "string", "error": "error",
	"int": "int", "int8": "int8", "int16": "int16", "int32": "int32", "int64": "int64",
	"uint": "uint", "uint8": "uint8", "uint16": "uint16", "uint32": "uint32", "uint64": "uint64", "uintptr": "uintptr",
	"float32": "float32", "float64": "float64", "complex64": "complex64", "complex128": "complex128",
	"byte": "uint8", "rune": "int32", "any": "interface {}",
}

// collect is every instance inst can name in the declarations of
// non-generic groups, nested ones included, and the package-scope names
// they use.
func (s *splitInsts) collect(f *ast.File, declGroup []int) ([]splitInst, map[string]bool) {
	var out []splitInst
	names := map[string]bool{}
	for i, d := range f.Decls {
		g := declGroup[i]
		if g < 0 || s.groups[g].generic {
			continue
		}
		ast.Inspect(d, func(n ast.Node) bool {
			x, ok := n.(ast.Expr)
			if !ok || !s.instance(x) {
				return true
			}
			out = append(out, splitInst{x: x, pkg: s.groups[g].pkg})
			ast.Inspect(x, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && s.global(id) >= 0 {
					names[id.Name] = true
				}
				return true
			})
			return true
		})
	}
	return out, names
}

// instWriter spells the instances once the split has named its packages.
type instWriter struct {
	s      *splitInsts
	plan   *splitPlan
	base   string
	rename map[string]string
}

func (w *instWriter) name(id *ast.Ident) string {
	if r, ok := w.rename[id.Name]; ok {
		return r
	}
	return id.Name
}

// key is t's link string, as the patched compile spells a shared
// instance (pkgpath.Name[args]).
func (w *instWriter) key(t ast.Expr) string {
	switch t := t.(type) {
	case *ast.Ident:
		if g := w.s.global(t); g >= 0 {
			return w.base + "/" + w.plan.pkgs[w.s.groups[g].pkg].dir + "." + w.name(t)
		}
		return predeclaredLink[t.Name]
	case *ast.IndexExpr, *ast.IndexListExpr:
		gen, args := instParts(t)
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = w.key(a)
		}
		return w.key(gen) + "[" + strings.Join(parts, ",") + "]"
	case *ast.StarExpr:
		return "*" + w.key(t.X)
	case *ast.ArrayType:
		return "[]" + w.key(t.Elt)
	case *ast.MapType:
		return "map[" + w.key(t.Key) + "]" + w.key(t.Value)
	case *ast.ParenExpr:
		return w.key(t.X)
	}
	panic("lower: an instance part inst cannot name")
}

// text is t as Go text in package from; uses collects the packages it
// names.
func (w *instWriter) text(t ast.Expr, uses map[int]bool) string {
	switch t := t.(type) {
	case *ast.Ident:
		if g := w.s.global(t); g >= 0 {
			k := w.s.groups[g].pkg
			uses[k] = true
			return w.plan.pkgs[k].alias + "." + w.name(t)
		}
		return t.Name
	case *ast.IndexExpr, *ast.IndexListExpr:
		gen, args := instParts(t)
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = w.text(a, uses)
		}
		return w.text(gen, uses) + "[" + strings.Join(parts, ", ") + "]"
	case *ast.StarExpr:
		return "*" + w.text(t.X, uses)
	case *ast.ArrayType:
		return "[]" + w.text(t.Elt, uses)
	case *ast.MapType:
		return "map[" + w.text(t.Key, uses) + "]" + w.text(t.Value, uses)
	case *ast.ParenExpr:
		return w.text(t.X, uses)
	}
	panic("lower: an instance part inst cannot name")
}

// lists are each package's shared instances by key, and body the
// declarations of inst: one blank variable per instance, a pointer for a
// type (so a type of no zero value costs nothing) and a value for a
// function.
func (w *instWriter) lists(insts []splitInst, uses map[int]bool) (map[int][]string, string, int) {
	byPkg := map[int]map[string]bool{}
	decl := map[string]string{}
	for _, in := range insts {
		k := w.key(in.x)
		if byPkg[in.pkg] == nil {
			byPkg[in.pkg] = map[string]bool{}
		}
		byPkg[in.pkg][k] = true
		if _, ok := decl[k]; ok {
			continue
		}
		gen, _ := instParts(in.x)
		if w.s.isType(gen) {
			decl[k] = "_ *" + w.text(in.x, uses)
		} else {
			decl[k] = "_ = " + w.text(in.x, uses)
		}
	}
	lists := map[int][]string{}
	for p, keys := range byPkg {
		lists[p] = slices.Sorted(maps.Keys(keys))
	}
	var b strings.Builder
	b.WriteString("\n// Each instance the program's packages name, compiled here once\n// (MONO-SHARED; lower/pkginst.go).\nvar (\n")
	for _, k := range slices.Sorted(maps.Keys(decl)) {
		b.WriteString("\t" + decl[k] + "\n")
	}
	b.WriteString(")\n")
	return lists, b.String(), len(decl)
}

// isType reports whether gen names a type.
func (s *splitInsts) isType(gen *ast.Ident) bool {
	obj := s.scope.Lookup(gen.Name)
	_, ok := obj.Decl.(*ast.TypeSpec)
	return ok
}
