package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Loop idioms (DT4). A counted loop whose whole body writes or appends one
// slice element per index is a fill, a move or a collect, and Go has one
// call for each: copy, which is memmove, and append of a slice. Go's
// compiler turns only `for i := range s { s[i] = zero }` into memclr, so
// the element loop a lowering writes stores one element, and checks its
// bounds, per index. The three forms, for `for i in lo..hi`:
//
//   - fill: `P[i+d] = v`, where v does not change in the loop. The
//     lowering writes v once and doubles the written part with copy, as
//     bytes.Repeat does: log2(n) memmoves.
//   - move: `P[i+d] = P[i+s]` on one slice P: copy(P[lo+d:hi+d],
//     P[lo+s:hi+s]). A forward loop is memmove only when the target starts
//     at or before the source, or after the source ends; else (it smears
//     the first elements forward) the loop runs.
//   - collect: `xs = xs.Append(Q[i+s])`, Q a slice or an array: one Go
//     append of the part Q[lo+s:hi+s], so one growth, where the loop grows
//     xs by doubling. The source of the append is the loop's own (V3,
//     valueSource): clipped where the loop's Append is not in place.
//
// d and s are `i`, `i + c`, `c + i` or `i - c`. The form is taken when it
// is provably the loop's effect:
//
//  1. The body is the one statement, so the loop has no break, continue or
//     return, and the frame takes no back-edge poll (a frame that polls
//     keeps its loop, so a long loop stays stoppable) and releases nothing
//     at the end of an iteration.
//  2. The element type holds no Map or slice (values.go, holdsValue): the
//     loop's store is a plain Go assignment of each element, with no copy
//     of the value rule, which a copy call matches bit for bit.
//  3. P (or Q), c and v are the same at every index: each is a name, a
//     constant, a field of one, or an operator on them (no call, index,
//     dereference, division or shift), and none names i. A field read
//     through a pointer to a type X is taken only when X is not the
//     element type and is not held by value in it: a store to an element
//     cannot change it. P is evaluated once in place of once per index;
//     it has no effect, so only the count of reads differs.
//  4. The loop would raise no defect: the call runs only when every index
//     is in bounds, which a guard tests first (lo < hi first, so an empty
//     loop reads nothing), with every bound computed from lo and hi so an
//     overflow fails the guard. Otherwise the loop itself runs, and raises
//     the defect it raises today at the same index after the same writes.
//
// The loop is written in both branches' place: `if guard { call } else
// { loop }`.
//
// The reference is loopLowering "plain", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.loopLowering=plain:
// every loop is the element loop. TestLoopIdiomsAgree runs generated
// programs of every form, offset, overlap, empty range and defect under
// both.
//
// Prior art: LLVM's LoopIdiomRecognize turns a store loop into memset and
// memcpy (and a forward copy loop with possible overlap into memmove only
// when it proves the direction); GCC's -ftree-loop-distribute-patterns
// does the same; Go recognizes only the zeroing range loop (memclr).
// Gleam, Elixir and ReScript have no mutable arrays in their source
// languages, so they write no such loops; ReScript leaves a JavaScript
// loop to the engine's JIT.
var loopLowering string

// loopIdiom is one loop the lowering writes as a call.
type loopIdiom struct {
	kind string // "fill", "move" or "collect"
	// place is P, the written slice (fill and move), or Q, the slice or
	// array read (collect).
	place syntax.Expr
	// dst is the target's offset (fill and move), src the source's (move
	// and collect); nil is 0.
	dst, src       syntax.Expr
	dstNeg, srcNeg bool
	val            syntax.Expr      // fill: the value written
	xs             *syntax.Ident    // collect: the slice appended to
	call           *syntax.CallExpr // collect: the Append
	c              *check.Call
}

// loopIdiomOf is the idiom the range loop s is, or nil.
func (fc *funcCtx) loopIdiomOf(s *syntax.RangeStmt, l *check.Loop) *loopIdiom {
	if loopLowering == "plain" || l.Domain != check.LoopRange || fc.frame == nil || fc.frame.polls {
		return nil
	}
	if b, ok := l.Elem.(*check.Basic); !ok || b.Kind != check.Int {
		return nil
	}
	if s.Key != nil || s.Body == nil || len(s.Body.List) != 1 || len(fc.l.info.Releases[s.Body]) > 0 {
		return nil
	}
	id, ok := s.Value.(*syntax.Ident)
	if !ok {
		return nil
	}
	iv, ok := fc.l.info.Defs[id].(*check.Var)
	if !ok || iv == nil {
		return nil
	}
	as, ok := s.Body.List[0].(*syntax.AssignStmt)
	if !ok || as.Tok != syntax.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return nil
	}
	if xs, ok := unparen(as.Lhs[0]).(*syntax.Ident); ok {
		return fc.collectIdiom(xs, as.Rhs[0], iv)
	}
	ix, ok := unparen(as.Lhs[0]).(*syntax.IndexExpr)
	if !ok || len(ix.Indices) != 1 {
		return nil
	}
	elem, ok := sliceElem(fc.typeOf(ix.X))
	if !ok || fc.l.holdsValue(elem) || !fc.invariant(ix.X, elem, iv) {
		return nil
	}
	dst, dstNeg, ok := fc.offsetOf(ix.Indices[0], elem, iv)
	if !ok {
		return nil
	}
	if _, conv := fc.l.info.Implicit[as.Rhs[0]]; !conv {
		if rx, ok := unparen(as.Rhs[0]).(*syntax.IndexExpr); ok && len(rx.Indices) == 1 && sameSyntax(rx.X, ix.X) {
			src, srcNeg, ok := fc.offsetOf(rx.Indices[0], elem, iv)
			if !ok {
				return nil
			}
			return &loopIdiom{kind: "move", place: ix.X, dst: dst, dstNeg: dstNeg, src: src, srcNeg: srcNeg}
		}
	}
	if !fc.invariant(as.Rhs[0], elem, iv) {
		return nil
	}
	return &loopIdiom{kind: "fill", place: ix.X, dst: dst, dstNeg: dstNeg, val: as.Rhs[0]}
}

// collectIdiom is the collect `xs = xs.Append(Q[i+s])`, or nil.
func (fc *funcCtx) collectIdiom(xs *syntax.Ident, rhs syntax.Expr, iv *check.Var) *loopIdiom {
	v, ok := fc.l.info.Uses[xs].(*check.Var)
	if !ok || fc.l.info.OwnedVars[v] != nil || fc.scalarIdent(xs) != nil {
		return nil
	}
	if _, local := fc.locals[v]; !local {
		return nil
	}
	call, ok := unparen(rhs).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return nil
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || !preludeAppend(c.Func) || c.StartsTask || failsHere(c) {
		return nil
	}
	list := call.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if len(list) != 2 {
		return nil
	}
	if src, ok := unparen(list[0]).(*syntax.Ident); !ok || fc.l.info.Uses[src] != v {
		return nil
	}
	elem, ok := sliceElem(v.Type)
	if !ok || fc.l.holdsValue(elem) {
		return nil
	}
	if _, conv := fc.l.info.Implicit[list[1]]; conv {
		return nil
	}
	rx, ok := unparen(list[1]).(*syntax.IndexExpr)
	if !ok || len(rx.Indices) != 1 {
		return nil
	}
	qElem, ok := sliceElem(fc.typeOf(rx.X))
	if !ok {
		a, isArray := underEqual(fc.typeOf(rx.X)).(*check.Array)
		if !isArray {
			return nil
		}
		qElem = a.Elem
	}
	if qElem.String() != elem.String() || !fc.invariant(rx.X, elem, iv) {
		return nil
	}
	src, srcNeg, ok := fc.offsetOf(rx.Indices[0], elem, iv)
	if !ok {
		return nil
	}
	return &loopIdiom{kind: "collect", place: rx.X, src: src, srcNeg: srcNeg, xs: xs, call: call, c: c}
}

// sliceElem is the element type of a slice type t.
func sliceElem(t check.Type) (check.Type, bool) {
	s, ok := underEqual(t).(*check.Slice)
	if !ok {
		return nil, false
	}
	return s.Elem, true
}

// offsetOf splits an index x into the loop variable iv and an offset:
// `i` (nil), `i + c`, `c + i` or `i - c` (neg). ok is false for any other
// index.
func (fc *funcCtx) offsetOf(x syntax.Expr, elem check.Type, iv *check.Var) (off syntax.Expr, neg, ok bool) {
	isI := func(e syntax.Expr) bool {
		id, ok := unparen(e).(*syntax.Ident)
		return ok && fc.l.info.Uses[id] == iv
	}
	x = unparen(x)
	if isI(x) {
		return nil, false, true
	}
	b, isBin := x.(*syntax.BinaryExpr)
	if !isBin {
		return nil, false, false
	}
	switch {
	case b.Op == syntax.ADD && isI(b.X):
		off = b.Y
	case b.Op == syntax.ADD && isI(b.Y):
		off = b.X
	case b.Op == syntax.SUB && isI(b.X):
		off, neg = b.Y, true
	default:
		return nil, false, false
	}
	if t, ok := fc.typeOf(off).(*check.Basic); !ok || t.Kind != check.Int || !fc.invariant(off, elem, iv) {
		return nil, false, false
	}
	return off, neg, true
}

// invariant reports that x has the same value at every index of a loop
// over iv that stores elements of type elem, and that evaluating it has
// no effect and raises no defect (rule 3 above).
func (fc *funcCtx) invariant(x syntax.Expr, elem check.Type, iv *check.Var) bool {
	if fc.emits(x) {
		return false
	}
	ok := true
	syntax.Inspect(x, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		if e, isExpr := n.(syntax.Expr); isExpr && e != x && fc.isConst(e) {
			return false // a constant: its parts are its own
		}
		switch n := n.(type) {
		case *syntax.Ident:
			switch o := fc.l.info.Uses[n].(type) {
			case *check.Var:
				ok = o != iv
			case *check.Const:
			default:
				ok = false
			}
		case *syntax.BasicLit, *syntax.ParenExpr, *syntax.KeyValueExpr:
		case *syntax.UnaryExpr:
			ok = n.Op == syntax.SUB || n.Op == syntax.ADD || n.Op == syntax.NOT || n.Op == syntax.XOR
		case *syntax.BinaryExpr:
			ok = n.Op != syntax.QUO && n.Op != syntax.REM && n.Op != syntax.SHL && n.Op != syntax.SHR && n.Op != syntax.DOTDOT
		case *syntax.CompositeLit:
			ok = n.Base == nil
			if ok {
				_, isRecord := underEqual(fc.typeOf(n)).(*check.Struct)
				ok = isRecord
			}
			// The type and the field names are no values: only the
			// elements are read.
			for _, e := range n.Elts {
				ok = ok && fc.invariant(unKey(e), elem, iv)
			}
			return false
		case *syntax.SelectorExpr:
			if id, isID := n.X.(*syntax.Ident); isID {
				if _, isPkg := fc.l.info.Uses[id].(*check.PkgName); isPkg {
					_, ok = fc.l.info.Uses[n.Sel].(*check.Var)
					return false
				}
			}
			t, typed := fc.typed(n.X)
			if !typed {
				ok = false
				return false
			}
			if p, isPtr := t.(*check.Pointer); isPtr {
				// A store to an element cannot reach a field of X when X
				// is not held by value in the element.
				if holdsByValue(elem, p.Elem, map[string]bool{}) {
					ok = false
					return false
				}
				t = p.Elem
			}
			if _, isRecord := underEqual(t).(*check.Struct); !isRecord || !hasField(t, n.Sel.Name) {
				ok = false
				return false
			}
			ok = fc.invariant(n.X, elem, iv)
			return false
		default:
			ok = false
		}
		return ok
	})
	return ok
}

// unKey is the value of a composite literal element.
func unKey(e syntax.Expr) syntax.Expr {
	if kv, ok := e.(*syntax.KeyValueExpr); ok {
		return kv.Value
	}
	return e
}

// hasField reports a field named name of the record t.
func hasField(t check.Type, name string) bool {
	s, ok := underEqual(t).(*check.Struct)
	if !ok {
		return false
	}
	for _, f := range s.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// holdsByValue reports that a value of type t is x or holds x in its
// memory: x itself, or a field, an array element or a tuple element that
// holds it. Types compare by their text, so two types of one spelling
// count as one: the answer errs toward true.
func holdsByValue(t, x check.Type, seen map[string]bool) bool {
	key := t.String()
	if key == x.String() {
		return true
	}
	if seen[key] {
		return false
	}
	seen[key] = true
	switch u := underEqual(t).(type) {
	case *check.Struct:
		for _, f := range u.Fields {
			if holdsByValue(f.Type, x, seen) {
				return true
			}
		}
	case *check.Array:
		return holdsByValue(u.Elem, x, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if holdsByValue(e, x, seen) {
				return true
			}
		}
	}
	return false
}

// sameSyntax reports two expressions of the same text: names, constants,
// fields, operators. It is only asked of expressions invariant took.
func sameSyntax(a, b syntax.Expr) bool {
	a, b = unparen(a), unparen(b)
	switch a := a.(type) {
	case *syntax.Ident:
		b, ok := b.(*syntax.Ident)
		return ok && a.Name == b.Name
	case *syntax.SelectorExpr:
		b, ok := b.(*syntax.SelectorExpr)
		return ok && a.Sel.Name == b.Sel.Name && sameSyntax(a.X, b.X)
	}
	return false
}

// loopIdiomHead writes the guard and the call of idiom for the range
// loop over the Range value whose Start and End are lo and hi (Go
// variables' fields), and opens the else branch the loop is written in.
func (fc *funcCtx) loopIdiomHead(s *syntax.RangeStmt, idiom *loopIdiom, lo, hi string) {
	at := func(base string, off syntax.Expr, neg bool) string {
		if off == nil {
			return base
		}
		if neg {
			return "(" + base + " - (" + fc.value(off) + "))"
		}
		return "(" + base + " + (" + fc.value(off) + "))"
	}
	// in tests that [a, b) is a non-empty part of a value of length
	// length. b - a is hi - lo when no bound overflowed, and an overflow
	// makes a < b or b <= len false.
	in := func(a, b, length string) string {
		return "0 <= " + a + " && " + a + " < " + b + " && " + b + " <= len(" + length + ")"
	}
	fc.at(s.Body.List[0])
	place := fc.value(idiom.place)
	guard := lo + " < " + hi
	switch idiom.kind {
	case "fill":
		a, b := at(lo, idiom.dst, idiom.dstNeg), at(hi, idiom.dst, idiom.dstNeg)
		fc.line("if " + guard + " && " + in(a, b, place) + " {")
		fc.depth++
		f := fc.temp("fill")
		fc.line(f + " := " + place + "[" + a + ":" + b + "]")
		fc.line(f + "[0] = " + fc.assigned(idiom.val))
		k := fc.temp("k")
		zero := fillLowering != "copy" && zeroComparable(fc.typeOf(idiom.val), map[check.Type]bool{})
		if zero {
			// A value whose Go form is all zero bits when it equals
			// the type's zero value is a clear, as Go writes the
			// memclr range loop (cmd/compile, walk/range.go arrayClear).
			z := fc.temp("zero")
			fc.line("var " + z + " " + fc.goType(fc.typeOf(idiom.val)))
			fc.line("if " + f + "[0] == " + z + " {")
			fc.line("\tclear(" + f + ")")
			fc.line("} else {")
			fc.depth++
		}
		fc.line("for " + k + " := 1; " + k + " < len(" + f + "); " + k + " *= 2 {")
		fc.line("\tcopy(" + f + "[" + k + ":], " + f + "[:" + k + "])")
		fc.line("}")
		if zero {
			fc.depth--
			fc.line("}")
		}
		fc.depth--
	case "move":
		da, db := at(lo, idiom.dst, idiom.dstNeg), at(hi, idiom.dst, idiom.dstNeg)
		sa, sb := at(lo, idiom.src, idiom.srcNeg), at(hi, idiom.src, idiom.srcNeg)
		// A forward loop is memmove when the target starts at or before
		// the source, or after it.
		forward := "(" + da + " <= " + sa + " || " + da + " >= " + sb + ")"
		fc.line("if " + guard + " && " + in(da, db, place) + " && " + in(sa, sb, place) + " && " + forward + " {")
		fc.line("\tcopy(" + place + "[" + da + ":" + db + "], " + place + "[" + sa + ":" + sb + "])")
	case "collect":
		sa, sb := at(lo, idiom.src, idiom.srcNeg), at(hi, idiom.src, idiom.srcNeg)
		xs := fc.ident(idiom.xs)
		src := fc.valueSource(idiom.call, idiom.c, []string{xs})[0]
		fc.line("if " + guard + " && " + in(sa, sb, place) + " {")
		fc.line("\t" + xs + " = append(" + src + ", " + place + "[" + sa + ":" + sb + "]...)")
	default:
		ice(s, "the loop idiom %q", idiom.kind)
	}
	fc.l.stats.Loops++
	fc.line("} else {")
	fc.depth++
}

// fillLowering "copy" (a test, or a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.fillLowering=copy)
// is the reference of the fill idiom: copy doubling alone, with no clear
// for a zero value (VI1).
var fillLowering string

// zeroComparable reports a type whose Go form compares with == and is
// all zero bits exactly when it equals the zero value: a bool, a string,
// an integer, a Duration, and a record of those. A float is not: -0.0
// equals 0.0, and a clear would write +0.0.
func zeroComparable(t check.Type, seen map[check.Type]bool) bool {
	switch x := t.(type) {
	case *check.Basic:
		return x.Kind == check.Bool || x.Kind == check.String || (x.Kind >= check.Int && x.Kind <= check.Uintptr) || x.Kind == check.Duration
	case *check.Named:
		if seen[t] {
			return false // a cycle: no Go form compares
		}
		seen[t] = true
		defer delete(seen, t)
		switch u := x.Underlying().(type) {
		case *check.Basic:
			return zeroComparable(u, seen)
		case *check.Struct:
			if len(u.Fields) == 0 {
				return false
			}
			for _, f := range u.Fields {
				if !zeroComparable(f.Type, seen) {
					return false
				}
			}
			return true
		}
	}
	return false
}
