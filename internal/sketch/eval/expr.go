package eval

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// expr evaluates x in a value position: an operand, an argument, a
// condition, an initializer. Whether a typed failure there leaves the
// frame is the one rule of tailExpr (rows.md, Catch 5: under a try, only
// the caught expression's own failure stays, ownFailure), so a value
// position and a tail position differ only in name.
func (in *interp) expr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	return in.exprK(fr, x, in.fx(fr, x))
}

// exprK is expr with x's facts, f: tailK, the one rule (Catch 5).
func (in *interp) exprK(fr *frame, x syntax.Expr, f *facts) (Value, *abrupt) {
	return in.tailK(fr, x, f)
}

// tailExpr evaluates x where its own failure is the enclosing
// expression's: the subject of a try or a catch, a parenthesized
// expression, a block's tail. Each evaluated node costs a step before its
// work.
func (in *interp) tailExpr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	return in.tailK(fr, x, in.fx(fr, x))
}

// tailK is tailExpr with x's facts, f.
func (in *interp) tailK(fr *frame, x syntax.Expr, f *facts) (Value, *abrupt) {
	if f.plain && fr.forms() {
		if f.fast != nil {
			return f.fast(in, fr) // compile.go
		}
		if f.form != formDone && in.visitExpr(fr, x, f) {
			return f.fast(in, fr)
		}
	}
	// A leaf, a name or a constant (a folded one too), is an operand of
	// the node that reads it, as Go's build reads it in that node's
	// instructions (a register or an immediate): the reading node's step
	// pays for it (FU-eval-step-per-op). A leaf evaluates no operand, so a
	// step still bounds the work: one node and the leaves it reads.
	if leaf := f.op == opConst || f.op == opIdent; !leaf {
		if ab := in.charge(fr, x, 1); ab != nil {
			return nil, ab
		}
	}
	// plain: no rule below applies, unless a try is open (ownFailure) or
	// the run is symbolic (symStop).
	if f.plain && fr.tries == 0 && in.sym == nil {
		return in.exprFacts(fr, x, f)
	}
	if in.sym != nil && in.sym.holes != nil {
		if h, ok := in.sym.holes[x]; ok {
			return h, nil // a template's hole (foldvalue.go)
		}
	}
	if f.ctx != nil {
		return in.makeContextValue(fr, x, f.ctx)
	}
	covered := f.covered
	if covered {
		fr.tries++
	}
	v, ab := in.exprFacts(fr, x, f)
	if covered {
		fr.tries--
	}
	if ab != nil {
		if ab.kind == ctlFail && (covered || fr.tries > 0 && !ownFailure(fr.info, fr.caught, unparen(x))) {
			// A right operand that a try on its left covers (q105,
			// check.Info.TryCovers), or an expression under a try but
			// the caught expression itself or a tail it yields: its
			// failure leaves the frame, as under the try itself, so a
			// catch it is inside of takes only its caught expression's
			// own failure (rows.md, Catch 5).
			out := *ab
			out.kind = ctlEscape
			return nil, &out
		}
		return nil, ab
	}
	if in.sym != nil && !in.sym.ok[x] && symbolic(v) {
		return nil, in.symStop(fr, x, v)
	}
	if f.owned != nil {
		in.register(fr, x, v, f.owned, x) // a temporary its statement owns (R43)
		if fr.info.ReturnedTemps[x] {
			// A new value a return gives: the frame keeps it until its
			// exit is known and it leaves with the value (R10,
			// v1.Returned), as a returned owned local does (handoff).
			fr.keep(x)
		}
	}
	// A variant, a sub-union's value (ConvWiden) or a row value goes into its union unchanged: the value
	// keeps its record type. A provider goes into an operation type as an
	// operation value (effects.go); a machine table waits for a later unit.
	if conv := f.conv; f.hasConv && conv.Kind != check.ConvVariant && conv.Kind != check.ConvRow && conv.Kind != check.ConvWiden {
		return in.toOperation(fr, x, v, conv)
	}
	if convs := f.elems; len(convs) > 0 {
		t, ok := v.(tuple)
		if !ok || len(t.elems) != len(convs) {
			ice("element conversions of %T", v)
		}
		var out tuple
		for i, conv := range convs {
			if conv.Kind == 0 || conv.Kind == check.ConvVariant || conv.Kind == check.ConvRow || conv.Kind == check.ConvWiden {
				continue
			}
			if out.elems == nil {
				out.elems = append([]Value(nil), t.elems...)
			}
			e, ab := in.toOperation(fr, x, t.elems[i], conv)
			if ab != nil {
				return nil, ab
			}
			out.elems[i] = e
		}
		if out.elems != nil {
			return out, nil
		}
	}
	return v, nil
}

func (in *interp) exprOf(fr *frame, x syntax.Expr) (Value, *abrupt) {
	return in.exprFacts(fr, x, in.fx(fr, x))
}

// exprFacts is exprOf with x's facts.
func (in *interp) exprFacts(fr *frame, x syntax.Expr, f *facts) (Value, *abrupt) {
	switch f.op {
	case opConst:
		if f.cval != nil && fr.forms() {
			return f.cval, nil
		}
		t := fr.rt(f.typ)
		v, ok := fromConstant(t.t, f.val)
		if !ok {
			return nil, fr.unsupported(x, "a constant of %s yet", t.t)
		}
		if fr.forms() {
			f.cval = v // a constant's value, made once per instance (O38)
		}
		return v, nil
	case opBare:
		return in.bareValue(fr, x, f), nil
	case opCtx:
		return nil, fr.unsupported(x, "a context value, which binds capabilities")
	case opParen:
		p := x.(*syntax.ParenExpr)
		return in.tailK(fr, p.X, in.kid(fr, f, 0, p.X))
	case opIdent:
		return in.identUse(fr, x.(*syntax.Ident), f.use)
	case opBinary:
		return in.binary(fr, x.(*syntax.BinaryExpr), f)
	case opCall:
		return in.callOf(fr, x.(*syntax.CallExpr), f)
	case opBasicLit:
		ice("a literal with no Values entry")
	}
	switch x := x.(type) {
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		if sel, ok := x.(*syntax.SelectorExpr); ok {
			in.takeField(fr, sel)
		}
		v, root, ab := in.peek(fr, x)
		if ab != nil {
			return nil, ab
		}
		if root != nil && !holdsNoStore(v) {
			// A part that holds a store leaves the slot: the slot lends.
			root.lend()
		}
		return v, nil
	case *syntax.SliceExpr:
		return in.sliceExpr(fr, x)
	case *syntax.UnaryExpr:
		return in.unary(fr, x, f)
	case *syntax.CompositeLit:
		return in.composite(fr, x)
	case *syntax.TryExpr:
		return in.try(fr, x)
	case *syntax.CatchExpr:
		return in.catch(fr, x, false)
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		v, ab := in.yieldStmt(fr, x.(syntax.Stmt), false)
		if ab != nil {
			return nil, ab
		}
		if v == nil {
			v = unit{}
		}
		return v, nil
	case *syntax.InterpolatedString:
		return in.interpolated(fr, x)
	case *syntax.TupleExpr:
		out := tuple{elems: make([]Value, len(x.Elts))}
		for i, e := range x.Elts {
			v, ab := in.expr(fr, e)
			if ab != nil {
				return nil, ab
			}
			out.elems[i] = v
		}
		in.handParts(fr, x.Elts...)
		return out, nil
	case *syntax.FuncLit:
		// Making a function literal runs nothing: the value is the literal,
		// the slots it reads and the capabilities bound where it is made.
		// It runs where it is written, or where a native the evaluator
		// runs calls it (sync.Ref's Update, natives.go).
		fr.escaped = true
		return &closure{lit: x, key: x, params: x.Type.Params, body: x.Body, vars: fr.vars.snapshot(), env: fr.env, home: fr, caps: fr.caps}, nil
	case *syntax.StarExpr:
		return in.deref(fr, x.X)
	}
	return nil, fr.unsupported(x, "the expression form %T", x)
}

// closure is a function literal's value, or a trailing block a native
// runs (task.Go's body): its parameters (none for a block), its body, and
// the slots of the frame that made it. lit is the literal, nil for a
// block; key is its Footprints key, the literal or the block.
type closure struct {
	lit    *syntax.FuncLit
	key    syntax.Node
	params *syntax.FieldList
	body   *syntax.BlockStmt
	vars   *varTable
	env    *tenv
	home   *frame // the frame that made it: its package, declaration and owner
	caps   *capEnv
	// outer is the frame whose entries the body's frame reads (own.go),
	// when it is not home: a task's root (tasks.go).
	outer *frame
}

// bareValue is the value of x, a bare record name (facts.bare).
func (in *interp) bareValue(fr *frame, x syntax.Expr, f *facts) Value {
	t := fr.rt(f.typ)
	n, ok := t.t.(*check.Named)
	if !ok || structOf(n) == nil || len(structOf(n).Fields) != 0 {
		ice("a bare value of %s is not a record with no fields", f.bare.Name)
	}
	return &record{t: n, key: argsKey(n, t.env)}
}

// ident reads a name. A local's slot lends: its value may now be shared.
func (in *interp) ident(fr *frame, id *syntax.Ident) (Value, *abrupt) {
	return in.identUse(fr, id, in.fx(fr, id).use)
}

// identUse is ident with the name's Uses entry.
func (in *interp) identUse(fr *frame, id *syntax.Ident, use check.Object) (Value, *abrupt) {
	switch o := use.(type) {
	case *check.Var:
		if s := in.slotOf(fr, o); s != nil {
			s.lend()
			in.handoff(fr, id)
			return s.v, nil
		}
		if in.fold {
			return nil, fr.unsupported(id, "the package let %s in a fold: %s", id.Name, foldLetWhy(o))
		}
		return nil, fr.unsupported(id, "the package let %s, which no initializer bound", id.Name)
	case *check.Const:
		return o.Val, nil
	case *check.Func:
		return in.funcValue(fr, id, o)
	case nil:
		ice("the identifier %s has no Uses entry", id.Name)
	}
	return nil, fr.unsupported(id, "the name %s here", id.Name)
}

// slotOf is v's slot: a local of the frame, or a package let; in a fold,
// a package let no initializer bound yet binds on this read (foldLet).
func (in *interp) slotOf(fr *frame, v *check.Var) *slot {
	if s, ok := fr.vars.get(v); ok {
		return s
	}
	if s := in.globals[v]; s != nil || !in.fold {
		return s
	}
	return in.foldLet(v)
}

// slotAt is slotOf from a compiled form, with varTable.getAt's hint.
func (in *interp) slotAt(fr *frame, v *check.Var, hint *int32) *slot {
	if s, ok := fr.vars.getAt(v, hint); ok {
		return s
	}
	return in.globals[v]
}

// peek reads x without lending: a local, a package let, a field of a
// record or an element of a slice, array or string, through a path of
// them. root is the local's slot at the path's root, nil when the path
// does not start at one; the caller lends it when the value it reads
// holds a store.
func (in *interp) peek(fr *frame, x syntax.Expr) (Value, *slot, *abrupt) {
	return in.peekPath(fr, x, nil)
}

// peekPath is peek that also appends to *path, when path is not nil, the
// steps from root to x: each field, and each index with its value.
func (in *interp) peekPath(fr *frame, x syntax.Expr, path *[]step) (Value, *slot, *abrupt) {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		if v, ok := in.fx(fr, x).use.(*check.Var); ok {
			if s := in.slotOf(fr, v); s != nil {
				// The path's root is a leaf the path's node pays for
				// (tailK).
				return s.v, s, nil
			}
		}
	case *syntax.SelectorExpr:
		if f := fr.info.MethodValues[x]; f != nil {
			// x.f as a value (types.md, Methods 2): f with x's value bound
			// now, as a dot call's receiver is passed.
			ref, ab := in.funcValue(fr, x, f)
			if ab != nil {
				return nil, nil, ab
			}
			recv, ab := in.receiver(fr, x.X, f)
			if ab != nil {
				return nil, nil, ab
			}
			r := ref.(*funcRef)
			return &funcRef{fn: r.fn, targs: r.targs, recv: recv, bound: true}, nil, nil
		}
		xf := in.fx(fr, x)
		if xf.field.t != nil && fr.forms() {
			// A record's field, checked at an earlier read (ORACLE O33).
			base, root, ab := in.peekBase(fr, x.X, path)
			if ab != nil {
				return nil, nil, ab
			}
			r, ok := base.(*record)
			if !ok {
				if in.sym != nil {
					return nil, nil, in.symStop(fr, x.X, base)
				}
				ice("a field of %T", base)
			}
			if path != nil {
				*path = append(*path, step{at: x, field: x.Sel.Name})
			}
			return r.fields[xf.fieldOf(r.t, x.Sel.Name)], root, nil
		}
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			if _, isPkg := in.fx(fr, id).use.(*check.PkgName); isPkg {
				// A package's let: its slot, which nothing writes.
				// The checker records no Uses entry for a qualified
				// name: the member is the imported package's own.
				var member check.Object
				if pn := fr.info.Uses[id].(*check.PkgName); !pn.Pkg.Host() {
					member = in.memberOf(pn.Pkg, x.Sel.Name)
				} else if v, ok := goVars()[pn.Pkg.Path+"."+x.Sel.Name]; ok {
					// A read-only Go variable that a Go function's
					// native reads (nativespure.go, goVars).
					return v, nil, nil
				}
				switch o := member.(type) {
				case *check.Var:
					if s := in.slotOf(fr, o); s != nil {
						return s.v, nil, nil
					}
				case *check.Func:
					v, ab := in.funcValue(fr, x, o)
					return v, nil, ab
				}
				return nil, nil, fr.unsupported(x, "a package member (%s.%s) as a value yet", id.Name, x.Sel.Name)
			}
		}
		t := fr.rtypeOf(x.X)
		if _, isPtr := t.t.(*check.Pointer); isPtr && in.sym != nil && !in.sym.tmpl {
			// A field through a provider's cell (symbolic.go).
			v, ab := in.expr(fr, x.X)
			if ab != nil {
				return nil, nil, ab
			}
			c, ok := v.(*cell)
			if !ok {
				return nil, nil, fr.unsupported(x, "a pointer that is no provider cell")
			}
			r, ok := c.s.v.(*record)
			if !ok {
				return nil, nil, in.symStop(fr, x.X, c.s.v)
			}
			return r.fields[fieldIndex(r.t, x.Sel.Name)], c.s, nil
		}
		if _, ref := check.GoRefElem(t.t); ref {
			// A field of a Go value through its go.Ref (gohost.go).
			base, root, ab := in.peekBase(fr, x.X, path)
			if ab != nil {
				return nil, nil, ab
			}
			g, ok := base.(goVal)
			if !ok {
				return nil, nil, fr.unsupported(x, "a field of %s", describe(base))
			}
			v, ok := goField(g, x.Sel.Name, fr.rtypeOf(x))
			if !ok {
				return nil, nil, fr.unsupported(x, "the Go field %s", x.Sel.Name)
			}
			return v, root, nil
		}
		if p, ok := t.t.(*check.Pointer); ok {
			t = rtype{t: p.Elem, env: t.env} // a field through a borrow
		}
		n, ok := t.t.(*check.Named)
		if !ok || (check.ScriptOf(n) != nil && !ergoProvider(n)) || structOf(n) == nil {
			return nil, nil, fr.unsupported(x, "a member of %s yet", t.t)
		}
		base, root, ab := in.peekBase(fr, x.X, path)
		if ab != nil {
			return nil, nil, ab
		}
		r, ok := base.(*record)
		if !ok {
			if in.sym != nil {
				return nil, nil, in.symStop(fr, x.X, base)
			}
			ice("a field of %T", base)
		}
		if path != nil {
			*path = append(*path, step{at: x, field: x.Sel.Name})
		}
		if fr.forms() {
			xf.field.t = n
		}
		return r.fields[xf.fieldOf(r.t, x.Sel.Name)], root, nil
	case *syntax.StarExpr:
		return in.peekBase(fr, x.X, path)
	case *syntax.IndexExpr:
		if inst := in.fx(fr, x).instance; inst != nil {
			v, ab := in.funcValue(fr, x, inst.Func)
			return v, nil, ab
		}
		if len(x.Indices) != 1 {
			ice("an index with %d indices", len(x.Indices))
		}
		base, root, ab := in.peekBase(fr, x.X, path)
		if ab != nil {
			return nil, nil, ab
		}
		iv, ab := in.expr(fr, x.Indices[0])
		if ab != nil {
			return nil, nil, ab
		}
		if path != nil {
			*path = append(*path, step{at: x, index: iv})
		}
		switch b := base.(type) {
		case string:
			i, ok := in.index(fr, x, iv, len(b))
			if !ok {
				return nil, nil, fr.defect(x, "%s", indexError(iv, len(b)))
			}
			return b[i], root, nil
		case sliceV:
			i, ok := in.index(fr, x, iv, b.n)
			if !ok {
				return nil, nil, fr.defect(x, "%s", indexError(iv, b.n))
			}
			return b.b.data[b.off+i], root, nil
		case mapV:
			ice("a Map read by index")
		}
		ice("an index of %T", base)
	}
	v, ab := in.expr(fr, x)
	return v, nil, ab
}

// index is the integer iv as an index below n.
func (in *interp) index(fr *frame, at syntax.Node, iv Value, n int) (int, bool) {
	i, negative, ok := shiftCount(iv)
	if !ok {
		ice("an index of %T", iv)
	}
	if negative || i >= uint64(n) {
		return 0, false
	}
	return int(i), true
}

// sliceExpr is xs[lo:hi] or xs[lo:hi:max] of a slice, an array or a
// string. A bound past the length is a defect for an array and a string;
// for a slice, Go reads up to the store's capacity, which the Go target's
// growth policy decides, so the evaluator does not guess: a skip.
func (in *interp) sliceExpr(fr *frame, x *syntax.SliceExpr) (Value, *abrupt) {
	base, root, ab := in.peek(fr, x.X)
	if ab != nil {
		return nil, ab
	}
	if root != nil {
		root.lend()
	}
	length := 0
	switch b := base.(type) {
	case string:
		length = len(b)
	case sliceV:
		length = b.n
	default:
		ice("a slice of %T", base)
	}
	// Every bound is evaluated, left to right, before any is checked, as
	// in Go.
	var bounds [3]int64
	given := [3]bool{}
	for i, e := range []syntax.Expr{x.Low, x.High, x.Max} {
		if e == nil {
			continue
		}
		v, ab := in.expr(fr, e)
		if ab != nil {
			return nil, ab
		}
		bounds[i], given[i] = boundValue(v), true
	}
	_, capacityUnknown := base.(sliceV)
	if s, ok := base.(sliceV); ok && s.array {
		capacityUnknown = false
	}
	if msg, skip := sliceBoundsError(bounds, given, int64(length), capacityUnknown); skip {
		return nil, fr.unsupported(x, "a slice bound past the length, which reads the Go target's capacity")
	} else if msg != "" {
		return nil, fr.defect(x, "%s", msg)
	}
	lo, hi, mx := int(bounds[0]), length, length
	if given[1] {
		hi = int(bounds[1])
	}
	if given[2] {
		mx = int(bounds[2])
	}
	switch b := base.(type) {
	case string:
		if ab := in.charge(fr, x, int64(hi-lo)); ab != nil {
			return nil, ab
		}
		return b[lo:hi], nil
	case sliceV:
		out := sliceV{b: b.b, off: b.off + lo, n: hi - lo}
		if b.array || x.Max != nil && mx < length {
			// A slice of an array is a new slice of its elements, and a
			// three-index slice ends its capacity at max: an Append on it
			// copies, so it never writes past its end.
			if ab := in.charge(fr, x, int64(hi-lo)+1); ab != nil {
				return nil, ab
			}
			out = newSlice(append([]Value(nil), b.b.data[b.off+lo:b.off+hi]...))
		}
		return out, nil
	}
	return nil, nil
}

// boundValue is an integer slice bound as an int64 (a uint64 past
// MaxInt64 stays past every length: it is clamped).
func boundValue(v Value) int64 {
	n, negative, ok := shiftCount(v)
	if !ok {
		ice("a slice bound of %T", v)
	}
	if !negative && n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}

// sliceBoundsError is Go's runtime error for a slice expression whose
// bounds (low, high, max; given says which were written) do not fit
// length, or "" when they fit. Go checks, in order, the last bound against
// the capacity, then each bound against the next one, comparing unsigned
// (a negative bound is past every other), and names a negative bound
// alone (runtime boundsError). A slice's capacity is the Go target's:
// when the check would read it (a last bound past the length, where
// capacityUnknown), skip says so.
func sliceBoundsError(b [3]int64, given [3]bool, length int64, capacityUnknown bool) (msg string, skip bool) {
	past := func(x, y int64) bool { return uint64(x) > uint64(y) }
	lo, hi := b[0], length
	if given[1] {
		hi = b[1]
	}
	if given[2] {
		mx := b[2]
		switch {
		case mx < 0:
			return fmt.Sprintf("runtime error: slice bounds out of range [::%d]", mx), false
		case mx > length && capacityUnknown:
			return "", true
		case mx > length:
			return fmt.Sprintf("runtime error: slice bounds out of range [::%d] with length %d", mx, length), false
		case hi < 0:
			return fmt.Sprintf("runtime error: slice bounds out of range [:%d:]", hi), false
		case past(hi, mx):
			return fmt.Sprintf("runtime error: slice bounds out of range [:%d:%d]", hi, mx), false
		case lo < 0:
			return fmt.Sprintf("runtime error: slice bounds out of range [%d::]", lo), false
		case past(lo, hi):
			return fmt.Sprintf("runtime error: slice bounds out of range [%d:%d:]", lo, hi), false
		}
		return "", false
	}
	if given[1] {
		switch {
		case hi < 0:
			return fmt.Sprintf("runtime error: slice bounds out of range [:%d]", hi), false
		case hi > length && capacityUnknown:
			return "", true
		case hi > length:
			return fmt.Sprintf("runtime error: slice bounds out of range [:%d] with length %d", hi, length), false
		}
	}
	switch {
	case lo < 0:
		return fmt.Sprintf("runtime error: slice bounds out of range [%d:]", lo), false
	case past(lo, hi):
		return fmt.Sprintf("runtime error: slice bounds out of range [%d:%d]", lo, hi), false
	}
	return "", false
}

// indexError is Go's runtime error for the index iv out of range of n
// elements: a negative index is named alone (runtime boundsError).
func indexError(iv Value, n int) string {
	if _, negative, _ := shiftCount(iv); negative {
		return fmt.Sprintf("runtime error: index out of range [%s]", describe(iv))
	}
	return fmt.Sprintf("runtime error: index out of range [%s] with length %d", describe(iv), n)
}

// binary evaluates x op y: && and || evaluate y only when x does not
// decide; every other operator evaluates x, then y.
func (in *interp) binary(fr *frame, x *syntax.BinaryExpr, f *facts) (Value, *abrupt) {
	switch x.Op {
	case syntax.LAND, syntax.LOR:
		a, ab := in.exprK(fr, x.X, in.kid(fr, f, 0, x.X))
		if ab != nil {
			return nil, ab
		}
		if truth(a) == (x.Op == syntax.LOR) {
			return a, nil
		}
		return in.exprK(fr, x.Y, in.kid(fr, f, 1, x.Y))
	}
	a, ab := in.exprK(fr, x.X, in.kid(fr, f, 0, x.X))
	if ab != nil {
		return nil, ab
	}
	b, ab := in.exprK(fr, x.Y, in.kid(fr, f, 1, x.Y))
	if ab != nil {
		return nil, ab
	}
	if x.Op == syntax.DOTDOT {
		// a..b is the prelude's Range[T]{Start: a, End: b} (Loops and len 2).
		t := fr.rtypeOf(x)
		n, ok := t.t.(*check.Named)
		if !ok || structOf(n) == nil {
			ice("a range of %s", t.t)
		}
		in.handParts(fr, x.X, x.Y)
		r := &record{t: n, key: argsKey(n, t.env), fields: make([]Value, 2)}
		r.fields[fieldIndex(n, "Start")] = a
		r.fields[fieldIndex(n, "End")] = b
		return r, nil
	}
	return in.binop(fr, x, x.Op, a, b)
}

func (in *interp) unary(fr *frame, x *syntax.UnaryExpr, xf *facts) (Value, *abrupt) {
	if x.Op == syntax.AND {
		return in.borrow(fr, x.X) // pointer.go
	}
	a, ab := in.exprK(fr, x.X, in.kid(fr, xf, 0, x.X))
	if ab != nil {
		return nil, ab
	}
	v, ok := unop(x.Op, a)
	if !ok {
		return nil, fr.unsupported(x, "the operator %s on %T yet", x.Op, a)
	}
	return v, nil
}

// composite builds a record literal, an update `T{..b, F: v}`, a spread
// from a record of another type, or a slice, array or Map literal. A field
// the literal leaves out takes its default value.
func (in *interp) composite(fr *frame, x *syntax.CompositeLit) (Value, *abrupt) {
	t := fr.rtypeOf(x)
	if check.RefinementOf(t.t) != nil {
		return nil, fr.unsupported(x, "a literal of the refined type %s, whose where runs at the literal", t.t)
	}
	switch u := t.under().t.(type) {
	case *check.Slice:
		return in.listLit(fr, x, 0, false, goSize(rtype{u.Elem, t.under().env}))
	case *check.Array:
		if len(x.Elts) == 0 {
			return in.zero(fr, x, t)
		}
		return in.listLit(fr, x, int(u.Len), true, goSize(rtype{u.Elem, t.under().env}))
	case *check.Map:
		m := newMap()
		if ab := in.build(fr, x, int64(len(x.Elts)), 2*int64(len(x.Elts)), 0); ab != nil {
			return nil, ab
		}
		for _, e := range x.Elts {
			kv, ok := e.(*syntax.KeyValueExpr)
			if !ok {
				ice("a Map literal entry with no key")
			}
			k, ab := in.expr(fr, kv.Key)
			if ab != nil {
				return nil, ab
			}
			v, ab := in.expr(fr, kv.Value)
			if ab != nil {
				return nil, ab
			}
			m.b.put(k, v)
		}
		in.handParts(fr, x.Elts...)
		return m, nil
	}
	n, ok := t.t.(*check.Named)
	if _, machine := t.under().t.(*check.MachineType); ok && machine && len(x.Elts) == 0 {
		return &machinePlan{tn: n.Obj}, nil // machinevalue.go
	}
	if ok && len(x.Elts) == 0 && x.Base == nil && ergoProvider(n) {
		return in.providerLit(fr, x, n, t) // provider.go
	}
	if !ok || structOf(n) == nil || check.ScriptOf(n) != nil || check.IsGoError(n) {
		return nil, fr.unsupported(x, "a literal of %s yet", t.t)
	}
	s := structOf(n)
	out := newRecord(n, argsKey(n, t.env), len(s.Fields))
	var setBuf [16]bool // which fields have a value: on the stack for most records
	set := setBuf[:]
	if len(s.Fields) > len(setBuf) {
		set = make([]bool, len(s.Fields))
	}
	var base *record
	if x.Base != nil {
		b, ab := in.expr(fr, x.Base)
		if ab != nil {
			return nil, ab
		}
		if base, ok = b.(*record); !ok {
			ice("a record update over %T", b)
		}
	}
	if ab := in.charge(fr, x, int64(len(s.Fields))); ab != nil {
		return nil, ab
	}
	for i, e := range x.Elts {
		idx := i
		val := e
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			id, ok := kv.Key.(*syntax.Ident)
			if !ok {
				ice("a record literal key that is not a field name")
			}
			idx, val = fieldIndex(n, id.Name), kv.Value
		}
		if idx >= len(s.Fields) {
			ice("a record literal with more values than fields")
		}
		v, ab := in.expr(fr, val)
		if ab != nil {
			return nil, ab
		}
		out.fields[idx], set[idx] = v, true
	}
	in.handParts(fr, x.Elts...)
	if base != nil {
		in.handParts(fr, x.Base)
		in.ownReplaced(fr, x, base)
		if copies, spread := fr.info.Spreads[x]; spread {
			for _, c := range copies {
				i := fieldIndex(n, c.Field.Name)
				out.fields[i], set[i] = base.fields[fieldIndex(base.t, c.From.Name)], true
			}
		} else {
			for i := range out.fields {
				if !set[i] {
					out.fields[i], set[i] = base.fields[i], true
				}
			}
		}
	}
	for i, f := range s.Fields {
		if set[i] {
			continue
		}
		z, ab := in.zero(fr, x, rtype{f.Type, t.env})
		if ab != nil {
			return nil, ab
		}
		out.fields[i] = z
	}
	return out, nil
}

// listLit builds a slice literal, or an array literal that lists all its
// elements, each slot elem bytes (goSize).
func (in *interp) listLit(fr *frame, x *syntax.CompositeLit, n int, array bool, elem int64) (Value, *abrupt) {
	if ab := in.buildSlots(fr, x, int64(len(x.Elts)), int64(len(x.Elts)), elem); ab != nil {
		return nil, ab
	}
	elems := make([]Value, 0, len(x.Elts))
	for _, e := range x.Elts {
		if _, keyed := e.(*syntax.KeyValueExpr); keyed {
			ice("a keyed slice or array literal")
		}
		v, ab := in.expr(fr, e)
		if ab != nil {
			return nil, ab
		}
		elems = append(elems, v)
	}
	in.handParts(fr, x.Elts...)
	if array && len(elems) != n {
		ice("an array literal of %d elements for %d", len(elems), n)
	}
	s := newSlice(elems)
	s.array = array
	s.b.elem = elem
	return s, nil
}

// zero is the default of t, its size charged before it is built, so a
// huge [N]T exhausts the budget instead of the host's memory.
func (in *interp) zero(fr *frame, at syntax.Node, t rtype) (Value, *abrupt) {
	size, ok := zeroSize(t, in.budget.Steps+1)
	if !ok {
		return nil, fr.unsupported(at, "the default value of %s yet", t.t)
	}
	// The steps build each value; the bytes are the Go value's size.
	if ab := in.build(fr, at, size, 0, goSize(t)); ab != nil {
		return nil, ab
	}
	z, ok := zeroOf(t)
	if !ok {
		return nil, fr.unsupported(at, "the default value of %s yet", t.t)
	}
	return z, nil
}

// try passes a failure of x out of the frame; on a catch it makes the
// catch partial.
func (in *interp) try(fr *frame, x *syntax.TryExpr) (Value, *abrupt) {
	fr.tries++
	defer func() { fr.tries-- }()
	if c, ok := unparen(x.X).(*syntax.CatchExpr); ok {
		return in.catch(fr, c, true)
	}
	v, ab := in.tailExpr(fr, x.X)
	if ab != nil && ab.kind == ctlFail {
		out := *ab
		out.kind = ctlEscape
		return nil, &out
	}
	return v, ab
}

// catch handles the typed failure of its subject with the first arm that
// takes it (rows.md, Catch). A failure no arm takes leaves the frame under
// try, and is a defect in a total catch (Catch 6). A defect, an exhausted
// budget and a failure already leaving the frame are never caught.
func (in *interp) catch(fr *frame, x *syntax.CatchExpr, partial bool) (Value, *abrupt) {
	m := in.fx(fr, x).match
	if m == nil {
		ice("a catch with no Matches entry")
	}
	// Under a try, only x.X's own failure is caught (rows.md, Catch 5):
	// every failure inside it leaves the frame (expr).
	outer := fr.caught
	fr.caught = unparen(x.X)
	v, failed := in.expr(fr, x.X)
	fr.caught = outer
	if failed == nil {
		return v, nil
	}
	if failed.kind != ctlFail {
		return nil, failed
	}
	for _, a := range x.Arms {
		for _, te := range a.Types {
			p := in.fx(fr, te).pattern
			if p == nil {
				ice("a catch arm with no Patterns entry")
			}
			if ab := in.charge(fr, te, 1); ab != nil {
				return nil, ab
			}
			ok, ab := in.match(fr, te, p, failed.val)
			if ab != nil {
				return nil, ab
			}
			if ok {
				val, ab := in.scope(fr, a, a.Body, false)
				if ab != nil {
					return nil, ab
				}
				if val == nil {
					val = unit{}
				}
				return val, nil
			}
		}
	}
	if partial {
		out := *failed
		out.kind = ctlEscape
		return nil, &out
	}
	return nil, fr.defect(x, "a failure no arm of the catch takes: %s", describe(failed.val))
}

// ownFailure reports whether a failure of x is the failure of caught, the
// caught expression of the catch being evaluated: x is caught itself, or a
// tail caught yields through the branches of an if or the arms of a match,
// through parentheses and nested ifs and matches (Info.Tails). The
// lowering passes such a tail's failure to its catch (lower/tails.go,
// branchExpr and tailFallible); a condition, a subject or a statement
// before the tail is not the caught expression's own failure (rows.md,
// Error rows 2, Catch 5).
func ownFailure(info *check.Info, caught, x syntax.Expr) bool {
	if caught == nil {
		return false
	}
	caught = unparen(caught)
	if caught == x {
		return true
	}
	s, ok := caught.(syntax.Stmt)
	return ok && tailOf(info, s, x)
}

// tailOf reports whether x is a tail the statement s yields (ownFailure).
func tailOf(info *check.Info, s syntax.Stmt, x syntax.Expr) bool {
	last := func(list []syntax.Stmt) bool {
		return len(list) > 0 && tailOf(info, list[len(list)-1], x)
	}
	switch s := s.(type) {
	case *syntax.ExprStmt:
		_, tail := info.Tails[s]
		return tail && ownFailure(info, s.X, x)
	case *syntax.IfStmt:
		if last(s.Body.List) {
			return true
		}
		switch e := s.Else.(type) {
		case *syntax.BlockStmt:
			return last(e.List)
		case *syntax.IfStmt:
			return tailOf(info, e, x)
		}
	case *syntax.SwitchStmt:
		for _, cl := range s.Clauses {
			if last(cl.Body) {
				return true
			}
		}
	case *syntax.TypeSwitchStmt:
		for _, cl := range s.Clauses {
			if last(cl.Body) {
				return true
			}
		}
	case *syntax.BlockStmt:
		return last(s.List)
	}
	return false
}

// callExpr runs a call: a conversion, len, a declared function with a
// body, a prelude collection function, or a function literal called where
// it is written. Every other callee waits for a later unit or never runs
// at check time.
func (in *interp) callExpr(fr *frame, x *syntax.CallExpr) (Value, *abrupt) {
	return in.callOf(fr, x, in.fx(fr, x))
}

// callOf is callExpr with the call's checked facts.
func (in *interp) callOf(fr *frame, x *syntax.CallExpr, xf *facts) (Value, *abrupt) {
	c := xf.call
	if c == nil {
		ice("a call with no Calls entry")
	}
	if xf.unreachable {
		// The runtime's text (runtime/v1 UnreachableDefect): the reason
		// with the call's position, which task.ExitOf's Defected and
		// actor.Defected give as a value.
		reason := ""
		if len(x.Args) == 1 {
			v, ab := in.expr(fr, x.Args[0])
			if ab != nil {
				return nil, ab
			}
			reason, _ = v.(string)
		}
		s := in.site(fr.pkg, fr.decl, x.Pos())
		return nil, fr.defect(x, "unreachable at %s:%d:%d: %s", s.File, s.Line, s.Column, reason)
	}
	switch c.Kind {
	case check.CallConversion:
		if len(x.Args) != 1 {
			ice("a conversion of %d values", len(x.Args))
		}
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		return in.convert(fr, x, v, fr.rtypeOf(x.Args[0]), fr.rt(c.Type))
	case check.CallPattern:
		// A refinement's pattern (types.md, Refinements 11).
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		s, ok := v.(string)
		if !ok {
			ice("a pattern of %T", v)
		}
		mustCharge(in, fr, int64(len(s)))
		return c.Pattern.Match(s), nil
	case check.CallTestSeed:
		// The run's --seed, for a harnessed test (check harness.go).
		mustCharge(in, fr, 1)
		return in.budget.Seed, nil
	case check.CallTestFile:
		// The test's file as the runner names it (check harness.go).
		mustCharge(in, fr, 1)
		site := nativeSite(in.site(fr.pkg, fr.decl, x.Pos()))
		for range 2 {
			site = site[:strings.LastIndex(site, ":")]
		}
		return site, nil
	case check.CallTestReport:
		// A harnessed test's failure (check harness.go): the test fails
		// with the harness's message as its whole report.
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		return nil, fr.abrupt(ctlAssert, x, &shownAssert{report: v.(string), reported: true}, "")
	case check.CallTestSource:
		// The path a harnessed test's replay names (check harness.go):
		// ergo test's source, or the test's file when the run names none.
		if in.budget.Source != "" {
			mustCharge(in, fr, 1)
			return in.budget.Source, nil
		}
		return in.expr(fr, x.Args[0])
	case check.CallTestShown:
		// A harnessed test's failed comparison shows its operands as the
		// runner's assert does (assertMessage; check harness.go).
		return in.shownText(fr, x)
	case check.CallHash:
		v, ab := in.expr(fr, x.Args[0])
		if ab != nil {
			return nil, ab
		}
		mustCharge(in, fr, 1)
		return hashValue(v), nil
	case check.CallLen:
		// len reads a length, not the elements: a part it reads leaves
		// no share behind, so its slot does not lend (MX-EVWITNESS: vt's
		// push read len(s.flags), and the next write copied the screen).
		var v Value
		var ab *abrupt
		switch a := unparen(x.Args[0]).(type) {
		case *syntax.SelectorExpr, *syntax.IndexExpr:
			if ab = in.charge(fr, a, 1); ab == nil {
				v, _, ab = in.peek(fr, a)
			}
		default:
			v, ab = in.expr(fr, x.Args[0])
		}
		if ab != nil {
			return nil, ab
		}
		if isTerm(v) && in.sym != nil {
			// A template run reads the length of a list that holds a
			// hole, never of a hole (foldvalue.go).
			return nil, in.symStop(fr, x.Args[0], v)
		}
		switch v := v.(type) {
		case string:
			return int64(len(v)), nil
		case sliceV:
			return int64(v.n), nil
		case mapV:
			return int64(len(v.b.m)), nil
		}
		ice("len of %T", v)
	case check.CallFunc:
		if c.Site != nil {
			return nil, fr.unsupported(x, "a call that passes its CallSite yet")
		}
		p := in.planAt(xf, c.Func)
		ergoBody := c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil
		if x.Trailing != nil && !ergoBody && (c.Func.Pkg.Host() || nativeOf(c.Func) == nil) {
			return nil, fr.unsupported(x, "a trailing block yet")
		}
		if p.collection {
			return in.collectionCall(fr, x, c, nil, nil)
		}
		// A bodiless std function that takes an owned local holds its
		// entry until it returns (own.go, holdTaken).
		var held []*syntax.Ident
		if p.takes {
			ops := x.Args
			if c.Recv != nil {
				ops = append([]syntax.Expr{c.Recv}, x.Args...)
			}
			held = in.holdTaken(fr, ops, c.Func.Sig.Takes)
		}
		if p.ergo && x.Trailing == nil && len(held) == 0 {
			// An Ergo body binds its arguments to its parameters and
			// keeps no slice of them.
			args, mark, ab := in.stackArgs(fr, x, c, xf)
			if ab != nil {
				return nil, ab
			}
			var v Value
			if len(c.Func.Sig.TParams) == 0 {
				v, ab = in.callPlanned(fr, x, p, nil, nil, args)
			} else {
				v, ab = in.call(fr, x, c.Func, c.TypeArgs, args)
			}
			in.argStack = in.argStack[:mark]
			return v, ab
		}
		args, ab := in.args(fr, x, c, xf)
		if ab != nil {
			for _, id := range held {
				delete(fr.held, id)
			}
			return nil, ab
		}
		if x.Trailing != nil {
			// The block is the native's last argument: a function of its
			// own, run where the native runs it (task.Go, scope.OnExit).
			fr.escaped = true
			args = append(args, &closure{key: x.Trailing, body: x.Trailing, vars: fr.vars, env: fr.env, home: fr, caps: fr.caps})
		}
		var v Value
		if len(c.Func.Sig.TParams) == 0 {
			v, ab = in.callPlanned(fr, x, p, nil, nil, args)
		} else {
			v, ab = in.call(fr, x, c.Func, c.TypeArgs, args)
		}
		if len(held) > 0 {
			ab = in.settleHeld(fr, held, ab)
		}
		return v, ab
	case check.CallFuncValue:
		if x.Trailing != nil {
			return nil, fr.unsupported(x, "a trailing block yet")
		}
		lit, ok := unparen(x.Fun).(*syntax.FuncLit)
		if !ok {
			// The callee is a value: evaluated before the arguments, as
			// Go evaluates a call's function value first.
			f, ab := in.expr(fr, x.Fun)
			if ab != nil {
				return nil, ab
			}
			args, ab := in.args(fr, x, c, xf)
			if ab != nil {
				return nil, ab
			}
			return in.callValue(fr, x, f, args)
		}
		args, ab := in.args(fr, x, c, xf)
		if ab != nil {
			return nil, ab
		}
		return in.callLiteral(fr, x, lit, args)
	case check.CallWith:
		return in.withExpr(fr, x)
	case check.CallProviderOp:
		return in.providerCall(fr, x, c)
	case check.CallOp, check.CallLawOp:
		return in.operationCall(fr, x, c)
	case check.CallEmbed:
		return in.embedValue(fr, x, c) // nativesembed.go
	case check.CallEncode, check.CallDecode:
		return in.wireCall(fr, x, c) // codecwalk.go
	case check.CallGoError:
		if c.Name == "go.New" || c.Name == "go.Zero" {
			return in.goBuiltin(fr, x, c) // gohost.go
		}
	}
	return nil, fr.unsupported(x, "this call (kind %d)", c.Kind)
}

// args evaluates a call's receiver and arguments, left to right.
func (in *interp) args(fr *frame, x *syntax.CallExpr, c *check.Call, xf *facts) ([]Value, *abrupt) {
	args := make([]Value, 0, len(x.Args)+1)
	if c.Recv != nil {
		v, ab := in.receiver(fr, c.Recv, c.Func)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	for i, a := range x.Args {
		v, ab := in.exprK(fr, a, in.kid(fr, xf, i, a))
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	in.handParts(fr, c.Recv)
	in.handParts(fr, x.Args...)
	return args, nil
}

// stackArgs is args on the interpreter's argument stack, for a callee
// that only binds them to its parameters (ORACLE O32): no slice is made
// per call. The caller pops the stack to mark once the call returns.
func (in *interp) stackArgs(fr *frame, x *syntax.CallExpr, c *check.Call, xf *facts) (args []Value, mark int, ab *abrupt) {
	mark = len(in.argStack)
	if c.Recv != nil {
		v, ab := in.receiver(fr, c.Recv, c.Func)
		if ab != nil {
			in.argStack = in.argStack[:mark]
			return nil, mark, ab
		}
		in.argStack = append(in.argStack, v)
	}
	for i, a := range x.Args {
		v, ab := in.exprK(fr, a, in.kid(fr, xf, i, a))
		if ab != nil {
			in.argStack = in.argStack[:mark]
			return nil, mark, ab
		}
		in.argStack = append(in.argStack, v)
	}
	in.handParts(fr, c.Recv)
	in.handParts(fr, x.Args...)
	n := len(in.argStack)
	return in.argStack[mark:n:n], mark, nil
}

// callLiteral runs a function literal called where it is written: its
// body reads and writes the slots of the frame around it, in place.
func (in *interp) callLiteral(fr *frame, at *syntax.CallExpr, lit *syntax.FuncLit, args []Value) (Value, *abrupt) {
	fr.escaped = true
	c := &closure{lit: lit, key: lit, params: lit.Type.Params, body: lit.Body, vars: fr.vars, env: fr.env, home: fr, caps: fr.caps}
	return in.callClosure(fr, at, c, args)
}

// callClosure runs c's body as a frame of its own that reads and writes
// the slots of the frame that made it, with c's parameters bound to args.
func (in *interp) callClosure(fr *frame, at *syntax.CallExpr, c *closure, args []Value) (Value, *abrupt) {
	outer := c.home
	if c.outer != nil {
		outer = c.outer
	}
	callee := &frame{pkg: c.home.pkg, info: c.home.info, decl: c.home.decl, key: c.key, vars: c.vars, env: c.env, owner: c.home.owner, lawOwn: c.home.lawOwn, tab: c.home.tab, caps: c.callerCaps(fr), outer: outer}
	var params []*check.Var
	if c.params != nil {
		for _, field := range c.params.List {
			if len(field.Names) == 0 {
				params = append(params, nil)
			}
			for _, id := range field.Names {
				v, _ := callee.info.Defs[id].(*check.Var)
				params = append(params, v)
			}
		}
	}
	if len(params) != len(args) {
		ice("a function literal of %d parameters called with %d", len(params), len(args))
	}
	for i, p := range params {
		callee.bind(p, args[i])
	}
	for _, p := range params {
		in.ownVar(callee, p, c.key) // a taken parameter (R8)
	}
	return in.runFrame(fr, at, callee, "literal", c.body.List)
}

// callerCaps is the capabilities a call of c from fr runs with: a
// capability the literal's own type names (`func(...) needs C`) is the
// caller's, as for a declared function's needs; any other the body uses
// is the one bound where the literal was made.
func (c *closure) callerCaps(fr *frame) *capEnv {
	caps := c.caps
	if c.lit == nil || c.lit.Type == nil || c.lit.Type.Needs == nil {
		return caps
	}
	sig, ok := c.home.info.Types[c.lit].(*check.Signature)
	if !ok {
		return caps
	}
	for _, t := range sig.Uses.Elems() {
		if _, named := t.(*check.Named); !named {
			continue // a row variable: the capabilities it stands for were bound where the literal was made
		}
		key := typeKey(c.home.rt(t))
		if op, ok := fr.caps.lookup(key); ok {
			caps = &capEnv{key: key, op: op, next: caps}
		}
	}
	return caps
}

// preludeCollection reports the prelude's Append, Get, Delete, Put or Take,
// which the evaluator runs as value operations, never by their Go bodies
// (plan section 4: pure prelude collection operations).
func preludeCollection(f *check.Func) bool {
	switch check.CollOpOf(f) {
	case check.CollAppend, check.CollGet, check.CollDelete, check.CollPut, check.CollTake:
		return true
	}
	return false
}

// collectionCall runs xs.Append(x), m.Get(k), m.Delete(k), m.Put(k, v) or
// m.Take(k).
// own is the slot an assignment hands the receiver over from (x =
// x.Append(e), x.f = x.f.Append(e)), and at the path from own's value to
// the receiver (stmt.go, handover); the receiver is then read without a
// lend.
func (in *interp) collectionCall(fr *frame, x *syntax.CallExpr, c *check.Call, own *slot, at []step) (Value, *abrupt) {
	var recv Value
	var ab *abrupt
	if own != nil {
		if ab := in.charge(fr, c.Recv, 1); ab != nil {
			return nil, ab
		}
		if recv, ab = in.read(fr, own.v, at); ab != nil {
			return nil, ab
		}
	} else if recv, ab = in.expr(fr, c.Recv); ab != nil {
		return nil, ab
	}
	args := make([]Value, len(x.Args))
	for i, a := range x.Args {
		if args[i], ab = in.expr(fr, a); ab != nil {
			return nil, ab
		}
	}
	in.handParts(fr, c.Recv)
	in.handParts(fr, x.Args...)
	var tok *token
	if own != nil {
		tok = own.own()
	}
	if in.sym != nil {
		if _, ok := recv.(term); ok {
			if check.CollOpOf(c.Func) != check.CollAppend {
				return nil, in.symStop(fr, c.Recv, recv)
			}
			if ab := in.charge(fr, x, 1); ab != nil {
				return nil, ab
			}
			return concatSlice(recv, args[0]), nil
		}
	}
	switch check.CollOpOf(c.Func) {
	case check.CollAppend:
		return in.appendTo(fr, x, recv.(sliceV), args[0], tok)
	case check.CollGet:
		if ab := in.charge(fr, x, 1); ab != nil {
			return nil, ab
		}
		v, found := recv.(mapV).b.get(args[0])
		return in.option(fr, x, v, found)
	case check.CollDelete:
		return in.deleteIn(fr, x, recv.(mapV), args[0], tok)
	case check.CollPut:
		return in.putIn(fr, x, recv.(mapV), args[0], args[1], tok)
	case check.CollTake:
		// k's value as an Option, and m without k (std ui's session takes
		// a request in flight; b25's guard found it had no native).
		if ab := in.charge(fr, x, 1); ab != nil {
			return nil, ab
		}
		m := recv.(mapV)
		v, found := m.b.get(args[0])
		rest, ab := in.deleteIn(fr, x, m, args[0], tok)
		if ab != nil {
			return nil, ab
		}
		t := fr.rtypeOf(x)
		u := t.under()
		return tuple{elems: []Value{optionOf(rtype{t: u.t.(*check.Tuple).Types[0], env: u.env}, v, found), rest}}, nil
	}
	ice("the prelude function %s", c.Func.Name)
	return nil, nil
}

// option is Some{v} or None of the call's Option type.
func (in *interp) option(fr *frame, x *syntax.CallExpr, v Value, found bool) (Value, *abrupt) {
	return optionOf(fr.rtypeOf(x), v, found), nil
}

// convert is T(v) from the type from: a basic conversion as Go's, between
// a defined type and the type it is defined over, or a record to another
// record type over the same fields.
func (in *interp) convert(fr *frame, x *syntax.CallExpr, v Value, from, to rtype) (Value, *abrupt) {
	// Refined conversions are admitted only for checked constants or inside
	// the derived parser, whose ordinary predicate runs before this cast.
	if b := to.basic(); b != nil {
		if from.basic() == nil {
			return nil, fr.unsupported(x, "a conversion from %s yet", from.t)
		}
		out, ok := convertBasic(v, b.Kind)
		if !ok {
			return nil, fr.unsupported(x, "the conversion of %s to %s, which the Go target leaves to the host or which needs a later unit", from.t, to.t)
		}
		return out, nil
	}
	switch v := v.(type) {
	case *record:
		if n, isNamed := to.t.(*check.Named); isNamed && structOf(n) != nil {
			return &record{t: n, key: argsKey(n, to.env), fields: v.fields}, nil
		}
	case sliceV:
		if _, isArray := to.under().t.(*check.Array); isArray == v.array {
			return v, nil
		}
	case mapV, tuple:
		return v, nil
	case *closure, *funcRef, *nativeFunc, seqSource:
		// A function value converts to a function type over the same
		// signature, as Seq[T](f) does (seq.go).
		if _, isFunc := to.under().t.(*check.Signature); isFunc {
			return v, nil
		}
	}
	return nil, fr.unsupported(x, "a conversion to %s yet", to.t)
}

// interpolated builds "a \(x) b": each value's text as the lowering writes
// it (lower/interp.go).
func (in *interp) interpolated(fr *frame, x *syntax.InterpolatedString) (Value, *abrupt) {
	if len(x.Texts) != len(x.Exprs)+1 {
		ice("an interpolated string with %d segments for %d values", len(x.Texts), len(x.Exprs))
	}
	var b strings.Builder
	for i, seg := range x.Texts {
		raw := seg.Value
		if len(raw) < 2 {
			ice("an interpolated string segment %q", raw)
		}
		raw = raw[1:]
		if i == len(x.Texts)-1 {
			raw = raw[:len(raw)-1]
		} else {
			raw = strings.TrimSuffix(raw, `\(`)
		}
		s, err := strconv.Unquote(`"` + raw + `"`)
		if err != nil {
			ice("an interpolated segment %q: %v", seg.Value, err)
		}
		b.WriteString(s)
		if i == len(x.Exprs) {
			break
		}
		v, ab := in.expr(fr, x.Exprs[i])
		if ab != nil {
			return nil, ab
		}
		text, ok := textOf(v, fr.rtypeOf(x.Exprs[i]).basic())
		if !ok {
			return nil, fr.unsupported(x.Exprs[i], "the text of %s yet", fr.typeOf(x.Exprs[i]))
		}
		b.WriteString(text)
	}
	if ab := in.build(fr, x, byteWork(int64(b.Len())), 0, int64(b.Len())); ab != nil {
		return nil, ab
	}
	return b.String(), nil
}

// textOf is the text of a basic value in an interpolation.
func textOf(v Value, b *check.Basic) (string, bool) {
	if b == nil {
		return "", false
	}
	if b.Kind == check.Duration {
		return time.Duration(v.(int64)).String(), true
	}
	switch v := v.(type) {
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), true
	}
	return "", false
}
