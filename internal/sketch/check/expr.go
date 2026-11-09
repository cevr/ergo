package check

import (
	"fmt"
	"go/constant"
	"regexp"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

type opMode int

const (
	modeInvalid opMode = iota // already reported: suppresses every diagnostic that follows (D3)
	modeNoValue
	modeValue
	modeConst
	modeType
	modeBuiltin
	modeCap     // a capability name: only the receiver of a method call
	modeCapFn   // a capability's method: only called
	modePkg     // a package name: only before a selector
	modeGeneric // a generic function not yet instantiated
	// modeExtern is a reference to an extern function, with or without
	// written type arguments: only the callee of a call (P3, extern.go).
	modeExtern
)

// operand is the result of checking an expression. errs is its error row
// while unhandled: a non-empty row makes the expression fallible (rows.md,
// Error rows 2).
type operand struct {
	mode opMode
	typ  Type
	val  constant.Value
	errs *Row
	obj  Object
	cap  Type // modeCap/modeCapFn: the capability
	expr syntax.Expr
	// addressable marks a variable, field or element: &x and assignment.
	addressable bool
	// mapElem marks a map index: assignable, as in Go, but not
	// addressable, so neither &m[k] nor m[k].f = v (M17).
	mapElem bool
	fn      *Func // the function an identifier names (main is S113 when called)
	recvVar *Var
	// self binds the type parameters of fn that x of a dot call x.f(…)
	// gives (196m, D-D8); the call records them in Info's TypeArgs.
	self map[*TypeParam]Type
	// method is the operation an operation-type or capability selector
	// names (Info.Calls).
	method *Method
}

func (o *operand) invalid() bool { return o.mode == modeInvalid }

func (o *operand) fallible() bool { return !o.errs.Empty() }

func bad(x syntax.Expr) operand { return operand{mode: modeInvalid, typ: invalid, expr: x} }

// record notes the type of an expression for later passes. A literal has
// its one type when it is checked (196u rule 7), so Info records each
// expression once, with no update pass.
func (c *checker) record(x syntax.Expr, op operand) {
	if c.silent == 0 && !isInvalid(op.typ) && op.mode != modeInvalid {
		c.pkg.Info.Types[x] = op.typ
		if op.mode == modeType {
			if c.typeNodes == nil {
				c.typeNodes = map[syntax.Expr]bool{}
			}
			c.typeNodes[x] = true
		}
		if op.mode == modeConst && op.val != nil {
			c.pkg.Info.Values[x] = op.val
		}
	}
}

// expr checks x with an optional expected type (bidirectional: composite
// literals, function literals and literals take it; literals.go lists the
// sites that pass one). The result may be fallible, a type or a package;
// positions decide what they accept.
func (c *checker) expr(x syntax.Expr, hint Type) operand {
	op := c.expr0(x, hint)
	op.expr = x
	c.record(x, op)
	return op
}

// valueExpr checks x where a value is needed: a fallible value is S105
// (rows.md, Error rows 3), a type, package or capability is an error.
func (c *checker) valueExpr(x syntax.Expr, hint Type) operand {
	op := c.expr(x, hint)
	c.needValue(&op)
	c.handled(&op)
	return op
}

// handled reports an unhandled failure in a value position (S105).
func (c *checker) handled(op *operand) {
	if op.invalid() || !op.fallible() {
		return
	}
	if c.fn != nil && c.fn.t != nil && c.fn.t.tries > 0 {
		// Under try, the failure escapes as try's own does; an unknown
		// row escapes with its invalid element.
		c.escape(op.expr, op.errs)
		op.errs = nil
		c.fn.t.tried = true
		return
	}
	// Only the known part is evidence of a failure; an unknown row also
	// makes the target's inferred row unknown, so nothing cascades from
	// the failure it may hold (Row.unknown).
	if known := op.errs.known(c.types); !known.Empty() && c.caught != nil && c.caught.arg == nil && c.silent == 0 && c.caught.isArg(op.expr) {
		// An argument of the call a catch catches: the catch reports it
		// once, knowing whether the call itself can fail (K4, G-ex-g-1).
		c.caught.arg, c.caught.row = op.expr, known
	} else if !known.Empty() {
		c.errorf(op.expr, CodeUnhandled, "the failure %s of this expression is not handled: use try, catch or return (rows.md, Error rows 3)", rowText(known))
	}
	if op.errs.unknown(c.types) {
		c.escape(op.expr, newRow().with(c.types, invalid))
	}
	op.errs = nil
}

// needValue requires a value: not a type, a package, a capability or no
// value.
func (c *checker) needValue(op *operand) {
	switch op.mode {
	case modeInvalid, modeValue, modeConst:
		return
	case modeNoValue:
		c.errorf(op.expr, CodeType, "this expression has no value")
	case modeType:
		// A record with no fields is written bare in value position, and
		// a record with fields keeps its braces (spec D-B2).
		if isZeroRecord(op.typ) {
			op.mode = modeValue
			return
		}
		if s, ok := under(op.typ).(*Struct); ok && len(s.Fields) > 0 && isRecordName(op.typ) {
			// A record whose construction authority is another package's
			// has no literal here either: name the way in (S114), not
			// braces (host-authority H1: fs.Host says use fs.NewHost).
			if obj := literalAuthority(op.typ, c.pkg); obj != nil {
				c.errorf(op.expr, CodeAuthority, "%s", authorityText(obj))
				break
			}
			name := exprText(op.expr)
			if name == "this expression" {
				name = op.typ.String()
			}
			c.errorf(op.expr, CodeType, "%s has fields; write %s{…} (types.md, Structs 2)", name, name)
			break
		}
		c.errorf(op.expr, CodeType, "%s is a type, not a value", op.typ)
	case modePkg:
		c.errorf(op.expr, CodeType, "package %s is not a value", exprText(op.expr))
	case modeCap:
		c.errorf(op.expr, CodeUnknown, "the capability %s is not a value: it denotes the provider the nearest with binds, and only its methods are called (types.md, Capabilities 2; README Gap 29)", op.cap)
	case modeCapFn:
		c.errorf(op.expr, CodeType, "a capability method is only called (types.md, Capabilities 2)")
	case modeBuiltin:
		c.errorf(op.expr, CodeType, "%s is only called", exprText(op.expr))
	case modeExtern:
		// G1: an extern whose Go body has no hidden argument is a function
		// value; the lowering wraps it in the uniform shape. Any other
		// names the literal to pass (K4).
		if externValue(op.fn) {
			op.mode = modeValue
			return
		}
		c.errorf(op.expr, CodeType, "%s", externValueText(*op))
	case modeGeneric:
		if ea, ok := op.obj.(*explicitArgs); ok {
			sig := op.typ.(*Signature)
			if len(ea.targs) == len(sig.TParams) {
				// Every parameter is written: Id[int] is a function value
				// (Go's explicit instantiation; M10).
				if c.instantiateValue(op, sig, ea.targs) {
					return
				}
				break
			}
			c.errorf(op.expr, CodeType, "%s leaves type parameters unwritten, which only a call infers; write every type argument, call it or write a function literal", exprText(op.expr))
			break
		}
		c.errorf(op.expr, CodeType, "the generic function %s is only called or instantiated", exprText(op.expr))
	}
	op.mode, op.typ, op.errs = modeInvalid, invalid, nil
}

// paramMap maps each type parameter to its argument.
func paramMap(tps []*TypeParam, targs []Type) map[*TypeParam]Type {
	m := make(map[*TypeParam]Type, len(tps))
	for i, tp := range tps {
		if i < len(targs) && targs[i] != nil {
			m[tp] = targs[i]
		}
	}
	return m
}

// instantiateValue turns a fully instantiated generic function into a
// function value: each type argument satisfies its constraint (S108), and
// the value's type is the signature with the arguments substituted.
func (c *checker) instantiateValue(op *operand, sig *Signature, targs []Type) bool {
	m := paramMap(sig.TParams, targs)
	for i, tp := range sig.TParams {
		if tp.Kind != KindType || tp.Constraint == nil {
			continue
		}
		cons := c.subst(tp.Constraint, m)
		if why := c.satisfies(targs[i], cons); why != "" {
			c.errorf(op.expr, CodeType, "%s does not satisfy the constraint %s of %s: %s (types.md, Generics 2)", targs[i], cons, tp.Name, why)
			return false
		}
	}
	name := exprText(op.expr)
	if op.fn != nil {
		name = op.fn.Name
	}
	if !c.typeArgsAdmitted(name, sig.TParams, targs, op.expr) {
		return false
	}
	if op.fn != nil {
		c.recordInstance(unparen(op.expr), op.fn, targs)
	}
	inst := c.subst(sig, m).(*Signature)
	inst.TParams = nil
	inst.NBracket = 0
	op.mode, op.typ, op.obj = modeValue, inst, nil
	// The expression and its parentheses record the instantiated type, not
	// the generic one recorded while they were checked (D1; round-2 M8).
	for x := op.expr; x != nil; {
		c.record(x, *op)
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			break
		}
		x = p.X
	}
	return true
}

func exprText(x syntax.Expr) string {
	switch x := x.(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.SelectorExpr:
		return exprText(x.X) + "." + x.Sel.Name
	case *syntax.ParenExpr:
		return exprText(x.X)
	}
	return "this expression"
}

func (c *checker) expr0(x syntax.Expr, hint Type) operand {
	switch x := x.(type) {
	case *syntax.Ident:
		return c.boolLiteral(c.ident(x), hint)
	case *syntax.BasicLit:
		return c.literal(x, hint)
	case *syntax.ParenExpr:
		// Parentheses keep what the operand denotes: (x) = 1 assigns x,
		// as in Go (N4). A generic function is the exception: Go reads
		// the inside of parentheses as a value, so (Fn)[int] and (Fn)(1)
		// are errors there and (Fn[int]) is the instance (round 5).
		op := c.expr(x.X, hint)
		if op.mode == modeGeneric {
			c.needValue(&op)
		}
		return op
	case *syntax.CompositeLit:
		return c.compositeLit(x, hint)
	case *syntax.FuncLit:
		sig, _ := under0(hint).(*Signature)
		return c.funcLit(x, sig, nil)
	case *syntax.SelectorExpr:
		return c.selector(x)
	case *syntax.IndexExpr:
		return c.index(x, hint)
	case *syntax.SliceExpr:
		return c.sliceExpr(x)
	case *syntax.CallExpr:
		return c.call(x, hint)
	case *syntax.StarExpr:
		op := c.valueExpr(x.X, nil)
		if op.invalid() {
			return op
		}
		p, ok := op.typ.(*Pointer)
		if !ok {
			c.errorf(x, CodeType, "cannot indirect %s", op.typ)
			return bad(x)
		}
		return operand{mode: modeValue, typ: p.Elem, addressable: true}
	case *syntax.UnaryExpr:
		return c.unary(x, hint)
	case *syntax.BinaryExpr:
		return c.binary(x, hint)
	case *syntax.TryExpr:
		return c.try(x, hint)
	case *syntax.CatchExpr:
		return c.catch(x, hint, c.useOf(x) != useDiscard, false)
	case *syntax.ContextLit:
		// A context literal outside its places (grammar.md, Expressions 8;
		// rows.md, Context 9): after `let x =` in a body, as a with operand,
		// or in a package-level let, which are checked there.
		c.errorf(x, CodeType, "%s", contextOutsideText)
		return bad(x)
	case *syntax.KeyValueExpr:
		c.errorf(x, CodeType, "a key: value pair belongs in a composite literal")
		return bad(x)
	case *syntax.TupleExpr:
		return c.tupleValue(x, hint)
	case *syntax.TypeGuard:
		c.errorf(x, CodeType, "x.(type) is not an expression; write match x { case T: … }")
		return bad(x)
	case *syntax.ArrayType, *syntax.MapType, *syntax.FuncType, *syntax.InterfaceType:
		t := c.typExpr(x, c.localTypeCtx())
		if isInvalid(t) {
			return bad(x)
		}
		return operand{mode: modeType, typ: t}
	case *syntax.ErrorRow, *syntax.MachineType, *syntax.StructType:
		c.errorf(x, CodeType, "unexpected type form in an expression")
		return bad(x)
	case *syntax.InterpolatedString:
		return c.interpolated(x)
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return c.branches(x, hint)
	case *syntax.AsPattern, *syntax.VariantSpec:
		return c.undecided(x, "a pattern or a variant in value position")
	}
	return c.undecided(x, "")
}

// interpolated checks `"a \(x) b"` (grammar.md, Lexical 5; D-E6; 196s):
// each part is a value with no expected type, so a literal takes its
// default type, and a fallible part is S105 unless it is tried. A part
// has a text form, Go's %v text of its value, exactly when its type is a
// basic type or a type defined over one (textForm). A part with no text
// form gets one diagnostic at the part, and the whole is still a string,
// so nothing cascades from it. An invalid part adds nothing.
func (c *checker) interpolated(x *syntax.InterpolatedString) operand {
	ok := true
	for _, e := range x.Exprs {
		op := c.valueExpr(e, nil)
		t := c.typeOf(&op)
		if op.invalid() || isInvalid(t) {
			ok = false
			continue
		}
		c.textForm(e, t)
	}
	if !ok {
		return bad(x)
	}
	return operand{mode: modeValue, typ: Typ[String]}
}

// textForm reports an interpolated part e of type t that has no text form
// in v1 (D-E6, Q5). The rule reads t's underlying type, which unwraps
// every level of definition (`type A Celsius` is float64):
//
//   - a basic type has a text form;
//   - a record, a union or a variant, a tuple, a slice or a Map (so
//     Option[T]) is data whose text form #49 derives: S109;
//   - a type parameter has none in v1, whatever its constraint, since a
//     text needs a per-instance function and v1 boxes nothing: S109 #49;
//   - any other type (a function, an operation type such as Task, a
//     pointer, a machine) never has one: S108.
//
// Prior art: MoonBit's "\{x}" requires Show, which records derive; Elixir's
// "#{x}" fails at run time for a struct with no String.Chars; Ergo
// refuses at check time.
func (c *checker) textForm(e syntax.Expr, t Type) {
	switch u := under(t).(type) {
	case *Basic:
		return
	case *Struct, *Union, *Tuple, *Slice, *Map:
		c.errorf(e, CodeOutside, "a value of type %s has no text form yet; interpolate one of its fields, or wait for derived text forms (#49)", c.localTypeText(t))
	case *TypeParam:
		c.errorf(e, CodeOutside, "a value of type %s has no text form yet: a type parameter has no text form in v1; interpolate a value of a basic type, or wait for derived text forms (#49)", c.localTypeText(u))
	default:
		c.errorf(e, CodeType, "a value of type %s has no text form; interpolate a value of a basic type or of a type defined over one (grammar.md, Strings 5)", c.localTypeText(t))
	}
}

func under0(t Type) Type {
	if t == nil {
		return nil
	}
	return under(t)
}

// localTypeCtx is the type context of a type written inside a body: the
// type parameters the body is generic in are in scope (funcCtx.typeParams).
func (c *checker) localTypeCtx() *typeCtx {
	ctx := &typeCtx{params: map[string]*TypeParam{}}
	for _, tp := range c.fn.typeParams() {
		ctx.params[tp.Name] = tp
	}
	if c.fn != nil {
		ctx.scope = c.fn.scope
	}
	return ctx
}

// lookup resolves a name in expression position, innermost frame first:
// locals and the body's frame, the package, the universe and the prelude,
// then the file's import names (D4). An import named after a prelude name
// is refused (D-D3) and still binds, so it wins over the prelude name it
// reuses.
func (c *checker) lookup(name string) Object { return c.lookupWith(name, (*Scope).lookup) }

// lookupWith is lookup with find as the scope search: Scope.lookup, or
// scopeLookupPast, which sees through refused binders.
func (c *checker) lookupWith(name string, find func(*Scope, string) (Object, *Scope)) Object {
	sc := c.pkg.Scope
	if c.fn != nil && c.fn.scope != nil {
		sc = c.fn.scope
	}
	if o, at := find(sc, name); o != nil {
		if imp := c.shadowImport(name, at, c.cur); imp != nil {
			return imp
		}
		return o
	}
	if c.cur != nil {
		if o, ok := c.cur.scope.names[name]; ok {
			return o
		}
	}
	return nil
}

func (c *checker) ident(x *syntax.Ident) operand {
	if x.Name == "_" {
		c.errorf(x, CodeType, "_ is not a value")
		return bad(x)
	}
	o := c.lookup(x.Name)
	if o == nil {
		if c.pkg.Contexts[x.Name] != nil {
			// A named context: only a with operand or an embedding (rows.md,
			// Context 9).
			return c.contextValue(x)
		}
		if form, ok := removedBuiltins[x.Name]; ok {
			c.errorf(x, CodeUnknown, "%s is not predeclared in Ergo; %s", x.Name, form)
			return bad(x)
		}
		if f := c.preludeOp(x.Name); f != nil {
			c.errorf(x, CodeUnknown, "unknown name %s; the prelude function %s is reached only by the dot form, %s", x.Name, x.Name, opForm(f))
			return bad(x)
		}
		c.errorf(x, CodeUnknown, "unknown name %s", x.Name)
		return bad(x)
	}
	c.recordUse(x, o)
	switch o := o.(type) {
	case *Var:
		o.used = true
		c.readLocal(o)
		if o.group != nil {
			o.group.used = true
		}
		if o.pkg {
			c.recordDep(o)
		}
		if o.pkg && o.Type == nil {
			c.ensurePkgVar(o)
		}
		if o.Type == nil || isInvalid(o.Type) {
			return bad(x)
		}
		return operand{mode: modeValue, typ: o.Type, addressable: !o.pkg, recvVar: o}
	case *Func:
		if c.isEntry(o) {
			// Only the host calls main, so it is never a value: an alias
			// or an argument would let ordinary code call it (D10; M15).
			c.errorf(x, CodeEntry, "main is called only by the host, so it is not a value (rows.md, Capability rows 4)")
			return bad(x)
		}
		if o.overloaded {
			c.errorf(x, CodeType, "%s", overloadedText(o.Name))
			return bad(x)
		}
		sig := c.funcSig(o)
		c.recordDep(o)
		if c.fn != nil && c.fn.reach != nil {
			c.fn.reach.funcs = append(c.fn.reach.funcs, o)
		}
		refused, unknown := c.groupRef(o)
		if refused {
			return bad(x)
		}
		sig = c.unknownSig(sig, unknown)
		if isExtern(o) {
			return externRef(o, sig)
		}
		if len(sig.TParams) > 0 {
			return operand{mode: modeGeneric, typ: sig, fn: o}
		}
		return operand{mode: modeValue, typ: sig, fn: o}
	case *TypeName:
		t := c.resolvedType(o.Type)
		if isInvalid(t) {
			return bad(x)
		}
		if o == universeOnce.errorName {
			return operand{mode: modeType, typ: invalid, obj: o}
		}
		if isCapability(o.Type) {
			return operand{mode: modeCap, typ: o.Type, cap: o.Type, obj: o}
		}
		return operand{mode: modeType, typ: o.Type, obj: o}
	case *capBinding:
		if isInvalid(c.resolvedType(o.tn.Type)) {
			return bad(x)
		}
		return operand{mode: modeCap, typ: o.tn.Type, cap: o.tn.Type, obj: o.tn}
	case *Const:
		v := constant.MakeBool(o.Val)
		return operand{mode: modeConst, typ: o.Type, val: v}
	case *Builtin:
		return operand{mode: modeBuiltin, obj: o, typ: invalid}
	case *PkgName:
		o.used = true
		if o.broken {
			return bad(x)
		}
		return operand{mode: modePkg, obj: o, typ: invalid}
	case *Context:
		// A local context: only a with operand or an embedding (rows.md,
		// Context 9).
		return c.contextValue(x)
	case *Method:
		// Only a law's operation scope holds an operation, and a call of
		// one is checked before its callee (lawOpCall): every other use is
		// a value (laws.go, rule 4).
		c.errorf(x, CodeType, "an operation of %s is called inside a law; it is not a value", c.fn.law.Owner.Name)
		return bad(x)
	}
	c.errorf(x, CodeUnknown, "unknown name %s", x.Name)
	return bad(x)
}

func (c *checker) ensurePkgVar(v *Var) {
	for _, pv := range c.vars {
		if pv.obj == v {
			c.checkPkgVar(pv)
			return
		}
	}
}

func fitsInt64(v constant.Value) bool {
	_, exact := constant.Int64Val(constant.ToInt(v))
	return exact
}

// ---------------------------------------------------------------- composite literals

// orphanElements types the element values of a composite literal whose
// type is unknown (reported, or invalid for another reason), as orphans
// (orphanValues; stage D1 fuzz triage, `with 0 { with context { A: A{read} }
// {} }`). A key is a field name of an unknown type and stays unchecked.
// lt is the literal's type as the reference walk reads it (reflits.go),
// whatever typing made of it: an elided literal among the values takes its
// type from lt (orphanValue).
func (c *checker) orphanElements(x *syntax.CompositeLit, mark int, lt litType) {
	if x.Base != nil {
		c.orphan(x.Base, mark) // a record update's base is a value the walk reads
	}
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			e = kv.Value
		}
		c.orphanValue(e, mark, lt, false)
	}
}

// orphanValue types v, an element, key (key) or value of a composite
// literal of type lt as the walk reads it, as an orphan. An elided literal
// takes its type from lt as the walk does (elemOf): where lt is a record,
// the value is a field, which never elides its type, so nothing in it is
// typed and the walk only visits it (rows.md, Inference 6). Typing so
// records a name inside a refused literal exactly when the walk takes it
// (FuzzCheck d1665805d2fed622, 353ea9dccd4807ec).
func (c *checker) orphanValue(v syntax.Expr, mark int, lt litType, key bool) {
	cl, ok := v.(*syntax.CompositeLit)
	if !ok || cl.Type != nil {
		c.orphan(v, mark)
		return
	}
	et := c.elemOf(lt, key)
	if et.kind == litInvalid {
		c.markReassignedIn(cl)
		return
	}
	c.orphanElements(cl, mark, et)
}

// elementValue checks v, an element or key (key) of an array, slice or map
// literal of type lt as the walk reads it, against its type want. An elided
// literal there learns the type the walk gives it (elemOf), which its
// refusal routes follow (compositeLit) where typing's own type is invalid.
// It is read only there, so a literal typing accepts costs no step more.
func (c *checker) elementValue(v syntax.Expr, want Type, lt func() litType, key bool) operand {
	if cl, ok := v.(*syntax.CompositeLit); ok && cl.Type == nil {
		c.elidedLit = cl
		c.elidedAs = func() litType { return c.elemOf(lt(), key) }
	}
	return c.valueExpr(v, want)
}

func (c *checker) compositeLit(x *syntax.CompositeLit, hint Type) operand {
	// x's type as the reference walk reads it: its written type, or the type
	// the enclosing literal gives it (elementValue). Each refusal route
	// follows it, so typing and the walk agree on the names inside.
	var elided func() litType
	if x.Type == nil && c.elidedLit == x {
		elided = c.elidedAs
	}
	c.elidedLit, c.elidedAs = nil, nil
	var walkLt *litType
	lt := func() litType {
		if walkLt == nil {
			l := c.litTypeOfLit(x, elided)
			walkLt = &l
		}
		return *walkLt
	}
	mark := c.undiagSeq
	if x.Base != nil {
		return c.recordUpdate(x, hint, mark, lt)
	}
	var t Type
	switch {
	case x.Type == nil:
		if hint == nil || isInvalid(hint) {
			if hint == nil {
				c.errorf(x, CodeType, "a composite literal without a type needs one from its context")
			} else {
				// The invalid hint may come from an undiagnosed read
				// earlier in the statement.
				mark = c.stmtMark
			}
			c.orphanElements(x, mark, lt())
			return bad(x)
		}
		t = hint
	default:
		t = c.literalType(x, hint)
		if t == nil {
			// A refused written type, or a generic record whose type
			// arguments failed: the values are orphans, and a record's are
			// fields, so an elided literal among them is not typed
			// (orphanValue).
			c.orphanElements(x, mark, lt())
			return bad(x)
		}
	}
	if isInvalid(t) {
		c.orphanElements(x, mark, lt())
		return bad(x)
	}
	if isZeroRecord(t) && len(x.Elts) == 0 {
		// One form per job (spec D-B2): a record with no fields is a value
		// by its name alone.
		name := c.localTypeText(t)
		if x.Type != nil {
			name = exprText(x.Type)
			if name == "this expression" {
				name = c.localTypeText(t)
			}
		}
		c.errorf(x, CodeType, "a record with no fields is written bare: %s (types.md, Structs 2)", name)
		return operand{mode: modeValue, typ: t}
	}
	switch u := under(t).(type) {
	case *Struct:
		if !c.structLit(x, t, u, mark) {
			return operand{mode: modeValue, typ: t}
		}
	case *Slice:
		c.elements(x, u.Elem, lt)
	case *Map:
		// A constant key appears once, compared after conversion to the
		// key type, as in Go (N3).
		type constKey struct {
			typ int32 // the interned id of the key's dynamic type
			val string
		}
		seen := map[constKey]bool{}
		for _, e := range x.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			if !ok {
				c.errorf(e, CodeType, "a map literal's elements are key: value pairs")
				c.orphanValue(e, mark, lt(), false)
				continue
			}
			k := c.elementValue(kv.Key, u.Key, lt, true)
			if c.assign(&k, u.Key, "map key") && k.mode == modeConst && k.val != nil {
				// Keys of an interface type compare by dynamic type and
				// value: int(1) and int64(1) are distinct (round-2 M7).
				dyn := u.Key
				if asInterface(u.Key) != nil {
					dyn = k.typ
				}
				key := constKey{c.types.id(dyn), k.val.Kind().String() + ":" + k.val.ExactString()}
				if seen[key] {
					c.errorf(kv.Key, CodeType, "the key %s appears twice in this map literal", k.val.ExactString())
				}
				seen[key] = true
			}
			v := c.elementValue(kv.Value, u.Elem, lt, false)
			c.assign(&v, u.Elem, "map value")
		}
	case *MachineType:
		if len(x.Elts) > 0 {
			c.errorf(x, CodeType, "a machine value has no fields")
			c.orphanElements(x, mark, lt())
		}
	default:
		c.errorf(x, CodeType, "cannot write a composite literal of %s", t)
		c.orphanElements(x, mark, lt())
		return bad(x)
	}
	return operand{mode: modeValue, typ: t}
}

// literalType resolves a composite literal's written type. A generic type
// written without type arguments takes them from the expected type (a union
// that lists one of its instances, or an instance), else from its field
// values (D6: Some{n}, Enter{x}). It is nil when the type is refused.
func (c *checker) literalType(x *syntax.CompositeLit, hint Type) Type {
	raw := c.typExpr0(x.Type, c.localTypeCtx())
	if isInvalid(raw) {
		return nil
	}
	n, ok := raw.(*Named)
	if !ok || n.Orig != nil || len(n.TParams) == 0 {
		if tp, ok := raw.(*TypeParam); ok && tp.Kind != KindType {
			c.errorf(x.Type, CodeType, "%s is %s parameter, not a type", tp.Name, tp.Kind)
			return nil
		}
		return raw
	}
	if inst := instanceFromHint(n, hint); inst != nil {
		return inst
	}
	// Infer from the field values.
	s, ok := n.underlying().(*Struct)
	if !ok {
		c.errorf(x.Type, CodeType, "generic type %s needs type arguments", n)
		return nil
	}
	m := map[*TypeParam]Type{}
	set := map[*TypeParam]bool{}
	for _, tp := range n.TParams {
		set[tp] = true
	}
	inf := &inferer{c: c, m: m, set: set}
	// Typed field values bind first; a parameter only literals bind takes
	// their common default, and mixed defaults are S108, as for a call
	// (196u rule 6).
	byLiteral := map[*TypeParam]Type{}
	for _, lits := range []bool{false, true} {
		for i, e := range x.Elts {
			var ft Type
			val := e
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				id, _ := kv.Key.(*syntax.Ident)
				if id == nil {
					continue
				}
				f, _ := s.field(id.Name)
				if f == nil {
					continue
				}
				ft, val = f.Type, kv.Value
			} else if i < len(s.Fields) {
				ft = s.Fields[i].Type
			}
			if ft == nil || c.isLiteral(val) != lits {
				continue
			}
			if cl, ok := val.(*syntax.CompositeLit); ok && cl.Type == nil {
				// A field never elides its type (fieldValue refuses it): it
				// binds nothing and is not typed.
				continue
			}
			c.silent++
			op := c.expr(val, nil)
			c.silent--
			if op.mode == modeType && isZeroRecord(op.typ) {
				op.mode = modeValue // a bare record with no fields is a value (D-B2)
			}
			if op.invalid() || op.mode == modeType {
				continue
			}
			if tp, isParam := ft.(*TypeParam); lits && isParam && set[tp] {
				if prev, seen := byLiteral[tp]; seen && !c.identical(prev, op.typ) {
					c.errorf(x.Type, CodeType, "cannot infer the type argument %s of %s: its literals take %s and %s; write %s[…] (D6)", tp.Name, n.Obj.Name, prev, op.typ, n.Obj.Name)
					return nil
				}
				if m[tp] == nil {
					byLiteral[tp] = op.typ
				}
			}
			_ = inf.unify(ft, op.typ, unifyAssign) // a mismatch is the field assignment's error
		}
	}
	targs := make([]Type, len(n.TParams))
	for i, tp := range n.TParams {
		b := m[tp]
		if b == nil {
			if tp.Kind != KindType {
				b = &RowArg{Row: newRow()}
			} else {
				c.errorf(x.Type, CodeType, "cannot infer the type argument %s of %s: write %s[…] (D6)", tp.Name, n.Obj.Name, n.Obj.Name)
				return nil
			}
		}
		targs[i] = b
	}
	// Inferred arguments meet the same constraints as written ones (D6; M5).
	for i, tp := range n.TParams {
		if tp.Kind == KindType && tp.Constraint != nil {
			// A constraint may name other parameters, I[T]: it is checked
			// with every argument substituted (round-2 M6).
			cons := c.subst(tp.Constraint, paramMap(n.TParams, targs))
			if why := c.satisfies(targs[i], cons); why != "" {
				c.errorf(x.Type, CodeType, "the inferred type argument %s of %s does not satisfy %s: %s (types.md, Generics 2)", targs[i], n.Obj.Name, cons, why)
				return nil
			}
		}
	}
	if !c.typeArgsAdmitted(n.Obj.Name, n.TParams, targs, x) {
		return nil
	}
	return c.instantiate(n, targs)
}

// instanceFromHint finds the instance of a generic origin that an expected
// type names: the hint itself, or the variant of a union hint.
func instanceFromHint(orig *Named, hint Type) Type {
	h, ok := hint.(*Named)
	if !ok {
		return nil
	}
	if h.origin() == orig && h.Orig != nil {
		return h
	}
	if u := asUnion(h); u != nil {
		for _, v := range u.Variants {
			if vn, ok := v.(*Named); ok && vn.origin() == orig {
				return vn
			}
		}
	}
	return nil
}

// structLit checks a struct literal: all keyed or all positional; a
// positional literal lists every field; a keyed one names declared fields
// once and sets every field that has no zero value (types.md, Structs 2;
// Zero values 2). A value it skips is an orphan (orphan), as every literal
// refusal route makes it, so its callback frames are recorded (round 3
// review M4).
func (c *checker) structLit(x *syntax.CompositeLit, t Type, s *Struct, mark int) bool {
	ok := true
	// A literal or record update of a record with another package's
	// unexported field builds a new value of it: S114 (types.md, Zero
	// values 6). Its values are still typed.
	denied := false
	if obj := literalAuthority(t, c.pkg); obj != nil {
		c.errorf(x, CodeAuthority, "%s", c.literalAuthorityText(t, obj, x.Base != nil))
		denied = true
	}
	if len(x.Elts) > 0 {
		if _, keyed := x.Elts[0].(*syntax.KeyValueExpr); !keyed {
			if len(x.Elts) != len(s.Fields) {
				c.errorf(x, CodeType, "a positional literal of %s lists all %d fields, not %d (types.md, Structs 2)", t, len(s.Fields), len(x.Elts))
				for i, e := range x.Elts {
					if i < len(s.Fields) {
						op := c.fieldValue(e, s.Fields[i].Type)
						c.assign(&op, s.Fields[i].Type, "field "+s.Fields[i].Name)
					} else {
						c.strayValue(e, mark, true)
					}
				}
				return false
			}
			for i, e := range x.Elts {
				f := s.Fields[i]
				op := c.fieldValue(e, f.Type)
				c.assign(&op, f.Type, "field "+f.Name)
			}
			return !denied
		}
	}
	set := map[string]bool{}
	for _, e := range x.Elts {
		kv, isKV := e.(*syntax.KeyValueExpr)
		if !isKV {
			c.strayValue(e, mark, false)
			continue
		}
		id, isID := kv.Key.(*syntax.Ident)
		if !isID {
			c.errorf(kv.Key, CodeType, "a struct literal's key is a field name")
			c.strayValue(kv.Value, mark, true)
			ok = false
			continue
		}
		f, _ := s.field(id.Name)
		if f == nil {
			c.errorf(id, CodeUnknown, "%s has no field %s", t, id.Name)
			c.strayValue(kv.Value, mark, true)
			ok = false
			continue
		}
		if c.hiddenField(s, id.Name) {
			c.errorf(id, CodeUnknown, "%s (grammar.md, Visibility 2)", hiddenFieldText(s, id.Name))
			c.strayValue(kv.Value, mark, true)
			ok = false
			continue
		}
		if set[id.Name] {
			c.errorf(id, CodeType, "field %s is set twice", id.Name)
			c.strayValue(kv.Value, mark, false)
			ok = false
			continue
		}
		set[id.Name] = true
		op := c.fieldValue(kv.Value, f.Type)
		c.assign(&op, f.Type, "field "+f.Name)
	}
	if !ok || denied {
		return false
	}
	if x.Base != nil {
		return true // a record update takes every field it omits from its base
	}
	var missing []string
	var held *TypeName // the authority an omitted field's zero needs
	var heldField string
	for _, f := range s.Fields {
		if set[f.Name] || isInvalid(f.Type) {
			continue
		}
		if !defaultable(f.Type) {
			missing = append(missing, f.Name)
		} else if a := zeroAuthority(f.Type, c.pkg); a != nil && held == nil {
			held, heldField = a, f.Name
		}
	}
	if len(missing) > 0 {
		c.errorf(x, CodeType, "%s must set %s: a %s has no zero value (types.md, Zero values 2)", t, strings.Join(missing, ", "), kindName(s, missing[0]))
		return false
	}
	if held != nil {
		c.errorf(x, CodeAuthority, "%s must set %s: its zero value builds a new value of %s, and %s (types.md, Zero values 6)", c.localTypeText(t), heldField, qualifiedName(held), authorityText(held))
		return false
	}
	return true
}

func kindName(s *Struct, field string) string {
	f, _ := s.field(field)
	if f == nil {
		return "field"
	}
	switch under(f.Type).(type) {
	case *Union:
		return "union"
	case *Interface:
		return "interface value"
	case *Signature:
		return "function value"
	case *Struct:
		return "struct of non-defaultable fields"
	}
	return f.Type.String()
}

// strayValue checks a value that a struct literal's refusal route skips (an
// extra positional value, an unknown field, a key that is not a name, a
// field set twice). A struct literal's value never elides its type, so an
// elided literal there is refused untyped, as fieldValue refuses one, and
// nothing in it is a reference: the reference walk only visits it (rows.md,
// Inference 6, round 18; reflits.go compositeLit; FuzzCheck input
// ae4371d31e8dd0ae). The let mut locals it reassigns count as reassigned,
// so no false S110 joins the refusal (markReassignedIn). report says
// whether the value's own errors show (valueExpr) or it is an orphan of an
// error already reported (orphan).
func (c *checker) strayValue(v syntax.Expr, mark int, report bool) {
	if cl, ok := v.(*syntax.CompositeLit); ok && cl.Type == nil {
		if report {
			c.errorf(cl, CodeType, "a composite literal without a type needs one from its context")
		}
		c.markReassignedIn(cl)
		return
	}
	if report {
		c.valueExpr(v, nil)
		return
	}
	c.orphan(v, mark)
}

// fieldValue checks a struct literal's field value against the field type
// ft. A composite literal there writes its type: as in Go, a type is
// elided only for the elements of an array, slice or map literal and for
// a map literal's keys (Go spec, Composite literals; round 17 C1). Its
// let mut reassignments still count (markReassignedIn).
func (c *checker) fieldValue(v syntax.Expr, ft Type) operand {
	if cl, ok := v.(*syntax.CompositeLit); ok && cl.Type == nil {
		text := "{…}"
		if src := c.cur.syntax.Src; int(cl.End()) <= len(src) {
			if s := string(src[cl.Pos():cl.End()]); len(s) <= 40 && !strings.Contains(s, "\n") {
				text = s
			}
		}
		c.errorf(cl, CodeType, "missing type in composite literal; write %s%s", c.localTypeText(ft), text)
		c.markReassignedIn(cl)
		return bad(cl)
	}
	return c.valueExpr(v, ft)
}

// localTypeText writes t as the source of this package spells it: without
// the package's own qualifier. It runs only for a diagnostic.
func (c *checker) localTypeText(t Type) string {
	re := regexp.MustCompile(`(^|[^\pL\pN_])` + regexp.QuoteMeta(c.pkg.Name) + `\.`)
	return re.ReplaceAllString(t.String(), "$1")
}

// elements checks the elements of an array or slice literal x of element
// type elem; lt is x's type as the walk reads it (elementValue).
func (c *checker) elements(x *syntax.CompositeLit, elem Type, lt func() litType) {
	for _, e := range x.Elts {
		val := e
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			k := c.elementValue(kv.Key, Typ[Int], lt, true)
			c.assign(&k, Typ[Int], "index")
			val = kv.Value
		}
		op := c.elementValue(val, elem, lt, false)
		c.assign(&op, elem, "element")
	}
}

// ---------------------------------------------------------------- selectors

func (c *checker) selector(x *syntax.SelectorExpr) operand {
	called := c.callee == x
	index := c.calleeIndex
	c.callee, c.calleeIndex = nil, nil
	if !called {
		index = nil
	}
	if id, ok := x.X.(*syntax.Ident); ok && id.Name == "go" {
		if o := c.lookup("go"); o != nil {
			if pn, ok := o.(*PkgName); ok && pn.Pkg == universeOnce.goPkg {
				return c.qualified(x, pn)
			}
		}
	}
	base := c.expr(x.X, nil)
	if c.selfRes != nil && base.mode != modeInvalid {
		// Typing reached the call: it resolves to a 196m function
		// (selfCall) or to another member, which takes no edge.
		if _, ok := c.selfRes[x]; !ok {
			c.selfRes[x] = nil
		}
	}
	switch base.mode {
	case modeInvalid:
		return bad(x)
	case modePkg:
		return c.qualified(x, base.obj.(*PkgName))
	case modeCap:
		it := asInterface(base.cap)
		m := it.method(x.Sel.Name)
		if m == nil {
			c.errorf(x.Sel, CodeUnknown, "the capability %s has no method %s", base.cap, x.Sel.Name)
			return bad(x)
		}
		if text := c.hiddenOperation(it, m.Name); text != "" {
			c.errorf(x.Sel, CodeUnknown, "%s (grammar.md, Visibility 3)", text)
			return bad(x)
		}
		if c.fn != nil && c.fn.reach != nil {
			c.fn.reach.capCalls = append(c.fn.reach.capCalls, capCall{cap: base.cap.(*Named), name: m.Name})
		}
		return operand{mode: modeCapFn, typ: m.Sig, cap: base.cap, method: m}
	case modeType:
		if !isZeroRecord(base.typ) {
			// os.Host.Args() names a record of another package's
			// authority as a value: say how to get one, as needValue does.
			if obj := literalAuthority(base.typ, c.pkg); obj != nil && isRecordName(base.typ) {
				c.errorf(base.expr, CodeAuthority, "%s", authorityText(obj))
				return bad(x)
			}
			c.errorf(x, CodeType, "%s.%s: a method expression is not part of the language; call the method on a value", base.typ, x.Sel.Name)
			return bad(x)
		}
		// A record with no fields is its own value (D-B2): R.M() calls M
		// on R; there are no method expressions to confuse it with.
	}
	c.needValue(&base)
	c.handled(&base)
	if base.invalid() {
		return bad(x)
	}
	t := base.typ
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	handle := false
	if e := refElem(t); e != nil {
		// go.Ref[T] reaches T's fields and methods, as Go's *T does.
		t, handle = e, true
	}
	if isHostNamed(t) {
		return c.hostSelector(x, base, t.(*Named), handle)
	}
	if isInvalid(under(t)) {
		// A refused declared type (`type MyQ queue.Queue[int]`) was
		// reported at its declaration; its uses add nothing (D3).
		return bad(x)
	}
	if fn := c.collFunc(base.typ, x.Sel.Name); fn != nil {
		return c.selfCall(x, base, c.selfSig(base.typ, fn), fn, called, index)
	}
	if s := asStruct(t); s != nil {
		if f, _ := s.field(x.Sel.Name); f != nil {
			if c.hiddenField(s, x.Sel.Name) {
				c.errorf(x.Sel, CodeUnknown, "%s (grammar.md, Visibility 2)", hiddenFieldText(s, x.Sel.Name))
				return bad(x)
			}
			return operand{mode: modeValue, typ: f.Type, addressable: base.addressable || isPointer(base.typ)}
		}
	}
	sig, fn := c.methodOf(t, x.Sel.Name)
	if fn != nil && fn.owner != nil {
		return c.selfCall(x, base, sig, fn, called, index)
	}
	if sig != nil {
		if fn != nil {
			c.recordDep(fn)
		}
		if c.fn != nil && c.fn.reach != nil {
			if fn != nil {
				c.fn.reach.funcs = append(c.fn.reach.funcs, fn)
			} else {
				c.fn.reach.methodNames = append(c.fn.reach.methodNames, x.Sel.Name)
			}
		}
		var m *Method
		if it := asInterface(t); it != nil {
			m = it.method(x.Sel.Name)
			if text := c.hiddenOperation(it, x.Sel.Name); m != nil && text != "" {
				c.errorf(x.Sel, CodeUnknown, "%s (grammar.md, Visibility 3)", text)
				return bad(x)
			}
		}
		if m != nil && isPointer(base.typ) {
			// An operation type's value is a reference already (types.md,
			// Operation types); a pointer to one adds a second (stage D3b,
			// Rule 5), so its operations are called on the value.
			c.errorf(x, CodeType, "an operation type is already a reference; call it on the value")
			return bad(x)
		}
		if tp, ok := t.(*TypeParam); ok && m == nil {
			// An operation of a type parameter's method-set constraint: the
			// lowering calls it through the function's witness (D-E1).
			if it := asInterface(tp.Constraint); it != nil {
				m = it.method(x.Sel.Name)
				if text := c.hiddenOperation(it, x.Sel.Name); m != nil && text != "" {
					c.errorf(x.Sel, CodeUnknown, "%s (grammar.md, Visibility 3)", text)
					return bad(x)
				}
			}
		}
		return operand{mode: modeValue, typ: sig, method: m}
	}
	if u := under(t); u != t && c.collFunc(u, x.Sel.Name) != nil {
		// D-D8: the first parameter's type is x's type exactly.
		// The base is a slice or a Map, unnamed, so the value reaches it by
		// assignment (clause 2), under a name not bound here.
		raw := c.freshName("raw")
		c.errorf(x.Sel, CodeUnknown, "%s has no field or method %s; the prelude function %s is on %s exactly; a defined type reaches it after assignment: let %s %s = %s; then %s.%s(…)", base.typ, x.Sel.Name, x.Sel.Name, u, raw, c.localTypeText(u), exprText(x.X), raw, x.Sel.Name)
		return bad(x)
	}
	if hint := c.noMethodHint(t, x, called); hint != "" {
		c.errorf(x.Sel, CodeUnknown, "%s has no field or method %s; %s", base.typ, x.Sel.Name, hint)
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			// The call the hint names may write the value (a *T first
			// parameter): no S110 "never reassigned" follows (K4, G-ex-f-5).
			if v, ok := c.lookup(id.Name).(*Var); ok && v.mut {
				v.reassigned = true
			}
		}
		return bad(x)
	}
	c.errorf(x.Sel, CodeUnknown, "%s has no field or method %s", base.typ, x.Sel.Name)
	return bad(x)
}

// noMethodHint names the rule a dot call x.f(…) misses when the package
// declares a function f on x's type that rule 1 of types.md Methods does
// not reach (K4, G-ex-e-2, G-ex-f-5), with the plain call to write: f's
// first parameter is an operation type, whose values reach only their
// operations; or it is one instance of a generic type, where a dot call
// needs the type's parameters. It is "" when no such function exists.
func (c *checker) noMethodHint(t Type, x *syntax.SelectorExpr, called bool) string {
	n, ok := t.(*Named)
	if !ok || !called || n.Obj == nil || n.Obj.Pkg == nil {
		return ""
	}
	recv := exprText(x.X)
	if recv == "this expression" {
		recv = "x"
	}
	for _, f := range n.Obj.Pkg.Funcs {
		if f.Name != x.Sel.Name || f.Sig == nil || len(f.Sig.Params) == 0 || (f.Pkg != c.pkg && !isExported(f.Name)) {
			continue
		}
		first := f.Sig.Params[0].Type
		ptr := ""
		if p, ok := first.(*Pointer); ok {
			first, ptr = p.Elem, "&"
		}
		fn, ok := first.(*Named)
		if !ok || fn.origin() != n.origin() {
			continue
		}
		rest := ""
		if len(f.Sig.Params) > 1 {
			rest = ", …"
		}
		// Another package's function is called through its package, and
		// only its own package can rewrite it.
		callee, foreign := f.Name, f.Pkg != c.pkg
		if foreign {
			callee = f.Pkg.Name + "." + f.Name
		}
		switch {
		case asInterface(n) != nil && c.identical(fn, n):
			return fmt.Sprintf("%s is an operation type, so a dot call on its value reaches only its operations, and %s is a function on it: call %s(%s%s) (types.md, Methods 1)", c.localTypeText(n), callee, callee, recv, rest)
		case n.Orig != nil && c.identical(fn, n):
			star := ""
			if ptr != "" {
				star = "*"
			}
			var tps []string
			for _, tp := range n.Orig.TParams {
				tps = append(tps, tp.Name)
			}
			list := strings.Join(tps, ", ")
			param := f.Sig.Params[0].Name
			if param == "" || param == "_" {
				param = "x"
			}
			if foreign {
				return fmt.Sprintf("%s takes %s%s, one instance, and a dot call reaches a function on a generic type only when its first parameter takes the function's own type parameters: call %s(%s%s%s) (types.md, Methods 1)", callee, star, c.localTypeText(fn), callee, ptr, recv, rest)
			}
			return fmt.Sprintf("%s takes %s%s, one instance, and a dot call reaches a function on a generic type only when its first parameter takes the function's own type parameters: write func %s[%s](%s %s%s[%s]), or call %s(%s%s%s) (types.md, Methods 1)", f.Name, star, c.localTypeText(fn), f.Name, list, param, star, n.Obj.Name, list, f.Name, ptr, recv, rest)
		}
	}
	return ""
}

// selfCall checks x.f where f is a package function on x's type (196m): it
// is reached only as the callee of x.f(…) (rule 4), and only when f is
// visible here. The call then checks the remaining arguments against f's
// signature without its first parameter (rule 1); x's type arguments bind
// the type parameters that parameter names, an instantiation of f.
func (c *checker) selfCall(x *syntax.SelectorExpr, base operand, sig *Signature, fn *Func, called bool, index *syntax.IndexExpr) operand {
	if fn.Pkg != c.pkg && !isExported(fn.Name) {
		c.errorf(x.Sel, CodeUnknown, "%s has no field or method %s", base.typ, x.Sel.Name)
		return bad(x)
	}
	if !called {
		c.errorf(x.Sel, CodeUnknown, "%s has no field %s; the function %s on its type is reached only by a call %s.%s(…)", base.typ, x.Sel.Name, fn.Name, exprText(x.X), fn.Name)
		return bad(x)
	}
	if index != nil && sig.NBracket == 0 {
		// x.f[i](…): f has no bracket type parameter x does not bind, so
		// the brackets are no type arguments and x.f is not called; a
		// function on a type has no method value (types.md, Methods 2;
		// round 8 review m1).
		src := c.cur.syntax.Src
		idx := src[index.Lbrack+1 : index.Rbrack]
		c.errorf(x.Sel, CodeUnknown, "%s has no field %s; the function %s on its type is reached only by a call %s.%s(…): write %s.%s()[%s]", base.typ, x.Sel.Name, fn.Name, exprText(x.X), fn.Name, exprText(x.X), fn.Name, idx)
		return bad(x)
	}
	if fn.Ptr && !isPointer(base.typ) && !base.addressable {
		c.errorf(x.X, CodeType, "%s takes *%s, and %s is not addressable", fn.Name, fn.owner.Obj.Name, exprText(x.X))
		return bad(x)
	}
	if fn.Ptr && !isPointer(base.typ) {
		c.borrowRoot(x.X, x.X, fn.Name) // D-D1: the implicit borrow could change x
	}
	if c.monoBad(fn.Sig.TParams) {
		return bad(x)
	}
	if c.selfRes != nil {
		c.selfRes[x] = fn
	}
	if c.selfFinal != nil && c.silent == 0 {
		c.selfFinal[x] = fn
	}
	c.recordDep(fn)
	if c.fn != nil && c.fn.reach != nil {
		c.fn.reach.funcs = append(c.fn.reach.funcs, fn)
	}
	refused, unknown := c.groupRef(fn)
	if refused {
		return bad(x)
	}
	sig = c.unknownSig(sig, unknown)
	t := base.typ
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	var self map[*TypeParam]Type
	if len(fn.selfArgs) > 0 {
		self = selfBinding(t, fn)
		var tps []*TypeParam
		var targs []Type
		for _, tp := range fn.Sig.TParams {
			if b, ok := self[tp]; ok {
				tps, targs = append(tps, tp), append(targs, b)
			}
		}
		if !c.typeArgsAdmitted(fn.Name, tps, targs, x) {
			return bad(x)
		}
	}
	if len(sig.TParams) > 0 {
		return operand{mode: modeGeneric, typ: sig, fn: fn, self: self}
	}
	return operand{mode: modeValue, typ: sig, fn: fn, self: self}
}

// overloadedText is the S108 of a plain reference to a name several package
// functions share (196m rule 3).
func overloadedText(name string) string {
	return "several functions are named " + name + "; call it as x." + name + "(…)"
}

func isPointer(t Type) bool {
	_, ok := t.(*Pointer)
	return ok
}

// qualified resolves pkg.Name: only exported declarations (grammar.md,
// Visibility 2).
func (c *checker) qualified(x *syntax.SelectorExpr, pn *PkgName) operand {
	pn.used = true
	if pn.broken {
		return bad(x)
	}
	o, ok := pn.Pkg.member(x.Sel.Name)
	if !ok || !isExported(x.Sel.Name) {
		c.unknownMember(x, pn)
		return bad(x)
	}
	switch o := o.(type) {
	case *TypeName:
		if isInvalid(c.resolvedType(o.Type)) {
			return bad(x)
		}
		if isCapability(o.Type) {
			return operand{mode: modeCap, typ: o.Type, cap: o.Type, obj: o}
		}
		return operand{mode: modeType, typ: o.Type, obj: o}
	case *Func:
		if o.overloaded {
			c.errorf(x, CodeType, "%s", overloadedText(o.Name))
			return bad(x)
		}
		if isExtern(o) {
			return externRef(o, o.Sig)
		}
		if len(o.Sig.TParams) > 0 {
			return operand{mode: modeGeneric, typ: o.Sig, fn: o}
		}
		return operand{mode: modeValue, typ: o.Sig, fn: o}
	case *Var:
		return operand{mode: modeValue, typ: o.Type}
	}
	c.errorf(x, CodeUnknown, "%s.%s is not a value or a type", pn.Name, x.Sel.Name)
	return bad(x)
}

// ---------------------------------------------------------------- index and slice

func (c *checker) index(x *syntax.IndexExpr, hint Type) operand {
	mark := c.undiagSeq
	base := c.expr(x.X, nil)
	switch base.mode {
	case modeInvalid:
		// The indices may be type arguments of a refused generic callee,
		// so they are typed silently: their names resolve as the
		// reference walk resolves them, and nothing cascades (D3). The
		// frames among them are recorded first, outside the silence, so
		// a refused-by-class-G frame is recorded in the reporting pass.
		c.skipOrphanFrames(x.Indices, mark)
		c.silent++
		c.orphanValues(x.Indices, mark)
		c.silent--
		return bad(x)
	case modeGeneric:
		return c.explicit(x, base)
	case modeExtern:
		// F[int] is still the extern's reference; (F)[int] reads (F) as a
		// value first, as for any generic function (round 5), and a
		// non-generic extern takes no type arguments.
		if len(base.typ.(*Signature).TParams) > 0 && unparen(x.X) == x.X {
			return c.explicit(x, base)
		}
	case modeType:
		t := c.typExpr(x, c.localTypeCtx())
		if isInvalid(t) {
			return bad(x)
		}
		return operand{mode: modeType, typ: t}
	}
	c.needValue(&base)
	c.handled(&base)
	if base.invalid() {
		c.orphanValues(x.Indices, mark)
		return bad(x)
	}
	if len(x.Indices) != 1 {
		c.errorf(x, CodeType, "one index expected")
		c.orphanValues(x.Indices, mark)
		return bad(x)
	}
	t := base.typ
	if r, ok := unparen(x.Indices[0]).(*syntax.BinaryExpr); ok && r.Op == syntax.DOTDOT && (isSliceType(t) || isString(t)) {
		// words[0..n]: one diagnostic at the index, and no mismatch of the
		// element type where the part was meant (K4, cron's ParseTable).
		c.orphanValues(x.Indices, mark)
		c.errorf(x.Indices[0], CodeType, "%s is indexed by an integer, not a range: Ergo has no sub-slice expression; build the part with a loop over the range", c.localTypeText(base.typ))
		return bad(x)
	}
	switch u := under(t).(type) {
	case *Pointer:
		// Go indexes through a pointer to an array only; Ergo has no
		// arrays, so an index never sees through a pointer (as len and
		// for do not, D-D4 and D-D5).
		c.errorf(x, CodeType, "an index does not see through a pointer: write (*p)[i]")
		c.orphanValues(x.Indices, mark)
		return bad(x)
	case *Map:
		k := c.valueExpr(x.Indices[0], u.Key)
		c.assign(&k, u.Key, "map key")
		if c.writeTarget != x {
			// A read has no value for a missing key: not every type has a
			// zero value (parse-v1 ruling 7), so m.Get(k) returns an
			// Option (stdlib.md; D2 design ruling).
			c.errorf(x, CodeType, "a Map read may miss; write m.Get(k), which returns an Option")
			return bad(x)
		}
		return operand{mode: modeValue, typ: u.Elem, mapElem: true}
	case *Slice:
		c.intIndex(x.Indices[0])
		return operand{mode: modeValue, typ: u.Elem, addressable: true}
	case *Basic:
		if isString(t) {
			if i, ok := c.intIndex(x.Indices[0]); ok {
				if n, known := constLength(base); known && i >= n {
					c.errorf(x.Indices[0], CodeType, "index %d is out of bounds of a string of length %d", i, n)
				}
			}
			return operand{mode: modeValue, typ: Typ[Uint8]}
		}
	}
	c.errorf(x, CodeType, "cannot index %s", base.typ)
	c.orphanValues(x.Indices, mark)
	return bad(x)
}

// isSliceType reports a slice type, named or not.
func isSliceType(t Type) bool {
	_, ok := under(t).(*Slice)
	return ok
}

// intIndex checks an index or a slice bound: an integer, where a literal
// takes int (D-A2), and a constant one is a nonnegative int (S108), as in
// Go. It returns the constant's value when there is one.
func (c *checker) intIndex(x syntax.Expr) (int64, bool) {
	op := c.valueExpr(x, Typ[Int])
	if op.invalid() {
		return 0, false
	}
	if !isInteger(op.typ) {
		c.errorf(x, CodeType, "an index must be an integer, not %s", op.typ)
		return 0, false
	}
	if op.mode != modeConst || op.val == nil {
		return 0, false
	}
	// A typed integer constant of any integer type indexes when its value
	// is representable as an int (Go's rule; round-2 M10).
	v, exact := constant.Int64Val(constant.ToInt(op.val))
	if !exact {
		c.errorf(x, CodeType, "the index %s overflows int", op.val.ExactString())
		return 0, false
	}
	if v < 0 {
		c.errorf(x, CodeType, "the index %d is negative", v)
		return 0, false
	}
	return v, true
}

// constLength returns the length of a constant string operand.
func constLength(op operand) (int64, bool) {
	if op.mode != modeConst || op.val == nil || op.val.Kind() != constant.String {
		return 0, false
	}
	return int64(len(constant.StringVal(op.val))), true
}

func (c *checker) sliceExpr(x *syntax.SliceExpr) operand {
	base := c.valueExpr(x.X, nil)
	// The constant bounds: ordered low <= high <= max, and within the
	// length of a constant string (S108; Go's rule).
	limit, limited := int64(0), false
	if !base.invalid() {
		if n, ok := constLength(base); ok {
			limit, limited = n, true
		}
	}
	last, haveLast := int64(0), false
	for _, e := range []syntax.Expr{x.Low, x.High, x.Max} {
		if e == nil {
			continue
		}
		v, ok := c.intIndex(e)
		if !ok {
			continue
		}
		switch {
		case limited && v > limit:
			c.errorf(e, CodeType, "the slice bound %d is out of bounds of length %d", v, limit)
		case haveLast && v < last:
			c.errorf(e, CodeType, "the slice bounds are out of order: %d is less than %d", v, last)
		}
		last, haveLast = v, true
	}
	if base.invalid() {
		return bad(x)
	}
	switch under(base.typ).(type) {
	case *Slice:
		return operand{mode: modeValue, typ: base.typ}
	case *Basic:
		if isString(base.typ) {
			if x.Slice3 {
				c.errorf(x, CodeType, "a 3-index slice of a string")
				return bad(x)
			}
			return operand{mode: modeValue, typ: base.typ}
		}
	}
	c.errorf(x, CodeType, "cannot slice %s", base.typ)
	return bad(x)
}

// ---------------------------------------------------------------- assignment

// assign checks that op may be assigned to t. A literal already has its
// one type (literals.go), so a literal that could not take t, and a name
// of another basic type, are S108 with the form to write (196u rules 1 and
// 3): no value converts by itself.
func (c *checker) assign(op *operand, t Type, what string) bool {
	if op.invalid() || t == nil || containsInvalid(t) || containsInvalid(op.typ) {
		// Already reported where the invalid part came from (D3).
		if op.expr != nil && t != nil && containsInvalid(t) {
			if c.intoInvalid == nil {
				c.intoInvalid = map[syntax.Expr]bool{}
			}
			c.intoInvalid[op.expr] = true
		}
		return true
	}
	if op.mode == modeNoValue {
		c.errorf(op.expr, CodeType, "this expression has no value; %s needs one", what)
		op.mode = modeInvalid
		return false
	}
	a, why := c.assignability(op.typ, t)
	if c.sealedRefused(op.expr, a) {
		op.mode = modeInvalid
		return false
	}
	if !a.ok() {
		repair := ""
		if what == "return" || what == "the body's tail value" {
			repair = c.tupleReturnRepair(op.expr, op.typ, t)
		}
		switch msg := literalMismatch(op.expr, t); {
		case msg != "":
			c.errorf(op.expr, CodeType, "%s", msg)
		case repair != "":
			c.errorf(op.expr, CodeType, "cannot use %s as %s in %s; write %s", op.typ, t, what, repair)
		case why != "":
			c.errorf(op.expr, CodeType, "cannot use %s as %s in %s: %s", op.typ, t, what, why)
		default:
			// A conversion assignability does not admit has both sides
			// named (clause 2 admits the rest), so t has a name to call,
			// written as this package spells it, so the repair checks.
			if id, isName := unparen(op.expr).(*syntax.Ident); isName && c.convertible(op.typ, t) {
				if _, isVar := c.lookup(id.Name).(*Var); isVar {
					local := c.localTypeText(t)
					c.errorf(op.expr, CodeType, "%s is %s; write %s(%s) or declare %s %s", id.Name, op.typ, local, id.Name, id.Name, local)
					break
				}
			}
			c.errorf(op.expr, CodeType, "cannot use %s as %s in %s", op.typ, t, what)
		}
		op.mode = modeInvalid
		return false
	}
	c.implicit(op.expr, a)
	return true
}

// A tuple call cannot convert its parts as a whole. Destructuring and
// returning a tuple literal gives each part the expected result type.
// Offer this repair only when both element assignments are valid. The
// repair is source on one line, the two statements joined by the ";" a
// newline implies, so it checks when pasted in place of the body.
func (c *checker) tupleReturnRepair(x syntax.Expr, v, t Type) string {
	if _, ok := unparen(x).(*syntax.CallExpr); !ok || c.cur == nil {
		return ""
	}
	vt, vok := under(v).(*Tuple)
	tt, tok := under(t).(*Tuple)
	if !vok || !tok || len(vt.Types) != 2 || len(tt.Types) != 2 {
		return ""
	}
	for i := range vt.Types {
		if a, _ := c.assignability(vt.Types[i], tt.Types[i]); !a.ok() {
			return ""
		}
	}
	src := c.cur.syntax.Src
	if x.Pos() < 0 || x.End() > syntax.Pos(len(src)) {
		return ""
	}
	// A call written over more than one line gets no repair: the message is
	// one line, and the text is copied byte for byte, never re-spaced
	// (stage D3a rewrite r3 review M1; the formatter owns printing).
	call := src[x.Pos():x.End()]
	if strings.ContainsAny(call, "\r\n") {
		return ""
	}
	m, n := c.freshName("m"), c.freshName("n")
	names := m + ", " + n
	return "let (" + names + ") = " + call + "; return (" + names + ")"
}

// typeOf is the type a declaration without a written type takes from op:
// its one type, since every literal has one (196u rule 2).
func (c *checker) typeOf(op *operand) Type {
	if op.invalid() {
		return invalid
	}
	if op.mode == modeNoValue {
		c.errorf(op.expr, CodeType, "this expression has no value")
		return invalid
	}
	return op.typ
}

// tupleValue checks a tuple value `(a, b, …)` (196t): one value of the
// structural tuple type of its elements. An expected tuple type, or a
// defined type over one, gives each element its expected type (D-A2).
func (c *checker) tupleValue(x *syntax.TupleExpr, hint Type) operand {
	var hints []Type
	if ht, ok := under0(hint).(*Tuple); ok && len(ht.Types) == len(x.Elts) {
		hints = ht.Types
	}
	tup := &Tuple{Types: make([]Type, len(x.Elts))}
	ok := true
	for i, e := range x.Elts {
		var h Type
		if hints != nil {
			h = hints[i]
		}
		op := c.valueExpr(e, h)
		t := c.typeOf(&op)
		if isInvalid(t) {
			ok = false
			continue
		}
		if h != nil {
			if !c.assign(&op, h, "tuple element") {
				ok = false
			}
			t = h
		}
		tup.Types[i] = t
	}
	if !ok {
		return bad(x)
	}
	return operand{mode: modeValue, typ: tup}
}
