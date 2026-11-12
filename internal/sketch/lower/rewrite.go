package lower

import (
	"go/constant"
	"go/token"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Rewrites (types.md, Laws 14; plan LR2). The run build applies a rewrite
// law `law L(params) [when R() == C] { lhs } == { rhs }` of an operation
// type T at a site when the site's receiver is a value of a provider P of
// T, so the lowering knows P statically, and the evidence for P's run of
// the law (RewriteEvidence) admits it at the site's constants.
//
// A window is a run of consecutive statements `x.Op(args)`, each a dot call
// of one of P's operation functions on the same local x, with no result and
// no failure, each argument a constant or a local of a basic type. Any
// other statement (a call of another function, a branch, a loop, a call
// with another argument) ends it: what it does to P's state is unknown.
// Every non-constant argument and the receiver are read into temporaries
// in source order where the window starts, so a rewrite never reorders,
// drops or repeats the evaluation of a user expression.
//
// The engine runs the window's rules to a fixpoint in a fixed order:
// unfold laws (no guard, one left call), then guarded laws, then every
// other (joins and sinks), and again from the start while one applied. A
// guard `R() == C` holds at a position when a post law fact says so: a
// post law is an ordinary law `{ Op(params); R() == X }`, and after a call
// of Op the read R is X there. Any other call forgets every fact. A
// computed argument of a right side is computed with the check-time
// evaluator (eval.Fold) from the site's constants. The engine counts its
// work (rewriter.work); a window whose work passes rewriteWorkPerCall per
// call keeps the reference lowering.
//
// The matcher reads only checked law facts: the laws, their owners'
// operations and the provider's conversion table. It never reads a
// package, type or function name (identity-blind; opt4).
//
// The reference lowering is rewriteLowering "reference", set by a test or
// by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.rewriteLowering=reference:
// no window is rewritten. A unit with no evidence (Unit.Rewrite nil) is
// lowered the same way.
var rewriteLowering = ""

// RewriteEvidence is what the build knows of the law runs: Admit reports
// whether the run of law against the provider passed with the site
// instance in among its cases, and why not. A post law's instance has no
// arguments.
type RewriteEvidence interface {
	Admit(law *check.Law, provider *check.Named, in RewriteInstance) (bool, string)
}

// rewriteWorkPerCall bounds the engine's work on a window: each attempt to
// match a rule at a position is one unit of work.
const rewriteWorkPerCall = 64

// rewriteFoldBudget bounds the evaluation of a right side's computed
// argument.
var rewriteFoldBudget = eval.Budget{Steps: 100_000, Depth: 64}

// rewriter holds the rules of the run build: by provider, each rewrite
// law and post law whose run the build reads.
type rewriter struct {
	ev    RewriteEvidence
	rules map[*check.Named][]rwRule
	// work counts every match attempt of every window (the growth guard).
	work int
	// applied counts the windows rewritten; refused the windows whose
	// work passed the bound.
	applied, refused int
}

// rwRule is a law the provider's run checks: conv is the provider's
// conversion into the law's owner, post the post-law form or nil.
type rwRule struct {
	law  *check.Law
	conv check.Conversion
	post *postLaw
	// ctor is the provider's constructor, when its run calls one.
	ctor *check.Func
}

// postLaw is `{ Op(params); R() == X }`: args are Op's arguments as the
// law's parameter indexes, and X is the constant value, or the parameter
// param (value nil).
type postLaw struct {
	op    *check.Method
	args  []int
	read  *check.Method
	value constant.Value
	param int
}

// newRewriter collects the rules the root package's law runs give each
// provider: a run of one slot whose provider runs (Unrun empty).
func newRewriter(pkg *check.Package, ev RewriteEvidence) *rewriter {
	rw := &rewriter{ev: ev, rules: map[*check.Named][]rwRule{}}
	for _, run := range pkg.LawRuns {
		law := run.Law
		if law.Owner == nil || law.Unrun != "" {
			continue
		}
		var post *postLaw
		if law.Rewrite == nil {
			if post = postForm(law); post == nil {
				continue
			}
		}
		for _, b := range run.Bindings {
			if len(b) != 1 || b[0].Unrun != "" || b[0].Type == nil || b[0].Conv.Kind != check.ConvOperations || b[0].Slot != law.Owner.Type {
				continue
			}
			rw.rules[b[0].Type] = append(rw.rules[b[0].Type], rwRule{law: law, conv: b[0].Conv, post: post, ctor: b[0].Ctor})
		}
	}
	return rw
}

// postForm is the post-law form of law, nil when it has another.
func postForm(law *check.Law) *postLaw {
	d := law.Decl
	if d == nil || d.Rewrite != nil || d.Body == nil || len(d.Body.List) != 2 || law.Pkg == nil {
		return nil
	}
	info := law.Pkg.Info
	param := func(x syntax.Expr) int {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return -1
		}
		for i, v := range law.Params {
			if info.Uses[id] == check.Object(v) {
				return i
			}
		}
		return -1
	}
	call, try := sideCall(d.Body.List[0])
	if call == nil || try {
		return nil
	}
	c := info.Calls[call]
	if c == nil || c.Kind != check.CallLawOp || c.Op == nil {
		return nil
	}
	p := &postLaw{op: c.Op, param: -1}
	for _, a := range call.Args {
		i := param(a)
		if i < 0 {
			return nil
		}
		p.args = append(p.args, i)
	}
	es, ok := d.Body.List[1].(*syntax.ExprStmt)
	if !ok {
		return nil
	}
	b, ok := unparen(es.X).(*syntax.BinaryExpr)
	if !ok || b.Op != syntax.EQL {
		return nil
	}
	rc, ok := unparen(b.X).(*syntax.CallExpr)
	if !ok || len(rc.Args) != 0 {
		return nil
	}
	ri := info.Calls[rc]
	if ri == nil || ri.Kind != check.CallLawOp || ri.Op == nil || len(ri.Op.Sig.Results) != 1 {
		return nil
	}
	p.read = ri.Op
	if v, ok := info.Values[b.Y]; ok {
		p.value = v
	} else if p.param = param(b.Y); p.param < 0 {
		return nil
	}
	return p
}

// sideCall is the call of a statement `Op(…)` or `try Op(…)`, or nil.
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

// rwCall is a call of a window: the provider's function fn and its
// arguments.
type rwCall struct {
	fn   *check.Func
	args []rwArg
}

// rwArg is an argument of a window call: the constant c, or the Go text of
// the temporary that holds it.
type rwArg struct {
	c    constant.Value
	text string
}

// rewritable reports a provider function a window may call: a plain Ergo
// function of no type parameters, on its value or *P, with no result
// and no failure, whose other parameters are basic.
func rewritable(f *check.Func) bool {
	if f == nil || f.Decl == nil || f.Decl.Extern != nil || f.Sig == nil || len(f.Sig.TParams) > 0 || len(f.Sig.Results) > 0 || f.Sig.Fallible || len(f.Sig.Params) == 0 {
		return false
	}
	if f.Sig.Errors != nil && len(rowElems(f.Sig.Errors)) > 0 {
		return false
	}
	for _, p := range f.Sig.Params[1:] {
		if _, ok := underlying(p.Type).(*check.Basic); !ok {
			return false
		}
	}
	return true
}

// windowCall is s as a call of a window on a provider: the receiver's
// variable, its provider type and the called function.
func (fc *funcCtx) windowCall(s syntax.Stmt) (*check.Var, *check.Named, *check.Func, *syntax.CallExpr) {
	es, ok := s.(*syntax.ExprStmt)
	if !ok {
		return nil, nil, nil, nil
	}
	call, ok := unparen(es.X).(*syntax.CallExpr)
	if !ok || call.Trailing != nil {
		return nil, nil, nil, nil
	}
	c := fc.l.info.Calls[call]
	if c == nil || c.Recv == nil {
		return nil, nil, nil, nil
	}
	id, ok := unparen(c.Recv).(*syntax.Ident)
	if !ok {
		return nil, nil, nil, nil
	}
	v, ok := fc.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil, nil, nil, nil
	}
	if _, local := fc.locals[v]; !local {
		return nil, nil, nil, nil
	}
	fn := c.Func
	typ := v.Type
	switch c.Kind {
	case check.CallFunc:
	case check.CallOp:
		// An operation on a local a let bound by a cell conversion: the
		// cell's own value, of the provider the conversion names (cells.go).
		conv, _, ok := fc.cellWindow(v)
		if !ok || c.Op == nil {
			return nil, nil, nil, nil
		}
		fn, typ = conv.Table[c.Op.Name], conv.From
	default:
		return nil, nil, nil, nil
	}
	if !rewritable(fn) {
		return nil, nil, nil, nil
	}
	if ptr, ok := typ.(*check.Pointer); ok {
		typ = ptr.Elem
	}
	prov, ok := typ.(*check.Named)
	if !ok {
		return nil, nil, nil, nil
	}
	known := false
	for _, r := range fc.l.rewrite.rules[prov] {
		if r.conv.Table[fn.Name] == fn {
			known = true
			break
		}
	}
	if !known {
		return nil, nil, nil, nil
	}
	for _, a := range call.Args {
		if _, isConst := fc.l.info.Values[a]; isConst {
			continue
		}
		aid, ok := unparen(a).(*syntax.Ident)
		if !ok {
			return nil, nil, nil, nil
		}
		av, ok := fc.l.info.Uses[aid].(*check.Var)
		if !ok {
			return nil, nil, nil, nil
		}
		if _, local := fc.locals[av]; !local {
			return nil, nil, nil, nil
		}
	}
	return v, prov, fn, call
}

// rewriteWindow lowers the window that starts list, when the engine
// rewrites it, and returns the number of statements it took; 0 leaves list
// to the reference lowering.
func (fc *funcCtx) rewriteWindow(list []syntax.Stmt) int {
	if fc.l.rewrite == nil || rewriteLowering == "reference" {
		return 0
	}
	recv, prov, _, _ := fc.windowCall(list[0])
	if recv == nil {
		return 0
	}
	var calls []*syntax.CallExpr
	var window []rwCall
	for _, s := range list {
		v, p, f, call := fc.windowCall(s)
		if v != recv || p != prov {
			break
		}
		wc := rwCall{fn: f}
		for _, a := range call.Args {
			arg := rwArg{}
			if c, ok := fc.l.info.Values[a]; ok {
				arg.c = c
			}
			wc.args = append(wc.args, arg)
		}
		calls = append(calls, call)
		window = append(window, wc)
	}
	if ev, ok := fc.l.rewrite.ev.(*LedgerEvidence); ok {
		// The sites the evidence records are this window's (inspect).
		ev.file, ev.line, ev.col = fc.l.locate(list[0], list[0].Pos())
	}
	out, changed := fc.l.rewrite.run(prov, window)
	if !changed {
		// The reference lowering of the whole window: a window that
		// starts later in it has fewer facts, so it matches no more.
		for _, s := range list[:len(calls)] {
			fc.stmt(s)
			fc.releaseAt(s)
		}
		return len(calls)
	}
	// The receiver and every non-constant argument, in source order.
	fc.at(list[0])
	r := fc.temp("recv")
	receiver := fc.value(unparen(fc.l.info.Calls[calls[0]].Recv))
	_, borrowed := recv.Type.(*check.Pointer)
	if _, cell, ok := fc.cellWindow(recv); ok {
		// The cell's own value, through the adapter it lives in.
		receiver, borrowed = cell, true
	}
	pointer := borrowed
	for _, rule := range fc.l.rewrite.rules[prov] {
		_, lends := rule.conv.From.(*check.Pointer)
		pointer = pointer || lends
	}
	for _, c := range window {
		pointer = pointer || c.fn.Ptr
	}
	if pointer && !borrowed {
		receiver = "&" + receiver
	}
	fc.line(r + " := " + receiver)
	texts := map[*syntax.CallExpr][]string{}
	for _, call := range calls {
		ts := make([]string, len(call.Args))
		for j, a := range call.Args {
			if _, ok := fc.l.info.Values[a]; ok {
				continue
			}
			ts[j] = fc.temp("arg")
			fc.line(ts[j] + " := " + fc.value(a))
		}
		texts[call] = ts
	}
	// The engine's pass-through arguments name their window position.
	used := map[string]bool{}
	var lines []string
	for _, c := range out {
		var vals []string
		for j, a := range c.args {
			switch {
			case a.c != nil:
				vals = append(vals, fc.l.constGo(c.fn.Sig.Params[j+1].Type, a.c, calls[0]))
			default:
				pos, arg := rwPlace(a.text)
				vals = append(vals, texts[calls[pos]][arg])
				used[texts[calls[pos]][arg]] = true
			}
		}
		used[r] = true
		self := r
		if pointer && !c.fn.Ptr {
			self = "(*" + r + ")"
		}
		lines = append(lines, fc.rewriteCall(c.fn, self, vals, calls[0]))
	}
	// A temporary the rewritten calls no longer pass was still read
	// where the window starts; Go wants it used.
	temps := []string{r}
	for _, call := range calls {
		temps = append(temps, texts[call]...)
	}
	for _, t := range temps {
		if t != "" && !used[t] {
			fc.line("_ = " + t)
		}
	}
	for _, s := range lines {
		fc.line(s)
	}
	n := len(calls)
	for _, s := range list[:n] {
		fc.releaseAt(s)
	}
	return n
}

// rewriteCall is the Go call of the provider function f on recv with the
// argument texts args, as callText writes a plain function's call.
func (fc *funcCtx) rewriteCall(f *check.Func, recv string, args []string, site syntax.Node) string {
	fd := fc.l.useFunc(f, site)
	var lead []string
	if !fd.pure {
		lead = []string{fc.envExpr(site)}
	}
	if fd.method != nil {
		return "(" + recv + ")." + fd.name + "(" + strings.Join(append(lead, args...), ", ") + ")"
	}
	if recv != "" {
		lead = append(lead, recv)
	}
	return fd.name + "(" + strings.Join(append(lead, args...), ", ") + ")"
}

// rwPlace reads a pass-through argument's place, "k.j": the window's
// call k, its argument j.
func rwPlace(text string) (int, int) {
	k, j, _ := strings.Cut(text, ".")
	a, _ := strconv.Atoi(k)
	b, _ := strconv.Atoi(j)
	return a, b
}

// run rewrites the window of calls on a provider of type prov and reports
// whether any rule applied. A pass-through
// argument of the result names its place in the window ("k.j").
func (rw *rewriter) run(prov *check.Named, window []rwCall) ([]rwCall, bool) {
	calls := make([]rwCall, len(window))
	for k, c := range window {
		calls[k] = rwCall{fn: c.fn, args: make([]rwArg, len(c.args))}
		for j, a := range c.args {
			calls[k].args[j] = a
			if a.c == nil {
				calls[k].args[j].text = strconv.Itoa(k) + "." + strconv.Itoa(j)
			}
		}
	}
	var phases [3][]rwRule
	for _, r := range rw.rules[prov] {
		if r.post != nil {
			continue
		}
		switch {
		case r.law.Rewrite.Guard == nil && len(r.law.Rewrite.Lhs) == 1:
			phases[0] = append(phases[0], r)
		case r.law.Rewrite.Guard != nil:
			phases[1] = append(phases[1], r)
		default:
			phases[2] = append(phases[2], r)
		}
	}
	bound := rewriteWorkPerCall * len(window)
	work, changed := 0, false
	for progressed := true; progressed; {
		progressed = false
		for _, rules := range phases {
			for applied := true; applied; {
				applied = false
				facts := rw.facts(prov, calls)
			scan:
				for i := range calls {
					for _, r := range rules {
						work++
						rw.work++
						if work > bound {
							rw.refused++
							return nil, false
						}
						if next, ok := rw.try(prov, r, calls, i, facts[i]); ok {
							calls, applied, progressed, changed = next, true, true, true
							break scan
						}
					}
				}
			}
		}
	}
	if changed {
		rw.applied++
	}
	return calls, changed
}

// rwFact is a read's known value at a position: read the provider's
// function of the read operation.
type rwFact struct {
	read  *check.Func
	value constant.Value
}

// facts are the facts before each call of calls: none at the window's
// start; after a call, those its post laws give, and no other.
func (rw *rewriter) facts(prov *check.Named, calls []rwCall) [][]rwFact {
	out := make([][]rwFact, len(calls)+1)
	for k, c := range calls {
		var next []rwFact
		for _, r := range rw.rules[prov] {
			p := r.post
			if p == nil || r.conv.Table[p.op.Name] != c.fn || len(p.args) != len(c.args) {
				continue
			}
			v := p.value
			if v == nil {
				// X is a parameter: the argument the call binds to it,
				// when that is a constant.
				for j, i := range p.args {
					if i == p.param {
						v = c.args[j].c
					}
				}
			}
			if v == nil {
				continue
			}
			if ok, _ := rw.ev.Admit(r.law, prov, RewriteInstance{Law: r.law}); !ok {
				continue
			}
			next = append(next, rwFact{read: r.conv.Table[p.read.Name], value: v})
		}
		out[k+1] = next
	}
	return out
}

// try applies the rule r at position i of calls, whose facts before i are
// facts, and returns the rewritten calls.
func (rw *rewriter) try(prov *check.Named, r rwRule, calls []rwCall, i int, facts []rwFact) ([]rwCall, bool) {
	law, w := r.law, r.law.Rewrite
	if i+len(w.Lhs) > len(calls) {
		return nil, false
	}
	bound := make([]rwArg, len(law.Params))
	for k, lc := range w.Lhs {
		c := calls[i+k]
		if lc.Try || r.conv.Table[lc.Op.Name] != c.fn || len(lc.Args) != len(c.args) {
			return nil, false
		}
		for j, a := range lc.Args {
			bound[a.Param] = c.args[j]
		}
	}
	in := RewriteInstance{Law: law, Args: make([]constant.Value, len(law.Params))}
	binds := map[*check.Var]constant.Value{}
	for p, isConst := range w.Const {
		if !isConst {
			continue
		}
		if bound[p].c == nil {
			return nil, false
		}
		in.Args[p] = bound[p].c
		binds[law.Params[p]] = bound[p].c
	}
	if w.Guard != nil {
		want, why := eval.Fold(law.Pkg, law.Decl, w.GuardValue, nil, rewriteFoldBudget)
		if why != "" {
			return nil, false
		}
		read := r.conv.Table[w.Guard.Name]
		held := false
		for _, f := range facts {
			if f.read == read && constant.Compare(f.value, token.EQL, want) {
				held = true
			}
		}
		if !held {
			return nil, false
		}
	}
	var rhs []rwCall
	for _, rc := range w.Rhs {
		fn := r.conv.Table[rc.Op.Name]
		if rc.Try || !rewritable(fn) {
			return nil, false
		}
		out := rwCall{fn: fn}
		for _, a := range rc.Args {
			if a.Param >= 0 {
				out.args = append(out.args, bound[a.Param])
				continue
			}
			v, why := eval.Fold(law.Pkg, law.Decl, a.X, binds, rewriteFoldBudget)
			if why != "" {
				return nil, false
			}
			out.args = append(out.args, rwArg{c: v})
		}
		rhs = append(rhs, out)
	}
	if ok, _ := rw.ev.Admit(law, prov, in); !ok {
		return nil, false
	}
	next := append(append(append([]rwCall(nil), calls[:i]...), rhs...), calls[i+len(w.Lhs):]...)
	return next, true
}
