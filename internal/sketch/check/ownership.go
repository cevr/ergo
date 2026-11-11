package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Ownership flow (#197 L8 A2; resources R4, R5, R10, R15-R17, R19-R21,
// R41-R43). A value whose type holds a resource (resource.go) has one
// owner. One forward pass per frame over the structured control flow
// decides, for each owned local, taken parameter and temporary, whether
// it is owned, handed off (moved) or handed off on some paths only
// (maybe), and reports:
//
//  1. a use after a hand-off, or of a value handed off on some path (R15,
//     R19, R20, R21): the use names the hand-off;
//  2. a hand-off of a lent value: a parameter the function does not take
//     (R10, R16, R17), or a local of an enclosing frame inside a function
//     literal or a trailing block (scope.OnExit, task.Go), which may run
//     any number of times;
//  3. a lend and a hand-off of one value in one call (R17);
//  4. a discarded resource: a call or literal whose resource value no one
//     owns, as a statement or bound to _ (R42, D4).
//
// A hand-off is: `let q = r`, an assignment `x = r`, `return r` or the
// block's value, a part of a tuple or composite literal that is handed
// off (R14), an argument for a parameter the callee's takes clause names
// (written, or inferred: takes.go), the value an operation with a bare
// takes is called on, and the receiver of a checked Close (R1, R19).
// Every other use lends (R16). A value of a type parameter is tracked
// too; a refusal of one is a copy site (takes.go), not a diagnostic.
//
// The pass records the release plan in Info.Releases: at each scope's end,
// each statement's end (its temporaries, R43), each exit edge (return,
// break, continue, a try's failure that leaves the function) and each
// assignment (the old value, R41), what is still owned there, last
// acquired first, with a drop flag for a value owned on some paths only.
//
// A loop body runs to its fixpoint: the lattice per value is owned,
// moved < maybe, so a body runs at most twice per change of its entry
// state, and each loop memoizes its last fixpoint (entry, exit state,
// diagnostics), so an enclosing loop's later pass reuses it and nesting
// does not multiply the work. Diagnostics of a pass that is not the last
// are dropped; a later pass overwrites its releases. ownSteps counts
// visited nodes and copied state slots.
//
// Prior art: Rust's move checker and drop elaboration (drop flags,
// assignment drops the old value, a temporary lives to the statement's
// end); Lean 4's owned and borrowed parameters; Swift's consuming and
// borrowing parameters. Gleam, Elixir and ReScript have no static
// ownership: the BEAM closes a port when its process ends.

type ownSt uint8

const (
	ownNone  ownSt = iota // not live here
	ownOwned              // owned on every path
	ownMoved              // handed off on every path
	ownMaybe              // handed off on some paths
)

// ownSlot is one value's state at a program point.
type ownSlot struct {
	st        ownSt
	at        syntax.Node // the hand-off (moved, maybe)
	how       string
	callLoans []ownLoan // result origins still lent to calls being evaluated
	// The fields of an owned record taken in place (`b.r.Close()`, R39,
	// Gap 38), by field index: on every path (fields), on some (maybe);
	// fieldAt is the first such take, for the diagnostics.
	fields, maybe uint64
	fieldAt       syntax.Node
	loans         []resourceLoan
}

// ownState is the flow state at one program point. Whether a path reaches
// the point is its own field, never an empty or nil slot list: a point
// that paths reach with no live value yet (a loop, match or catch arm at
// the start of a function) is checked like any other (batch-15 A2 M1).
// The zero value is unreachable.
type ownState struct {
	reached bool
	slots   []ownSlot // when reached: one per tracked value in scope
}

type ownLoanCall struct {
	name   string
	active bool
}

type ownLoan struct {
	call *ownLoanCall
	id   *syntax.Ident
}

// liveLoans unions immutable path facts. A completed or aborted call no
// longer lends its operands, including in saved failure-edge states.
func liveLoans(lists ...[]ownLoan) []ownLoan {
	var out []ownLoan
	for _, list := range lists {
		for _, l := range list {
			if !l.call.active {
				continue
			}
			found := false
			for _, old := range out {
				found = found || old == l
			}
			if !found {
				out = append(out, l)
			}
		}
	}
	return out
}

// live reports an owned or maybe-moved slot: one whose fields count.
func (s ownSlot) live() bool { return s.st == ownOwned || s.st == ownMaybe }

// ownLocal is what a slot holds: a local, a parameter or a temporary.
type ownLocal struct {
	v     *Var
	temp  syntax.Expr
	lent  bool // a parameter the function does not take
	depth int  // the frame that owns it
	param int  // the declaration's parameter index plus 1; 0 for none
	// from names what a lent binding is lent from: a for-in's container
	// (R35) or a match subject that is lent (Gap 36).
	from     string
	root     *Var
	lendEnd  *lendPoint // a binding lent from a subject temporary
	loanOnly bool
	// t is the type of a temporary that is no expression of the tree: a
	// field an update replaces (Info.Replaced).
	t Type
}

// typ is the type of the value l holds.
func (w *ownWalk) typ(l ownLocal) Type {
	if l.v != nil {
		return l.v.Type
	}
	if l.t != nil {
		return l.t
	}
	return w.info.Types[l.temp]
}

// genericOnly reports a type that holds a resource only through a type
// parameter: a refusal on such a value is a copy site (takes.go).
func (w *ownWalk) genericOnly(t Type) bool {
	return t != nil && !w.c.holdsResource(t)
}

// refuse reports a refusal on slot i, or records it as a copy site when
// the slot's value is a resource only through a type parameter.
func (w *ownWalk) refuse(i int, at syntax.Node, what string, format string, args ...any) {
	if !w.st.reached {
		return
	}
	if t := w.typ(w.vars[i]); w.genericOnly(t) {
		w.copySite(t, at, what, false)
		return
	}
	w.report(at, format, args...)
}

type ownScope struct {
	key   syntax.Node
	first int
}

type ownLoop struct {
	first        int // slots below belong to the loop's enclosing scopes
	breaks, cont []ownState
}

type ownCatch struct {
	first int
	fails []ownState
}

// ownFrameSave is the walk state a nested frame replaces.
type ownFrameSave struct {
	loops     []*ownLoop
	catches   []*ownCatch
	first     int
	name      string
	nested    string
	tail      ownTail
	onFailure func()
}

type ownDiag struct {
	at   syntax.Node
	code string
	msg  string
}

type ownWalk struct {
	c     *checker
	info  *Info
	st    ownState
	vars  []ownLocal
	slot  map[*Var]int
	depth int
	// first is the frame's first slot; name its function's name; nested
	// the text naming a nested frame ("" for a declaration).
	first        int
	name, nested string
	scopes       []ownScope
	loops        []*ownLoop
	catches      []*ownCatch
	loopMemo     map[syntax.Node]*ownLoopMemo
	diags        []ownDiag
	// tail is where the value of the block being walked goes (ownTail).
	tail ownTail
	// onFailure propagates tried failures out of this function, after a
	// call's operands and before its result exists. A catch handles only
	// result failures (hand.failed), never an operand's tried failure.
	onFailure func()
	// tryPaths is each try's failure paths, which the right operands it
	// covers extend (cover.go); covering marks the operand being walked.
	tryPaths map[*syntax.TryExpr][][]Release
	covering map[syntax.Expr]bool
	// Takes inference (takes.go): infer is the silent pass that promotes
	// fn's lent parameters (promote) and records its copy sites (copies);
	// fn is the declaration walked.
	infer   bool
	fn      *Func
	top     *syntax.BlockStmt // the declaration's body
	promote map[int]bool
	copies  map[*TypeParam]CopySite
	// borrow maps a let-bound borrow `let p = &h` of an owned local to
	// that local: a Close called through p takes h (R19).
	borrow    map[*Var]*Var
	lending   *lendWalk
	lendFrame syntax.Node
	// ranged is the range of the for-in being walked, without its
	// parentheses: the one place a queue's Values() of resources may be
	// (valuesSeq).
	ranged syntax.Expr
}

func (c *checker) newOwnWalk() *ownWalk {
	return &ownWalk{c: c, info: c.pkg.Info, slot: map[*Var]int{}, loopMemo: map[syntax.Node]*ownLoopMemo{}}
}

// checkOwnership runs the flow pass over every body of the package, after
// the takes clauses and copy sites are inferred.
func (c *checker) checkOwnership() {
	saved := c.cur
	defer func() { c.cur = saved }()
	c.inferTakes()
	for _, fi := range c.files {
		c.cur = fi
		for _, d := range fi.syntax.Decls {
			w := c.newOwnWalk()
			switch d := d.(type) {
			case *syntax.FuncDecl:
				if d.Body == nil || d.Extern != nil {
					continue
				}
				f, _ := c.pkg.Info.Defs[d.Name].(*Func)
				if f == nil || f.Sig == nil {
					continue
				}
				w.fn = f
				w.frame(d.Body, takesOf(f), w.paramVars(d.Recv, d.Type.Params), d.Name.Name, "", f.Name == "Close" && c.pkg.Info.Closes[d] != nil)
			case *syntax.TestDecl:
				w.frame(d.Body, nil, w.paramVars(nil, d.Params), d.Name.Name, "", false)
			case *syntax.LawDecl:
				if d.Body != nil {
					w.frame(d.Body, nil, w.paramVars(nil, d.Params), d.Name.Name, "", false)
				}
			default:
				continue
			}
			seen := map[ownDiag]bool{}
			for _, g := range w.diags {
				if !seen[g] {
					seen[g] = true
					c.errorf(g.at, g.code, "%s", g.msg)
				}
			}
		}
	}
}

// paramVars is the declared parameters, the receiver list first, in the
// signature's order; nil for a parameter written with no name.
func (w *ownWalk) paramVars(lists ...*syntax.FieldList) []*Var {
	var out []*Var
	for _, l := range lists {
		if l == nil {
			continue
		}
		for _, f := range l.List {
			if len(f.Names) == 0 {
				out = append(out, nil)
			}
			for _, id := range f.Names {
				v, _ := w.info.Defs[id].(*Var)
				out = append(out, v)
			}
		}
	}
	return out
}

func (w *ownWalk) step(n int) { w.c.ownSteps += n }

func (w *ownWalk) report(at syntax.Node, format string, args ...any) {
	w.reportCode(at, CodeType, format, args...)
}

func (w *ownWalk) reportCode(at syntax.Node, code, format string, args ...any) {
	if !w.st.reached || w.infer {
		return
	}
	w.diags = append(w.diags, ownDiag{at: at, code: code, msg: fmt.Sprintf(format, args...)})
}

func (w *ownWalk) pos(n syntax.Node) string {
	line, col := w.c.cur.syntax.Position(n.Pos())
	return fmt.Sprintf("%d:%d", line, col)
}

// tracked reports whether a value of type t has an owner here: a value of
// a type parameter counts, so a generic body's copies are found (A3).
func (w *ownWalk) tracked(t Type) bool {
	return t != nil && !isInvalid(t) && w.c.mayHoldResource(t)
}

// copyState copies s; a reached state stays reached, however few slots
// it has.
func (w *ownWalk) copyState(s ownState) ownState {
	if !s.reached {
		return ownState{}
	}
	w.step(len(s.slots))
	return ownState{reached: true, slots: append([]ownSlot(nil), s.slots...)}
}

// join is the state where two paths meet, cut to n slots.
func (w *ownWalk) join(as, bs ownState, n int) ownState {
	if !as.reached {
		if !bs.reached {
			return ownState{}
		}
		return padState(w.copyState(bs), n)
	}
	as = padState(as, n)
	if !bs.reached {
		return as
	}
	a, b := as.slots, padState(bs, n).slots
	w.step(n)
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		switch {
		case x.st == y.st:
		case x.st == ownNone:
			a[i] = y
		case y.st == ownNone:
		case x.st == ownOwned:
			a[i] = ownSlot{st: ownMaybe, at: y.at, how: y.how}
		case y.st == ownOwned || y.st == ownMaybe:
			a[i] = ownSlot{st: ownMaybe, at: x.at, how: x.how}
		}
		a[i].callLoans = liveLoans(x.callLoans, y.callLoans)
		// The fields taken in place: on every path where the record is
		// still held, or on some (a drop flag each, B2).
		switch {
		case x.live() && y.live():
			all := x.fields & y.fields
			a[i].fields, a[i].maybe = all, (x.fields|y.fields|x.maybe|y.maybe)&^all
		case x.live():
			a[i].fields, a[i].maybe = x.fields, x.maybe
		case y.live():
			a[i].fields, a[i].maybe = y.fields, y.maybe
		}
		a[i].fieldAt = x.fieldAt
		a[i].loans = mergeLoans(x.loans, y.loans)
		if a[i].fieldAt == nil {
			a[i].fieldAt = y.fieldAt
		}
	}
	return as
}

// padState cuts s to n slots, or extends it with slots not live yet: a
// failure edge leaves before later temporaries are acquired.
func padState(s ownState, n int) ownState {
	if !s.reached {
		return s
	}
	if len(s.slots) >= n {
		s.slots = s.slots[:n]
		return s
	}
	s.slots = append(s.slots, make([]ownSlot, n-len(s.slots))...)
	return s
}

func sameState(as, bs ownState) bool {
	a, b := as.slots, bs.slots
	if as.reached != bs.reached || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].st != b[i].st || a[i].fields != b[i].fields || a[i].maybe != b[i].maybe {
			return false
		}
		x, y := liveLoans(a[i].callLoans), liveLoans(b[i].callLoans)
		if len(x) != len(y) {
			return false
		}
		for j := range x {
			if x[j] != y[j] {
				return false
			}
		}
		if len(mergeLoans(a[i].loans, b[i].loans)) != len(a[i].loans) || len(a[i].loans) != len(b[i].loans) {
			return false
		}
	}
	return true
}

// acquire adds a value the current scope owns.
func (w *ownWalk) acquire(l ownLocal) int {
	l.depth = w.depth
	w.vars = append(w.vars, l)
	if w.st.reached {
		w.st.slots = append(w.st.slots, ownSlot{st: ownOwned, loans: w.aggregateLoans(l.v)})
	}
	i := len(w.vars) - 1
	if l.v != nil {
		w.slot[l.v] = i
	}
	if !l.lent && !l.loanOnly {
		w.owned(l)
	}
	return i
}

// owned records the release the lowering registers for a value the
// current frame owns (Info.OwnedVars, Info.OwnedTemps): the call of its
// type's checked Close, a Close function or a Close operation, or the
// generated release of its parts (dropOf, B2). A value that holds a queue
// or a task of resources waits for its drain (B3): S109 until then. A
// value of a type parameter records nothing (its release is its
// instance's).
func (w *ownWalk) owned(l ownLocal) {
	t := w.typ(l)
	var at syntax.Node = l.temp
	if l.v != nil {
		if l.v.id == nil {
			return
		}
		at = l.v.id
	}
	if t == nil || isInvalid(t) || !w.c.holdsResource(t) {
		return
	}
	d, bad := w.c.dropOf(t)
	if d == nil && l.v != nil && l.v.param {
		return // a taken parameter whose type has no release: refused where it is made
	}
	if d == nil {
		w.reportCode(at, CodeType, "%s has no release: it holds %s, an operation type that keeps the values it takes and has no Close() operation, so nothing could release them (resources R1, R13); give it a Close() operation", t, bad)
		return
	}
	if l.v != nil {
		w.info.OwnedVars[l.v] = d
	} else {
		w.info.OwnedTemps[l.temp] = d
	}
}

// releases is what slots first… still hold, last acquired first.
func (w *ownWalk) releases(first int) []Release {
	if !w.st.reached {
		return nil
	}
	var out []Release
	for i := len(w.st.slots) - 1; i >= first; i-- {
		s := w.st.slots[i].st
		if s != ownOwned && s != ownMaybe {
			continue
		}
		l := w.vars[i]
		if l.lent || l.loanOnly {
			continue // its owner releases it
		}
		if t := w.typ(l); w.copies != nil && w.genericOnly(t) {
			// A generic body that drops a T would release it through its
			// instance; that release is not built (Gap 42), so the drop
			// is a copy site: an instance with a resource is refused.
			var at syntax.Node = l.temp
			name := "a value"
			if l.v != nil {
				at, name = nil, l.v.Name
				if l.v.id != nil {
					at = l.v.id
				}
			}
			if at != nil {
				w.copySite(t, at, name+" is dropped at the end of its scope, and a generic body does not release a value of its type parameter yet (#197 Gap 42)", false)
			}
		}
		out = append(out, Release{Var: l.v, Temp: l.temp, Flag: s == ownMaybe, Fields: w.st.slots[i].fields, MaybeFields: w.st.slots[i].maybe})
	}
	return out
}

func (w *ownWalk) record(key syntax.Node, rs []Release) {
	if len(rs) == 0 {
		delete(w.info.Releases, key)
		return
	}
	w.info.Releases[key] = rs
}

func (w *ownWalk) open(key syntax.Node) {
	w.scopes = append(w.scopes, ownScope{key: key, first: len(w.vars)})
}

// close ends the innermost scope: it releases what the scope still owns.
func (w *ownWalk) close() {
	s := w.scopes[len(w.scopes)-1]
	w.scopes = w.scopes[:len(w.scopes)-1]
	if w.st.reached {
		// An end no path reaches keeps what its exit edge recorded.
		w.record(s.key, w.releases(s.first))
	}
	for _, l := range w.vars[s.first:] {
		if l.v != nil {
			delete(w.slot, l.v)
		}
	}
	w.vars = w.vars[:s.first]
	if w.st.reached {
		w.st.slots = w.st.slots[:s.first]
	}
}

// frame checks one body: a declaration's (depth 1) or a nested function
// literal's or trailing block's. params are its parameters in its
// signature's order; a parameter its takes clause (takes) names is owned,
// any other is lent. A nested frame starts from the enclosing state (its
// captures are lent) and leaves it unchanged.
func (w *ownWalk) frame(body *syntax.BlockStmt, takes []int, params []*Var, name, nested string, lendAll bool) {
	w.frameCaptured(body, takes, params, name, nested, lendAll, nil)
}

func (w *ownWalk) frameCaptured(body *syntax.BlockStmt, takes []int, params []*Var, name, nested string, lendAll bool, captures *TaskCapture) {
	w.initLends(body)
	previousFrame := w.lendFrame
	w.lendFrame = body
	defer func() { w.lendFrame = previousFrame }()
	saved := ownFrameSave{loops: w.loops, catches: w.catches, first: w.first, name: w.name, nested: w.nested, tail: w.tail, onFailure: w.onFailure}
	w.tail = frameTail
	w.onFailure = nil
	outer := w.copyState(w.st)
	nvars := len(w.vars)
	w.loops, w.catches = nil, nil
	w.depth++
	if w.depth == 1 {
		w.top = body
	}
	w.first = len(w.vars)
	w.name = name
	w.nested = nested
	if !w.st.reached {
		// A body no path reaches is still checked on its own.
		w.st = ownState{reached: true, slots: make([]ownSlot, len(w.vars))}
	}
	w.open(body)
	oldSlots := map[*Var]int{}
	if captures != nil {
		for _, v := range captures.Vars {
			oldSlots[v] = w.slot[v]
			w.acquire(ownLocal{v: v, lent: captures.Repeated})
		}
		for _, v := range captures.Borrowed {
			oldSlots[v] = w.slot[v]
			w.acquire(ownLocal{v: v, lent: true})
		}
	}
	taken := map[int]bool{}
	for _, i := range takes {
		taken[i] = true
	}
	for i, v := range params {
		if v != nil && w.tracked(v.Type) {
			p := 0
			if w.depth == 1 {
				p = i + 1
			}
			w.acquire(ownLocal{v: v, lent: lendAll || !taken[i], param: p})
		}
	}
	w.stmts(body.List, true)
	w.close()
	for v, i := range oldSlots {
		w.slot[v] = i
	}
	w.vars = w.vars[:nvars]
	w.st = outer
	w.depth--
	w.loops, w.catches, w.first, w.name, w.nested, w.tail = saved.loops, saved.catches, saved.first, saved.name, saved.nested, saved.tail
	w.onFailure = saved.onFailure
}

// block checks a scope's statements; key is the scope.
func (w *ownWalk) block(key syntax.Node, list []syntax.Stmt) {
	w.open(key)
	w.stmts(list, false)
	w.close()
}

func (w *ownWalk) stmts(list []syntax.Stmt, _ bool) {
	for _, s := range list {
		w.stmt(s)
	}
}

// region evaluates a statement or header f whose temporaries key owns.
func (w *ownWalk) region(key syntax.Node, f func()) {
	w.open(key)
	f()
	w.close()
}

func (w *ownWalk) stmt(s syntax.Stmt) {
	w.step(1)
	switch s := s.(type) {
	case *syntax.DeclStmt:
		if s.Decl == nil {
			return
		}
		for _, spec := range s.Decl.Specs {
			w.letSpec(s, spec)
		}
	case *syntax.ExprStmt:
		if _, tail := w.info.Tails[s]; tail {
			t := w.tail
			w.region(s, func() {
				w.expr(s.X, t.mode, hand{at: s.X, how: t.how, lend: t.lend, yieldRoot: t.yieldRoot, failed: t.failed})
			})
			return
		}
		w.region(s, func() { w.expr(s.X, mDiscard, hand{}) })
	case *syntax.AssignStmt:
		w.region(s, func() { w.assign(s) })
	case *syntax.ReturnStmt:
		w.open(s)
		for _, r := range s.Results {
			w.expr(r, mMove, hand{at: r, how: "it is returned"})
		}
		w.record(s, w.releases(w.first))
		w.st = ownState{}
		w.close()
	case *syntax.BranchStmt:
		if len(w.loops) == 0 {
			return
		}
		lp := w.loops[len(w.loops)-1]
		w.record(s, w.releases(lp.first))
		if w.st.reached {
			st := w.copyState(ownState{reached: true, slots: w.st.slots[:lp.first]})
			if s.Tok == syntax.BREAK {
				lp.breaks = append(lp.breaks, st)
			} else {
				lp.cont = append(lp.cont, st)
			}
		}
		w.st = ownState{}
	case *syntax.BlockStmt:
		w.block(s, s.List)
	case *syntax.IfStmt:
		w.region(s.Cond, func() { w.expr(s.Cond, mLend, hand{}) })
		n := len(w.vars)
		saved := w.copyState(w.st)
		w.block(s.Body, s.Body.List)
		then := w.st
		w.st = saved
		if s.Else != nil {
			w.stmt(s.Else)
		}
		w.st = w.join(then, w.st, n)
	case *syntax.SwitchStmt:
		if s.Tag != nil {
			w.region(s.Tag, func() { w.expr(s.Tag, mLend, hand{}) })
		}
		w.clauses(s.Clauses)
	case *syntax.TypeSwitchStmt:
		w.region(s, func() {
			var subjects []syntax.Expr
			for _, g := range s.Subjects {
				w.expr(g.X, mLend, hand{})
				subjects = append(subjects, g.X)
			}
			w.patternClauses(s, subjects)
		})
	case *syntax.ForStmt:
		w.loop(s, s.Cond, s.Body, nil)
	case *syntax.RangeStmt:
		w.region(s.X, func() {
			saved := w.ranged
			w.ranged = unparen(s.X)
			w.expr(s.X, mLend, hand{})
			w.ranged = saved
		})
		w.loop(s, nil, s.Body, w.rangeBinds(s))
	case *syntax.AssertStmt:
		w.region(s, func() { w.expr(s.X, mLend, hand{}) })
	}
}

func (w *ownWalk) clauses(cs []*syntax.CaseClause) {
	if len(cs) == 0 {
		return
	}
	n := len(w.vars)
	entry := w.copyState(w.st)
	var out ownState
	for _, cl := range cs {
		w.st = w.copyState(entry)
		w.block(cl, cl.Body)
		out = w.join(out, w.st, n)
	}
	w.st = out
}

// rangeBinds acquires a for-in's bindings that hold a resource, at each
// iteration's start. An element of a queue's Values() is the body's own
// (Q7): its binding consumes it, so a pattern there partitions it as a
// let's does (Pattern owners, R39: an omitted resource part is S108); any
// other is lent from the container (R35).
func (w *ownWalk) rangeBinds(s *syntax.RangeStmt) func() {
	lp := w.info.Loops[s]
	if lp == nil {
		return nil
	}
	if _, owned := w.valuesSeq(s.X); owned && lp.Domain == LoopSeq {
		var p *Pattern
		if s.Value != nil {
			p = w.info.Patterns[s.Value]
		}
		if p == nil {
			return func() {
				for _, v := range lp.Bindings {
					if v != nil && w.tracked(v.Type) {
						w.acquire(ownLocal{v: v})
					}
				}
			}
		}
		w.patternCover(p, lp.Elem, s.Value)
		parts := w.patternParts(p, lp.Elem, nil)
		loans := w.patternTypeLoans(lp.Elem, parts)
		return func() { w.acquireParts(parts, ownLocal{}, loans, s.Value) }
	}
	var out []ownLocal
	for _, v := range lp.Bindings {
		if v == nil || !w.tracked(v.Type) {
			continue
		}
		root, _, _ := w.placeRoot(s.X)
		out = append(out, ownLocal{v: v, lent: true, from: exprText(s.X), root: root})
	}
	return func() {
		for _, b := range out {
			w.acquire(b)
		}
	}
}

// valuesSeq reports x, a call q.Values() of a std queue (Q7), and the
// queue's element type. Each value that Seq yields was popped from the
// queue, so it is the iterating code's own: only a for-in that ranges over
// the call owns and releases it (rangeBinds). A Seq has no owner of its
// own, and a Seq[T] parameter does not say whether its values are owned
// or lent, so a queue's Values() of resources anywhere else (a let, an
// argument, a field, a returned or yielded value) is S108 (call;
// resources R35).
func (w *ownWalk) valuesSeq(x syntax.Expr) (Type, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	k := w.info.Calls[call]
	if k == nil || k.Kind != CallOp || k.Op == nil || k.Op.Name != "Values" {
		return nil, false
	}
	sel, ok := unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok || !isQueue(w.info.Types[sel.X]) {
		return nil, false
	}
	n := w.info.Types[sel.X].(*Named)
	if len(n.TArgs) != 1 {
		return nil, true
	}
	return n.TArgs[0], true
}

// isQueue reports std queue.Queue[T] or an instance of it.
func isQueue(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Pkg != nil && o.Pkg.std && o.Pkg.Path == "queue" && o.Name == "Queue"
}

// ownLoopMemo is a loop's last fixpoint: its entry state, its exit state
// and the diagnostics of its final pass. An enclosing loop's later pass
// that reaches the loop with the same entry reuses it, so each loop body
// runs a bounded number of times in all (the lattice height), however
// deep it nests.
type ownLoopMemo struct {
	entry, out ownState
	diags      []ownDiag
}

// sameExact reports equal states, hand-offs included (they name the
// diagnostics).
func sameExact(as, bs ownState) bool {
	if !sameState(as, bs) {
		return false
	}
	a, b := as.slots, bs.slots
	for i := range a {
		if a[i].at != b[i].at || a[i].how != b[i].how {
			return false
		}
	}
	return true
}

// loop runs a loop body to its fixpoint. A for-in's body runs zero or
// more times, as a for with a condition does; a bare for ends only by
// break.
func (w *ownWalk) loop(n syntax.Node, cond syntax.Expr, body *syntax.BlockStmt, binds func()) {
	first := len(w.vars)
	entry := w.st
	if m := w.loopMemo[n]; m != nil && entry.reached {
		entry = w.join(w.copyState(entry), m.entry, first)
		if sameExact(entry, m.entry) {
			// The fixpoint from this entry is known: its exit state and
			// its diagnostics; its release plan is already recorded.
			w.diags = append(w.diags, m.diags...)
			w.st = w.copyState(m.out)
			return
		}
	}
	_, ranged := n.(*syntax.RangeStmt)
	for {
		mark := len(w.diags)
		lp := &ownLoop{first: first}
		w.loops = append(w.loops, lp)
		w.st = w.copyState(entry)
		var exits ownState
		if cond != nil {
			w.region(cond, func() { w.expr(cond, mLend, hand{}) })
			exits = w.copyState(w.st)
		} else if ranged {
			exits = w.copyState(w.st)
		}
		w.open(body)
		if binds != nil {
			binds() // each iteration's bindings (R35, Q7)
		}
		w.stmts(body.List, false)
		w.close()
		back := w.st
		for _, c := range lp.cont {
			back = w.join(back, c, first)
		}
		w.loops = w.loops[:len(w.loops)-1]
		next := w.join(w.copyState(entry), back, first)
		if !entry.reached || sameState(next, entry) {
			out := exits
			for _, b := range lp.breaks {
				out = w.join(out, b, first)
			}
			if entry.reached {
				w.loopMemo[n] = &ownLoopMemo{entry: entry, out: w.copyState(out), diags: append([]ownDiag(nil), w.diags[mark:]...)}
			}
			w.st = out
			return
		}
		entry = next
		w.diags = w.diags[:mark]
	}
}

// letSpec binds a let's names; the values move into them.
func (w *ownWalk) letSpec(s syntax.Stmt, spec *syntax.VarSpec) {
	if spec.Pattern != nil {
		w.letOwners(spec)
		return
	}
	var bound []*Var
	loans := map[*Var][]resourceLoan{}
	w.region(spec, func() {
		pair := len(spec.Values) == len(spec.Names) && spec.Pattern == nil
		for i, x := range spec.Values {
			mode := mMove
			if pair && spec.Names[i].Name == "_" {
				mode = mDiscard
			}
			w.expr(x, mode, hand{at: x, how: "let moves it"})
			if pair {
				v, _ := w.info.Defs[spec.Names[i]].(*Var)
				loans[v] = w.valueLoans(x)
			}
		}
		if len(spec.Values) == 1 && len(spec.Names) > 1 {
			if tup, ok := w.info.Types[spec.Values[0]].(*Tuple); ok {
				for i, id := range spec.Names {
					if id.Name == "_" && i < len(tup.Types) && w.c.holdsResource(tup.Types[i]) {
						w.report(id, "the %s bound to _ is discarded, and a resource has one owner; bind it to a name, so its scope releases it, or hand it off (resources R42)", tup.Types[i])
					}
				}
			}
		}
	})
	_ = s
	for _, id := range spec.Names {
		if v, ok := w.info.Defs[id].(*Var); ok {
			_, callable := under(v.Type).(*Signature)
			if w.tracked(v.Type) || callable || len(loans[v]) > 0 {
				bound = append(bound, v)
			}
		}
	}
	for _, v := range bound {
		i := w.acquire(ownLocal{v: v, loanOnly: !w.tracked(v.Type)})
		if w.st.reached {
			w.st.slots[i].loans = mergeLoans(loans[v], w.aggregateLoans(v))
		}
	}
	w.letBorrow(spec)
}

// letBorrow records `let p = &h`, or `let q = p` of such a p, where h is
// an owned local and the new name is not mut: a Close through it takes h.
func (w *ownWalk) letBorrow(spec *syntax.VarSpec) {
	if len(spec.Names) != 1 || len(spec.Values) != 1 || spec.Pattern != nil {
		return
	}
	p, _ := w.info.Defs[spec.Names[0]].(*Var)
	if p == nil || p.mut {
		return
	}
	var root *Var
	switch x := unparen(spec.Values[0]).(type) {
	case *syntax.UnaryExpr:
		if id, ok := unparen(x.X).(*syntax.Ident); ok && x.Op == syntax.AND {
			if v, _ := w.info.Uses[id].(*Var); v != nil {
				if _, ok := w.slot[v]; ok {
					root = v
				}
			}
		}
	case *syntax.Ident:
		if v, _ := w.info.Uses[x].(*Var); v != nil {
			root = w.borrow[v]
		}
	}
	if root == nil {
		return
	}
	if w.borrow == nil {
		w.borrow = map[*Var]*Var{}
	}
	w.borrow[p] = root
}

// borrowed is the owned local a borrow a points at: `&h`, or a let-bound
// borrow p of it (letBorrow), with the ident the hand-off is recorded at.
func (w *ownWalk) borrowed(a syntax.Expr) (*syntax.Ident, int, bool) {
	var id *syntax.Ident
	var root *Var
	switch x := unparen(a).(type) {
	case *syntax.UnaryExpr:
		if x.Op != syntax.AND {
			return nil, 0, false
		}
		id, _ = unparen(x.X).(*syntax.Ident)
		if id != nil {
			root, _ = w.info.Uses[id].(*Var)
		}
	case *syntax.Ident:
		id = x
		if v, _ := w.info.Uses[x].(*Var); v != nil {
			root = w.borrow[v]
		}
	}
	if root == nil {
		return nil, 0, false
	}
	i, ok := w.slot[root]
	return id, i, ok
}

func (w *ownWalk) assign(s *syntax.AssignStmt) {
	if s.Tok != syntax.ASSIGN {
		for _, x := range s.Rhs {
			w.expr(x, mLend, hand{})
		}
		for _, x := range s.Lhs {
			w.expr(x, mLend, hand{})
		}
		return
	}
	replacementLoans := map[int][]resourceLoan{}
	for i, x := range s.Rhs {
		mode := mLend
		if i < len(s.Lhs) && w.tracked(w.info.Types[s.Lhs[i]]) || len(s.Lhs) > 1 {
			mode = mMove
		}
		w.expr(x, mode, hand{at: x, how: "it is assigned"})
		if i < len(s.Lhs) {
			if _, k, ok := w.placeRoot(s.Lhs[i]); ok && w.st.reached {
				if _, direct := unparen(s.Lhs[i]).(*syntax.Ident); direct {
					replacementLoans[k] = w.valueLoans(x)
				} else {
					w.st.slots[k].loans = mergeLoans(w.st.slots[k].loans, w.valueLoans(x))
				}
			}
		}
	}
	for _, x := range s.Lhs {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			if _, i, found := w.placeRoot(x); found {
				w.loanTake(i, x, -1)
			}
			w.lhsPlace(x)
			continue
		}
		v, _ := w.info.Uses[id].(*Var)
		i, ok := w.slot[v]
		if v == nil || !ok || !w.st.reached {
			continue
		}
		// R41: the old value is released, where it is still owned.
		w.loanTake(i, id, -1)
		var rs []Release
		if st := w.st.slots[i].st; st == ownOwned || st == ownMaybe {
			w.loanConflict(i, hand{at: id, how: "it is replaced"})
			rs = []Release{{Var: v, Flag: st == ownMaybe}}
		}
		w.record(id, rs)
		if w.vars[i].depth != w.depth {
			w.refuse(i, id, fmt.Sprintf("%s replaces %s, which may run more than once", w.nestedText(), v.Name), "%s belongs to the enclosing function; %s cannot replace it (resources R4)", v.Name, w.nestedText())
			continue
		}
		w.st.slots[i] = ownSlot{st: ownOwned, loans: mergeLoans(replacementLoans[i], w.aggregateLoans(v))}
	}
}

// lhsPlace evaluates an assigned place: its root is used, not moved.
func (w *ownWalk) lhsPlace(x syntax.Expr) {
	switch x := x.(type) {
	case *syntax.SelectorExpr:
		w.lhsPlace(x.X)
	case *syntax.IndexExpr:
		w.lhsPlace(x.X)
		for _, i := range x.Indices {
			w.expr(i, mLend, hand{})
		}
	case *syntax.ParenExpr:
		w.lhsPlace(x.X)
	default:
		w.expr(x, mLend, hand{})
	}
}

type ownMode uint8

const (
	mLend    ownMode = iota // the value is lent: read, or passed to a lending parameter
	mMove                   // the value moves to a new owner
	mDiscard                // the value is dropped
	// mHold: a yielded result is owned by w.tail.slot when its origin
	// cannot survive that result's consumer. An enclosing name stays lent.
	// With no slot the context build owns new provider values.
	mHold
)

// hand names a hand-off for the diagnostics: where it is and what it does.
type hand struct {
	at        syntax.Node
	how       string
	lend      func(int, *syntax.Ident) // the root owners lent by this result
	yieldRoot bool                     // this root is projected into an enclosing block result
	failed    func()                   // the result's catch, not its operands' propagation target
	// provider marks a with's provider: a package let there is reported
	// by withown.go, once per entry.
	provider bool
	// root marks the root of a field place (x in x.f): its use is checked
	// as a root (useRoot), so a field taken in place does not refuse the
	// record's other fields (R39).
	root     bool
	endLoans bool
}

func (w *ownWalk) nestedText() string {
	if w.nested != "" {
		return w.nested
	}
	return "this function literal"
}

// use checks a lend of slot i at x: of the whole value, so none of its
// fields may be taken (R39).
func (w *ownWalk) use(i int, x *syntax.Ident) {
	w.useRoot(i, x)
	if !w.st.reached {
		return
	}
	if s := w.st.slots[i]; s.live() && s.fields|s.maybe != 0 {
		w.refuse(i, x, fmt.Sprintf("%s is used whole after a field is taken at %s", x.Name, w.pos(s.fieldAt)), "a field of %s was taken at %s, so %s cannot be used whole (resources R39, Gap 38); use its other fields", x.Name, w.pos(s.fieldAt), x.Name)
	}
}

// useRoot checks a use of slot i at x as the root of a place (x.f, x[i]).
func (w *ownWalk) useRoot(i int, x *syntax.Ident) {
	if !w.st.reached {
		return
	}
	s := w.st.slots[i]
	switch s.st {
	case ownMoved:
		w.refuse(i, x, fmt.Sprintf("%s is used after it is handed off at %s", x.Name, w.pos(s.at)), "%s was handed off at %s (%s) and cannot be used after that (resources R15, R20); use %s before it is handed off", x.Name, w.pos(s.at), s.how, x.Name)
	case ownMaybe:
		w.refuse(i, x, fmt.Sprintf("%s is used after it may be handed off at %s", x.Name, w.pos(s.at)), "%s may have been handed off at %s (%s) on one path, and cannot be used after that (resources R21); hand it off on every path or on none", x.Name, w.pos(s.at), s.how)
	}
}

// move hands slot i off at h.
func (w *ownWalk) move(i int, x *syntax.Ident, h hand) {
	if !w.st.reached {
		return
	}
	w.use(i, x)
	w.handLoans(i, x, h)
	if w.vars[i].loanOnly {
		return
	}
	if w.loanTake(i, x, -1) {
		return
	}
	if s := w.st.slots[i]; s.st != ownOwned {
		// Reported by use; the first hand-off stays the one named.
		w.st.slots[i] = ownSlot{st: ownMoved, at: s.at, how: s.how}
		return
	}
	l := w.vars[i]
	switch {
	case l.depth != w.depth:
		w.refuse(i, x, fmt.Sprintf("%s hands off %s, which may run more than once", w.nestedText(), x.Name), "%s cannot hand off %s (%s): %s belongs to the enclosing function, which releases it, and the block may run at another time or more than once (resources R4, R30)", w.nestedText(), x.Name, h.how, x.Name)
		return
	case l.lent && w.infer && l.param > 0 && w.c.infersTakes(w.fn):
		// Inference (takes.go): the parameter is taken.
		w.promote[l.param-1] = true
	case l.lent && l.from != "":
		w.refuse(i, x, fmt.Sprintf("%s, lent from %s, is handed off (%s)", x.Name, l.from, h.how), "%s is lent from %s, so it cannot be handed off (%s) (resources R35, Gap 36); hand off %s whole", x.Name, l.from, h.how, l.from)
		return
	case l.lent:
		fn := w.name
		if w.nested != "" {
			fn = w.nested
		}
		// A lent T handed off is a copy of it (the caller keeps its own):
		// a mode, not a mark, so a call with a resource is refused (q56).
		w.refuse(i, x, fmt.Sprintf("%s is lent and handed off (%s); write takes %s to take it", x.Name, h.how, x.Name), "%s is lent to %s, so it cannot hand %s off (%s) (resources R10, R17); write takes %s in the signature", x.Name, fn, x.Name, h.how, x.Name)
		return
	}
	w.loanConflict(i, h)
	w.st.slots[i] = ownSlot{st: ownMoved, at: h.at, how: h.how, loans: w.st.slots[i].loans}
	w.info.Handoffs[x] = l.v
}

// qualifiedLet is the package let pkg.Name that x names, or nil.
func (w *ownWalk) qualifiedLet(x *syntax.SelectorExpr) *Var {
	id, ok := unparen(x.X).(*syntax.Ident)
	if !ok {
		return nil
	}
	pn, ok := w.info.Uses[id].(*PkgName)
	if !ok || pn.Pkg == nil || pn.Pkg.Scope == nil {
		return nil
	}
	if v, ok := w.info.Uses[x.Sel].(*Var); ok && v.pkg {
		return v
	}
	v, _ := pn.Pkg.Scope.names[x.Sel.Name].(*Var)
	if v == nil || !v.pkg {
		return nil
	}
	return v
}

// pkgLetMove refuses a hand-off of v, a package let that holds a
// resource: it has no owner (R11, R15). A Go package variable is one too:
// borrowed, never released (IG9, q62).
func (w *ownWalk) pkgLetMove(x syntax.Expr, v *Var, mode ownMode, h hand) {
	if v == nil || !v.pkg || mode != mMove || h.provider || !w.c.holdsResource(v.Type) {
		return
	}
	what := "a package let"
	if sel, ok := x.(*syntax.SelectorExpr); ok {
		if id, ok := unparen(sel.X).(*syntax.Ident); ok && isHostPkgName(w.info.Uses[id]) {
			what = "a Go package variable, borrowed from Go (q62)"
		}
	}
	w.report(x, "%s is %s, which has no owner to hand it off (%s) (resources R11, R15); lend it, or make a new value", exprText(x), what, h.how)
}

// loanConflict checks on the moving path, before the root owner ends.
// Joining a lend in one arm with a take in the other must not invent a
// path that does both. A lend that survives the join is checked if a later
// operand moves or replaces that owner.
func (w *ownWalk) loanConflict(i int, h hand) {
	for _, l := range w.st.slots[i].callLoans {
		if l.call.active {
			w.refuse(i, l.id, fmt.Sprintf("%s is lent and handed off in one call", l.id.Name), "%s is lent to %s here and handed off at %s (%s) in the same call (resources R17); hand it off in a statement of its own", l.id.Name, l.call.name, w.pos(h.at), h.how)
		}
	}
}

// expr evaluates x in mode. Its type switch names every kind of
// expression and has no default (TestMintFormNamesEveryKind reads it): an
// expression with statement children (a block, a case clause, a catch
// arm, a function body) walks them with the flow, and a new kind is an
// internal compiler error until it has a rule here (batch-15 A2 M1: the
// arms of an if or match used as a value were skipped).
func (w *ownWalk) expr(x syntax.Expr, mode ownMode, h hand) {
	if x == nil || w.c.typeNodes[x] {
		return
	}
	w.step(1)
	if t := w.info.TryCovers[x]; t != nil && !w.covering[x] {
		// A right operand a try on its left covers (cover.go): its
		// failures leave the function as the try's own do, on the try's
		// failure edge.
		if w.covering == nil {
			w.covering = map[syntax.Expr]bool{}
			if w.tryPaths == nil {
				w.tryPaths = map[*syntax.TryExpr][][]Release{}
			}
		}
		w.covering[x] = true
		saved := w.onFailure
		w.onFailure = func() { w.tryPaths[t] = append(w.tryPaths[t], w.releases(w.first)) }
		h.failed = nil
		w.expr(x, mode, h)
		w.onFailure = saved
		delete(w.covering, x)
		w.recordFailures(t, w.tryPaths[t])
		return
	}
	lend := func(xs ...syntax.Expr) {
		for _, e := range xs {
			w.expr(e, mLend, hand{})
		}
	}
	switch x := x.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		w.branchValue(x, mode, h)
		return
	case *syntax.ContextLit:
		// Its providers are built and owned by the with that discharges it
		// (Context ownership as built): each with that builds it evaluates
		// them (withProviders), so the literal evaluates nothing.
		return
	case *syntax.BasicLit, *syntax.ArrayType, *syntax.MapType, *syntax.FuncType, *syntax.StructType,
		*syntax.InterfaceType, *syntax.ErrorRow, *syntax.MachineType, *syntax.VariantSpec, *syntax.AsPattern:
		// A literal, a type or a pattern: no value of a local is used.
		return
	case *syntax.KeyValueExpr:
		lend(x.Key, x.Value)
		return
	case *syntax.SelectorExpr:
		if v := w.qualifiedLet(x); v != nil {
			// An imported package let or a Go package variable is borrowed.
			w.pkgLetMove(x, v, mode, h)
			return
		}
		if mode == mMove {
			w.checkLoanValue(x)
		}
		if w.info.Closes[x] != nil {
			// A method value of a checked Close would own its receiver,
			// and no function value owns a resource (plan 1.2 rule 4,
			// R33): it could be called twice, or never.
			w.reportCode(x, CodeType, "%s is a method value of Close, which would own %s; a function value owns no resource, so it could release %s twice or never (resources R19, R33); call %s() where %s is released", exprText(x), exprText(x.X), exprText(x.X), exprText(x), exprText(x.X))
			w.expr(x.X, mLend, hand{})
			return
		}
		if op := w.resourceMethod(x); op != nil {
			// The same for any other operation of a resource (Task.Wait,
			// Task.Stop; #197 L8 B3t): the function value would own its
			// receiver, or hold a lend of it past its release.
			w.reportCode(x, CodeType, "%s is a method value of %s on %s, a resource; a function value owns no resource and holds no lend of one (resources R19, R33); call %s() instead", exprText(x), op.Name, exprText(x.X), exprText(x))
			w.expr(x.X, mLend, hand{})
			return
		}
		if w.selector(x, mode, h) {
			return
		}
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		return
	case *syntax.IndexExpr:
		if id, ok := unparen(x.X).(*syntax.Ident); ok {
			if _, fn := w.info.Uses[id].(*Func); fn {
				w.funcValue(id, w.info.Types[x]) // f[T] as a value
				lend(x.Indices...)
				return
			}
		}
		if mode == mMove {
			w.checkLoanValue(x)
			w.placeMove(x, "an element", "it is an element of %s, lent by the index; an element cannot be moved or taken out by index (resources R35, R36); hand off %s whole, or lend the element")
		}
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		lend(x.Indices...)
		return
	case *syntax.SliceExpr:
		w.lendRoot(x.X, w.info.Types[x], mode, h)
		lend(x.Low, x.High, x.Max)
		return
	case *syntax.TypeGuard:
		lend(x.X)
		return
	case *syntax.StarExpr:
		lend(x.X)
		return
	case *syntax.UnaryExpr:
		lend(x.X)
		return
	case *syntax.BinaryExpr:
		if x.Op == syntax.LAND || x.Op == syntax.LOR {
			w.shortCircuit(x)
			return
		}
		lend(x.X, x.Y)
		return
	case *syntax.TupleExpr:
		if mode == mMove {
			// R14: a value built to be handed off moves its parts.
			w.parts(x, h)
			return
		}
		for _, e := range x.Elts {
			w.lendRoot(e, w.info.Types[x], mode, h)
		}
		return
	case *syntax.InterpolatedString:
		lend(x.Exprs...)
		return
	case *syntax.CompositeLit:
		switch {
		case mode == mMove:
			// R14: a value built to be handed off moves its parts.
			w.parts(x, h)
			return
		case w.tracked(w.info.Types[x]):
			// R14: a literal moves its parts, also one lent where it is
			// made: its statement owns it (R43).
			w.parts(x, hand{at: x, how: "it moves into the literal"})
		default:
			lend(x.Base)
			lend(x.Elts...)
		}
		w.produced(x, mode, h)
		return
	case *syntax.Ident:
		v, _ := w.info.Uses[x].(*Var)
		i, ok := w.slot[v]
		if v == nil || !ok {
			w.pkgLetMove(x, v, mode, h)
			w.funcValue(x, w.info.Types[x])
			return
		}
		if mode == mHold && w.tail.slot >= 0 && i >= w.tail.first && !h.yieldRoot {
			// The source owner ends inside the result expression. Transfer
			// it before closing that scope, just as for a directly produced
			// result. Nested result blocks share the same destination.
			owned := w.st.reached && w.st.slots[i].st == ownOwned
			w.move(i, x, hand{at: x, how: "it is yielded to its consumer"})
			if owned && w.st.slots[i].st == ownMoved {
				w.st.slots[w.tail.slot] = ownSlot{st: ownOwned}
			}
		} else if mode == mMove {
			w.move(i, x, h)
		} else {
			if h.root {
				w.useRoot(i, x) // the root of a field: R39 checks the field
			} else {
				w.use(i, x)
			}
			if h.yieldRoot && w.tail.slot >= 0 && i >= w.tail.first {
				w.report(x, "the yielded resource is rooted at %s, whose owner ends before its consumer; yield the whole owner instead of a part (resources R43)", x.Name)
			}
			if h.lend != nil && w.st.reached && w.st.slots[i].st == ownOwned {
				h.lend(i, x)
			}
		}
		return
	case *syntax.ParenExpr:
		w.expr(x.X, mode, h)
		return
	case *syntax.TryExpr:
		saved := w.onFailure
		var paths [][]Release
		w.onFailure = func() { paths = append(paths, w.releases(w.first)) }
		h.failed = nil // an explicit try escapes even in a caught value block
		w.expr(x.X, mode, h)
		w.onFailure = saved
		if w.tryPaths == nil {
			w.tryPaths = map[*syntax.TryExpr][][]Release{}
		}
		w.tryPaths[x] = paths // a covered right operand adds its paths (q105)
		w.recordFailures(x, paths)
		return
	case *syntax.CatchExpr:
		w.yieldValue(x, mode, h, func(m ownMode) { w.catch(x, m, h) })
		return
	case *syntax.CallExpr:
		if !w.call(x, mode, h) {
			w.callFailure(x, h)
			w.produced(x, mode, h)
		}
		return
	case *syntax.FuncLit:
		var takes []int
		if s, ok := w.info.Literals[x]; ok {
			takes = s.Takes
		}
		var params *syntax.FieldList
		if x.Type != nil {
			params = x.Type.Params
		}
		if w.literalEscapes(x) {
			capture := &TaskCapture{}
			w.literalCaptures(x, x.Body, capture, false)
			w.frameCaptured(x.Body, takes, w.paramVars(params), w.name, "this function literal", false, capture)
		} else {
			w.frame(x.Body, takes, w.paramVars(params), w.name, "this function literal", false)
		}
		return

	}
	panic(fmt.Sprintf("check: internal compiler error: the ownership flow has no rule for a %T (ownWalk.expr)", x))
}

// callFailure is the production boundary. An unsuccessful call has evaluated
// its operands, but has not initialized a destination or acquired a temporary.
func (w *ownWalk) callFailure(x *syntax.CallExpr, h hand) {
	if call := w.info.Calls[x]; call != nil && !call.Fails.Empty() && w.st.reached {
		if h.failed != nil {
			h.failed()
		} else if w.onFailure != nil {
			w.onFailure()
		}
	}
}

// recordFailures joins release obligations by value identity, not by a slot's
// position: a later failure may follow a block whose locals are already gone.
// A value absent on any failure path has a drop flag. Each path supplies the
// order at its production boundary, never the success destination's state.
func (w *ownWalk) recordFailures(key syntax.Node, paths [][]Release) {
	var out []Release
	for i, path := range paths {
		for j := range out {
			found := false
			for _, r := range path {
				if out[j].Var == r.Var && out[j].Temp == r.Temp {
					out[j].Flag = out[j].Flag || r.Flag
					found = true
				}
			}
			out[j].Flag = out[j].Flag || !found
		}
		for p := len(path) - 1; p >= 0; p-- {
			r := path[p]
			found := false
			for _, old := range out {
				found = found || old.Var == r.Var && old.Temp == r.Temp
			}
			if !found {
				r.Flag = r.Flag || i > 0
				// Keep this path's reverse-acquisition order. A later
				// failure can own new operands before an earlier owner.
				at := len(out)
				if p+1 < len(path) {
					next := path[p+1]
					for j, old := range out {
						if old.Var == next.Var && old.Temp == next.Temp {
							at = j
							break
						}
					}
				}
				out = append(out, Release{})
				copy(out[at+1:], out[at:])
				out[at] = r
			}
		}
	}
	w.record(key, out)
}

// lendRoot carries the root owner only when the expression's result holds
// a resource. Reading r.id as an int does not lend r to that int's consumer.
func (w *ownWalk) lendRoot(root syntax.Expr, result Type, mode ownMode, h hand) {
	if !w.tracked(result) {
		h.lend = nil
		h.yieldRoot = false
	} else if mode == mHold && w.tail.slot >= 0 {
		h.yieldRoot = true
	}
	w.expr(root, mLend, hand{lend: h.lend, yieldRoot: h.yieldRoot, root: h.root})
}

// fieldIndex is the index of the record field x selects, or -1 when x is
// no field of a record value (a method, an operation, a package member).
func (w *ownWalk) fieldIndex(x *syntax.SelectorExpr) int {
	t := w.info.Types[x.X]
	if t == nil {
		return -1
	}
	s, ok := under(t).(*Struct)
	if !ok {
		return -1
	}
	for k, f := range s.Fields {
		if f.Name == x.Sel.Name {
			return k
		}
	}
	return -1
}

// rootSlot is the slot of the local a place x.f is selected from.
func (w *ownWalk) rootSlot(x syntax.Expr) (*syntax.Ident, int, bool) {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil, 0, false
	}
	v, _ := w.info.Uses[id].(*Var)
	i, ok := w.slot[v]
	if v == nil || !ok {
		return nil, 0, false
	}
	return id, i, true
}

// placeMove refuses a hand-off of a place that holds a resource: a field
// (R39) or an element (R35). A generic-only one is a copy site (A3).
func (w *ownWalk) placeMove(x syntax.Expr, what, format string) {
	t := w.info.Types[x]
	if !w.tracked(t) {
		return
	}
	var root syntax.Expr
	switch p := x.(type) {
	case *syntax.IndexExpr:
		switch under(w.info.Types[p.X]).(type) {
		case *Slice, *Array, *Map:
		default:
			return
		}
		root = p.X
	case *syntax.SelectorExpr:
		root = p.X
	}
	if w.genericOnly(t) {
		w.copySite(t, x, fmt.Sprintf("%s %s is handed off", what, placeText(x)), false)
		return
	}
	w.report(x, "%s cannot be handed off: "+format, placeText(x), exprText(root), exprText(root))
}

// placeText is a place for a message: b.r, xs[…].
func placeText(x syntax.Expr) string {
	if ix, ok := unparen(x).(*syntax.IndexExpr); ok {
		return exprText(ix.X) + "[…]"
	}
	return exprText(x)
}

// resourceMethod returns the operation of the method value x when the
// value it is called on holds a resource; nil otherwise (a field, plain
// data).
func (w *ownWalk) resourceMethod(x *syntax.SelectorExpr) *Method {
	if _, ok := w.info.Types[x].(*Signature); !ok {
		return nil
	}
	t := w.info.Types[x.X]
	if t == nil || isInvalid(t) || !w.c.holdsResource(t) {
		return nil
	}
	it := asInterface(t)
	if it == nil {
		return nil
	}
	return it.method(x.Sel.Name)
}

// selector evaluates a field x.f of a record; it reports false for any
// other selector. The field is lent from its root, and a field taken in
// place cannot be used again (R39, Gap 38).
func (w *ownWalk) selector(x *syntax.SelectorExpr, mode ownMode, h hand) bool {
	k := w.fieldIndex(x)
	if k < 0 {
		return false
	}
	if mode == mMove {
		w.placeMove(x, "a field", "it is a field of %s; a field cannot be moved out of its record (resources R39); move %s whole, or lend the field")
	}
	h.root = true
	w.lendRoot(x.X, w.info.Types[x], mode, h)
	_, i, ok := w.rootSlot(x.X)
	if !ok || !w.st.reached || k >= 64 {
		return true
	}
	if s := w.st.slots[i]; s.live() && (s.fields|s.maybe)&(1<<k) != 0 {
		w.refuse(i, x.Sel, fmt.Sprintf("%s is used after it is taken", exprText(x)), "%s was taken in place (on this path or another) and cannot be used after that (resources R39, R20)", exprText(x))
	}
	return true
}

// fieldTake takes the field x.f of an owned record in place, as the value
// a consuming call is on (`b.r.Close()`): the field is marked, and the
// record's release skips it (R39, Gap 38). It reports false when x is no
// field of a local; the caller then hands x off as a whole value.
func (w *ownWalk) fieldTake(x syntax.Expr, h hand) bool {
	sel, ok := unparen(x).(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	k := w.fieldIndex(sel)
	id, i, ok := w.rootSlot(sel.X)
	if k < 0 || !ok || !w.tracked(w.info.Types[sel]) {
		return false
	}
	w.useRoot(i, id)
	if !w.st.reached {
		return true
	}
	if w.loanTake(i, sel, k) {
		return true
	}
	s := w.st.slots[i]
	l := w.vars[i]
	switch {
	case !s.live():
		return true // reported by useRoot
	case k >= 64:
		w.report(sel, "%s has more than 64 fields, so its field %s cannot be taken in place; take %s whole", id.Name, sel.Sel.Name, id.Name)
	case (s.fields|s.maybe)&(1<<k) != 0:
		w.refuse(i, sel.Sel, fmt.Sprintf("%s is taken twice", exprText(sel)), "%s was taken in place (on this path or another) and cannot be taken again (resources R39, R20)", exprText(sel))
	case l.depth != w.depth:
		w.refuse(i, sel, fmt.Sprintf("%s takes %s, which may run more than once", w.nestedText(), exprText(sel)), "%s cannot take %s (%s): %s belongs to the enclosing function, which releases it, and the block may run at another time or more than once (resources R4, R30)", w.nestedText(), exprText(sel), h.how, id.Name)
	case l.lent && w.infer && l.param > 0 && w.c.infersTakes(w.fn):
		w.promote[l.param-1] = true
	case l.lent:
		w.refuse(i, sel, fmt.Sprintf("%s is lent and its field is taken", id.Name), "%s is lent to %s, so its field %s cannot be taken (%s) (resources R10, R17, R39); write takes %s in the signature", id.Name, w.name, sel.Sel.Name, h.how, id.Name)
	default:
		w.st.slots[i].fields |= 1 << k
		w.info.FieldTakes[sel] = FieldTake{Var: l.v, Index: k}
		if w.st.slots[i].fieldAt == nil {
			w.st.slots[i].fieldAt = sel
		}
	}
	return true
}

// children lends each operand of x.
func (w *ownWalk) children(x syntax.Expr) {
	syntax.Inspect(x, func(n syntax.Node) bool {
		if n == syntax.Node(x) {
			return true
		}
		if e, ok := n.(syntax.Expr); ok && !w.c.typeNodes[e] {
			w.expr(e, mLend, hand{})
		}
		return false
	})
}

// parts moves the parts of a tuple or composite literal that is handed
// off: a field's or element's value (not a field name or a map key, which
// is plain, R40), and a `..base`.
func (w *ownWalk) parts(x syntax.Expr, h hand) {
	// The operands in evaluation order; moved marks a part's value.
	var src, xs []syntax.Expr
	var moved []bool
	switch x := x.(type) {
	case *syntax.TupleExpr:
		src = x.Elts
	case *syntax.CompositeLit:
		if x.Base != nil {
			src = append(src, x.Base)
		}
		src = append(src, x.Elts...)
	}
	for _, e := range src {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if _, field := kv.Key.(*syntax.Ident); !field {
				xs, moved = append(xs, kv.Key), append(moved, false)
			}
			e = kv.Value
		}
		xs, moved = append(xs, e), append(moved, true)
	}
	w.sequence(xs, func(i int) bool {
		if moved[i] {
			w.expr(xs[i], mMove, h)
		} else {
			w.expr(xs[i], mLend, hand{})
		}
		return moved[i]
	})
	if lit, ok := x.(*syntax.CompositeLit); ok && lit.Base != nil && w.st.reached {
		rs := w.replaced(lit)
		for i := len(rs) - 1; i >= 0; i-- {
			w.acquire(ownLocal{temp: rs[i].Temp, t: rs[i].Field.Type})
		}
	}
}

// replaced is the fields of the base of the update or spread x that hold
// a resource and that its value does not copy: each field x names, in
// written order, then, in a spread, each field of the base that x's type
// lacks, in declaration order. The base moved into the value being built,
// so when the value is built the statement owns each such field, as a
// temporary released at its end (R43), last acquired first: so parts
// acquires them in the reverse of this order. Rust drops the fields of a functional record
// update's base that the value does not take at the end of the base's
// scope (a temporary's: the statement). Cached in Info.Replaced, so every
// pass of a loop uses one key per field.
func (w *ownWalk) replaced(x *syntax.CompositeLit) []Replaced {
	if rs, ok := w.info.Replaced[x]; ok {
		return rs
	}
	bt := w.info.Types[x.Base]
	bs := asStruct(bt)
	if bt == nil || isInvalid(bt) || bs == nil {
		return nil
	}
	copied := map[*Field]bool{}
	copies, spread := w.info.Spreads[x]
	for _, c := range copies {
		copied[c.From] = true
	}
	named := map[string]syntax.Pos{}
	var order []string
	for _, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok {
				named[id.Name] = id.Pos()
				order = append(order, id.Name)
			}
		}
	}
	dropped := func(f *Field) bool {
		if spread {
			return !copied[f]
		}
		_, ok := named[f.Name]
		return ok
	}
	var rs []Replaced
	add := func(f *Field, at syntax.Pos) {
		if dropped(f) && w.tracked(f.Type) {
			rs = append(rs, Replaced{Field: f, Temp: &syntax.Ident{NamePos: at, Name: f.Name}})
		}
	}
	seen := map[*Field]bool{}
	for _, name := range order {
		for _, f := range bs.Fields {
			if f.Name == name && !seen[f] {
				seen[f] = true
				add(f, named[name])
			}
		}
	}
	for _, f := range bs.Fields {
		if !seen[f] {
			add(f, x.Base.Pos())
		}
	}
	if len(rs) > 0 {
		w.info.Replaced[x] = rs
	}
	return rs
}

// sequence evaluates the operands xs of one value that is still being
// built (a tuple or composite literal, a call's receiver and arguments),
// left to right, through eval, which reports whether it moved operand i.
// A moved operand that holds a resource is no one's while a later operand
// still runs, and a later operand may leave the statement: a try's
// failure, a defect, a stop. So until the value is built, the statement
// owns it (Info.OwnedTemps, R43), and then it moves into the value
// (Info.PartHands). An operand followed only by inert ones needs no such
// owner. Prior art: Rust's drop elaboration owns each operand of an
// aggregate or a call already evaluated, and drops it on unwind or on `?`.
func (w *ownWalk) sequence(xs []syntax.Expr, eval func(i int) bool) {
	inert := make([]bool, len(xs)+1)
	inert[len(xs)] = true
	for i := len(xs) - 1; i >= 0; i-- {
		inert[i] = inert[i+1] && w.inert(xs[i])
	}
	var held []int
	for i, x := range xs {
		if !eval(i) || inert[i+1] || !w.st.reached {
			continue
		}
		if t := w.info.Types[x]; t != nil && !isInvalid(t) && w.c.holdsResource(t) {
			held = append(held, w.acquire(ownLocal{temp: x}))
		}
	}
	for _, k := range held {
		x := w.vars[k].temp
		if w.info.OwnedTemps[x] != nil {
			w.info.PartHands[x] = true
		}
		if w.st.reached && k < len(w.st.slots) {
			w.st.slots[k] = ownSlot{st: ownMoved, at: x, how: "it moves into the value it is a part of"}
		}
	}
}

// inert reports an operand whose evaluation cannot leave its statement:
// it calls nothing, tries nothing and cannot panic, so a part made before
// it needs no owner of its own while it runs (sequence).
func (w *ownWalk) inert(x syntax.Expr) bool {
	if x == nil {
		return true
	}
	if _, ok := w.info.Values[x]; ok {
		return true
	}
	switch x := x.(type) {
	case *syntax.BasicLit, *syntax.Ident, *syntax.FuncLit:
		return true
	case *syntax.ParenExpr:
		return w.inert(x.X)
	case *syntax.KeyValueExpr:
		return w.inert(x.Key) && w.inert(x.Value)
	case *syntax.SelectorExpr:
		return w.qualifiedLet(x) != nil || w.fieldIndex(x) >= 0 && w.inert(x.X)
	case *syntax.UnaryExpr:
		return w.inert(x.X)
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.QUO, syntax.REM, syntax.SHL, syntax.SHR:
			return false // a division by zero or a negative shift panics
		}
		return w.inert(x.X) && w.inert(x.Y)
	case *syntax.TupleExpr:
		for _, e := range x.Elts {
			if !w.inert(e) {
				return false
			}
		}
		return true
	case *syntax.CompositeLit:
		if x.Base != nil {
			return false
		}
		for _, e := range x.Elts {
			if !w.inert(e) {
				return false
			}
		}
		return true
	}
	return false // a call, a try, a catch, an index or a block may leave
}

// produced owns or refuses a new value x of a resource type: a call's
// result or a literal. A lent one is a temporary of the statement (R43);
// a dropped one is discarded (R42); a moved one has its new owner; a held
// one is the value of the lent if or match whose slot is w.tail.slot.
func (w *ownWalk) produced(x syntax.Expr, mode ownMode, h hand) {
	t := w.info.Types[x]
	if !w.tracked(t) || !w.st.reached {
		return
	}
	if h.yieldRoot {
		w.report(x, "the yielded resource is rooted in this temporary, whose owner ends before its consumer; bind and yield the whole owner instead of a part (resources R43)")
	}
	switch mode {
	case mLend:
		w.acquire(ownLocal{temp: x})
	case mDiscard:
		if w.genericOnly(t) {
			return // a generic body may drop a T (plan 1.3); B2 releases it
		}
		w.report(x, "the %s this produces is discarded, and a resource has one owner; bind it with let, so its scope releases it, or hand it off (resources R42)", t)
	case mHold:
		if i := w.tail.slot; i >= 0 && i < len(w.st.slots) {
			w.st.slots[i] = ownSlot{st: ownOwned}
		}
	}
}

// ownTail is where a block's value goes: the mode its tail expression is
// evaluated in, the hand-off it names, and, for mHold, the slot of the
// temporary that owns a new value (-1: an owner outside the flow).
type ownTail struct {
	mode      ownMode
	how       string
	slot      int
	first     int // locals at/after this boundary cannot outlive the consumer
	lend      func(int, *syntax.Ident)
	yieldRoot bool
	failed    func()
}

// frameTail is a function body's: its value is returned.
var frameTail = ownTail{mode: mMove, how: "it is the block's value", slot: -1}

// branchValue walks the structured paths of an if or match as a value.
func (w *ownWalk) branchValue(x syntax.Expr, mode ownMode, h hand) {
	w.yieldValue(x, mode, h, func(ownMode) { w.stmt(x.(syntax.Stmt)) })
}

// yieldValue gives every block-valued expression one result destination:
// if, match, catch and with all transfer their result before arm locals
// are released. A lending consumer owns a new or block-local result until
// its statement ends; an enclosing lender keeps its original owner. The
// boundary is that of the outer result, not the innermost arm, so nested
// blocks cannot shorten the yielded value's lifetime. Prior art: Rust's
// MIR transfers an arm result to an outer place before dropping locals.
func (w *ownWalk) yieldValue(x syntax.Expr, mode ownMode, h hand, walk func(ownMode)) {
	saved := w.tail
	t := ownTail{mode: mode, how: h.how, slot: -1, first: len(w.vars), lend: h.lend, yieldRoot: h.yieldRoot}
	switch {
	case mode == mHold:
		t = saved
	case mode == mLend && w.tracked(w.info.Types[x]) && w.st.reached:
		t.mode = mHold
		t.slot = w.acquire(ownLocal{temp: x})
		w.st.slots[t.slot] = ownSlot{st: ownMoved, at: x, how: "no arm made it"}
		t.first = len(w.vars)
		if h.yieldRoot {
			t.first = saved.first
		}
	}
	t.failed = h.failed
	w.tail = t
	walk(t.mode)
	w.resultAfterOperands(x, t.slot)
	w.tail = saved
}

// resultAfterOperands separates reserving a result place from acquiring its
// value. Operand temporaries are acquired before the result, so the result
// must be released first (R5). Only this expression's own statement-level
// result place moves; an inherited place belongs to an enclosing consumer.
func (w *ownWalk) resultAfterOperands(x syntax.Expr, slot int) {
	if !w.st.reached || slot < 0 || slot+1 >= len(w.vars) || w.vars[slot].temp != x {
		return
	}
	n := len(w.vars)
	for _, l := range w.vars[slot+1:] {
		if l.v != nil {
			panic("check: internal compiler error: a result place crosses a named owner")
		}
	}
	l := w.vars[slot]
	copy(w.vars[slot:], w.vars[slot+1:])
	w.vars[n-1] = l
	rotate := func(s ownState) ownState {
		if !s.reached {
			return s
		}
		s = padState(s, n)
		st := s.slots
		result := st[slot]
		copy(st[slot:], st[slot+1:])
		st[n-1] = result
		w.step(n - slot)
		return s
	}
	w.st = rotate(w.st)
	// An enclosing catch may have saved a failure before the result was
	// acquired. Its states must use the same place layout at the join.
	for _, k := range w.catches {
		for i, st := range k.fails {
			k.fails[i] = rotate(w.copyState(st))
		}
	}
}

// shortCircuit walks `a && b` or `a || b`: a runs on every path, b only on
// some, so the state after it joins the state after a, as an if with no
// else does (resources R21: a value handed off in b is owned on the other
// path and gets a drop flag). A temporary b acquires lives to the end of
// the statement on the paths that ran b only, so it is released under a
// flag too. Prior art: Rust lowers && and || to control flow before
// borrow checking, so a move in the right operand is a conditional move.
func (w *ownWalk) shortCircuit(x *syntax.BinaryExpr) {
	w.expr(x.X, mLend, hand{})
	n := len(w.vars)
	skipped := w.copyState(w.st)
	w.expr(x.Y, mLend, hand{})
	w.st = w.join(w.st, skipped, len(w.vars))
	for i := n; i < len(w.st.slots); i++ {
		if w.st.slots[i].st == ownOwned {
			w.st.slots[i] = ownSlot{st: ownMaybe, at: x.Y, how: "its operand did not run"}
		}
	}
}

func (w *ownWalk) catch(x *syntax.CatchExpr, mode ownMode, h hand) {
	first := len(w.vars)
	k := &ownCatch{first: first}
	w.catches = append(w.catches, k)
	caught := hand{at: h.at, how: h.how, lend: h.lend, yieldRoot: h.yieldRoot, failed: func() { k.fails = append(k.fails, w.copyState(w.st)) }}
	w.expr(x.X, mode, caught)
	w.catches = w.catches[:len(w.catches)-1]
	// The caught call fails after its operands are evaluated; a try inside
	// it fails before the later temporaries exist.
	n := len(w.vars)
	var fail ownState
	for _, f := range k.fails {
		fail = w.join(fail, f, n)
	}
	out := w.st
	saved := h.failed
	if saved == nil {
		saved = w.onFailure
	}
	if m := w.info.Matches[x]; m != nil && m.Partial && saved != nil {
		// Unhandled alternatives leave with the same pre-production
		// states. They must not borrow the successful result's lifetime.
		for _, f := range k.fails {
			w.st = padState(w.copyState(f), n)
			saved()
		}
	}
	for _, arm := range x.Arms {
		w.st = w.copyState(fail)
		w.block(arm, arm.Body)
		out = w.join(out, w.st, n)
	}
	w.st = out
}

// call evaluates a call; it reports false when x is to be treated as a
// produced value by expr.
func (w *ownWalk) call(x *syntax.CallExpr, mode ownMode, h hand) bool {
	call := w.info.Calls[x]
	if call == nil {
		for _, a := range x.Args {
			w.expr(a, mLend, hand{})
		}
		return false
	}
	if elem, ok := w.valuesSeq(x); ok && syntax.Expr(x) != w.ranged && elem != nil && w.tracked(elem) && w.st.reached {
		text := calleeText(x) + "()"
		if w.genericOnly(elem) {
			w.copySite(elem, x, fmt.Sprintf("%s yields owned values of %s outside a for-in", text, elem), false)
		} else {
			w.report(x, "%s yields the queue's values of %s, each owned by the code that iterates it, and only a for-in over the call releases them; a Seq has no owner, so it cannot be kept or passed on (resources R35, Q7); write for v in %s { … } here", text, elem, text)
		}
	}
	switch call.Kind {
	case CallConversion:
		for _, a := range x.Args {
			if mode != mMove && w.converts(a, w.info.Types[x]) {
				// R23: the interface value owns it; the statement owns
				// the interface value (R43).
				w.expr(a, mMove, hand{at: a, how: fmt.Sprintf("converting it to %s moves it", w.info.Types[x])})
				continue
			}
			w.expr(a, mode, h)
		}
		return true
	case CallWith:
		w.yieldValue(x, mode, h, func(ownMode) {
			for _, a := range x.Args {
				w.expr(a, mLend, hand{})
			}
			w.withProviders(x)
			if x.Trailing != nil {
				w.block(x.Trailing, x.Trailing.List)
			}
		})
		return true
	}
	var sig *Signature
	name := ""
	switch call.Kind {
	case CallFunc:
		if call.Func != nil {
			sig, name = call.Func.Sig, call.Func.Name
		}
	case CallOp, CallProviderOp, CallLawOp:
		if call.Op != nil {
			sig, name = call.Op.Sig, call.Op.Name
		}
	case CallFuncValue:
		if s, ok := under(w.info.Types[x.Fun]).(*Signature); ok {
			sig = s
		}
		name = "the function value"
	}
	taken := map[int]bool{}
	selfTaken := false
	takes := []int(nil)
	if sig != nil {
		takes, selfTaken = sig.Takes, sig.TakesSelf
	}
	if call.Kind == CallFunc && call.Func != nil {
		takes = takesOf(call.Func)
		if len(call.TypeArgs) > 0 {
			w.instance(x, call.Func, call.TypeArgs)
		}
	}
	for _, i := range takes {
		taken[i] = true
	}
	closes := w.info.Closes[x] != nil || w.info.Closes[x.Fun] != nil
	drains := w.drainsLends(call)
	// A Go function's arguments are lent for the call: a Go signature
	// cannot move a value (IG9, hostres.go), so converting a resource to
	// its Go interface parameter is no move (R23).
	hostCallee := call.Kind == CallFunc && call.Func != nil && call.Func.Pkg.Host()
	if closes {
		selfTaken = true
		if call.Kind == CallFunc {
			taken[0] = true
		}
	}
	var loan *ownLoanCall
	defer func() {
		if loan != nil {
			loan.active = false
		}
	}()
	// A bodiless std call that takes a local keeps its entry until the
	// call returns (the lowering's holdTaken): that entry owns it meanwhile.
	held := w.holdsTaken(call)
	// operand evaluates one operand; it reports a moved value (sequence).
	operand := func(a syntax.Expr, take, recv bool, pname string, pt Type) bool {
		if take {
			how := fmt.Sprintf("%s takes %s", name, pname)
			if pname == "" || closes {
				how = fmt.Sprintf("%s takes it", name)
			}
			if id, i, ok := w.borrowed(a); ok && closes {
				// A Close through a borrow of an owned local, `(&h).Close()`
				// or `let p = &h; p.Close()`, takes that local.
				w.move(i, id, hand{at: a, how: how})
				return false
			}
			if recv && w.fieldTake(a, hand{at: a, how: how}) {
				return false // a field taken in place (R39, Gap 38)
			}
			w.expr(a, mMove, hand{at: a, how: how, endLoans: drains})
			_, local := unparen(a).(*syntax.Ident)
			return !(held && local)
		}
		if w.converts(a, pt) && !hostCallee {
			// R23: a resource converted to an operation type with Close
			// moves into the interface value.
			w.expr(a, mMove, hand{at: a, how: fmt.Sprintf("converting it to %s moves it", pt)})
			return true
		}
		w.expr(a, mLend, hand{lend: func(i int, id *syntax.Ident) {
			if loan == nil {
				loan = &ownLoanCall{name: name, active: true}
			}
			w.st.slots[i].callLoans = append(liveLoans(w.st.slots[i].callLoans), ownLoan{loan, id})
		}})
		if len(w.valueLoans(a)) > 0 && !nonEscaping(pt) {
			w.checkLoanValue(a)
		}
		return false
	}
	pname := func(i int) string {
		if sig != nil && i < len(sig.Params) {
			return sig.Params[i].Name
		}
		return ""
	}
	ptype := func(i int) Type {
		if sig != nil && i < len(sig.Params) {
			return sig.Params[i].Type
		}
		return nil
	}
	// The receiver and the arguments, in evaluation order, for sequence.
	var xs []syntax.Expr
	var evals []func() bool
	off := 0
	switch {
	case call.Recv != nil && call.Kind == CallFunc:
		xs = append(xs, call.Recv)
		evals = append(evals, func() bool { return operand(call.Recv, taken[0], true, pname(0), nil) })
		off = 1
	case call.Recv != nil:
		xs = append(xs, call.Recv)
		evals = append(evals, func() bool { return operand(call.Recv, selfTaken, true, "", nil) })
	case call.Kind == CallFunc:
		// The callee is a function's name: no value of its own.
	default:
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok && call.Kind != CallFuncValue {
			// The callee is an operation selected on its value (a
			// capability's): no method value is made.
			w.expr(sel.X, mLend, hand{})
			break
		}
		w.expr(x.Fun, mLend, hand{})
	}
	for i, a := range x.Args {
		xs = append(xs, a)
		if p, repeated, moving := movingBody(call); moving && i+off == p {
			if lit, ok := unparen(a).(*syntax.FuncLit); ok {
				evals = append(evals, func() bool {
					var params *syntax.FieldList
					if lit.Type != nil {
						params = lit.Type.Params
					}
					var takes []int
					if s := w.info.Literals[lit]; s != nil {
						takes = s.Takes
					}
					w.taskFrame(lit, lit.Body, w.paramVars(params), takes, repeated)
					return false
				})
				continue
			}
		}
		evals = append(evals, func() bool { return operand(a, taken[i+off], closes, pname(i+off), ptype(i+off)) })
	}
	w.sequence(xs, func(i int) bool { return evals[i]() })

	if x.Trailing != nil {
		var takes []int
		if s, ok := w.info.Literals[x.Trailing]; ok {
			takes = s.Takes
		}
		if p, repeated, moving := movingBody(call); moving && p == len(x.Args)+off {
			w.taskFrame(x.Trailing, x.Trailing, nil, takes, repeated)
		} else if w.literalEscapes(x.Trailing) {
			capture := &TaskCapture{}
			w.literalCaptures(x.Trailing, x.Trailing, capture, false)
			w.frameCaptured(x.Trailing, takes, nil, w.name, fmt.Sprintf("the block passed to %s", calleeText(x)), false, capture)
		} else {
			w.frame(x.Trailing, takes, nil, w.name, fmt.Sprintf("the block passed to %s", calleeText(x)), false)
		}
	}
	if drains && call.Recv != nil && w.st.reached {
		w.drainLoans(call.Recv)
	}
	w.keepArgumentLoans(x)
	return false
}

// withProviders evaluates the provider expressions with x builds that lie
// in this body (an inline or a local context's): a resource provider
// moves into the with, which releases it at the block's end (R11); any
// other is lent. A context literal itself evaluates nothing: each with
// that builds it does (expr). A package let provider is withown.go's.
func (w *ownWalk) withProviders(x *syntax.CallExpr) {
	if w.top == nil {
		return
	}
	for f := w.info.Withs[x]; f != nil; f = f.Next {
		for _, a := range f.Acquired {
			if a == nil || a.Entry == nil || a.Entry.Provider == nil {
				continue
			}
			p := a.Entry.Provider
			if owner := w.c.contextOwners.of[a.Entry]; owner == nil || owner.file != w.c.cur || p.Pos() < w.top.Pos() || p.End() > w.top.End() {
				continue
			}
			if a.Releases() {
				w.expr(p, mMove, hand{at: p, how: "the with owns it", provider: true})
			} else {
				w.expr(p, mLend, hand{})
			}
		}
	}
}

// holdsTaken reports a call of a bodiless std operation or function that
// takes a value: the lowering keeps a taken local's entry on its frame
// until the call returns (lower.holdTaken, Q7, D-C).
func (w *ownWalk) holdsTaken(call *Call) bool {
	switch {
	case call.Kind == CallOp && call.Op != nil && call.Op.Sig != nil && call.Recv != nil:
		n, ok := w.info.Types[call.Recv].(*Named)
		return ok && n.Obj != nil && n.Obj.Pkg != nil && n.Obj.Pkg.std && len(call.Op.Sig.Takes) > 0
	case call.Kind == CallFunc && call.Func != nil && call.Func.Pkg != nil && call.Func.Pkg.std && call.Func.Decl != nil && call.Func.Decl.Extern != nil:
		return len(call.Func.Sig.Takes) > 0
	}
	return false
}

// converts reports whether a, a value that holds a resource and is no
// interface value, is converted to the operation type t (R23).
func (w *ownWalk) converts(a syntax.Expr, t Type) bool {
	if t == nil {
		return false
	}
	if _, tp := t.(*TypeParam); tp {
		return false
	}
	if _, ok := under(t).(*Interface); !ok || !w.tracked(t) {
		return false
	}
	at := w.info.Types[a]
	if !w.tracked(at) {
		return false
	}
	_, iface := under(at).(*Interface)
	return !iface
}

// calleeText is the written callee of x, for a message.
func calleeText(x *syntax.CallExpr) string {
	switch f := x.Fun.(type) {
	case *syntax.Ident:
		return f.Name
	case *syntax.SelectorExpr:
		if id, ok := f.X.(*syntax.Ident); ok {
			return id.Name + "." + f.Sel.Name
		}
		return f.Sel.Name
	}
	return "the call"
}
