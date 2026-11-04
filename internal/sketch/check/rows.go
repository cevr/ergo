package check

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// target is a failure target and a capability boundary: a function body, a
// function literal, a trailing block, a go block, a named context's build or a
// package var's initializer (rows.md, Error rows 4). A with block is not a
// target: it discharges capabilities inside the target that holds it.
type target struct {
	name string // for messages: "function f", "this literal", "context W"
	// Declared rows; nil means the row is inferred from the body
	// (rows-and-constraints §7 amendment 1).
	declErrors, declUses *Row
	errors, uses         *Row // what escapes or is charged (inferred rows)
	// scope is what the with frames around the body being checked bind
	// (withScope): a charge reads it once, whatever their number (#229).
	scope withScope
	// results are the value results a return gives; resultKnown is false
	// for a block whose result is its last expression's type.
	results     []Type
	resultKnown bool
	isFunc      bool // return is allowed (a function or a func literal)
	// law marks a law's own body: lawResult, not assignment to results,
	// admits the value a return gives (laws.go, rule 8). A function
	// literal inside the law has a target of its own, without it.
	law     bool
	main    bool
	context bool // a named context's build: closed (README Gap 6)
	// internal is what a named context binds itself: its providers may use it
	// (rows.md, Context 4); charged collects the internal capabilities the
	// entry being checked uses, for the context's entry-cycle check (M14).
	internal *capSet
	charged  *Row
	// tries counts the try expressions being checked in this body: inside
	// one, every fallible value propagates (196r sixth round: try covers
	// the whole expression to its right); tried records that one did.
	tries int
	tried bool
}

func (t *target) row(declared, inferred *Row) *Row {
	if declared != nil {
		return declared
	}
	return inferred
}

// charge records that the body charges capability cap at n: a with of this
// target discharges it; otherwise it must be in the declared row (S102), or
// it joins the inferred row (rows.md, Capability rows 2 and 3). The with
// that discharges it records it with its providing entry, which the
// membership test found (withFrame.discharge, With.Discharged).
func (c *checker) charge(n syntax.Node, cap Type) {
	if c.fn == nil || c.fn.t == nil {
		return
	}
	t := c.fn.t
	if cap == Type(invalid) {
		// An unknown capability row (Row.unknown): no with is known to bind
		// what it holds, an inferred row takes invalid and so is unknown
		// too, and a written row is not known to miss it (stage D1 round 4
		// review M1).
		if t.declUses == nil && !t.context {
			t.uses = t.uses.with(c.types, invalid)
		}
		return
	}
	// The innermost with that binds cap discharges it. An unknown scope
	// inside it is a with whose context is unknown (reported): it
	// discharges everything, so no charge cascades from it (D3; M18).
	if f, unknown := t.scope.binder(c.types, cap); unknown {
		return
	} else if f != nil {
		_, node := f.binds.find(c.types, cap)
		f.discharge(cap, node)
		return
	}
	if t.context && t.internal.has(c.types, cap) {
		t.charged = t.charged.with(c.types, cap)
		return
	}
	if t.declUses == nil {
		t.uses = t.uses.with(c.types, cap)
		return
	}
	if t.declUses.has(c.types, cap) || t.declUses.unknown(c.types) {
		// An unknown written row (a refused entry, rowExpr) may hold cap.
		return
	}
	switch {
	case t.context:
		c.errorf(n, CodeMissingCap, "%s requires %s while it is built; a named context is closed: its providers require no capability from the with site (README Gap 6)", t.name, cap)
	default:
		c.errorf(n, CodeMissingCap, "%s requires %s, which is not in the capability row of %s and no with provides it", describeCharge(n), cap, t.name)
	}
}

func describeCharge(n syntax.Node) string {
	switch n.(type) {
	case *syntax.CallExpr:
		return "this call"
	}
	return "this expression"
}

// escape records failures that escape to the current target at n (try, a
// fallible return, a return of an error value, the rest of a partial catch):
// each must be in the declared error row (S101), or joins the inferred row.
// An inferred row takes invalid too, so it is unknown where the escaping row
// is; a declared row is checked against the known part (Row.unknown).
func (c *checker) escape(n syntax.Node, row *Row) {
	if row.Empty() || c.fn == nil || c.fn.t == nil {
		return
	}
	t := c.fn.t
	if t.declErrors == nil {
		t.errors = t.errors.union(c.types, row)
		return
	}
	// A written row with a refused entry is unknown (rowExpr): it may hold
	// what escapes, so nothing is reported against it, as an unknown
	// destination row accepts any row (assignability clause 5). A literal or
	// a block whose expected row is unknown infers its row instead (funcLit,
	// blockLit; stage D1 round 4 review M2).
	if t.declErrors.unknown(c.types) {
		return
	}
	extra := row.minus(c.types, t.declErrors).known(c.types)
	if extra.Empty() {
		return
	}
	switch {
	case t.context:
		c.errorf(n, CodeUndeclared, "%s can fail with %s while it is built; a named context is closed: no failure escapes its build (README Gap 6)", t.name, rowText(extra))
	default:
		c.errorf(n, CodeUndeclared, "%s escapes %s, whose error row %s does not list it", rowText(extra), t.name, rowText(t.declErrors))
	}
}

func (c *checker) chargeRow(n syntax.Node, row *Row) {
	for _, e := range row.Elems() {
		c.charge(n, e)
	}
}

// ---------------------------------------------------------------- bodies in SCC order

type bodyNode struct {
	fn    *Func
	v     *pkgVar
	edges []*bodyNode
	// Tarjan state
	index, low int
	onStack    bool
	// The worklist order (worklist.go): the body's syntax nodes, its
	// postorder index, its slot in the queue (-1 when not queued) and
	// whether it is on the current walk.
	size, post, slot int
	active           bool
}

// owner is the node's body: its *Func or its *pkgVar.
func (n *bodyNode) owner() any {
	if n.fn != nil {
		return n.fn
	}
	return n.v
}

// file is the file of the node's declaration.
func (n *bodyNode) file() *fileInfo {
	if n.fn != nil {
		return n.fn.file
	}
	return n.v.file
}

// checkBodies checks every body, one strongly connected component of the
// package's reference graph at a time, in reverse topological order: a
// callee's inferred rows are final before its callers are checked. Inside a
// cycle the rows are joined to a fixpoint (finite sets ordered by
// inclusion), silently, then the component is checked once more with
// diagnostics. An acyclic graph is linear: each body is checked once.
//
// The graph's edges are the references that row inference needs: to a
// function whose rows are inferred and to a package var. They are the
// plain identifiers of each body resolved through its lexical scopes by a
// walk that runs before typing and visits every node (refs.go, class H):
// a local, a parameter or a receiver that shadows a package function adds
// no edge (round 14 M1), and a tentative type error hides none (round 15
// M1). A method or a function that spells its
// rows is read by its signature, so it adds no edge: one edge per
// referenced object, never one per same-named method (round-3 M5). A
// call x.f(…) of a 196m function adds one edge, to the function typing
// resolves it to (selfcalls.go; round 6 review M1).
func (c *checker) checkBodies() {
	var nodes []*bodyNode
	byFunc := map[*Func]*bodyNode{}
	byVar := map[*pkgVar]*bodyNode{}
	for _, f := range c.funcs {
		if f.Decl.Body == nil || f.Sig == nil {
			continue
		}
		n := &bodyNode{fn: f, index: -1, slot: -1, size: f.bodySize()}
		nodes = append(nodes, n)
		byFunc[f] = n
	}
	for _, v := range c.vars {
		n := &bodyNode{v: v, index: -1, slot: -1, size: v.bodySize()}
		nodes = append(nodes, n)
		byVar[v] = n
	}
	// Class H: the edges come from the lexical walk of every body, before
	// any body is typed (refs.go).
	c.refs = make(map[any]map[any]bool, len(nodes))
	c.valueRefs = make(map[any]map[*Func][2]useSite, len(nodes))
	c.guardedRefs = map[any][]guardedUse{}
	pending := map[any][]pendingSelf{}
	for _, n := range nodes {
		var w *refWalker
		if n.fn != nil {
			w = c.bodyRefs(n.fn)
		} else {
			w = c.varRefs(n.v)
		}
		owner := n.owner()
		c.refs[owner], c.valueRefs[owner] = w.refs, w.values
		if len(w.guarded) > 0 {
			c.guardedRefs[owner] = w.guarded
		}
		if len(w.pending) > 0 {
			pending[owner] = w.pending
		}
		c.walkOps += w.ops
	}
	c.bodyNodes = len(nodes)
	c.resolveSelfCalls(nodes, byFunc, byVar, pending)
	c.discoverInit()
	c.bodyEdges = c.linkBodies(nodes, byFunc, byVar)
	c.refs = nil
	for _, scc := range tarjan(nodes) {
		c.checkComponent(scc)
	}
	c.valueRefs, c.guardedRefs = nil, nil
	// Instantiation cycles through methods alone close outside any cyclic
	// component (methods add no edge); the reporting pass recorded them.
	c.checkInstantiationCycles()
	c.checkInitCycles()
	c.recordInitOrder()
}

// discoverInit finds the package's initialization cycles before any body
// is checked with diagnostics (round-2 M2, round-3 M5). Each body's
// dependencies start from its lexical references (c.refs, class H), so no
// function or var dependency depends on typing; it then checks, silently,
// each package var's initializer and every function and method those
// reach, each once, which adds the references that need types (a method
// value or call, round-3 M5): the dependency closure of Go's
// initialization order, linear in its size. A cycle through a var is S108
// and its vars become invalid, so no row diagnostic follows from them. The
// silent checks leave no row behind: the inferred rows restart empty.
func (c *checker) discoverInit() {
	if len(c.vars) == 0 {
		return
	}
	if c.init.deps == nil {
		c.init.deps = map[any]map[any]bool{}
	}
	for owner, refs := range c.refs {
		m := c.init.deps[owner]
		if m == nil {
			m = map[any]bool{}
			c.init.deps[owner] = m
		}
		for o := range refs {
			m[o] = true
		}
	}
	c.silent++
	c.init.tentative = true
	visited := map[*Func]bool{}
	var queue []*Func
	enqueue := func(owner any) {
		for _, d := range sortedDeps(c.init.deps[owner]) {
			if f, ok := d.(*Func); ok && !visited[f] && f.Decl != nil && f.Decl.Body != nil && f.Sig != nil {
				visited[f] = true
				queue = append(queue, f)
			}
		}
	}
	for _, pv := range c.vars {
		c.checkPkgVar(pv)
		enqueue(pv)
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		c.checkFuncBody(f)
		enqueue(f)
	}
	c.init.tentative = false
	c.silent--
	for _, f := range c.funcs {
		if f.Sig == nil {
			continue
		}
		f.resetRows()
	}
	c.newSolutions()
	c.checkInitCycles()
}

func (c *checker) checkComponent(scc []*bodyNode) {
	// The group whose rows are being inferred (class G, pinGroup).
	c.group = map[*Func]bool{}
	for _, n := range scc {
		if n.fn != nil {
			c.group[n.fn] = true
		}
	}
	c.groupValue, c.grew, c.pinKept = map[*Func][2]groupSite{}, map[*Func][2]bool{}, map[any][]guardedUse{}
	defer func() { c.group, c.groupValue, c.grew, c.pinKept = nil, nil, nil, nil }()
	cyclic := len(scc) > 1
	if !cyclic {
		for _, e := range scc[0].edges {
			if e == scc[0] {
				cyclic = true
			}
		}
	}
	inferred := false
	for _, n := range scc {
		if n.fn != nil && (n.fn.inferErrors || n.fn.inferUses) {
			inferred = true
		}
	}
	if cyclic {
		// One silent pass records the component's instantiations, so an
		// expanding cycle is rejected before the rows iterate (round-2 M1).
		// It is also typing's record of the callback frames class G reads
		// (pinGroup, liveUses; round 8 review M2): each member is typed from
		// empty rows, its own rows reset after its check as in the typing of
		// waiting calls (resolveSelfCalls), so the record does not depend on
		// the members' order. It may depend on a row still being inferred:
		// a type built from such a row is provisional (rowsOpen; catchType),
		// and a frame typing skips under it is provisional, not invalid, so
		// it keeps its class G uses and the pin is conservative
		// (provisional.go; round 9 review M1). Only a frame invalid for a
		// diagnosed reason, which holds whatever the rows, drops its uses.
		c.silent++
		c.rowsOpen++
		for _, n := range scc {
			c.checkNode(n)
			if n.fn != nil && n.fn.Sig != nil {
				n.fn.resetRows()
			}
		}
		c.rowsOpen--
		c.silent--
		c.checkInstantiationCycles()
		// The fixpoint starts from empty rows (least fixpoint): the silent
		// pass ran before the cycle's instantiations were rejected.
		for _, n := range scc {
			if n.fn == nil || n.fn.Sig == nil {
				continue
			}
			n.fn.resetRows()
		}
		c.newSolutions()
		c.pinGroup(scc)
	}
	if cyclic && inferred {
		// A refused component is reported as is: its rows are not settled.
		settled := c.converge(scc)
		for _, n := range scc {
			if settled && n.fn != nil {
				c.settled[n.fn] = true
			}
		}
	}
	for _, n := range scc {
		c.checkNode(n)
	}
	c.reportGroup(scc)
	for _, n := range scc {
		if n.fn != nil {
			delete(c.settled, n.fn)
			// A refused member stays refused for the components checked
			// after this one (groupRef), with the rows its body exceeded:
			// only those are unknown. A written row, or an inferred row
			// whose pinned empty row held, is evidence (stage D1 round 4
			// review M1).
			if c.refused(n.fn) {
				c.refusedMembers[n.fn] = c.exceeded(n.fn)
			}
		}
	}
	c.finalComponent(scc)
}

// litFrame is a function literal, trailing block or go expression being
// typed in the reporting pass: the refused group members it refers to make
// it invalid, so nothing cascades from their pinned rows (D3). Which uses
// are value uses is decided before typing (refWalker, class H).
type litFrame struct {
	refs []*Func
}

// pushLit starts a literal frame; endLit ends it.
func (c *checker) pushLit() *litFrame {
	l := &litFrame{}
	c.lits = append(c.lits, l)
	return l
}

// endLit ends the innermost literal frame, l, and reports whether it refers
// to a refused member.
func (c *checker) endLit(l *litFrame) bool {
	c.lits = c.lits[:len(c.lits)-1]
	for _, f := range l.refs {
		if c.refused(f) {
			return true
		}
	}
	return false
}

// pinGroup applies class G to a cyclic component (rows.md, Capability rows
// 6): a member used as a value inside its own group has the rows it
// writes, and each row it does not write is pinned empty, as for an
// exported function. The value uses come from the lexical walk of the
// component's bodies (refWalker.values, class H): a use that is not a call
// is a value use of both rows, and a function literal, trailing block or
// go task that is a value is a value use of each row of the members it
// calls that its own row would carry (not written, not taken from a closed
// parameter type, not called on the spot). Typing decides only which frames drop their uses:
// a use inside a callback frame that the silent pass recorded
// invalid-diagnosed, skipped for a reason that holds whatever the rows,
// is no use (liveUses); a checked or provisional frame keeps its uses
// (provisional.go), and reportGroup drops the uses the reporting pass
// finds invalid-diagnosed (reportedUses). A var on an initialization cycle is invalid, so its
// initializer adds no value use (D3). So the pin does not depend on a row
// still being inferred, call admission does not depend on the rows, and
// the rows grow monotonically to their least fixpoint. When the body of a pinned
// member needs more than the empty row, the member's declaration is S108
// (reportGroup), never a chosen fixpoint.
//
// Prior art: OCaml's let rec is monomorphic inside its group; Haskell needs
// a signature for polymorphic recursion; TypeScript reports a name
// "referenced directly or indirectly in its own initializer"; Go and Rust
// require every signature. Each refuses the circular case instead of
// choosing a fixpoint.
func (c *checker) pinGroup(scc []*bodyNode) {
	c.groupSites(scc, c.groupValue, func(owner any) map[*Func][2]useSite {
		uses, kept := c.liveUses(owner)
		c.pinKept[owner] = kept
		return uses
	})
	var pin []*RowVar
	for _, n := range scc {
		if n.fn == nil {
			continue
		}
		uses := c.groupValue[n.fn]
		for k, v := range []*RowVar{n.fn.errVar, n.fn.useVar} {
			if v != nil && !v.final && uses[k].ok {
				pin = append(pin, v)
			}
		}
	}
	if len(pin) > 0 {
		c.types.final(pin)
		c.newSolutions()
	}
}

// groupSites adds to sites, for each member of the component, the least
// class G use of each row (groupSite.before) among the uses usesOf gives
// for each body of scc. A var on an initialization cycle adds none (D3).
func (c *checker) groupSites(scc []*bodyNode, sites map[*Func][2]groupSite, usesOf func(owner any) map[*Func][2]useSite) {
	for _, n := range scc {
		site := groupSite{file: n.file()}
		var owner any = n.fn
		if n.v != nil {
			if n.v.initCycle {
				continue
			}
			owner = n.v
			site.key, site.anchor = "let "+n.v.obj.Name, n.v.obj.Pos
			if n.v.spec != nil {
				site.anchor = n.v.spec.Pos()
			}
		} else {
			site.key, site.anchor = "func "+funcKey(n.fn), n.fn.Decl.Pos()
		}
		for f, kinds := range usesOf(owner) {
			if !c.group[f] {
				continue
			}
			uses := sites[f]
			for k, u := range kinds {
				if !u.ok {
					continue
				}
				s := site
				s.useSite = u
				if !uses[k].ok || s.before(uses[k]) {
					uses[k] = s
				}
			}
			sites[f] = uses
		}
	}
}

// A groupSite is a class G value use inside a component: the use (where,
// and through which literal), the body it is in (key: its kind and name)
// and that body's declaration position (anchor). The use reported for a
// member is the least by (key, offset from the anchor), which moves with
// its declaration, so the choice does not depend on declaration order.
type groupSite struct {
	useSite
	key    string
	anchor syntax.Pos
	file   *fileInfo
}

func (s groupSite) before(t groupSite) bool {
	if s.key != t.key {
		return s.key < t.key
	}
	return s.at-s.anchor < t.at-t.anchor
}

// funcKey names a function or method for groupSite.
func funcKey(f *Func) string {
	return f.Name
}

// groupRef handles a reference to f while f's component is checked: every
// enclosing literal frame takes f, and the reference is refused when f is
// (refused): in the reporting pass, every reference to a member whose
// pinned row its body exceeds is invalid, so nothing cascades from it
// (D3); the S108 is at the member's declaration (reportGroup). A reference
// from outside the component, checked after it settled, keeps f's written
// signature, so an arity or argument mistake there is still reported; only
// only the pinned rows its body exceeded are no evidence, so when f is
// refused each of those rows is unknown in the reference: it holds invalid
// (unknownSig), and every row that takes it, through bindings, literals,
// generics and inference, is unknown too, so no definite-row check cascades
// from it (classf/go-statement.ergo's caller F got a cascade "returns no
// value"; stage D1 round 1 review M5; round 3 review M1, M2; rows.md,
// Inference 6). Only an unknown row holds invalid: a written row, and an
// inferred row whose pinned empty row held, stay as they are (round 4
// review M1).
func (c *checker) groupRef(f *Func) (refused bool, unknown [2]bool) {
	if !f.inferErrors && !f.inferUses {
		return false, unknown
	}
	if !c.group[f] {
		// The silent passes read it too, so the fixpoint and the
		// reporting pass see one row (unknownSig).
		return false, c.refusedMembers[f]
	}
	for _, l := range c.lits {
		if !slices.Contains(l.refs, f) {
			l.refs = append(l.refs, f)
		}
	}
	if c.refused(f) {
		// An undiagnosed value: its S108 is decided last (reportGroup).
		c.readUndiagnosed()
		return true, unknown
	}
	return false, unknown
}

// unknownSig is the signature a reference to a refused class G member from a
// later component has: its written signature, with invalid in each row its
// body exceeded (rows: errors, capabilities; Row.unknown), so every row that
// takes it is unknown and no definite-row check cascades from it. The
// member's own row variables are not changed. It is sig itself when no row
// is unknown.
func (c *checker) unknownSig(sig *Signature, rows [2]bool) *Signature {
	if !rows[0] && !rows[1] {
		return sig
	}
	s := *sig
	if rows[0] {
		s.Errors = sig.Errors.with(c.types, invalid)
	}
	if rows[1] {
		s.Uses = sig.Uses.with(c.types, invalid)
	}
	return &s
}

// exceeded returns which of f's pinned rows (errors, capabilities) its body
// exceeds. The fixpoint decides it; only the reporting pass reads it.
func (c *checker) exceeded(f *Func) [2]bool {
	grew, uses := c.grew[f], c.groupValue[f]
	return [2]bool{grew[0] && uses[0].ok, grew[1] && uses[1].ok}
}

// refused reports, in the reporting pass, whether f is refused by class G.
func (c *checker) refused(f *Func) bool {
	if c.silent > 0 {
		return false
	}
	ex := c.exceeded(f)
	return ex[0] || ex[1]
}

// reportGroup reports the class G S108 of each refused member of a
// component at its declaration, where the row is written: one diagnostic
// per function, at a position that does not depend on the order of the
// declarations (round 16). The message names the value use (groupSite),
// or the literal, trailing block or go task that caused it, and the row to
// write (round 17 m2).
//
// The uses are the pin's, filtered by the reporting pass (reportedUses;
// round 10 ruling): a use whose frame the reporting pass skipped as
// invalid-diagnosed is dropped, since that frame already has its own
// diagnostic and a refusal never cascades. A member with no use left on an
// exceeded row has no S108 for it, and none at all when no exceeded row
// keeps a use. Its references stay invalid, so nothing cascades from the
// pinned row either.
func (c *checker) reportGroup(scc []*bodyNode) {
	var sites map[*Func][2]groupSite
	for _, n := range scc {
		f := n.fn
		if f == nil || !c.refused(f) {
			continue
		}
		if sites == nil {
			sites = map[*Func][2]groupSite{}
			c.groupSites(scc, sites, c.reportedUses)
		}
		ex := c.exceeded(f)
		for k := range ex {
			ex[k] = ex[k] && sites[f][k].ok
		}
		if !ex[0] && !ex[1] {
			continue
		}
		params := "()"
		if ps := f.Decl.Type.Params; ps != nil && len(ps.List) > 0 {
			params = "(…)"
		}
		// The fix names the result, as the decided signature writes it
		// (spec D-A7): func b(…) T fails ….
		if f.Sig != nil && len(f.Sig.Results) == 1 {
			params += " " + c.localTypeText(f.Sig.Results[0])
		} else if f.Sig != nil && len(f.Sig.Results) > 1 {
			params += " (…)"
		}
		var row string
		site := sites[f][0]
		switch {
		case ex[0] && ex[1]:
			row = fmt.Sprintf("write its error row and capability row: func %s%s fails … needs …", f.Name, params)
		case ex[0]:
			row = fmt.Sprintf("write its error row: func %s%s fails …", f.Name, params)
		default:
			row = fmt.Sprintf("write its capability row: func %s%s needs …", f.Name, params)
			site = sites[f][1]
		}
		line, col := site.file.syntax.Position(site.at)
		use := fmt.Sprintf("at %d:%d", line, col)
		if site.via != "" {
			use = fmt.Sprintf("through %s at %d:%d", site.via, line, col)
		}
		saved := c.cur
		c.cur = f.file
		c.errorf(f.Decl.Name, CodeType, "%s is used as a value %s inside its own recursive group; %s", f.Name, use, row)
		c.cur = saved
	}
}

// finalComponent binds the row variables of a component for good and
// discards its inference context (intern.go): the checker's instances and
// switch subjects whose ids depended on an unbound variable.
func (c *checker) finalComponent(scc []*bodyNode) {
	var vars []*RowVar
	for _, n := range scc {
		if n.fn == nil {
			continue
		}
		for _, v := range []*RowVar{n.fn.errVar, n.fn.useVar} {
			if v != nil && !v.final {
				vars = append(vars, v)
			}
		}
	}
	if len(vars) == 0 {
		return
	}
	c.types.final(vars)
	c.newSolutions()
}

// newSolutions discards the inference context (intern.go newSolutions)
// and the checker's instances and switch subjects in it.
func (c *checker) newSolutions() {
	c.types.newSolutions()
	clear(c.inferInstances)
	clear(c.inferSubjects)
}

// refuseRows reports the functions of a component whose inferred rows did
// not reach a fixpoint within the bound (S108: soundness by refusal).
func (c *checker) refuseRows(scc []*bodyNode) {
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, n := range scc {
		f := n.fn
		if f == nil || !f.inferErrors && !f.inferUses {
			continue
		}
		which := "error row"
		if !f.inferErrors {
			which = "capability row"
		}
		c.cur = f.file // a component may span files (E2 round 3)
		c.errorf(f.Decl.Name, CodeType, "cannot infer the %s of %s; write it", which, f.Name)
	}
}

// resetRows empties the solutions of a function's row variables that are
// not final (a least fixpoint starts from empty rows).
func (f *Func) resetRows() {
	for _, v := range []*RowVar{f.errVar, f.useVar} {
		if v != nil && !v.final {
			v.sol = nil
		}
	}
}

// converge joins the inferred rows of a cyclic component to their fixpoint,
// silently, with the worklist settling shares (worklist.go: callees first
// on demand, the smallest body first among the roots; round 8 review M1
// and O1): a body is checked again only when a body it references grew.
// Rows are finite sets that only grow (a body's
// rows are monotone in its callees' rows), so the worklist empties; no row
// is published to another component before it has converged.
// They are monotone because no type inside the group depends on a row still
// being inferred: a member used as a value inside its group, directly or
// through a function literal or go task that is a value, has its unwritten
// rows pinned empty (class G, pinGroup).
//
// The worklist is bounded whatever the rows do: 2·(bodies + edges)·(D + 1)
// checks, where D counts the distinct row elements (kind and interned id)
// the component has produced so far. A monotone solve changes each row at
// most D times and requeues each predecessor once per change, so it fits.
// Only an element seen for the first time raises D; an element removed and
// added again earns nothing, so an oscillating component reaches the bound
// (TestConvergeBoundOscillation). D is finite: the elements are types of
// the check, and expanding instantiation cycles are rejected before the
// solve (checkInstantiationCycles). Past the bound the component is S108
// "cannot infer the error row of f; write it" (refuseRows), never a guess.
// converge is the only iterating solver path of a component: the silent
// pass before it and the reporting pass after it check each body once.
//
// Prior art: Koka and Gleam infer per strongly connected component; the
// worklist is the standard chaotic-iteration solver of dataflow analysis.
func (c *checker) converge(scc []*bodyNode) bool {
	in := make(map[*bodyNode]bool, len(scc))
	for _, n := range scc {
		in[n] = true
	}
	preds := map[*bodyNode][]*bodyNode{}
	edges := 0
	for _, n := range scc {
		for _, e := range n.edges {
			if in[e] {
				preds[e] = append(preds[e], n)
				edges++
			}
		}
	}
	// The bound: (bodies + edges) checks for each distinct row element
	// the component has produced, plus one round. Only an element seen
	// for the first time extends it; one removed and added again earns
	// nothing, so an oscillation ends in the refusal (round 14 O1).
	seen := map[[2]int32]bool{}
	budget := func() int { return 2 * (len(scc) + edges) * (len(seen) + 1) }
	check := c.checkNode
	if c.nodeCheck != nil {
		check = c.nodeCheck // the synthetic oscillation test
	}
	w := &worklist{member: func(n *bodyNode) bool { return in[n] }}
	for _, n := range scc {
		w.push(n)
	}
	c.silent++
	checks := 0
	settled := true
	w.run(func(n *bodyNode) bool {
		if checks >= budget() {
			settled = false
			return false
		}
		checks++
		changed := check(n)
		c.seenRows(n, seen)
		if changed {
			for _, p := range preds[n] {
				w.push(p)
			}
		}
		return true
	})
	c.silent--
	c.convergeChecks += checks
	c.worklistSteps += w.steps
	if !settled {
		c.refuseRows(scc)
	}
	return settled
}

// seenRows adds the elements of n's inferred rows (kind and interned id)
// to seen, the distinct elements a solve has produced: its budget.
func (c *checker) seenRows(n *bodyNode, seen map[[2]int32]bool) {
	if n.fn == nil {
		return
	}
	for k, v := range []*RowVar{n.fn.errVar, n.fn.useVar} {
		if v != nil {
			for _, id := range v.sol.idsOf(c.types) {
				seen[[2]int32{int32(k), id}] = true
			}
		}
	}
}

// checkNode checks one body and reports whether its inferred rows grew.
func (c *checker) checkNode(n *bodyNode) bool {
	if n.v != nil {
		// A var's type carries its literal's inferred rows: when it
		// changes, the bodies that call the var are checked again.
		before := n.v.obj.Type
		c.checkPkgVar(n.v)
		after := n.v.obj.Type
		return before == nil || after == nil || !c.identical(before, after)
	}
	return c.checkFuncBody(n.fn)
}

// tarjan returns the strongly connected components in reverse topological
// order (each component after every component it reaches), iteratively, so
// a 20 000-function chain needs no deep recursion.
func tarjan(nodes []*bodyNode) [][]*bodyNode {
	var out [][]*bodyNode
	var stack []*bodyNode
	index, post := 0, 0
	type frame struct {
		n *bodyNode
		i int
	}
	for _, root := range nodes {
		if root.index >= 0 {
			continue
		}
		work := []frame{{n: root}}
		root.index, root.low = index, index
		index++
		stack = append(stack, root)
		root.onStack = true
		for len(work) > 0 {
			top := &work[len(work)-1]
			if top.i < len(top.n.edges) {
				next := top.n.edges[top.i]
				top.i++
				if next.index < 0 {
					next.index, next.low = index, index
					index++
					stack = append(stack, next)
					next.onStack = true
					work = append(work, frame{n: next})
				} else if next.onStack && next.index < top.n.low {
					top.n.low = next.index
				}
				continue
			}
			n := top.n
			work = work[:len(work)-1]
			n.post = post
			post++
			if len(work) > 0 && n.low < work[len(work)-1].n.low {
				work[len(work)-1].n.low = n.low
			}
			if n.low == n.index {
				var scc []*bodyNode
				for {
					m := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					m.onStack = false
					scc = append(scc, m)
					if m == n {
						break
					}
				}
				out = append(out, scc)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- function bodies

// funcCtx is the state of one top-level body: the current target, the
// lexical scope, the branch targets and the locals to report unused.
type funcCtx struct {
	t        *target
	scope    *Scope
	branches []branch
	locals   []*Var
	fn       *Func
	law      *Law // the law whose body this is (laws.go)
	reach    *reachNode
}

// typeParams are the type parameters the body is generic in: a function's
// own, or a law's type's (laws.go, rule 6). The type context of a body
// (localTypeCtx) and a with operand's name (operandScope) both read them
// here, so the two cannot disagree on what a body is generic in.
func (fc *funcCtx) typeParams() []*TypeParam {
	switch {
	case fc == nil:
		return nil
	case fc.fn != nil && fc.fn.Sig != nil:
		return fc.fn.Sig.TParams
	case fc.law != nil:
		return fc.law.typeParams()
	}
	return nil
}

type branchKind int

const (
	brLoop branchKind = iota
	brSwitch
	brBarrier // a with or trailing block: nothing crosses it
)

type branch struct {
	kind branchKind
}

// bodySize counts the syntax nodes of n: the cost of one check of a body
// (c.workNodes).
func bodySize(n syntax.Node) int {
	count := 0
	syntax.Inspect(n, func(syntax.Node) bool {
		count++
		return true
	})
	return max(count, 1)
}

// bodySize is the syntax nodes of f's declaration, counted once.
func (f *Func) bodySize() int {
	if f.size == 0 {
		f.size = bodySize(f.Decl)
	}
	return f.size
}

// bodySize is the syntax nodes of pv's initializer (its declaration when
// the names and values do not pair), counted once.
func (pv *pkgVar) bodySize() int {
	if pv.size == 0 {
		var root syntax.Node = pv.spec
		if pv.idx < len(pv.spec.Values) && len(pv.spec.Values) == len(pv.spec.Names) {
			root = pv.spec.Values[pv.idx]
		}
		pv.size = bodySize(root)
	}
	return pv.size
}

func (c *checker) checkFuncBody(f *Func) bool {
	c.workNodes += f.bodySize()
	defer c.beginBodyCheck(f)()
	defer c.beginDeps(f)()
	defer c.monoOwner(f.Sig.TParams)()
	saved := c.cur
	c.cur = f.file
	defer func() { c.cur = saved }()
	sig := f.Sig
	d := f.Decl
	name := "function " + f.Name
	t := &target{name: name, results: sig.Results, resultKnown: true, isFunc: true, main: f.Name == "main"}
	if f.inferErrors {
		t.errors = newRow()
	} else {
		t.declErrors = sig.Errors
	}
	if f.inferUses {
		t.uses = newRow()
	} else {
		t.declUses = sig.Uses
	}
	frame := newScope(c.pkg.Scope)
	frame.frame = true
	fc := &funcCtx{t: t, scope: frame, fn: f, reach: c.reachOf(f)}
	prev := c.fn
	c.fn = fc
	defer func() { c.fn = prev }()
	for _, p := range sig.Params {
		if p.Name == "_" {
			continue
		}
		if prev, ok := frame.names[p.Name]; ok {
			if _, isVar := prev.(*Var); isVar && prev.(*Var).param {
				c.errorf(p.id, CodeType, "%s is already a parameter of %s", p.Name, f.Name)
				continue
			}
		}
		refused := c.preludeName(p.id) // D-D3; the parameter still binds
		v := &Var{Name: p.Name, Type: p.Type, Pos: p.Pos, id: p.id, param: true, used: true, refused: refused}
		c.def(p.id, v)
		frame.insert(p.Name, v)
	}
	c.bindRequirements(frame, d.Type)
	c.functionBody(d.Body, name)
	c.reportUnused(fc)
	c.reportNeverReassigned(fc)
	changed := false
	// Inference writes the solved rows into the variables, never into the
	// signature (class F); a final variable keeps its solution. After a
	// component settled, its reporting pass reads the same solutions and
	// must compute no wider row (the invariant c.widened records).
	for _, p := range []struct {
		v   *RowVar
		row *Row
	}{{f.errVar, t.errors}, {f.useVar, t.uses}} {
		v := p.v
		if v == nil {
			continue
		}
		if v.final {
			if c.group[f] && c.silent > 0 {
				// Pinned empty by class G: record whether the body needs more.
				// The fixpoint decides; the reporting pass, which refuses the
				// value uses, only reads it.
				grew := c.grew[f]
				// An unknown row's invalid is no evidence (Row.unknown).
				grew[v.Kind-KindErrors] = !p.row.known(c.types).Empty()
				c.grew[f] = grew
			}
			continue
		}
		if c.settled[f] && c.silent == 0 {
			if !p.row.subsetOf(c.types, v.sol) {
				c.widened = append(c.widened, f.Name)
			}
			continue
		}
		if !p.row.equal(c.types, v.sol) {
			changed = true
			v.sol = p.row
			// Ids resolved through the old solution no longer hold.
			c.newSolutions()
		}
	}
	return changed
}

// bindRequirements enters the written capability-row names of a body into
// its frame (rows-and-constraints §7 amendment 3): a local of one of these
// names in the same frame is S104.
func (c *checker) bindRequirements(frame *Scope, ft *syntax.FuncType) {
	if ft == nil || ft.Needs == nil {
		return
	}
	for _, x := range ft.Needs.Names {
		var bare string
		switch x := x.(type) {
		case *syntax.Ident:
			bare = x.Name
		case *syntax.SelectorExpr:
			bare = x.Sel.Name
		}
		if bare == "" {
			continue
		}
		tn := c.capTypeName(x)
		if tn == nil {
			continue
		}
		frame.insert(bare, &capBinding{tn: tn, at: x.Pos()})
	}
}

// capTypeName finds the type name a capability-row entry denotes, without
// reporting (the signature already reported a bad entry).
func (c *checker) capTypeName(x syntax.Expr) *TypeName {
	switch x := x.(type) {
	case *syntax.Ident:
		o := c.lookupType(x.Name, nil)
		if tn, ok := o.(*TypeName); ok && isCapability(tn.Type) {
			return tn
		}
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok {
			if pn, ok := c.lookupType(id.Name, nil).(*PkgName); ok {
				if tn, ok := pn.Pkg.Scope.names[x.Sel.Name].(*TypeName); ok && isCapability(tn.Type) {
					return tn
				}
			}
		}
	}
	return nil
}

func (c *checker) reportUnused(fc *funcCtx) {
	for _, v := range fc.locals {
		if v.used || v.Name == "_" {
			continue
		}
		if v.group != nil {
			continue
		}
		c.errorf(v.id, CodeUnused, "%s is declared and not used", v.Name)
	}
}

// checkPkgVar checks a package-level let's initializer: a closed context,
// with no row (grammar.md, Declarations 6: package-level let is immutable).
func (c *checker) checkPkgVar(pv *pkgVar) {
	if pv.done || pv.checking {
		return
	}
	if g := pv.group; g != nil && g.members[0] != pv {
		// A let that binds several names from one value is checked once,
		// by its first name, for all of them.
		lead := g.members[0]
		c.checkPkgVar(lead)
		pv.done = lead.done
		if pv.obj.Type == nil || pv.initCycle {
			pv.obj.Type = invalid
		}
		return
	}
	c.workNodes += pv.bodySize()
	pv.checking = true
	defer c.beginBodyCheck(pv)()
	defer func() { pv.checking = false }()
	defer c.beginDeps(pv)()
	// A silent check (a fixpoint round) does not count: the reporting
	// pass checks the initializer again against converged rows.
	if c.silent == 0 {
		pv.done = true
	}
	saved := c.cur
	c.cur = pv.file
	defer func() { c.cur = saved }()
	t := &target{name: "the initializer of " + pv.obj.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
	prev := c.fn
	fc := &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
	c.fn = fc
	defer func() { c.fn = prev }()
	// The locals of the initializer's function literals follow the
	// function rules: unused, and never reassigned (Q3; stage D1 round 1
	// review M4).
	defer func() {
		c.reportUnused(fc)
		c.reportNeverReassigned(fc)
	}()
	if g := pv.group; g != nil {
		c.pkgPatternCheck(g)
		for _, m := range g.members {
			if m.obj.Type == nil || m.initCycle {
				m.obj.Type = invalid
			}
		}
		return
	}
	var declared Type
	if pv.spec.Type != nil {
		declared = c.typExpr(pv.spec.Type, nil)
		if c.taskRefused(pv.spec.Type, declared) {
			declared = invalid
		}
	}
	switch {
	case len(pv.spec.Values) == 0:
		if pv.spec.Type == nil {
			c.untypedLet(pv.spec.Names[pv.idx])
		}
		if declared != nil && !isInvalid(declared) && !defaultable(declared) {
			c.errorf(pv.spec.Names[pv.idx], CodeType, "let %s of type %s needs a value: %s has no zero value (types.md, Zero values 3)", pv.obj.Name, declared, declared)
		}
		pv.obj.Type = declared
	case len(pv.spec.Values) == len(pv.spec.Names):
		op := c.valueExpr(pv.spec.Values[pv.idx], declared)
		if declared != nil {
			c.assign(&op, declared, "let "+pv.obj.Name)
			pv.obj.Type = declared
		} else {
			pv.obj.Type = c.typeOf(&op)
		}
	default:
		c.errorf(pv.spec, CodeType, "%d names and %d values", len(pv.spec.Names), len(pv.spec.Values))
		pv.obj.Type = invalid
	}
	// A var on a reported initialization cycle stays invalid: its
	// initializer is checked for its own errors, but no use of the var
	// cascades (D3).
	if pv.obj.Type == nil || pv.initCycle {
		pv.obj.Type = invalid
	}
}

func (c *checker) describeTarget() string {
	if c.fn == nil || c.fn.t == nil {
		return "this body"
	}
	return c.fn.t.name
}

var _ = fmt.Sprintf
