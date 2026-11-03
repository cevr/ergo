package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Expressions. expr writes the statements an expression needs and returns
// its Go value. Evaluation order (spec D4): operands are evaluated once,
// left to right; when a later operand needs statements of its own (a call,
// try, catch, with), every earlier operand that is not a constant is bound
// to a temporary first, so Go's unspecified order between a variable read
// and a call never decides a result. && and || evaluate their right operand
// only when needed.

// operand is a lowered expression: its Go value ("" when it has none) and,
// for an untried fallible expression, the name of its error variable.
type operand struct {
	val, err string
}

func unparen(x syntax.Expr) syntax.Expr {
	for {
		p, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

// typeOf is x's checked type.
func (fc *funcCtx) typeOf(x syntax.Expr) check.Type {
	t, ok := fc.typed(x)
	if !ok {
		ice(x, "an expression with no Types entry")
	}
	return t
}

// typed is x's checked type, and whether x has one: a package name or a
// type has none.
func (fc *funcCtx) typed(x syntax.Expr) (check.Type, bool) {
	if t, ok := fc.l.info.Types[x]; ok {
		return t, true
	}
	if p, isParen := x.(*syntax.ParenExpr); isParen {
		return fc.typed(p.X)
	}
	return nil, false
}

// noValue reports an expression with no value.
func (fc *funcCtx) noValue(x syntax.Expr) bool {
	return fc.typeOf(x).String() == "no value"
}

// fallibleExpr reports an expression that can fail and is not tried: a call
// whose error row at the site is not empty, or a with whose block ends in
// one (rows.md, Error rows 2).
func (fc *funcCtx) fallibleExpr(x syntax.Expr) bool {
	x = unparen(x)
	switch s := x.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		// An if or match in value position passes a fallible branch
		// tail's failure on, as a with block does (review O2).
		return fc.tailFallible(s.(syntax.Stmt))
	}
	call, ok := x.(*syntax.CallExpr)
	if !ok {
		return false
	}
	c := fc.l.info.Calls[call]
	if c == nil {
		return false
	}
	// A with's Fails is the row its block's tails pass through.
	return failsHere(c)
}

// emits reports whether lowering x writes statements: whether x holds a
// call, try, catch, with, if, match or method value (which binds its
// receiver, methodValue) outside a function literal. A constant never does.
func (fc *funcCtx) emits(x syntax.Expr) bool {
	if _, ok := fc.l.info.Values[x]; ok {
		return false
	}
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.TryExpr, *syntax.CatchExpr, *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
			found = true
		case *syntax.CallExpr:
			if c := fc.l.info.Calls[n]; c == nil || (c.Kind != check.CallConversion && c.Kind != check.CallLen) {
				found = true
			}
		case *syntax.SelectorExpr:
			if _, ok := fc.l.info.Values[n]; ok {
				return false
			}
			if fc.methodOf(n) != nil {
				found = true
			}
		case syntax.Expr:
			if _, ok := fc.l.info.Values[n]; ok {
				return false
			}
		}
		return true
	})
	return found
}

// spill binds a value to a temporary, unless it is a constant.
func (fc *funcCtx) spill(val string) string {
	if val == "" || isLiteralText(val) {
		return val
	}
	t := fc.temp("t")
	fc.line(t + " := " + val)
	return t
}

// isLiteralText reports a constant's Go text, which a spill need not bind.
func isLiteralText(s string) bool {
	return strings.HasPrefix(s, `"`) || s == "true" || s == "false"
}

// operands lowers a list left to right, spilling an operand when a later
// one writes statements.
func (fc *funcCtx) operands(list []syntax.Expr, convert bool) []string {
	later := make([]bool, len(list)+1)
	for i := len(list) - 1; i >= 0; i-- {
		later[i] = later[i+1] || fc.emits(list[i])
	}
	out := make([]string, len(list))
	for i, x := range list {
		if convert {
			out[i] = fc.assigned(x)
		} else {
			out[i] = fc.value(x)
		}
		if later[i+1] && !fc.isConst(x) {
			out[i] = fc.spill(out[i])
		}
	}
	return out
}

func (fc *funcCtx) isConst(x syntax.Expr) bool {
	_, ok := fc.l.info.Values[x]
	return ok
}

// value lowers x, which cannot fail, to its Go value.
func (fc *funcCtx) value(x syntax.Expr) string {
	o := fc.expr(x)
	if o.err != "" {
		if fc.tries == 0 {
			ice(x, "an untried fallible expression in a value position")
		}
		fc.propagate(o.err)
	}
	return o.val
}

// assigned lowers x at an assignment site: its value with the implicit
// conversion Info records there (Info.Implicit).
func (fc *funcCtx) assigned(x syntax.Expr) string {
	return fc.convertAt(x, fc.value(x))
}

// convertAt applies the implicit conversion at x to its lowered value. A
// variant needs none: Go assigns a struct to the sealed interface its marker
// method satisfies. An operation table becomes the provider value itself
// when its functions are the type's Go methods, else an adapter.
func (fc *funcCtx) convertAt(x syntax.Expr, val string) string {
	conv, ok := fc.l.info.Implicit[x]
	if !ok {
		return val
	}
	return fc.convert(conv, val, x)
}

// elemAt applies the implicit conversion of element i of x, a value that a
// multi-name let takes apart, to that element's lowered value
// (Info.ImplicitElems).
func (fc *funcCtx) elemAt(x syntax.Expr, i int, val string) string {
	convs, ok := fc.l.info.ImplicitElems[x]
	if !ok {
		return val
	}
	if i >= len(convs) {
		ice(x, "element %d of a value with %d element conversions", i, len(convs))
	}
	return fc.convert(convs[i], val, x)
}

func (fc *funcCtx) convert(conv check.Conversion, val string, site syntax.Node) string {
	switch conv.Kind {
	case 0, check.ConvVariant:
		return val
	case check.ConvOperations:
		return fc.operations(conv, val, site)
	case check.ConvRow:
		// The row value holds one of the union's error variants, which
		// implement its sealed interface (D-C3).
		return val + ".(" + fc.goType(conv.To) + ")"
	}
	ice(site, "an unknown implicit conversion")
	return ""
}

// expr lowers x.
func (fc *funcCtx) expr(x syntax.Expr) operand {
	if v, ok := fc.l.info.Values[x]; ok {
		return operand{val: fc.constant(fc.typeOf(x), v, x)}
	}
	if val, ok := fc.bareRecord(x); ok {
		return operand{val: val}
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return fc.expr(x.X)
	case *syntax.Ident:
		return operand{val: fc.ident(x)}
	case *syntax.BasicLit:
		ice(x, "a literal with no Values entry")
	case *syntax.SelectorExpr:
		return operand{val: fc.selector(x)}
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.QUO, syntax.REM, syntax.SHL, syntax.SHR:
			return operand{val: fc.atSite(x.OpPos, fc.binary(x))}
		}
		return operand{val: fc.binary(x)}
	case *syntax.UnaryExpr:
		return operand{val: fc.unary(x)}
	case *syntax.StarExpr:
		return operand{val: fc.atSite(x.Pos(), "(*"+fc.value(x.X)+")")}
	case *syntax.CompositeLit:
		return operand{val: fc.composite(x, fc.typeOf(x))}
	case *syntax.IndexExpr:
		return operand{val: fc.atSite(x.Pos(), fc.index(x))}
	case *syntax.SliceExpr:
		return operand{val: fc.atSite(x.Pos(), fc.sliceExpr(x))}
	case *syntax.CallExpr:
		return fc.call(x)
	case *syntax.TryExpr:
		return fc.try(x)
	case *syntax.CatchExpr:
		return fc.catch(x, false)
	case *syntax.FuncLit:
		return operand{val: fc.funcLit(x.Type, x.Body.List, x)}
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return fc.branchExpr(x)
	case *syntax.InterpolatedString:
		return operand{val: fc.interpolated(x)}
	case *syntax.ContextLit:
		refuse(x, "an inline wire value is lowered after check-v1 stage E (196x)")
	case *syntax.TupleExpr:
		vals := fc.operands(x.Elts, true)
		parts := make([]string, len(vals))
		for i, v := range vals {
			parts[i] = tupleField(i) + ": " + v
		}
		return operand{val: fc.goType(fc.typeOf(x)) + "{" + strings.Join(parts, ", ") + "}"}
	case *syntax.MachineType:
		refuse(x, "machine plan values are lowered in lower-v1 stage 4 (spec D5)")
	}
	refuse(x, "this expression is not lowered yet")
	return operand{}
}

// bareRecord reads the TypeName use of a bare record value. Its checked
// type holds any type arguments; those arguments are never value indices.
func (fc *funcCtx) bareRecord(x syntax.Expr) (string, bool) {
	head := x
	if ix, ok := head.(*syntax.IndexExpr); ok {
		head = unparen(ix.X)
	}
	var tn *check.TypeName
	switch head := head.(type) {
	case *syntax.Ident:
		tn, _ = fc.l.info.Uses[head].(*check.TypeName)
	case *syntax.SelectorExpr:
		id, ok := unparen(head.X).(*syntax.Ident)
		if !ok {
			return "", false
		}
		pn, ok := fc.l.info.Uses[id].(*check.PkgName)
		named, isNamed := fc.typeOf(x).(*check.Named)
		if !ok || !isNamed || named.Obj.Pkg != pn.Pkg || named.Obj.Name != head.Sel.Name {
			return "", false
		}
		// A qualified expression records its package use and final type;
		// the Named's object is the selected TypeName.
		tn = named.Obj
	default:
		return "", false
	}
	if tn == nil {
		return "", false
	}
	td := fc.l.typeDecl(tn, x)
	if td.kind != kindRecord || len(td.fields) != 0 {
		ice(x, "a bare value of %s is not a record with no fields", tn.Name)
	}
	return fc.goType(fc.typeOf(x)) + "{}", true
}

// ident lowers a name: a local, a package let or a function value.
func (fc *funcCtx) ident(id *syntax.Ident) string {
	switch o := fc.l.info.Uses[id].(type) {
	case *check.Var:
		if name, ok := fc.locals[o]; ok {
			return name
		}
		if name, ok := fc.l.vars[o]; ok {
			return name
		}
		ice(id, "the variable %s has no Go name", id.Name)
	case *check.Func:
		return fc.funcValue(o, fc.typeOf(id), id, nil)
	case *check.Const:
		if o.Val {
			return "true"
		}
		return "false"
	case nil:
		ice(id, "the identifier %s has no Uses entry", id.Name)
	}
	refuse(id, "%s is not a value that lowering supports yet", id.Name)
	return ""
}

// selector lowers x.f: a record's field, or a package's declaration.
func (fc *funcCtx) selector(x *syntax.SelectorExpr) string {
	if id, ok := x.X.(*syntax.Ident); ok {
		if pn, isPkg := fc.l.info.Uses[id].(*check.PkgName); isPkg {
			for _, o := range pn.Pkg.Exported() {
				if f, ok := o.(*check.Func); ok && f.Name == x.Sel.Name {
					return fc.funcValue(f, fc.typeOf(x), x, nil)
				}
			}
			refuse(x, "%s.%s is not a value that lowering supports yet", id.Name, x.Sel.Name)
		}
	}
	if op := fc.methodOf(x); op != nil {
		return fc.methodValue(x, op)
	}
	recv := fc.value(x.X)
	return recv + "." + fc.fieldName(x)
}

// methodOf is the operation x takes as a value (a method value, lowered by
// methodValue), or nil: a field, or a package's declaration. selector and
// emits share it, so the operand order sees every method value.
func (fc *funcCtx) methodOf(x *syntax.SelectorExpr) *check.Method {
	t, ok := fc.typed(x.X)
	if !ok {
		return nil
	}
	return operation(t, x.Sel.Name)
}

// operation is the operation name of the operation type t, or of the
// method-set constraint of the type parameter t or *t, or nil when t has no
// operation of that name (a field).
func operation(t check.Type, name string) *check.Method {
	if p, ok := t.(*check.Pointer); ok {
		if _, isParam := p.Elem.(*check.TypeParam); isParam {
			t = p.Elem
		}
	}
	var it *check.Interface
	switch t := t.(type) {
	case *check.Named:
		it, _ = t.Underlying().(*check.Interface)
	case *check.TypeParam:
		it = constraintOf(t)
	}
	if it == nil {
		return nil
	}
	for _, m := range it.Methods {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// fieldName is the Go name of the field x selects.
func (fc *funcCtx) fieldName(x *syntax.SelectorExpr) string {
	t := fc.typeOf(x.X)
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*check.Named)
	if !ok {
		ice(x, "a field selection on %s", t)
	}
	td := fc.l.typeDecl(n.Obj, x)
	name, ok := td.fieldName[x.Sel.Name]
	if !ok {
		ice(x.Sel, "%s has no field %s", n, x.Sel.Name)
	}
	return name
}

// binary lowers a binary operator; && and || evaluate their right operand
// only when the left one does not decide.
func (fc *funcCtx) binary(x *syntax.BinaryExpr) string {
	op := x.Op.String()
	if (op == "&&" || op == "||") && fc.emits(x.Y) {
		result := fc.temp("b")
		fc.line(result + " := " + fc.value(x.X))
		if op == "&&" {
			fc.line("if " + result + " {")
		} else {
			fc.line("if !" + result + " {")
		}
		fc.depth++
		fc.line(result + " = " + fc.value(x.Y))
		fc.depth--
		fc.line("}")
		return result
	}
	if op == ".." {
		return fc.rangeValue(x)
	}
	parts := fc.operands([]syntax.Expr{x.X, x.Y}, false)
	return "(" + parts[0] + " " + op + " " + parts[1] + ")"
}

// rangeValue lowers a..b, which the checker types as the prelude's
// Range[T]: a literal of its lowered record, Start then End.
func (fc *funcCtx) rangeValue(x *syntax.BinaryExpr) string {
	t := fc.typeOf(x)
	n, ok := t.(*check.Named)
	if !ok || !isPreludeType(t, "Range") {
		ice(x, "a range typed %s, not the prelude's Range", t)
	}
	td := fc.l.typeDecl(n.Obj, x)
	parts := fc.operands([]syntax.Expr{x.X, x.Y}, true)
	return fc.goType(t) + "{" + td.fieldName["Start"] + ": " + parts[0] + ", " + td.fieldName["End"] + ": " + parts[1] + "}"
}

// unary lowers -x, +x, !x, ^x and &x.
func (fc *funcCtx) unary(x *syntax.UnaryExpr) string {
	op := x.Op.String()
	switch op {
	case "-", "+", "!", "^", "&":
		return "(" + op + fc.value(x.X) + ")"
	}
	refuse(x, "the operator %s is not produced by the decided parser", op)
	return ""
}

// composite lowers a record, slice or Map literal of type t. A record's
// fields are always keyed, in source order. A literal of a defined type
// over a slice or Map type (`Ints{1, 2}`) is that slice or Map literal
// written with the defined type's Go name, as Go writes it.
func (fc *funcCtx) composite(x *syntax.CompositeLit, t check.Type) string {
	goType := fc.goType(t)
	shape := t
	if n, ok := t.(*check.Named); ok {
		switch u := n.Underlying().(type) {
		case *check.Slice, *check.Map:
			shape = u
		}
	}
	switch u := shape.(type) {
	case *check.Named:
		td := fc.l.typeDecl(u.Obj, x)
		if td.kind != kindRecord && (td.kind != kindDefined || td.fieldName == nil) {
			refuse(x, "a literal of %s is not a record literal", t)
		}
		var keys []string
		var values []syntax.Expr
		set := map[string]bool{}
		for i, e := range x.Elts {
			if kv, ok := e.(*syntax.KeyValueExpr); ok {
				id, ok := kv.Key.(*syntax.Ident)
				if !ok {
					ice(kv.Key, "a record literal key that is not a field name")
				}
				name, ok := td.fieldName[id.Name]
				if !ok {
					ice(id, "%s has no field %s", t, id.Name)
				}
				keys = append(keys, name)
				values = append(values, kv.Value)
				set[id.Name] = true
				continue
			}
			if i >= len(td.fields) {
				ice(e, "a positional record literal with more values than fields")
			}
			keys = append(keys, td.fieldName[td.fields[i].Name])
			values = append(values, e)
			set[td.fields[i].Name] = true
		}
		vals := fc.operands(values, true)
		parts := make([]string, len(keys))
		for i := range keys {
			parts[i] = keys[i] + ": " + vals[i]
		}
		// An omitted field takes its default: a Map field is an empty Map.
		if s, ok := u.Underlying().(*check.Struct); ok {
			parts = append(parts, fc.fieldDefaults(td, s, set)...)
		}
		return goType + "{" + strings.Join(parts, ", ") + "}"
	case *check.Slice:
		var values []syntax.Expr
		for _, e := range x.Elts {
			if _, ok := e.(*syntax.KeyValueExpr); ok {
				refuse(e, "an indexed slice literal is not lowered yet")
			}
			values = append(values, e)
		}
		vals := fc.elements(values, u.Elem)
		return goType + "{" + strings.Join(vals, ", ") + "}"
	case *check.Map:
		var list []syntax.Expr
		for _, e := range x.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			if !ok {
				ice(e, "a Map literal element without a key")
			}
			list = append(list, kv.Key, kv.Value)
		}
		vals := fc.elements(list, nil)
		parts := make([]string, 0, len(vals)/2)
		for i := 0; i+1 < len(vals); i += 2 {
			parts = append(parts, vals[i]+": "+vals[i+1])
		}
		return goType + "{" + strings.Join(parts, ", ") + "}"
	}
	refuse(x, "a literal of %s is not lowered yet", t)
	return ""
}

// elements lowers composite elements left to right; an elided-type element
// {…} takes its checked type.
func (fc *funcCtx) elements(list []syntax.Expr, _ check.Type) []string {
	later := make([]bool, len(list)+1)
	for i := len(list) - 1; i >= 0; i-- {
		later[i] = later[i+1] || fc.emits(list[i])
	}
	out := make([]string, len(list))
	for i, e := range list {
		if lit, ok := e.(*syntax.CompositeLit); ok && lit.Type == nil {
			out[i] = fc.composite(lit, fc.typeOf(lit))
		} else {
			out[i] = fc.assigned(e)
		}
		if later[i+1] && !fc.isConst(e) {
			out[i] = fc.spill(out[i])
		}
	}
	return out
}

// index lowers xs[i], and Fn[int], an explicit instance of a generic
// function used as a value (Info.Instances, D-E1). A Map read never
// reaches it: the checker refuses it (D-D4, write m.Get(k)), and a Map
// write is an assignment target.
func (fc *funcCtx) index(x *syntax.IndexExpr) string {
	if sig, ok := fc.typeOf(x.X).(*check.Signature); ok && len(sig.TParams) > 0 {
		inst := fc.l.info.Instances[x]
		if inst == nil {
			ice(x, "the instance %s has no Instances entry", fc.typeOf(x.X))
		}
		return fc.funcValue(inst.Func, fc.typeOf(x), x, inst)
	}
	if _, isMap := fc.typeOf(x.X).(*check.Map); isMap {
		ice(x, "a Map read reached lowering; the checker refuses it (D-D4)")
	}
	if len(x.Indices) != 1 {
		ice(x, "an index with %d indices", len(x.Indices))
	}
	parts := fc.operands([]syntax.Expr{x.X, x.Indices[0]}, false)
	return parts[0] + "[" + parts[1] + "]"
}

// sliceExpr lowers xs[a:b] and xs[a:b:c].
func (fc *funcCtx) sliceExpr(x *syntax.SliceExpr) string {
	list := []syntax.Expr{x.X}
	for _, e := range []syntax.Expr{x.Low, x.High, x.Max} {
		if e != nil {
			list = append(list, e)
		}
	}
	parts := fc.operands(list, false)
	i := 1
	part := func(e syntax.Expr) string {
		if e == nil {
			return ""
		}
		s := parts[i]
		i++
		return s
	}
	low, high := part(x.Low), part(x.High)
	if x.Slice3 {
		return parts[0] + "[" + low + ":" + high + ":" + part(x.Max) + "]"
	}
	return parts[0] + "[" + low + ":" + high + "]"
}

// try lowers try e: e's value, or a failure of the current frame (rows.md,
// Error rows 7). try on a catch makes the catch partial: the failures no arm
// handles escape.
func (fc *funcCtx) try(x *syntax.TryExpr) operand {
	// try covers the whole expression: a failure inside the caught
	// expression of a partial catch propagates too (D-C3).
	fc.tries++
	if c, ok := unparen(x.X).(*syntax.CatchExpr); ok {
		o := fc.catch(c, true)
		fc.tries--
		return o
	}
	o := fc.expr(x.X)
	fc.tries--
	if o.err != "" {
		fc.propagate(o.err)
	}
	return operand{val: o.val}
}

// propagate leaves the frame with err when it is not nil (rows.md, Error
// rows 7).
func (fc *funcCtx) propagate(err string) {
	fc.line("if " + err + " != nil {")
	fc.depth++
	fc.fail(err)
	fc.depth--
	fc.line("}")
}

// catch lowers e catch { arms } to a type switch on e's error (rows.md,
// Catch). An arm handles exactly its types. What no arm names is, under try,
// the rest of the row, which leaves the frame; in a total catch it is a
// defect, re-panicked, never a typed failure (Catch 6).
func (fc *funcCtx) catch(x *syntax.CatchExpr, partial bool) operand {
	m := fc.l.info.Matches[x]
	if m == nil {
		ice(x, "a catch with no Matches entry")
	}
	if m.Partial != partial {
		ice(x, "a catch whose Partial fact disagrees with its try")
	}
	if x.Bind != nil {
		refuse(x, "catch with a binding is not produced by the decided parser")
	}
	o := fc.expr(x.X)
	if o.err == "" {
		ice(x.X, "catch on an expression that cannot fail")
	}
	result := ""
	if m.Result != nil {
		result = fc.temp("v")
		fc.line("var " + result + " " + fc.goType(m.Result))
	} else if o.val != "" {
		// The catch's value is discarded (D-C1), so the caught value is
		// too; its failure is not (stage C round 3 review M1).
		fc.line("_ = " + o.val)
	}
	if !fc.simpleCatch(x) {
		fc.patternCatch(x, m, o, result, partial)
		return operand{val: result}
	}
	type arm struct {
		types []string
		as    *check.Var
		body  []syntax.Stmt
	}
	var arms []arm
	bind := false
	seen := map[string]bool{}
	caseType := func(t check.Type, item syntax.Node) string {
		goType := fc.goType(t)
		if seen[goType] {
			refuse(item, "this arm's type is an earlier arm's type, so this arm never runs; check-v1 stage B reports such an arm as unreachable (D-B4)")
		}
		seen[goType] = true
		return goType
	}
	for _, a := range x.Arms {
		ar := arm{body: a.Body}
		for _, te := range a.Types {
			p := fc.l.info.Patterns[te]
			if p == nil {
				ice(te, "a catch arm with no Patterns entry")
			}
			switch p.Kind {
			case check.PatTypeTest:
				ar.types = append(ar.types, caseType(p.Type, te))
			case check.PatAs:
				if len(a.Types) != 1 || len(p.Elems) != 1 || p.Elems[0].Kind != check.PatTypeTest || p.Var == nil {
					refuse(te, "this catch pattern is lowered after check-v1 stage C (one catch)")
				}
				ar.types = append(ar.types, caseType(p.Elems[0].Type, te))
				ar.as, bind = p.Var, true
			default:
				refuse(te, "this catch pattern is lowered after check-v1 stage C (one catch)")
			}
		}
		arms = append(arms, ar)
	}
	fc.line("if " + o.err + " != nil {")
	fc.depth++
	narrowed := ""
	if bind {
		narrowed = fc.temp("caught")
		fc.line("switch " + narrowed + " := " + o.err + ".(type) {")
	} else {
		fc.line("switch " + o.err + ".(type) {")
	}
	fc.enterSwitch()
	for i, a := range arms {
		fc.line("case " + strings.Join(a.types, ", ") + ":")
		fc.depth++
		if a.as != nil {
			name := fc.declare(a.as)
			fc.line(name + " := " + narrowed)
			if !fc.l.reads[a.as] {
				fc.line("_ = " + name)
			}
		} else if bind {
			fc.line("_ = " + narrowed)
		}
		fc.arm(x.Arms[i], result)
		fc.depth--
	}
	fc.line("default:")
	fc.depth++
	if bind {
		fc.line("_ = " + narrowed)
	}
	if partial {
		fc.fail(o.err)
	} else {
		fc.line("panic(" + o.err + ")")
	}
	fc.depth--
	fc.leaveSwitch()
	fc.line("}")
	fc.depth--
	if result != "" && o.val != "" {
		fc.line("} else {")
		fc.line("\t" + result + " = " + o.val)
	}
	fc.line("}")
	return operand{val: result}
}

// arm writes a catch arm's statements; its last expression is the catch's
// value when the catch has one.
func (fc *funcCtx) arm(a *syntax.CatchArm, result string) {
	list := a.Body
	if len(list) == 0 {
		return
	}
	if result != "" {
		fc.yieldList(list, dest{kind: destAssign, name: result})
		return
	}
	last, isExpr := list[len(list)-1].(*syntax.ExprStmt)
	if !isExpr {
		fc.stmts(list)
		return
	}
	fc.stmts(list[:len(list)-1])
	fc.at(last)
	if result == "" {
		fc.discard(last.X)
		return
	}
	fc.line(result + " = " + fc.assigned(last.X))
}

// funcLit lowers a function literal or a trailing block to a Go closure of
// the uniform function shape (typeCtx.funcType): it takes the caller's
// environment and returns an error. Its rows are its Info.Literals
// signature; its body is its own frame.
func (fc *funcCtx) funcLit(ft *syntax.FuncType, list []syntax.Stmt, n syntax.Node) string {
	sig := fc.l.info.Literals[n]
	if sig == nil {
		ice(n, "a function literal with no Literals entry")
	}
	if len(sig.Results) > 1 {
		refuse(n, "a literal with several results is a tuple, lowered after check-v1 stage B (196t)")
	}
	inner := fc.innerFuncCtx()
	return fc.envClosure(callerEnv, "env", n, func(env string) (parts []string, results, body string) {
		inner.env = env
		inner.frame = newFrame(nil, true, framePolls(fc.l.info, n))
		if sig.ResultIsTask() {
			// A literal that returns a Task receives its caller's frame scope
			// as its destination (stage D3b, Rule 2).
			inner.frame.dest = fc.scope.alloc("dest")
			parts = append(parts, inner.frame.dest+" *"+fc.l.rt()+".Scope")
		}
		var params []*check.Var
		if ft != nil && ft.Params != nil {
			for _, field := range ft.Params.List {
				for _, id := range field.Names {
					v, _ := fc.l.info.Defs[id].(*check.Var)
					params = append(params, v)
				}
			}
		}
		if len(params) != len(sig.Params) {
			ice(n, "a literal declares %d parameters and its signature has %d", len(params), len(sig.Params))
		}
		for i, p := range sig.Params {
			name := fc.scope.alloc("_p")
			if params[i] != nil {
				name = inner.declare(params[i])
			}
			parts = append(parts, name+" "+fc.goType(p.Type))
		}
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		inner.frame.result = result
		inner.depth = fc.depth + 1
		results = fc.resultList(result, true)
		if fc.l.bodyNeedsFrame(list) {
			// The literal is a frame of its own (stage D3b, Rule 2).
			errName := fc.scope.alloc("err")
			results = fc.namedResults(result, errName)
			inner.site = n
			inner.openFrame(errName)
		}
		inner.entryPoll(fc.l.info, n)
		// A trailing block yields its last statement (Info.Tails); a function
		// literal's body is a function body.
		if ft == nil {
			inner.blockBody(list)
		} else {
			inner.body(list)
		}
		return parts, results, inner.out.String()
	})
}

// retTail returns a block's tail value from a literal's frame.
func (fc *funcCtx) retTail(x syntax.Expr) {
	f := fc.frame
	if fc.fallibleExpr(x) {
		o := fc.expr(x)
		if f.result != nil {
			fc.returnFallible(x, o)
		} else {
			fc.line("return " + o.err)
		}
		return
	}
	if f.result == nil {
		fc.discard(x)
		fc.line("return nil")
		return
	}
	fc.line("return " + fc.moved(fc.assigned(x)) + ", nil")
}

// simpleCatch reports a catch a Go type switch on the error lowers: arms of
// type tests and a single `T as e`.
func (fc *funcCtx) simpleCatch(x *syntax.CatchExpr) bool {
	for _, a := range x.Arms {
		for _, te := range a.Types {
			p := fc.l.info.Patterns[te]
			if p == nil {
				return false
			}
			switch p.Kind {
			case check.PatTypeTest:
			case check.PatAs:
				if len(a.Types) != 1 || len(p.Elems) != 1 || p.Elems[0].Kind != check.PatTypeTest || p.Var == nil {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// patternCatch lowers a catch with record patterns, `case err:` or `case _`
// (D-C3) to an expressionless Go switch over the error, as patternMatch does
// for a match: the subject is the row value of the caught row, a Go error. A
// `case err:` arm binds that error (its row value); what no arm handles
// leaves the frame under try, and is a re-panicked defect otherwise
// (rows.md, Catch 6).
func (fc *funcCtx) patternCatch(x *syntax.CatchExpr, m *check.Match, o operand, result string, partial bool) {
	row := &check.Union{Variants: m.Subjects}
	fc.line("if " + o.err + " != nil {")
	fc.depth++
	fc.line("switch {")
	fc.enterSwitch()
	hasDefault := false
	for _, a := range x.Arms {
		var conds []string
		var binds [][]patBind
		always := false
		for _, te := range a.Types {
			p := fc.l.info.Patterns[te]
			if p == nil {
				ice(te, "a catch arm with no Patterns entry")
			}
			var tests []string
			var b []patBind
			fc.pattern(p, o.err, row, te, &tests, &b)
			cond := strings.Join(tests, " && ")
			if cond == "" {
				always, cond = true, "true"
			}
			conds = append(conds, cond)
			binds = append(binds, b)
		}
		if always {
			hasDefault = true
			fc.line("default:")
		} else {
			parts := make([]string, len(conds))
			for i, c := range conds {
				parts[i] = c
				if len(conds) > 1 {
					parts[i] = "(" + c + ")"
				}
			}
			fc.line("case " + strings.Join(parts, " || ") + ":")
		}
		fc.depth++
		fc.bindArm(conds, binds)
		fc.arm(a, result)
		fc.depth--
	}
	if !hasDefault {
		fc.line("default:")
		fc.depth++
		if partial {
			fc.fail(o.err)
		} else {
			fc.line("panic(" + o.err + ")")
		}
		fc.depth--
	}
	fc.leaveSwitch()
	fc.line("}")
	fc.depth--
	if result != "" && o.val != "" {
		fc.line("} else {")
		fc.line("\t" + result + " = " + o.val)
	}
	fc.line("}")
}
