package lower

import (
	"fmt"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Declarations. Every declaration of the user package is lowered, used or
// not (spec, acceptance 6), except a bodiless or extern function with no Go
// body, which is refused where it is used. A declaration of the prelude or a
// standard-library package is lowered when the program reaches it.
//
// Representation (spec D2): a record is a named Go struct; an operation
// type is a Go interface; a union is a sealed Go interface with an
// unexported marker method on each variant (types.md, Unions 6); a type's
// row parameters are erased, its type parameters are Go type parameters. A
// 196m function on a record declared in its package is a Go method on the
// record when Go can express it (types.md, Methods 3), else a function. A
// function's capability row is an environment pointer, its error row a Go
// error result (spec D3).

type typeKind int

const (
	kindRecord typeKind = iota + 1
	kindOps
	kindUnion
	// kindDefined is `type N T` (D-B7): Go's `type N T`, with T's fields
	// when T is a record.
	kindDefined
	// kindMachine is `type X machine[S, E] { … }` (spec D5): a plan value
	// (machine.go).
	kindMachine
)

// typeDecl is a lowered type declaration.
type typeDecl struct {
	tn        *check.TypeName
	named     *check.Named // the origin
	name      string
	kind      typeKind
	tparams   []*check.TypeParam // the type-kinded parameters, the Go ones
	fields    []*check.Field
	fieldName map[string]string
	ops       []*check.Method
	defined   check.Type // kindDefined: T, the type N is defined over
	machine   *machineDecl
	variants  []*check.TypeName
	marker    string
	errorType bool
	library   bool
	emitted   bool
	queued    bool // a library type queued for emission
	// refused is the refusal that ended the reading of the type's
	// structure; every later use raises it again, so a half-read type is
	// never used, even when the first reading ran under quiet.
	refused *refusal
}

// funcDecl is a lowered function declaration.
type funcDecl struct {
	fn       *check.Func
	name     string // the Go function name, or the method name
	method   *typeDecl
	ptr      bool // a method on *T
	fallible bool // returns an error
	extern   string
	library  bool
	emitted  bool
	// lowered is set when the Go body has been written, and readsEnv then
	// says whether that body names its environment parameter: the fact a
	// machine plan's Step reads to decide whether it takes one (machine.go).
	lowered  bool
	readsEnv bool
}

// declareNames gives every package-level declaration of the user package its
// Go name before anything else is named, in source order, so a source name
// keeps its spelling unless Go reserves it.
func (l *lowerer) declareNames() {
	// Package.Types includes the synthetic declarations of inline variants,
	// immediately after their union. Merge that order with the source's
	// other declarations before reserving any generated names.
	nextType := 0
	for _, d := range l.file.Decls {
		for nextType < len(l.pkg.Types) && l.pkg.Types[nextType].Pos < d.End() {
			l.typeName(l.pkg.Types[nextType])
			nextType++
		}
		l.guard(func() { l.declareName(d) })
	}
}

func (l *lowerer) declareName(d syntax.Decl) {
	{
		switch d := d.(type) {
		case *syntax.TypeDecl:
			if tn, ok := l.info.Defs[d.Name].(*check.TypeName); ok {
				l.typeName(tn)
			}
		case *syntax.FuncDecl:
			if f, ok := l.info.Defs[d.Name].(*check.Func); ok {
				l.funcDecl(f)
			}
		case *syntax.VarDecl:
			for _, spec := range d.Specs {
				for _, id := range spec.Names {
					if v, ok := l.info.Defs[id].(*check.Var); ok {
						l.vars[v] = l.pkgName(pkgKey{"let", v}, v.Name)
					}
				}
				if spec.Pattern != nil {
					for _, v := range patternVars(l.info.Patterns[spec.Pattern], nil) {
						l.vars[v] = l.pkgName(pkgKey{"let", v}, v.Name)
					}
				}
				if len(spec.Values) == 1 && (spec.Pattern != nil || len(spec.Names) > 1) {
					l.pkgName(pkgKey{"letinit", spec}, "ergoLet")
				}
			}
		case *syntax.ContextDecl:
			if w, ok := l.info.Defs[d.Name].(*check.Context); ok {
				// One builder for each distinct set of providers a with of
				// this context acquires, in the withs' source order.
				for _, with := range l.withsOf(w) {
					l.pkgName(pkgKey{"context", acquiredSet(with)}, "build"+w.Name)
				}
			}
		}
	}
}

// withsOf are the with operands of the named context w that this build
// lowers, in source order (each operand of a with is a fact, With.Next).
func (l *lowerer) withsOf(w *check.Context) []*check.With {
	var xs []*syntax.CallExpr
	for x := range l.info.Withs {
		if l.readsFact(l.pkg, x) {
			xs = append(xs, x)
		}
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].Pos() < xs[j].Pos() })
	var out []*check.With
	for _, x := range xs {
		for with := l.info.Withs[x]; with != nil; with = with.Next {
			if len(with.Contexts) > 0 && with.Contexts[0] == w {
				out = append(out, with)
			}
		}
	}
	return out
}

// decl lowers one top-level declaration of the user package.
func (l *lowerer) decl(d syntax.Decl) {
	switch d := d.(type) {
	case *syntax.ImportDecl:
	case *syntax.TypeDecl:
		tn, ok := l.info.Defs[d.Name].(*check.TypeName)
		if !ok {
			ice(d.Name, "the type %s has no Defs entry", d.Name.Name)
		}
		l.emitType(l.typeDecl(tn, d.Name))
	case *syntax.FuncDecl:
		f, ok := l.info.Defs[d.Name].(*check.Func)
		if !ok {
			ice(d.Name, "the function %s has no Defs entry", d.Name.Name)
		}
		fd := l.funcDecl(f)
		if d.Body == nil {
			return // an extern or bodiless declaration: lowered where it is used
		}
		l.emitFunc(fd)
	case *syntax.VarDecl:
		l.packageLet(d)
	case *syntax.ContextDecl:
		w, ok := l.info.Defs[d.Name].(*check.Context)
		if !ok {
			ice(d.Name, "the context %s has no Defs entry", d.Name.Name)
		}
		l.context(w, d.Name)
	case *syntax.TestDecl:
		l.testDecl(d) // the test build only (tests.go, rule 1)
	case *syntax.LawDecl:
		// A top-level law is in neither build (types.md, Laws 11): its facts
		// are OwnerLaw's, and #162 runs laws.
	default:
		refuse(d, "this declaration is not lowered yet")
	}
}

// typeName is a type declaration's Go name in the package scope.
func (l *lowerer) typeName(tn *check.TypeName) string {
	if td := l.types[tn]; td != nil {
		return td.name
	}
	td := &typeDecl{tn: tn, name: l.pkgName(pkgKey{"type", tn}, tn.Name), library: tn.Pkg != l.pkg}
	l.types[tn] = td
	return td.name
}

// typeDecl is the lowered declaration of tn, its structure read from the
// declaring package's Info: a record's fields and an operation type's
// operations are Defs of its declaring names, a union's variants its
// checked underlying type. It queues nothing (useType). A refusal while it
// reads the structure is kept on the declaration and raised at every later
// use, at its own site: the naming pass reads types under quiet, and the
// lowering must still report that refusal, not a name the pass missed.
func (l *lowerer) typeDecl(tn *check.TypeName, site syntax.Node) *typeDecl {
	l.typeName(tn)
	td := l.types[tn]
	if td.refused != nil {
		panic(*td.refused)
	}
	if td.kind != 0 {
		return td
	}
	defer func() {
		if r := recover(); r != nil {
			if ref, ok := r.(refusal); ok {
				td.refused = &ref
			}
			panic(r)
		}
	}()
	named, ok := tn.Type.(*check.Named)
	if !ok || tn.Decl == nil || tn.Pkg == nil || tn.Pkg.Info == nil {
		refuse(site, "the type %s is not a declared record, operation type or union, which lowering supports so far", tn.Name)
	}
	if named.Orig != nil {
		named = named.Orig
	}
	td.named = named
	for _, tp := range named.TParams {
		if tp.Kind == check.KindType {
			td.tparams = append(td.tparams, tp)
		}
	}
	info := tn.Pkg.Info
	switch spec := tn.Decl.Spec.(type) {
	case *syntax.StructType:
		td.kind = kindRecord
		td.fieldName = map[string]string{}
		td.errorType = l.errorType(named, l.buildView())
		// A defined error type over this record keeps its Go field names,
		// so they avoid its Error method too.
		avoidError := td.errorType || l.definedErrorOver(named)
		taken := map[string]bool{}
		for _, f := range spec.Fields.List {
			for _, id := range f.Names {
				field, ok := info.Defs[id].(*check.Field)
				if !ok {
					ice(id, "the field %s of %s has no Defs entry", id.Name, tn.Name)
				}
				goName := memberName(field.Name)
				if avoidError && goName == "Error" {
					goName = "Error_"
				}
				if taken[goName] {
					refuse(id, "the field %s of %s and another field have the same Go name %s", field.Name, tn.Name, goName)
				}
				taken[goName] = true
				td.fields = append(td.fields, field)
				td.fieldName[field.Name] = goName
			}
		}
	case *syntax.InterfaceType:
		if len(spec.Union) > 0 {
			td.kind = kindUnion
			union, ok := named.Underlying().(*check.Union)
			if !ok {
				ice(site, "the union %s has no checked variants", tn.Name)
			}
			for _, v := range union.Variants {
				variant, ok := v.(*check.Named)
				if !ok {
					ice(site, "the variant %s of %s is not named", v, tn.Name)
				}
				td.variants = append(td.variants, variant.Obj)
			}
			break
		}
		td.kind = kindOps
		taken := map[string]bool{}
		for _, m := range spec.Methods {
			for _, id := range m.Names {
				op, ok := info.Defs[id].(*check.Method)
				if !ok {
					ice(id, "the operation %s of %s has no Defs entry", id.Name, tn.Name)
				}
				if taken[memberName(op.Name)] {
					refuse(id, "two operations of %s have the same Go name %s", tn.Name, memberName(op.Name))
				}
				taken[memberName(op.Name)] = true
				td.ops = append(td.ops, op)
			}
		}
	case *syntax.MachineType:
		td.kind, td.machine = kindMachine, l.machineOf(tn, named, spec, site)
	default:
		def := named.Defined()
		if def == nil {
			ice(site, "the type %s has no shape the lowering knows", tn.Name)
		}
		td.kind, td.defined = kindDefined, def
		if base, ok := def.(*check.Named); ok {
			// Go's `type N T` keeps T's fields, by their Go names.
			if bd := l.typeDecl(base.Obj, site); bd.fieldName != nil {
				td.fields, td.fieldName = bd.fields, bd.fieldName
			}
		}
		// A defined type over a record, named in an error row, is an error
		// type of its own: it gets its own Error method (types.md, Error
		// types 1).
		td.errorType = l.errorType(named, l.buildView())
	}
	return td
}

// definedErrorOver reports whether an error type of the program is defined
// over named, directly or through other defined types.
func (l *lowerer) definedErrorOver(named *check.Named) bool {
	for _, pkg := range []*check.Package{named.Obj.Pkg, l.pkg} {
		if pkg == nil {
			continue
		}
		for _, n := range l.errorTypesRead(pkg) {
			for t := n.Defined(); t != nil; {
				d, ok := t.(*check.Named)
				if !ok {
					break
				}
				if d.Orig != nil {
					d = d.Orig
				}
				if d == named {
					return true
				}
				t = d.Defined()
			}
		}
	}
	return false
}

// typeParams writes a type parameter list and returns the scope that names
// the parameters.
func (l *lowerer) typeParams(params []*check.TypeParam, scope *namer, site syntax.Node) (string, map[*check.TypeParam]string) {
	names := map[*check.TypeParam]string{}
	if len(params) == 0 {
		return "", names
	}
	parts := make([]string, len(params))
	for i, tp := range params {
		names[tp] = scope.alloc(tp.Name)
		parts[i] = names[tp] + " " + l.constraint(tp, site)
	}
	return "[" + strings.Join(parts, ", ") + "]", names
}

// constraintOf is the interface a type parameter's constraint names: a
// predeclared constraint's, or an operation type's underlying one. It is
// nil for an unconstrained parameter.
func constraintOf(tp *check.TypeParam) *check.Interface {
	switch c := tp.Constraint.(type) {
	case *check.Interface:
		return c
	case *check.Named:
		it, _ := c.Underlying().(*check.Interface)
		return it
	}
	return nil
}

// constraint is the Go constraint of the type parameter tp (D-E1, C12):
// comparable is Go's comparable, ordered and number are generated type-set
// interfaces (typeSetDecl), and no constraint is any. A method set is any
// too: Go type arguments lack Ergo's operations (196m functions are not Go
// methods of int or of another package's type), so a function passes them
// as a witness value (witness.go; stage E1 orchestrator answer 2), and a
// type declaration's fields need none.
func (l *lowerer) constraint(tp *check.TypeParam, site syntax.Node) string {
	if tp.Constraint == nil {
		return "any"
	}
	it := constraintOf(tp)
	if it == nil {
		ice(site, "the constraint %s of %s is not an interface", tp.Constraint, tp.Name)
	}
	switch it.Set {
	case check.SetComparable:
		return "comparable"
	case check.SetOrdered, check.SetNumber:
		return l.typeSetDecl(it.Set)
	}
	return "any"
}

// sameGoConstraint reports two type parameters whose Go constraints are
// the same and need no witness: a Go method takes its receiver type's
// parameters with the type declaration's constraints.
func sameGoConstraint(a, b *check.TypeParam) bool {
	setOf := func(tp *check.TypeParam) (check.TypeSet, bool) {
		it := constraintOf(tp)
		if it == nil {
			return check.SetAll, false
		}
		return it.Set, len(it.Methods) > 0
	}
	as, am := setOf(a)
	bs, bm := setOf(b)
	return as == bs && !am && !bm
}

// typeSetDecl is the Go name of the interface that lowers the predeclared
// constraint s, ordered or number, emitted once. Its terms are the Go
// types Ergo's set holds and the types defined over them (types.md,
// Generics 2): duration is time.Duration, an int64 underneath. A Go type
// set, never any, so Go admits the operators the checker admitted (<, +,
// -, * and /) and stencils by GC shape as it does for cmp.Ordered.
func (l *lowerer) typeSetDecl(s check.TypeSet) string {
	if name, ok := l.sets[s]; ok {
		return name
	}
	name := l.pkgName(pkgKey{"constraint", s}, "ergo"+strings.ToUpper(s.String()[:1])+s.String()[1:])
	l.sets[s] = name
	terms := "~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr | ~float32 | ~float64"
	what := "the integer and float types"
	if s == check.SetOrdered {
		terms += " | ~string"
		what = "the integer, float, string and duration types"
	}
	l.gen.WriteString("// " + name + " is Ergo's " + s.String() + ": " + what + " and the types defined over them.\n")
	l.gen.WriteString("type " + name + " interface {\n\t" + terms + "\n}\n\n")
	return name
}

// useType returns td after queueing a library declaration's emission: the
// lowering calls it wherever it spells the type's Go name, so a library type
// is emitted when, and only when, the program's Go names it. typeDecl alone
// reads structure, which the naming pass needs without emitting anything.
func (l *lowerer) useType(td *typeDecl) *typeDecl {
	if td.library && !td.queued {
		td.queued = true
		l.pending = append(l.pending, func() { l.emitType(td) })
	}
	return td
}

// emitType writes a type declaration once.
func (l *lowerer) emitType(td *typeDecl) {
	if td.emitted {
		return
	}
	td.emitted = true
	b := &l.user
	if td.library {
		b = &l.gen
	} else {
		b.WriteString(l.directive(td.tn.Decl.Pos()))
	}
	site := syntax.Node(td.tn.Decl.Name)
	tparams, names := l.typeParams(td.tparams, newNamer(l.names), site)
	tc := &typeCtx{l: l, params: names, site: site}
	switch td.kind {
	case kindRecord:
		b.WriteString("type " + td.name + tparams + " struct {\n")
		for _, f := range td.fields {
			b.WriteString("\t" + td.fieldName[f.Name] + " " + tc.goType(f.Type) + "\n")
		}
		b.WriteString("}\n\n")
		if td.errorType {
			l.errorMethod(b, td)
		}
	case kindOps:
		b.WriteString("type " + td.name + tparams + " interface {\n")
		for _, op := range td.ops {
			b.WriteString("\t" + memberName(op.Name) + tc.methodSig(op.Sig) + "\n")
		}
		b.WriteString("}\n\n")
	case kindUnion:
		l.unionMarker(td)
		b.WriteString("type " + td.name + tparams + " interface{ " + td.marker + "() }\n\n")
		// Two instances of one generic record, V[A] | V[B], share the
		// generic type's one marker method: Go declares methods on V, not
		// on its instances. The checker admits only the listed instances.
		marked := map[*typeDecl]bool{}
		for _, v := range td.variants {
			vd := l.useType(l.typeDecl(v, site))
			if vd.kind != kindRecord {
				refuse(site, "the variant %s of %s is not a record", v.Name, td.tn.Name)
			}
			l.emitType(vd)
			if marked[vd] {
				continue
			}
			marked[vd] = true
			b.WriteString("func (" + l.recvType(vd) + ") " + td.marker + "() {}\n")
		}
		b.WriteString("\n")
	case kindDefined:
		b.WriteString("type " + td.name + tparams + " " + tc.goType(td.defined) + "\n\n")
		if td.errorType {
			l.errorMethod(b, td)
		}
	case kindMachine:
		l.machinePlan(b, td)
	}
}

// recvType is a method receiver's type with blank type parameters.
func (l *lowerer) recvType(td *typeDecl) string {
	if len(td.tparams) == 0 {
		return td.name
	}
	return td.name + "[" + strings.TrimSuffix(strings.Repeat("_, ", len(td.tparams)), ", ") + "]"
}

// unionMarker names a union's marker method: free among the members of
// every variant, so no field or method of a variant is captured.
func (l *lowerer) unionMarker(td *typeDecl) {
	if td.marker != "" {
		return
	}
	members := map[string]bool{"Error": true}
	for _, v := range td.variants {
		vd := l.typeDecl(v, td.tn.Decl.Name)
		for _, name := range vd.fieldName {
			members[name] = true
		}
		for _, f := range l.methodsOf(v) {
			members[l.funcDecl(f).name] = true
		}
	}
	base := "is" + td.name
	td.marker = base
	for i := 1; members[td.marker]; i++ {
		td.marker = fmt.Sprintf("%s_%d", base, i)
	}
}

// methodsOf lists the functions of tn's package that lower as Go methods
// on tn.
func (l *lowerer) methodsOf(tn *check.TypeName) []*check.Func {
	byType, ok := l.methods[tn.Pkg]
	if !ok {
		byType = map[*check.TypeName][]*check.Func{}
		for _, f := range tn.Pkg.Funcs {
			if fd := l.funcDecl(f); fd.method != nil {
				byType[fd.method.tn] = append(byType[fd.method.tn], f)
			}
		}
		l.methods[tn.Pkg] = byType
	}
	return byType[tn]
}

// funcDecl decides how f lowers: as a Go method on the record its first
// parameter names (types.md, Methods 3), or as a Go function with a name in
// the package scope.
func (l *lowerer) funcDecl(f *check.Func) *funcDecl {
	if fd := l.funcs[f]; fd != nil {
		return fd
	}
	fd := &funcDecl{fn: f, library: f.Pkg != l.pkg}
	l.funcs[f] = fd
	fd.fallible = len(rowElems(f.Sig.Errors)) > 0
	if f.Decl != nil && f.Decl.Extern != nil && f.Decl.Extern.Target != nil {
		fd.extern = strings.Trim(f.Decl.Extern.Target.Value, "\"`")
	}
	if td, ptr := l.methodReceiver(f); td != nil {
		fd.method, fd.ptr, fd.name = td, ptr, memberName(f.Name)
		return fd
	}
	base := f.Name
	if f.Name == "main" && !fd.library {
		base = "main"
	}
	fd.name = l.pkgName(pkgKey{"func", f}, base)
	return fd
}

// methodReceiver is the record f lowers as a method of, or nil: f's first
// parameter is a record T or *T declared in f's package, every type
// parameter of f is one of T's arguments in order (Go methods take no type
// parameters of their own) with T's Go constraint and no witness, and no
// field of T has the method's Go name.
func (l *lowerer) methodReceiver(f *check.Func) (*typeDecl, bool) {
	if len(f.Sig.Params) == 0 || f.Decl == nil || f.Name == "main" {
		return nil, false
	}
	first := f.Sig.Params[0].Type
	ptr := false
	if p, ok := first.(*check.Pointer); ok {
		first, ptr = p.Elem, true
	}
	n, ok := first.(*check.Named)
	if !ok || n.Obj.Pkg != f.Pkg || n.Obj.Decl == nil {
		return nil, false
	}
	if _, isRecord := n.Obj.Decl.Spec.(*syntax.StructType); !isRecord {
		return nil, false
	}
	var own []*check.TypeParam
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			own = append(own, tp)
		}
	}
	origin := n
	if n.Orig != nil {
		origin = n.Orig
	}
	var typeArgs []check.Type
	for i, a := range n.TArgs {
		if i < len(origin.TParams) && origin.TParams[i].Kind == check.KindType {
			typeArgs = append(typeArgs, a)
		}
	}
	if len(typeArgs) != len(own) {
		return nil, false
	}
	var declared []*check.TypeParam
	for _, tp := range origin.TParams {
		if tp.Kind == check.KindType {
			declared = append(declared, tp)
		}
	}
	for i, a := range typeArgs {
		if tp, ok := a.(*check.TypeParam); !ok || tp != own[i] || i >= len(declared) || !sameGoConstraint(tp, declared[i]) {
			return nil, false
		}
	}
	td := l.typeDecl(n.Obj, f.Decl.Name)
	if td.kind != kindRecord {
		return nil, false
	}
	name := memberName(f.Name)
	for _, goName := range td.fieldName {
		if goName == name {
			return nil, false
		}
	}
	if td.errorType && name == "Error" {
		return nil, false
	}
	return td, ptr
}

// intrinsic is the Go body of an extern declaration this lowering
// implements, a call into the runtime with the declaration's parameters, or
// nil. Collection intrinsics bind to the prelude declaration and its exact
// signature. The finite host-provider bridge instead resolves the declared
// Go target through runtimeBindings; BodyOf supplies its hidden arguments.
// Both paths require a prelude declaration. A user extern has no Go body:
// each call of it is a direct call of its Go target (hostCall).
//
// The body is Go statements, one per line, each indented by one tab.
func (l *lowerer) intrinsic(f *check.Func) func(fc *funcCtx, params []string) string {
	if f.Pkg == nil || f.Pkg.Path != "prelude" || f.Decl == nil || f.Decl.Extern == nil {
		return nil
	}
	s := f.Sig
	if len(rowElems(s.Errors)) > 0 || len(rowElems(s.Uses)) > 0 {
		return nil
	}
	if bridge := l.hostBlockingIntrinsic(f); bridge != nil {
		return bridge
	}
	// The functions on slices and Maps (D-D6): Append and Delete call
	// their runtime/v1 bodies; Get builds the program's Option, which the
	// lowering emits in each program, so its body is written here.
	if len(s.Params) != 2 || len(s.Results) != 1 {
		return nil
	}
	switch {
	case f.Name == "Append" && len(s.TParams) == 1 && isSliceOf(s.Params[0].Type, s.TParams[0]) && s.Params[1].Type == s.TParams[0] && isSliceOf(s.Results[0], s.TParams[0]):
		return func(_ *funcCtx, params []string) string {
			return "\treturn " + l.rt() + ".Append(" + params[0] + ", " + params[1] + ")\n"
		}
	case f.Name == "Delete" && len(s.TParams) == 2 && isMapOf(s.Params[0].Type, s.TParams[0], s.TParams[1]) && s.Params[1].Type == s.TParams[0] && isMapOf(s.Results[0], s.TParams[0], s.TParams[1]):
		return func(_ *funcCtx, params []string) string {
			return "\treturn " + l.rt() + ".Delete(" + params[0] + ", " + params[1] + ")\n"
		}
	case f.Name == "Get" && len(s.TParams) == 2 && isMapOf(s.Params[0].Type, s.TParams[0], s.TParams[1]) && s.Params[1].Type == s.TParams[0] && isPreludeType(s.Results[0], "Option"):
		return func(fc *funcCtx, params []string) string {
			some, none := optionVariants(s.Results[0], fc.site)
			v := fc.scope.alloc("v")
			return "\tif " + v + ", ok := " + params[0] + "[" + params[1] + "]; ok {\n" +
				"\t\treturn " + fc.goType(some) + "{" + memberName("Value") + ": " + v + "}\n" +
				"\t}\n" +
				"\treturn " + fc.goType(none) + "{}\n"
		}
	}
	return nil
}

// optionVariants are the Some and None variants of an Option instance.
func optionVariants(t check.Type, site syntax.Node) (some, none check.Type) {
	u, ok := t.(*check.Named).Underlying().(*check.Union)
	if !ok || len(u.Variants) != 2 {
		ice(site, "the prelude's Option is not a union of two variants")
	}
	for _, v := range u.Variants {
		switch {
		case isPreludeType(v, "Some"):
			some = v
		case isPreludeType(v, "None"):
			none = v
		}
	}
	if some == nil || none == nil {
		ice(site, "the prelude's Option is not Some | None")
	}
	return some, none
}

// isSliceOf reports []T for the type parameter T.
func isSliceOf(t check.Type, elem *check.TypeParam) bool {
	s, ok := t.(*check.Slice)
	return ok && s.Elem == elem
}

// isMapOf reports Map[K, V] for the type parameters K and V.
func isMapOf(t check.Type, key, val *check.TypeParam) bool {
	m, ok := t.(*check.Map)
	return ok && m.Key == key && m.Elem == val
}

// isPreludeType reports the prelude's declared type name.
func isPreludeType(t check.Type, name string) bool {
	n, ok := t.(*check.Named)
	return ok && n.Obj.Name == name && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude"
}

// isBasic reports the basic type of kind k.
func isBasic(t check.Type, k check.BasicKind) bool {
	b, ok := t.(*check.Basic)
	return ok && b.Kind == k
}

// useFunc is a use of f at site: it refuses a function with no Go body and
// queues a library function's emission. A user extern has no Go
// declaration: its call is a direct call of its Go target (hostCall).
func (l *lowerer) useFunc(f *check.Func, site syntax.Node) *funcDecl {
	fd := l.funcDecl(f)
	switch {
	case userExtern(f):
		return fd
	case fd.extern != "" && l.intrinsic(f) == nil:
		if _, _, ok := l.stdBody(fd); ok {
			return fd // called at its site, its runtime/v1 body directly (stdCall)
		}
		// Every std extern is an intrinsic or has a runtime/v1 body
		// (TestStdExternsHaveBodies): a program declares no extern that
		// is not a user extern.
		ice(site, "the std function %s binds %s, which is neither an intrinsic nor a runtime/v1 body", f.Pkg.Name+"."+f.Name, fd.extern)
	case fd.extern == "" && (f.Decl == nil || f.Decl.Body == nil):
		refuse(site, "%s has no body, so it cannot be called", f.Name)
	}
	if fd.library && !fd.emitted {
		fd.emitted = true
		l.pending = append(l.pending, func() { l.emitFuncBody(fd) })
	}
	return fd
}

// emitFunc writes a user function once.
func (l *lowerer) emitFunc(fd *funcDecl) {
	if fd.emitted {
		return
	}
	fd.emitted = true
	l.emitFuncBody(fd)
}

// emitFuncBody writes fd's Go declaration.
func (l *lowerer) emitFuncBody(fd *funcDecl) {
	l.bodies++
	f := fd.fn
	b := &l.user
	directives := !fd.library
	if fd.library {
		b = &l.gen
		// A library body with Ergo source (std log and trace) reads the
		// facts of its own package's Info: every fact of the lowering is
		// keyed by syntax node, and its nodes are its package's. A library
		// body is emitted from the pending queue, never inside another
		// body, so the swap covers exactly this body.
		if f.Pkg.Info != nil && f.Pkg.Info != l.info {
			saved := l.info
			l.info = f.Pkg.Info
			defer func() { l.info = saved }()
		}
	}
	scope := newNamer(l.names)
	site := syntax.Node(f.Decl.Name)
	fc := newFuncCtx(l, scope, directives)
	fc.owner = f.Name
	if fd.method != nil {
		fc.owner = fd.method.tn.Name + "." + f.Name
	}
	if directives {
		l.recordDecl(fc.owner, f.Decl)
	}
	var head strings.Builder
	var params []*check.Var
	if f.Decl.Type != nil && f.Decl.Type.Params != nil {
		for _, field := range f.Decl.Type.Params.List {
			if len(field.Names) == 0 {
				// An unnamed parameter, func M(S, int): no name binds, so
				// it gets a fresh Go name (stage C round 4 review T1).
				params = append(params, nil)
				continue
			}
			for _, id := range field.Names {
				v, ok := f.Pkg.Info.Defs[id].(*check.Var)
				if !ok {
					if id.Name != "_" {
						ice(id, "the parameter %s has no Defs entry", id.Name)
					}
					v = nil
				}
				params = append(params, v)
			}
		}
	}
	if len(params) != len(f.Sig.Params) {
		ice(site, "%s declares %d parameters and its signature has %d", f.Name, len(params), len(f.Sig.Params))
	}
	// Type parameters: a method's are its receiver's; a function's are its
	// own type-kinded ones.
	var own []*check.TypeParam
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			own = append(own, tp)
		}
	}
	paramNames := make([]string, len(params))
	name := func(i int) string {
		if params[i] == nil {
			return scope.alloc("_p")
		}
		n := scope.alloc(params[i].Name)
		fc.locals[params[i]] = n
		return n
	}
	if fd.method != nil {
		tps := map[*check.TypeParam]string{}
		var args []string
		for _, tp := range own {
			tps[tp] = scope.alloc(tp.Name)
			args = append(args, tps[tp])
		}
		fc.params = tps
		recv := l.useType(fd.method).name
		if len(args) > 0 {
			recv += "[" + strings.Join(args, ", ") + "]"
		}
		if fd.ptr {
			recv = "*" + recv
		}
		paramNames[0] = name(0)
		head.WriteString("func (" + paramNames[0] + " " + recv + ") " + fd.name + "(")
	} else {
		list, tps := l.typeParams(own, scope, site)
		fc.params = tps
		head.WriteString("func " + fd.name + list + "(")
	}
	// Every function takes the environment first, which carries the running
	// task (stage D3b, Rule 1).
	fc.env = scope.alloc("env")
	parts := []string{fc.env + " *" + l.env()}
	fc.frame = newFrame(nil, fd.fallible, false)
	if fd.extern == "" && f.Decl.Body != nil {
		fc.frame.polls = framePolls(f.Pkg.Info, f.Decl)
	}
	if f.Pkg.Info.TaskResults[f] && fd.extern == "" {
		// A function whose result holds a Task receives its caller's frame
		// scope as the destination its return moves the Task to (stage D3b,
		// Rule 2). Since stage E2 (C13) a result holds a Task only by being
		// one: the checker refuses a Task held otherwise (S108).
		if !f.Sig.ResultIsTask() {
			ice(site, "the function %s, whose result holds a Task, does not return one", f.Name)
		}
		fc.frame.dest = scope.alloc("dest")
		parts = append(parts, fc.frame.dest+" *"+l.rt()+".Scope")
	}
	// A type parameter a method set constrains takes its operations as a
	// witness, after the environment and the destination (witness.go).
	for _, tp := range witnessParams(f.Sig) {
		if fd.method != nil {
			ice(site, "the Go method %s has a witness for %s; methodReceiver keeps such a function a Go function", f.Name, tp.Name)
		}
		fc.witness[tp] = scope.alloc("w" + tp.Name)
		parts = append(parts, fc.witness[tp]+" "+fc.witnessType(tp, tp))
	}
	for i, p := range f.Sig.Params {
		if fd.method != nil && i == 0 {
			continue
		}
		paramNames[i] = name(i)
		parts = append(parts, paramNames[i]+" "+fc.goType(p.Type))
	}
	head.WriteString(strings.Join(parts, ", ") + ")")
	if len(f.Sig.Results) > 1 {
		refuse(site, "several results are a tuple, which lowering supports after check-v1 stage B (196t)")
	}
	if len(f.Sig.Results) == 1 {
		fc.frame.result = f.Sig.Results[0]
	}
	if fd.extern != "" {
		if isClose(f) {
			// A checked Close masks its own body (closeBody, below); an
			// extern body would run unmasked, and no release route binds
			// one (runtime.md, I2). No accepted program declares one today.
			ice(site, "the checked Close %s has an extern body, which no release route masks", f.Name)
		}
		head.WriteString(fc.resultList(fc.frame.result, fd.fallible))
		body := l.intrinsic(f)(fc, paramNames)
		fd.lowered, fd.readsEnv = true, namesIdent(body, fc.env)
		b.WriteString(head.String() + " {\n" + body + "}\n\n")
		return
	}
	// A function that starts a task, registers an exit action or receives a
	// Task by return opens its frame scope first (stage D3b, Rule 2).
	frameErr := ""
	needsFrame := l.bodyNeedsFrame(f.Decl.Body.List)
	switch {
	case needsFrame && fd.fallible:
		frameErr = scope.alloc("err")
		head.WriteString(fc.namedResults(fc.frame.result, frameErr))
	default:
		head.WriteString(fc.resultList(fc.frame.result, fd.fallible))
	}
	if directives {
		b.WriteString(l.directive(f.Decl.Pos()))
	}
	b.WriteString(head.String() + " {\n")
	fc.depth = 1
	fc.closeBody(f, site, func() {
		if needsFrame {
			fc.site = site
			fc.openFrame(frameErr)
		}
		fc.entryPoll(f.Pkg.Info, f.Decl)
		fc.body(f.Decl.Body.List)
	})
	fd.lowered, fd.readsEnv = true, namesIdent(fc.out.String(), fc.env)
	b.WriteString(fc.out.String())
	b.WriteString("}\n\n")
}

// directive is the //line directive that maps the next Go line to pos in
// the user file.
func (l *lowerer) directive(pos syntax.Pos) string {
	line, column := l.file.Position(pos)
	return Directive(l.src, line, column)
}

// packageLet lowers a package-level let to a Go package variable. A
// constant initializer stays the variable's Go initializer, which cannot
// fail; any other runs in initFunc, in Info.InitOrder, inside the program's
// root task, so a defect in it is reported with its Ergo frames as any
// other defect is (stage C round 2 review m1).
func (l *lowerer) packageLet(d *syntax.VarDecl) {
	for _, spec := range d.Specs {
		switch {
		case spec.Pattern == nil && len(spec.Names) == 1 && len(spec.Values) <= 1:
			var x syntax.Expr
			if len(spec.Values) == 1 {
				x = spec.Values[0]
			}
			l.packageVar(spec, spec.Names[0], x)
		case spec.Pattern == nil && len(spec.Names) == len(spec.Values):
			// `let a, b = x, y`: one Go variable per name.
			for i, id := range spec.Names {
				l.packageVar(spec, id, spec.Values[i])
			}
		case len(spec.Values) == 1:
			l.packagePattern(spec)
		default:
			ice(spec, "a package let whose names and values do not pair")
		}
	}
}

// addInit records the Go code of the package initializer x for initFunc:
// after the //line directive of at, a block of its own that runs stmts and
// then assigns.
func (l *lowerer) addInit(x syntax.Expr, at syntax.Pos, stmts string, assigns []string) {
	var b strings.Builder
	b.WriteString(l.directive(at))
	b.WriteString("\t{\n" + stmts)
	for _, a := range assigns {
		b.WriteString("\t\t" + a + "\n")
	}
	b.WriteString("\t}\n")
	if l.inits == nil {
		l.inits = map[syntax.Expr]string{}
	}
	l.inits[x] = b.String()
}

// hasInits reports whether initFunc runs any initializer.
func (l *lowerer) hasInits() bool {
	for _, in := range l.info.InitOrder {
		if _, ok := l.inits[in.Value]; ok {
			return true
		}
	}
	return len(l.machineInits) > 0
}

// initFunc is the Go function that runs the package-level initializers in
// Info.InitOrder (Go's initialization order: a let after every let it
// refers to, directly or through functions, else in declaration order),
// then each machine's initial value in declaration order (machine.go), or
// "" when there is none. The entry calls it first inside v1.Main.
func (l *lowerer) initFunc() string {
	if !l.hasInits() {
		return ""
	}
	name := l.pkgName(pkgKey{"init", nil}, "ergoInit")
	var b strings.Builder
	b.WriteString("// " + name + " runs the package-level lets in Go's initialization order,\n// inside the program's root task.\nfunc " + name + "(" + initEnv(l) + " *" + l.env() + ") {\n")
	for _, in := range l.info.InitOrder {
		b.WriteString(l.inits[in.Value])
	}
	for _, x := range l.machineInits {
		b.WriteString(l.inits[x])
	}
	b.WriteString("}\n\n")
	return b.String()
}

// initEnv is the Go name of initFunc's environment parameter, which every
// initializer's code reads. Each initializer allocates its locals in a fresh
// scope under the package names, so the name is the same in each.
func initEnv(l *lowerer) string {
	return newNamer(l.names).alloc("env")
}

// packageVar lowers one name of a package-level let, with its initializer
// x (nil for none). A name the checker gave no object, `_`, keeps its
// initializer's evaluation.
func (l *lowerer) packageVar(spec *syntax.VarSpec, id *syntax.Ident, x syntax.Expr) {
	v, ok := l.info.Defs[id].(*check.Var)
	if !ok && (id.Name != "_" || x == nil) {
		ice(id, "the let %s has no Defs entry", id.Name)
	}
	fc := newFuncCtx(l, newNamer(l.names), true)
	fc.depth = 2
	fc.env = fc.scope.alloc("env") // initFunc's parameter (initEnv)
	name, goType := "_", ""
	if ok {
		fc.owner = v.Name
		fc.frame = newFrame(v.Type, false, false)
		name, goType = l.vars[v], fc.goType(v.Type)
	} else {
		fc.owner = "_"
		fc.frame = newFrame(fc.typeOf(x), false, false)
		goType = fc.goType(fc.typeOf(x))
	}
	if x != nil {
		fc.frame.polls = framePolls(l.info, spec)
	}
	l.recordDecl(fc.owner, spec)
	if x == nil {
		l.user.WriteString(l.directive(id.Pos()))
		if def := fc.defaultValue(v.Type); def != "" {
			l.user.WriteString("var " + name + " " + goType + " = " + def + "\n\n")
		} else {
			l.user.WriteString("var " + name + " " + goType + "\n\n")
		}
		return
	}
	fc.entryPoll(l.info, spec)
	val := fc.assigned(x)
	if _, constant := l.info.Values[x]; constant && fc.out.Len() == 0 {
		l.user.WriteString(l.directive(id.Pos()))
		l.user.WriteString("var " + name + " " + goType + " = " + val + "\n\n")
		return
	}
	if name != "_" {
		l.user.WriteString(l.directive(id.Pos()))
		l.user.WriteString("var " + name + " " + goType + "\n\n")
	}
	l.addInit(x, id.Pos(), fc.out.String(), []string{name + " = " + val})
}

// packagePattern lowers a package-level let that destructures one value
// (D-B5; 196t): `let P = e`, whose pattern the checker proved irrefutable,
// or `let q, r = e` over a tuple. Its initializer, in initFunc, evaluates
// the value once into a Go local and assigns each binding, a Go package
// variable, from it.
func (l *lowerer) packagePattern(spec *syntax.VarSpec) {
	x := spec.Values[0]
	fc := newFuncCtx(l, newNamer(l.names), true)
	fc.depth = 2
	fc.env = fc.scope.alloc("env") // initFunc's parameter (initEnv)
	t := fc.typeOf(x)
	if c, ok := l.info.Implicit[x]; ok && spec.Pattern != nil {
		t = c.To
	}
	// The owner of the initializer's calls is the let's first name (#230).
	fc.owner = "_"
	if len(spec.Names) > 0 {
		fc.owner = spec.Names[0].Name
	} else if vs := patternVars(l.info.Patterns[spec.Pattern], nil); len(vs) > 0 {
		fc.owner = vs[0].Name
	}
	fc.frame = newFrame(t, false, framePolls(l.info, spec))
	fc.entryPoll(l.info, spec)
	l.recordDecl(fc.owner, spec)
	at := spec.Pos()
	if len(spec.Names) > 0 {
		at = spec.Names[0].Pos()
	}
	val := ""
	if spec.Pattern != nil {
		val = fc.assigned(x)
	} else {
		val = fc.value(x)
	}
	init := l.pkgName(pkgKey{"letinit", spec}, "ergoLet")
	var binds []patBind
	if spec.Pattern != nil {
		p := l.info.Patterns[spec.Pattern]
		if p == nil {
			ice(spec.Pattern, "a let pattern with no Patterns entry")
		}
		var tests []string
		fc.pattern(p, init, t, spec.Pattern, &tests, &binds)
	} else {
		tup, ok := underlying(t).(*check.Tuple)
		if !ok || len(tup.Types) != len(spec.Names) {
			refuse(spec, "this let of several names is lowered after check-v1 stage D (the Map rule)")
		}
		for i, id := range spec.Names {
			if w, ok := l.info.Defs[id].(*check.Var); ok {
				binds = append(binds, patBind{w, fc.elemAt(x, i, init+"."+tupleField(i))})
			}
		}
	}
	assigns := []string{init + " := " + val}
	if len(binds) == 0 {
		assigns = append(assigns, "_ = "+init)
	}
	if len(binds) > 0 {
		l.user.WriteString(l.directive(at))
	}
	for _, b := range binds {
		name, ok := l.vars[b.v]
		if !ok {
			ice(spec, "the let binding %s has no Go name", b.v.Name)
		}
		l.user.WriteString("var " + name + " " + fc.goType(b.v.Type) + "\n\n")
		assigns = append(assigns, name+" = "+b.val)
	}
	l.addInit(x, at, fc.out.String(), assigns)
}

// patternVars lists the variables a pattern binds, in order.
func patternVars(p *check.Pattern, out []*check.Var) []*check.Var {
	if p == nil {
		return out
	}
	if (p.Kind == check.PatBind || p.Kind == check.PatAs) && p.Var != nil {
		out = append(out, p.Var)
	}
	for _, e := range p.Elems {
		out = patternVars(e, out)
	}
	for _, f := range p.Fields {
		out = patternVars(f.Pattern, out)
	}
	return out
}

// env is the environment type's name: the running task, then one field per
// capability the program binds or needs, each an operation type (spec D3;
// runtime.md, Host and lowering notes). with copies it; a task.Go block runs
// with a copy that holds the child task.
func (l *lowerer) env() string {
	if l.envName == "" {
		l.envName = l.pkgName(pkgKey{"env", nil}, "Env")
		l.taskPkg()
	}
	return l.envName
}

// envTask is the field of the environment that holds the running task, an
// *rt.Task. It is lowercase, and a capability field is capitalized
// (memberName), so the two never clash.
const envTask = "task"

// taskOf is the running task of the environment env, the only reader of
// envTask's field. Every emitter that passes the task names it here, so the
// blocking owner scan classifies each one (runtimeCall's hidden argument,
// a capability or program operation, a poll, a frame scope).
func taskOf(env string) string { return env + "." + envTask }

// envField is the environment's field for a capability.
func (l *lowerer) envField(cap *check.TypeName) string {
	if f, ok := l.envFields[cap]; ok {
		return f
	}
	l.env()
	taken := map[string]bool{}
	for _, f := range l.envFields {
		taken[f] = true
	}
	name := memberName(cap.Name)
	for i := 1; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d", memberName(cap.Name), i)
	}
	l.envFields[cap] = name
	l.envOrder = append(l.envOrder, cap)
	l.useType(l.typeDecl(cap, cap.Decl.Name))
	return name
}

// envType writes the environment type, when the program has one.
func (l *lowerer) envType(b *strings.Builder) {
	if l.envName == "" {
		return
	}
	b.WriteString("type " + l.envName + " struct {\n")
	b.WriteString("\t" + envTask + " *" + l.taskPkg() + ".Task\n")
	for _, cap := range l.envOrder {
		b.WriteString("\t" + l.envFields[cap] + " " + l.types[cap].name + "\n")
	}
	b.WriteString("}\n\n")
}

// rowElems are a row's elements with every inferred row replaced by its
// solution and every row argument by its row; a row parameter stays.
func rowElems(r *check.Row) []check.Type {
	var out []check.Type
	var walk func(r *check.Row)
	walk = func(r *check.Row) {
		for _, e := range r.Elems() {
			switch e := e.(type) {
			case *check.RowVar:
				walk(e.Solution())
			case *check.RowArg:
				walk(e.Row)
			default:
				out = append(out, e)
			}
		}
	}
	walk(r)
	return out
}
