// Package provenance is the checker's source-provenance analyzer (check-v1
// unit E2d). A diagnostic lies in the file that holds the node it is
// reported at. A node the checker makes is in no file's tree, so it records
// its origin through made or clone and lies in its origin's file. A syntax
// node the checker makes any other way has no file, and a diagnostic at it
// is an internal compiler error (E2 round 3 M1).
//
// The analyzer holds that class over SSA, per function and per generic
// instance, so it reasons about values rather than spellings. It replaces
// the inventory that owner_e2_test.go kept over go/types (E2b, E2c), whose
// gaps were flows: a closure at a parser site carried the parser to any pass
// (E2c round 1 review M1), a type that existed only by substitution escaped
// the layout ban (m2), and an interface hid a node-returning syntax method
// (m3).
//
// In a package the configuration lists, outside the functions its manifest
// trusts (made and clone), the analyzer flags:
//   - an allocation of a node, or of storage that holds nodes by value (an
//     Alloc from a composite literal, new or an escaping variable, MakeSlice,
//     MakeMap, MakeChan), unless it is a construction: a fresh node whose
//     only uses are its own field stores and one made call, at a site the
//     manifest lists with its kind;
//   - any SSA value that holds a node by value: a copy;
//   - a Convert, ChangeType or MultiConvert into a node, into a pointer to a
//     node from a pointer to anything else, or into a type parameter whose
//     type set holds a node;
//   - a call to a syntax function, method or function-valued variable that
//     yields a node, outside the sites the manifest lists for it, and any
//     use of one as a value (a function value, a method value, a closure
//     over a bound method);
//   - a conversion to an interface, from a type or an interface that a
//     syntax method yielding a node implements it with (m3);
//   - a closure, or a function literal used as a value, whose function makes
//     a node the analyzer flags;
//   - a reference to the package's own caller-text entries (CheckSource)
//     outside their listed sites;
//   - made or clone used as a value, a clone of an unknown type, and a call
//     or value of a generic instance with a node type argument;
//   - in a generic body, a composite literal, new or dereference of a type
//     parameter;
//   - any type the package declares, spells or holds in an SSA value of any
//     instance, other than a syntax node type, with a syntax node's layout;
//   - a local node type, an alias of an unnamed node type, and an import off
//     the manifest's allow-list (reflect and unsafe never on it).
//
// Sites are SSA functions named relative to the package, closures as Go's
// SSA names them (CheckSource$1, prelude$1, nested f$1$2), so a closure at a
// listed site is a site of its own and is not listed.
package provenance

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// Analyzer is the provenance analyzer. Its -manifest flag names a manifest
// file (manifest.go); it may be given more than once. A package no manifest
// names is not analysed.
var Analyzer = New()

// New returns a fresh analyzer with its own -manifest flag.
func New() *analysis.Analyzer {
	var paths manifestPaths
	a := &analysis.Analyzer{
		Name: "provenance",
		Doc:  "flag syntax nodes a checker makes outside its registered constructions",
		URL:  "https://github.com/cevr/effra",
	}
	a.Flags.Var(&paths, "manifest", "a provenance manifest file (repeatable)")
	a.Run = func(pass *analysis.Pass) (any, error) {
		for _, path := range paths {
			m, err := ReadManifest(path)
			if err != nil {
				return nil, err
			}
			if m.Package == pass.Pkg.Path() {
				return run(m, pass)
			}
		}
		return nil, nil
	}
	return a
}

type manifestPaths []string

func (p *manifestPaths) String() string { return strings.Join(*p, ",") }

func (p *manifestPaths) Set(v string) error {
	*p = append(*p, v)
	return nil
}

type state struct {
	pass   *analysis.Pass
	m      *Manifest
	syntax *types.Package
	node   *types.Interface
	prog   *ssa.Program
	pkg    *ssa.Package

	reported map[string]bool
	// creates records the functions with a flagged node.
	creates map[*ssa.Function]bool
	sites   map[Site]int
	calls   map[Site]int // callee and site
	// layouts are the syntax node types' underlying types.
	layouts    []layout
	layoutSeen map[types.Type]bool
	// implementers caches, per interface, the syntax methods that yield a
	// node and implement one of its methods.
	implementers map[*types.Interface][]string
}

type layout struct {
	name string
	typ  types.Type
}

func run(m *Manifest, pass *analysis.Pass) (any, error) {
	s := &state{
		pass: pass, m: m,
		reported: map[string]bool{}, creates: map[*ssa.Function]bool{},
		sites: map[Site]int{}, calls: map[Site]int{},
		layoutSeen: map[types.Type]bool{}, implementers: map[*types.Interface][]string{},
	}
	s.imports()
	for _, imp := range pass.Pkg.Imports() {
		if imp.Path() == m.Syntax {
			s.syntax = imp
		}
	}
	if s.syntax == nil {
		return nil, nil
	}
	nodeObj, ok := s.syntax.Scope().Lookup("Node").(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("package %s declares no Node type", m.Syntax)
	}
	s.node = nodeObj.Type().Underlying().(*types.Interface)
	s.collectLayouts()
	s.declarations()
	s.build()
	funcs := s.functions()
	for _, fn := range funcs {
		s.function(fn)
	}
	s.closures(funcs)
	s.manifestChecks()
	return nil, nil
}

// build makes the package's SSA with every generic instance built from its
// body (buildssa builds instances as wrappers of the generic body, which
// would hide a node made only by substitution, E2c round 1 review m2).
func (s *state) build() {
	s.prog = ssa.NewProgram(s.pass.Fset, ssa.InstantiateGenerics)
	created := map[*types.Package]bool{}
	var createAll func([]*types.Package)
	createAll = func(pkgs []*types.Package) {
		for _, p := range pkgs {
			if !created[p] {
				created[p] = true
				s.prog.CreatePackage(p, nil, nil, true)
				createAll(p.Imports())
			}
		}
	}
	createAll(s.pass.Pkg.Imports())
	s.pkg = s.prog.CreatePackage(s.pass.Pkg, s.pass.Files, s.pass.TypesInfo, false)
	s.pkg.Build()
}

// functions returns every function of the package to analyse: the declared
// functions and methods, the package initializer, their closures, and every
// generic instance they reach, transitively.
func (s *state) functions() []*ssa.Function {
	seen := map[*ssa.Function]bool{}
	var out []*ssa.Function
	var add func(fn *ssa.Function)
	add = func(fn *ssa.Function) {
		if fn == nil || seen[fn] || s.home(fn) != s.pkg {
			return
		}
		if fn.Synthetic != "" && fn.Origin() == nil && fn != s.pkg.Func("init") {
			return // wrappers and thunks: their objects are checked at each use
		}
		seen[fn] = true
		out = append(out, fn)
		for _, anon := range fn.AnonFuncs {
			add(anon)
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, op := range instr.Operands(nil) {
					if f, ok := (*op).(*ssa.Function); ok {
						add(f)
					}
				}
			}
		}
	}
	add(s.pkg.Func("init"))
	for _, f := range s.pass.Files {
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if obj, ok := s.pass.TypesInfo.Defs[fd.Name].(*types.Func); ok {
					add(s.prog.FuncValue(obj))
				}
			}
		}
	}
	return out
}

// home is the package whose source declares fn.
func (s *state) home(fn *ssa.Function) *ssa.Package {
	for {
		switch {
		case fn.Parent() != nil:
			fn = fn.Parent()
		case fn.Origin() != nil && fn.Origin() != fn:
			fn = fn.Origin()
		default:
			return fn.Pkg
		}
	}
}

// site names fn relative to its package, closures by their SSA suffix.
func (s *state) site(fn *ssa.Function) string {
	if p := fn.Parent(); p != nil {
		name := fn.Name()
		return s.site(p) + name[strings.LastIndex(name, "$"):]
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	return fn.RelString(s.pass.Pkg)
}

// top is the declared function fn belongs to.
func (s *state) top(fn *ssa.Function) *ssa.Function {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	return fn
}

func (s *state) trusted(fn *ssa.Function) bool {
	t := s.top(fn)
	if t.Pkg != s.pkg || t.Signature.Recv() != nil {
		return false
	}
	return t.Name() == s.m.Made || t.Name() == s.m.Clone
}

// genericBody reports a function built over its type parameters.
func (s *state) genericBody(fn *ssa.Function) bool {
	for f := fn; f != nil; f = f.Parent() {
		if f.TypeParams().Len() > 0 && len(f.TypeArgs()) == 0 {
			return true
		}
	}
	return false
}

func (s *state) report(fn *ssa.Function, pos token.Pos, format string, args ...any) {
	if !pos.IsValid() && fn != nil {
		pos = fn.Pos()
	}
	msg := fmt.Sprintf(format, args...)
	key := s.pass.Fset.Position(pos).String() + " " + msg
	if fn != nil {
		s.creates[fn] = true
	}
	if s.reported[key] {
		return
	}
	s.reported[key] = true
	s.pass.Report(analysis.Diagnostic{Pos: pos, Message: msg})
}

// ---- types

func (s *state) kind(t types.Type) string {
	return types.TypeString(t, func(*types.Package) string { return "" })
}

// nodeType reports a node: a type that is neither a pointer, an interface
// nor a type parameter and implements syntax.Node by value or by pointer.
func (s *state) nodeType(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	t = types.Unalias(t)
	switch t.(type) {
	case *types.TypeParam, *types.Tuple, *types.Signature, *types.Basic:
		// An unnamed basic type has no methods; the invalid type, which SSA
		// gives an absent key or value of a range (ssa.Next), implements
		// every interface to go/types.
		return "", false
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Interface:
		return "", false
	}
	if types.Implements(t, s.node) || types.Implements(types.NewPointer(t), s.node) {
		return s.kind(t), true
	}
	return "", false
}

// refersToNode is the inventory's isNode: a node, a pointer to one, or an
// interface that implements syntax.Node.
func (s *state) refersToNode(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	switch t.(type) {
	case *types.TypeParam, *types.Basic:
		return "", false
	}
	return s.kind(t), types.Implements(t, s.node) || types.Implements(types.NewPointer(t), s.node)
}

// holds reports a value type that holds a node by value: a node, or an
// array or struct with one.
func (s *state) holds(t types.Type, seen map[types.Type]bool) (string, bool) {
	if t == nil || seen[t] {
		return "", false
	}
	seen[t] = true
	if kind, ok := s.nodeType(t); ok {
		return kind, true
	}
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Array:
		return s.holds(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if kind, ok := s.holds(u.Field(i).Type(), seen); ok {
				return kind, true
			}
		}
	case *types.Tuple:
		for i := range u.Len() {
			if kind, ok := s.holds(u.At(i).Type(), seen); ok {
				return kind, true
			}
		}
	}
	return "", false
}

// stores reports a type whose storage holds a node by value: holds, or a
// slice, map or channel of such a type.
func (s *state) stores(t types.Type, seen map[types.Type]bool) (string, bool) {
	if t == nil || seen[t] {
		return "", false
	}
	if kind, ok := s.holds(t, map[types.Type]bool{}); ok {
		return kind, true
	}
	seen[t] = true
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Array:
		return s.stores(u.Elem(), seen)
	case *types.Slice:
		return s.stores(u.Elem(), seen)
	case *types.Chan:
		return s.stores(u.Elem(), seen)
	case *types.Map:
		if kind, ok := s.stores(u.Key(), seen); ok {
			return kind, true
		}
		return s.stores(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if kind, ok := s.stores(u.Field(i).Type(), seen); ok {
				return kind, true
			}
		}
	}
	return "", false
}

// yields reports whether a value of t can hold or yield a node (the
// inventory's containsNode): through pointers, containers and results.
func (s *state) yields(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if tuple, ok := t.(*types.Tuple); ok {
		for i := range tuple.Len() {
			if s.yields(tuple.At(i).Type(), seen) {
				return true
			}
		}
		return false
	}
	if _, ok := s.refersToNode(t); ok {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return s.yields(u.Elem(), seen)
	case *types.Slice:
		return s.yields(u.Elem(), seen)
	case *types.Array:
		return s.yields(u.Elem(), seen)
	case *types.Chan:
		return s.yields(u.Elem(), seen)
	case *types.Map:
		return s.yields(u.Key(), seen) || s.yields(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if s.yields(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		return s.yields(u.Results(), seen)
	}
	return false
}

func (s *state) yieldsNode(sig *types.Signature) bool {
	return s.yields(sig.Results(), map[types.Type]bool{})
}

// nodeTerm reports a term of a constraint's type set that is a node or a
// pointer to one at any depth, named pointers included (E2c round 1 review
// o2), walking unions, ~ terms and embedded interfaces.
func (s *state) nodeTerm(constraint types.Type, seen map[types.Type]bool) (string, bool) {
	iface, ok := constraint.Underlying().(*types.Interface)
	if !ok || seen[constraint] {
		return "", false
	}
	seen[constraint] = true
	for i := range iface.NumEmbeddeds() {
		var terms []types.Type
		if u, ok := types.Unalias(iface.EmbeddedType(i)).(*types.Union); ok {
			for j := range u.Len() {
				terms = append(terms, u.Term(j).Type())
			}
		} else {
			terms = append(terms, iface.EmbeddedType(i))
		}
		for _, term := range terms {
			term = types.Unalias(term)
			if _, ok := term.(*types.TypeParam); !ok {
				if _, ok := term.Underlying().(*types.Interface); ok {
					if kind, ok := s.nodeTerm(term, seen); ok {
						return kind, true
					}
					continue
				}
			}
			for {
				if _, ok := term.(*types.TypeParam); ok {
					break
				}
				p, ok := term.Underlying().(*types.Pointer)
				if !ok {
					break
				}
				term = types.Unalias(p.Elem())
			}
			if kind, ok := s.nodeType(term); ok {
				return kind, true
			}
		}
	}
	return "", false
}

// parameter reports a type parameter or a pointer to one.
func parameter(t types.Type) (*types.TypeParam, bool) {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	tp, ok := t.(*types.TypeParam)
	return tp, ok
}

// parameterized reports a type that mentions a type parameter.
func parameterized(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := types.Unalias(t).(type) {
	case *types.TypeParam:
		return true
	case *types.Named:
		for i := range u.TypeArgs().Len() {
			if parameterized(u.TypeArgs().At(i), seen) {
				return true
			}
		}
		return false
	case *types.Pointer:
		return parameterized(u.Elem(), seen)
	case *types.Slice:
		return parameterized(u.Elem(), seen)
	case *types.Array:
		return parameterized(u.Elem(), seen)
	case *types.Chan:
		return parameterized(u.Elem(), seen)
	case *types.Map:
		return parameterized(u.Key(), seen) || parameterized(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if parameterized(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Tuple:
		for i := range u.Len() {
			if parameterized(u.At(i).Type(), seen) {
				return true
			}
		}
	case *types.Signature:
		return parameterized(u.Params(), seen) || parameterized(u.Results(), seen)
	}
	return false
}

// syntaxName names a syntax object relative to its package.
func (s *state) syntaxName(obj types.Object) string {
	if fn, ok := obj.(*types.Func); ok {
		return strings.ReplaceAll(fn.FullName(), s.syntax.Path()+".", "")
	}
	return obj.Name()
}

// ---- package rules

// imports holds the reviewed import allow-list (E2b round 3 review M2):
// a package off it could build a node out of the analyzer's sight.
func (s *state) imports() {
	for _, f := range s.pass.Files {
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			switch {
			case path == "reflect" || path == "unsafe":
				s.report(nil, imp.Pos(), "%s import permits unrecorded construction", path)
			case !slices.Contains(s.m.Imports, path):
				s.report(nil, imp.Pos(), "import %q is not in the manifest's reviewed import allow-list", path)
			}
		}
	}
}

// declarations flags a local node type and an alias of an unnamed node
// type, reached or not, and runs the layout ban over every type the
// package declares or spells.
func (s *state) declarations() {
	info := s.pass.TypesInfo
	for id, obj := range info.Defs {
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		if !tn.IsAlias() {
			if _, ok := tn.Type().(*types.Named); ok {
				if _, ok := s.refersToNode(tn.Type()); ok {
					s.report(nil, id.Pos(), "local node type %s is outside the parser's trees", tn.Name())
				}
			}
			continue
		}
		// An alias of a named node type only renames a syntax type; an
		// alias of an unnamed one declares a node type.
		target := types.Unalias(tn.Type())
		if p, ok := target.(*types.Pointer); ok {
			target = types.Unalias(p.Elem())
		}
		if _, named := target.(*types.Named); !named {
			if kind, ok := s.refersToNode(target); ok {
				s.report(nil, id.Pos(), "alias %s declares the unnamed node type %s", tn.Name(), kind)
			}
		}
	}
	type ref struct {
		at  token.Pos
		typ types.Type
	}
	var refs []ref
	for e, tv := range info.Types {
		refs = append(refs, ref{e.Pos(), tv.Type})
	}
	for id, obj := range info.Defs {
		if obj != nil {
			refs = append(refs, ref{id.Pos(), obj.Type()})
		}
	}
	for id, inst := range info.Instances {
		refs = append(refs, ref{id.Pos(), inst.Type})
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].at < refs[j].at })
	for _, r := range refs {
		s.layout(nil, r.typ, r.at)
	}
}

func (s *state) collectLayouts() {
	for _, name := range s.syntax.Scope().Names() {
		tn, ok := s.syntax.Scope().Lookup(name).(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		if _, ok := s.refersToNode(tn.Type()); !ok {
			continue
		}
		if _, ok := tn.Type().Underlying().(*types.Interface); !ok {
			s.layouts = append(s.layouts, layout{name, tn.Type().Underlying()})
		}
	}
}

// layout holds the type-level condition of every conversion: Go converts a
// value to a node only from a type whose underlying type is identical to
// the node's, ignoring tags, or from a pointer to one. So no type the
// package holds, at any depth, other than the syntax node types, may have a
// node's underlying type. Run over SSA values, it sees the types of every
// generic instance, which closes E2c round 1 review m2.
func (s *state) layout(fn *ssa.Function, t types.Type, at token.Pos) {
	qualifier := func(p *types.Package) string {
		if p == s.pass.Pkg {
			return ""
		}
		return p.Name()
	}
	check := func(t types.Type, at token.Pos) {
		for _, l := range s.layouts {
			if types.IdenticalIgnoreTags(t.Underlying(), l.typ) {
				s.report(fn, at, "type %s has the layout of syntax.%s; a conversion from it makes a node outside made and clone", types.TypeString(t, qualifier), l.name)
				return
			}
		}
	}
	var walk, parts func(t types.Type, at token.Pos)
	walk = func(t types.Type, at token.Pos) {
		if t == nil || s.layoutSeen[t] {
			return
		}
		s.layoutSeen[t] = true
		switch n := t.(type) {
		case *types.Alias:
			walk(types.Unalias(n), at)
			return
		case *types.Named:
			if n.Obj().Pkg() == s.syntax {
				if _, ok := s.refersToNode(n); ok {
					return
				}
			}
			if n.Obj().Pkg() == s.pass.Pkg && fn == nil {
				at = n.Obj().Pos()
			}
			check(n, at)
			for i := range n.TypeArgs().Len() {
				walk(n.TypeArgs().At(i), at)
			}
			s.layoutSeen[n.Underlying()] = true
			parts(n.Underlying(), at)
			return
		}
		check(t, at)
		parts(t, at)
	}
	parts = func(t types.Type, at token.Pos) {
		switch u := t.(type) {
		case *types.Pointer:
			walk(u.Elem(), at)
		case *types.Slice:
			walk(u.Elem(), at)
		case *types.Array:
			walk(u.Elem(), at)
		case *types.Chan:
			walk(u.Elem(), at)
		case *types.Map:
			walk(u.Key(), at)
			walk(u.Elem(), at)
		case *types.Struct:
			for i := range u.NumFields() {
				walk(u.Field(i).Type(), at)
			}
		case *types.Tuple:
			for i := range u.Len() {
				walk(u.At(i).Type(), at)
			}
		case *types.Signature:
			for _, tuple := range []*types.Tuple{u.Params(), u.Results()} {
				if tuple != nil {
					parts(tuple, at)
				}
			}
			for i := range u.TypeParams().Len() {
				walk(u.TypeParams().At(i), at)
			}
		case *types.Interface:
			for i := range u.NumExplicitMethods() {
				walk(u.ExplicitMethod(i).Type(), at)
			}
			for i := range u.NumEmbeddeds() {
				walk(u.EmbeddedType(i), at)
			}
		case *types.Union:
			for i := range u.Len() {
				walk(u.Term(i).Type(), at)
			}
		case *types.TypeParam:
			walk(u.Constraint(), at)
		}
	}
	walk(t, at)
}

// ---- function rules

func (s *state) function(fn *ssa.Function) {
	for _, p := range fn.Params {
		s.layout(fn, p.Type(), p.Pos())
	}
	if s.trusted(fn) {
		return
	}
	name := s.site(fn)
	generic := s.genericBody(fn)
	for _, p := range fn.Params {
		if kind, ok := s.holds(p.Type(), map[types.Type]bool{}); ok {
			s.report(fn, p.Pos(), "a %s value the checker copies outside made and clone (parameter %s of %s)", kind, p.Name(), name)
		}
	}
	for _, fv := range fn.FreeVars {
		if kind, ok := s.holds(fv.Type(), map[types.Type]bool{}); ok {
			s.report(fn, fv.Pos(), "a %s value the checker copies outside made and clone (captured %s in %s)", kind, fv.Name(), name)
		}
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			s.instruction(fn, name, generic, instr)
		}
	}
}

func (s *state) instruction(fn *ssa.Function, name string, generic bool, instr ssa.Instruction) {
	pos := instr.Pos()
	if v, ok := instr.(ssa.Value); ok {
		s.layout(fn, v.Type(), pos)
	}
	// Function and global operands: a value position is any but a static
	// call's callee.
	var callee *ssa.Value
	if call, ok := instr.(ssa.CallInstruction); ok && !call.Common().IsInvoke() {
		callee = &call.Common().Value
	}
	for _, op := range instr.Operands(nil) {
		if op == nil || *op == nil {
			continue
		}
		switch v := (*op).(type) {
		case *ssa.Function:
			if op == callee {
				s.staticCall(fn, name, instr, v)
			} else {
				s.functionValue(fn, name, pos, v)
			}
		case *ssa.Global:
			s.global(fn, name, instr, v)
		}
	}

	switch instr := instr.(type) {
	case *ssa.Alloc:
		elem := instr.Type().(*types.Pointer).Elem()
		if generic {
			if tp, ok := types.Unalias(elem).(*types.TypeParam); ok && !s.variable(instr) {
				s.report(fn, pos, "construction or copy of type parameter %s outside made and clone", tp.Obj().Name())
			}
		}
		if kind, ok := s.nodeType(elem); ok && s.registered(instr) {
			s.construction(fn, name, pos, kind)
			return
		}
		if kind, ok := s.holds(elem, map[types.Type]bool{}); ok {
			s.report(fn, pos, "allocation of a %s outside made and clone (in %s)", kind, name)
		}
		return
	case *ssa.MakeSlice, *ssa.MakeMap, *ssa.MakeChan:
		if kind, ok := s.stores(instr.(ssa.Value).Type(), map[types.Type]bool{}); ok {
			s.report(fn, pos, "storage of %s node values made outside made and clone (in %s)", kind, name)
		}
		return
	case *ssa.Convert:
		s.conversion(fn, pos, instr.X.Type(), instr.Type())
		return
	case *ssa.ChangeType:
		s.conversion(fn, pos, instr.X.Type(), instr.Type())
		return
	case *ssa.MultiConvert:
		s.conversion(fn, pos, instr.X.Type(), instr.Type())
		return
	case *ssa.MakeInterface:
		s.interfaceConversion(fn, name, pos, instr.X.Type(), instr.Type(), false)
	case *ssa.ChangeInterface:
		s.interfaceConversion(fn, name, pos, instr.X.Type(), instr.Type(), true)
	case *ssa.TypeAssert:
		if _, ok := instr.AssertedType.Underlying().(*types.Interface); ok {
			s.interfaceConversion(fn, name, pos, instr.X.Type(), instr.AssertedType, true)
		}
	case *ssa.UnOp:
		// In a generic body, a dereference the source writes (*p) copies a
		// value of a type parameter with no instance in sight; an element or
		// field read (xs[i], s.f) is checked in each instance the package
		// builds, as every other copy is.
		if generic && instr.Op == token.MUL && !elementAddress(instr.X) {
			if tp, ok := parameter(instr.Type()); ok && !variableCell(instr.X) {
				s.report(fn, pos, "construction or copy of type parameter %s outside made and clone", tp.Obj().Name())
			}
		}
	}
	if call, ok := instr.(ssa.CallInstruction); ok {
		s.call(fn, name, pos, call.Common())
	}
	if v, ok := instr.(ssa.Value); ok {
		if kind, ok := s.holds(v.Type(), map[types.Type]bool{}); ok {
			s.report(fn, pos, "a %s value the checker makes or copies outside made and clone (%s in %s)", kind, strings.TrimPrefix(fmt.Sprintf("%T", instr), "*ssa."), name)
		}
	}
}

// variable reports an Alloc that is a variable's cell, with no composite
// literal or new behind it: its first store is not a construction.
func (s *state) variable(a *ssa.Alloc) bool {
	return a.Comment != "complit" && a.Comment != "new" && a.Comment != "slicelit" && a.Comment != "varargs"
}

// variableCell reports a variable's cell: a load from it reads a variable,
// it does not dereference a pointer the source wrote.
func variableCell(v ssa.Value) bool {
	switch v := v.(type) {
	case *ssa.Alloc:
		return v.Comment != "complit" && v.Comment != "new"
	case *ssa.FreeVar:
		return true
	}
	return false
}

// elementAddress reports the address of a slice or array element or of a
// struct field.
func elementAddress(v ssa.Value) bool {
	switch v.(type) {
	case *ssa.IndexAddr, *ssa.FieldAddr:
		return true
	}
	return false
}

// registered reports a fresh node whose only uses are stores into its own
// fields and one made call that takes it as the node.
func (s *state) registered(a *ssa.Alloc) bool {
	if a.Comment != "complit" && a.Comment != "new" {
		return false
	}
	made := 0
	for _, r := range *a.Referrers() {
		switch r := r.(type) {
		case *ssa.FieldAddr:
			for _, rr := range *r.Referrers() {
				if st, ok := rr.(*ssa.Store); !ok || st.Addr != r || st.Val == ssa.Value(a) {
					return false
				}
			}
		case *ssa.Call:
			c := r.Common()
			f, ok := c.Value.(*ssa.Function)
			if !ok || c.IsInvoke() || !s.isHelper(f, s.m.Made) || len(c.Args) < 2 || c.Args[1] != ssa.Value(a) {
				return false
			}
			for i, arg := range c.Args {
				if i != 1 && arg == ssa.Value(a) {
					return false
				}
			}
			made++
		default:
			return false
		}
	}
	return made == 1
}

// isHelper reports f, or the generic function f instantiates, as the
// package's helper of that name.
func (s *state) isHelper(f *ssa.Function, name string) bool {
	if o := f.Origin(); o != nil {
		f = o
	}
	return f.Pkg == s.pkg && f.Parent() == nil && f.Signature.Recv() == nil && f.Name() == name
}

func (s *state) construction(fn *ssa.Function, name string, pos token.Pos, kind string) {
	site := Site{Fn: name, Kind: kind}
	s.sites[site]++
	if !slices.ContainsFunc(s.m.Sites, func(listed Site) bool { return listed.key() == site }) {
		s.report(fn, pos, "construction site %s makes a %s and is not in the manifest", name, kind)
	}
}

func (s *state) conversion(fn *ssa.Function, pos token.Pos, from, to types.Type) {
	if tp, ok := types.Unalias(to).(*types.TypeParam); ok {
		if kind, ok := s.nodeTerm(tp.Constraint(), map[types.Type]bool{}); ok {
			s.report(fn, pos, "conversion to type parameter %s, whose type set holds the node %s, outside made and clone", tp.Obj().Name(), kind)
		}
		return
	}
	if kind, ok := s.nodeType(to); ok {
		s.report(fn, pos, "conversion produces a %s node outside made and clone", kind)
		return
	}
	// A pointer conversion retypes memory unless its pointee stays the same
	// type: identRef(x) for x *syntax.Ident only renames the pointer.
	pt, ok := types.Unalias(to).Underlying().(*types.Pointer)
	if !ok {
		return
	}
	kind, ok := s.nodeType(pt.Elem())
	if !ok {
		return
	}
	if pf, ok := types.Unalias(from).Underlying().(*types.Pointer); ok && types.Identical(pf.Elem(), pt.Elem()) {
		return
	}
	s.report(fn, pos, "conversion produces a %s node outside made and clone", kind)
}

// interfaceConversion flags a conversion to an interface that a syntax
// method yielding a node implements (E2c round 1 review m3): a call
// through the interface reaches that method from any site. From a concrete
// type, the method is the type's own; from an interface (dynamic), any
// syntax type that implements the target counts.
func (s *state) interfaceConversion(fn *ssa.Function, name string, pos token.Pos, from, to types.Type, dynamic bool) {
	iface, ok := to.Underlying().(*types.Interface)
	if !ok {
		return
	}
	var methods []string
	if dynamic {
		methods = s.syntaxImplementers(iface)
	} else {
		ms := s.prog.MethodSets.MethodSet(from)
		for i := range iface.NumMethods() {
			m := iface.Method(i)
			if !s.yieldsNode(m.Signature()) {
				continue
			}
			if sel := ms.Lookup(m.Pkg(), m.Name()); sel != nil && sel.Obj().Pkg() == s.syntax {
				methods = append(methods, s.syntaxName(sel.Obj()))
			}
		}
	}
	for _, m := range methods {
		s.report(fn, pos, "syntax method %s yields a node through interface %s in %s", m, s.kind(to), name)
	}
}

func (s *state) syntaxImplementers(iface *types.Interface) []string {
	if out, ok := s.implementers[iface]; ok {
		return out
	}
	var out []string
	for i := range iface.NumMethods() {
		m := iface.Method(i)
		if !s.yieldsNode(m.Signature()) {
			continue
		}
		for _, name := range s.syntax.Scope().Names() {
			tn, ok := s.syntax.Scope().Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			if _, ok := tn.Type().Underlying().(*types.Interface); ok {
				continue
			}
			for _, t := range []types.Type{tn.Type(), types.NewPointer(tn.Type())} {
				if !types.Implements(t, iface) {
					continue
				}
				if sel := s.prog.MethodSets.MethodSet(t).Lookup(m.Pkg(), m.Name()); sel != nil && sel.Obj().Pkg() == s.syntax {
					if n := s.syntaxName(sel.Obj()); !slices.Contains(out, n) {
						out = append(out, n)
					}
				}
			}
		}
	}
	sort.Strings(out)
	s.implementers[iface] = out
	return out
}

// object is the source object of a function, a wrapper's or thunk's
// method included.
func object(f *ssa.Function) types.Object {
	if o := f.Origin(); o != nil {
		f = o
	}
	return f.Object()
}

// staticCall checks a call whose callee is a function.
func (s *state) staticCall(fn *ssa.Function, name string, instr ssa.Instruction, f *ssa.Function) {
	pos := instr.Pos()
	if obj := object(f); obj != nil && obj.Pkg() == s.syntax && s.yieldsNode(f.Signature) {
		s.syntaxCall(fn, name, pos, s.syntaxName(obj))
	}
	if s.isEntry(f) != "" {
		s.entry(fn, name, pos, s.isEntry(f))
	}
	switch {
	case s.isHelper(f, s.m.Clone):
		targs := f.TypeArgs()
		if len(targs) == 0 || parameterized(targs[0], map[types.Type]bool{}) {
			if len(targs) == 0 || !s.genericBody(fn) {
				s.report(fn, pos, "a clone whose copied type is unknown (in %s)", name)
			}
			return
		}
		if kind, ok := s.nodeType(targs[0]); ok {
			s.construction(fn, name, pos, kind)
		}
	case s.isHelper(f, s.m.Made):
	default:
		s.instantiation(fn, pos, f)
	}
}

// instantiation flags a generic instance with a node type argument, but
// made and clone: its body copies or makes nodes, built or not.
func (s *state) instantiation(fn *ssa.Function, pos token.Pos, f *ssa.Function) {
	for _, t := range f.TypeArgs() {
		if kind, ok := s.stores(t, map[types.Type]bool{}); ok {
			fname := f.Name()
			if o := f.Origin(); o != nil {
				fname = o.Name()
			}
			s.report(fn, pos, "generic instantiation %s with node argument %s", fname, kind)
			return
		}
	}
}

func (s *state) isEntry(f *ssa.Function) string {
	if f.Pkg != s.pkg || f.Parent() != nil {
		return ""
	}
	n := f.RelString(s.pass.Pkg)
	if _, ok := s.m.Entries[n]; ok {
		return n
	}
	return ""
}

func (s *state) entry(fn *ssa.Function, name string, pos token.Pos, entry string) {
	if slices.Contains(s.m.Entries[entry], name) {
		s.calls[Site{Fn: name, Kind: entry}]++
		return
	}
	s.report(fn, pos, "%s parses caller text and is referenced in %s, which the manifest does not list for it", entry, name)
}

func (s *state) syntaxCall(fn *ssa.Function, name string, pos token.Pos, callee string) {
	sites, ok := s.m.Callees[callee]
	switch {
	case !ok:
		s.report(fn, pos, "syntax callee %s yields a node and is not in the manifest", callee)
	case !slices.Contains(sites, name):
		s.report(fn, pos, "syntax callee %s is called in %s, which the manifest does not list for it", callee, name)
	default:
		s.calls[Site{Fn: name, Kind: callee}]++
	}
}

// functionValue checks a function used as a value: stored, passed,
// returned or bound into a closure.
func (s *state) functionValue(fn *ssa.Function, name string, pos token.Pos, f *ssa.Function) {
	if obj := object(f); obj != nil && obj.Pkg() == s.syntax && s.yieldsNode(f.Signature) {
		s.report(fn, pos, "syntax callee %s is used as a value in %s; its listed sites may only call it", s.syntaxName(obj), name)
	}
	if entry := s.isEntry(f); entry != "" {
		s.entry(fn, name, pos, entry)
	}
	for _, helper := range []string{s.m.Made, s.m.Clone} {
		if s.isHelper(f, helper) {
			s.report(fn, pos, "%s is used as a value in %s; it may only be called, so the manifest sees each site", helper, name)
			return
		}
	}
	s.instantiation(fn, pos, f)
}

// global checks a reference to a syntax function-valued variable that
// yields a node: a load whose every use is a call is a call.
func (s *state) global(fn *ssa.Function, name string, instr ssa.Instruction, g *ssa.Global) {
	obj := g.Object()
	if obj == nil || obj.Pkg() != s.syntax {
		return
	}
	sig, ok := obj.Type().Underlying().(*types.Signature)
	if !ok || !s.yieldsNode(sig) {
		return
	}
	if load, ok := instr.(*ssa.UnOp); ok && load.Op == token.MUL {
		called := len(*load.Referrers()) > 0
		for _, r := range *load.Referrers() {
			c, ok := r.(ssa.CallInstruction)
			if !ok || c.Common().IsInvoke() || c.Common().Value != ssa.Value(load) {
				called = false
			}
		}
		if called {
			s.syntaxCall(fn, name, instr.Pos(), s.syntaxName(obj))
			return
		}
	}
	s.report(fn, instr.Pos(), "syntax callee %s is used as a value in %s; its listed sites may only call it", s.syntaxName(obj), name)
}

// call checks an interface call to a syntax method and the builtins that
// copy node values.
func (s *state) call(fn *ssa.Function, name string, pos token.Pos, c *ssa.CallCommon) {
	if c.IsInvoke() {
		if c.Method.Pkg() == s.syntax && s.yieldsNode(c.Method.Signature()) {
			s.syntaxCall(fn, name, pos, s.syntaxName(c.Method))
		}
		return
	}
	if b, ok := c.Value.(*ssa.Builtin); ok && (b.Name() == "append" || b.Name() == "copy") && len(c.Args) > 0 {
		if kind, ok := s.stores(c.Args[0].Type(), map[types.Type]bool{}); ok {
			s.report(fn, pos, "%s copies %s node values outside made and clone (in %s)", b.Name(), kind, name)
		}
	}
}

// closures flags a closure, or a function literal used as a value, whose
// function makes a node the analyzer flags.
func (s *state) closures(funcs []*ssa.Function) {
	var makes func(f *ssa.Function) bool
	memo := map[*ssa.Function]bool{}
	makes = func(f *ssa.Function) bool {
		if v, ok := memo[f]; ok {
			return v
		}
		memo[f] = s.creates[f]
		for _, anon := range f.AnonFuncs {
			if makes(anon) {
				memo[f] = true
			}
		}
		return memo[f]
	}
	for _, fn := range funcs {
		if s.trusted(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var callee *ssa.Value
				if call, ok := instr.(ssa.CallInstruction); ok && !call.Common().IsInvoke() {
					callee = &call.Common().Value
				}
				for _, op := range instr.Operands(nil) {
					f, ok := (*op).(*ssa.Function)
					if !ok || op == callee || f.Parent() == nil || !makes(f) {
						continue
					}
					what := "a function value"
					if _, ok := instr.(*ssa.MakeClosure); ok {
						what = "a closure"
					}
					s.report(fn, instr.Pos(), "%s makes a node outside the manifest and becomes %s in %s", s.site(f), what, s.site(fn))
				}
			}
		}
	}
}

// manifestChecks reports a manifest entry the package no longer reaches.
func (s *state) manifestChecks() {
	pos := s.pass.Files[0].Name.Pos()
	for _, site := range s.m.Sites {
		if s.sites[site.key()] == 0 {
			s.report(nil, pos, "the manifest lists construction site %s %s, which the package no longer makes", site.Fn, site.Kind)
		}
	}
	for _, manifest := range []map[string][]string{s.m.Callees, s.m.Entries} {
		names := make([]string, 0, len(manifest))
		for name := range manifest {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, callee := range names {
			for _, site := range manifest[callee] {
				if s.calls[Site{Fn: site, Kind: callee}] == 0 {
					s.report(nil, pos, "the manifest lists %s in %s, which no longer calls it", callee, site)
				}
			}
		}
	}
}
