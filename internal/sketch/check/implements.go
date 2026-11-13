package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Declared implementation (types.md, Interface assignability 1; audit CSX
// §4.1, owner q167 and q170). A type implements an operation type I
// exactly when its declaration names I in its `implements` clause, or
// names an operation type that includes I (`..I`, at any depth):
//
//	type Mem { rows Map[int, Row] } implements domain.Store, Closer
//	type Conn { ..Closer, Number() int }
//
// The clause is checked once, at the type (checkImplementsClauses): each
// operation of each named operation type needs a function of the type
// whose shape and rows fit (implements, the compatibility rule of
// Interface assignability 1). A use reads the declared relation
// (declares).
//
// Prior art: Java and C# declare a conformance on the type (`implements`,
// `: IDisposable`); Rust declares it apart, in the crate of the trait or
// of the type (`impl Trait for T`); Swift's extension conformances; Go
// asserts it by hand (`var _ io.Closer = T{}`); Elixir's `@behaviour` is
// checked at compile time in the module. Gleam has no interfaces.

// implementsClause resolves the clause of n's declaration d in ctx, the
// declaration's own type parameters. Each name must be an operation type,
// named once; the declaring type must not be an operation type, which
// includes another with `..I` instead.
func (c *checker) implementsClause(n *Named, d *syntax.TypeDecl, ctx *typeCtx) {
	if len(d.Implements) == 0 {
		return
	}
	if _, isOps := n.under.(*Interface); isOps {
		c.errorf(d.Implements[0], CodeType, "an operation type includes another with ..%s as a member; implements is written on a type with functions (types.md, Interface assignability 1)", syntaxText(d.Implements[0]))
		return
	}
	for _, x := range d.Implements {
		var t Type
		if id, ok := x.(*syntax.Ident); ok && id.Name == n.Obj.Name && c.pkg.Path != std.PreludePath {
			// A type named like a prelude type hides it in its package
			// (types.md, Scope 2), and no type implements itself: its
			// own name in its clause is the prelude's type it hides
			// (`type Reader { … } implements Reader` in std json).
			if p := preludeType(id.Name); p != nil {
				t = p
				c.recordUse(id, p.Obj)
			}
		}
		if t == nil {
			t = c.typExpr(x, ctx)
		}
		if isInvalid(t) {
			continue
		}
		op, ok := t.(*Named)
		if !ok || asInterface(op) == nil || op.origin().Obj.Decl == nil && !op.origin().foreign {
			c.errorf(x, CodeType, "implements names operation types; %s is %s (types.md, Interface assignability 1)", t, typeNoun(t))
			continue
		}
		if it := asInterface(op); it.Set != SetAll {
			c.errorf(x, CodeType, "implements names operation types; %s is a constraint (types.md, Interface assignability 1)", t)
			continue
		}
		if slices.ContainsFunc(n.impls, func(o *Named) bool { return c.identical(o, op) }) {
			c.errorf(x, CodeType, "%s is named twice in this implements clause", t)
			continue
		}
		n.impls = append(n.impls, op)
		n.implExprs = append(n.implExprs, x)
	}
}

// typeNoun names what kind of type t is, for a clause that wants an
// operation type.
func typeNoun(t Type) string {
	switch under(t).(type) {
	case *Struct:
		return "a record"
	case *Union:
		return "a union"
	case *Signature:
		return "a function type"
	}
	return "not an operation type"
}

// syntaxText is the source spelling of a type name expression.
func syntaxText(x syntax.Expr) string {
	switch x := x.(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.SelectorExpr:
		return syntaxText(x.X) + "." + x.Sel.Name
	case *syntax.IndexExpr:
		return syntaxText(x.X) + "[…]"
	}
	return "I"
}

// interfaceIncludes resolves the members `..I` of an operation type: each
// includes the operations of the operation type I, and a value of the
// including type is a value of I (Interface assignability 1). seen holds
// the operations already declared; an operation of I that the type also
// lists is S108, as a twice-declared operation is.
func (c *checker) interfaceIncludes(spec *syntax.InterfaceType, ctx *typeCtx, it *Interface, seen map[string]bool) {
	for _, f := range spec.Includes {
		t := c.typExpr(f.Type, ctx)
		if isInvalid(t) {
			continue
		}
		n, ok := t.(*Named)
		if ok && n.origin().state == 0 {
			c.resolveNamed(n.origin())
		}
		inc := asInterface(t)
		if !ok || inc == nil || inc.Set != SetAll || n.origin().state == 1 {
			why := typeNoun(t)
			if ok && n.origin().state == 1 {
				why = "being declared: an operation type cannot include itself"
			}
			c.errorf(f.Type, CodeType, "..%s includes the operations of an operation type; %s is %s (types.md, Interface assignability 1)", t, t, why)
			continue
		}
		it.includes = append(it.includes, n)
		for _, m := range inc.Methods {
			if seen[m.Name] {
				c.errorf(f.Type, CodeType, "method %s is declared twice: ..%s includes it", m.Name, t)
				continue
			}
			seen[m.Name] = true
			it.Methods = append(it.Methods, m)
		}
	}
}

// closer is the prelude's release contract, `type Closer { Close()
// takes }` (resources R1): a type that implements it is a resource.
func (c *checker) closer() *Named { return c.preludeContract("Closer") }

// preludeContract is the prelude's operation type name, a contract the
// language gives a meaning (Closer, R1; Keeping, R13), or nil.
func (c *checker) preludeContract(name string) *Named {
	if c.pkg.Path == std.PreludePath {
		// The prelude's own check reads its scope: prelude() is not
		// done yet.
		tn, _ := c.pkg.Scope.names[name].(*TypeName)
		if tn == nil {
			return nil
		}
		n, _ := tn.Type.(*Named)
		return n
	}
	return preludeType(name)
}

// isGoCloser reports the binding of Go's io.Closer: in a binding, the
// contract's Go form, `Close() error` (interop.md, Resources 1). Package
// io declares its own Closer (types.md, Scope 2), so its interfaces
// include io.Closer and its types name it, and io.Closer stands for the
// prelude's Closer there.
func isGoCloser(n *Named) bool {
	o := n.origin()
	return o.Obj != nil && o.Obj.Name == "Closer" && o.Obj.Pkg != nil && o.Obj.Pkg.Host() && o.Obj.Pkg.Path == "io"
}

// declaredOps are the operation types t declares that it implements, each
// at t's type arguments, closed over includes: for a named type that is
// not an operation type, its clause and what each named type includes;
// for an operation type, what it includes at any depth; for a type
// parameter, its constraint and what that includes. A borrow and a
// go.Ref read their element.
func (c *checker) declaredOps(t Type) []*Named {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	if e := refElem(t); e != nil {
		t = e
	}
	if n, ok := t.(*Named); ok && n.origin().state == 2 {
		if ops, ok := c.declOps[n]; ok {
			return ops
		}
		ops := c.declaredOpsOf(n)
		if c.declOps == nil {
			c.declOps = map[*Named][]*Named{}
		}
		c.declOps[n] = ops
		return ops
	}
	return c.declaredOpsOf(t)
}

func (c *checker) declaredOpsOf(t Type) []*Named {
	var out []*Named
	var add func(n *Named)
	add = func(n *Named) {
		if slices.ContainsFunc(out, func(o *Named) bool { return o == n || o.origin() == n.origin() && c.identical(o, n) }) {
			return
		}
		out = append(out, n)
		if isGoCloser(n) {
			if cl := c.closer(); cl != nil {
				add(cl)
			}
		}
		if it := asInterface(n); it != nil {
			for _, inc := range it.includes {
				add(inc)
			}
		}
	}
	switch t := t.(type) {
	case *Named:
		o := t.origin()
		if o.state == 0 {
			c.resolveNamed(o)
		}
		if asInterface(o) == nil && len(o.impls) == 0 {
			// Read on the origin: an instance is not expanded to learn
			// that it declares nothing (D16, round-6 M5).
			return out
		}
		if asInterface(o) != nil {
			it := asInterface(t)
			if isGoCloser(t) {
				if cl := c.closer(); cl != nil {
					add(cl)
				}
			}
			for _, inc := range it.includes {
				add(inc)
			}
			return out
		}
		m := map[*TypeParam]Type{}
		for i, tp := range o.TParams {
			if i < len(t.TArgs) {
				m[tp] = t.TArgs[i]
			}
		}
		for _, op := range o.impls {
			if n, ok := c.subst(op, m).(*Named); ok {
				add(n)
			}
		}
	case *TypeParam:
		if n, ok := t.Constraint.(*Named); ok && asInterface(n) != nil {
			add(n)
		}
	}
	return out
}

// declares reports whether t declares that it implements the operation
// type op: op is t itself, or one of declaredOps(t).
func (c *checker) declares(t Type, op Type) bool {
	if c.identical(t, op) {
		return true
	}
	return slices.ContainsFunc(c.declaredOps(t), func(o *Named) bool { return c.identical(o, op) })
}

// implementsCloser reports a type that declares the release contract
// (resources R1).
func (c *checker) implementsCloser(t Type) bool {
	cl := c.closer()
	if cl == nil {
		return false
	}
	if n, ok := t.(*Named); ok && (n.origin() == cl || isGoCloser(n)) {
		return true
	}
	return slices.ContainsFunc(c.declaredOps(t), func(o *Named) bool { return o.origin() == cl })
}

// undeclared is why the value type v is not a value of the operation type
// op at a use (a conversion, a context entry, a constraint, a law's
// provider): v does not declare that it implements op; "" when it does,
// or when the old method-set rule still decides. Go's method sets decide
// between two Go types (interop.md, Checking), and a protocol of the
// runtime (an unnamed interface) is met by its functions. The message
// names the clause to write.
func (c *checker) undeclared(v, op Type, site string) string {
	opn, ok := op.(*Named)
	if !ok {
		return ""
	}
	it := asInterface(opn)
	if it == nil || it.Set != SetAll {
		return ""
	}
	if c.declares(v, op) {
		return ""
	}
	if vn, ok := v.(*Named); ok && asInterface(vn) != nil && vn.origin() == opn.origin() {
		// Two instances of one operation type (a Task whose row
		// widens): their operations decide, as an instance's do.
		return ""
	}
	if legacyRecord != nil && c.implements(v, it, true) == "" {
		// ergo fix: the old rule admits the pair, and the fix writes it.
		c.recordLegacy(v, op, site)
		return ""
	}
	e := v
	if p, ok := e.(*Pointer); ok {
		e = p.Elem
	}
	if r := refElem(e); r != nil {
		e = r
	}
	if ScriptOf(e) != nil {
		// A Script has the operations of its capability (script.go,
		// rule 4): std declares it for every capability.
		return ""
	}
	if isHostNamed(e) && isHostNamed(op) {
		return ""
	}
	if asInterface(e) != nil && isHostNamed(op) {
		// An Ergo operation type's value into a Go interface: Go's
		// conversion decides it, and assignability refuses a program's
		// operation type there for its calling convention (interop.md).
		return ""
	}
	for _, m := range it.Methods {
		// A sealed operation names why no clause of v's package can
		// declare it (grammar.md, Visibility 2).
		if got, fn := c.methodOf(v, m.Name); got != nil {
			if why := sealedBy(v, it, m, fn); why != "" {
				return why
			}
		}
	}
	n, ok := e.(*Named)
	if !ok {
		return fmt.Sprintf("%s does not declare that it implements %s", v, op)
	}
	o := n.origin()
	switch {
	case o.Obj == nil || o.Obj.Pkg == nil:
		return fmt.Sprintf("%s does not declare that it implements %s", v, op)
	case o.Obj.Pkg.Host():
		return fmt.Sprintf("%s is a Go type and does not implement %s: wrap it in a record that implements %s (interop.md, Checking)", v, op, op)
	case asInterface(n) != nil && o.Obj.Pkg != c.pkg:
		// Another package's operation type, std's included: its package
		// would have to include op, so the value is wrapped here.
		return fmt.Sprintf("%s does not include %s: wrap the value in a record that implements %s, or write ..%s in %s in package %s (types.md, Interface assignability 1)", v, op, op, op, o.Obj.Name, o.Obj.Pkg.Name)
	case asInterface(n) != nil:
		return fmt.Sprintf("%s does not include %s: write ..%s in %s (types.md, Interface assignability 1)", v, op, op, o.Obj.Name)
	case o.Obj.Pkg != c.pkg:
		return fmt.Sprintf("%s does not declare that it implements %s: write implements %s after its body in package %s, or wrap the value in a record that implements %s (types.md, Interface assignability 1)", v, op, op, o.Obj.Pkg.Name, op)
	}
	return fmt.Sprintf("%s does not declare that it implements %s: write implements %s after %s's body (types.md, Interface assignability 1)", v, op, op, o.Obj.Name)
}

// declaredInstance is the instance of the operation type op's origin that
// the value type v declares (its clause, at v's type arguments), or nil:
// inference reads the written instance (Generics 6), and the conversion
// that follows reads the declaration.
func (in *inferer) declaredInstance(op, v Type) Type {
	opn, ok := op.(*Named)
	if !ok {
		return nil
	}
	for _, d := range in.c.declaredOps(v) {
		if d.origin() == opn.origin() {
			return d
		}
	}
	return nil
}

// clauseUnmet reports a use of a declared pair whose clause failed at the
// type (S106 there): the use stays silent. A *T function that implements
// through the clause on T still needs &x at a conversion (Borrows 6), so
// that use is not silent.
func (c *checker) clauseUnmet(v Type, it *Interface) bool {
	n, ok := v.(*Named)
	if !ok || asInterface(n) != nil || len(n.origin().impls) == 0 || n.origin().Obj.Pkg.Host() {
		return false
	}
	// The release contract's clause checks only the Close function's
	// shape at the type (checkImplementsClauses), so a refusal of the
	// conversion itself, a release that needs a capability (R5), is
	// reported at the use.
	if cl := c.closer(); cl != nil && asInterface(cl) == it {
		return false
	}
	for _, d := range c.declaredOps(v) {
		if isGoCloser(d) && asInterface(d) == it {
			return false
		}
	}
	foreign := false // as checkImplementsClauses reads the clause
	return c.implementsCapturing(&Pointer{Elem: v}, it, true, &foreign) != ""
}

// checkImplementsClauses checks each written clause of the package at its
// type: the type has a function for every operation of each named
// operation type, with the same shape and rows that are no wider (the
// compatibility rule of Interface assignability 1). A borrowing function
// (on *T) implements through the clause on T; a conversion still writes
// &x (Borrows 6).
func (c *checker) checkImplementsClauses() {
	for _, tn := range c.pkg.Types {
		n, ok := tn.Type.(*Named)
		if !ok || len(n.impls) == 0 || tn.Pkg.Host() {
			continue
		}
		c.cur = tn.file
		self := n
		if len(n.TParams) > 0 {
			args := make([]Type, len(n.TParams))
			for i, tp := range n.TParams {
				args[i] = tp
			}
			self = c.instantiate(n, args)
		}
		for i, op := range n.impls {
			it := asInterface(op)
			if it == nil {
				continue
			}
			if op.origin() == c.closer() || isGoCloser(op) {
				// The release contract is the release shape (resources
				// R1): a Close of the type that takes no other argument
				// and returns nothing. It may need capabilities, which
				// the scope that releases it provides; a call through a
				// Closer value is checked at the conversion.
				if c.closeFunc(n) == nil {
					c.errorf(n.implExprs[i], CodeProvider, "%s does not implement %s: it needs a function Close(x %s) that returns nothing and cannot fail (resources R1, R2)", n.Obj.Name, op, n.Obj.Name)
				}
				continue
			}
			// A method that needs go.Foreign beyond the operation's row
			// implements it: the build of a value as the operation type
			// holds go.Foreign, and that conversion charges it (README
			// Gap 17), so the declaration admits what the conversion does.
			foreign := false
			if why := c.implementsCapturing(&Pointer{Elem: self}, it, true, &foreign); why != "" {
				c.errorf(n.implExprs[i], CodeProvider, "%s does not implement %s: %s (types.md, Interface assignability 1)", n.Obj.Name, op, strings.TrimPrefix(why, "*"))
			}
		}
	}
}

// operationIncludeBodies decides each body that has only `..X` members:
// the parser reads it as a record, and it is an operation type when X is
// one (`type Releaser { ..Closer }`; grammar.md, Declarations 15). It runs
// after collect, before any type resolves, and gives such a declaration
// its operation type's body.
func (c *checker) operationIncludeBodies() {
	seen := map[*TypeName]bool{}
	for _, tn := range c.pkg.Types {
		c.cur = tn.file
		c.operationBody(tn, seen)
	}
}

// operationBody reports whether tn's declaration is an operation type,
// deciding an includes-only body as operationIncludeBodies says.
func (c *checker) operationBody(tn *TypeName, seen map[*TypeName]bool) bool {
	if tn.Decl == nil {
		it := asInterface(tn.Type)
		return it != nil && it.Set == SetAll
	}
	switch spec := tn.Decl.Spec.(type) {
	case *syntax.InterfaceType:
		return spec.Lbrace.IsValid()
	case *syntax.StructType:
		fields := spec.Fields.List
		if len(fields) == 0 || seen[tn] {
			return false
		}
		for _, f := range fields {
			if !f.Include {
				return false
			}
		}
		seen[tn] = true
		target := c.includedName(fields[0].Type)
		if target == nil {
			return false
		}
		if target.Pkg == c.pkg {
			saved := c.cur
			c.cur = target.file
			op := c.operationBody(target, seen)
			c.cur = saved
			if !op {
				return false
			}
		} else if it := asInterface(c.resolvedType(target.Type)); it == nil || it.Set != SetAll {
			return false
		}
		tn.Decl.Spec = &syntax.InterfaceType{Lbrace: spec.Fields.Opening, Includes: fields, Rbrace: spec.Fields.Closing}
		return true
	}
	return false
}

// includedName is the type name a member `..X` or `..X[A]` names, read
// without resolving it; nil when it names none.
func (c *checker) includedName(x syntax.Expr) *TypeName {
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = ix.X
	}
	switch x := x.(type) {
	case *syntax.Ident:
		tn, _ := c.lookupType(x.Name, nil).(*TypeName)
		return tn
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			return nil
		}
		pn, ok := c.lookupType(id.Name, nil).(*PkgName)
		if !ok || pn.broken {
			return nil
		}
		m, _ := pn.Pkg.member(x.Sel.Name)
		tn, _ := m.(*TypeName)
		return tn
	}
	return nil
}

// ---------------------------------------------------------------- the old rule, for ergo fix

// LegacyPair is a pair the old method-set rule admitted where no clause
// declares it: a type whose functions match an operation type (Include
// false), or an operation type whose operations hold another's (Include
// true). ergo fix (internal/sketch/fix) writes the clause or the include.
type LegacyPair struct {
	// Type is the implementing declaration; Pkg is its package's path.
	Type *TypeName
	Pkg  string
	// Op is the operation type, at Type's own type parameters when Type
	// is generic; nil when no instance at those parameters fits.
	Op Type
	// Include is set when Type is an operation type: its members `..Op`.
	Include bool
	// Site names the reader of the old rule: "conversion", "constraint",
	// "law", "release".
	Site string
}

// legacyRecord, when set, records each pair the old rule admits (ergo
// fix). It is set only by RecordLegacy.
var legacyRecord func(LegacyPair)

// RecordLegacy sets the recorder of the old rule's pairs for every check
// that runs until restore is called. It is for ergo fix only, and not
// safe for concurrent checks.
func RecordLegacy(f func(LegacyPair)) (restore func()) {
	saved := legacyRecord
	legacyRecord = f
	return func() { legacyRecord = saved }
}

// recordLegacy records that the old rule admitted v as op at site, when
// no clause declares it.
func (c *checker) recordLegacy(v Type, op Type, site string) {
	if legacyRecord == nil {
		return
	}
	if p, ok := v.(*Pointer); ok {
		v = p.Elem
	}
	if e := refElem(v); e != nil {
		v = e
	}
	n, ok := v.(*Named)
	opn, ok2 := op.(*Named)
	if !ok || !ok2 || c.declares(v, op) || sealedStdOps(op) || ScriptOf(v) != nil {
		return
	}
	if slices.ContainsFunc(c.declaredOps(v), func(d *Named) bool { return d.origin() == opn.origin() }) {
		// A clause names an instance of op's origin: op is that instance
		// while a call's type arguments are still being inferred.
		return
	}
	o := n.origin()
	if o.Obj == nil || o.Obj.Decl == nil || o.Obj.Pkg == nil {
		return
	}
	if isHostNamed(v) && isHostNamed(op) {
		// Go's method set decides between Go types (interop.md, Checking).
		return
	}
	pair := LegacyPair{Type: o.Obj, Pkg: o.Obj.Pkg.Path, Include: asInterface(o) != nil, Site: site}
	pair.Op = c.opAtOwnParams(n, opn)
	if pair.Include && (len(o.TParams) > 0 || !c.sameOps(asInterface(o), asInterface(opn))) {
		// An include replaces the including type's own operations with
		// the included ones: written only where they are the same.
		pair.Op = nil
	}
	legacyRecord(pair)
}

// sameOps reports whether every operation of op is an operation of k with
// the same shape, rows and takes.
func (c *checker) sameOps(k, op *Interface) bool {
	if k == nil || op == nil {
		return false
	}
	for _, m := range op.Methods {
		km := k.method(m.Name)
		if km == nil || km.Sig == nil || m.Sig == nil || !c.sameShape(km.Sig, m.Sig) ||
			km.Sig.Fallible != m.Sig.Fallible || km.Sig.TakesSelf != m.Sig.TakesSelf ||
			!km.Sig.Errors.equal(c.types, m.Sig.Errors) || !km.Sig.Uses.equal(c.types, m.Sig.Uses) {
			return false
		}
	}
	return true
}

// opAtOwnParams is op as n's origin implements it, at the origin's own type
// parameters: op itself when neither side is generic or only op's
// arguments are fixed, else the instance of op's origin that unifying the
// origin's functions with op's operations gives; nil when none does.
func (c *checker) opAtOwnParams(n *Named, op *Named) Type {
	o := n.origin()
	if len(o.TParams) == 0 {
		return op
	}
	opo := op.origin()
	if len(opo.TParams) == 0 {
		return op
	}
	args := make([]Type, len(o.TParams))
	for i, tp := range o.TParams {
		args[i] = tp
	}
	self := c.instantiate(o, args)
	in := &inferer{c: c, m: map[*TypeParam]Type{}, set: map[*TypeParam]bool{}}
	for _, tp := range opo.TParams {
		in.set[tp] = true
	}
	it, _ := opo.Underlying().(*Interface)
	if it == nil || !in.implements(it, self, 0) {
		return nil
	}
	var targs []Type
	for _, tp := range opo.TParams {
		if in.m[tp] == nil {
			return nil
		}
		targs = append(targs, in.m[tp])
	}
	return c.instantiate(opo, targs)
}

// recordLegacyReleases records the release pairs of the package under the
// old rule: a type whose function Close is of the release shape (R1 by
// name), and an operation type that lists a Close() of the contract's
// shape, where neither declares the contract.
func (c *checker) recordLegacyReleases() {
	if legacyRecord == nil || c.pkg.Path == std.PreludePath {
		return
	}
	cl := c.closer()
	if cl == nil {
		return
	}
	for _, tn := range c.pkg.Types {
		n, ok := tn.Type.(*Named)
		if !ok || tn.Decl == nil || c.implementsCloser(n) {
			continue
		}
		if it := asInterface(n); it != nil {
			if it.Set != SetAll {
				continue
			}
			if m := it.method("Close"); closeShape(m) && !m.Sig.Fallible && m.Sig.TakesSelf {
				legacyRecord(LegacyPair{Type: tn, Pkg: c.pkg.Path, Op: cl, Include: true, Site: "release"})
			}
			continue
		}
		if c.closeFunc(n) != nil {
			legacyRecord(LegacyPair{Type: tn, Pkg: c.pkg.Path, Op: cl, Site: "release"})
		}
	}
}

// String prints a pair for ergo fix's report.
func (p LegacyPair) String() string {
	op := "?"
	if p.Op != nil {
		op = p.Op.String()
	}
	verb := "implements"
	if p.Include {
		verb = "includes"
	}
	return fmt.Sprintf("%s.%s %s %s (%s)", p.Pkg, p.Type.Name, verb, op, p.Site)
}
