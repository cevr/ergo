package eval

import (
	"fmt"
	"math"
	"unsafe"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// interp is one evaluation request: its budget and the work it used.
type interp struct {
	budget Budget
	// atInt are the type parameters of the law a licence run runs, each
	// read at int wherever it occurs (in a Maybe[T], a Tree[T], a field):
	// types.md Laws 14, rule 5 (LICENCE-ADT).
	atInt map[*check.TypeParam]bool
	root  *check.Package
	// fold is set in a build-time fold (FoldValue, FoldTemplate): std
	// build's InFold, which a law evaluated at check time sees false, as
	// a built program does.
	fold  bool
	steps int64
	bytes int64 // value bytes allocated
	// stepsAt and bytesAt are steps and bytes when the obligation in
	// progress began (the package's initializers are their own): each
	// obligation has the whole budget, so one that runs no longer leaves
	// less to the obligations after it.
	stepsAt, bytesAt int64
	// stepsEnd caches stepsAt + stepLimit() for charge; it is never past
	// the true end (steps only grow), so a stale one only takes chargeSlow.
	stepsEnd int64
	// perCase is the case count the obligation in progress multiplies the
	// step budget by (casesBudget); 0 is one, as for an initializer, a
	// test without parameters or a machine search.
	perCase int64
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
	plans  map[*check.Func]*funcPlan // plan.go
	// walk turns compiled forms off for this request (compile.go).
	walk  bool
	files map[syntax.Decl]*syntax.File
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
	// entries are the machines' computed entries, and a generic machine's
	// initial values, computed once per instance (machineentry.go).
	entries map[entryKey]Value
	// sym is the state of a symbolic run (symbolic.go), nil otherwise.
	sym *symRun
	// mapOrder is the order this run visits a Map's keys in, and
	// mapRanged says a for over a Map ran (maporder.go).
	mapOrder  mapOrder
	mapRanged bool
	// ambient is the process state a licence run's side evaluates under
	// (licence.go): a native that would read the working directory, the
	// environment or the time zone reads it here.
	ambient ambient
	// goBodies runs a module's own Go bodies in a licence run (LAWFFI
	// LF2m; gobodies.go), nil elsewhere.
	goBodies GoBodies
	// caseEv records structured bounded-test evidence submitted during the
	// active evaluated test/law case (std testing.RecordCaseEvidence).
	caseEv evalCaseEvidence
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
	// tries counts the trys whose operand is being evaluated, a covered
	// root's included (Info.TryCovers); caught is the caught expression
	// of the innermost catch being evaluated. Under a try, a failure of
	// any expression but caught leaves the frame (rows.md, Catch 5), as
	// the lowering's fc.tries propagates it.
	tries  int
	caught syntax.Expr
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
	// ret is the completion a compiled return gives (compile.go): one per
	// frame, as a return ends the frame.
	ret abrupt
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

// framePlanned is newFrame for a call of p's function, with the owner
// and the table its plan read once.
func (in *interp) framePlanned(p *funcPlan) *frame {
	var fr *frame
	pkg, decl := p.fn.Pkg, p.fn.Decl
	if n := len(in.framePool); n > 0 {
		fr = in.framePool[n-1]
		in.framePool = in.framePool[:n-1]
		fr.pkg, fr.info, fr.decl, fr.key, fr.owner, fr.tab = pkg, pkg.Info, decl, decl, p.owner, p.tab
	} else {
		fr = &frame{pkg: pkg, info: pkg.Info, decl: decl, key: decl, owner: p.owner, tab: p.tab}
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

// forms reports whether the frame runs compiled forms (compile.go): it has
// no substitution, or it reads the facts of one instance (ORACLE O38), so
// what a form reads of the types is the same at every evaluation.
func (fr *frame) forms() bool {
	return fr.env == nil || fr.tab != nil && fr.tab.instance
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
	kind   ctlKind
	val    Value
	msg    string
	defect *defectV
	pkg    *check.Package
	decl   syntax.Decl
	at     syntax.Pos
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
	// One unsigned compare against the cached end: a negative n is past
	// every budget. A stale end (a new obligation, a fresh interp) takes
	// chargeSlow, which reads the budget itself.
	if uint64(n) <= uint64(in.stepsEnd-in.steps) {
		in.steps += n
		return nil
	}
	return in.chargeSlow(fr, at, n)
}

// chargeSlow is charge out of line, so that charge inlines: it renews the
// cached end of the budget, then charges or stops the obligation.
func (in *interp) chargeSlow(fr *frame, at syntax.Node, n int64) *abrupt {
	in.stepsEnd = in.stepsAt + in.stepLimit()
	if n >= 0 && n <= in.stepsEnd-in.steps {
		in.steps += n
		return nil
	}
	in.steps = in.stepsEnd
	if in.perCase > 1 {
		return fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the evaluation used its %d steps (%d for each of its %d cases)", in.stepLimit(), in.budget.Steps, in.perCase))
	}
	return fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the evaluation used its %d steps", in.budget.Steps))
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
func (in *interp) stepsLeft() int64 { return in.stepLimit() - (in.steps - in.stepsAt) }

// stepLimit is the step budget of the obligation in progress: the budget
// for each of its cases, so a property's or law's limit grows with the
// cases its plan runs (TP-BUILT-FAST; docs/testing.md, Budgets).
func (in *interp) stepLimit() int64 {
	if in.perCase <= 1 {
		return in.budget.Steps
	}
	if in.budget.Steps > math.MaxInt64/4/in.perCase {
		return math.MaxInt64 / 4 // stepsAt + the limit stays an int64
	}
	return in.budget.Steps * in.perCase
}

// casesBudget gives the obligation in progress the step budget of count
// cases.
func (in *interp) casesBudget(count int) {
	in.perCase = int64(count)
	in.stepsEnd = in.stepsAt + in.stepLimit()
}

// begin gives the obligation that starts now the whole budget, of one
// case until its plan says how many it runs (casesBudget).
func (in *interp) begin() {
	in.stepsAt, in.bytesAt, in.perCase = in.steps, in.bytes, 0
	in.stepsEnd = in.stepsAt + in.budget.Steps
}

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

// byteWork is the steps of a copy or a comparison of n bytes: Go's build
// moves and compares them a word at a time (memmove, memequal), so a step
// is a word, not a byte (FU-eval-step-per-op). The node that copies or
// compares pays its own step.
func byteWork(n int64) int64 { return (n + 7) / 8 }

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
	// Each type argument is read in the caller's substitution, and so is
	// each operation witness (witness.go).
	var rt []rtype
	for _, t := range targs {
		rt = append(rt, fr.rt(t))
	}
	if at != nil {
		if wits := witnessesOf(fr.info.Instances[at], fr.env); wits != nil {
			return in.callWitnessed(fr, at, fn, rt, wits, args)
		}
	}
	return in.callResolved(fr, at, fn, rt, args)
}

// callResolved is call with its type arguments already read. A std
// function with no Ergo body runs as the evaluator's native, if it has
// one (natives.go); any other Go function is skipped. The callee sees
// the caller's capabilities.
func (in *interp) callResolved(fr *frame, at *syntax.CallExpr, fn *check.Func, targs []rtype, args []Value) (Value, *abrupt) {
	return in.callWitnessed(fr, at, fn, targs, nil, args)
}

// callWitnessed is callResolved with the operation witnesses of the
// callee's constrained type parameters (witness.go).
func (in *interp) callWitnessed(fr *frame, at *syntax.CallExpr, fn *check.Func, targs []rtype, wits map[*check.TypeParam]*witnessV, args []Value) (Value, *abrupt) {
	return in.callPlanned(fr, at, in.planOf(fn), targs, wits, args)
}

// callPlanned is callResolved with the callee's plan (plan.go).
func (in *interp) callPlanned(fr *frame, at *syntax.CallExpr, p *funcPlan, targs []rtype, wits map[*check.TypeParam]*witnessV, args []Value) (Value, *abrupt) {
	fn := p.fn
	if p.appendsString {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		xs, isSlice := args[0].(sliceV)
		s, isText := args[1].(string)
		if in.sym != nil || !isSlice || !isText {
			return appendString(fn, args[0], args[1]), nil
		}
		// A run appends each byte as Go's append does, growing the
		// store in place (MX-EVBUILDER: a copy per call made civil's
		// formats quadratic, at one step a call).
		var site syntax.Node = fr.decl
		if at != nil {
			site = at
		}
		var v Value = xs
		for i := 0; i < len(s); i++ {
			var ab *abrupt
			if v, ab = in.appendTo(fr, site, v.(sliceV), s[i], nil); ab != nil {
				return nil, ab
			}
		}
		return v, nil
	}
	if !p.ergo {
		if check.GoBuiltin(fn) == "len" && len(args) == 1 {
			v := args[0]
			if pv, ok := v.(*ptrV); ok {
				var ab *abrupt
				v, ab = in.read(fr, pv.root.v, pv.path)
				if ab != nil {
					return nil, ab
				}
			}
			if isTerm(v) && in.sym != nil {
				return nil, in.symStop(fr, at, v)
			}
			return evalLenValue(v), nil
		}
		if check.GoBuiltin(fn) == "delete" && len(args) == 2 {
			var site syntax.Node = fr.decl
			if at != nil {
				site = at
			}
			return in.deleteIn(fr, site, args[0].(mapV), args[1], nil)
		}
		if spreads := check.AppendSpreads(fn); check.GoBuiltin(fn) == "append" && (in.sym == nil || in.sym.tmpl && isSliceV(args[0]) && (!spreads || isSliceV(args[1]))) {
			// An extern of Go's append (check/builtin.go) at its first
			// shape, [T](xs []T, x T) []T: the prelude's xs.Append(x),
			// which the lowering also writes as Go's append (MX-EVAPPEND);
			// or at its spread shape, [T](xs []T, ys []T) []T, Go's
			// append(xs, ys...): slices.AppendAll (EVAL-IFP: it ran as
			// the first shape, ys one element).
			xs, ok := args[0].(sliceV)
			if !ok {
				ice("append's first argument is %T", args[0])
			}
			var site syntax.Node = fr.decl
			if at != nil {
				site = at
			}
			if spreads {
				return in.appendAll(fr, site, xs, args[1].(sliceV))
			}
			return in.appendTo(fr, site, xs, args[1], nil)
		}
		if in.sym != nil && in.sym.tmpl {
			if ab := in.symExtern(fr, at, fn, args); ab != nil { // foldvalue.go
				return nil, ab
			}
		} else if in.sym != nil {
			v, why := externTerm(fn, args)
			if why != "" {
				return nil, fr.abrupt(ctlUnsupported, at, nil, why)
			}
			return v, nil
		}
		if p.nat != nil {
			var site syntax.Node = fr.decl
			if at != nil {
				site = at
			}
			return p.nat(in, fr, site, fn, args)
		}
		return nil, fr.unsupported(at, "a Go function (%s), even a pure one", fn.Name)
	}
	if in.sym != nil {
		if !rowless(fn) {
			return nil, fr.abrupt(ctlUnsupported, at, nil, fn.Name+" has a needs or fails row")
		}
		in.sym.mark(fn.Pkg.Info, fn.Decl.Body)
	}
	callee := in.framePlanned(p)
	callee.caps = fr.caps
	if len(fn.Sig.TParams) > 0 {
		if len(targs) != len(fn.Sig.TParams) {
			ice("the generic call of %s has %d type arguments for %d parameters", fn.Name, len(targs), len(fn.Sig.TParams))
		}
		n := len(targs)
		callee.env = &tenv{tps: fn.Sig.TParams[:n:n], args: targs[:n:n], wits: wits} // full slices: a bind appends to neither
	}
	// A generic callee reads the facts of its instance (ORACLE O38): its
	// forms are compiled for its type arguments.
	bodyForm, bodyFast := &p.bodyForm, &p.body
	if callee.env != nil {
		bodyForm, bodyFast = nil, nil
		if !in.walk {
			if inst := in.instanceOf(p, targs); inst != nil {
				callee.tab = inst.tab
				bodyForm, bodyFast = &inst.bodyForm, &inst.body
			}
		}
	}
	params := p.params
	if len(params) != len(args) {
		ice("%s takes %d parameters and was called with %d", fn.Name, len(params), len(args))
	}
	for i, v := range params {
		callee.bind(v, args[i])
	}
	for _, i := range p.owned {
		in.ownVar(callee, params[i], fn.Decl) // a taken parameter (R8)
	}
	var body fastBody
	if bodyForm != nil && !in.walk {
		switch *bodyForm {
		case formUnseen:
			*bodyForm = formSeen
		case formSeen:
			*bodyForm = formDone
			*bodyFast = in.compileBody(callee, fn.Decl.Body.List)
		}
		body = *bodyFast
	}
	var v Value
	var ab *abrupt
	if p.closeParts != nil {
		v, ab = in.runClosingFrame(fr, at, callee, p, body, args[0])
	} else {
		v, ab = in.runFrameBody(fr, at, callee, fn.Name, fn.Decl.Body.List, body)
	}
	in.freeFrame(callee)
	return v, ab
}

// runFrame runs the body of a called frame one call deeper, and turns its
// completion into the call's: a return's value, or the callee's failure.
func (in *interp) runFrame(fr *frame, at *syntax.CallExpr, callee *frame, name string, body []syntax.Stmt) (Value, *abrupt) {
	return in.runFrameBody(fr, at, callee, name, body, nil)
}

// runFrameBody is runFrame with the body's compiled form, when it has one.
func (in *interp) runFrameBody(fr *frame, at *syntax.CallExpr, callee *frame, name string, body []syntax.Stmt, fast fastBody) (Value, *abrupt) {
	if in.depth >= in.budget.Depth {
		return nil, fr.abrupt(ctlExhausted, at, nil, fmt.Sprintf("the call depth reached its bound of %d", in.budget.Depth))
	}
	in.depth++
	defer func() { in.depth-- }()
	var val Value
	ab := in.entryPoll(callee)
	if ab == nil {
		if fast != nil {
			val, ab = fast(in, callee)
		} else {
			val, ab = in.bodyValue(callee, body)
		}
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
	if in.returnsError(fr, x) {
		if _, isRecord := v.(*record); isRecord {
			return nil, fr.abrupt(ctlEscape, x, v, "")
		}
	}
	return v, nil
}

// returnsError caches the checker's return decision for the frame's
// source expression.
func (in *interp) returnsError(fr *frame, x syntax.Expr) bool {
	if !fr.forms() || fr.tab == nil {
		return fr.returnIsFailure(x)
	}
	f := in.fx(fr, x)
	if !f.errSeen {
		f.errIs = fr.returnIsFailure(x)
		f.errSeen = true
	}
	return f.errIs
}

// returnIsFailure reads the checker's decision, not the instantiated type
// of x. A generic function returning T succeeds even if T is later
// instantiated with an error type. An absent entry is not a failure.
func (fr *frame) returnIsFailure(x syntax.Expr) bool {
	value, recorded := fr.info.ReturnedValues[x]
	return recorded && !value
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
