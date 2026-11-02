package check

import "ergo.local/prototype/internal/sketch/syntax"

// Non-escaping function values (check-v1 stage E2, C16 and C17; 196e;
// types.md, Function values 2; spec Q1).
//
// A parameter whose type is written as a function type without `escaping`
// is non-escaping: its type is a Signature with NonEscaping set, and so is
// the type of every local a let binds from it. A function type anywhere
// else (a field, a result, an element, a type argument, a defined type's
// base, a local's written type) is escaping, so `escaping` there only
// repeats it. A non-escaping value lives no longer than the call that
// received it. One rule over its uses keeps it so:
//
//	every use of a non-escaping local is one of
//	  1. the callee of a call;
//	  2. an argument whose parameter is non-escaping;
//	  3. the value of a local let that binds one name, whose type is then
//	     non-escaping too;
//	  4. the target of an assignment, or its value when the target is a
//	     non-escaping local declared in the value's frame or in one nested
//	     in it;
//	and every frame between the frame that declares the local and the use
//	is non-escaping.
//
// Every other use is S108 at the use: a field value, a return or block
// result, an element, an argument of an escaping or generic parameter (a
// type argument is never non-escaping), a conversion, a tuple, a match
// subject. The rule is an allow-list over the parent of the use, so a
// position the checker gains later is refused until it is listed here
// (escaping_e2_test.go enumerates the positions from the syntax package).
//
// A frame is non-escaping when it cannot outlive the call it is passed to:
// a function literal that is the callee of a call or an argument of a
// non-escaping parameter, and a trailing block whose parameter is
// non-escaping (C17: the trailing block is the escape boundary; a block
// passed to an `escaping` parameter, such as task.Go's, is kept by its
// callee). A with's block is no frame: it runs in place. Every other
// function literal is an escaping frame.
//
// Function types compare with the marker: func(g func()) and
// func(g escaping func()) are distinct types (intern.go, mayBeIdentical),
// so a function value cannot launder a non-escaping argument through a
// parameter that keeps it.
//
// Prior art: Swift closures are non-escaping by default and `@escaping`
// marks the exception; a non-escaping closure may only be called or passed
// on as non-escaping, and a closure that captures one is non-escaping
// itself. Kotlin's inline lambdas (and `crossinline`) do the same for
// inline functions. Rust expresses it with lifetimes ('static bounds for a
// stored closure). Gleam, Elixir, ReScript and MoonBit have no marker:
// every function value may escape. Go has none either; its escape analysis
// decides heap allocation, not legality.

// escapeText and captureText are the two S108 messages.
const (
	escapeText  = "%s is a non-escaping function value: it may be called or passed to a non-escaping parameter, never stored, returned or passed where it may be kept; write `escaping` on its parameter's type to keep it (types.md, Function values 2)"
	captureText = "%s is a non-escaping function value, and this function literal or block may outlive the call that received it; capture it only in a block or literal passed to a non-escaping parameter, or write `escaping` on its parameter's type (types.md, Function values 2)"
)

// nonEscaping reports whether t is a non-escaping function type.
func nonEscaping(t Type) bool {
	s, ok := t.(*Signature)
	return ok && s.NonEscaping
}

// checkEscapes applies the rule to every file once Info's calls are final.
func (c *checker) checkEscapes() {
	for _, f := range c.files {
		c.cur = f
		w := &escWalk{c: c, info: c.pkg.Info, varFrame: map[*Var]int{}}
		for _, d := range f.syntax.Decls {
			w.visit(d)
		}
	}
}

type escWalk struct {
	c    *checker
	info *Info
	// stack is the path from the declaration to the node being visited.
	stack []syntax.Node
	// frames are the function bodies around the node, each with whether
	// it may outlive the call it is passed to.
	frames []bool
	// varFrame is the index in frames of the frame that declares a local
	// (-1 at the package level).
	varFrame map[*Var]int
}

func (w *escWalk) visit(n syntax.Node) {
	if x, ok := n.(syntax.Expr); ok && w.c.typeNodes[x] {
		return
	}
	if id, ok := n.(*syntax.Ident); ok {
		if v, ok := w.info.Defs[id].(*Var); ok {
			w.varFrame[v] = len(w.frames) - 1
		} else if v, ok := w.info.Uses[id].(*Var); ok && !v.pkg && nonEscaping(v.Type) {
			w.use(id, v)
		}
		return
	}
	frame, escaping := w.frame(n)
	if frame {
		w.frames = append(w.frames, escaping)
	}
	w.stack = append(w.stack, n)
	syntax.Inspect(n, func(m syntax.Node) bool {
		if m == n {
			return true
		}
		if m != nil {
			w.visit(m)
		}
		return false
	})
	w.stack = w.stack[:len(w.stack)-1]
	if frame {
		w.frames = w.frames[:len(w.frames)-1]
	}
}

// frame reports whether n opens a frame and whether that frame may
// outlive the call it is passed to.
func (w *escWalk) frame(n syntax.Node) (frame, escaping bool) {
	switch n := n.(type) {
	case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
		return true, false
	case *syntax.FuncLit:
		parent, child := w.parentOf(n)
		call, ok := parent.(*syntax.CallExpr)
		if !ok {
			return true, true
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
		call, ok := w.parent().(*syntax.CallExpr)
		if !ok || call.Trailing != n {
			return false, false
		}
		if k := w.info.Calls[call]; k != nil && k.Kind == CallWith {
			return false, false
		}
		return true, !w.paramNonEscaping(call, len(call.Args))
	}
	return false, false
}

func (w *escWalk) parent() syntax.Node {
	if len(w.stack) == 0 {
		return nil
	}
	return w.stack[len(w.stack)-1]
}

// parentOf is the nearest ancestor of n, the node being visited (not yet
// on the stack), that is not a parenthesis, with its child on the path
// (n or the outermost parenthesis around it).
func (w *escWalk) parentOf(n syntax.Node) (parent, child syntax.Node) {
	child = n
	for j := len(w.stack) - 1; j >= 0; j-- {
		if _, ok := w.stack[j].(*syntax.ParenExpr); ok {
			child = w.stack[j]
			continue
		}
		return w.stack[j], child
	}
	return nil, child
}

// paramNonEscaping reports whether the i-th argument of call binds a
// non-escaping parameter: the trailing block is the argument after the
// written ones. A dot call's receiver is never a non-escaping value: it
// has the named type its function is declared on (196m). A call that was
// never checked (its callee is in error, such as a recursive group that
// needs its row written) has no Calls entry; its own diagnostic stands, so
// it adds none here, as in the Task use rule (taskescape.go).
func (w *escWalk) paramNonEscaping(call *syntax.CallExpr, i int) bool {
	k := w.info.Calls[call]
	if k == nil {
		return true
	}
	var sig *Signature
	switch k.Kind {
	case CallFunc:
		if k.Func != nil {
			sig = k.Func.Sig
		}
		if k.Recv != nil {
			i++ // a 196m dot call: the receiver is the first parameter
		}
	case CallOp, CallProviderOp:
		if k.Op != nil {
			sig = k.Op.Sig
		}
	case CallFuncValue:
		sig, _ = under(w.info.Types[call.Fun]).(*Signature)
	}
	return sig != nil && i < len(sig.Params) && nonEscaping(sig.Params[i].Type)
}

// use checks one use of the non-escaping local v.
func (w *escWalk) use(id *syntax.Ident, v *Var) {
	if !w.allowed(id, v) {
		w.c.errorf(id, CodeType, escapeText, id.Name)
		return
	}
	from, ok := w.varFrame[v]
	if !ok {
		return
	}
	for k := from + 1; k < len(w.frames); k++ {
		if w.frames[k] {
			w.c.errorf(id, CodeType, captureText, id.Name)
			return
		}
	}
}

// allowed reports whether the use id of v stands in one of the rule's four
// positions.
func (w *escWalk) allowed(id *syntax.Ident, v *Var) bool {
	parent, child := w.parentOf(id)
	switch p := parent.(type) {
	case *syntax.CallExpr:
		if p.Fun == child {
			k := w.info.Calls[p]
			return k == nil || k.Kind == CallFuncValue
		}
		for i, a := range p.Args {
			if a == child {
				return w.paramNonEscaping(p, i)
			}
		}
	case *syntax.VarSpec:
		// A local let: a package let's value is no use of a local.
		if len(p.Names) == 1 && p.Pattern == nil && len(p.Values) == 1 && p.Values[0] == child {
			if p.Names[0].Name == "_" {
				return true // a discard keeps nothing
			}
			t, ok := w.info.Defs[p.Names[0]].(*Var)
			return ok && nonEscaping(t.Type)
		}
	case *syntax.AssignStmt:
		for _, l := range p.Lhs {
			if l == child {
				return true // the target; the value assigned is its own use
			}
		}
		if p.Tok != syntax.ASSIGN || len(p.Lhs) != len(p.Rhs) {
			return false
		}
		for i, r := range p.Rhs {
			if r != child {
				continue
			}
			lhs, ok := unparen(p.Lhs[i]).(*syntax.Ident)
			if !ok {
				return false
			}
			if lhs.Name == "_" {
				return true // a discard keeps nothing
			}
			t, ok := w.info.Uses[lhs].(*Var)
			if !ok || t.pkg || !nonEscaping(t.Type) {
				return false
			}
			// The target must not outlive the value: it is declared in
			// the value's frame or in one nested in it.
			tf, ok1 := w.varFrame[t]
			vf, ok2 := w.varFrame[v]
			return ok1 && ok2 && tf >= vf
		}
	}
	return false
}
