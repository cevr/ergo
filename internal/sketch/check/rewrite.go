package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// Rewrite is the checked form of a rewrite law (types.md, Laws 14):
// `law L(params) [when R() == C] rewrite { lhs } to { rhs }`. Lhs and Rhs
// are straight-line calls of the owner's own operations. Const marks, by
// parameter index, the parameters that Rhs uses inside a computed
// expression: a site must pass a constant for each, which the lowering
// computes the expression from. Every other parameter passes through.
type Rewrite struct {
	// Guard is the read operation R of `when R() == C`, nil without a
	// guard; GuardValue is C.
	Guard      *Method
	GuardValue syntax.Expr
	Lhs, Rhs   []RewriteCall
	Const      []bool
	// Steps are the owner's operations a run draws into a case's prefix
	// and continuation: each whose parameters have generators, in order.
	Steps []*Method
}

// RewriteCall is one call of a rewrite law's side: its operation and
// arguments in order. Try is set when the statement is `try Op(…)`.
type RewriteCall struct {
	Call *syntax.CallExpr
	Op   *Method
	Try  bool
	Args []RewriteArg
}

// RewriteArg is an argument of a RewriteCall: a bare parameter, Param its
// index, or a computed expression (Param -1) X.
type RewriteArg struct {
	Param int
	X     syntax.Expr
}

// The bounds of a rewrite law's sides (types.md, Laws 14): the lowering
// matches a left side of one to maxRewriteLhs calls in a window, and a
// right side writes at most maxRewriteRhs.
const (
	maxRewriteLhs = 3
	maxRewriteRhs = 8
)

// rewriteLaw checks the guard and both sides of a rewrite law in the
// law's frame, then its shape (rewriteShape). Each side is a block of its
// own, so a side's locals do not reach the other; each side's escaping
// failures are its own row, and the two rows must be equal.
func (c *checker) rewriteLaw(law *Law, tg *target) {
	r := law.Decl.Rewrite
	if law.Owner == nil {
		// The parser reported a top-level rewrite law; a machine's law
		// is reported here.
		if law.Machine != nil {
			c.errorf(law.Decl.Name, CodeType, "a rewrite law is a member of an operation type, not of a machine")
		}
		return
	}
	if r.Guard != nil {
		c.condition(r.Guard)
	}
	tg.errors = newRow()
	c.block(r.Lhs)
	lhsRow := c.types.resolve(tg.errors).known(c.types)
	tg.errors = newRow()
	c.block(r.Rhs)
	rhsRow := c.types.resolve(tg.errors).known(c.types)
	tg.errors = lhsRow.union(c.types, rhsRow)
	if !lhsRow.equal(c.types, rhsRow) {
		c.errorf(r.Rhs, CodeType, "the sides of rewrite law %s fail differently: %s on the left, %s on the right; a rewrite keeps the failures", law.Name, rowText(lhsRow), rowText(rhsRow))
	}
	if rw := c.rewriteShape(law); rw != nil {
		law.Rewrite = rw
	}
}

// rewriteShape checks the shape rules of a rewrite law and returns its
// checked form, or nil after a diagnostic:
//   - the guard is `R() == C`, R a read operation of the owner with no
//     parameters, C a closed expression (no parameter, no call);
//   - each side is a list of calls of the owner's operations, `Op(…)` or
//     `try Op(…)`: one to maxRewriteLhs on the left, at most maxRewriteRhs
//     on the right;
//   - each left argument is a bare parameter, and each parameter is one
//     left argument exactly once (linear: the left side binds it);
//   - a right argument is a bare parameter, which passes through, or a
//     computed expression, whose parameters are then constant at the site
//     and which calls no operation; a pass-through parameter is a right
//     argument at most once (linear).
func (c *checker) rewriteShape(law *Law) *Rewrite {
	d := law.Decl
	r := d.Rewrite
	rw := &Rewrite{Const: make([]bool, len(law.Params))}
	param := map[*Var]int{}
	for i, v := range law.Params {
		param[v] = i
	}
	paramOf := func(x syntax.Expr) (int, bool) {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return -1, false
		}
		v, ok := c.pkg.Info.Uses[id].(*Var)
		if !ok {
			return -1, false
		}
		i, ok := param[v]
		return i, ok
	}
	ok := true
	if r.Guard != nil {
		b, isEq := unparen(r.Guard).(*syntax.BinaryExpr)
		var read *Method
		if isEq && b.Op == syntax.EQL {
			if call, isCall := unparen(b.X).(*syntax.CallExpr); isCall && len(call.Args) == 0 {
				if info := c.pkg.Info.Calls[call]; info != nil && info.Kind == CallLawOp && len(info.Op.Sig.Params) == 0 && len(info.Op.Sig.Results) == 1 && info.Op.Sig.Errors.Empty() {
					read = info.Op
				}
			}
		}
		switch {
		case read == nil:
			c.errorf(r.Guard, CodeType, "a rewrite law's guard is R() == C, R an operation of %s with no parameters that reads one value", law.Owner.Name)
			ok = false
		case !c.closedExpr(b.Y, param):
			c.errorf(b.Y, CodeType, "a rewrite law's guard compares R() with a constant: no parameter, no call")
			ok = false
		default:
			rw.Guard, rw.GuardValue = read, b.Y
		}
	}
	side := func(b *syntax.BlockStmt, what string, max int) ([]RewriteCall, bool) {
		var out []RewriteCall
		good := true
		for _, s := range b.List {
			call, try := sideCall(s)
			var info *Call
			if call != nil {
				info = c.pkg.Info.Calls[call]
			}
			if info == nil || info.Kind != CallLawOp {
				c.errorf(s, CodeType, "a rewrite law's %s side is calls of %s's own operations, one per statement", what, law.Owner.Name)
				good = false
				continue
			}
			rc := RewriteCall{Call: call, Op: info.Op, Try: try}
			for _, a := range call.Args {
				i, _ := paramOf(a)
				rc.Args = append(rc.Args, RewriteArg{Param: i, X: a})
			}
			out = append(out, rc)
		}
		if good && len(out) > max {
			c.errorf(b.List[max], CodeType, "a rewrite law's %s side has at most %d calls", what, max)
			good = false
		}
		return out, good
	}
	lhs, okL := side(r.Lhs, "left", maxRewriteLhs)
	rhs, okR := side(r.Rhs, "right", maxRewriteRhs)
	if !okL || !okR || !ok {
		return nil
	}
	if len(lhs) == 0 {
		c.errorf(r.Lhs, CodeType, "a rewrite law's left side has at least one call")
		return nil
	}
	bound := make([]bool, len(law.Params))
	for _, rc := range lhs {
		for _, a := range rc.Args {
			if a.Param < 0 {
				c.errorf(a.X, CodeType, "a rewrite law's left side passes parameters only; name this value as a parameter of %s", law.Name)
				return nil
			}
			if bound[a.Param] {
				c.errorf(a.X, CodeType, "parameter %s is bound twice by the left side; a rewrite's left side names each parameter once", law.Params[a.Param].Name)
				return nil
			}
			bound[a.Param] = true
		}
	}
	for i, v := range law.Params {
		if !bound[i] {
			c.errorf(v.id, CodeType, "parameter %s is not bound by the rewrite's left side; pass it to a left operation", v.Name)
			return nil
		}
	}
	passed := make([]bool, len(law.Params))
	for _, rc := range rhs {
		for _, a := range rc.Args {
			if a.Param >= 0 {
				continue
			}
			calls := false
			syntax.Inspect(a.X, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.CallExpr:
					if info := c.pkg.Info.Calls[n]; info != nil && (info.Kind == CallLawOp || info.Kind == CallOp || info.Kind == CallProviderOp) {
						calls = true
					}
				case *syntax.TryExpr, *syntax.CatchExpr:
					calls = true
				case *syntax.Ident:
					if v, isVar := c.pkg.Info.Uses[n].(*Var); isVar {
						if i, isParam := param[v]; isParam {
							rw.Const[i] = true
						}
					}
				}
				return true
			})
			if calls {
				c.errorf(a.X, CodeType, "a computed argument of a rewrite's right side calls no operation and has no try; the lowering computes it at compile time")
				return nil
			}
		}
	}
	for _, rc := range rhs {
		for _, a := range rc.Args {
			if a.Param < 0 || rw.Const[a.Param] {
				continue
			}
			if passed[a.Param] {
				c.errorf(a.X, CodeType, "parameter %s passes through the right side twice; a rewrite passes a value through once (linear)", law.Params[a.Param].Name)
				return nil
			}
			passed[a.Param] = true
		}
	}
	rw.Lhs, rw.Rhs = lhs, rhs
	return rw
}

// sideCall is the call of a rewrite side's statement, `Op(…)` or `try
// Op(…)`, or nil.
func sideCall(s syntax.Stmt) (*syntax.CallExpr, bool) {
	es, ok := s.(*syntax.ExprStmt)
	if !ok {
		return nil, false
	}
	x := unparen(es.X)
	try := false
	if t, isTry := x.(*syntax.TryExpr); isTry {
		x, try = unparen(t.X), true
	}
	call, _ := x.(*syntax.CallExpr)
	return call, try
}

// closedExpr reports whether x names no parameter and calls nothing: a
// constant of its type, a guard's C.
func (c *checker) closedExpr(x syntax.Expr, param map[*Var]int) bool {
	closed := true
	syntax.Inspect(x, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			if info := c.pkg.Info.Calls[n]; info == nil || info.Kind != CallConversion {
				closed = false
			}
		case *syntax.Ident:
			if _, isVar := c.pkg.Info.Uses[n].(*Var); isVar {
				closed = false
			}
		}
		return closed
	})
	return closed
}

// checkUnfoldCycles is S108 at a rewrite law of an operation type whose
// unfold laws (no guard, one left call) unfold again: an operation reaches
// itself through the operations their right sides call (types.md, Laws 14).
func (c *checker) checkUnfoldCycles() {
	byOwner := map[*TypeName][]*Law{}
	var owners []*TypeName
	for _, law := range c.pkg.Laws {
		if law.Rewrite == nil || law.Rewrite.Guard != nil || len(law.Rewrite.Lhs) != 1 {
			continue
		}
		if byOwner[law.Owner] == nil {
			owners = append(owners, law.Owner)
		}
		byOwner[law.Owner] = append(byOwner[law.Owner], law)
	}
	for _, own := range owners {
		laws := byOwner[own]
		next := map[*Method][]*Method{}
		for _, law := range laws {
			from := law.Rewrite.Lhs[0].Op
			for _, rc := range law.Rewrite.Rhs {
				next[from] = append(next[from], rc.Op)
			}
		}
		for _, law := range laws {
			start := law.Rewrite.Lhs[0].Op
			seen := map[*Method]bool{}
			stack := append([]*Method(nil), next[start]...)
			cycle := false
			for len(stack) > 0 && !cycle {
				m := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if m == start {
					cycle = true
					break
				}
				if seen[m] {
					continue
				}
				seen[m] = true
				stack = append(stack, next[m]...)
			}
			if cycle {
				saved := c.cur
				c.cur = law.file
				defer func() { c.cur = saved }()
				c.errorf(law.Decl.Name, CodeType, "rewrite law %s unfolds %s into itself: an unfold law's right side unfolds again (a cycle)", law.Name, start.Name)
				law.Rewrite = nil
				return
			}
		}
	}
}
