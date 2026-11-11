package lower

import (
	"go/token"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Ergo-side inlining (OPT4a). Go inlines a function only when its body
// costs at most 80 of Go's units, and a generic shape body pays for its
// dictionary at every call it makes, so a small Ergo helper over a std
// operation stays a Go call: lru made 6.5 calls per operation where hand
// Go makes 3 (attribution, wf10 OPT4). The lowering knows the whole
// program, so it writes a small body at its call site itself.
//
// The cost rule reads checked facts only, never a package or function
// name (internal/residue, TestLoweringKeysOnNoStdName). A call of a
// declared function f is inlined when
//   - f has an Ergo body: no extern, no Close, not main;
//   - its effects allow it: no task start or Task result, no capability
//     in its footprint (a needs row it does not use changes nothing), no
//     frame scope (a with that owns, an exit action), no context value,
//     no witness, at most one result;
//   - its body has no loop (a loop amortizes its call) and at most
//     inlineBudget syntax nodes;
//   - it is not recursive: no chain of calls from its body reaches it;
//   - each callback parameter is non-escaping with a plain shape (its rows
//     may be the callee's row parameters or a declared failure), the body
//     only calls it, and the call passes for it a known pure literal
//     (known.go, pureLit) or names a pure package function (pureFuncArg:
//     no environment, no failure, no type parameter, an Ergo body). The
//     literal's body is written where the body calls the parameter (beta
//     reduction), its parameters bound to the call's operands; a named
//     function is called there directly, as a known variant calls its
//     plain callback. Either way the callback fails nothing here. The
//     footprint of f is empty, or opaque only through such calls
//     (knownVariant, or inlineCb: every other call of its body is one a
//     known variant may make): pure code cannot re-enter the frame, so the
//     entry poll it would need is not written, as a known variant writes
//     none;
//   - its site is at most inlineDepth sites deep, and the Go function has
//     taken at most inlineGrowth nodes of inlined bodies (the growth
//     bound).
//
// An inlined call evaluates its operands as the call does, left to right,
// binds each to a fresh local of the parameter's type (a Go call's copy),
// and writes the body in a Go block. An owned parameter (V5) takes its
// argument as the value rule passes it: every call of a function with an
// owned parameter is an ownCall, whose argument the rule copies where it
// stays live (values.go clone) and hands over where it does not. The
// block fails only where the call can (failsHere): a row parameter of the
// callee's errors that is empty at the call adds no failure. So std's
// maps.Update(m, k, f), with f a pure function, is written at its site
// with f called directly and m written in place (OPT6; it replaced a
// rewrite keyed on Update's name). A return assigns the result and the
// error and jumps past the block (goto); the block's last return falls
// through. Failures, defects and polls are the body's own: the frame of
// the block has the callee's result, failure row and footprint.
//
// The defect report stays exact (runtime.md, Exit causes 6): the lines of
// an inlined body name their file with a site tag (inlineTag), and the
// program registers each site, the call table entry of the inlined call
// and the site around it (v1.RegisterInlines). The report writes the
// frames the inlined calls would have had.
//
// The reference is inlineLowering "call": every call is a Go call, as
// before OPT4a. It is set by a test, or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.inlineLowering=call.
// TestInlineAgrees runs generated programs under both.
//
// Prior art: OCaml's flambda inlines by a size budget before the backend
// and substitutes a known closure's body; ReScript inlines small functions
// and constants across modules before it writes JavaScript; GHC inlines by
// size and reduces a known lambda (beta reduction). Gleam and Elixir leave
// inlining to the host (the BEAM JIT, a JavaScript engine); Erlang inlines
// only on request (inline_size).
var inlineLowering string

const (
	// inlineBudget is the most syntax nodes a body has to be inlined.
	inlineBudget = 120
	// inlineDepth is the most inlined sites around an inlined call.
	inlineDepth = 4
	// inlineGrowth is the most body nodes one Go function takes in.
	inlineGrowth = 800
)

// inlineRet is where the returns of an inlined body go: its result and
// error variables, and the label after its block.
type inlineRet struct {
	res, err, label string
	gotos           int
}

// inlineSite is one inlined call: its call table key and the site around
// it (its index plus one, 0 for none).
type inlineSite struct {
	src, offset int
	parent      int
}

// litSub is a pure literal written where an inlined body calls its
// parameter: the literal and the code it was written in.
type litSub struct {
	lit    *syntax.FuncLit
	origin *funcCtx
	info   *check.Info
}

// inlineFrame is the frame of an inlined body: a Go block of the frame
// that calls it, not a Go frame (newFrame and the frame catalog,
// TestRuntime199FrameClassCoverage, own those). It has no interruption
// point of its own: the rule inlines no loop and no capability call, and
// its entry poll is dropped only where nothing can re-enter it (an empty
// footprint, a closed frame, or known pure literals), as a known variant's
// is (known.go).
func inlineFrame(result check.Type, fallible bool, ret *inlineRet) *frame {
	return &frame{result: result, fallible: fallible, inline: ret}
}

// inlineTag marks a path in a //line directive with the inline site of the
// lines that follow (v1.RegisterInlines reads it back).
const inlineTag = "#ergo-inline-"

// inlineBody reports whether f's body may be inlined (the rule above, but
// for the call's own conditions).
func (l *lowerer) inlineBody(f *check.Func) bool { return l.inlineWithin(f, inlineBudget) }

// inlineWithin is inlineBody with budget as the most syntax nodes: a
// field call of a known record in a copy (record.go) takes inlineGrowth,
// since the copy is written once for its record, not at each call.
func (l *lowerer) inlineWithin(f *check.Func, budget int) bool {
	if inlineLowering == "call" || f == nil {
		return false
	}
	if l.inlineOK == nil {
		l.inlineOK = map[inlineKey]knownState{}
	}
	key := inlineKey{f, budget}
	switch l.inlineOK[key] {
	case knownYes:
		return true
	case knownNo, knownDeciding:
		return false
	}
	l.inlineOK[key] = knownDeciding
	ok := l.inlineRule(f, budget)
	l.inlineOK[key] = knownNo
	if ok {
		l.inlineOK[key] = knownYes
	}
	return ok
}

// inlineKey is one inline decision: a function and its budget.
type inlineKey struct {
	f      *check.Func
	budget int
}

// inlineRule is inlineWithin's decision for an undecided f.
func (l *lowerer) inlineRule(f *check.Func, budget int) bool {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" || isClose(f) {
		return false
	}
	info := f.Pkg.Info
	s := f.Sig
	if s.ResultIsTask() || info.TaskResults[f] || len(witnessParams(s)) > 0 || len(s.Results) > 1 {
		return false
	}
	fp, ok := info.Footprints[d]
	if !ok || len(fp.Caps) > 0 || fp.Tasks {
		return false
	}
	vars := paramVars(f)
	if len(vars) != len(s.Params) {
		return false
	}
	cbs := map[*check.Var]bool{}
	for _, i := range callbackParams(f) {
		cb := s.Params[i].Type.(*check.Signature)
		if !plainShape(cb) || !cb.NonEscaping || vars[i] == nil {
			return false
		}
		cbs[vars[i]] = true
	}
	if !fp.Empty() && len(cbs) == 0 && !l.knownClosed(f) {
		return false
	}
	size := inlineSize(d.Body)
	if size > budget {
		return false
	}
	called := map[*syntax.Ident]bool{}
	ok = true
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.ForStmt, *syntax.RangeStmt:
			ok = false
		case *syntax.FuncLit:
			// A literal is a Go frame of its own: a callback parameter
			// used in it would need a binding.
			syntax.Inspect(n, func(m syntax.Node) bool {
				if id, isID := m.(*syntax.Ident); isID {
					if v, isVar := info.Uses[id].(*check.Var); isVar && cbs[v] {
						ok = false
					}
				}
				return ok
			})
		case *syntax.CallExpr:
			c := info.Calls[n]
			if c == nil || n.Trailing != nil || c.Kind == check.CallWith || c.TaskResult {
				ok = false
				break
			}
			if c.Kind == check.CallFuncValue {
				if id, isID := unparen(n.Fun).(*syntax.Ident); isID {
					if v, isVar := info.Uses[id].(*check.Var); isVar && cbs[v] {
						called[id] = true
					}
				}
			}
		case *syntax.Ident:
			if v, isVar := info.Uses[n].(*check.Var); isVar && cbs[v] && !called[n] {
				ok = false
			}
		}
		if x, isExpr := n.(syntax.Expr); isExpr && ok && info.ContextValues[x] != nil {
			ok = false
		}
		return ok
	})
	if !ok {
		return false
	}
	saved := l.info
	l.info = info
	frame := l.bodyNeedsFrame(d.Body.List)
	l.info = saved
	if frame || l.inlineRecursive(f) {
		return false
	}
	// Unknown code only through the callbacks: every call of the body is
	// one a known variant may make (knownBodyCall), with the callback
	// parameters plain. Where the call passes known pure functions for
	// them, nothing can re-enter the frame (inlineAt).
	if len(cbs) > 0 {
		only := true
		allowed := map[*syntax.Ident]bool{}
		syntax.Inspect(d.Body, func(n syntax.Node) bool {
			if x, isCall := n.(*syntax.CallExpr); isCall && only && !l.knownBodyCall(info, x, cbs, allowed, false) {
				only = false
			}
			return only
		})
		if l.inlineCb == nil {
			l.inlineCb = map[*check.Func]bool{}
		}
		l.inlineCb[f] = only
	}
	return true
}

// plainShape reports a callback signature that a plain Go function can
// carry but for its rows: no type parameter, at most one result, no Task.
// Its rows may be anything (a row parameter of the callee, a declared
// failure): a call is inlined only where the function it passes is plain
// (pureLit, pureFuncArg), one that fails nothing and needs nothing.
func plainShape(cb *check.Signature) bool {
	return cb != nil && len(cb.TParams) == 0 && len(cb.Results) <= 1 && !cb.ResultIsTask()
}

// inlineSize is the cost of a body: its syntax nodes.
func inlineSize(n syntax.Node) int {
	size := 0
	syntax.Inspect(n, func(syntax.Node) bool {
		size++
		return true
	})
	return size
}

// inlineRecursive reports whether a chain of calls from f's body reaches f.
func (l *lowerer) inlineRecursive(f *check.Func) bool {
	seen := map[*check.Func]bool{}
	var reaches func(g *check.Func) bool
	reaches = func(g *check.Func) bool {
		if g.Decl == nil || g.Decl.Body == nil || g.Pkg == nil || g.Pkg.Info == nil {
			return false
		}
		found := false
		syntax.Inspect(g.Decl.Body, func(n syntax.Node) bool {
			if found {
				return false
			}
			if x, ok := n.(*syntax.CallExpr); ok {
				if c := g.Pkg.Info.Calls[x]; c != nil && c.Kind == check.CallFunc && c.Func != nil {
					switch {
					case c.Func == f:
						found = true
					case !seen[c.Func]:
						seen[c.Func] = true
						found = reaches(c.Func)
					}
				}
			}
			return !found
		})
		return found
	}
	return reaches(f)
}

// inlineAt inlines the call x when the rule allows it, and reports whether
// it did; the call's value is then fc.inlined.
func (fc *funcCtx) inlineAt(x *syntax.CallExpr, c *check.Call) bool {
	if inlineLowering == "call" || fc.frame == nil || x.Trailing != nil {
		return false
	}
	if c.Kind == check.CallFuncValue {
		id, ok := unparen(x.Fun).(*syntax.Ident)
		if !ok || fc.lits == nil {
			return false
		}
		v, ok := fc.l.info.Uses[id].(*check.Var)
		if !ok || fc.lits[v] == nil {
			return false
		}
		op := fc.inlineLit(x, c, fc.lits[v])
		fc.inlined = &op
		return true
	}
	if c.Kind != check.CallFunc || c.TaskResult || fc.inlDepth >= inlineDepth || c.Func == nil {
		return false
	}
	if len(c.Func.Sig.Results) == 1 && fc.noValue(x) {
		return false // a result a block with no value binds: struct{}
	}
	f := c.Func
	if !fc.l.inlineBody(f) {
		return false
	}
	fd := fc.l.funcDecl(f)
	if !fd.pure && fc.env == "" {
		return false
	}
	list := callOperands(x, c)
	n := len(list)
	if c.Site != nil {
		n++
	}
	if n != len(f.Sig.Params) {
		return false
	}
	cbs := callbackParams(f)
	lits := map[int]*syntax.FuncLit{}
	funs := map[int]*check.Func{}
	for _, i := range cbs {
		// The callback is plain at this instance (pureFuncArg and pureLit
		// read the argument's own signature), so the rows plainRows let
		// through are empty here.
		if g := fc.pureFuncArg(list[i]); g != nil {
			funs[i] = g
			continue
		}
		lit, ok := unparen(list[i]).(*syntax.FuncLit)
		if !ok || !fc.l.pureLit(fc.l.info, lit, fc.env != "") {
			return false
		}
		lits[i] = lit
	}
	if fp := f.Pkg.Info.Footprints[f.Decl]; !fp.Empty() && !(len(cbs) > 0 && (fc.l.knownVariant(f) || fc.l.inlineCb[f])) && !fc.l.knownClosed(f) {
		return false
	}
	if fc.l.inlineGrown == nil {
		fc.l.inlineGrown = map[*namer]int{}
	}
	size := inlineSize(f.Decl.Body)
	if fc.l.inlineGrown[fc.scope]+size > inlineGrowth {
		return false
	}
	fc.l.inlineGrown[fc.scope] += size
	if token.IsExported(f.Name) {
		// An exported function keeps its Go function: inlining copies its
		// body into the caller, and the declaration stays.
		fc.l.useFunc(f, x)
	}
	op := fc.inlineCall(x, c, fd, list, lits, funs)
	fc.inlined = &op
	return true
}

// pureFuncArg is the function x names when x names a pure package
// function (an empty footprint: no environment, no capability, no
// failure) with an Ergo body, no type parameter and no witness, not a
// method: an inlined body calls it directly (inlineCall). Else nil.
func (fc *funcCtx) pureFuncArg(x syntax.Expr) *check.Func {
	var id *syntax.Ident
	switch e := unparen(x).(type) {
	case *syntax.Ident:
		id = e
	case *syntax.SelectorExpr:
		pkg, ok := unparen(e.X).(*syntax.Ident)
		if !ok {
			return nil
		}
		if _, ok := fc.l.info.Uses[pkg].(*check.PkgName); !ok {
			return nil
		}
		id = e.Sel
	default:
		return nil
	}
	g, ok := fc.l.info.Uses[id].(*check.Func)
	if !ok || g.Decl == nil || g.Decl.Body == nil || g.Decl.Extern != nil || g.Pkg == nil || g.Pkg.Info == nil || g.Sig == nil {
		return nil
	}
	if len(g.Sig.TParams) > 0 || len(witnessParams(g.Sig)) > 0 || g.Pkg.Info.TaskResults[g] || !plainSig(g.Sig) {
		return nil
	}
	gd := fc.l.funcDecl(g)
	if gd.method != nil || gd.fallible || !gd.pure {
		return nil
	}
	return g
}

// inlineCall writes the inlined call x of fd (inlineAt decided it): its
// operands, the block of its body and the label its returns jump to.
func (fc *funcCtx) inlineCall(x *syntax.CallExpr, c *check.Call, fd *funcDecl, list []syntax.Expr, lits map[int]*syntax.FuncLit, funs map[int]*check.Func) operand {
	f := c.Func
	addr := false
	if c.Recv != nil && f.Ptr {
		_, isPtr := fc.typeOf(c.Recv).(*check.Pointer)
		addr = !isPtr
	}
	skip := map[int]bool{}
	for i := range lits {
		skip[i] = true
	}
	named, plainType := map[int]string{}, map[int]string{}
	for i, g := range funs {
		skip[i] = true
		named[i], plainType[i] = fc.l.useFunc(g, x).name, fc.plainFuncType(g.Sig)
	}
	vals := fc.inlineArgs(list, x, addr, skip)
	ic := &funcCtx{typeCtx: typeCtx{l: fc.l, params: map[*check.TypeParam]string{}, site: x, mono: fc.mono}, out: &strings.Builder{}, scope: fc.scope, locals: fc.locals, witness: map[*check.TypeParam]string{}, directives: fc.l.isSourced(f.Pkg), inlDepth: fc.inlDepth + 1}
	for i, tp := range f.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(c.TypeArgs) {
			ice(x, "an inlined call with %d type arguments for %d parameters", len(c.TypeArgs), len(f.Sig.TParams))
		}
		ic.params[tp] = fc.goType(c.TypeArgs[i])
		if t := fc.boundType(c.TypeArgs[i]); !mentionsTypeParam(t) {
			if ic.bound == nil {
				ic.bound = map[*check.TypeParam]check.Type{}
			}
			ic.bound[tp] = t
		}
	}
	ic.owner = f.Name
	if fd.method != nil {
		ic.owner = fd.method.tn.Name + "." + f.Name
	}
	if !fd.pure {
		ic.env = fc.env
	}
	ic.inl = fc.site0(x, c)
	ret := &inlineRet{label: fc.temp("inl")}
	var result check.Type
	if len(f.Sig.Results) == 1 {
		result = f.Sig.Results[0]
	}
	if result != nil {
		ret.res = fc.temp("v")
		fc.line("var " + ret.res + " " + ic.goType(result))
	}
	// The block fails where the call can: a row parameter of f's errors
	// that is empty at this instance gives no failure (failsHere).
	fallible := fd.fallible && failsHere(c)
	if fallible {
		ret.err = fc.temp("err")
		fc.line("var " + ret.err + " error")
	}
	ic.frame = inlineFrame(result, fallible, ret)
	fc.l.stats.Inlined++
	before := fc.l.lastDir
	callerInfo := fc.l.info
	func() {
		defer fc.l.leave(fc.l.enter(f.Decl.Name, f.Pkg))
		if !ic.directives && f.Pkg.Info != fc.l.info {
			saved := fc.l.info
			fc.l.info = f.Pkg.Info
			defer func() { fc.l.info = saved }()
		}
		ic.frame.polls = framePolls(f.Pkg.Info, f.Decl)
		ic.depth = 1
		params := paramVars(f)
		for i, p := range f.Sig.Params {
			if lit := lits[i]; lit != nil {
				if ic.lits == nil {
					ic.lits = map[*check.Var]*litSub{}
				}
				ic.lits[params[i]] = &litSub{lit: lit, origin: fc, info: callerInfo}
				continue
			}
			if g := named[i]; g != "" {
				// A pure package function: the body calls it directly,
				// as a known variant calls a plain callback (known.go).
				if params[i] == nil {
					continue
				}
				if ic.plain == nil {
					ic.plain = map[*check.Var]bool{}
				}
				ic.plain[params[i]] = true
				name := ic.declare(params[i])
				ic.line("var " + name + " " + plainType[i] + " = " + g)
				ic.line("_ = " + name)
				continue
			}
			if params[i] == nil {
				ic.line("_ = " + vals[i])
				continue
			}
			name := ic.declare(params[i])
			ic.line("var " + name + " " + ic.goType(p.Type) + " = " + vals[i])
			ic.line("_ = " + name)
		}
		fc.l.analyzeValues(f.Decl, f.Pkg.Info)
		ic.goGeneric = fc.goGeneric
		ic.scalarSet = fc.l.scalarVars(f.Decl.Body, f.Pkg.Info)
		ic.body(f.Decl.Body.List)
	}()
	fc.closeInline(ic.out.String(), ret, before)
	return operand{val: ret.res, err: ret.err}
}

// inlineArgs are the operands of an inlined call, evaluated as callArgsAddr
// evaluates them; a substituted literal or a named pure function (skip)
// has none.
func (fc *funcCtx) inlineArgs(list []syntax.Expr, call *syntax.CallExpr, addr bool, skip map[int]bool) []string {
	later := make([]bool, len(list)+1)
	for i := len(list) - 1; i >= 0; i-- {
		later[i] = later[i+1] || (!skip[i] && fc.emits(list[i]))
	}
	out := make([]string, len(list))
	for i, x := range list {
		if skip[i] {
			continue
		}
		if i == 0 && addr {
			out[i] = fc.place(x, later[1])
		} else {
			out[i] = fc.assigned(x)
		}
		if later[i+1] && !fc.isConst(x) {
			out[i] = fc.spill(out[i])
		}
	}
	if c := fc.l.info.Calls[call]; c != nil && c.Site != nil {
		out = append(out, fc.siteValue(call, c.Site))
	}
	return out
}

// site0 records the identity of the inlined call x, writes its directive,
// and returns its inline site (plus one) for the lines of its body; 0 when
// the code fc writes has no directives.
func (fc *funcCtx) site0(x *syntax.CallExpr, c *check.Call) int {
	if !fc.directives || !x.Pos().IsValid() {
		return fc.inl
	}
	fc.l.recordCall(x, fc.owner, fc.callee(x, c))
	fc.at(x)
	src, _, _, _ := fc.l.at(x, x.Pos())
	fc.l.inlineSites = append(fc.l.inlineSites, inlineSite{src: src, offset: int(x.Pos()), parent: fc.inl})
	return len(fc.l.inlineSites)
}

// closeInline writes an inlined body: its block, then its label when a
// return jumps to it, then the directive that was current before it.
func (fc *funcCtx) closeInline(body string, ret *inlineRet, before string) {
	jump := "goto " + ret.label + "\n"
	// The block's last return falls through.
	if strings.HasSuffix(body, "\t"+jump) && !strings.HasSuffix(body, "\t\t"+jump) {
		body = strings.TrimSuffix(body, "\t"+jump)
	}
	fc.line("{")
	ind := strings.Repeat("\t", fc.depth)
	for _, ln := range strings.SplitAfter(body, "\n") {
		if ln == "" {
			continue
		}
		if strings.HasPrefix(ln, "//line ") {
			fc.out.WriteString(ln)
			continue
		}
		fc.out.WriteString(ind + ln)
	}
	fc.line("}")
	if strings.Contains(body, jump) {
		fc.line(ret.label + ":")
	}
	if ret.res != "" {
		fc.line("_ = " + ret.res)
	}
	if ret.err != "" {
		fc.line("_ = " + ret.err)
	}
	// Only a body with directives changed the current one. Code with
	// directives names its own again; code with none (a std body, a copy
	// for a known record, record.go) names no Ergo file again, so a
	// defect report reads none of its lines as the inlined body's.
	switch {
	case !strings.Contains(body, "//line "):
	case !fc.directives:
		fc.writeDir(generatedDir)
	case before != "" && fc.l.lastDir != before:
		fc.writeDir(before)
	}
}

// generatedDir is the directive after an inlined body with directives in
// code that has none: its path is no source of the program, so a defect
// report skips the lines it names, as it skips the generated code's own.
const generatedDir = "//line ergo-generated.go:1\n"

// inlineLit writes the substituted literal sub where the inlined body fc
// calls its parameter (x): the call's operands, bound to the literal's
// parameters, then its body as a block, as inlineCall writes a body. The
// call of the parameter is an inline site: the defect report writes the
// frame the call of the literal would have had.
func (fc *funcCtx) inlineLit(x *syntax.CallExpr, c *check.Call, sub *litSub) operand {
	vals := fc.callArgs(x.Args, x)
	o := sub.origin
	sig := sub.info.Literals[sub.lit]
	if sig == nil {
		ice(sub.lit, "a function literal with no Literals entry")
	}
	lc := &funcCtx{typeCtx: o.typeCtx, out: &strings.Builder{}, scope: fc.scope, locals: o.locals, witness: o.witness, directives: o.directives, owner: o.owner, env: o.env, plain: o.plain, inl: fc.site0(x, c), inlDepth: fc.inlDepth + 1}
	ret := &inlineRet{label: fc.temp("inl")}
	var result check.Type
	if len(sig.Results) == 1 {
		result = sig.Results[0]
	}
	if result != nil {
		ret.res = fc.temp("v")
		fc.line("var " + ret.res + " " + lc.goType(result))
	}
	lc.frame = inlineFrame(result, false, ret)
	fc.l.stats.Substituted++
	before := fc.l.lastDir
	func() {
		defer fc.l.leave(fc.l.enter(sub.lit, nil))
		saved := fc.l.info
		fc.l.info = sub.info
		defer func() { fc.l.info = saved }()
		lc.depth = 1
		var params []*check.Var
		if sub.lit.Type != nil && sub.lit.Type.Params != nil {
			for _, field := range sub.lit.Type.Params.List {
				for _, id := range field.Names {
					v, _ := sub.info.Defs[id].(*check.Var)
					params = append(params, v)
				}
			}
		}
		if len(params) != len(sig.Params) || len(vals) != len(sig.Params) {
			ice(sub.lit, "a substituted literal declares %d parameters for %d operands", len(params), len(vals))
		}
		for i, p := range sig.Params {
			if params[i] == nil {
				lc.line("_ = " + vals[i])
				continue
			}
			name := lc.declare(params[i])
			lc.line("var " + name + " " + lc.goType(p.Type) + " = " + vals[i])
			lc.line("_ = " + name)
		}
		lc.body(sub.lit.Body.List)
	}()
	fc.closeInline(lc.out.String(), ret, before)
	return operand{val: ret.res}
}

// inlineReturn writes a return of the inlined body: its values into the
// site's variables, then the jump past its block.
func (fc *funcCtx) inlineReturn(vals []string) {
	in := fc.frame.inline
	var lhs, rhs []string
	i := 0
	if in.res != "" && len(vals) > 0 {
		lhs, rhs = append(lhs, in.res), append(rhs, vals[0])
		i = 1
	}
	if in.err != "" && i < len(vals) && vals[i] != "nil" {
		lhs, rhs = append(lhs, in.err), append(rhs, vals[i])
	}
	if len(lhs) > 0 {
		fc.line(strings.Join(lhs, ", ") + " = " + strings.Join(rhs, ", "))
	}
	fc.line("goto " + in.label)
}

// writeDir writes a //line directive and remembers it as the current one.
func (fc *funcCtx) writeDir(d string) {
	fc.out.WriteString(d)
	fc.l.lastDir = d
}

// tagPath is path with the inline site tag of the code fc writes.
func (fc *funcCtx) tagPath(path string) string {
	if fc.inl == 0 {
		return path
	}
	return path + inlineTag + strconv.Itoa(fc.inl-1)
}

// inlineTable is the Go declaration of the program's inline sites and the
// statement of the source map's init that registers them
// (v1.RegisterInlines), after the call table is in source order: each
// site's call table entry and the site around it. Both are empty when no
// call was inlined, so a program with none registers nothing.
func (l *lowerer) inlineTable(entryOf func(src, offset int) int) (decl, register string) {
	if len(l.inlineSites) == 0 {
		return "", ""
	}
	name := l.pkgName(pkgKey{"inlines", nil}, "ergoInlines")
	var b strings.Builder
	b.WriteString("// " + name + " lists the program's inlined calls: each one's call table\n// entry and the inlined call around it, or -1 (runtime.md, Exit causes 6).\n")
	b.WriteString("var " + name + " = [...]struct{ Call, Parent int }{\n")
	for _, s := range l.inlineSites {
		b.WriteString("\t{Call: " + strconv.Itoa(entryOf(s.src, s.offset)) + ", Parent: " + strconv.Itoa(s.parent-1) + "},\n")
	}
	b.WriteString("}\n\n")
	return b.String(), "\t" + l.rt() + ".RegisterInlines(" + name + "[:])\n"
}
