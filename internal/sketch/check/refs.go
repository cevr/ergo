package check

import (
	"maps"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Class H (round 16): the body graph does not depend on typing. Its edges
// are the plain identifiers of each body that resolve, through the body's
// lexical scopes, to a package function or a package var. refWalker finds
// them before any body is typed, visits every node of the body and never
// stops early, so a tentative type error (a generic call refused while the
// rows are still provisional) cannot hide an edge (round 15 M1).
//
// The scopes mirror the checker's (stmt.go, rows.go checkFuncBody, call.go
// body): a function's frame holds its receiver, its parameters and the bare
// names of its written capability row; a block, an if, a for, a range, a
// switch and each of its clauses, a select clause, a catch arm and a
// function literal or block open a scope; := and a local var bind after
// their right-hand side. Type parameters are not values (lookup does not
// see them), so they shadow nothing.
//
// Identifiers that are not value references are skipped: a selector's
// name, a label, a type (a composite literal's type, a local var's type, a
// function literal's signature, a case type of a type switch or a catch
// arm), a context entry key and a named context, and a struct literal's field names. A
// composite literal's key is a field label when the literal's type is a
// struct and an expression otherwise (a map key, an array index), as in
// go/types: the walk decides which from the literal's written type
// (reflits.go), walks every expression key, so a map key that names a
// package var or function is an edge and a class G value use (round 17),
// and walks a key whose literal's kind it cannot decide as an expression.
//
// References that need types (a method through a selector, a function of
// another package) are no edge: a method and an imported function spell
// their rows. A call x.f(…) that can reach a package function on x's type
// (196m) is an edge to the one function it resolves to, never to every
// function named f (round 6 review M1): when the walk fixes x's type from
// declarations (a typed local, a parameter, a composite literal) it
// resolves the call itself, and otherwise the call waits for typing
// (selfCallee, resolveSelfCalls), as go/types records a method use.
//
// The walk also finds the class G value uses (rows.go pinGroup), so they do
// not depend on typing either (round 16). A reference to a package function
// with an inferred row that is not the callee of a call is a value use of
// both its rows. A call of it is a value use of a row when an enclosing
// frame carries that row as a value, walking the frames from the innermost
// out: a function literal stops the walk for a row it writes (an error row,
// a needs clause) and passes it on when it is called on the spot, else it
// is a value; a trailing block of a call other than with is a value; a go
// expression that is not a statement is a value for errors (its task's
// type carries them) and passes capabilities on (they are charged at the go
// site). A with block and a go statement are no frame. A function literal
// or trailing block passed directly to a declared callee whose parameter
// has a function type with a closed row (no type parameter in it) takes
// that row from the parameter (call.go funcLit, blockLit), so the walk
// stops there for that row (round 17 m1): the callee's signature was
// resolved before any body (decl.go run), so this is order-invariant.
//
// Which callbacks carry a row at all is typing's verdict, not the walk's: a
// call whose callee is invalid checks none of the literals and blocks
// passed to it (call.go silentArgs), and typing records each function
// literal, trailing block and with block it checks (typedFrame) and each
// one it skips for a diagnosed reason (skipFrames). The walk keeps the
// uses it finds inside such a frame apart (guardedUse), and class G drops
// them from the pin only when an enclosing frame is invalid-diagnosed in
// the component's silent pass, which types each member from empty rows
// (liveUses; round 8 review M2); a frame skipped under a value that waits
// on a row is provisional and keeps them (provisional.go; round 9 review
// M1). The S108 then drops the uses whose frame the reporting pass finds
// invalid-diagnosed (reportedUses; round 10 ruling). So no callee is judged invalid by its
// spelling, wherever the invalid value flows. Prior art: go/types records
// each expression's type and mode once (Info.Types) and every later pass
// reads that record; for a call whose callee is invalid it only uses the
// arguments (call.go, check.use).
type refWalker struct {
	c      *checker
	scopes []map[string]*localBinding
	refs   map[any]bool
	// values holds the class G value uses: for each package function with
	// an inferred row, where the body first uses each of its rows (errors,
	// capabilities) as a value, in the walk's order.
	values  map[*Func][2]useSite
	frames  []valueFrame
	callees map[syntax.Node]bool // callees of calls: identifiers and function literals
	// argRows marks a function literal passed to a parameter whose function
	// type closes the row (errors, capabilities): see refWalker.
	argRows map[syntax.Node][2]bool
	// tparams are the type parameters of the function walked, which come
	// before declarations in a written type (localTypeCtx).
	tparams map[string]bool
	// ops counts the literal elements walked (an operation count for the
	// linear-growth tests).
	ops int
	// pending holds the calls x.f(…) whose callee needs x's type from
	// typing (selfCallee), in walk order, with the value-use sites their
	// frames give a called function (callSite).
	pending []pendingSelf
	// retired holds the calls x.f(…) whose receiver x names nothing in
	// scope: typing reports it S104 whatever the rows, so the call waits for
	// nothing and takes no edge (selfCallee; RefWalkProblems checks that
	// the reporting pass resolves none of them).
	retired []*syntax.SelectorExpr
	// Test builds only (refTrace set): the resolution of each walked
	// identifier (a package object or nil), the identifiers in type
	// and context positions (not value references), the composite-literal
	// keys walked as values because the literal's kind is not decided
	// (reflits.go), and every node visited.
	idents     map[*syntax.Ident]any
	nameIdents map[*syntax.Ident]bool
	binders    map[*syntax.Ident]bool // the identifiers that declare a local
	undecided  map[*syntax.Ident]bool
	visited    map[syntax.Node]bool
	// guard is the innermost callback frame the walk is in (nil outside
	// every one), and guarded holds the class G value uses found inside
	// callback frames: they count unless typing recorded a frame invalid
	// (checker.liveUses; round 8 review M2). cbFrames lists every callback
	// frame walked, in test builds only.
	guard    *guardNode
	guarded  []guardedUse
	cbFrames []syntax.Node
	// owner is the body walked: its *Func or its *pkgVar.
	owner any
}

// A useSite is where a body first uses a row of a function as a value: a
// reference that is not a call (via "") or the literal, trailing block or
// go task that carries the row (via names it).
type useSite struct {
	ok  bool
	at  syntax.Pos
	via string
}

// A frameMode says what a value frame does with a row of the calls inside
// it.
type frameMode uint8

const (
	framePass  frameMode = iota // the row flows to the enclosing frame
	frameValue                  // the row is carried by a value
	frameStop                   // the row is written: it stops here
)

// A valueFrame is a function literal, trailing block or go expression, with
// its mode for errors and for capabilities, its position and its name.
type valueFrame struct {
	mode [2]frameMode
	at   syntax.Pos
	via  string
}

// refTrace, when set by a test, receives each body's walk.
var refTrace func(root syntax.Node, w *refWalker)

// bodyRefs walks f's body: the package functions and vars it refers to, and
// its class G value uses.
func (c *checker) bodyRefs(f *Func) *refWalker {
	w := c.newRefWalker()
	w.owner = f
	saved := c.cur
	// Defensive: reference collection records edges and does not report.
	c.cur = f.file
	defer func() { c.cur = saved }()
	w.push()
	d := f.Decl
	if f.Sig != nil {
		for _, tp := range f.Sig.TParams {
			w.tparams[tp.Name] = true
		}
	}
	w.params(d.Type, f.Sig)
	w.list(d.Body.List)
	w.pop()
	w.visit(d.Body)
	if refTrace != nil {
		refTrace(d.Body, w)
	}
	return w
}

// varRefs walks a var initializer, as bodyRefs.
func (c *checker) varRefs(pv *pkgVar) *refWalker {
	w := c.newRefWalker()
	w.owner = pv
	saved := c.cur
	// Defensive: initializer reference collection records edges and does not report.
	c.cur = pv.file
	defer func() { c.cur = saved }()
	var root syntax.Node
	if pv.spec != nil {
		if pv.idx < len(pv.spec.Values) {
			root = pv.spec.Values[pv.idx]
		} else if len(pv.spec.Values) == 1 {
			root = pv.spec.Values[0]
		}
	}
	if root == nil {
		return w
	}
	w.expr(root.(syntax.Expr))
	if refTrace != nil {
		refTrace(root, w)
	}
	return w
}

func (c *checker) newRefWalker() *refWalker {
	w := &refWalker{c: c, refs: map[any]bool{}, values: map[*Func][2]useSite{}, callees: map[syntax.Node]bool{},
		argRows: map[syntax.Node][2]bool{}, tparams: map[string]bool{}}
	if refTrace != nil {
		w.idents = map[*syntax.Ident]any{}
		w.nameIdents = map[*syntax.Ident]bool{}
		w.binders = map[*syntax.Ident]bool{}
		w.undecided = map[*syntax.Ident]bool{}
		w.visited = map[syntax.Node]bool{}
	}
	return w
}

// A localBinding is what the walk knows of a local's type, which its
// declaration fixes: a written type, resolved where it is declared (a
// declared function's parameter or receiver takes its resolved signature's
// type; a function literal's parameter, a typed var or a local bound to a
// composite literal resolves the written name), or the signature of the
// declared function it aliases. Either may be nil (unknown). A local
// declared later with the type's name does not change it (round 18 m1).
type localBinding struct {
	rt  Type
	sig *Signature
	// row marks a bare name of the written needs row: the capability
	// typing binds as a capBinding (bindRequirements), not a value.
	row bool
}

// writtenType resolves a written type at the point it is declared: a type
// name, possibly instantiated or behind *, through the package scope and
// the imports. It is nil for a type parameter, a name a local shadows
// there, and any other form. An instance resolves to its generic type,
// whose members keep their type parameters, so closedRows treats a row
// that mentions them as open.
func (w *refWalker) writtenType(t syntax.Expr) Type {
	if s, ok := t.(*syntax.StarExpr); ok {
		t = s.X
	}
	if ix, ok := t.(*syntax.IndexExpr); ok {
		t = ix.X
	}
	id, ok := t.(*syntax.Ident)
	if !ok || w.tparams[id.Name] || w.local(id.Name) {
		return nil
	}
	if tn, ok := w.c.typeObject(id.Name, w.c.cur).(*TypeName); ok {
		return tn.Type
	}
	return nil
}

func (w *refWalker) push() { w.scopes = append(w.scopes, map[string]*localBinding{}) }
func (w *refWalker) pop()  { w.scopes = w.scopes[:len(w.scopes)-1] }

func (w *refWalker) bindAs(name string, b *localBinding) {
	w.scopes[len(w.scopes)-1][name] = b
}

// binder binds the local that the identifier id declares (a parameter, a
// let or loop name, a pattern or catch binder).
func (w *refWalker) binder(id *syntax.Ident, b *localBinding) {
	if w.binders != nil {
		w.binders[id] = true
	}
	w.bindAs(id.Name, b)
}

// catchPattern walks one pattern of a catch arm and binds every name typing
// binds there that could hide a package object, valid or not (stage C
// round 6 fuzz): a record pattern T{F: p} binds the names of its field
// patterns, as a match pattern does, and `as x` binds x. Any other part is
// a type position. A lone name binds the rest exactly when typing's
// catchAll says so (catchAllObject, over the walk's scopes; fuzz-17d6),
// even when it hides a package object or a prelude name.
func (w *refWalker) catchPattern(p syntax.Expr) {
	core, name := asParts(p)
	if name != nil {
		w.visit(p)
	}
	id, lone := core.(*syntax.Ident)
	lit, isLit := core.(*syntax.CompositeLit)
	switch {
	case isLit:
		w.pattern(lit, false)
	case lone && id.Name != "_" && w.catchAll(id):
		w.visit(id)
		w.binder(id, &localBinding{})
	default:
		w.typ(core)
	}
	if name != nil {
		w.visit(name)
		if name.Name != "_" {
			w.binder(name, &localBinding{})
		}
	}
}

func (w *refWalker) visit(n syntax.Node) {
	if w.visited != nil && n != nil {
		w.visited[n] = true
	}
}

// params binds a function type's parameter names and the bare names of its
// written capability row (bindRequirements), and visits the signature. sig
// is the declared function's resolved signature (nil for a function
// literal): each parameter takes its type from there, so a written
// structural type (`r struct{ Run func(…) }`) is known too.
func (w *refWalker) params(ft *syntax.FuncType, sig *Signature) {
	if ft == nil {
		return
	}
	if ft.Params != nil {
		count := 0
		for _, field := range ft.Params.List {
			count += max(len(field.Names), 1)
		}
		if sig != nil && len(sig.Params) != count {
			sig = nil
		}
		i := 0
		for _, field := range ft.Params.List {
			for j := range max(len(field.Names), 1) {
				var rt Type
				switch {
				case sig != nil:
					rt = sig.Params[i].Type
				default:
					rt = w.writtenType(field.Type)
				}
				i++
				if j < len(field.Names) && field.Names[j].Name != "_" {
					w.binder(field.Names[j], &localBinding{rt: rt})
				}
			}
		}
	}
	if ft.Needs != nil {
		for _, x := range ft.Needs.Names {
			if w.c.capTypeName(x) == nil {
				continue
			}
			switch x := x.(type) {
			case *syntax.Ident:
				w.bindAs(x.Name, &localBinding{row: true})
			case *syntax.SelectorExpr:
				w.bindAs(x.Sel.Name, &localBinding{row: true})
			}
		}
	}
	w.typ(ft)
}

// ident resolves a plain identifier in a value position.
func (w *refWalker) ident(x *syntax.Ident) {
	w.visit(x)
	var o any
	if !w.local(x.Name) {
		switch obj := w.c.typeObject(x.Name, w.c.cur).(type) {
		case *Func:
			if obj.Pkg == w.c.pkg {
				o = obj
			}
		case *Var:
			if pv := w.c.pkgVarOf(obj); pv != nil {
				o = pv
			}
		}
	}
	if o != nil {
		w.refs[o] = true
	}
	if f, ok := o.(*Func); ok && (f.inferErrors || f.inferUses) {
		w.c.inferredRefs[x] = f
		w.valueUse(f, w.callees[x], x.Pos())
	}
	if w.idents != nil {
		w.idents[x] = o
	}
}

// valueUse records the class G value uses of a reference to f at at:
// called or not (see refWalker). The first site of each row is kept.
func (w *refWalker) valueUse(f *Func, called bool, at syntax.Pos) {
	site := [2]useSite{{ok: true, at: at}, {ok: true, at: at}}
	if called {
		site = w.callSite()
	}
	if w.guard != nil {
		if site[0].ok || site[1].ok {
			w.guarded = append(w.guarded, guardedUse{f: f, site: site, guard: w.guard})
		}
		return
	}
	uses := w.values[f]
	for k := range uses {
		if !uses[k].ok {
			uses[k] = site[k]
		}
	}
	w.values[f] = uses
}

// A guardNode is a callback frame the walk is in: a function literal, a
// trailing block or a with block, with the frame around it (up). Typing
// does not check every such frame: a call whose callee is invalid (a
// refused import, an unknown name, a value of invalid type wherever it
// came from) or is no function checks none of the literals and blocks
// passed to it (call.go silentArgs). Whether typing checked a frame, or
// skipped it as invalid or provisional, is typing's verdict, recorded per
// frame (frameStateAt), never decided by the walk from the callee's
// spelling (round 8 review M2; round 9 review M1).
type guardNode struct {
	frame syntax.Node
	up    *guardNode
}

// A guardedUse is a class G value use inside callback frames: it counts
// only when typing checked each frame of guard in the latest check of the
// body (checker.liveUses).
type guardedUse struct {
	f     *Func
	site  [2]useSite
	guard *guardNode
}

// inFrame walks walk inside the callback frame n.
func (w *refWalker) inFrame(n syntax.Node, walk func()) {
	if w.visited != nil {
		w.cbFrames = append(w.cbFrames, n)
	}
	saved := w.guard
	w.guard = &guardNode{frame: n, up: saved}
	walk()
	w.guard = saved
}

// beginBodyCheck starts a check of owner's body: the callback frames typed
// until the returned function runs belong to this check (typedFrame).
func (c *checker) beginBodyCheck(owner any) func() {
	if c.bodyGen == nil {
		c.bodyGen, c.frameGen, c.frameInv, c.frameRef = map[any]int{}, map[syntax.Node]int{}, map[syntax.Node]int{}, map[syntax.Node]int{}
	}
	c.genSeq++
	saved, savedMark, savedOwner := c.curGen, c.stmtMark, c.owner
	c.curGen = c.genSeq
	c.stmtMark = c.undiagSeq
	c.bodyGen[owner] = c.genSeq
	// The body's view of the package's facts (owners.go), restored when a
	// body checked on demand inside another one ends.
	c.owner = bodyOwner(owner)
	return func() { c.curGen, c.stmtMark, c.owner = saved, savedMark, savedOwner }
}

// typedFrame records that typing checks the callback frame n (a function
// literal, a trailing block or a with block) in the current body check.
func (c *checker) typedFrame(n syntax.Node) {
	if c.curGen != 0 {
		c.frameGen[n] = c.curGen
	}
}

// A frameVerdict is the verdict liveUses read for a callback frame and the
// body check it read it from (test builds).
type frameVerdict struct {
	state frameState
	gen   int
}

func (v frameVerdict) kept() bool { return v.state != frameInvalid }

// liveUses is owner's class G value uses for the pin: the walk's uses
// outside every callback frame, and each use inside frames none of which
// is invalid-diagnosed in owner's latest check, which is the component's
// silent pass (the earliest site of each row is kept). An invalid frame
// (a callee invalid for a diagnosed reason) carries no row, so nothing
// inside it is a use (D3; round 8 review M2). A provisional frame, and
// every frame inside it, which typing did not enter, keeps its uses
// (provisional.go; round 9 review M1). kept is the guarded uses counted.
func (c *checker) liveUses(owner any) (uses map[*Func][2]useSite, kept []guardedUse) {
	return c.usesIn(owner, c.guardedRefs[owner], c.guardTrace)
}

// reportedUses is owner's class G value uses after the reporting pass: the
// walk's uses outside every callback frame, and each use the pin counted
// whose frame is not invalid-diagnosed in the reporting pass, which types
// the bodies on the converged rows (round 10 ruling). A use in a frame
// refused by class G stays, so a class G refusal is never dropped.
func (c *checker) reportedUses(owner any) map[*Func][2]useSite {
	uses, _ := c.usesIn(owner, c.pinKept[owner], nil)
	return uses
}

// usesIn is owner's uses outside every callback frame and the uses of gs
// whose frame is not invalid-diagnosed in owner's latest check, with the
// guarded uses it kept.
func (c *checker) usesIn(owner any, gs []guardedUse, trace map[syntax.Node]frameVerdict) (map[*Func][2]useSite, []guardedUse) {
	if len(gs) == 0 {
		return c.valueRefs[owner], nil
	}
	out := maps.Clone(c.valueRefs[owner])
	if out == nil {
		out = map[*Func][2]useSite{}
	}
	gen := c.bodyGen[owner]
	memo := map[*guardNode]frameState{}
	var kept []guardedUse
	for _, g := range gs {
		if c.chainState(g.guard, gen, memo, trace) == frameInvalid {
			continue
		}
		kept = append(kept, g)
		uses := out[g.f]
		for k := range uses {
			if g.site[k].ok && (!uses[k].ok || g.site[k].at < uses[k].at) {
				uses[k] = g.site[k]
			}
		}
		out[g.f] = uses
	}
	return out, kept
}

// callSite is where a call at the current point of the walk uses each row
// of its callee as a value: the innermost frame that does not pass the row
// on decides, and only a frame that carries it as a value is a use.
func (w *refWalker) callSite() [2]useSite {
	var site [2]useSite
	for k := range site {
		for i := len(w.frames) - 1; i >= 0; i-- {
			fr := w.frames[i]
			if m := fr.mode[k]; m != framePass {
				if m == frameValue {
					site[k] = useSite{ok: true, at: fr.at, via: fr.via}
				}
				break
			}
		}
	}
	return site
}

// A pendingSelf is a call x.f(…) whose callee typing resolves, with the
// value-use sites of its frames (callSite).
type pendingSelf struct {
	sel   *syntax.SelectorExpr
	site  [2]useSite
	guard *guardNode
}

// pattern walks a pattern of a match case, a let or a for (196p, 196t);
// letFor marks a let's or a for's. The names it binds (patternNames, by
// patternTests) become locals after it, and every other node is a name,
// not a value reference (a type or a constant that tests the value, a
// field key, a literal).
func (w *refWalker) pattern(p syntax.Expr, letFor bool) {
	names := patternNames(p, func(name string) bool { return w.patternTests(name, letFor) })
	binds := make(map[*syntax.Ident]bool, len(names))
	for _, id := range names {
		binds[id] = true
	}
	syntax.Inspect(p, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && !binds[id] {
			w.name(id)
			return true
		}
		w.visit(n)
		return true
	})
	for _, id := range names {
		w.binder(id, &localBinding{})
	}
}

// catchAll is typing's catchAll over the walk's scopes: a type parameter
// comes first, as in lookupType, then walkObject.
func (w *refWalker) catchAll(id *syntax.Ident) bool {
	var o Object
	if w.tparams[id.Name] {
		o = &TypeName{Name: id.Name}
	} else {
		o = w.walkObject(id.Name)
	}
	return catchAllObject(o)
}

// walkObject is the object a name resolves to in the walk's scopes, as
// typing's lookup does: a local first, then the package scope, the universe
// and the file's imports (typeObject). A local row binding is the
// capability typing binds (capBinding, bindRequirements); any other local
// is a local value.
func (w *refWalker) walkObject(name string) Object {
	if b := w.lookupLocal(name); b != nil {
		if b.row {
			return &capBinding{}
		}
		return &Var{Name: name}
	}
	return w.c.typeObject(name, w.c.cur)
}

// patternTests is typing's rule for a bare name in a pattern
// (patternNameTests) over the walk's scopes.
func (w *refWalker) patternTests(name string, letFor bool) bool {
	return patternNameTests(w.walkObject(name), w.c.isPreludeName(name), letFor)
}

// namePattern is typing's single-name rule of a let or a for
// (checker.namePattern): true when the name tests, so it is a pattern.
func (w *refWalker) namePattern(id *syntax.Ident) bool {
	return w.patternTests(id.Name, true)
}

// frame walks walk inside a value frame.
func (w *refWalker) frame(f valueFrame, walk func()) {
	w.frames = append(w.frames, f)
	walk()
	w.frames = w.frames[:len(w.frames)-1]
}

// callee marks the callee of a call: a plain identifier, possibly
// instantiated, or a function literal called on the spot.
func (w *refWalker) callee(fun syntax.Expr) {
	x := unparen(fun)
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = unparen(ix.X)
	}
	switch x.(type) {
	case *syntax.Ident, *syntax.FuncLit:
		w.callees[x] = true
	}
}

// selfCallee records the edge of a call x.f(…) that can reach a package
// function on x's type (196m rule 1). The edge goes to the one function the
// call resolves to, never to every function named f (round 6 review M1):
// an edge by name merges unrelated bodies into one component, which makes
// false class G and initialization-cycle refusals, and k same-named
// functions called m times give k·m edges.
//
// A receiver whose type its declaration fixes (a parameter, a typed local,
// a local bound to a composite literal, a composite literal) resolves here:
// a field f of that type comes first and is no edge, else the function on
// that type is. Any other receiver (a call's result, a local bound to one,
// an element, a field) needs typing: the call is pending, and
// resolveSelfCalls gives it the callee typing resolves. The callee is
// called, so its rows are value uses only through a frame that carries
// them as a value (callSite).
func (w *refWalker) selfCallee(fun syntax.Expr) {
	sel := calledSelector(fun)
	if sel == nil {
		return
	}
	first, ok := w.c.pkg.Scope.names[sel.Sel.Name].(*Func)
	if !ok || first.owner == nil {
		return
	}
	if id, ok := sel.X.(*syntax.Ident); ok && !w.local(id.Name) {
		switch w.c.typeObject(id.Name, w.c.cur).(type) {
		case *PkgName:
			return
		case nil:
			// x names nothing: an S104 whatever the rows, a terminal
			// resolution that waits for no body (round 7 review M1).
			w.retired = append(w.retired, sel)
			return
		}
	}
	t := w.staticType(sel.X)
	if t == nil {
		w.pending = append(w.pending, pendingSelf{sel: sel, site: w.callSite(), guard: w.guard})
		return
	}
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, _ := t.(*Named)
	if n == nil {
		return
	}
	if s := asStruct(n); s != nil {
		if fld, _ := s.field(sel.Sel.Name); fld != nil {
			return
		}
	}
	if f := n.method(sel.Sel.Name); f != nil && f.owner != nil {
		if _, indexed := unparen(fun).(*syntax.IndexExpr); indexed && ownBrackets(f) == 0 {
			// x.f[i](…) with no bracket type parameter to fill: an S104,
			// no call of f (expr.go selfCall; round 8 review m1).
			return
		}
		w.refs[f] = true
		if f.inferErrors || f.inferUses {
			w.valueUse(f, true, sel.Sel.Pos())
		}
	}
}

// ownBrackets counts the bracket type parameters of the 196m function f
// that its first parameter does not name: the type arguments x.f[…](…)
// fills (typeops.go selfSig counts the same on an instance).
func ownBrackets(f *Func) int {
	if f.Sig == nil {
		return 0
	}
	named := map[string]bool{}
	for _, a := range f.selfArgs {
		if id, ok := a.(*syntax.Ident); ok {
			named[id.Name] = true
		}
	}
	n := 0
	for i, tp := range f.Sig.TParams {
		if i < f.Sig.NBracket && !named[tp.Name] {
			n++
		}
	}
	return n
}

// calleeSig returns the signature of a call's callee when it is fixed
// before any body is typed (decl.go run resolves every signature first):
//   - a plain identifier, possibly instantiated, that names a function of
//     the package or the universe, or a local that aliases one;
//   - a function of an imported package;
//   - a method, or a field of function type, selected from a receiver whose
//     type is written: a parameter, a receiver, a typed local or one bound
//     to a composite literal, a composite literal itself (round 17 m1), or
//     a bare record with no fields (D-B2).
//
// Any other callee (a value whose type is inferred) gives nil, and the
// round-15 rule stays: conservative.
func (w *refWalker) calleeSig(fun syntax.Expr) *Signature {
	x := unparen(fun)
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = unparen(ix.X)
	}
	switch x := x.(type) {
	case *syntax.Ident:
		if b := w.lookupLocal(x.Name); b != nil {
			return b.sig
		}
		return declaredSig(w.c.typeObject(x.Name, w.c.cur))
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok && !w.local(id.Name) {
			if pn, ok := w.c.typeObject(id.Name, w.c.cur).(*PkgName); ok && !pn.broken {
				return declaredSig(pn.Pkg.Scope.names[x.Sel.Name])
			}
			if t := w.bareRecord(id); t != nil {
				return memberSig(t, x.Sel.Name) // Runner.Each(…) on the bare record (D-B2)
			}
			return nil
		}
		return memberSig(w.staticType(x.X), x.Sel.Name)
	}
	return nil
}

// declaredSig is the signature of a declared function that is not generic
// in a way the call must infer: a function, not a method.
func declaredSig(o Object) *Signature {
	if f, ok := o.(*Func); ok {
		return f.Sig
	}
	return nil
}

// initBinding is what a local's initializer fixes of its type: the
// signature of the declared function it aliases, or a composite literal's
// written type.
func (w *refWalker) initBinding(e syntax.Expr) *localBinding {
	switch e := unparen(e).(type) {
	case *syntax.Ident:
		if b := w.lookupLocal(e.Name); b != nil {
			return &localBinding{rt: b.rt, sig: b.sig}
		}
		if sig := declaredSig(w.c.typeObject(e.Name, w.c.cur)); sig != nil && len(sig.TParams) == 0 {
			return &localBinding{sig: sig}
		}
		if t := w.bareRecord(e); t != nil {
			return &localBinding{rt: t}
		}
	case *syntax.SelectorExpr:
		if id, ok := e.X.(*syntax.Ident); ok && !w.local(id.Name) {
			if pn, ok := w.c.typeObject(id.Name, w.c.cur).(*PkgName); ok && !pn.broken {
				if sig := declaredSig(pn.Pkg.Scope.names[e.Sel.Name]); sig != nil && len(sig.TParams) == 0 {
					return &localBinding{sig: sig}
				}
			}
		}
	case *syntax.CompositeLit:
		return &localBinding{rt: w.writtenType(e.Type)}
	}
	return &localBinding{}
}

// staticType is the type of a receiver expression fixed before bodies, or
// nil: a local whose declaration resolved it (localBinding), or a
// composite literal's written type.
func (w *refWalker) staticType(e syntax.Expr) Type {
	switch e := unparen(e).(type) {
	case *syntax.Ident:
		if b := w.lookupLocal(e.Name); b != nil {
			return b.rt
		}
		return w.bareRecord(e)
	case *syntax.CompositeLit:
		return w.writtenType(e.Type)
	}
	return nil
}

// bareRecord is the type of a bare record with no fields written as a
// value, `Runner` (D-B2), or nil.
func (w *refWalker) bareRecord(id *syntax.Ident) Type {
	if w.tparams[id.Name] || w.local(id.Name) {
		return nil
	}
	if tn, ok := w.c.typeObject(id.Name, w.c.cur).(*TypeName); ok && isZeroRecord(tn.Type) {
		return tn.Type
	}
	return nil
}

// memberSig is the signature of the method or the function-typed field
// name of t: a named type, a pointer to one, or a written struct type. A
// method or field of a generic type keeps its type parameters, so
// closedRows treats a row that mentions them as open.
func memberSig(t Type, name string) *Signature {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok {
		return nil
	}
	switch u := under(n).(type) {
	case *Interface:
		if m := u.method(name); m != nil {
			return m.Sig
		}
		return nil
	case *Struct:
		if f := n.method(name); f != nil {
			return declaredMethodSig(f)
		}
		if fld, _ := u.field(name); fld != nil {
			sig, _ := under0(fld.Type).(*Signature)
			return sig
		}
	}
	if f := n.method(name); f != nil {
		return declaredMethodSig(f)
	}
	return nil
}

// declaredMethodSig is the signature a call x.f(…) of the method f checks
// its arguments against: a package function on x's type without the first
// parameter, which x fills (196m).
func declaredMethodSig(f *Func) *Signature {
	if f.owner != nil && f.Sig != nil && len(f.Sig.Params) > 0 {
		return dropSelf(f.Sig)
	}
	return f.Sig
}

// closedRows reports which rows (errors, capabilities) of a parameter of
// function type are closed: written in the signature with no type
// parameter, so a literal passed there takes the row from it.
func closedRows(pt Type) [2]bool {
	ps, ok := under0(pt).(*Signature)
	if !ok {
		return [2]bool{}
	}
	closed := func(r *Row) bool {
		if r == nil {
			return false
		}
		for _, e := range r.Elems() {
			if _, ok := e.(*TypeParam); ok {
				return false
			}
		}
		return true
	}
	return [2]bool{closed(ps.Errors), closed(ps.Uses)}
}

func (w *refWalker) local(name string) bool { return w.lookupLocal(name) != nil }

// operandScope resolves a with operand's name lexically in the walk's
// scopes, as checker.operandScope does in typing's (context.go).
func (w *refWalker) operandScope(name string) operandScope {
	if b := w.lookupLocal(name); b != nil {
		if b.row {
			return operandRow
		}
		return operandValue
	}
	if w.tparams[name] {
		return operandParam
	}
	return operandPackage
}

// lookupLocal returns the innermost local named name, or nil.
func (w *refWalker) lookupLocal(name string) *localBinding {
	for i := len(w.scopes) - 1; i >= 0; i-- {
		if b, ok := w.scopes[i][name]; ok {
			return b
		}
	}
	return nil
}

// typ visits a type expression: no identifier in it is a value.
func (w *refWalker) typ(n syntax.Node) {
	if n == nil {
		return
	}
	syntax.Inspect(n, func(x syntax.Node) bool {
		if id, ok := x.(*syntax.Ident); ok {
			w.name(id)
		} else {
			w.visit(x)
		}
		return true
	})
}

// name visits an identifier in a type or context position: not a value
// reference.
func (w *refWalker) name(id *syntax.Ident) {
	w.visit(id)
	if w.nameIdents != nil {
		w.nameIdents[id] = true
	}
}

func (w *refWalker) exprs(list []syntax.Expr) {
	for _, x := range list {
		w.expr(x)
	}
}

func (w *refWalker) expr(x syntax.Expr) {
	if x == nil {
		return
	}
	w.visit(x)
	switch x := x.(type) {
	case *syntax.Ident:
		w.ident(x)
	case *syntax.SelectorExpr:
		w.expr(x.X)
		w.visit(x.Sel)
	case *syntax.CompositeLit:
		w.compositeLit(x, litType{})
	case *syntax.FuncLit:
		f := valueFrame{mode: [2]frameMode{frameValue, frameValue}, at: x.Pos(), via: "the function literal"}
		if w.callees[x] {
			f.mode = [2]frameMode{framePass, framePass}
		}
		closed := w.argRows[x]
		if writesErrors(x.Type) || closed[0] {
			f.mode[0] = frameStop
		}
		if x.Type.Needs != nil || closed[1] {
			f.mode[1] = frameStop
		}
		w.inFrame(x, func() {
			w.frame(f, func() {
				w.push()
				w.params(x.Type, nil)
				w.visit(x.Body)
				w.list(x.Body.List)
				w.pop()
			})
		})
	case *syntax.CallExpr:
		if id, ok := x.Fun.(*syntax.Ident); ok && id.Name == "with" {
			// The with construct: its first argument is a context literal, a
			// named context, or a value (a context value; context.go withArg). A
			// Lexical bindings precede the package's context namespace, by
			// the same resolver as withArg, including parenthesised names.
			w.visit(id)
			for _, a := range x.Args {
				// Every operand of `with A, B { … }` is one (rows.md, With 7).
				if x.Trailing == nil {
					// Without a block it is an error, and the arguments are
					// checked as values (context.go with, silentArgs).
					w.expr(a)
					continue
				}
				if wid, ok := unparen(a).(*syntax.Ident); ok && w.c.withNamedContext(wid.Name, w.operandScope(wid.Name)) != nil {
					for p, ok := a.(*syntax.ParenExpr); ok; p, ok = p.X.(*syntax.ParenExpr) {
						w.visit(p)
					}
					w.name(wid)
					continue
				}
				w.expr(a)
			}
			if x.Trailing != nil {
				w.inFrame(x.Trailing, func() { w.block(x.Trailing) })
			}
		} else {
			w.callee(x.Fun)
			w.expr(x.Fun)
			w.selfCallee(x.Fun)
			sig := w.calleeSig(x.Fun)
			// Whether typing checks the literals and the block passed here
			// (it does not when the callee is invalid, call.go silentArgs)
			// is typing's verdict: each is a callback frame (inFrame), and
			// a class G use inside it counts unless typing recorded it invalid-diagnosed
			// (checker.liveUses; round 8 review M2).
			for i, a := range x.Args {
				if lit, ok := unparen(a).(*syntax.FuncLit); ok && sig != nil {
					w.argRows[lit] = closedRows(paramType(sig, i))
				}
				w.expr(a)
			}
			if x.Trailing != nil {
				f := valueFrame{mode: [2]frameMode{frameValue, frameValue}, at: x.Trailing.Pos(), via: "the trailing block"}
				if sig != nil {
					closed := closedRows(paramType(sig, len(x.Args)))
					for k := range closed {
						if closed[k] {
							f.mode[k] = frameStop
						}
					}
				}
				w.inFrame(x.Trailing, func() {
					w.frame(f, func() { w.block(x.Trailing) })
				})
			}
		}
	case *syntax.CatchExpr:
		w.expr(x.X)
		for _, arm := range x.Arms {
			w.visit(arm)
			w.push()
			for _, t := range arm.Types {
				w.catchPattern(t)
			}
			w.list(arm.Body)
			w.pop()
		}
	case *syntax.ContextLit:
		// A header names capabilities: no value reference.
		if x.Provides != nil {
			w.typ(x.Provides)
		}
		if x.Needs != nil {
			w.typ(x.Needs)
		}
		w.contextBody(x.Body)
	case *syntax.ParenExpr:
		w.expr(x.X)
	case *syntax.IndexExpr:
		w.expr(x.X)
		w.exprs(x.Indices)
	case *syntax.SliceExpr:
		w.expr(x.X)
		w.expr(x.Low)
		w.expr(x.High)
		w.expr(x.Max)
	case *syntax.TypeGuard:
		w.expr(x.X)
	case *syntax.StarExpr:
		w.expr(x.X)
	case *syntax.UnaryExpr:
		w.expr(x.X)
	case *syntax.BinaryExpr:
		w.expr(x.X)
		w.expr(x.Y)
	case *syntax.TryExpr:
		w.expr(x.X)
	case *syntax.TupleExpr:
		w.exprs(x.Elts)
	case *syntax.KeyValueExpr:
		w.expr(x.Key)
		w.expr(x.Value)
	case *syntax.InterpolatedString:
		for _, t := range x.Texts {
			w.visit(t)
		}
		w.exprs(x.Exprs)
	case *syntax.BasicLit:
	case *syntax.IfStmt:
		// An if or match in value position (D-C2) holds value references
		// as its statement form does.
		w.stmt(x)
	case *syntax.SwitchStmt:
		w.stmt(x)
	case *syntax.TypeSwitchStmt:
		w.stmt(x)
	default:
		// A type in a value position (a conversion's type, an array or
		// map type, a function type): no value reference.
		w.typ(x)
	}
}

// contextBody visits a context literal's entries: a key is a capability name or
// an embedded context, a value is an expression.
func (w *refWalker) contextBody(b *syntax.ContextBody) {
	if b == nil {
		return
	}
	w.visit(b)
	for _, e := range b.Entries {
		w.visit(e)
		w.typ(e.Key)
		for _, k := range e.Keys {
			w.typ(k)
		}
		w.expr(e.Value)
	}
}

// writesErrors reports whether a function type writes its error row.
func writesErrors(ft *syntax.FuncType) bool {
	if ft == nil || ft.Results == nil {
		return false
	}
	for _, r := range ft.Results.List {
		if _, ok := r.Type.(*syntax.ErrorRow); ok {
			return true
		}
	}
	return false
}

// block visits a trailing block or a go block: a frame with no parameter.
func (w *refWalker) block(b *syntax.BlockStmt) {
	if b == nil {
		return
	}
	w.visit(b)
	w.push()
	w.list(b.List)
	w.pop()
}

func (w *refWalker) list(list []syntax.Stmt) {
	for _, s := range list {
		w.stmt(s)
	}
}

func (w *refWalker) stmt(s syntax.Stmt) {
	if s == nil {
		return
	}
	w.visit(s)
	switch s := s.(type) {
	case *syntax.DeclStmt:
		w.visit(s.Decl)
		for _, spec := range s.Decl.Specs {
			w.visit(spec)
			w.typ(spec.Type)
			w.exprs(spec.Values)
			if spec.Pattern != nil {
				w.pattern(spec.Pattern, true)
			}
			if spec.Pattern == nil && len(spec.Names) == 1 && w.namePattern(spec.Names[0]) {
				// The single name tests, as in typing's varDecl: a pattern.
				w.pattern(spec.Names[0], true)
				continue
			}
			for i, n := range spec.Names {
				w.visit(n)
				b := &localBinding{rt: w.writtenType(spec.Type)}
				if spec.Type == nil && len(spec.Values) == len(spec.Names) {
					b = w.initBinding(spec.Values[i])
				}
				w.binder(n, b)
			}
		}
	case *syntax.AssertStmt:
		w.expr(s.X)
	case *syntax.ExprStmt:
		w.expr(s.X)
	case *syntax.AssignStmt:
		w.exprs(s.Rhs)
		w.exprs(s.Lhs)
	case *syntax.ReturnStmt:
		w.exprs(s.Results)
	case *syntax.BranchStmt:
	case *syntax.BlockStmt:
		w.push()
		w.list(s.List)
		w.pop()
	case *syntax.IfStmt:
		w.push()
		w.expr(s.Cond)
		w.stmt(s.Body)
		w.stmt(s.Else)
		w.pop()
	case *syntax.ForStmt:
		w.push()
		w.expr(s.Cond)
		w.stmt(s.Body)
		w.pop()
	case *syntax.RangeStmt:
		w.push()
		w.expr(s.X)
		for _, l := range []syntax.Expr{s.Key, s.Value} {
			if l == nil {
				continue
			}
			if id, ok := l.(*syntax.Ident); ok && !w.namePattern(id) {
				// As typing's bindLoop: a single name that does not test.
				w.visit(id)
				w.binder(id, &localBinding{})
				continue
			}
			w.pattern(l, true)
		}
		w.stmt(s.Body)
		w.pop()
	case *syntax.SwitchStmt:
		w.push()
		w.expr(s.Tag)
		for _, cl := range s.Clauses {
			w.visit(cl)
			w.exprs(cl.List)
			w.push()
			w.list(cl.Body)
			w.pop()
		}
		w.pop()
	case *syntax.TypeSwitchStmt:
		w.push()
		for _, g := range s.Subjects {
			w.expr(g)
		}
		for _, cl := range s.Clauses {
			w.visit(cl)
			// A case's patterns bind their names in its arm (196p): a
			// read of one there is a local, never a package object.
			w.push()
			for _, p := range cl.List {
				w.pattern(p, false)
			}
			w.list(cl.Body)
			w.pop()
		}
		w.pop()
	}
}
