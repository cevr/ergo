package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Value semantics for Maps and slices (value-rule; owner q26, 2026-11-08:
// "Maps and slices are values: copy where two bindings would share, elided
// when the source is new or dead"; types.md, Bindings).
//
// A Map and a slice are Go references, so a Go assignment shares them. The
// language says a value is seen only through the binding it was made
// through: a write through a `let mut` root (or a borrow, *T) is never
// seen through another binding, field, element, argument, capture or task.
// Only a `let mut` root and a borrow write in place, so a copy is needed
// only where a value enters one of them, or leaves one that is written
// again. The analysis below decides each place once per body, from the
// checker's facts, and the lowering reads the decisions:
//
//   - owning (rule V1): the value of `let mut x = e`, of an assignment to a
//     place rooted at a `let mut` or a borrow, and the element of an
//     in-place Append is copied unless it is fresh: a literal of fresh
//     parts, a call whose result is fresh (a function each of whose
//     results is fresh, the prelude's Append, Delete and Put on values that
//     hold no Map or slice, slices.WithCapacity), or a local that is dead
//     after the bind and was never shared (move on last use);
//   - escaping (V2): a read of a `let mut` root (a path x, x.f, x[i],
//     x[a:b]) whose value holds a Map or slice and is kept elsewhere (a
//     binding, an argument, a field or element, a capture, a match
//     subject) is copied when a write through the root can follow it: a
//     write after it in the body, a write in a loop that holds both, or a
//     write in another frame. A read that keeps nothing (len, ==,
//     interpolation, Get of a value that holds none, a pure call whose
//     result is basic) and the return of the root from its own frame are
//     never copied. A read through a borrow is always copied when kept: its
//     caller writes after the call;
//   - in place (V3): Append, Delete and Put write their source only for
//     `p = p.Append(x)`, `p = p.Delete(k)` and `p = p.Put(k, v)` on one
//     place p rooted at a `let mut` or a borrow, or on a local that is
//     dead after the call and was never shared; elsewhere Append appends
//     to a clipped slice (a new array when it grows), and Delete and Put
//     write a copy of the Map. Put's value is not owned as Append's
//     element is: a Map's value has no write path (a Map read is Get),
//     so a value Put stores is never written through the Map;
//   - captures (V4): a function literal or block that may outlive its call
//     (an escaping frame, types.md Function values 2 and 3) captures a
//     `let mut` only when no write to it follows (the checker refuses the
//     rest, owner q60), so it takes the value, not a copy of the storage
//     (closures.go, snapshot; runtime.md, Tasks 6: values copy into a
//     task);
//   - owned parameters (V5, ownedParams): a parameter that a function
//     takes into a let mut once and never reads after is copied by the
//     caller that shares it, not by the callee, and a rebinding p = f(…,
//     p, …) whose old value nothing else reads hands it over with no copy
//     (consumable): m = maps.Update(m, k, f) writes m in place, as hand Go
//     writes m[k] = f(m[k]);
//   - a Ref owns its value (V6, refown.go): the parameter of a function
//     literal written at Ref.Update or sync.Modify takes the held value
//     over, as an unshared local; the cell copies a value that may be
//     reached from outside before it hands it over.
//
// Three facts sharpen the uses: a pure call (or pure std body) whose
// result reaches no Map or slice type its argument reaches keeps nothing
// of the argument (mayShare); a part of a returned tuple or literal is a
// return; and a use in one return of a frame never runs before a use in
// another, since each ends the frame.
//
// Only a slice, a Map, and a record or tuple that holds one can be written
// through a place, so only they are copied; a union, an Option and a
// function value hold their parts behind a match or a call, which bind
// them immutably. A copy is deep through the written paths (a slice of
// Maps copies each Map) and shallow elsewhere.
//
// The reference lowering shares, as v1 did: valueLowering "alias", set by
// a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.valueLowering=alias.
// TestValueRuleAgrees proves the two equal on programs with no shared
// write and the rule equal to a value-semantics interpreter on programs
// with one. valueLowering "copy" (-X=…valueLowering=copy) is the rule
// without owned parameters (V5): each callee copies its parameter at the
// bind, as before; TestValueRuleAgrees holds it and the rule to the
// interpreter on the same programs.
//
// Prior art: Swift's Array and Dictionary are values copied on write by a
// uniqueness test of their reference count, and its `consuming` parameters
// and `consume` operator hand a value over with no copy; Hylo's mutable
// value semantics and `sink` parameters; Gleam, Elixir and ReScript maps
// and lists are immutable, so no write is shared (Erlang's maps update a
// copy, and BEAM reuses a binary appended to in place when the old one is
// dead); Rust moves a value into a parameter and refuses the shared write
// in its borrow checker. Ergo has no reference count, so it copies on a
// static fact, as Swift's optimizer removes the copies it proves unneeded,
// and hands a dead binding over as a Rust move or a Swift consume does.
var valueLowering string

// valueMutant switches off one rule of the analysis, for the mutant runs
// of TestValueRuleMutants only: "never-copy", "shallow", "elide-live" (a
// live local moves), "elide-escape" (a kept read of a root written later
// shares) and
// "in-place" (Append, Delete and Put always write their source), and for owned
// parameters (V5) "consume-live" (an argument that stays live is passed
// uncopied), "consume-captured" (a rebinding hands over a binding another
// frame reads) and "own-live" (a parameter read after its bind is owned),
// and for owned Refs (V6) "hand-nonliteral" (a function value takes the
// held value over), "store-unfresh" (every site is proved fresh),
// "share-blind" (every pure call keeps nothing) and "return-blind" (two
// parts of one returned value count as two returns). Empty in every
// build.
var valueMutant string

// valueRule reports whether the value rule lowers this build.
func valueRule() bool { return valueLowering != "alias" }

// valueFacts are the decisions of the analysis, keyed by syntax node.
type valueFacts struct {
	clone    map[syntax.Expr]bool       // values copied where they are lowered
	inPlace  map[*syntax.CallExpr]bool  // Append, Delete and Put calls that write their source
	roots    map[syntax.Node]*valueWalk // analyzed bodies
	fresh    map[*check.Func]int8       // 1 deciding, 2 fresh, 3 not fresh
	consumed map[syntax.Expr]bool       // arguments an owned parameter takes in place (V5)
	owned    map[*syntax.FuncDecl][]ownedParam
	ownCall  map[*syntax.CallExpr]bool     // calls that pass owned arguments unshared (V5)
	refSites map[*syntax.CallExpr]*refSite // Update and Modify calls whose literal owns the held value (V6)
}

// ownedParam is an owned parameter (V5): its index, counting a dot call's
// receiver first, and the value of the bind that takes it in the body.
type ownedParam struct {
	index int
	bind  syntax.Expr
}

func (l *lowerer) valueFacts() *valueFacts {
	if l.vals == nil {
		l.vals = &valueFacts{
			clone:    map[syntax.Expr]bool{},
			inPlace:  map[*syntax.CallExpr]bool{},
			roots:    map[syntax.Node]*valueWalk{},
			fresh:    map[*check.Func]int8{},
			consumed: map[syntax.Expr]bool{},
			owned:    map[*syntax.FuncDecl][]ownedParam{},
			ownCall:  map[*syntax.CallExpr]bool{},
			refSites: map[*syntax.CallExpr]*refSite{},
		}
	}
	return l.vals
}

// analyzeValues decides the copies of the body rooted at root, a function,
// test, package let value, context or machine initial value, once, with
// info, the facts of root's package.
func (l *lowerer) analyzeValues(root syntax.Node, info *check.Info) *valueWalk {
	vf := l.valueFacts()
	if w, ok := vf.roots[root]; ok {
		return w
	}
	w := &valueWalk{l: l, info: info, vf: vf, vars: map[*check.Var]*vvar{}, pre: map[syntax.Node]int{}, post: map[syntax.Node]int{}, parent: map[syntax.Node]syntax.Node{}, frameOf: map[syntax.Node]*vframe{}}
	vf.roots[root] = w
	if !valueRule() || isNilNode(root) {
		return w
	}
	w.root = &vframe{node: root, tails: map[syntax.Expr]bool{}}
	if d, ok := root.(*syntax.FuncDecl); ok && d.Body != nil {
		tailsOf(d.Body.List, w.root.tails)
	}
	w.walk(root, nil)
	w.decide()
	return w
}

func isNilNode(n syntax.Node) bool {
	switch n := n.(type) {
	case nil:
		return true
	case syntax.Expr:
		return n == nil
	}
	return false
}

// vframe is a frame of the body: the root, a function literal or a
// trailing block that is not a with's.
type vframe struct {
	node     syntax.Node
	parent   *vframe
	escaping bool
	tails    map[syntax.Expr]bool // the values its body returns
}

// inside reports whether f is g or nested in g.
func (f *vframe) inside(g *vframe) bool {
	for ; f != nil; f = f.parent {
		if f == g {
			return true
		}
	}
	return false
}

// vvar is a variable the body reads or declares.
type vvar struct {
	v      *check.Var
	local  bool // declared in this body (not a parameter, package let or capture)
	mut    bool // a let mut
	ptr    bool // a borrow, *T
	handed bool // the parameter of an owned Ref site's literal (V6): unshared, as a let mut is
	init   syntax.Expr
	frame  *vframe
	loops  []syntax.Node
	occs   []*vocc
	writes []*vocc
}

// root reports a variable writes go through: a let mut or a borrow.
func (v *vvar) root() bool { return v.mut || v.ptr }

type occKind uint8

const (
	occRead   occKind = iota // keeps nothing
	occEscape                // kept elsewhere
	occReturn                // returned from its own frame
	occWrite                 // the root of a write
	occPart                  // the source of an in-place Append, Delete or Put
)

// vocc is one use of a variable: its identifier, the largest path it
// heads (x, x.f, x[i], x[a:b], *x, a conversion), and how that path is
// used.
type vocc struct {
	id    *syntax.Ident
	path  syntax.Expr
	kind  occKind
	frame *vframe
	loops []syntax.Node
	pre   int
	at    int // for a write, when it happens: after its statement or call
}

type owning struct {
	value syntax.Expr
	root  *vvar
	whole bool // the target is the root itself, x = e
}

type valueWalk struct {
	l        *lowerer
	info     *check.Info
	vf       *valueFacts
	root     *vframe
	vars     map[*check.Var]*vvar
	order    []*vvar
	count    int
	pre      map[syntax.Node]int
	post     map[syntax.Node]int
	parent   map[syntax.Node]syntax.Node
	frameOf  map[syntax.Node]*vframe
	frames   []*vframe
	owns     []owning
	calls    []*syntax.CallExpr       // prelude Append, Delete and Put calls
	ergo     []*syntax.CallExpr       // calls of functions with an Ergo body (V5)
	args     []syntax.Expr            // arguments of owned parameters (V5)
	goArgs   []syntax.Expr            // operands of Go calls of import go that hold a Map or slice (gocall.go)
	sites    []*refSite               // owned Ref sites (V6, refown.go)
	handOver map[*syntax.FuncLit]bool // their literals
}

// children are n's direct children, in Inspect's order.
func children(n syntax.Node) []syntax.Node {
	var out []syntax.Node
	syntax.Inspect(n, func(c syntax.Node) bool {
		if c == n {
			return true
		}
		out = append(out, c)
		return false
	})
	return out
}

func (w *valueWalk) varOf(v *check.Var) *vvar {
	vv := w.vars[v]
	if vv == nil {
		vv = &vvar{v: v}
		_, vv.ptr = v.Type.(*check.Pointer)
		w.vars[v] = vv
		w.order = append(w.order, vv)
	}
	return vv
}

// walk visits n with the frame and loops that hold it.
func (w *valueWalk) walk(n syntax.Node, loops []syntax.Node) {
	w.pre[n] = w.count
	w.count++
	frame := w.root
	if len(w.frames) > 0 {
		frame = w.frames[len(w.frames)-1]
	}
	if opens, escaping := w.opens(n); opens {
		f := &vframe{node: n, parent: frame, escaping: escaping}
		w.frameOf[n] = f
		w.frames = append(w.frames, f)
		frame = f
		defer func() { w.frames = w.frames[:len(w.frames)-1] }()
	}
	switch n := n.(type) {
	case *syntax.Ident:
		if v, ok := w.info.Uses[n].(*check.Var); ok {
			vv := w.varOf(v)
			vv.occs = append(vv.occs, &vocc{id: n, frame: frame, loops: loops, pre: w.pre[n]})
		}
	case *syntax.DeclStmt:
		for _, spec := range n.Decl.Specs {
			for i, id := range spec.Names {
				v, ok := w.info.Defs[id].(*check.Var)
				if !ok {
					continue
				}
				vv := w.varOf(v)
				vv.local, vv.mut, vv.frame, vv.loops = true, n.Decl.Mut.IsValid(), frame, loops
				if spec.Pattern == nil && len(spec.Names) == 1 && len(spec.Values) == 1 && i == 0 {
					vv.init = spec.Values[0]
					if vv.mut {
						w.owns = append(w.owns, owning{value: spec.Values[0], root: vv})
					}
				}
			}
		}
	case *syntax.AssignStmt:
		if n.Tok == syntax.ASSIGN && len(n.Lhs) == 1 && len(n.Rhs) == 1 {
			if id := pathRoot(n.Lhs[0]); id != nil {
				if v, ok := w.info.Uses[id].(*check.Var); ok {
					w.owns = append(w.owns, owning{value: n.Rhs[0], root: w.varOf(v), whole: unparen(n.Lhs[0]) == syntax.Expr(id)})
				}
			}
		}
	case *syntax.FuncLit:
		w.handParams(n, frame)
	case *syntax.CallExpr:
		w.refCall(n)
		if c := w.info.Calls[n]; c != nil && c.Kind == check.CallFunc && (preludeAppend(c.Func) || preludeMapWrite(c.Func)) {
			w.calls = append(w.calls, n)
		} else if c != nil && c.Kind == check.CallFunc && c.Func != nil && c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil {
			w.ergo = append(w.ergo, n)
		} else if c != nil && (goImportCall(c, w.typeOf) || goExternCall(c)) {
			list := n.Args
			if c.Recv != nil {
				list = append([]syntax.Expr{c.Recv}, list...)
			}
			for _, a := range list {
				if goShared(w.typeOf(a)) {
					w.goArgs = append(w.goArgs, a)
				}
			}
		}
	}
	if f := w.frameOf[n]; f != nil {
		f.tails = map[syntax.Expr]bool{}
		switch n := n.(type) {
		case *syntax.FuncDecl:
			if n.Body != nil {
				tailsOf(n.Body.List, f.tails)
			}
		case *syntax.FuncLit:
			tailsOf(n.Body.List, f.tails)
		case *syntax.BlockStmt:
			tailsOf(n.List, f.tails)
		}
	}
	inner := loops
	switch n.(type) {
	case *syntax.ForStmt:
		inner = append(append([]syntax.Node{}, loops...), n)
	}
	for _, c := range children(n) {
		w.parent[c] = n
		cl := inner
		if r, ok := n.(*syntax.RangeStmt); ok && c != syntax.Node(r.X) {
			cl = append(append([]syntax.Node{}, loops...), n)
		}
		if w.frameOf[n] != nil {
			cl = nil // a new frame: its loops are its own
		}
		w.walk(c, cl)
	}
	w.post[n] = w.count
	w.count++
}

// tailsOf adds the values a body yields as its result: its last
// statement's value, through if and match arms; explicit returns are read
// at their statements.
func tailsOf(list []syntax.Stmt, out map[syntax.Expr]bool) {
	if len(list) == 0 {
		return
	}
	switch s := list[len(list)-1].(type) {
	case *syntax.ExprStmt:
		switch x := unparen(s.X).(type) {
		case *syntax.IfStmt:
			tailsOfIf(x, out)
		case *syntax.SwitchStmt:
			for _, c := range x.Clauses {
				tailsOf(c.Body, out)
			}
		case *syntax.TypeSwitchStmt:
			for _, c := range x.Clauses {
				tailsOf(c.Body, out)
			}
		default:
			out[s.X] = true
		}
	case *syntax.IfStmt:
		tailsOfIf(s, out)
	case *syntax.SwitchStmt:
		for _, c := range s.Clauses {
			tailsOf(c.Body, out)
		}
	case *syntax.TypeSwitchStmt:
		for _, c := range s.Clauses {
			tailsOf(c.Body, out)
		}
	case *syntax.BlockStmt:
		tailsOf(s.List, out)
	}
}

func tailsOfIf(s *syntax.IfStmt, out map[syntax.Expr]bool) {
	tailsOf(s.Body.List, out)
	switch e := s.Else.(type) {
	case *syntax.BlockStmt:
		tailsOf(e.List, out)
	case *syntax.IfStmt:
		tailsOfIf(e, out)
	}
}

// opens reports whether n opens a frame and whether it may outlive the
// call it is passed to, as the checker decides (check/escaping.go).
func (w *valueWalk) opens(n syntax.Node) (frame, escaping bool) {
	switch n := n.(type) {
	case *syntax.FuncLit:
		parent, child := w.unparenParent(n)
		call, ok := parent.(*syntax.CallExpr)
		if !ok {
			return true, !w.info.LocalFrames[n]
		}
		if call.Fun == child {
			return true, false
		}
		for i, a := range call.Args {
			if a == child {
				return true, !w.paramNonEscaping(call, i)
			}
		}
		return true, true
	case *syntax.BlockStmt:
		call, ok := w.parent[n].(*syntax.CallExpr)
		if !ok || call.Trailing != n {
			return false, false
		}
		if k := w.info.Calls[call]; k != nil && k.Kind == check.CallWith {
			return false, false
		}
		return true, !w.paramNonEscaping(call, len(call.Args))
	}
	return false, false
}

// unparenParent is n's nearest ancestor that is not a parenthesis, with
// the child on the path.
func (w *valueWalk) unparenParent(n syntax.Node) (parent, child syntax.Node) {
	child = n
	for {
		p := w.parent[child]
		if _, ok := p.(*syntax.ParenExpr); ok {
			child = p
			continue
		}
		return p, child
	}
}

// paramNonEscaping is the checker's rule (check/escaping.go).
func (w *valueWalk) paramNonEscaping(call *syntax.CallExpr, i int) bool {
	k := w.info.Calls[call]
	if k == nil {
		return true
	}
	var sig *check.Signature
	switch k.Kind {
	case check.CallFunc:
		if k.Func != nil {
			sig = k.Func.Sig
		}
		if k.Recv != nil {
			i++
		}
	case check.CallOp, check.CallProviderOp, check.CallLawOp:
		if k.Op != nil {
			sig = k.Op.Sig
		}
	case check.CallFuncValue:
		t := w.info.Types[call.Fun]
		if n, ok := t.(*check.Named); ok {
			t = n.Underlying()
		}
		sig, _ = t.(*check.Signature)
	}
	if sig == nil || i >= len(sig.Params) {
		return false
	}
	s, ok := sig.Params[i].Type.(*check.Signature)
	return ok && s.NonEscaping
}

// pathRoot is the variable that heads the place or path x: x, x.f, x[i],
// x[a:b], *x, in parentheses; nil for any other expression.
func pathRoot(x syntax.Expr) *syntax.Ident {
	for {
		switch e := x.(type) {
		case *syntax.Ident:
			return e
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.SliceExpr:
			x = e.X
		case *syntax.StarExpr:
			x = e.X
		default:
			return nil
		}
	}
}

func preludeAppend(f *check.Func) bool {
	return f != nil && f.Pkg != nil && f.Pkg.Path == "prelude" && f.Decl != nil && f.Decl.Extern != nil && f.Name == "Append"
}

// preludeMapWrite reports the prelude's Delete or Put: a call that writes
// its Map source when it may (V3), and writes a copy of it otherwise.
func preludeMapWrite(f *check.Func) bool {
	return f != nil && f.Pkg != nil && f.Pkg.Path == "prelude" && f.Decl != nil && f.Decl.Extern != nil && (f.Name == "Delete" || f.Name == "Put")
}

func (w *valueWalk) typeOf(x syntax.Expr) check.Type {
	t, _ := typedIn(w.info, x)
	return t
}

// holds reports whether a write through a place of type t can reach a Map
// or a slice: t is one, or a record or tuple with a field that holds one.
func (l *lowerer) holdsValue(t check.Type) bool {
	return holdsValueSeen(t, map[check.Type]bool{})
}

func holdsValueSeen(t check.Type, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Slice, *check.Map:
		return true
	case *check.Array:
		// Go copies an array as a whole: it needs a copy function only
		// when an element holds a slice or a Map (196y).
		return holdsValueSeen(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if holdsValueSeen(e, seen) {
				return true
			}
		}
	case *check.Struct:
		for _, f := range u.Fields {
			if holdsValueSeen(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// elemOf is the element of a slice or the value of a Map.
func elemOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Slice:
		return u.Elem
	case *check.Array:
		return u.Elem
	case *check.Map:
		return u.Elem
	}
	return nil
}

// recvOf is the source operand of a call of Append, Delete or Put.
func (w *valueWalk) recvOf(call *syntax.CallExpr) syntax.Expr {
	c := w.info.Calls[call]
	if c.Recv != nil {
		return c.Recv
	}
	if len(call.Args) > 0 {
		return call.Args[0]
	}
	return nil
}

// decide classifies every use, then marks the copies, the in-place calls
// and the snapshots.
func (w *valueWalk) decide() {
	vf := w.vf
	// In-place pattern: p = p.Append(x), p = p.Delete(k) or p = p.Put(k, v), one place
	// rooted at a let mut or a borrow.
	for _, call := range w.calls {
		parent, child := w.unparenParent(call)
		s, ok := parent.(*syntax.AssignStmt)
		if !ok || s.Tok != syntax.ASSIGN || len(s.Rhs) != 1 || s.Rhs[0] != child {
			continue
		}
		recv := w.recvOf(call)
		id := pathRoot(s.Lhs[0])
		if id == nil || !samePlace(s.Lhs[0], recv) {
			continue
		}
		if v, ok := w.info.Uses[id].(*check.Var); ok && w.varOf(v).root() {
			vf.inPlace[call] = true
		}
	}
	// Owned parameters (V5): each argument of one, and the arguments a
	// rebinding hands over in place.
	for _, call := range w.ergo {
		c := w.info.Calls[call]
		owned := w.l.ownedParams(c.Func)
		if len(owned) > 0 {
			vf.ownCall[call] = true
		}
		for _, p := range owned {
			if arg := argOf(call, c, p.index); arg != nil {
				w.args = append(w.args, arg)
				if w.consumable(call, arg) {
					vf.consumed[arg] = true
				}
			}
		}
	}
	for _, vv := range w.order {
		for _, o := range vv.occs {
			w.classify(vv, o)
			if o.kind == occWrite {
				vv.writes = append(vv.writes, o)
			}
		}
	}
	// Escapes (V2).
	if valueMutant != "never-copy" {
		for _, vv := range w.order {
			if !vv.root() {
				continue
			}
			for _, o := range vv.occs {
				if o.kind == occEscape || o.kind == occReturn {
					if w.needsCopy(vv, o) {
						vf.clone[o.path] = true
					}
				}
			}
		}
	}
	// In place on a dead, unshared source (V3).
	for _, call := range w.calls {
		if vf.inPlace[call] {
			continue
		}
		if id, ok := unparen(w.recvOf(call)).(*syntax.Ident); ok {
			if v, ok := w.info.Uses[id].(*check.Var); ok && w.freshIdent(w.varOf(v), id, false) {
				vf.inPlace[call] = true
			}
		}
	}
	// Owning (V1). A bind that takes an owned parameter of this function
	// copies nothing: its callers pass the value unshared (V5).
	if valueMutant != "never-copy" {
		takes := map[syntax.Expr]bool{}
		if d, ok := w.root.node.(*syntax.FuncDecl); ok {
			if f, ok := w.info.Defs[d.Name].(*check.Func); ok {
				for _, p := range w.l.ownedParams(f) {
					takes[p.bind] = true
				}
			}
		}
		for _, o := range w.owns {
			if !o.root.root() || takes[o.value] {
				continue
			}
			if call, ok := unparen(o.value).(*syntax.CallExpr); ok && vf.inPlace[call] {
				if c := w.info.Calls[call]; c != nil && preludeAppend(c.Func) {
					args := call.Args
					if c.Recv == nil && len(args) > 0 {
						args = args[1:]
					}
					for _, a := range args {
						w.freshen(a)
					}
				}
				continue
			}
			if o.whole && w.slicesOf(o.value, o.root) {
				continue
			}
			w.freshen(o.value)
		}
		// An argument of an owned parameter enters the callee's root: it
		// is copied unless fresh, or handed over by a rebinding (V5).
		for _, arg := range w.args {
			if !vf.consumed[arg] && valueMutant != "consume-live" {
				w.freshen(arg)
			}
		}
	}
	// An operand of a Go call (import go) that holds a Map or slice goes
	// where Go may write or keep it: copied unless fresh or a local dead
	// after the call, and always under the reference lowering (gocall.go).
	for _, a := range w.goArgs {
		if hostCopyAlways {
			vf.clone[a] = true
		} else {
			w.freshen(a)
		}
	}
	// Owned Ref sites (V6), once every copy is decided.
	w.decideSites()
}

// valueOwned reports whether owned parameters (V5) lower this build: the
// value rule does, and the copying lowering "copy" is not selected.
func valueOwned() bool { return valueRule() && valueLowering != "copy" }

// argOf is the operand of call c for parameter i, counting a dot call's
// receiver first, or nil (a trailing block).
func argOf(call *syntax.CallExpr, c *check.Call, i int) syntax.Expr {
	if c.Recv != nil {
		if i == 0 {
			return c.Recv
		}
		i--
	}
	if i < len(call.Args) {
		return call.Args[i]
	}
	return nil
}

// consumable reports a rebinding p = f(…, p, …) (or p = try f(…)) of one
// place p, rooted at a let mut local of this body that no other frame
// reads or writes, with no other use of the local in the statement: the
// old value of p is dead once the call starts, so the owned parameter
// takes it with no copy, as hand Go writes m[k] = v for
// m = maps.Update(m, k, f).
func (w *valueWalk) consumable(call *syntax.CallExpr, arg syntax.Expr) bool {
	parent, child := w.unparenParent(call)
	if t, ok := parent.(*syntax.TryExpr); ok && t.X == child {
		parent, child = w.unparenParent(t)
	}
	s, ok := parent.(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != child {
		return false
	}
	lid, aid := pathRoot(s.Lhs[0]), pathRoot(arg)
	if lid == nil || aid == nil || !samePlace(s.Lhs[0], arg) {
		return false
	}
	v, ok := w.info.Uses[aid].(*check.Var)
	if !ok || w.info.Uses[lid] != check.Object(v) {
		return false
	}
	vv := w.varOf(v)
	if !vv.local || !vv.mut || vv.ptr {
		return false
	}
	for _, o := range vv.occs {
		if o.frame != vv.frame {
			if valueMutant == "consume-captured" {
				continue
			}
			return false
		}
		if o.id != lid && o.id != aid && o.pre > w.pre[s] && o.pre < w.post[s] {
			return false
		}
	}
	return true
}

// ownedParams are the owned parameters of f (V5), in order: a parameter
// whose value holds a slice or Map and that f's body takes into a let mut
// local once, at a bind in the body's own frame and outside any loop, and
// otherwise only reads, before the bind, in uses that keep nothing. After
// the bind no failure can leave f: no call that can fail at its site, and
// no returned value of an error type, so a caller never sees a value f
// wrote and then failed on. f lowers as a Go function, not a method.
//
// Such an f lowers twice (emitFuncBody): its body, under the name
// ownedName, whose bind copies nothing, and a wrapper under f's own name
// that copies each owned argument and calls it. A call this analysis
// decided (ownedCall) calls the body and passes each owned argument
// unshared: a copy unless it is fresh, a dead local, or the old value a
// rebinding hands over (consumable). Every other route to f (a function
// value, an operations conversion, a witness, a context protocol) reaches
// the wrapper, which copies as before. The copy moves from the callee to
// the caller that shares, and a rebinding needs none.
func (l *lowerer) ownedParams(fn *check.Func) []ownedParam {
	if !valueOwned() || fn == nil || fn.Decl == nil {
		return nil
	}
	d := fn.Decl
	vf := l.valueFacts()
	if p, ok := vf.owned[d]; ok {
		return p
	}
	vf.owned[d] = nil
	if d.Body == nil || d.Extern != nil || fn.Pkg == nil || fn.Pkg.Info == nil || d.Type == nil || d.Type.Params == nil {
		return nil
	}
	info := fn.Pkg.Info
	f, ok := info.Defs[d.Name].(*check.Func)
	if !ok || f.Sig == nil {
		return nil
	}
	saved := l.info
	l.info = info
	defer func() { l.info = saved }()
	w := &valueWalk{l: l, info: info, vf: vf, vars: map[*check.Var]*vvar{}, pre: map[syntax.Node]int{}, post: map[syntax.Node]int{}, parent: map[syntax.Node]syntax.Node{}, frameOf: map[syntax.Node]*vframe{}}
	w.root = &vframe{node: d, tails: map[syntax.Expr]bool{}}
	tailsOf(d.Body.List, w.root.tails)
	w.walk(d, nil)
	var out []ownedParam
	i := 0
	for _, field := range d.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			names = []*syntax.Ident{nil}
		}
		for _, id := range names {
			if id != nil {
				if v, ok := info.Defs[id].(*check.Var); ok && w.vars[v] != nil && l.holdsValue(v.Type) {
					if bind := w.ownedBind(f, w.vars[v]); bind != nil {
						out = append(out, ownedParam{index: i, bind: bind})
					}
				}
			}
			i++
		}
	}
	if len(out) > 0 && l.funcDecl(fn).method != nil {
		out = nil
	}
	vf.owned[d] = out
	return out
}

// ownedBind is the value of the one bind that takes the parameter vv of f
// (ownedParams), or nil.
func (w *valueWalk) ownedBind(f *check.Func, vv *vvar) syntax.Expr {
	if vv.ptr || vv.local {
		return nil
	}
	var bind *vocc
	var value syntax.Expr
	for _, o := range w.owns {
		id, ok := unparen(o.value).(*syntax.Ident)
		if !ok || w.info.Uses[id] != check.Object(vv.v) {
			continue
		}
		if bind != nil || !o.root.local || !o.root.mut || o.root.ptr {
			return nil
		}
		for _, oc := range vv.occs {
			if oc.id == id {
				bind = oc
			}
		}
		value = o.value
	}
	if bind == nil || bind.frame != w.root || len(bind.loops) > 0 {
		return nil
	}
	for _, o := range vv.occs {
		if o == bind {
			continue
		}
		if o.frame != w.root || o.pre > bind.pre && valueMutant != "own-live" {
			return nil
		}
		w.classify(vv, o)
		if o.kind != occRead {
			return nil
		}
	}
	if len(rowElems(f.Sig.Errors)) > 0 && w.failsAfter(f, bind.pre) {
		return nil
	}
	return value
}

// failsAfter reports whether f's body can fail after the point at: a call
// that can fail at its site, or a returned value whose type is not f's
// result (an error value converts to the row).
func (w *valueWalk) failsAfter(f *check.Func, at int) bool {
	fails := false
	result := check.Type(nil)
	if len(f.Sig.Results) == 1 {
		result = f.Sig.Results[0]
	}
	returned := func(x syntax.Expr) {
		if w.pre[x] <= at {
			return
		}
		switch t := w.typeOf(x); t.(type) {
		case *check.Named, *check.TypeParam:
			// An error type is named, or a type parameter of the row.
			if t != result {
				fails = true
			}
		}
	}
	for x := range w.root.tails {
		returned(x)
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			if c := w.info.Calls[n]; c != nil && w.pre[n] > at && failsHere(c) {
				fails = true
			}
		case *syntax.ReturnStmt:
			if w.frameOfReturn(n) == w.root {
				for _, x := range n.Results {
					returned(x)
				}
			}
		}
		return true
	})
	return fails
}

// ownedKey is the package-scope key of the body of f, which has owned
// parameters (ownedParams); f's own key names its wrapper.
func ownedKey(f *check.Func) pkgKey { return pkgKey{"owned", f} }

// reserveOwned gives, in the naming pass, the name of the body of each
// function of pkgs that has owned parameters.
func (l *lowerer) reserveOwned(pkgs []*check.Package) {
	if !valueOwned() {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if len(l.ownedParams(f)) > 0 {
				l.pkgName(ownedKey(f), f.Name+"Owned")
			}
		}
	}
}

// ownedWrapper is the Go declaration of f's wrapper (ownedParams): head is
// f's Go head up to its parameters' opening parenthesis, params its
// parameter list, results its result list, typeArgs the names of its type
// parameters, and args its Go operands in order (the environment, the
// destination and the witnesses first, then one per parameter; the wrapper
// copies each owned one). It calls body, the owned body.
func (fc *funcCtx) ownedWrapper(f *check.Func, head, params, results, body string, typeArgs []string, args []string, site syntax.Node) string {
	owned := map[int]bool{}
	for _, p := range fc.l.ownedParams(f) {
		owned[p.index] = true
	}
	lead := len(args) - len(f.Sig.Params)
	var ops []string
	for i, a := range args {
		if j := i - lead; j >= 0 && owned[j] {
			a = fc.cloneValue(f.Sig.Params[j].Type, a, site)
		}
		ops = append(ops, a)
	}
	if len(typeArgs) > 0 {
		body += "[" + strings.Join(typeArgs, ", ") + "]"
	}
	call := body + "(" + strings.Join(ops, ", ") + ")"
	if strings.TrimSpace(results) != "" {
		call = "return " + call
	}
	return head + params + results + " {\n\t" + call + "\n}\n\n"
}

// samePlace reports whether a and b are one place: a variable and its
// fields, through parentheses and indirections.
func samePlace(a, b syntax.Expr) bool {
	a, b = unparen(a), unparen(b)
	if sa, ok := a.(*syntax.StarExpr); ok {
		a = unparen(sa.X)
	}
	if sb, ok := b.(*syntax.StarExpr); ok {
		b = unparen(sb.X)
	}
	switch x := a.(type) {
	case *syntax.Ident:
		y, ok := b.(*syntax.Ident)
		return ok && x.Name == y.Name
	case *syntax.SelectorExpr:
		y, ok := b.(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && samePlace(x.X, y.X)
	}
	return false
}

// slicesOf reports x = x[a:b] (any depth of slicing), which keeps x's own
// array: the old value is overwritten, so nothing else shares it.
func (w *valueWalk) slicesOf(e syntax.Expr, root *vvar) bool {
	s, ok := unparen(e).(*syntax.SliceExpr)
	if !ok {
		return false
	}
	for {
		switch x := unparen(s.X).(type) {
		case *syntax.Ident:
			v, ok := w.info.Uses[x].(*check.Var)
			return ok && w.vars[v] == root
		case *syntax.SliceExpr:
			s = x
		default:
			return false
		}
	}
}

// classify sets o's path and kind.
func (w *valueWalk) classify(vv *vvar, o *vocc) {
	cur := syntax.Node(o.id)
	for {
		p := w.parent[cur]
		switch p := p.(type) {
		case *syntax.ParenExpr:
			cur = p
			continue
		case *syntax.SelectorExpr:
			if p.X == cur && w.dotCall(cur) == nil {
				cur = p
				continue
			}
		case *syntax.IndexExpr:
			if p.X == cur && len(p.Indices) == 1 {
				cur = p
				continue
			}
		case *syntax.SliceExpr:
			if p.X == cur {
				cur = p
				continue
			}
		case *syntax.StarExpr:
			if p.X == cur {
				cur = p
				continue
			}
		case *syntax.CallExpr:
			if c := w.info.Calls[p]; c != nil && c.Kind == check.CallConversion && len(p.Args) == 1 && p.Args[0] == cur {
				cur = p
				continue
			}
		}
		break
	}
	o.path = cur.(syntax.Expr)
	parent, child := w.unparenParent(cur)
	if call := w.dotCall(cur); call != nil {
		// The receiver of a dot call x.f(…) is its first operand.
		parent, child = call, cur
	}
	o.kind = occEscape
	switch p := parent.(type) {
	case *syntax.AssignStmt:
		for _, l := range p.Lhs {
			if l == child {
				o.kind, o.at = occWrite, w.post[p]
				return
			}
		}
	case *syntax.UnaryExpr:
		if p.Op == syntax.AND {
			o.kind, o.at = occWrite, w.post[w.enclosingCall(p)]
			return
		}
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c != nil && c.Kind == check.CallFunc && c.Recv == child && c.Func.Ptr {
			if _, isPtr := w.typeOf(o.path).(*check.Pointer); !isPtr {
				o.kind, o.at = occWrite, w.post[p]
				return
			}
		}
		if c != nil && w.vf.inPlace[p] && w.recvOf(p) == child {
			o.kind = occPart
			return
		}
		if x, ok := child.(syntax.Expr); ok && w.vf.consumed[x] {
			o.kind = occPart
			return
		}
	}
	t := w.typeOf(o.path)
	if !w.l.holdsValue(t) {
		o.kind = occRead
		return
	}
	switch p := parent.(type) {
	case *syntax.BinaryExpr:
		if p.Op == syntax.EQL || p.Op == syntax.NEQ {
			o.kind = occRead
		}
	case *syntax.InterpolatedString:
		o.kind = occRead
	case *syntax.RangeStmt:
		if p.X == child {
			if l := w.info.Loops[p]; l != nil && !w.l.holdsValue(l.Elem) {
				o.kind = occRead
			}
		}
	case *syntax.ReturnStmt:
		if o.frame.node != nil && w.frameOfReturn(p) == o.frame {
			o.kind = occReturn
		}
	case *syntax.ExprStmt:
		if f := w.frameOfTail(p.X); f != nil && f == o.frame {
			o.kind = occReturn
		}
	case *syntax.TupleExpr, *syntax.CompositeLit, *syntax.KeyValueExpr:
		// A part of the value its frame returns leaves with the frame, as
		// the value itself does (V6: write's (Changed, u)).
		if w.returnNode(o, o.path) != nil {
			o.kind = occReturn
		}
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c == nil {
			break
		}
		switch {
		case c.Kind == check.CallLen:
			o.kind = occRead
		case c.Kind == check.CallFunc && preludeGet(c.Func):
			if w.recvOf(p) == child && !w.l.holdsValue(elemOf(t)) {
				o.kind = occRead
			}
		case c.Kind == check.CallFunc && (preludeAppend(c.Func) || preludeMapWrite(c.Func)):
			if w.recvOf(p) == child && !w.l.holdsValue(elemOf(t)) {
				o.kind = occRead
			}
		case c.Kind == check.CallFunc && c.Func != nil && basicResult(w.typeOf(p)) && w.l.isPure(c.Func):
			o.kind = occRead
		case c.Kind == check.CallFunc && c.Func != nil && (w.l.isPure(c.Func) || pureStdBody(c.Func)) && (valueMutant == "share-blind" || !w.l.mayShare(w.typeOf(p), t)):
			// A pure call keeps nothing of its argument past its result,
			// and a result that reaches no Map or slice type the argument
			// reaches shares none of it (V6: find(t, path) is a place).
			o.kind = occRead
		}
	}
}

// dotCall is the call whose receiver is x, a dot call x.f(…) or an
// operation call, or nil.
func (w *valueWalk) dotCall(x syntax.Node) *syntax.CallExpr {
	sel, ok := w.parent[x].(*syntax.SelectorExpr)
	if !ok || syntax.Node(sel.X) != x {
		return nil
	}
	parent, child := w.unparenParent(sel)
	call, ok := parent.(*syntax.CallExpr)
	if !ok || call.Fun != child {
		return nil
	}
	if c := w.info.Calls[call]; c != nil && c.Recv != nil && syntax.Node(c.Recv) == x {
		return call
	}
	return nil
}

// basicResult reports a result that can keep no part of an argument: a
// basic type, or no value.
func basicResult(t check.Type) bool {
	if t == nil {
		return true
	}
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Tuple:
		return len(u.Types) == 0
	}
	return false
}

// enclosingCall is the nearest call that holds n, or n.
func (w *valueWalk) enclosingCall(n syntax.Node) syntax.Node {
	for p := w.parent[n]; p != nil; p = w.parent[p] {
		if _, ok := p.(*syntax.CallExpr); ok {
			return p
		}
	}
	return n
}

// frameOfReturn is the frame a return statement leaves.
func (w *valueWalk) frameOfReturn(n syntax.Node) *vframe {
	for p := n; p != nil; p = w.parent[p] {
		if f := w.frameOf[p]; f != nil {
			return f
		}
	}
	return w.root
}

// frameOfTail is the frame whose result x is, or nil.
func (w *valueWalk) frameOfTail(x syntax.Expr) *vframe {
	f := w.frameOfReturn(x)
	if f.tails[x] {
		return f
	}
	return nil
}

// later reports whether the write wr can run after the use o of vv.
func (w *valueWalk) later(vv *vvar, o, wr *vocc) bool {
	if wr.frame != o.frame {
		return true
	}
	if valueMutant == "elide-escape" {
		return false
	}
	if wr.at > o.pre {
		return true
	}
	for _, l := range o.loops {
		if !containsNode(vv.loops, l) && containsNode(wr.loops, l) {
			return true
		}
	}
	return false
}

func containsNode(list []syntax.Node, n syntax.Node) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// needsCopy reports whether the kept use o of the root vv must copy.
func (w *valueWalk) needsCopy(vv *vvar, o *vocc) bool {
	if vv.ptr {
		return true // the caller writes after the call
	}
	if !vv.mut {
		return false
	}
	if w.snapshotted(vv, o.frame) {
		return false
	}
	for _, wr := range vv.writes {
		if wr.frame != vv.frame {
			return true // a write in another frame can run at any time
		}
	}
	if o.frame != vv.frame {
		return len(vv.writes) > 0
	}
	if o.kind == occReturn {
		return false
	}
	for _, wr := range vv.writes {
		if w.later(vv, o, wr) {
			return true
		}
	}
	return false
}

// snapshotted reports whether frame f sees a copy of vv taken when an
// enclosing escaping frame was made (the checker's value captures,
// Info.ValueCaptures; types.md, Function values 3): no write to vv
// follows that capture, so a read of the copy keeps a value no write
// changes.
func (w *valueWalk) snapshotted(vv *vvar, f *vframe) bool {
	for ; f != nil && f != vv.frame; f = f.parent {
		for _, s := range w.info.ValueCaptures[f.node] {
			if s == vv.v {
				return true
			}
		}
	}
	return false
}

// freshen makes the value e, which enters a root, unshared: nothing when
// it is fresh, its parts when it is a literal, else a copy.
func (w *valueWalk) freshen(e syntax.Expr) {
	if e == nil || !w.l.holdsValue(w.typeOf(e)) || w.fresh(e) {
		return
	}
	switch x := e.(type) {
	case *syntax.ParenExpr:
		w.freshen(x.X)
		return
	case *syntax.CompositeLit:
		if x.Base != nil {
			w.freshen(x.Base)
		}
		for _, el := range x.Elts {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				w.freshen(kv.Value)
			} else {
				w.freshen(el)
			}
		}
		return
	case *syntax.TupleExpr:
		for _, el := range x.Elts {
			w.freshen(el)
		}
		return
	}
	w.vf.clone[e] = true
}

// fresh reports whether no other live binding reaches a Map or slice of
// e's value.
func (w *valueWalk) fresh(e syntax.Expr) bool {
	if e == nil {
		return true
	}
	if !w.l.holdsValue(w.typeOf(e)) || w.vf.clone[e] {
		return true
	}
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return w.fresh(x.X)
	case *syntax.Ident:
		if v, ok := w.info.Uses[x].(*check.Var); ok {
			return w.freshIdent(w.varOf(v), x, false)
		}
		return false
	case *syntax.SelectorExpr, *syntax.IndexExpr, *syntax.SliceExpr, *syntax.StarExpr:
		if id := pathRoot(x); id != nil && w.parentIsPath(id, x) {
			if v, ok := w.info.Uses[id].(*check.Var); ok {
				return w.freshIdent(w.varOf(v), id, false)
			}
		}
		return false
	case *syntax.CompositeLit:
		if x.Base != nil && !w.fresh(x.Base) {
			return false
		}
		for _, el := range x.Elts {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				el = kv.Value
			}
			if !w.fresh(el) {
				return false
			}
		}
		return true
	case *syntax.TupleExpr:
		for _, el := range x.Elts {
			if !w.fresh(el) {
				return false
			}
		}
		return true
	case *syntax.TryExpr:
		return w.fresh(x.X)
	case *syntax.CallExpr:
		c := w.info.Calls[x]
		if c == nil {
			return false
		}
		switch c.Kind {
		case check.CallConversion:
			return len(x.Args) == 1 && w.fresh(x.Args[0])
		case check.CallFunc:
			switch {
			case preludeAppend(c.Func) || preludeMapWrite(c.Func):
				if w.vf.inPlace[x] {
					return w.fresh(w.recvOf(x))
				}
				return !w.l.holdsValue(elemOf(w.typeOf(x)))
			case c.Func != nil && c.Func.Pkg != nil && c.Func.Pkg.Path == "slices" && c.Func.Name == "WithCapacity":
				return true
			}
			return w.l.freshResult(c.Func)
		}
	}
	return false
}

// parentIsPath reports whether x is the path its root id heads.
func (w *valueWalk) parentIsPath(id *syntax.Ident, x syntax.Expr) bool {
	return pathRoot(x) == id
}

// freshIdent reports whether the use id of vv may take its value without
// a copy: vv is a local of this frame, read nowhere else after this use
// and never shared before it, captured by no other frame, and its value
// is unshared (a let mut always is; a let when its value was fresh). At a
// return the frame ends, so later uses do not count.
func (w *valueWalk) freshIdent(vv *vvar, id *syntax.Ident, _ bool) bool {
	if valueMutant == "elide-live" && vv.local {
		return true
	}
	if !vv.local || vv.ptr {
		return false
	}
	var use *vocc
	for _, o := range vv.occs {
		if o.id == id {
			use = o
		}
	}
	if use == nil || use.frame != vv.frame {
		return false
	}
	// A use that is the value, or a part of the value, its frame returns
	// ends the frame: a use after it never runs, nor does one in another
	// return of the frame, which ends it too. A use in the same returned
	// value counts (the two parts would share).
	ret := w.returnNode(use, use.id)
	returning := ret != nil
	if !returning {
		for _, l := range use.loops {
			if !containsNode(vv.loops, l) {
				return false
			}
		}
	}
	for _, o := range vv.occs {
		if o == use {
			continue
		}
		if o.frame != vv.frame {
			return false
		}
		// An earlier use in a returned value that does not hold use ends
		// the frame when it runs, so use does not run after it.
		if other := w.returnNode(o, o.id); other != nil && o.pre < use.pre {
			if !(w.pre[other] <= use.pre && use.pre < w.post[other]) || valueMutant == "return-blind" {
				continue
			}
		}
		if returning {
			inRet := w.pre[ret] <= o.pre && o.pre < w.post[ret]
			if !inRet && o.pre > use.pre || valueMutant == "return-blind" && inRet {
				continue
			}
			if inRet && o.pre > use.pre {
				return false
			}
		} else if o.pre > use.pre {
			return false
		}
		if o.kind == occEscape || o.kind == occReturn {
			if !w.vf.clone[o.path] {
				return false
			}
		}
	}
	if vv.mut || vv.handed {
		return true
	}
	return vv.init != nil && w.fresh(vv.init)
}

// freshResult reports whether every result of f is fresh: an Ergo body
// whose every returned value is fresh in it. A function being decided is
// not fresh (a recursive result is shared until proved otherwise).
func (l *lowerer) freshResult(f *check.Func) bool {
	vf := l.valueFacts()
	switch vf.fresh[f] {
	case 1, 3:
		return false
	case 2:
		return true
	}
	if f != nil && f.Pkg.Host() {
		// A Go call's Map or slice result is new (freshGoResults) or
		// copied at the call (goResult).
		return true
	}
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		vf.fresh[f] = 3
		return false
	}
	vf.fresh[f] = 1
	saved := l.info
	l.info = f.Pkg.Info
	w := l.analyzeValues(f.Decl, f.Pkg.Info)
	ok := w.root != nil && w.resultsFresh()
	l.info = saved
	vf.fresh[f] = 3
	if ok {
		vf.fresh[f] = 2
	}
	return ok
}

// resultsFresh reports whether every value the root frame returns is
// fresh.
func (w *valueWalk) resultsFresh() bool {
	ok := true
	for x := range w.root.tails {
		if !w.fresh(x) {
			ok = false
		}
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		if w.frameOf[n] != nil && w.frameOf[n] != w.root {
			return false
		}
		if r, isRet := n.(*syntax.ReturnStmt); isRet {
			for _, x := range r.Results {
				if !w.fresh(x) {
					ok = false
				}
			}
		}
		return true
	})
	return ok
}

// valueSource lowers the source operand of a call of the prelude's Append,
// Delete or Put that must not write it (V3): Append takes the source
// clipped to its length, so Go's append makes a new array, and Delete and
// Put take a copy of the Map. vals are the call's lowered operands, the source first.
func (fc *funcCtx) valueSource(x *syntax.CallExpr, c *check.Call, vals []string) []string {
	if !valueRule() || len(vals) == 0 || fc.l.valueFacts().inPlace[x] || valueMutant == "in-place" {
		return vals
	}
	switch {
	case preludeAppend(c.Func):
		vals[0] = fc.l.goPkg("slices") + ".Clip(" + vals[0] + ")"
	case preludeMapWrite(c.Func):
		vals[0] = fc.l.goPkg("maps") + ".Clone(" + vals[0] + ")"
	}
	return vals
}

// cloneValue is the Go text of a copy of val, a value of type t: Go's
// maps.Clone and slices.Clone when no element holds a Map or slice, else
// t's generated copy function, which copies each element.
func (tc *typeCtx) cloneValue(t check.Type, val string, site syntax.Node) string {
	if !tc.l.holdsValue(t) {
		return val
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		if !tc.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			return tc.l.goPkg("maps") + ".Clone(" + val + ")"
		}
	case *check.Slice:
		if !tc.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			return tc.l.goPkg("slices") + ".Clone(" + val + ")"
		}
	}
	name, free := tc.l.cloneFunc(t, site)
	if len(free) > 0 {
		args := make([]string, len(free))
		for i, tp := range free {
			args[i] = tc.goType(tp)
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name + "(" + val + ")"
}

// goPkg is the name in main.go of the Go standard package path, which the
// naming pass reserves (nameAll) and main.go imports.
func (l *lowerer) goPkg(path string) string {
	if l.hostImports == nil {
		l.hostImports = map[string]bool{}
	}
	l.hostImports[path] = true
	return l.pkgName(pkgKey{"import", path}, path)
}

// cloneKey is the package-scope key of t's copy function.
func (l *lowerer) cloneKey(t check.Type) pkgKey { return pkgKey{"clone", l.typeKey(t)} }

// cloneBase is the preferred Go name of t's copy function.
func cloneBase(t check.Type) string {
	switch u := t.(type) {
	case *check.Named:
		return "ergoClone" + u.Obj.Name
	case *check.Slice:
		return "ergoCloneSlice"
	case *check.Array:
		return "ergoCloneArray"
	case *check.Map:
		return "ergoCloneMap"
	case *check.Tuple:
		return "ergoCloneTuple"
	}
	return "ergoClone"
}

// needsCloneFunc reports a type whose copy is a generated function: one
// that holds a Map or slice and is not a Map or slice of elements that
// hold none.
func (l *lowerer) needsCloneFunc(t check.Type) bool {
	if !l.holdsValue(t) {
		return false
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		return l.holdsValue(u.Elem)
	case *check.Slice:
		return l.holdsValue(u.Elem)
	}
	return true
}

// cloneFunc is the copy function of t, and the type parameters of the
// enclosing declaration that t mentions, emitted once at first use, as
// equality functions are (equal.go).
func (l *lowerer) cloneFunc(t check.Type, site syntax.Node) (string, []*check.TypeParam) {
	key := l.cloneKey(t)
	name := l.pkgName(key, cloneBase(t))
	free := freeTypeParams(t)
	if l.clones[key] {
		return name, free
	}
	if l.clones == nil {
		l.clones = map[pkgKey]bool{}
	}
	l.clones[key] = true
	scope := newNamer(l.names)
	list, names := l.typeParams(free, scope, site)
	tc := &typeCtx{l: l, params: names, site: site}
	goType := tc.goType(t)
	v, c := scope.alloc("v"), scope.alloc("c")
	var body strings.Builder
	switch u := underEqual(t).(type) {
	case *check.Slice:
		i, e := scope.alloc("i"), scope.alloc("e")
		body.WriteString("\tif " + v + " == nil {\n\t\treturn nil\n\t}\n")
		body.WriteString("\t" + c + " := make(" + goType + ", len(" + v + "))\n")
		body.WriteString("\tfor " + i + ", " + e + " := range " + v + " {\n")
		body.WriteString("\t\t" + c + "[" + i + "] = " + tc.cloneValue(u.Elem, e, site) + "\n\t}\n\treturn " + c + "\n")
	case *check.Map:
		k, e := scope.alloc("k"), scope.alloc("e")
		body.WriteString("\t" + c + " := make(" + goType + ", len(" + v + "))\n")
		body.WriteString("\tfor " + k + ", " + e + " := range " + v + " {\n")
		body.WriteString("\t\t" + c + "[" + k + "] = " + tc.cloneValue(u.Elem, e, site) + "\n\t}\n\treturn " + c + "\n")
	case *check.Array:
		// Go copied the array; each element that holds a slice or a Map
		// is copied in turn.
		i := scope.alloc("i")
		body.WriteString("\t" + c + " := " + v + "\n")
		body.WriteString("\tfor " + i + " := range " + c + " {\n")
		body.WriteString("\t\t" + c + "[" + i + "] = " + tc.cloneValue(u.Elem, c+"["+i+"]", site) + "\n\t}\n\treturn " + c + "\n")
	case *check.Tuple:
		body.WriteString("\t" + c + " := " + v + "\n")
		for i, et := range u.Types {
			if l.holdsValue(et) {
				f := tupleField(i)
				body.WriteString("\t" + c + "." + f + " = " + tc.cloneValue(et, v+"."+f, site) + "\n")
			}
		}
		body.WriteString("\treturn " + c + "\n")
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			ice(site, "the record type %s has no name", t)
		}
		td := l.typeDecl(n.Obj, site)
		body.WriteString("\t" + c + " := " + v + "\n")
		for _, f := range u.Fields {
			if !l.holdsValue(f.Type) {
				continue
			}
			goName, ok := td.fieldName[f.Name]
			if !ok {
				ice(site, "%s has no field %s", td.name, f.Name)
			}
			body.WriteString("\t" + c + "." + goName + " = " + tc.cloneValue(f.Type, v+"."+goName, site) + "\n")
		}
		body.WriteString("\treturn " + c + "\n")
	default:
		ice(site, "the type %s needs a copy function but holds no Map or slice", t)
	}
	l.gen.WriteString("func " + name + list + "(" + v + " " + goType + ") " + goType + " {\n" + body.String() + "}\n\n")
	return name, free
}

// reserveClones gives, in the naming pass, the key and preferred name of
// every copy function the lowering can emit: one for each type of an
// expression of the build that may be copied, and each such type it reaches
// through its elements and fields. Reserving one the lowering does not
// emit only renames a local.
func (l *lowerer) reserveClones(pkgs []*check.Package, reserve func(key pkgKey, t check.Type)) {
	if !valueRule() {
		return
	}
	walked := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if t == nil || walked[t] || !l.holdsValue(t) {
			return
		}
		walked[t] = true
		if l.needsCloneFunc(t) {
			reserve(l.cloneKey(t), t)
		}
		switch u := underEqual(t).(type) {
		case *check.Slice:
			walk(u.Elem)
		case *check.Array:
			walk(u.Elem)
		case *check.Map:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Struct:
			for _, f := range u.Fields {
				walk(f.Type)
			}
		}
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for x, t := range p.Info.Types {
			if l.readsFact(p, x) {
				walk(t)
			}
		}
	}
}
