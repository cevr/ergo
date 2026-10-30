package check

import (
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// run checks the package: collect declarations, resolve types lazily, check
// signatures, check bodies one strongly connected component at a time, then
// the package-level rules that need every body (Go types2's phase order).
func (c *checker) run(files []*syntax.File) *Package {
	for i, f := range files {
		c.files = append(c.files, &fileInfo{syntax: f, index: i, scope: newScope(nil)})
	}
	c.pkg.files = c.files
	// A decided source has no package clause: the directory names the
	// package (grammar.md, Old spellings), so the name is the last element
	// of the import path.
	c.pkg.Name = path.Base(c.pkg.Path)
	c.pkg.qualify = true
	c.collect()
	c.solveDeclKinds()
	for _, tn := range c.pkg.Types {
		c.cur = tn.file
		c.resolveNamed(tn.Type.(*Named))
	}
	// Expanding type instantiations are rejected before any walk expands
	// instances (round-2 M1, M5).
	c.checkInstantiationCycles()
	c.collectSelfFuncs()
	for _, f := range c.funcs {
		c.cur = f.file
		c.funcSig(f)
	}
	// Expanding instantiations in signatures, a method's receiver
	// parameters included, are rejected before any row is inferred
	// (round-4 M1).
	c.checkInstantiationCycles()
	c.collectBodyErrorRows()
	c.checkEntry()
	// The wire pass runs once, before any body, so its budget verdict does
	// not depend on which body meets a wire first (wiresum.go).
	c.wireSummaries()
	// Bodies converge first: a machine's initial value and the named wires
	// are closed contexts that read the converged inferred rows (M2).
	c.checkBodies()
	c.checkTypeDecls()
	c.checkWires()
	c.checkWireCycles()
	c.checkErrorMethods()
	c.checkUnusedImports()
	c.finishInfo()
	c.pkg.ErrorTypes = make(map[*Named]bool, len(c.errorTypes))
	for n := range c.errorTypes {
		c.pkg.ErrorTypes[n] = true
	}
	c.cur = nil
	return c.pkg
}

// collectBodyErrorRows records every struct type that an error row of the
// package names, anywhere: signatures, bodies, function literals, package
// var initializers, machine initial values and wire providers, before any
// body is checked, so whether a type is an error type (types.md, Error types
// 1) does not depend on declaration order (M3). It reads each element's
// head type name syntactically (`Fault`, `pkg.Fault`, `Fault[T]`), so an
// element whose type arguments name a body's type parameter still counts.
// It reports nothing; the checks of the rows report.
func (c *checker) collectBodyErrorRows() {
	for _, fi := range c.files {
		c.cur = fi
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			if er, ok := n.(*syntax.ErrorRow); ok {
				for _, x := range er.Types {
					if n := c.errorRowHead(x); n != nil {
						if _, seen := c.errorTypes[n]; !seen {
							c.errorTypes[n] = x.Pos()
						}
					}
				}
			}
			return true
		})
	}
}

// errorRowHead returns the origin struct type an error-row element names by
// its head, or nil.
func (c *checker) errorRowHead(x syntax.Expr) *Named {
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = ix.X
	}
	tn, n := c.namedHead(x)
	if n == nil || tn.Decl == nil {
		return nil
	}
	if _, isStruct := tn.Decl.Spec.(*syntax.StructType); !isStruct {
		return nil
	}
	return n.origin()
}

// namedHead returns the declared type a name or a qualified name denotes,
// without resolving it, or nil.
func (c *checker) namedHead(x syntax.Expr) (*TypeName, *Named) {
	var o Object
	switch x := x.(type) {
	case *syntax.Ident:
		o = c.lookupType(x.Name, nil)
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			return nil, nil
		}
		if pn, ok := c.lookupType(id.Name, nil).(*PkgName); ok {
			o = pn.Pkg.Scope.names[x.Sel.Name]
		}
	}
	tn, ok := o.(*TypeName)
	if !ok {
		return nil, nil
	}
	n, ok := tn.Type.(*Named)
	if !ok {
		return nil, nil
	}
	return tn, n
}

// ---------------------------------------------------------------- collection

func (c *checker) collect() {
	// A package-level let with a pattern (196p, 196t) declares its names
	// once every type is declared, so a type name in it tests the variant.
	type patternSpec struct {
		spec *syntax.VarSpec
		file *fileInfo
	}
	var patterns []patternSpec
	for _, fi := range c.files {
		c.cur = fi
		f := fi.syntax
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.ImportDecl:
				if len(d.Specs) != 1 || d.Lparen.IsValid() {
					c.undecided(d, "a grouped import")
					continue
				}
				c.importSpec(fi, d.Specs[0])
			case *syntax.TypeDecl:
				tn := &TypeName{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi, id: newID()}
				tn.Type = &Named{Obj: tn}
				if c.declare(d.Name, tn) {
					c.pkg.Types = append(c.pkg.Types, tn)
				}
				// An inline variant declares a record in the package scope
				// (spec D-B1), after its union in source order.
				if it, ok := d.Spec.(*syntax.InterfaceType); ok {
					for _, x := range it.Union {
						if vs, ok := x.(*syntax.VariantSpec); ok {
							c.declareVariant(fi, d, vs)
						}
					}
				}
			case *syntax.FuncDecl:
				if d.Recv != nil {
					c.undecided(d.Recv, "a receiver")
					continue
				}
				fn := &Func{Name: d.Name.Name, Pkg: c.pkg, Pos: d.Name.Pos(), Decl: d, file: fi}
				c.funcs = append(c.funcs, fn)
				// A second function of one name waits for the types:
				// collectSelfFuncs accepts the group when the first
				// parameters are distinct local types (196m rule 3) and
				// reports the redeclaration otherwise.
				if prev, ok := c.pkg.Scope.names[d.Name.Name].(*Func); ok && d.Name.Name != "_" {
					c.preludeName(d.Name) // D-D3, at each declaration
					prev.same = append(prev.same, fn)
					continue
				}
				if c.declare(d.Name, fn) {
					c.pkg.Funcs = append(c.pkg.Funcs, fn)
				}
			case *syntax.VarDecl:
				if len(d.Specs) != 1 || d.Lparen.IsValid() {
					c.undecided(d, "a grouped let")
					continue
				}
				if d.Mut.IsValid() {
					c.mutPackageLet(d)
				}
				for _, spec := range d.Specs {
					if spec.Pattern != nil {
						patterns = append(patterns, patternSpec{spec, fi})
						continue
					}
					var group *pkgPattern
					if len(spec.Names) > 1 && len(spec.Values) == 1 {
						// `let q, r = e` destructures one tuple (196t).
						group = &pkgPattern{spec: spec}
						c.pkgPatterns[spec] = group
					}
					for i, name := range spec.Names {
						// A refused package-level let mut (mutPackageLet)
						// keeps mut, so its assignments add nothing (D3).
						v := &Var{Name: name.Name, Pos: name.Pos(), pkg: true, used: true, mut: d.Mut.IsValid()}
						c.declare(name, v)
						pv := &pkgVar{obj: v, spec: spec, idx: i, file: fi, name: name, group: group}
						if group != nil {
							group.members = append(group.members, pv)
						}
						c.vars = append(c.vars, pv)
					}
				}
			case *syntax.TestDecl:
				c.notYet(d)
			case *syntax.WireDecl:
				c.preludeName(d.Name) // D-D3; the wire still binds
				w := &Wire{Name: d.Name.Name, Pos: d.Name.Pos(), Decl: d, file: fi, pkg: c.pkg}
				if _, dup := c.pkg.Wires[w.Name]; dup {
					c.errorf(d.Name, CodeType, "wire %s is declared twice", w.Name)
					continue
				}
				c.pkg.Wires[w.Name] = w
				c.def(d.Name, w)
			default:
				c.undecided(d, "")
			}
		}
	}
	// A package-level let with a pattern declares the names the pattern
	// binds, read with every type declared (a type name in it tests the
	// type, D-B3 rule 1); checkPkgVar types them all from the one value.
	for _, p := range patterns {
		c.cur = p.file
		group := &pkgPattern{spec: p.spec}
		c.pkgPatterns[p.spec] = group
		for _, id := range patternNames(p.spec.Pattern, c.isPatternType) {
			v := &Var{Name: id.Name, Pos: id.Pos(), pkg: true, used: true}
			c.declare(id, v)
			pv := &pkgVar{obj: v, spec: p.spec, file: p.file, name: id, group: group}
			group.members = append(group.members, pv)
			c.vars = append(c.vars, pv)
		}
		if len(group.members) == 0 {
			// A pattern that binds nothing still checks its value once.
			pv := &pkgVar{obj: &Var{Name: "_", Pos: p.spec.Pos(), pkg: true, used: true}, spec: p.spec, file: p.file, group: group}
			group.members = append(group.members, pv)
			c.vars = append(c.vars, pv)
		}
	}
	// The export guard: the step of an exported machine type spells its
	// rows even when it is unexported (rows-and-constraints §2).
	for _, tn := range c.pkg.Types {
		m, ok := tn.Decl.Spec.(*syntax.MachineType)
		if !ok || !isExported(tn.Name) {
			continue
		}
		if id, ok := m.Step.(*syntax.Ident); ok {
			if f, ok := c.pkg.Scope.names[id.Name].(*Func); ok {
				c.guarded[f] = true
			}
		}
	}
}

// declare enters a package-level name; a second declaration of one name is
// S108 (a type and a function share the package's namespace). A prelude
// name is S108 (D-D3) and still declares, so its uses add nothing.
func (c *checker) declare(id *syntax.Ident, o Object) bool {
	if id.Name == "_" {
		return false
	}
	if c.preludeName(id) {
		// reported; the declaration binds below
	} else if prev, ok := c.pkg.Scope.names[id.Name]; ok {
		what := "declared"
		if _, isType := prev.(*TypeName); isType {
			what = "declared as a type"
		}
		c.errorf(id, CodeType, "%s is already %s in this package", id.Name, what)
		c.dupNames[id.Name] = true
		return false
	}
	c.pkg.Scope.insert(id.Name, o)
	c.def(id, o)
	return true
}

func (c *checker) importSpec(fi *fileInfo, spec *syntax.ImportSpec) {
	// A decided import path is one unquoted token (grammar.md, Lexical 6).
	path := spec.Path.Value
	if spec.Path.Kind != syntax.PATH {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			c.errorf(spec.Path, CodeType, "malformed import path %s", spec.Path.Value)
			return
		}
		path = p
	}
	switch spec.Kind {
	case syntax.ImportPlain, syntax.ImportNamed:
	case syntax.ImportGo:
		c.errorf(spec, CodeOutside, "import go %q is #202's (Go imports); it is not checked yet", path)
		c.bindBrokenImport(fi, spec, path)
		return
	default:
		c.undecided(spec.Path, "this import form")
		return
	}
	pkg, err := c.imp.Import(path)
	if err != nil || pkg == nil {
		c.errorf(spec.Path, CodeUnknown, "unknown package %q", path)
		c.bindBrokenImport(fi, spec, path)
		return
	}
	name := pkg.Name
	if spec.Kind == syntax.ImportNamed {
		name = spec.Name.Name
		c.preludeName(spec.Name) // D-D3; the import still binds
	}
	pn := &PkgName{Name: name, Pos: spec.Pos(), Pkg: pkg, spec: spec}
	if name == "_" {
		return
	}
	if _, dup := fi.scope.names[name]; dup {
		c.errorf(spec, CodeType, "%s is imported twice", name)
		return
	}
	fi.scope.insert(name, pn)
	fi.imports = append(fi.imports, pn)
}

// bindBrokenImport binds the name of a failed import to an empty package,
// so its uses add no diagnostic of their own (D3).
func (c *checker) bindBrokenImport(fi *fileInfo, spec *syntax.ImportSpec, path string) {
	name := path[strings.LastIndex(path, "/")+1:]
	if spec.Kind == syntax.ImportNamed && spec.Name != nil {
		name = spec.Name.Name
	}
	if name == "_" || name == "" {
		return
	}
	if _, dup := fi.scope.names[name]; dup {
		return
	}
	empty := &Package{Path: path, Name: name, Scope: newScope(nil)}
	fi.scope.insert(name, &PkgName{Name: name, Pos: spec.Pos(), Pkg: empty, spec: spec, used: true, broken: true})
}

func (c *checker) checkUnusedImports() {
	for _, fi := range c.files {
		c.cur = fi
		for _, pn := range fi.imports {
			if !pn.used {
				c.errorf(pn.spec, CodeUnused, "package %s is imported and not used", pn.Name)
			}
		}
	}
}

// ---------------------------------------------------------------- type declarations

// resolveNamed resolves a declared type's body once, with Go types2's object
// colouring: a declaration reached again while it resolves is a cycle.
func (c *checker) resolveNamed(n *Named) {
	if n.state != 0 || n.Obj.Decl == nil {
		return
	}
	n.state = 1
	saved := c.cur
	c.cur = n.Obj.file
	defer func() { c.cur = saved }()
	d := n.Obj.Decl
	ctx := &typeCtx{params: map[string]*TypeParam{}, decl: true}
	if d.TypeParams != nil {
		kinds, conflicts := c.bracketKinds(n.Obj)
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				c.preludeName(name) // D-D3; the parameter still binds
				tp := &TypeParam{Name: name.Name, Pos: name.Pos(), id: newID()}
				c.def(name, tp)
				switch {
				case field.Type != nil && isErrorName(field.Type):
					tp.Kind = KindErrors
				case field.Type != nil && c.uncheckedConstraint(field.Type, ctx):
					tp.unchecked = true
				case field.Type != nil:
					tp.Constraint = c.constraint(field.Type, ctx)
				case conflicts[name.Name]:
					c.errorf(name, CodeType, "type parameter %s is used both as a type and as a row (types.md, Generics 1)", name.Name)
					tp.conflict = true
				default:
					tp.Kind = kinds[name.Name]
				}
				if _, dup := ctx.params[name.Name]; dup {
					c.errorf(name, CodeType, "type parameter %s is declared twice", name.Name)
				}
				ctx.params[name.Name] = tp
				n.TParams = append(n.TParams, tp)
			}
		}
	}
	restore := c.monoOwner(n.TParams)
	defer restore()
	switch spec := d.Spec.(type) {
	case *syntax.StructType:
		ctx.field = true
		n.under = c.structType(spec, ctx)
	case *syntax.InterfaceType:
		if len(spec.Union) > 0 {
			n.under = c.unionType(n, spec, ctx)
		} else {
			n.under = c.interfaceType(spec, ctx)
		}
	case *syntax.MachineType:
		// The state and signal must be unions (checkMachine, after every
		// type declaration: a type still being declared here, on a cycle
		// through this machine, has no underlying type yet).
		n.under = &MachineType{State: c.typExpr(spec.State, ctx), Signal: c.typExpr(spec.Signal, ctx)}
		c.machines = append(c.machines, n.Obj)
	default:
		n.under = c.definedType(n, spec, ctx)
	}
	n.state = 2
}

// bracketKinds returns the solved kind of each unconstrained bracket
// parameter of a type declaration and the parameters whose positions
// disagree (solveDeclKinds).
func (c *checker) bracketKinds(tn *TypeName) (map[string]Kind, map[string]bool) {
	kinds := map[string]Kind{}
	i := 0
	ks := c.kinds.kinds[tn]
	for _, f := range tn.Decl.TypeParams.List {
		for _, n := range f.Names {
			if i < len(ks) {
				kinds[n.Name] = ks[i]
			}
			i++
		}
	}
	return kinds, c.kinds.conflicts[tn]
}

func isErrorName(x syntax.Expr) bool {
	id, ok := x.(*syntax.Ident)
	return ok && id.Name == "error"
}

func (c *checker) structType(spec *syntax.StructType, ctx *typeCtx) *Struct {
	s := &Struct{}
	seen := map[string]bool{}
	for _, field := range spec.Fields.List {
		t := c.typExpr(field.Type, ctx)
		for _, name := range field.Names {
			if seen[name.Name] && name.Name != "_" {
				c.errorf(name, CodeType, "field %s is declared twice", name.Name)
				continue
			}
			seen[name.Name] = true
			f := &Field{Name: name.Name, Type: t, Pos: name.Pos()}
			s.Fields = append(s.Fields, f)
			c.def(name, f)
		}
	}
	return s
}

func (c *checker) interfaceType(spec *syntax.InterfaceType, ctx *typeCtx) *Interface {
	it := &Interface{}
	seen := map[string]bool{}
	for _, l := range spec.Laws {
		c.notYet(l)
	}
	for _, m := range spec.Methods {
		ft, ok := m.Type.(*syntax.FuncType)
		if !ok || len(m.Names) == 0 {
			c.errorf(m, CodeType, "an interface lists method signatures")
			continue
		}
		name := m.Names[0]
		mctx := &typeCtx{method: true, reported: new(bool)}
		if ctx != nil {
			// A package var's type has no context (FuzzCheck found
			// var A interface{A()} dereferencing it).
			mctx.params, mctx.decl = ctx.params, ctx.decl
		}
		sig := c.signature(ft, mctx)
		if ft.Needs != nil {
			c.errorf(ft.Needs, CodeType, "an interface method writes no capability row: a provider's requirements are captured when it is built (types.md, Capabilities 4; README Gap 17)")
			sig.Uses = newRow()
		}
		if seen[name.Name] {
			c.errorf(name, CodeType, "method %s is declared twice", name.Name)
			continue
		}
		seen[name.Name] = true
		m := &Method{Name: name.Name, Sig: sig, Pos: name.Pos()}
		it.Methods = append(it.Methods, m)
		c.def(name, m)
		c.defParams(ft, sig)
	}
	// Sorted once, here, for identity (intern.go shape; round 11 M3).
	it.byName = slices.Clone(it.Methods)
	slices.SortStableFunc(it.byName, func(a, b *Method) int { return strings.Compare(a.Name, b.Name) })
	return it
}

// defParams records the declaring names of a signature with no body (an
// operation, an extern function): each named parameter is its Var in sig,
// in the order signature made them.
func (c *checker) defParams(ft *syntax.FuncType, sig *Signature) {
	if ft == nil || ft.Params == nil {
		return
	}
	i := 0
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, pn := range field.Names {
			if i < len(sig.Params) {
				c.def(pn, sig.Params[i])
			}
			i++
		}
	}
}

func (c *checker) unionType(n *Named, spec *syntax.InterfaceType, ctx *typeCtx) Type {
	u := &Union{}
	seen := map[int32]bool{}
	for _, x := range spec.Union {
		t := c.typExpr(x, ctx)
		if isInvalid(t) {
			u.Bad = true
			continue
		}
		vn, ok := t.(*Named)
		if !ok {
			c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
			u.Bad = true
			continue
		}
		if p := vn.origin().Obj.Pkg; p != c.pkg {
			if isUnion(t) {
				c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
				u.Bad = true
				continue
			}
			// A variant from another package needs a sealed interface over
			// a foreign type (spec Q7): 196b's.
			c.errorf(x, CodeOutside, "a union variant from another package (%s) is 196b's (packages); declare the record in this package", t)
			u.Bad = true
			continue
		}
		// The variant's kind comes from its declaration, so a variant that is
		// still resolving (a struct whose field is this union) is accepted.
		if decl := vn.origin().Obj.Decl; decl == nil {
			c.errorf(x, CodeType, "a union variant is a record declared in this package, not %s (types.md, Unions 1)", t)
			u.Bad = true
			continue
		} else if _, isStruct := decl.Spec.(*syntax.StructType); !isStruct {
			c.errorf(x, CodeType, "a union variant is a record declared in this package; %s is %s (types.md, Unions 1)", t, shapeNoun(decl.Spec))
			u.Bad = true
			continue
		}
		key := c.types.id(t)
		if seen[key] {
			c.errorf(x, CodeType, "variant %s is listed twice", t)
			continue
		}
		seen[key] = true
		u.Variants = append(u.Variants, t)
	}
	return u
}

// ---------------------------------------------------------------- type expressions

// typeCtx is the context of a type expression: the type parameters in scope
// and the rules for unbound one-letter names (rows-and-constraints §3).
type typeCtx struct {
	params   map[string]*TypeParam // bracket, receiver and implicit parameters
	implicit map[string]*TypeParam // implicit parameters found by the prescan
	method   bool                  // a method or interface method: no type parameters of its own (D6)
	decl     bool                  // a type declaration: bracket parameters only
	field    bool                  // a struct field: *T is not a field type (types.md, Borrows 1)
	param    bool                  // a parameter or receiver: *T allowed
	reported *bool                 // shared by the copies of one method context: one diagnostic per method
	scope    *Scope
}

// isParamName reports whether name has the shape of an implicit type
// parameter: one upper-case letter optionally followed by digits.
func isParamName(name string) bool {
	r, size := utf8.DecodeRuneInString(name)
	if !unicode.IsUpper(r) {
		return false
	}
	for _, d := range name[size:] {
		if d < '0' || d > '9' {
			return false
		}
	}
	return true
}

func (c *checker) lookupType(name string, ctx *typeCtx) Object {
	if ctx != nil {
		if tp, ok := ctx.params[name]; ok {
			return &TypeName{Name: name, Type: tp, Pkg: c.pkg}
		}
	}
	scope := c.pkg.Scope
	if ctx != nil && ctx.scope != nil {
		scope = ctx.scope
	}
	if o, at := scope.lookup(name); o != nil {
		// A refused import of a prelude name still binds (D-D3; lookup).
		if c.abovePackage(at) && c.cur != nil {
			if imp, ok := c.cur.scope.names[name]; ok {
				return imp
			}
		}
		return o
	}
	if c.cur != nil {
		if o, ok := c.cur.scope.names[name]; ok {
			return o
		}
	}
	if ctx != nil {
		if tp, ok := ctx.implicit[name]; ok {
			return &TypeName{Name: name, Type: tp, Pkg: c.pkg}
		}
	}
	return nil
}

// typExpr resolves a type expression. An invalid type is reported once and
// suppresses every diagnostic that would follow from it (D3).
func (c *checker) typExpr(x syntax.Expr, ctx *typeCtx) Type {
	t := c.typExpr0(x, ctx)
	if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
		c.errorf(x, CodeType, "generic type %s needs type arguments", n)
		return invalid
	}
	if tp, ok := t.(*TypeParam); ok && tp.conflict {
		return invalid
	}
	if tp, ok := t.(*TypeParam); ok && tp.Kind != KindType {
		c.errorf(x, CodeType, "%s is %s parameter, not a type", tp.Name, tp.Kind)
		return invalid
	}
	return t
}

func (c *checker) typExpr0(x syntax.Expr, ctx *typeCtx) Type {
	switch x := x.(type) {
	case *syntax.Ident:
		return c.typeName(x, ctx)
	case *syntax.SelectorExpr:
		return c.qualifiedType(x)
	case *syntax.ParenExpr:
		return c.typExpr0(x.X, ctx)
	case *syntax.IndexExpr:
		base := c.typExpr0(x.X, ctx)
		if isInvalid(base) {
			return invalid
		}
		n, ok := base.(*Named)
		if !ok || n.Orig != nil || len(n.TParams) == 0 {
			c.errorf(x, CodeType, "%s is not a generic type", base)
			return invalid
		}
		return c.instantiateExprs(n, x.Indices, x, ctx)
	case *syntax.ArrayType:
		if x.Len != nil {
			c.undecided(x, "an array type with a length")
			return invalid
		}
		return &Slice{Elem: c.typExpr(x.Elt, ctx)}
	case *syntax.MapType:
		k := c.typExpr(x.Key, ctx)
		v := c.typExpr(x.Value, ctx)
		if !isInvalid(k) && !comparable(k) {
			c.errorf(x.Key, CodeType, "map key type %s is not comparable", k)
			return invalid
		}
		return &Map{Key: k, Elem: v}
	case *syntax.StarExpr:
		if ctx == nil || !ctx.param {
			c.errorf(x, CodeType, "a borrow *T is only a parameter or receiver type (types.md, Borrows 1)")
			return invalid
		}
		inner := *ctx
		inner.param = false
		return &Pointer{Elem: c.typExpr(x.X, &inner)}
	case *syntax.FuncType:
		if x.Escaping.IsValid() {
			c.outside(x, "an escaping function type (196e, escaping)")
			return invalid
		}
		inner := typeCtx{}
		if ctx != nil {
			inner = *ctx
		}
		inner.field, inner.param = false, false
		sig := c.signature(x, &inner)
		// A written function type's parameter names declare its
		// parameters (Info.Defs), as an operation's do (defParams); a
		// function literal's parameter of type func(v T) bool needs it.
		c.defParams(x, sig)
		return sig
	case *syntax.StructType, *syntax.InterfaceType:
		// The decided parser builds a record, an operation type or a union
		// only as a type declaration's body (resolveNamed).
		c.errorf(x, CodeType, "a record, an operation type or a union is a declared type: write type T { … }")
		return invalid
	case *syntax.ErrorRow:
		c.errorf(x, CodeType, "an error row appears only after fails in a signature")
		return invalid
	case *syntax.TupleExpr:
		// A tuple type is structural (196t): two or more elements, which
		// the parser guarantees.
		inner := typeCtx{}
		if ctx != nil {
			inner = *ctx
		}
		inner.param = false
		tup := &Tuple{Types: make([]Type, len(x.Elts))}
		for i, e := range x.Elts {
			tup.Types[i] = c.typExpr(e, &inner)
			if isInvalid(tup.Types[i]) {
				return invalid
			}
		}
		return tup
	case *syntax.VariantSpec:
		return c.variantType(x, ctx)
	case *syntax.AsPattern:
		c.errorf(x, CodeType, "expected a type")
		return invalid
	case *syntax.BasicLit, *syntax.CompositeLit, *syntax.KeyValueExpr, *syntax.FuncLit, *syntax.SliceExpr,
		*syntax.TypeGuard, *syntax.CallExpr, *syntax.UnaryExpr, *syntax.BinaryExpr, *syntax.TryExpr,
		*syntax.CatchExpr, *syntax.WireLit, *syntax.MachineType, *syntax.InterpolatedString,
		*syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		c.errorf(x, CodeType, "expected a type")
		return invalid
	}
	c.undecided(x, "")
	return invalid
}

func (c *checker) typeName(x *syntax.Ident, ctx *typeCtx) Type {
	o := c.lookupType(x.Name, ctx)
	if o == nil {
		if ctx != nil && ctx.method && isParamName(x.Name) {
			if ctx.reported == nil || !*ctx.reported {
				if ctx.reported != nil {
					*ctx.reported = true
				}
				c.errorf(x, CodeType, "%s would be a type parameter of a method; methods take no type parameters of their own (D6; Go proposal 43651)", x.Name)
			}
			return invalid
		}
		c.errorf(x, CodeUnknown, "unknown type %s", x.Name)
		return invalid
	}
	c.recordUse(x, o)
	switch o := o.(type) {
	case *TypeName:
		if o == universeOnce.errorName {
			c.errorf(x, CodeType, "error is only the head of an error row or a constraint")
			return invalid
		}
		if n, ok := o.Type.(*Named); ok {
			c.resolveNamed(n)
		}
		return o.Type
	case *capBinding:
		return o.tn.Type
	case *PkgName:
		c.errorf(x, CodeType, "package %s is not a type", x.Name)
		return invalid
	}
	c.errorf(x, CodeType, "%s is not a type", x.Name)
	return invalid
}

func (c *checker) qualifiedType(x *syntax.SelectorExpr) Type {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		c.errorf(x, CodeType, "expected a type")
		return invalid
	}
	o := c.lookupType(id.Name, nil)
	pn, ok := o.(*PkgName)
	if !ok {
		if o == nil {
			c.errorf(id, CodeUnknown, "unknown package %s", id.Name)
		} else {
			c.errorf(x, CodeType, "%s is not a package", id.Name)
		}
		return invalid
	}
	pn.used = true
	c.recordUse(id, pn)
	if pn.broken {
		return invalid
	}
	m, ok := pn.Pkg.Scope.names[x.Sel.Name]
	if !ok || !isExported(x.Sel.Name) {
		c.errorf(x.Sel, CodeUnknown, "%s.%s is not an exported declaration of package %s", id.Name, x.Sel.Name, pn.Pkg.Path)
		return invalid
	}
	tn, ok := m.(*TypeName)
	if !ok {
		c.errorf(x, CodeType, "%s.%s is not a type", id.Name, x.Sel.Name)
		return invalid
	}
	return tn.Type
}

func (c *checker) recordUse(id *syntax.Ident, o Object) {
	if c.useGens != nil && c.curGen != 0 {
		c.useGens[id] = append(c.useGens[id], c.curGen)
	}
	if c.silent == 0 {
		c.pkg.Info.Uses[id] = o
	}
}

// constraint resolves a bracket constraint: any, comparable, an interface
// with methods (types.md, Generics 2).
func (c *checker) constraint(x syntax.Expr, ctx *typeCtx) Type {
	t := c.typExpr(x, ctx)
	if isInvalid(t) {
		return nil
	}
	it := asInterface(t)
	if it == nil {
		c.errorf(x, CodeType, "a constraint is an interface, not %s", t)
		return nil
	}
	if !it.Comparable && len(it.Methods) == 0 {
		return nil
	}
	return t
}

// instantiateExprs instantiates a generic type with written type arguments.
// A row-kinded parameter takes an error type, a capability or a row
// parameter, as a row of one element.
func (c *checker) instantiateExprs(n *Named, args []syntax.Expr, at syntax.Node, ctx *typeCtx) Type {
	c.resolveNamed(n)
	if len(args) != len(n.TParams) {
		c.errorf(at, CodeType, "%s takes %d type arguments, not %d", n.Obj.Name, len(n.TParams), len(args))
		return invalid
	}
	targs := make([]Type, len(args))
	for i, a := range args {
		tp := n.TParams[i]
		if tp.Kind != KindType {
			row := c.rowExpr([]syntax.Expr{a}, tp.Kind, ctx)
			if row == nil {
				return invalid
			}
			targs[i] = &RowArg{Row: row}
			continue
		}
		t := c.typExpr(a, ctx)
		if isInvalid(t) {
			return invalid
		}
		targs[i] = t
	}
	c.recordInstantiation(n.Obj.Name, n.TParams, targs, at)
	inst := c.instantiate(n, targs)
	for i, tp := range n.TParams {
		if tp.Kind == KindType && tp.Constraint != nil && n.state == 2 {
			cons := c.subst(tp.Constraint, paramMap(n.TParams, targs))
			if why := c.satisfies(targs[i], cons); why != "" {
				c.errorf(args[i], CodeType, "%s does not satisfy %s: %s", targs[i], cons, why)
				return invalid
			}
		}
	}
	return inst
}

// instantiate returns the instance of a generic named type. Its body is
// substituted lazily, so a recursive generic type terminates. The cache is
// keyed by the instance's interned id, (origin, argument ids), and looked up
// before anything is built (class E): an argument interned before costs one
// lookup, however deep it is.
func (c *checker) instantiate(orig *Named, targs []Type) *Named {
	key, infer := c.types.named(orig, targs)
	cache := c.instance
	if infer {
		// The id waits on an unbound row variable: the instance is cached
		// for the component's inference only (class F).
		cache = c.inferInstances
	}
	if prev, ok := cache[key]; ok {
		return prev
	}
	inst := &Named{Obj: orig.Obj, Orig: orig, TArgs: targs, state: 2, ctx: c}
	c.types.remember(inst, key, infer)
	cache[key] = inst
	c.instancesMade++
	return inst
}

func (n *Named) instanceUnder(inst *Named) Type {
	if n.under == nil || inst.ctx == nil {
		return invalid
	}
	m := map[*TypeParam]Type{}
	for i, tp := range n.TParams {
		if i < len(inst.TArgs) {
			m[tp] = inst.TArgs[i]
		}
	}
	return inst.ctx.subst(n.under, m)
}

// ---------------------------------------------------------------- rows in signatures

// rowExpr resolves the elements of an error row or a capability row.
func (c *checker) rowExpr(xs []syntax.Expr, kind Kind, ctx *typeCtx) *Row {
	row := newRow()
	ok := true
	for _, x := range xs {
		t := c.rowElem(x, kind, ctx)
		if t == nil {
			ok = false
			continue
		}
		row = row.with(c.types, t)
	}
	if !ok {
		return nil
	}
	return row
}

func (c *checker) rowElem(x syntax.Expr, kind Kind, ctx *typeCtx) Type {
	var t Type
	switch x := x.(type) {
	case *syntax.Ident:
		o := c.lookupType(x.Name, ctx)
		if o == nil {
			if ctx != nil && ctx.method && isParamName(x.Name) {
				if ctx.reported == nil || !*ctx.reported {
					if ctx.reported != nil {
						*ctx.reported = true
					}
					c.errorf(x, CodeType, "%s would be a row parameter of a method; methods take no type parameters of their own (D6)", x.Name)
				}
				return nil
			}
			c.errorf(x, CodeUnknown, "unknown %s %s", kindNoun(kind), x.Name)
			return nil
		}
		c.recordUse(x, o)
		switch o := o.(type) {
		case *TypeName:
			if n, ok := o.Type.(*Named); ok {
				c.resolveNamed(n)
			}
			t = o.Type
		case *capBinding:
			t = o.tn.Type
		default:
			c.errorf(x, CodeUnknown, "%s is not %s", x.Name, kindNoun(kind))
			return nil
		}
	default:
		t = c.typExpr0(x, ctx)
		if isInvalid(t) {
			return nil
		}
	}
	if tp, ok := t.(*TypeParam); ok {
		if tp.conflict {
			return nil
		}
		if tp.Kind != kind {
			c.errorf(x, CodeType, "%s is %s parameter, used here as %s", tp.Name, tp.Kind, kind)
			return nil
		}
		return tp
	}
	if ra, ok := t.(*RowArg); ok {
		return ra
	}
	switch kind {
	case KindErrors:
		if n, ok := t.(*Named); ok && n.Orig == nil && len(n.TParams) > 0 {
			c.errorf(x, CodeType, "generic type %s needs type arguments", n)
			return nil
		}
		if !isStructNamed(t) {
			c.errorf(x, CodeUnknown, "%s is not an error type: an error type is a named struct type (types.md, Error types 1)", t)
			return nil
		}
		n := t.(*Named)
		if _, seen := c.errorTypes[n.origin()]; !seen {
			c.errorTypes[n.origin()] = x.Pos()
		}
	case KindUses:
		if !isCapability(t) {
			c.errorf(x, CodeUnknown, "%s is not a capability: a capability is a named interface with methods (rows.md, Capability rows 3)", t)
			return nil
		}
	}
	return t
}

func kindNoun(k Kind) string {
	if k == KindErrors {
		return "error type"
	}
	if k == KindUses {
		return "capability"
	}
	return "type"
}

// ---------------------------------------------------------------- signatures

// walkTypePositions calls f for every name in a type position of x, with
// the kind its position gives (rows-and-constraints §3).
func walkTypePositions(x syntax.Node, k Kind, f func(*syntax.Ident, Kind)) {
	typeWalker{f: f}.walk(x, k)
}

// typeWalker visits the names in type positions with the kind of the
// position. slots, when set, gives the parameter kinds of a generic type
// named as the head of an index expression, so `Task[T, E]` puts E in a row
// position (D6; M6).
type typeWalker struct {
	f     func(*syntax.Ident, Kind)
	slots func(head syntax.Expr) []Kind
	// index, when set, may take over an index expression (returning true)
	// before the default walk.
	index func(ix *syntax.IndexExpr) bool
}

func (w typeWalker) walk(x syntax.Node, k Kind) {
	switch x := x.(type) {
	case nil:
	case *syntax.Ident:
		w.f(x, k)
	case *syntax.SelectorExpr:
	case *syntax.ParenExpr:
		w.walk(x.X, k)
	case *syntax.BinaryExpr:
		// A row argument written as a union, E | Expired.
		w.walk(x.X, k)
		w.walk(x.Y, k)
	case *syntax.IndexExpr:
		if w.index != nil && w.index(x) {
			return
		}
		w.walk(x.X, KindType)
		var kinds []Kind
		if w.slots != nil {
			kinds = w.slots(x.X)
		}
		for i, idx := range x.Indices {
			kind := KindType
			if i < len(kinds) {
				kind = kinds[i]
			}
			w.walk(idx, kind)
		}
	case *syntax.ArrayType:
		w.walk(x.Elt, KindType)
	case *syntax.MapType:
		w.walk(x.Key, KindType)
		w.walk(x.Value, KindType)
	case *syntax.StarExpr:
		w.walk(x.X, KindType)
	case *syntax.FuncType:
		w.fields(x.Params)
		w.fields(x.Results)
		if x.Needs != nil {
			for _, n := range x.Needs.Names {
				w.walk(n, KindUses)
			}
		}
	case *syntax.ErrorRow:
		for _, t := range x.Types {
			w.walk(t, KindErrors)
		}
	case *syntax.StructType:
		w.fields(x.Fields)
	case *syntax.InterfaceType:
		for _, m := range x.Methods {
			w.walk(m.Type, KindType)
		}
		for _, u := range x.Union {
			w.walk(u, KindType)
		}
	case *syntax.MachineType:
		w.walk(x.State, KindType)
		w.walk(x.Signal, KindType)
	}
}

func (w typeWalker) fields(l *syntax.FieldList) {
	if l == nil {
		return
	}
	for _, field := range l.List {
		w.walk(field.Type, KindType)
	}
}

// slotKinds returns the parameter kinds of the generic type a head names,
// or nil when the head is not a generic type or is still resolving.
func (c *checker) slotKinds(head syntax.Expr) []Kind {
	tn, n := c.namedHead(head)
	if n == nil {
		return nil
	}
	if ks, ok := c.kinds.kinds[tn]; ok {
		// A local declaration's kinds are solved before any resolves, so
		// a recursive head has them too (round-2 M4).
		return ks
	}
	if n.state == 0 {
		c.resolveNamed(n)
	}
	if n.state != 2 {
		return nil
	}
	kinds := make([]Kind, len(n.TParams))
	for i, tp := range n.TParams {
		kinds[i] = tp.Kind
	}
	return kinds
}

// prescanImplicit creates the implicit type parameters of a function
// signature: every otherwise unbound one-letter name, kinded by position. A
// name used in two kinds is S108 (D6).
func (c *checker) prescanImplicit(ft *syntax.FuncType, ctx *typeCtx) {
	type seen struct {
		kind Kind
		at   *syntax.Ident
	}
	found := map[string]*seen{}
	var order []string
	conflict := map[string]bool{}
	typeWalker{slots: c.slotKinds, f: func(id *syntax.Ident, k Kind) {
		if !isParamName(id.Name) || conflict[id.Name] {
			return
		}
		if _, bound := ctx.params[id.Name]; bound {
			return
		}
		if o, _ := c.pkg.Scope.lookup(id.Name); o != nil {
			return
		}
		if c.cur != nil {
			if _, ok := c.cur.scope.names[id.Name]; ok {
				return
			}
		}
		if s, ok := found[id.Name]; ok {
			if s.kind != k {
				conflict[id.Name] = true
				c.errorf(id, CodeType, "%s is used as %s and as %s; a parameter has one kind (types.md, Generics 1)", id.Name, s.kind, k)
			}
			return
		}
		found[id.Name] = &seen{kind: k, at: id}
		order = append(order, id.Name)
	}}.walk(ft, KindType)
	if ctx.implicit == nil {
		ctx.implicit = map[string]*TypeParam{}
	}
	for _, name := range order {
		if conflict[name] {
			continue
		}
		s := found[name]
		ctx.implicit[name] = &TypeParam{Name: name, Kind: s.kind, Pos: s.at.Pos(), Implicit: true, id: newID()}
	}
	for name := range conflict {
		// A conflicting name resolves to nothing further: no cascade.
		ctx.implicit[name] = &TypeParam{Name: name, Implicit: true, id: newID(), conflict: true}
	}
}

// signature resolves a function type. Parameters, results, the error row and
// the capability row.
func (c *checker) signature(ft *syntax.FuncType, ctx *typeCtx) *Signature {
	sig := &Signature{Errors: newRow(), Uses: newRow()}
	pctx := *ctx
	pctx.param, pctx.field = true, false
	if ft.Params != nil {
		for _, field := range ft.Params.List {
			var t Type
			if field.Type == nil {
				// A function literal's parameter written without a type
				// (grammar.md; inferring it is #214).
				c.errorf(field, CodeOutside, "a function literal parameter without a type is #214's (inferred parameter types); it is not checked yet")
				t = invalid
			} else {
				t = c.typExpr(field.Type, &pctx)
			}
			if len(field.Names) == 0 {
				sig.Params = append(sig.Params, &Var{Name: "_", Type: t, Pos: field.Pos(), param: true})
			}
			for _, name := range field.Names {
				sig.Params = append(sig.Params, &Var{Name: name.Name, Type: t, Pos: name.Pos(), param: true})
				c.paramIdents[name.Pos()] = name
			}
		}
	}
	rctx := *ctx
	rctx.param, rctx.field = false, false
	if ft.Results != nil {
		for _, field := range ft.Results.List {
			if er, ok := field.Type.(*syntax.ErrorRow); ok {
				sig.Fallible = true
				if row := c.rowExpr(er.Types, KindErrors, &rctx); row != nil {
					sig.Errors = row
				}
				continue
			}
			t := c.typExpr(field.Type, &rctx)
			for range max(1, len(field.Names)) {
				sig.Results = append(sig.Results, t)
			}
		}
	}
	if ft.Needs != nil {
		if row := c.rowExpr(ft.Needs.Names, KindUses, &rctx); row != nil {
			sig.Uses = row
		}
	}
	return sig
}

// funcSig resolves a function's or method's signature, its type parameters
// (bracket-listed first, then implicit ones) and its receiver.
func (c *checker) funcSig(f *Func) *Signature {
	if f.state == 2 || f.state == 1 {
		if f.Sig == nil {
			f.Sig = &Signature{Errors: newRow(), Uses: newRow()}
		}
		return f.Sig
	}
	f.state = 1
	saved := c.cur
	c.cur = f.file
	defer func() { c.cur = saved }()
	d := f.Decl
	ctx := &typeCtx{params: map[string]*TypeParam{}}
	var tparams []*TypeParam
	// The bracket parameters are declared before any constraint resolves,
	// so the instantiations in the constraints and the signature are
	// attributed to them (round-4 M1).
	type pending struct {
		tp *TypeParam
		x  syntax.Expr
	}
	var constraints []pending
	var unconstrained []*TypeParam
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				c.preludeName(name) // D-D3; the parameter still binds
				tp := &TypeParam{Name: name.Name, Pos: name.Pos(), id: newID()}
				c.def(name, tp)
				switch {
				case field.Type != nil && isErrorName(field.Type):
					tp.Kind = KindErrors
				case field.Type != nil:
					constraints = append(constraints, pending{tp, field.Type})
				default:
					unconstrained = append(unconstrained, tp)
				}
				if _, dup := ctx.params[name.Name]; dup {
					c.errorf(name, CodeType, "type parameter %s is declared twice", name.Name)
				}
				ctx.params[name.Name] = tp
				tparams = append(tparams, tp)
			}
		}
	}
	// A signature instantiates generic types with the declaration's
	// parameters, as its body does:
	// they own those instantiations, so a growing one is an instantiation
	// cycle before rows are inferred (round-4 M1; go/types mono.go records
	// signature instances too).
	owner := append([]*TypeParam(nil), tparams...)
	restore := c.monoOwner(owner)
	for _, p := range constraints {
		if c.uncheckedConstraint(p.x, ctx) {
			p.tp.unchecked = true
			continue
		}
		p.tp.Constraint = c.constraint(p.x, ctx)
	}
	c.kindBracketParams(d.Type, ctx, unconstrained)
	c.monoLinkSelf(f, ctx)
	nbracket := len(tparams)
	c.prescanImplicit(d.Type, ctx)
	implicit := implicitOrder(d.Type, ctx)
	for _, name := range implicit {
		owner = append(owner, ctx.implicit[name])
	}
	c.mono.owner = owner
	sig := c.signature(d.Type, ctx)
	restore()
	sig.NBracket = nbracket
	for _, name := range implicit {
		tparams = append(tparams, ctx.implicit[name])
	}
	sig.TParams = tparams
	f.Sig = sig
	f.state = 2
	if d.Body == nil {
		// A bodiless (extern) function's parameters are declared here; a
		// body's are declared when it is checked.
		c.defParams(d.Type, sig)
	}
	c.inferFlags(f)
	// An inferred row is a row variable (class F): the signature never
	// changes, inference solves the variable.
	if f.inferErrors {
		f.errVar = &RowVar{Fn: f, Kind: KindErrors}
		sig.Errors = c.types.row(f.errVar)
	}
	if f.inferUses {
		f.useVar = &RowVar{Fn: f, Kind: KindUses}
		sig.Uses = c.types.row(f.useVar)
	}
	return sig
}

// kindBracketParams gives each unconstrained bracket parameter of a
// function the kind of its positions in the signature: after fails an
// error row, after needs a capability row, elsewhere a type (grammar.md,
// Declarations 3; 196g as amended). It is the rule solveDeclKinds applies
// to type declarations and prescanImplicit to implicit parameters; a
// parameter used in two kinds is S108, as there.
func (c *checker) kindBracketParams(ft *syntax.FuncType, ctx *typeCtx, unconstrained []*TypeParam) {
	free := map[*TypeParam]bool{}
	for _, tp := range unconstrained {
		free[tp] = true
	}
	if len(free) == 0 || ft == nil {
		return
	}
	seen := map[*TypeParam]Kind{}
	// A parameter passed to a declaration's parameter whose kinds conflict
	// takes no kind from it: that conflict is reported at the declaration,
	// once (a 196m method's first parameter, `func Run[E](j Job[E])`).
	conflicted := func(ix *syntax.IndexExpr) bool {
		tn, _ := c.namedHead(ix.X)
		if tn == nil || len(c.kinds.conflicts[tn]) == 0 || tn.Decl == nil || tn.Decl.TypeParams == nil {
			return false
		}
		i := 0
		for _, fld := range tn.Decl.TypeParams.List {
			for _, n := range fld.Names {
				if i < len(ix.Indices) && c.kinds.conflicts[tn][n.Name] {
					if id, ok := unparen(ix.Indices[i]).(*syntax.Ident); ok {
						if tp := ctx.params[id.Name]; tp != nil && free[tp] {
							tp.conflict = true
						}
					}
				}
				i++
			}
		}
		return false
	}
	typeWalker{slots: c.slotKinds, index: conflicted, f: func(id *syntax.Ident, k Kind) {
		tp := ctx.params[id.Name]
		if tp == nil || !free[tp] || tp.conflict {
			return
		}
		if prev, ok := seen[tp]; ok {
			if prev != k {
				tp.conflict = true
				c.errorf(id, CodeType, "%s is used as %s and as %s; a parameter has one kind (types.md, Generics 1)", id.Name, prev, k)
			}
			return
		}
		seen[tp] = k
		tp.Kind = k
	}}.walk(ft, KindType)
}

// implicitOrder lists the implicit parameters in order of first occurrence.
func implicitOrder(ft *syntax.FuncType, ctx *typeCtx) []string {
	var out []string
	seen := map[string]bool{}
	walkTypePositions(ft, KindType, func(id *syntax.Ident, _ Kind) {
		if tp, ok := ctx.implicit[id.Name]; ok && !seen[id.Name] && !tp.conflict {
			seen[id.Name] = true
			out = append(out, id.Name)
		}
	})
	return out
}

// collectSelfFuncs resolves x.f(…) to package functions (196m; round-6
// ruling B1). A package function whose first parameter's type is T or *T,
// for a non-interface type T declared in this package, is what x.f(…)
// calls on a value of T (rule 1) and what makes T satisfy an operation
// type (rule 2). It stays a function, with a function's rows, and a plain
// call f(x, …) still calls it. A field of T named f wins: x.f is the field
// and the function is reached only by a plain call. main is never reached
// this way.
//
// Several package functions may share a name when each one's first
// parameter is a distinct such type (rule 3); they are called only as
// x.f(…). Any other repetition of a name is the redeclaration it always
// was, reported at each later function.
func (c *checker) collectSelfFuncs() {
	for _, f := range c.funcs {
		if f.Name != "main" {
			c.cur = f.file
			f.owner, f.selfArgs, f.Ptr = c.selfOwner(f)
		}
	}
	for _, f := range c.pkg.Funcs {
		if len(f.same) == 0 {
			continue
		}
		group := append([]*Func{f}, f.same...)
		// A first parameter that names a duplicated declaration counts as
		// a distinct local type: whether the kept declaration is one
		// depends on which comes first, so the group's verdict must not
		// read it (a follow-on of the duplicate; stage D1 round 2 review
		// T1). Two members naming it still collide, whichever is kept. The
		// restrictions that do not read the declaration (main is never a
		// member; the type arguments are distinct type parameters of the
		// function) still apply first (round 3 review M3).
		owners := map[any]bool{}
		valid := true
		for _, g := range group {
			var key any
			if g.owner != nil {
				key = g.owner
			}
			if id, args, _ := selfOwnerName(g.Decl); id != nil && c.dupNames[id.Name] && g.Name != "main" {
				c.cur = g.file
				if c.selfArgsOK(g.Decl, args) {
					key = id.Name
				}
			}
			if key == nil || owners[key] {
				valid = false
				break
			}
			owners[key] = true
		}
		if valid {
			for _, g := range group {
				g.overloaded = true
				c.def(g.Decl.Name, g) // a later same-named function is in no scope
			}
			c.pkg.Funcs = append(c.pkg.Funcs, f.same...)
			continue
		}
		for _, g := range f.same {
			c.cur = g.file
			c.errorf(g.Decl.Name, CodeType, "%s is already declared in this package", g.Name)
			g.owner, g.selfArgs, g.Ptr = nil, nil, false
		}
	}
	for _, f := range c.funcs {
		if f.owner != nil {
			f.owner.addMethod(f)
		}
	}
	if c.pkg.Path == std.PreludePath {
		for _, f := range c.funcs {
			c.cur = f.file
			if key, args, ok := c.collOwner(f); ok {
				if c.pkg.collFuncs == nil {
					c.pkg.collFuncs = map[collKey]*Func{}
				}
				f.selfArgs = args
				c.pkg.collFuncs[key] = f
			}
		}
	}
}

// collOwner reports a prelude function x.f(…) reaches from a slice or a Map
// (D-D6, D-D8): its first parameter is written []T or Map[K, V], with T, K
// and V distinct type parameters of f, which the receiver's element, key
// and value types bind. The arguments are returned in that order.
func (c *checker) collOwner(f *Func) (collKey, []syntax.Expr, bool) {
	d := f.Decl
	if f.Name == "main" || d.Type == nil || d.Type.Params == nil || len(d.Type.Params.List) == 0 {
		return collKey{}, nil, false
	}
	var args []syntax.Expr
	key := collKey{name: f.Name}
	switch t := d.Type.Params.List[0].Type.(type) {
	case *syntax.ArrayType:
		if t.Len != nil {
			return collKey{}, nil, false
		}
		args = []syntax.Expr{t.Elt}
	case *syntax.MapType:
		args, key.isMap = []syntax.Expr{t.Key, t.Value}, true
	default:
		return collKey{}, nil, false
	}
	if d.TypeParams == nil || !c.selfArgsOK(d, args) {
		return collKey{}, nil, false
	}
	return key, args, true
}

// selfOwner returns the type x.f(…) reaches f from, or nil: the type of
// f's first parameter, written T, *T or, for a generic T, T[A, B] whose
// arguments are distinct type parameters of f, which a call on an instance
// binds.
func (c *checker) selfOwner(f *Func) (*Named, []syntax.Expr, bool) {
	d := f.Decl
	id, args, ptr := selfOwnerName(d)
	if id == nil {
		return nil, nil, false
	}
	tn, ok := c.pkg.Scope.names[id.Name].(*TypeName)
	if !ok {
		return nil, nil, false
	}
	n, _ := tn.Type.(*Named)
	if n == nil || n.Obj.Pkg != c.pkg || n.method(f.Name) != nil {
		return nil, nil, false
	}
	if _, isIface := n.underlying().(*Interface); isIface {
		return nil, nil, false
	}
	// A field of the same name does not stop f: x.f selects the field
	// first, and f stays in the type's operation set, so it satisfies an
	// interface method f (types.md, Structs 1; round 7 review O2).
	if len(args) != len(n.TParams) || !c.selfArgsOK(d, args) {
		return nil, nil, false
	}
	return n, args, ptr
}

// selfOwnerName returns the type name d's first parameter writes, T, *T or
// T[A, B], with the type arguments and whether it is a pointer; nil when
// the first parameter writes another type or d has none.
func selfOwnerName(d *syntax.FuncDecl) (*syntax.Ident, []syntax.Expr, bool) {
	if d.Type == nil || d.Type.Params == nil || len(d.Type.Params.List) == 0 {
		return nil, nil, false
	}
	rt := d.Type.Params.List[0].Type
	ptr := false
	if s, ok := rt.(*syntax.StarExpr); ok {
		rt, ptr = s.X, true
	}
	var args []syntax.Expr
	if ix, ok := rt.(*syntax.IndexExpr); ok {
		rt, args = ix.X, ix.Indices
	}
	id, ok := rt.(*syntax.Ident)
	if !ok {
		return nil, nil, false
	}
	// A name the function's own bracket list declares is its type
	// parameter, which shadows a package type of that name (round 6
	// review m1): the function is an ordinary generic function.
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				if name.Name == id.Name {
					return nil, nil, false
				}
			}
		}
	}
	return id, args, ptr
}

// selfArgsOK reports whether every type argument of a first parameter's
// type is a distinct type parameter of the function: one its bracket list
// declares, or an implicit one (a parameter name no type declares).
func (c *checker) selfArgsOK(d *syntax.FuncDecl, args []syntax.Expr) bool {
	declared := map[string]bool{}
	if d.TypeParams != nil {
		for _, field := range d.TypeParams.List {
			for _, name := range field.Names {
				declared[name.Name] = true
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range args {
		id, ok := a.(*syntax.Ident)
		if !ok || seen[id.Name] {
			return false
		}
		if !declared[id.Name] && !(isParamName(id.Name) && c.typeObject(id.Name, c.cur) == nil) {
			return false
		}
		seen[id.Name] = true
	}
	return true
}

// inferFlags decides which rows of a function are inferred from its body
// (rows.md, Capability rows 6; rows-and-constraints §7 amendment 1): an
// unexported function with a body, when the row is unwritten. main, LawX,
// exported functions, methods, bodiless declarations and the step of an
// exported machine (the export guard) spell their rows.
func (c *checker) inferFlags(f *Func) {
	d := f.Decl
	if d.Body == nil || isExported(f.Name) || f.Name == "main" || c.guarded[f] {
		return
	}
	hasErrRow := false
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			if _, ok := r.Type.(*syntax.ErrorRow); ok {
				hasErrRow = true
			}
		}
	}
	f.inferErrors = !hasErrRow
	f.inferUses = d.Type.Needs == nil
}
