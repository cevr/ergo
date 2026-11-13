package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// A borrow as a type argument (types.md, Borrows 7; v1-audit-3 G4).
//
// Inside a generic body a value of a type parameter T is a plain value, so
// the body may keep it: capture it in a frame that may outlive the call,
// store it in a field, an element or a payload, or pass it where it may be
// kept. A borrow instantiates T only as a lend, when the body keeps no
// value of T. The rule is the allow-list of the borrow rules again, over
// every expression of the body whose type mentions T:
//
//	1. the callee of a call, or the receiver of a field read, a method
//	   value or an operation call (x.Check());
//	2. an argument of a parameter that is a type parameter U of a generic
//	   function that keeps no U (checked the same way; a cycle keeps), or
//	   of a function value's parameter of T, which is a borrow parameter
//	   in the function the caller wrote;
//	3. the value of a let, of an assignment to a local or through a borrow
//	   parameter (*out = x: the caller's local), a match subject, an operand of a
//	   comparison or an interpolation;
//	and no use of a local of T inside a function literal or block that may
//	outlive the call it is passed to (task.Go's block among them).
//
// A return or a block's value keeps T (INS-3): the written instance of a
// body that returns its T returns a *T, which Borrows 1 refuses, so the
// call refuses it too. A block's value is kept even when it flows only
// into a local of the body (the simpler rule). A generic function
// with no body Ergo reads (an extern) keeps every type parameter. A
// literal of a generic type built over a borrow (Box{V: t}) is refused at
// the literal, since a record never holds a borrow.
//
// Prior art: Rust carries the lifetime in the instantiated type, so
// `T = &'a mut Tally` bounds whatever the body builds from T by 'a; Swift
// cannot pass an inout as a generic T; resources take the copy-site rule
// of R44 (takes.go), which this mirrors for the uses a borrow forbids.

// typeMentions reports whether a value of type t may hold a value of the
// type parameter tp. A function value holds none: what it captures was
// judged where it was captured.
func typeMentions(t Type, tp *TypeParam) bool {
	if _, ok := under(t).(*Signature); ok {
		return false
	}
	found := false
	eachTypeParam(t, func(p *TypeParam) {
		if p == tp {
			found = true
		}
	})
	return found
}

// keepsTParam is why f's body may keep a value of its type parameter tp,
// or "" when it keeps none.
func (w *escWalk) keepsTParam(f *Func, tp *TypeParam) string {
	if w.keeps == nil {
		w.keeps = map[*Func]map[*TypeParam]string{}
	}
	if m, ok := w.keeps[f]; ok {
		if why, done := m[tp]; done {
			return why
		}
	}
	if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return "it has no body Ergo reads"
	}
	if w.keeps[f] == nil {
		w.keeps[f] = map[*TypeParam]string{}
	}
	// A cycle keeps until the body is read through.
	w.keeps[f][tp] = "it calls itself with it"
	k := &keepWalk{w: w, info: f.Pkg.Info, tp: tp}
	k.walk(f.Decl.Body)
	w.keeps[f][tp] = k.why
	return k.why
}

// keepWalk reads one generic body for one type parameter.
type keepWalk struct {
	w     *escWalk
	info  *Info
	tp    *TypeParam
	stack []syntax.Node
	// frames are the function literals and blocks around the node, with
	// whether each may outlive the call it is passed to.
	frames []bool
	// declared is the number of frames around each local's declaration.
	declared map[*Var]int
	why      string
}

func (k *keepWalk) walk(n syntax.Node) {
	if k.declared == nil {
		k.declared = map[*Var]int{}
	}
	if k.why != "" || n == nil {
		return
	}
	if id, ok := n.(*syntax.Ident); ok {
		if v, ok := k.info.Defs[id].(*Var); ok {
			k.declared[v] = len(k.frames)
		}
	}
	if x, ok := n.(syntax.Expr); ok {
		k.use(x)
	}
	frame, escaping := k.frame(n)
	if frame {
		k.frames = append(k.frames, escaping)
	}
	k.stack = append(k.stack, n)
	syntax.Inspect(n, func(m syntax.Node) bool {
		if m == n {
			return true
		}
		if m != nil {
			k.walk(m)
		}
		return false
	})
	k.stack = k.stack[:len(k.stack)-1]
	if frame {
		k.frames = k.frames[:len(k.frames)-1]
	}
}

// frame reports whether n opens a frame and whether it may outlive the
// call it is passed to (escaping.go's frame, over the callee's Info).
func (k *keepWalk) frame(n syntax.Node) (frame, escaping bool) {
	switch n := n.(type) {
	case *syntax.FuncLit:
		parent, child := parentIn(k.stack, n)
		call, ok := parent.(*syntax.CallExpr)
		if !ok {
			return true, !k.info.LocalFrames[n]
		}
		if call.Fun == child {
			return true, false
		}
		for i, a := range call.Args {
			if a == child {
				return true, !k.paramNonEscaping(call, i)
			}
		}
		return true, true
	case *syntax.BlockStmt:
		if len(k.stack) == 0 {
			return false, false
		}
		call, ok := k.stack[len(k.stack)-1].(*syntax.CallExpr)
		if !ok || call.Trailing != n {
			return false, false
		}
		if c := k.info.Calls[call]; c != nil && c.Kind == CallWith {
			return false, false
		}
		return true, !k.paramNonEscaping(call, len(call.Args))
	}
	return false, false
}

func (k *keepWalk) paramNonEscaping(call *syntax.CallExpr, i int) bool {
	c := k.info.Calls[call]
	if c == nil {
		return false
	}
	var sig *Signature
	switch c.Kind {
	case CallFunc:
		if c.Func != nil {
			sig = c.Func.Sig
		}
		if c.Recv != nil {
			i++
		}
	case CallOp, CallProviderOp, CallLawOp:
		if c.Op != nil {
			sig = c.Op.Sig
		}
	case CallFuncValue:
		sig, _ = under(k.info.Types[call.Fun]).(*Signature)
	}
	return sig != nil && i < len(sig.Params) && nonEscaping(sig.Params[i].Type)
}

// parentIn is the nearest ancestor of n, the node being visited (not yet
// on stack), that is no parenthesis, with its child on the path (n or the
// outermost parenthesis around it).
func parentIn(stack []syntax.Node, n syntax.Node) (parent, child syntax.Node) {
	child = n
	for j := len(stack) - 1; j >= 0; j-- {
		if _, ok := stack[j].(*syntax.ParenExpr); ok {
			child = stack[j]
			continue
		}
		return stack[j], child
	}
	return nil, child
}

// use judges x, the node being visited.
func (k *keepWalk) use(x syntax.Expr) {
	if _, ok := x.(*syntax.ParenExpr); ok {
		return
	}
	if id, ok := x.(*syntax.Ident); ok {
		v, ok := k.info.Uses[id].(*Var)
		if !ok || v.Type == nil || !typeMentions(v.Type, k.tp) {
			return
		}
		// A parameter is declared outside every frame of the body.
		for _, esc := range k.frames[k.declared[v]:] {
			if esc {
				k.why = "it captures " + id.Name + " in a function literal or block that may outlive the call"
				return
			}
		}
	} else if t := k.info.Types[x]; t == nil || !typeMentions(t, k.tp) {
		return
	}
	if why := k.position(x); why != "" {
		k.why = why
	}
}

// position is why the position of x keeps its value, or "".
func (k *keepWalk) position(x syntax.Expr) string {
	parent, child := parentIn(k.stack, x)
	switch p := parent.(type) {
	case *syntax.CallExpr:
		if p.Fun == child {
			return ""
		}
		for i, a := range p.Args {
			if a == child {
				return k.argument(p, i)
			}
		}
		return "it passes a value of it where it may be kept"
	case *syntax.SelectorExpr:
		if p.X != child {
			return ""
		}
		// A dot call of a function passes the receiver first.
		if len(k.stack) >= 2 {
			if call, ok := k.stack[len(k.stack)-2].(*syntax.CallExpr); ok && call.Fun == syntax.Expr(p) {
				if c := k.info.Calls[call]; c != nil && c.Kind == CallFunc && c.Recv != nil {
					return k.param(call, c.Func, 0)
				}
			}
		}
		return ""
	case *syntax.VarSpec, *syntax.InterpolatedString, *syntax.BinaryExpr:
		return ""
	case *syntax.ReturnStmt:
		return "it returns a value of it"
	case *syntax.ExprStmt:
		// A block's value flows out of the block: to the function's
		// result, or to what the block's if or match gives it to. (A
		// statement that discards a value of T is a call of a body that
		// returns one, which keeps it already.)
		return "it returns a value of it"
	case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		return ""
	case *syntax.StarExpr, *syntax.UnaryExpr:
		// A read or write through a borrow, or a borrow of a local:
		// the value it makes is judged where it stands.
		return ""
	case *syntax.AssignStmt:
		for _, l := range p.Lhs {
			if l == child {
				return ""
			}
		}
		// A local, or the place a borrow parameter points at: a caller's
		// local, since no field or element holds a borrow.
		for _, l := range p.Lhs {
			switch l := unparen(l).(type) {
			case *syntax.Ident:
			case *syntax.StarExpr:
				if _, ok := unparen(l.X).(*syntax.Ident); !ok {
					return "it stores a value of it"
				}
			default:
				return "it stores a value of it"
			}
		}
		return ""
	}
	return "it stores a value of it"
}

// argument judges a value of the type parameter as the i-th argument of
// call.
func (k *keepWalk) argument(call *syntax.CallExpr, i int) string {
	c := k.info.Calls[call]
	if c != nil && c.Kind == CallFuncValue {
		// A function value's parameter of T is the borrow itself in the
		// function the caller wrote, where the borrow rules hold.
		if sig, ok := under(k.info.Types[call.Fun]).(*Signature); ok && i < len(sig.Params) {
			if _, ok := sig.Params[i].Type.(*TypeParam); ok || isPointer(sig.Params[i].Type) {
				return ""
			}
		}
	}
	if c == nil || c.Kind != CallFunc || c.Func == nil {
		return "it passes a value of it where it may be kept"
	}
	if c.Recv != nil {
		i++
	}
	return k.param(call, c.Func, i)
}

// param judges a value of the type parameter bound to g's i-th parameter.
func (k *keepWalk) param(call *syntax.CallExpr, g *Func, i int) string {
	if g == nil || g.Sig == nil || i >= len(g.Sig.Params) {
		return "it passes a value of it where it may be kept"
	}
	pt := g.Sig.Params[i].Type
	if u, ok := pt.(*TypeParam); ok && len(g.Sig.TParams) > 0 {
		if why := k.w.keepsTParam(g, u); why != "" {
			return "it passes a value of it to " + g.Name + ", and " + why
		}
		return ""
	}
	return "it passes a value of it to " + g.Name + ", which may keep it"
}
