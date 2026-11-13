package eval

import (
	"fmt"
	"unsafe"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// interp is one evaluation request: its budget and the work it used.
type interp struct {
	budget Budget
	root   *check.Package
	steps  int64
	bytes  int64 // value bytes allocated
	// stepsAt and bytesAt are steps and bytes when the obligation in
	// progress began (the package's initializers are their own): each
	// obligation has the whole budget, so one that runs no longer leaves
	// less to the obligations after it.
	stepsAt, bytesAt int64
	// factTables are the facts of each package's expressions (facts.go);
	// factInfo and facts are the table in use.
	factTables map[*check.Info]factTable
	// framePool holds cleared call frames for reuse (freeFrame).
	framePool []*frame
	factInfo  *check.Info
	facts     factTable
	// params and owners are a function's parameters and a declaration's
	// fact owner, read once (facts.go).
	params map[*check.Func][]*check.Var
	owners map[unsafe.Pointer]check.FactOwner
	files  map[syntax.Decl]*syntax.File
	// task is the running task's own state: its call depth, its context
	// values and its active slice ranges (sched.go); the scheduler swaps
	// it when another task runs.
	taskState
	// sched runs the tasks of the obligation being evaluated (sched.go);
	// bodies counts the case bodies run, for each one's schedule seed.
	sched  *scheduler
	bodies int
	// host is the simulated process of a RunMain run (process.go), nil
	// under Run.
	host *host
	// members are the exported lets and functions of each imported
	// package, by name (effects.go, memberOf).
	members map[*check.Package]map[string]check.Object
	// globals are the package-level lets, bound once by the packages'
	// initializers before any obligation runs (init.go). Nothing writes
	// them after.
	globals map[*check.Var]*slot
	// initials are the machines' initial values, computed once after
	// every let (machine.go initMachines).
	initials map[*check.TypeName]Value
	// mapOrder is the order this run visits a Map's keys in, and
	// mapRanged says a for over a Map ran (maporder.go).
	mapOrder  mapOrder
	mapRanged bool
	// pairs are the by-name codec pairs every package's check recorded
	// (codecwalk.go), built at the first codec call.
	pairs map[*check.TypeName]*check.WirePair
}

// frame is one running body: a test, a law, a function call or a call of
// a function literal. Locals are keyed by the checker's *Var, which is
// unique per binding, so a frame needs no scope chain; a literal called
// where it is written shares its enclosing frame's slots, which it reads
// and writes in place (types.md, Function values 2).
type frame struct {
	pkg  *check.Package
	info *check.Info
	decl syntax.Decl // the declaration whose body runs, for sites
	vars *varTable
	// ownVars is vars for a frame newFrame makes: one allocation, not two.
	ownVars varTable
	// tab is the fact table of the frame's package, once it asked (facts.go).
	tab factTable
	// env is a generic call's substitution, nil outside one.
	env *tenv
	// owner is the fact owner of the body (test, law or production): an
	// error type only a test's row names is a failure in a test body.
	owner  check.FactOwner
	lawOwn Value // the law's own provider: an *operation or an opObject
	// resultPos is the expression returned by this frame, including the
	// chosen nested tail. The enclosing law uses it for a false result.
	resultPos syntax.Pos
	// caps are the capabilities bound here (effects.go).
	caps *capEnv
	// key is the frame's Footprints key: its declaration, or the literal
	// or trailing block a closure runs (interruption points, sched.go).
	key syntax.Node
	// Owned values (own.go): own is the innermost owner, the frame's
	// (frameOwn) or an owning with's; owned names each entry by its
	// checker key; kept are the values the frame returns; returned is the
	// owned local a return or the body's tail gives back; held are the
	// owned locals a taking std call holds until it returns; outer is the
	// frame a literal was made in.
	own      *owner
	frameOwn *owner
	owned    map[any]*owned
	kept     []*owned
	returned *syntax.Ident
	held     map[*syntax.Ident]bool
	outer    *frame
	// escaped marks a frame something may still hold once its call
	// returns: a function literal or a trailing block made in it, or an
	// actor it started (ORACLE O33). A call's frame that did not escape
	// goes back to the pool (freeFrame).
	escaped bool
}

func (in *interp) newFrame(pkg *check.Package, decl syntax.Decl) *frame {
	var fr *frame
	if n := len(in.framePool); n > 0 {
		fr = in.framePool[n-1]
		in.framePool = in.framePool[:n-1]
		fr.pkg, fr.info, fr.decl, fr.key, fr.owner, fr.tab = pkg, pkg.Info, decl, decl, in.ownerOf(pkg, decl), in.table(pkg.Info)
	} else {
		fr = &frame{pkg: pkg, info: pkg.Info, decl: decl, key: decl, owner: in.ownerOf(pkg, decl), tab: in.table(pkg.Info)}
	}
	fr.vars = &fr.ownVars
	return fr
}

// freeFrame gives fr, a call's frame whose call returned, back to the
// pool, unless it escaped (ORACLE O33). Nothing else holds a frame past
// its call: a borrow of its slots ends with the call (the checker's
// Borrows rules), a range ends with its loop, and its owned values were
// released or handed to the caller when it ended. The frame is cleared,
// so a use after it is freed reads no stale slot.
func (in *interp) freeFrame(fr *frame) {
	if fr.escaped || len(in.framePool) >= maxPooledFrames {
		return
	}
	mk, mv := fr.ownVars.moreKeys[:0], fr.ownVars.moreVals[:0]
	clear(mv[:cap(mv)])
	*fr = frame{}
	fr.ownVars.moreKeys, fr.ownVars.moreVals = mk, mv
	in.framePool = append(in.framePool, fr)
}

const maxPooledFrames = 256

// bind binds v to a new slot holding val.
func (fr *frame) bind(v *check.Var, val Value) {
	if v != nil {
		fr.vars.bind(v, val)
	}
}

// rt is t read in the frame's substitution.
func (fr *frame) rt(t check.Type) rtype { return fr.env.resolve(t) }

// rtypeOf is x's checked type in the frame's substitution.
func (fr *frame) rtypeOf(x syntax.Expr) rtype { return fr.rt(fr.typeOf(x)) }

// ctlKind is how evaluation left the normal flow.
type ctlKind int

const (
	ctlReturn ctlKind = iota + 1 // return, with val
	ctlBreak
	ctlContinue
	// ctlFail is the failure of an untried fallible call, val the failure:
	// a catch handles it, and a tail or return passes it out of the frame.
	ctlFail
	// ctlEscape is a failure leaving the frame (try, a returned error
	// value, a partial catch's unhandled failure): no catch inside the
	// frame handles it.
	ctlEscape
	ctlAssert      // a false assert
	ctlDefect      // a defect: stops the run, no catch handles it
	ctlExhausted   // the budget ran out
	ctlUnsupported // a form check-time evaluation does not run
	// ctlInterrupt is a stopped task's exit from an interruption point:
	// no catch handles it, and its frames release what they own (sched.go).
	ctlInterrupt
	// ctlAbort unwinds a task once the obligation has ended (an exhausted
	// budget, a skip or a deadlock elsewhere): nothing more runs.
	ctlAbort
)

// abrupt is a completion other than a normal one, with its site.
type abrupt struct {
	kind ctlKind
	val  Value
	msg  string
	pkg  *check.Package
	decl syntax.Decl
	at   syntax.Pos
}

func (fr *frame) abrupt(kind ctlKind, at syntax.Node, val Value, msg string) *abrupt {
	pos := syntax.NoPos
	if at != nil {
		pos = at.Pos()
	}
	return &abrupt{kind: kind, val: val, msg: msg, pkg: fr.pkg, decl: fr.decl, at: pos}
}

func (fr *frame) defect(at syntax.Node, format string, args ...any) *abrupt {
	return fr.abrupt(ctlDefect, at, nil, fmt.Sprintf(format, args...))
}

func (fr *frame) unsupported(at syntax.Node, format string, args ...any) *abrupt {
	return fr.abrupt(ctlUnsupported, at, nil, "check-time evaluation does not run "+fmt.Sprintf(format, args...))
}

// charge spends n steps before the work they pay for; it returns the
// exhaustion when the obligation's budget cannot pay.
func (in *interp) charge(fr *frame, at syntax.Node, n int64) *abrupt {
	if n < 0 || n > in.stepsLeft() {
		in.steps = in.stepsAt + in.budget.Steps
		return fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the evaluation used its %d steps", in.budget.Steps))
	}
	in.steps += n
	return nil
}

// allocate spends n bytes of the byte budget before the allocation they
// pay for; it returns the exhaustion when the budget cannot pay. Like a
// step stop, a byte stop is incomplete, never passed.
func (in *interp) allocate(fr *frame, at syntax.Node, n int64) *abrupt {
	if n < 0 || in.bytes-in.bytesAt > in.budget.Bytes-n {
		in.bytes = in.bytesAt + in.budget.Bytes
		return fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the evaluation allocated its %d bytes", in.budget.Bytes))
	}
	in.bytes += n
	return nil
}

// stepsLeft is what the obligation in progress may still spend.
func (in *interp) stepsLeft() int64 { return in.budget.Steps - (in.steps - in.stepsAt) }

// begin gives the obligation that starts now the whole budget.
func (in *interp) begin() { in.stepsAt, in.bytesAt = in.steps, in.bytes }

// build charges both budgets of an allocation of slots collection slots
// and raw bytes: steps for the work, bytes for the values.
func (in *interp) build(fr *frame, at syntax.Node, steps, slots, raw int64) *abrupt {
	if ab := in.charge(fr, at, steps); ab != nil {
		return ab
	}
	if slots > (1<<62)/valueBytes {
		return in.allocate(fr, at, -1)
	}
	return in.allocate(fr, at, slots*valueBytes+raw)
}

// typeOf is x's checked type; a missing entry is an internal error.
func (fr *frame) typeOf(x syntax.Expr) check.Type {
	if fr.tab != nil {
		if t := fr.tab.of(fr.info, x).typ; t != nil {
			return t
		}
	}
	t, ok := fr.info.Types[x]
	if !ok || t == nil {
		ice("no type for the expression at %d (%T)", x.Pos(), x)
	}
	return t
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

// call runs fn's body with args as a new frame (a CallFunc of a declared
// function with a body). It returns the result, or a ctlFail with the
// callee's failure.
func (in *interp) call(fr *frame, at *syntax.CallExpr, fn *check.Func, targs []check.Type, args []Value) (Value, *abrupt) {
	if len(fn.Sig.TParams) == 0 {
		return in.callResolved(fr, at, fn, nil, args)
	}
	if at != nil && fr.info.Instances[at] != nil && len(fr.info.Instances[at].Witnesses) > 0 {
		return nil, fr.unsupported(at, "a generic call whose type parameter an operation type constrains (%s) yet", fn.Name)
	}
	// Each type argument is read in the caller's substitution.
	var rt []rtype
	for _, t := range targs {
		rt = append(rt, fr.rt(t))
	}
	return in.callResolved(fr, at, fn, rt, args)
}

// callResolved is call with its type arguments already read. A std
// function with no Ergo body runs as the evaluator's native, if it has
// one (natives.go); any other Go function is skipped. The callee sees
// the caller's capabilities.
func (in *interp) callResolved(fr *frame, at *syntax.CallExpr, fn *check.Func, targs []rtype, args []Value) (Value, *abrupt) {
	switch {
	case fn.Decl == nil || fn.Decl.Body == nil || fn.Decl.Extern != nil || fn.Pkg.Host():
		if nat := nativeOf(fn); nat != nil && !fn.Pkg.Host() {
			var site syntax.Node = fr.decl
			if at != nil {
				site = at
			}
			return nat(in, fr, site, fn, args)
		}
		if nat := goNativeOf(fn); nat != nil {
			var site syntax.Node = fr.decl
			if at != nil {
				site = at
			}
			return nat(in, fr, site, fn, args)
		}
		return nil, fr.unsupported(at, "a Go function (%s), even a pure one", fn.Name)
	case fn.Pkg.Info == nil:
		ice("the package of %s has no Info", fn.Name)
	}
	callee := in.newFrame(fn.Pkg, fn.Decl)
	callee.caps = fr.caps
	if len(fn.Sig.TParams) > 0 {
		if len(targs) != len(fn.Sig.TParams) {
			ice("the generic call of %s has %d type arguments for %d parameters", fn.Name, len(targs), len(fn.Sig.TParams))
		}
		n := len(targs)
		callee.env = &tenv{tps: fn.Sig.TParams[:n:n], args: targs[:n:n]} // full slices: a bind appends to neither
	}
	params := in.paramsOf(fn)
	if len(params) != len(args) {
		ice("%s takes %d parameters and was called with %d", fn.Name, len(params), len(args))
	}
	for i, p := range params {
		callee.bind(p, args[i])
	}
	for _, p := range params {
		in.ownVar(callee, p, fn.Decl) // a taken parameter (R8)
	}
	v, ab := in.runFrame(fr, at, callee, fn.Name, fn.Decl.Body.List)
	in.freeFrame(callee)
	return v, ab
}

// runFrame runs the body of a called frame one call deeper, and turns its
// completion into the call's: a return's value, or the callee's failure.
func (in *interp) runFrame(fr *frame, at *syntax.CallExpr, callee *frame, name string, body []syntax.Stmt) (Value, *abrupt) {
	if in.depth >= in.budget.Depth {
		return nil, fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the call depth reached its bound of %d", in.budget.Depth))
	}
	in.depth++
	defer func() { in.depth-- }()
	var val Value
	ab := in.entryPoll(callee)
	if ab == nil {
		val, ab = in.bodyValue(callee, body)
	}
	// The frame ends: it releases what it still owns, on every exit.
	ab = in.endFrame(callee, ab)
	if ab == nil {
		if val == nil {
			val = unit{}
		}
		return val, nil
	}
	switch ab.kind {
	case ctlReturn:
		if ab.val == nil {
			return unit{}, nil
		}
		return ab.val, nil
	case ctlFail, ctlEscape:
		// The callee's failure is the call's: the caller's try, catch,
		// tail or return decides.
		out := *ab
		out.kind = ctlFail
		return nil, &out
	case ctlBreak, ctlContinue:
		ice("a %d left the function %s", ab.kind, name)
	}
	return nil, ab
}

// bodyValue runs a function or law body, whose final statement yields the
// result (lower/tails.go, destReturn).
func (in *interp) bodyValue(fr *frame, list []syntax.Stmt) (Value, *abrupt) {
	return in.yield(fr, list, true)
}

// yield runs a statement list whose last statement yields its value: into
// a return when ret is set (a function body: its final expression
// statement is the result), else into a value-position if, match or catch
// arm, where only an Info.Tails statement yields. A list that yields
// nothing gives nil.
func (in *interp) yield(fr *frame, list []syntax.Stmt, ret bool) (Value, *abrupt) {
	if len(list) == 0 {
		return nil, nil
	}
	if ab := in.stmts(fr, list[:len(list)-1]); ab != nil {
		return nil, ab
	}
	last := list[len(list)-1]
	v, ab := in.yieldStmt(fr, last, ret)
	if ab == nil {
		ab = in.releaseAt(fr, last) // the statement's temporaries (R43)
	}
	return v, ab
}

// scope runs a scope's list (a block, an arm, a clause) and, when it ends
// normally, the releases of the scope's end (Info.Releases[key]).
func (in *interp) scope(fr *frame, key syntax.Node, list []syntax.Stmt, ret bool) (Value, *abrupt) {
	v, ab := in.yield(fr, list, ret)
	if ab == nil {
		ab = in.releaseAt(fr, key)
	}
	return v, ab
}

func (in *interp) yieldStmt(fr *frame, s syntax.Stmt, ret bool) (Value, *abrupt) {
	if ab := in.charge(fr, s, 1); ab != nil {
		return nil, ab
	}
	switch s := s.(type) {
	case *syntax.ExprStmt:
		if ret {
			return in.retValue(fr, s.X)
		}
		if _, tail := fr.info.Tails[s]; tail {
			return in.resultExpr(fr, s.X)
		}
		_, ab := in.expr(fr, s.X)
		return nil, ab
	case *syntax.IfStmt:
		c, ab := in.expr(fr, s.Cond)
		if ab == nil {
			ab = in.releaseAt(fr, s.Cond) // the header's temporaries
		}
		if ab != nil {
			return nil, ab
		}
		if truth(c) {
			return in.scope(fr, s.Body, s.Body.List, ret)
		}
		switch e := s.Else.(type) {
		case nil:
			return nil, nil
		case *syntax.BlockStmt:
			return in.scope(fr, e, e.List, ret)
		case *syntax.IfStmt:
			return in.yieldStmt(fr, e, ret)
		}
		ice("an else of %T", s.Else)
	case *syntax.BlockStmt:
		return in.scope(fr, s, s.List, ret)
	case *syntax.SwitchStmt:
		return in.valueMatch(fr, s, ret)
	case *syntax.TypeSwitchStmt:
		return in.typeMatch(fr, s, ret)
	}
	return nil, in.stmtOnly(fr, s)
}

// retValue is the value a return or a function body's tail gives back. A
// value of an error type that the declared result does not make the
// success value is a failure (types.md, Error types 2; Info.ReturnedValues).
// A fallible value's failure leaves the frame: no catch inside the frame
// sees it.
func (in *interp) retValue(fr *frame, x syntax.Expr) (Value, *abrupt) {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		// A returned owned local stays the frame's until its exit is
		// known (own.go, keep).
		fr.returned = id
	}
	v, ab := in.resultExpr(fr, x)
	fr.returned = nil
	if ab != nil {
		if ab.kind == ctlFail {
			out := *ab
			out.kind = ctlEscape
			return nil, &out
		}
		return nil, ab
	}
	if fr.isErrorValue(fr.rtypeOf(x).t) && !fr.info.ReturnedValues[x] {
		if _, isRecord := v.(*record); isRecord {
			return nil, fr.abrupt(ctlEscape, x, v, "")
		}
	}
	return v, nil
}

// resultExpr retains the chosen tail of a result-position control
// expression, but not that of a control expression used as an operand of
// (for example) a comparison. The comparison itself is then the site.
func (in *interp) resultExpr(fr *frame, x syntax.Expr) (Value, *abrupt) {
	origin := unparen(x)
	for {
		t, ok := origin.(*syntax.TryExpr)
		if !ok {
			break
		}
		origin = unparen(t.X)
	}
	fr.resultPos = origin.Pos()
	v, ab := in.tailExpr(fr, x)
	switch origin.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.CatchExpr:
	default:
		fr.resultPos = origin.Pos()
	}
	return v, ab
}

// isErrorValue reports a value of an error type in this frame's view: the
// checker's IsErrorType of its package for the body's owner, or of the
// type's own package (lower/owners.go reads the same facts).
func (fr *frame) isErrorValue(t check.Type) bool {
	if u, ok := t.(*check.Union); ok && u.Row != nil {
		return true
	}
	n, ok := t.(*check.Named)
	if !ok {
		return false
	}
	if n.Orig != nil {
		n = n.Orig
	}
	if fr.pkg.IsErrorType(n, fr.owner) {
		return true
	}
	p := n.Obj.Pkg
	return p != nil && p != fr.pkg && p.IsErrorType(n, check.OwnerProduction)
}
