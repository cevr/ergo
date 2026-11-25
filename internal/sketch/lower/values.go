package lower

import (
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Value semantics for Maps and slices (value-rule; owner q26, 2026-11-08:
// "Maps and slices are values: copy where two bindings would share, elided
// when the source is new or dead"; types.md, Bindings).
//
// A Map and a slice are Go references, so a Go assignment shares them. The
// language says a value is seen only through the binding it was made
// through: a write through a `let mut` root (or a borrow, *T) is never
// seen through another binding, field, element, argument, capture or task.
// Only a `let mut` root and a borrow write in place, so a copy is needed
// only where a value enters one of them, or leaves one that is written
// again. The analysis below decides each place once per body, from the
// checker's facts, and the lowering reads the decisions:
//
//   - owning (rule V1): the value of `let mut x = e`, of an assignment to a
//     place rooted at a `let mut` or a borrow, and the element of an
//     in-place Append is copied unless it is fresh: a literal of fresh
//     parts, a call whose result is fresh (a function each of whose
//     results is fresh, the prelude's Append, Delete and Put on values that
//     hold no Map or slice, slices.WithCapacity), or a local that is dead
//     after the bind and was never shared (move on last use);
//   - escaping (V2): a read of a `let mut` root (a path x, x.f, x[i],
//     x[a:b]) whose value holds a Map or slice and is kept elsewhere (a
//     binding, an argument, a field or element, a capture, a match
//     subject) is copied when a write through the root can follow it: a
//     write after it in the body, a write in a loop that holds both, or a
//     write in another frame. A read that keeps nothing (len, ==,
//     interpolation, Get of a value that holds none, a pure call whose
//     result is basic) and the return of the root from its own frame are
//     never copied. A read through a borrow is always copied when kept: its
//     caller writes after the call;
//   - in place (V3): Append, Delete and Put write their source only for
//     `p = p.Append(x)`, `p = p.Delete(k)` and `p = p.Put(k, v)` on one
//     place p rooted at a `let mut` or a borrow, or on a local that is
//     dead after the call and was never shared; elsewhere Append appends
//     to a clipped slice (a new array when it grows), and Delete and Put
//     write a copy of the Map. Put's value is not owned as Append's
//     element is: a Map's value has no write path (a Map read is Get),
//     so a value Put stores is never written through the Map;
//   - captures (V4): a function literal or block that may outlive its call
//     (an escaping frame, types.md Function values 2 and 3) captures a
//     `let mut` only when no write to it follows (the checker refuses the
//     rest, owner q60), so it takes the value, not a copy of the storage
//     (closures.go, snapshot; runtime.md, Tasks 6: values copy into a
//     task);
//   - owned parameters (V5, ownedParams): a parameter that a function
//     takes into a let mut once and never reads after is copied by the
//     caller that shares it, not by the callee, and a rebinding p = f(…,
//     p, …) whose old value nothing else reads hands it over with no copy
//     (consumable): m = maps.Update(m, k, f) writes m in place, as hand Go
//     writes m[k] = f(m[k]); p may be rooted at a let mut or a borrow;
//   - hand-over (V7, handedArg): a rebinding p = f(…, p, …) of a pure f
//     with no failure row whose result shares no storage but p's (a pure
//     runtime/v1 body, or an Ergo body freshFrom proves so), on one place
//     rooted at a let mut or a borrow, passes p with no copy and takes
//     the result with none; so does p = T{..p, …} with its base, and a
//     literal's base is apart from a field it sets (update.go);
//   - a Ref owns its value (V6, refown.go): the parameter of a function
//     literal written at Ref.Update or sync.Modify takes the held value
//     over, as an unshared local; the cell copies a value that may be
//     reached from outside before it hands it over.
//
// Three facts sharpen the uses: a pure call (or pure std body) whose
// result reaches no Map or slice type its argument reaches keeps nothing
// of the argument (mayShare); a part of a returned tuple or literal is a
// return; and a use in one return of a frame never runs before a use in
// another, since each ends the frame.
//
// Only a slice, a Map, and a record or tuple that holds one can be written
// through a place, so only they are copied; a union, an Option and a
// function value hold their parts behind a match or a call, which bind
// them immutably. A copy is deep through the written paths (a slice of
// Maps copies each Map) and shallow elsewhere.
//
// A Map's entry is one of those parts with no write path (V8, entry
// rule): a Map is read with Get, so no place reaches into an entry
// (m[k][i] = v and m[k].f = v are S108), and a write through a Map only
// replaces or removes whole entries (m[k] = v, Put, Delete). So:
//
//   - a Get keeps nothing a write through its receiver's root can change:
//     its receiver is never copied, only evaluated before a write that a
//     later operand makes (inFlight). A lookup costs O(1), not the size of
//     the Map;
//   - a copy of a Map copies its entries' table, not the entries
//     (cloneValue): an entry two Maps share is written by neither. Go may
//     write anything it is given, so a value that crosses into Go or out
//     of it is copied deep (cloneDeep);
//   - a value kept out of a pure call whose result shares its argument
//     (slices.Slice(r.stack, i, j)) is the result, which is copied instead
//     of the argument, for a pure std body as for an Ergo one (alias.go),
//     when the copy of the result covers every part a write can reach
//     (copyCovers). The copy costs the window, not the field;
//   - a call's result used only by a read that runs at once (len, an index
//     read, a comparison) copies nothing (readonly.go);
//   - Append, Delete and Put on the result of another such call
//     (x.Append(a).Append(b)) write that result in place: it is new storage
//     no other binding reaches, as V3's dead local is (inPlaceChain).
//
// A read-modify-write of an entry moves it out and back in place (V9,
// entry move-out, moveout.go): every Map type the program moves out of is
// entry-written, so its copies copy the entries, Put's values are
// unshared and a kept Get copies what it gives, and the entry has no
// other holder at the site.
//
// The reference is copyLowering "whole", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.copyLowering=whole:
// the rule before V8 and V9, which copies the root a Get reads, copies a Map's
// entries, copies the argument of a pure std body, and clips each Append of
// a chain. TestCopyCountsMatchModel holds the copies the lowering emits
// to a model of the copies the semantics need; TestCopyRuleAgrees runs the
// same programs under both lowerings.
//
// Prior art: Swift's Dictionary is copied on write by a uniqueness test of
// its storage, and its values are copied only when one is written; Gleam,
// Elixir and ReScript maps are immutable, so a lookup never copies and an
// update copies one path (BEAM's HAMT above 32 keys); Koka and Lean 4 reuse
// a value in place when its reference count is one (Perceus). Ergo copies
// on a static fact: no write reaches an entry, so the entry is never
// copied for a write.
//
// The reference lowering shares, as v1 did: valueLowering "alias", set by
// a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.valueLowering=alias.
// TestValueRuleAgrees proves the two equal on programs with no shared
// write and the rule equal to a value-semantics interpreter on programs
// with one. valueLowering "copy" (-X=…valueLowering=copy) is the rule
// without owned parameters (V5): each callee copies its parameter at the
// bind, as before; TestValueRuleAgrees holds it and the rule to the
// interpreter on the same programs.
//
// Prior art: Swift's Array and Dictionary are values copied on write by a
// uniqueness test of their reference count, and its `consuming` parameters
// and `consume` operator hand a value over with no copy; Hylo's mutable
// value semantics and `sink` parameters; Gleam, Elixir and ReScript maps
// and lists are immutable, so no write is shared (Erlang's maps update a
// copy, and BEAM reuses a binary appended to in place when the old one is
// dead); Rust moves a value into a parameter and refuses the shared write
// in its borrow checker. Ergo has no reference count, so it copies on a
// static fact, as Swift's optimizer removes the copies it proves unneeded,
// and hands a dead binding over as a Rust move or a Swift consume does.
var valueLowering string

// valueMutant switches off one rule of the analysis, for the mutant runs
// of TestValueRuleMutants only: "never-copy", "shallow", "elide-live" (a
// live local moves), "elide-escape" (a kept read of a root written later
// shares) and
// "in-place" (Append, Delete and Put always write their source), and for owned
// parameters (V5) "consume-live" (an argument that stays live is passed
// uncopied), "consume-captured" (a rebinding hands over a binding another
// frame reads) and "own-live" (a parameter read after its bind is owned),
// and for owned Refs (V6) "hand-nonliteral" (a function value takes the
// held value over), "store-unfresh" (every site is proved fresh),
// "site-alone-always" (every site is proved alone, its footprint unread),
// "share-blind" (every pure call keeps nothing) and "return-blind" (two
// parts of one returned value count as two returns), and for hand-over
// (V7) "hand-shared" (an Ergo body's result is taken as sharing only the
// handed parameter), and for consumed arguments "consume-blind" (no
// parameter is consumed) and "consume-any" (every first operand of an
// owned form is consumed, live or not). Empty in every build.
var valueMutant string

// valueRule reports whether the value rule lowers this build.
func valueRule() bool { return valueLowering != "alias" }

// copyLowering "whole" selects the reference of the entry rule (V8,
// above). Empty in every build.
var copyLowering string

// entryRule reports whether the entry rule (V8) lowers this build.
func entryRule() bool { return copyLowering != "whole" }

// valueFacts are the decisions of the analysis, keyed by syntax node.
type valueFacts struct {
	clone    map[syntax.Expr]bool       // values copied where they are lowered
	why      map[syntax.Expr]string     // the reason of each clone, for the copy report (copyAt)
	inPlace  map[*syntax.CallExpr]bool  // Append, Delete and Put calls that write their source
	roots    map[syntax.Node]*valueWalk // analyzed bodies
	fresh    map[*check.Func]int8       // 1 deciding, 2 fresh, 3 not fresh
	consumed map[syntax.Expr]bool       // arguments an owned parameter takes in place (V5)
	handed   map[syntax.Expr]bool       // arguments a rebinding hands over to a pure body (V7)
	self     map[syntax.Expr]bool       // bases of an update stored back into its own let mut (parts.go, selfBases)
	from     map[fromKey]int8           // freshFrom: 1 deciding, 2 fresh, 3 not fresh
	fromAll  map[*check.Func]int8       // freshAll (handover.go): 1 deciding, 2 fresh, 3 not fresh
	consumes map[*syntax.CallExpr]bool  // calls of a runtime/v1 body's owned form: their first operand is consumed
	owned    map[*syntax.FuncDecl][]ownedParam
	ownCall  map[*syntax.CallExpr]bool     // calls that pass owned arguments unshared (V5)
	refSites map[*syntax.CallExpr]*refSite // Update and Modify calls whose literal owns the held value (V6)
	goShare  map[syntax.Expr]bool          // Go calls and Go field reads whose Map or slice value only Go takes, as Go holds it (H4, H5)
	moved    map[syntax.Expr]bool          // reads of a place its next write overwrites whole: a take or a reslice (V7, moves.go)
	lineage  map[*syntax.CallExpr]bool     // one-extender Append sites (handover.go, own-lineage)
	byName   map[*check.Func]bool          // functions the program names as a value (handover.go, passedByName)
	field    map[*syntax.CallExpr]bool     // Appends in place on a dead field path (handover.go, own-field)
	deep     map[syntax.Expr]bool          // copies that go to Go, which may write a Map's entries (V8, cloneDeep)
	reads    map[syntax.Expr]readsGuard    // copies the test build compares after the Go call, where the run build passes the operand (LF1, gofacts.go)
	view     map[syntax.Expr]hostViewSite  // Go operands that take a view of a string's bytes, or its guarded copy (LF2b, view.go)
	// The entry move-out (V9, moveout.go): the Map types the program
	// moves an entry out of (by their type strings) and the Gets it reads.
	entryWrites map[string]bool
	moveOut     map[syntax.Expr]bool // the binds of an entry move-out, and the entry its arm form takes (V9, moveout.go)
	moveRead    map[*syntax.CallExpr]bool
	copied      map[*check.Var]bool                  // the bindings of a matched Get whose entry is copied
	borrows     *borrowWrites[borrowSum]             // what a function writes through a borrow parameter (storeread.go, own-read-pass)
	fieldWrites *borrowWrites[fieldSum]              // the fields it writes through one (storefield.go, own-enter-field)
	borrowRet   map[*check.Func]map[syntax.Expr]bool // the parts of borrows a function returns shared (borrowret.go)
	// A collect's source whose read in the loop is its last, unshared (lastread.go).
	lastRead map[*syntax.Ident]bool
	// Slice literals that seed a let mut the body grows in place (seedcap.go).
	seeds map[*syntax.CompositeLit]bool
	// The fields of a parameter its function's body uses (fieldflow.go).
	touches map[touchKey]touched
}

// ownedParam is an owned parameter (V5): its index, counting a dot call's
// receiver first, and the value of the bind that takes it in the body.
type ownedParam struct {
	index int
	bind  syntax.Expr
	// consumed reports a parameter that a call consumes (consumedUse)
	// rather than one a let mut takes: bind is that call's operand, and
	// the body's analysis reads v as an unshared local.
	consumed bool
	v        *check.Var
}

func (l *lowerer) valueFacts() *valueFacts {
	if l.vals == nil {
		l.vals = &valueFacts{
			clone:    map[syntax.Expr]bool{},
			why:      map[syntax.Expr]string{},
			inPlace:  map[*syntax.CallExpr]bool{},
			roots:    map[syntax.Node]*valueWalk{},
			fresh:    map[*check.Func]int8{},
			consumed: map[syntax.Expr]bool{},
			handed:   map[syntax.Expr]bool{},
			self:     map[syntax.Expr]bool{},
			from:     map[fromKey]int8{},
			fromAll:  map[*check.Func]int8{},
			consumes: map[*syntax.CallExpr]bool{},
			owned:    map[*syntax.FuncDecl][]ownedParam{},
			ownCall:  map[*syntax.CallExpr]bool{},
			refSites: map[*syntax.CallExpr]*refSite{},
			goShare:  map[syntax.Expr]bool{},
			moved:    map[syntax.Expr]bool{},
			lineage:  map[*syntax.CallExpr]bool{},
			field:    map[*syntax.CallExpr]bool{},
			deep:     map[syntax.Expr]bool{},
			moveOut:  map[syntax.Expr]bool{},
			reads:    map[syntax.Expr]readsGuard{},
			moveRead: map[*syntax.CallExpr]bool{},
			copied:   map[*check.Var]bool{},
			lastRead: map[*syntax.Ident]bool{},
			seeds:    map[*syntax.CompositeLit]bool{},
			touches:  map[touchKey]touched{},
		}
	}
	return l.vals
}

// analyzeValues decides the copies of the body rooted at root, a function,
// test, package let value, context or machine initial value, once, with
// info, the facts of root's package.
func (l *lowerer) analyzeValues(root syntax.Node, info *check.Info) *valueWalk {
	vf := l.valueFacts()
	if w, ok := vf.roots[root]; ok {
		return w
	}
	if !valueRule() || isNilNode(root) {
		w := newValueWalk(l, info, vf, nil)
		vf.roots[root] = w
		return w
	}
	w := newValueWalk(l, info, vf, root)
	vf.roots[root] = w
	w.root = &vframe{node: root, tails: map[syntax.Expr]bool{}}
	if d, ok := root.(*syntax.FuncDecl); ok && d.Body != nil {
		tailsOf(d.Body.List, w.root.tails)
	}
	w.walk(root, nil)
	w.consumedParams(root)
	w.decide()
	w.done = true
	return w
}

// vnode is a node's place in the walk: its order numbers on entry and
// exit, and its parent (nil for the root).
type vnode struct {
	pre, post int
	parent    syntax.Node
}

func (w *valueWalk) preOf(n syntax.Node) int            { return w.nodes[n].pre }
func (w *valueWalk) postOf(n syntax.Node) int           { return w.nodes[n].post }
func (w *valueWalk) parentOf(n syntax.Node) syntax.Node { return w.nodes[n].parent }

// setParent records n's parent before n is visited; the visit keeps it.
func (w *valueWalk) setParent(n, parent syntax.Node) { w.nodes[n] = vnode{parent: parent} }

func (w *valueWalk) setPre(n syntax.Node, pre int) {
	v := w.nodes[n]
	v.pre = pre
	w.nodes[n] = v
}

func (w *valueWalk) setPost(n syntax.Node, post int) {
	v := w.nodes[n]
	v.post = post
	w.nodes[n] = v
}

// newValueWalk is an empty walk whose node map holds the nodes of root
// (nil: none) without growing: the walk puts every node in it, and a
// map that grows copies itself (SL9b: these maps are most of
// the lowering's bytes).
func newValueWalk(l *lowerer, info *check.Info, vf *valueFacts, root syntax.Node) *valueWalk {
	n := 0
	if root != nil {
		syntax.Inspect(root, func(m syntax.Node) bool {
			if m != nil {
				n++
			}
			return true
		})
	}
	return &valueWalk{l: l, info: info, vf: vf, vars: map[*check.Var]*vvar{}, nodes: make(map[syntax.Node]vnode, n), frameOf: map[syntax.Node]*vframe{}, handed: map[*syntax.CallExpr]bool{}}
}

func isNilNode(n syntax.Node) bool {
	switch n := n.(type) {
	case nil:
		return true
	case syntax.Expr:
		return n == nil
	}
	return false
}

// vframe is a frame of the body: the root, a function literal or a
// trailing block that is not a with's.
type vframe struct {
	node     syntax.Node
	parent   *vframe
	escaping bool
	tails    map[syntax.Expr]bool // the values its body returns
}

// inside reports whether f is g or nested in g.
func (f *vframe) inside(g *vframe) bool {
	for ; f != nil; f = f.parent {
		if f == g {
			return true
		}
	}
	return false
}

// vvar is a variable the body reads or declares.
type vvar struct {
	v      *check.Var
	local  bool // declared in this body (not a parameter, package let or capture)
	mut    bool // a let mut
	ptr    bool // a borrow, *T
	handed bool // the parameter of an owned Ref site's literal (V6): unshared, as a let mut is
	init   syntax.Expr
	// whole is the value of the let pattern that binds a part of it to
	// this local (a name of `let (l, w) = f(…)`), or nil.
	whole  syntax.Expr
	frame  *vframe
	loops  []syntax.Node
	occs   []*vocc
	writes []*vocc
}

// root reports a variable writes go through: a let mut or a borrow.
func (v *vvar) root() bool { return v.mut || v.ptr }

type occKind uint8

const (
	occRead   occKind = iota // keeps nothing
	occEscape                // kept elsewhere
	occReturn                // returned from its own frame
	occWrite                 // the root of a write
	occPart                  // the source of an in-place Append, Delete or Put
)

// vocc is one use of a variable: its identifier, the largest path it
// heads (x, x.f, x[i], x[a:b], *x, a conversion), and how that path is
// used.
type vocc struct {
	id    *syntax.Ident
	path  syntax.Expr
	kind  occKind
	frame *vframe
	loops []syntax.Node
	pre   int
	at    int // for a write, when it happens: after its statement or call
	// call is the pure call this use is an operand of, when a rule of the
	// call decided its kind (a pure call keeps nothing of it, or keeps it
	// only in its result); result marks the second: the call's result is
	// copied instead of the operand (alias.go).
	call   *syntax.CallExpr
	result bool
	deps   bool // the summary, not the types, decided it
	unkept bool // the callee's keeps summary decided it (keeps.go)
	// entry marks the receiver of a Get of an entry-written Map (V9)
	// that a match takes apart (getMatch): when the use must copy, the
	// entry the Get gives is copied, not the receiver.
	entry bool
}

type owning struct {
	value syntax.Expr
	root  *vvar
	whole bool   // the target is the root itself, x = e
	field string // the target is a field of the root, x.f = e (storefield.go)
}

type valueWalk struct {
	l     *lowerer
	info  *check.Info
	vf    *valueFacts
	root  *vframe
	vars  map[*check.Var]*vvar
	order []*vvar
	count int
	// nodes holds each node's pre and post order numbers and its parent
	// in one map (preOf, postOf, parentOf): three maps keyed by every node
	// were most of the lowering's bytes (SL9b).
	nodes    map[syntax.Node]vnode
	frameOf  map[syntax.Node]*vframe
	frames   []*vframe
	owns     []owning
	calls    []*syntax.CallExpr        // prelude Append, Delete and Put calls
	ergo     []*syntax.CallExpr        // calls of functions with an Ergo body (V5)
	goReads  []*syntax.CallExpr        // calls of import go functions (readonly.go)
	pure     []*syntax.CallExpr        // calls of other pure std bodies (V7)
	selves   map[syntax.Expr]bool      // arm values a catch stores back into their own place (catchSelf)
	handed   map[*syntax.CallExpr]bool // V7 calls: a fresh result (new storage, or the dead handed argument's)
	owner    *check.Var                // freshFrom's parameter, read as an unshared local (V7)
	owners   map[*check.Var]bool       // freshAll's parameters, each read as an unshared local (handover.go)
	done     bool                      // decide has run
	args     []syntax.Expr             // arguments of owned parameters (V5)
	goArgs   []syntax.Expr             // operands of Go calls of import go that hold a Map or slice (gocall.go)
	goOps    map[syntax.Expr]goOperand // each goArgs operand's call (gofacts.go)
	factUse  map[syntax.Expr]string    // what the lowering did with a Go operand's reads fact (gofacts.go)
	sites    []*refSite                // owned Ref sites (V6, refown.go)
	handOver map[*syntax.FuncLit]bool  // their literals
	cells    []syntax.Expr             // values that move into a provider cell (cells.go)
	// kids is the walk's stack of children (appendChildren).
	kids []syntax.Node
}

// appendChildren appends n's direct children to buf, in Inspect's order.
// A recursive walk keeps one buffer as a stack: a visit appends its
// children, reads them by index (a deeper visit may move the array), and
// cuts the buffer back to where it started, so no visit allocates a slice
// of its own (SL9b: a slice per visit was 17 % of the lowering's objects).
func appendChildren(buf []syntax.Node, n syntax.Node) []syntax.Node {
	syntax.Inspect(n, func(c syntax.Node) bool {
		if c == n {
			return true
		}
		buf = append(buf, c)
		return false
	})
	return buf
}

func (w *valueWalk) varOf(v *check.Var) *vvar {
	vv := w.vars[v]
	if vv == nil {
		vv = &vvar{v: v}
		_, vv.ptr = v.Type.(*check.Pointer)
		w.vars[v] = vv
		w.order = append(w.order, vv)
	}
	return vv
}

// walk visits n with the frame and loops that hold it.
func (w *valueWalk) walk(n syntax.Node, loops []syntax.Node) {
	w.setPre(n, w.count)
	w.count++
	frame := w.root
	if len(w.frames) > 0 {
		frame = w.frames[len(w.frames)-1]
	}
	if opens, escaping := w.opens(n); opens {
		f := &vframe{node: n, parent: frame, escaping: escaping}
		w.frameOf[n] = f
		w.frames = append(w.frames, f)
		frame = f
		defer func() { w.frames = w.frames[:len(w.frames)-1] }()
	}
	if x, ok := n.(syntax.Expr); ok && cellSite(w.info, x) {
		w.cells = append(w.cells, x)
	}
	switch n := n.(type) {
	case *syntax.Ident:
		if v, ok := w.info.Uses[n].(*check.Var); ok {
			vv := w.varOf(v)
			vv.occs = append(vv.occs, &vocc{id: n, frame: frame, loops: loops, pre: w.preOf(n)})
		}
	case *syntax.DeclStmt:
		for _, spec := range n.Decl.Specs {
			if spec.Pattern != nil && !n.Decl.Mut.IsValid() && len(spec.Values) == 1 && sharingLowering != "params" {
				// A let pattern, `let (l, w) = startLoad(k, u)`
				// (LWR-REF-GENERIC, std cache's begin): each name is a
				// local whose value is a part of the one value; it is
				// unshared when that value is (freshPath, whole).
				syntax.Inspect(spec.Pattern, func(p syntax.Node) bool {
					if id, ok := p.(*syntax.Ident); ok {
						if v, ok := w.info.Defs[id].(*check.Var); ok {
							vv := w.varOf(v)
							vv.local, vv.frame, vv.loops, vv.whole = true, frame, loops, spec.Values[0]
						}
					}
					return true
				})
			}
			for i, id := range spec.Names {
				v, ok := w.info.Defs[id].(*check.Var)
				if !ok {
					continue
				}
				vv := w.varOf(v)
				vv.local, vv.mut, vv.frame, vv.loops = true, n.Decl.Mut.IsValid(), frame, loops
				if spec.Pattern == nil && len(spec.Names) == 1 && len(spec.Values) == 1 && i == 0 {
					vv.init = spec.Values[0]
					if vv.mut {
						w.owns = append(w.owns, owning{value: spec.Values[0], root: vv})
					}
				}
				if call, ok := takeOf(spec); ok && i == 1 && !vv.mut && w.isTake(call) {
					// let v, rest = m.Take(k): rest's value is the Take's
					// (fresh, PRIM-3).
					vv.init = call
				}
			}
		}
	case *syntax.AssignStmt:
		if n.Tok == syntax.ASSIGN && len(n.Lhs) == 1 && len(n.Rhs) == 1 {
			if id := pathRoot(n.Lhs[0]); id != nil {
				if v, ok := w.info.Uses[id].(*check.Var); ok {
					o := owning{value: n.Rhs[0], root: w.varOf(v), whole: unparen(n.Lhs[0]) == syntax.Expr(id)}
					if sel, ok := unparen(n.Lhs[0]).(*syntax.SelectorExpr); ok && unparen(sel.X) == syntax.Expr(id) {
						o.field = sel.Sel.Name
					}
					w.owns = append(w.owns, o)
				}
			}
		}
	case *syntax.FuncLit:
		w.handParams(n, frame)
	case *syntax.CallExpr:
		w.refCall(n)
		if c := w.info.Calls[n]; c != nil && goImportCall(c, w.typeOf) {
			w.goReads = append(w.goReads, n)
		}
		if c := w.info.Calls[n]; c != nil && c.Kind == check.CallFunc && (appendsSource(c.Func) || writesMapSource(c.Func) || takesMapSource(c.Func)) {
			w.calls = append(w.calls, n)
		} else if c != nil && c.Kind == check.CallFunc && c.Func != nil && c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil {
			w.ergo = append(w.ergo, n)
		} else if c != nil && (goImportCall(c, w.typeOf) || goExternCall(c)) {
			// A Go call is never a V7 hand-over, even in the reviewed
			// pure tier (check.pureGo: sort.Ints): it is pure in effects,
			// not in memory, since Go writes or keeps a Map or slice it is
			// given. Its operands take the boundary rule.
			list := n.Args
			if c.Recv != nil {
				list = append([]syntax.Expr{c.Recv}, list...)
			}
			for i, a := range list {
				if goShared(w.typeOf(a)) || w.l.holdsValue(w.typeOf(a)) {
					w.goArgs = append(w.goArgs, a)
					w.goOperand(a, n, c, i)
				}
			}
		} else if c != nil && c.Kind == check.CallFunc && pureStdBody(c.Func) {
			w.pure = append(w.pure, n)
		}
	}
	if f := w.frameOf[n]; f != nil {
		f.tails = map[syntax.Expr]bool{}
		switch n := n.(type) {
		case *syntax.FuncDecl:
			if n.Body != nil {
				tailsOf(n.Body.List, f.tails)
			}
		case *syntax.FuncLit:
			tailsOf(n.Body.List, f.tails)
		case *syntax.BlockStmt:
			tailsOf(n.List, f.tails)
		}
	}
	inner := loops
	switch n.(type) {
	case *syntax.ForStmt:
		inner = append(append([]syntax.Node{}, loops...), n)
	}
	start := len(w.kids)
	w.kids = appendChildren(w.kids, n)
	end := len(w.kids)
	for i := start; i < end; i++ {
		c := w.kids[i]
		w.setParent(c, n)
		cl := inner
		if r, ok := n.(*syntax.RangeStmt); ok && c != syntax.Node(r.X) {
			cl = append(append([]syntax.Node{}, loops...), n)
		}
		if w.frameOf[n] != nil {
			cl = nil // a new frame: its loops are its own
		}
		w.walk(c, cl)
	}
	w.kids = w.kids[:start]
	w.setPost(n, w.count)
	w.count++
}

// tailsOf adds the values a body yields as its result: its last
// statement's value, through if and match arms; explicit returns are read
// at their statements.
func tailsOf(list []syntax.Stmt, out map[syntax.Expr]bool) {
	if len(list) == 0 {
		return
	}
	switch s := list[len(list)-1].(type) {
	case *syntax.ExprStmt:
		switch x := unparen(s.X).(type) {
		case *syntax.IfStmt:
			tailsOfIf(x, out)
		case *syntax.SwitchStmt:
			for _, c := range x.Clauses {
				tailsOf(c.Body, out)
			}
		case *syntax.TypeSwitchStmt:
			for _, c := range x.Clauses {
				tailsOf(c.Body, out)
			}
		default:
			out[s.X] = true
		}
	case *syntax.IfStmt:
		tailsOfIf(s, out)
	case *syntax.SwitchStmt:
		for _, c := range s.Clauses {
			tailsOf(c.Body, out)
		}
	case *syntax.TypeSwitchStmt:
		for _, c := range s.Clauses {
			tailsOf(c.Body, out)
		}
	case *syntax.BlockStmt:
		tailsOf(s.List, out)
	}
}

func tailsOfIf(s *syntax.IfStmt, out map[syntax.Expr]bool) {
	tailsOf(s.Body.List, out)
	switch e := s.Else.(type) {
	case *syntax.BlockStmt:
		tailsOf(e.List, out)
	case *syntax.IfStmt:
		tailsOfIf(e, out)
	}
}

// opens reports whether n opens a frame and whether it may outlive the
// call it is passed to, as the checker decides (check/escaping.go).
func (w *valueWalk) opens(n syntax.Node) (frame, escaping bool) {
	switch n := n.(type) {
	case *syntax.FuncLit:
		parent, child := w.unparenParent(n)
		call, ok := parent.(*syntax.CallExpr)
		if !ok {
			return true, !w.info.LocalFrames[n]
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
		call, ok := w.parentOf(n).(*syntax.CallExpr)
		if !ok || call.Trailing != n {
			return false, false
		}
		if k := w.info.Calls[call]; k != nil && k.Kind == check.CallWith {
			return false, false
		}
		return true, !w.paramNonEscaping(call, len(call.Args))
	}
	return false, false
}

// unparenParent is n's nearest ancestor that is not a parenthesis, with
// the child on the path.
func (w *valueWalk) unparenParent(n syntax.Node) (parent, child syntax.Node) {
	child = n
	for {
		p := w.parentOf(child)
		if _, ok := p.(*syntax.ParenExpr); ok {
			child = p
			continue
		}
		return p, child
	}
}

// paramNonEscaping is the checker's rule (check.ParamNonEscaping).
func (w *valueWalk) paramNonEscaping(call *syntax.CallExpr, i int) bool {
	return check.ParamNonEscaping(w.info, call, i)
}

// pathRoot is the variable that heads the place or path x: x, x.f, x[i],
// x[a:b], *x, in parentheses; nil for any other expression.
func pathRoot(x syntax.Expr) *syntax.Ident {
	for {
		switch e := x.(type) {
		case *syntax.Ident:
			return e
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.SelectorExpr:
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.SliceExpr:
			x = e.X
		case *syntax.StarExpr:
			x = e.X
		default:
			return nil
		}
	}
}

// The value rule reads how a call treats its first operand's storage from
// the callee's Go form, never from which operation the callee is (RU6;
// internal/residue, TestValueRuleReadsNoOperation): the prelude's Append,
// Put, Delete and Take are users of the rule, as a user's extern of Go's
// append is. Three forms write their source:
//
//   - appendsSource: Go's builtin append, or a runtime/v1 appender
//     (gobody.go, goBodyAppends: the prelude's Append, slices.Concat);
//   - writesMapSource: Go's builtin delete (the prelude's Delete binds it),
//     or a runtime/v1 body that writes its Map operand and returns it
//     (goBodyWritesMap: the prelude's Put);
//   - takesMapSource: an intrinsic that takes an entry out of its Map
//     operand in place (decls.go, intrinsicTakes: the prelude's Take).
//
// Prior art: Swift's copy-on-write collections decide a write in place by
// the buffer's uniqueness at run time, for every mutating method alike;
// Koka's and Lean's Perceus reuse decides it by the reference count, for
// any constructor; neither keys the decision on the operation's name.

// appendsSource reports a call that may write past its source's length, so
// its source is clipped where it is not new or dead (V3).
func appendsSource(f *check.Func) bool {
	return check.GoBuiltin(f) == "append" || appenderExtern(f)
}

// writesMapSource reports a call that writes its Map source when it may
// (V3), and writes a copy of it otherwise.
func writesMapSource(f *check.Func) bool {
	return check.GoBuiltin(f) == "delete" || mapWriterExtern(f)
}

// takesMapSource reports a call that takes its Map source apart, in place
// when it may (V3), and from a copy otherwise, as a Map write does
// (PRIM-3). Its result is the taken value and the rest, so it heads no
// chain (writeCall).
func takesMapSource(f *check.Func) bool {
	return intrinsicTakes(f)
}

func (w *valueWalk) typeOf(x syntax.Expr) check.Type {
	t, _ := typedIn(w.info, x)
	return t
}

// holds reports whether a write through a place of type t can reach a Map
// or a slice: t is one, or a record or tuple with a field that holds one.
func (l *lowerer) holdsValue(t check.Type) bool {
	return holdsValueSeen(t, map[check.Type]bool{})
}

func holdsValueSeen(t check.Type, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Slice, *check.Map:
		return true
	case *check.Array:
		// Go copies an array as a whole: it needs a copy function only
		// when an element holds a slice or a Map (196y).
		return holdsValueSeen(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if holdsValueSeen(e, seen) {
				return true
			}
		}
	case *check.Struct:
		for _, f := range u.Fields {
			if holdsValueSeen(f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// elemOf is the element of a slice or the value of a Map.
func elemOf(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Slice:
		return u.Elem
	case *check.Array:
		return u.Elem
	case *check.Map:
		return u.Elem
	}
	return nil
}

// recvOf is the source operand of a call of Append, Delete or Put.
func (w *valueWalk) recvOf(call *syntax.CallExpr) syntax.Expr {
	c := w.info.Calls[call]
	if c.Recv != nil {
		return c.Recv
	}
	if len(call.Args) > 0 {
		return call.Args[0]
	}
	return nil
}

// decide classifies every use, then marks the copies, the in-place calls
// and the snapshots.
func (w *valueWalk) decide() {
	vf := w.vf
	// In-place pattern: p = p.Append(x), p = p.Delete(k) or p = p.Put(k, v), one place
	// rooted at a let mut or a borrow. A chain whose source is such a call,
	// p = append(append(p, a), b), is in place at each call: no binding
	// holds an inner call's result, so the chain is the statements
	// p = append(p, a); p = append(p, b), which the pattern takes in place.
	// MAPCOPY x OWN1: chainOf walks both forms, p.Append(a).Append(b) and
	// append(append(p, a), b), from the outermost call to its first source.
	for _, call := range w.calls {
		parent, child := w.unparenParent(call)
		s, ok := parent.(*syntax.AssignStmt)
		if !ok || s.Tok != syntax.ASSIGN || len(s.Rhs) != 1 || s.Rhs[0] != child {
			continue
		}
		chain := w.chainOf(call)
		recv := w.recvOf(chain[len(chain)-1])
		id := pathRoot(s.Lhs[0])
		if id == nil || !samePlace(s.Lhs[0], recv) && !w.sameElem(s.Lhs[0], recv) {
			continue
		}
		if v, ok := w.info.Uses[id].(*check.Var); ok && w.varOf(v).root() {
			for _, c := range chain {
				vf.inPlace[c] = true
			}
		}
	}
	// Take's form of the pattern (PRIM-3): let v, rest = p.Take(k) and,
	// as the next statement, p = rest: p's old value is read by the Take
	// alone, so the Take writes it in place.
	for _, call := range w.calls {
		if w.isTake(call) && w.takeRebound(call) {
			vf.inPlace[call] = true
		}
	}
	// The one-extender sites (handover.go), before any freshness is read.
	w.lineageSites()
	// Owned parameters (V5): each argument of one, and the arguments a
	// rebinding hands over in place.
	for _, call := range w.ergo {
		c := w.info.Calls[call]
		owned := w.l.ownedParams(c.Func)
		if len(owned) > 0 {
			vf.ownCall[call] = true
		}
		for _, p := range owned {
			if arg := argOf(call, c, p.index); arg != nil {
				w.args = append(w.args, arg)
				if w.consumable(call, arg) {
					vf.consumed[arg] = true
				}
			}
		}
	}
	// Hand-over (V7): p = f(…, p, …) with f a pure body, Ergo or Go.
	for _, calls := range [][]*syntax.CallExpr{w.pure, w.ergo} {
		for _, call := range calls {
			if arg := w.handedArg(call); arg != nil {
				vf.handed[arg] = true
				w.handed[call] = true
			}
		}
	}
	w.selfBases()
	for _, vv := range w.order {
		for _, o := range vv.occs {
			w.classify(vv, o)
			if o.kind == occWrite {
				vv.writes = append(vv.writes, o)
			}
		}
	}
	// The entry move-outs that hold (V9, moveout.go).
	w.decideMoveOuts()
	// Moves (V7, moves.go): reads the escapes and the binds leave alone.
	w.decideMoves()
	// Escapes (V2).
	if valueMutant != "never-copy" {
		for _, vv := range w.order {
			if !vv.root() {
				continue
			}
			for _, o := range vv.occs {
				switch {
				case vf.moved[o.path]:
					// Its place is overwritten next (V7).
				case o.unkept && w.callWrites(vv, o):
					// The callee could see a write to the root (keeps.go).
					vf.clone[o.path] = true
				case o.unkept:
					w.l.stats.Unkept++
				case o.kind == occRead && o.call != nil && valueMutant != "in-flight-blind" && w.inFlight(vv, o):
					// The callee would see a write a later operand makes.
					if w.l.holdsValue(w.typeOf(o.path)) {
						w.copyAt(o.path, whyInFlight)
					}
				case o.kind == occRead && o.deps:
					if w.needsCopy(vv, o) {
						w.l.stats.ArgsShared++
					}
				case o.kind == occEscape || o.kind == occReturn:
					if vf.handed[o.path] || w.reslices(o.path, vv) || w.catchSelf(o.path) || !w.needsCopy(vv, o) {
						break
					}
					if w.sharedReturn(vv, o) {
						// Its callers copy it (borrowret.go).
						break
					}
					if o.entry && !w.inFlight(vv, o) {
						// The entry the matched Get gives (V9), which
						// its arm's bindings then hold unshared.
						w.copyAt(o.call, whyEntry)
						for _, v := range w.matchBinds(o.call) {
							vf.copied[v] = true
						}
						break
					}
					if o.result && !w.inFlight(vv, o) && w.l.copyCovers(w.typeOf(o.call)) {
						// A pure call's result, not its argument, is
						// copied (alias.go).
						w.copyAt(o.call, whyResult)
						w.l.stats.ArgsShared++
						w.l.stats.ResultCopies++
						break
					}
					w.copyAt(o.path, whyEscape)
				}
			}
		}
		// The parts of an owned parameter that a let keeps (V5).
		w.ownedKept()
	}
	// A generic result through a borrow (alias.go, borrowResult), or a
	// part of a borrow returned shared (borrowret.go), when kept.
	if valueMutant != "never-copy" && valueMutant != "borrow-blind" {
		for _, call := range w.ergo {
			if w.borrowResult(call) && !w.readOnlyUse(call) && !w.keptCall(call) {
				w.copyAt(call, whyBorrow)
				w.l.stats.BorrowCopies++
			}
		}
	}
	// In place on a dead, unshared source (V3).
	for _, call := range w.calls {
		if vf.inPlace[call] {
			continue
		}
		if recv := w.recvOf(call); recv != nil && vf.moveOut[recv] {
			// The entry a move-out's arm form takes (moveout.go, V9).
			vf.inPlace[call] = true
			delete(vf.lineage, call)
		} else if id, ok := unparen(w.recvOf(call)).(*syntax.Ident); ok {
			if v, ok := w.info.Uses[id].(*check.Var); ok && w.freshIdent(w.varOf(v), id, false) {
				vf.inPlace[call] = true
			}
		} else if w.deadField(call) {
			// And on a dead field path (handover.go, own-field).
			vf.inPlace[call], vf.field[call] = true, true
			delete(vf.lineage, call)
		}
	}
	// In place on the result of another Append, Delete or Put (V8): new
	// storage, or a dead source's, that no binding reaches; never on a
	// one-extender site's result (inPlaceChain), which that site may
	// extend in place later (handover.go, own-lineage).
	for _, call := range w.calls {
		if !vf.inPlace[call] && w.inPlaceChain(call) {
			vf.inPlace[call] = true
		}
	}
	// An appender in place takes its owned form (handover.go).
	w.appenderConsumes()
	// Owning (V1). A bind that takes an owned parameter of this function
	// copies nothing: its callers pass the value unshared (V5).
	if valueMutant != "never-copy" {
		takes := map[syntax.Expr]bool{}
		if d, ok := w.root.node.(*syntax.FuncDecl); ok {
			if f, ok := w.info.Defs[d.Name].(*check.Func); ok {
				for _, p := range w.l.ownedParams(f) {
					takes[p.bind] = true
				}
			}
		}
		for _, o := range w.owns {
			if !o.root.root() || takes[o.value] || vf.moved[o.value] || vf.moveOut[o.value] {
				continue
			}
			if call, ok := unparen(o.value).(*syntax.CallExpr); ok && vf.inPlace[call] {
				// Each element an Append of the chain adds enters the
				// root, and so does the storage of a source that is not
				// written in place: a new slice or Map whose elements
				// are shared, freshened as a whole.
				for _, call := range w.chainOf(call) {
					if !vf.inPlace[call] {
						w.freshen(call, whyChain)
						break
					}
					if c := w.info.Calls[call]; c != nil && appendsSource(c.Func) {
						args := call.Args
						if c.Recv == nil && len(args) > 0 {
							args = args[1:]
						}
						// An operand enters the slice only as an element:
						// one whose element type holds no Map or slice keeps
						// nothing an operand holds (an appender's []byte
						// operand is read, its bytes copied; GX10).
						if s, ok := underEqual(w.typeOf(call)).(*check.Slice); ok && !w.l.holdsValue(s.Elem) {
							continue
						}
						for _, a := range args {
							if w.spreads(call, a) && !w.l.holdsValue(elemOf(w.typeOf(a))) {
								// Only its elements enter the root, and they
								// hold no Map or slice (handover.go).
								continue
							}
							w.freshen(a, whyElement)
						}
					}
				}
				continue
			}
			if o.whole && w.slicesOf(o.value, o.root) || w.reslices(o.value, o.root) {
				continue
			}
			w.moveOver(o.value)
			if w.catchJoin(o.value) {
				continue
			}
			if lit, ok := unparen(o.value).(*syntax.CompositeLit); ok && o.root.init != nil && unparen(o.root.init) == syntax.Expr(lit) {
				// A let mut's literal: a field may enter shared (storefield.go).
				w.freshenLit(lit, o.root)
				continue
			}
			if o.field != "" && w.sharedField(o.root, o.field, o.value) {
				// A path of a parameter assigned to a field nothing
				// writes (storefield.go).
				continue
			}
			w.freshen(o.value, whyEnters)
		}
		// A value Put stores in a Map whose entries are written in place
		// is unshared, as Append's element is (V9).
		for _, call := range w.calls {
			c := w.info.Calls[call]
			if ops := callOperands(call, c); writesMapSource(c.Func) && len(ops) == 3 && w.l.entryWritten(w.typeOf(ops[0])) {
				w.freshen(ops[2], whyPut)
			}
		}
		// An argument of an owned parameter enters the callee's root: it
		// is copied unless fresh, or handed over by a rebinding (V5).
		for _, arg := range w.args {
			if !vf.consumed[arg] && valueMutant != "consume-live" {
				w.freshen(arg, whyOwnedArg)
			}
		}
	}
	// An operand of a Go call (import go) that holds a Map or slice goes
	// where Go may write or keep it: copied unless fresh or a local dead
	// after the call, and always under the reference lowering (gocall.go).
	// A Go call's result read at once is not copied (readonly.go).
	for _, call := range w.goReads {
		if w.readOnlyUse(call) {
			vf.goShare[call] = true
		}
	}
	goOnly := w.goOnlyLets()
	for _, a := range w.goArgs {
		// Go's own Map or slice (a Go call's result or a Go struct's
		// field) that is an operand of a Go call (a receiver:
		// w.Header().Set(k, v), r.Header.Set(k, v)) is never an Ergo
		// value: it goes on to Go uncopied, as hand Go passes it, so a
		// write reaches the map Go keeps (interop.md, Lowering 3). Under
		// both lowerings: a copy here would lose the write, not keep a
		// value.
		if inner := unparen(a); w.goHeld(inner) {
			w.goOperandReport(a, "shared", "Go's own Map or slice, a Go call's result or a Go struct's field, goes back to Go as Go holds it")
			vf.goShare[inner] = true
			continue
		}
		// So is a let that holds one and that only Go calls use (H5).
		if id, ok := unparen(a).(*syntax.Ident); ok {
			w.goOnlyReport(a, id, goOnly)
			if v, ok := w.info.Uses[id].(*check.Var); ok && goOnly[w.vars[v]] {
				continue
			}
		}
		switch {
		case w.l.entryShared(w.typeOf(a)):
			// Two Maps may share an entry, which Go may write (V8): Go
			// is given a deep copy, fresh or not.
			w.copyAt(a, whyGoDeep)
			vf.deep[a] = true
			w.goOperandReport(a, "copy", "a deep copy: two Maps may share an entry, which Go may write")
		case hostCopyAlways:
			w.copyAt(a, whyGoArg)
			w.goOperandReport(a, "copy", "the reference lowering copies every Go operand")
		default:
			// A reads fact (LF1): Go neither writes, keeps nor returns a
			// part of the operand, so the run build passes it as it is;
			// the test build copies it and compares after the call.
			if w.viewOK(a) {
				// A view of a string's bytes (LF2b, view.go).
				w.goOperandReport(a, "view", "Go only reads the string's bytes")
				continue
			}
			take, guard := w.readsOK(a)
			if take {
				w.goOperandReport(a, "no copy", "Go only reads the operand")
				continue
			}
			w.freshen(a, whyGoArg)
			if guard && vf.clone[a] {
				w.readsGuardAt(a)
			}
			if vf.clone[a] {
				w.goOperandReport(a, "copy", "Go may write or keep the operand, and another binding reads its value later")
			} else {
				w.goOperandReport(a, "no copy", "the operand is fresh, or a local dead after the call")
			}
		}
	}
	// A value that moves into a provider cell (cells.go).
	for _, x := range w.cells {
		w.cellCopy(x)
	}
	// Owned Ref sites (V6), once every copy is decided.
	w.decideSites()
	// Consumed operands of owned runtime/v1 forms, likewise.
	w.consumes()
	w.settleLineage()
	w.lastReads() // lastread.go: once every copy is decided
	w.seedLits()  // seedcap.go
}

// takeRest is the let that binds the rest of the Take call, let v, rest =
// p.Take(k), its rest's name, and the let's statement, or nil.
func (w *valueWalk) takeRest(call *syntax.CallExpr) (*syntax.DeclStmt, *syntax.Ident) {
	_, child := w.unparenParent(call)
	// The let's statement, above its spec and declaration nodes.
	var d *syntax.DeclStmt
	for p := w.parentOf(child); p != nil && d == nil; p = w.parentOf(p) {
		switch p := p.(type) {
		case *syntax.DeclStmt:
			d = p
		case *syntax.VarSpec, *syntax.VarDecl:
		default:
			return nil, nil
		}
	}
	if d == nil || len(d.Decl.Specs) != 1 {
		return nil, nil
	}
	spec := d.Decl.Specs[0]
	if spec.Pattern != nil || len(spec.Names) != 2 || len(spec.Values) != 1 || spec.Values[0] != child {
		return nil, nil
	}
	return d, spec.Names[1]
}

// takeRebound reports the Take call's pattern: let v, rest = p.Take(k),
// then p = rest as the next statement of the same block, with p a place
// rooted at a let mut or a borrow (as p = p.Delete(k)).
func (w *valueWalk) takeRebound(call *syntax.CallExpr) bool {
	d, rest := w.takeRest(call)
	if d == nil {
		return false
	}
	block, ok := w.parentOf(d).(*syntax.BlockStmt)
	if !ok {
		return false
	}
	i := slices.Index(block.List, syntax.Stmt(d))
	if i < 0 || i+1 >= len(block.List) {
		return false
	}
	s, ok := block.List[i+1].(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false
	}
	r, ok := unparen(s.Rhs[0]).(*syntax.Ident)
	if !ok || w.info.Uses[r] == nil || w.info.Uses[r] != w.info.Defs[rest] {
		return false
	}
	id := pathRoot(s.Lhs[0])
	if id == nil || !samePlace(s.Lhs[0], w.recvOf(call)) {
		return false
	}
	v, ok := w.info.Uses[id].(*check.Var)
	return ok && w.varOf(v).root()
}

// takeOf is the call a let of two names binds, let v, rest = call.
func takeOf(spec *syntax.VarSpec) (*syntax.CallExpr, bool) {
	if spec.Pattern != nil || len(spec.Names) != 2 || len(spec.Values) != 1 {
		return nil, false
	}
	call, ok := unparen(spec.Values[0]).(*syntax.CallExpr)
	return call, ok
}

// isTake reports call as a call of the Map operation Take.
func (w *valueWalk) isTake(call *syntax.CallExpr) bool {
	c := w.info.Calls[call]
	return c != nil && c.Kind == check.CallFunc && takesMapSource(c.Func)
}

// writeCall reports x, unparenthesized, as a call of the prelude's Append,
// Delete or Put.
func (w *valueWalk) writeCall(x syntax.Expr) (*syntax.CallExpr, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	c := w.info.Calls[call]
	return call, c != nil && c.Kind == check.CallFunc && (appendsSource(c.Func) || writesMapSource(c.Func))
}

// chainOf is call and the calls of Append,
// Delete and Put down its source: x.Append(a).Append(b) is the outer
// call, then x.Append(a).
func (w *valueWalk) chainOf(call *syntax.CallExpr) []*syntax.CallExpr {
	out := []*syntax.CallExpr{call}
	for {
		inner, ok := w.writeCall(w.recvOf(out[len(out)-1]))
		if !ok {
			break
		}
		out = append(out, inner)
	}
	return out
}

// inPlaceChain reports an Append, Delete or Put whose source is the
// result of another (V8): the storage that call gives is new (a clipped
// Append grows into a new array, a Delete or Put not in place writes a
// copy) or the storage of a source it wrote in place, which no binding
// reaches either, so this call may write it.
func (w *valueWalk) inPlaceChain(call *syntax.CallExpr) bool {
	if !entryRule() {
		return false
	}
	inner, ok := w.writeCall(w.recvOf(call))
	// A one-extender site's result is not the chain's alone: the site may
	// extend its array in place later (handover.go, own-lineage).
	return ok && !w.vf.lineage[inner]
}

// goHeld reports x, unparenthesized, as Go's own Map or slice: the result
// of a Go call (import go) or a field of a Go struct, which Go may keep
// and write (gocall.go, goResult).
func (w *valueWalk) goHeld(x syntax.Expr) bool {
	if !goShared(w.typeOf(x)) {
		return false
	}
	switch x := x.(type) {
	case *syntax.CallExpr:
		return goImportCall(w.info.Calls[x], w.typeOf)
	case *syntax.SelectorExpr:
		// A field read through a go.Ref lowers through goResult (expr.go,
		// selector); a method value has a function type, so goShared
		// refused it. A field of a Go struct value is part of an Ergo
		// value, so it keeps the value rule.
		_, ref := check.GoRefElem(w.typeOf(x.X))
		return ref
	}
	return false
}

// goOnlyLets is the immutable lets of this body that hold Go's own Map or
// slice (goHeld) and whose every use is a whole operand of a Go call (let
// h = w.Header(); h.Set(k, v)). Such a let is never an Ergo value: as for
// w.Header().Set(k, v), Go's map is not copied where it is bound (goShare)
// nor where Go takes it, so a write reaches the map Go keeps (interop.md,
// Lowering 3; H5). A let that the program also uses another way is an
// Ergo value and is copied, by the value rule.
func (w *valueWalk) goOnlyLets() map[*vvar]bool {
	operand := map[syntax.Expr]bool{}
	for _, a := range w.goArgs {
		operand[unparen(a)] = true
	}
	out := map[*vvar]bool{}
	for _, vv := range w.order {
		if !vv.local || vv.root() || vv.init == nil {
			continue
		}
		init := unparen(vv.init)
		if t, ok := init.(*syntax.TryExpr); ok {
			init = unparen(t.X)
		}
		if !w.goHeld(init) {
			continue
		}
		only := true
		for _, o := range vv.occs {
			if w.info.Defs[o.id] == nil && !operand[o.id] {
				only = false
				break
			}
		}
		if only {
			out[vv] = true
			w.vf.goShare[init] = true
		}
	}
	return out
}

// valueOwned reports whether owned parameters (V5) lower this build: the
// value rule does, and the copying lowering "copy" is not selected.
func valueOwned() bool { return valueRule() && valueLowering != "copy" }

// argOf is the operand of call c for parameter i, counting a dot call's
// receiver first, or nil (a trailing block).
func argOf(call *syntax.CallExpr, c *check.Call, i int) syntax.Expr {
	if c.Recv != nil {
		if i == 0 {
			return c.Recv
		}
		i--
	}
	if i < len(call.Args) {
		return call.Args[i]
	}
	return nil
}

// consumable reports a rebinding p = f(…, p, …) (or p = try f(…)) of one
// place p, rooted at a let mut local of this body that no other frame
// reads or writes, with no other use of the local in the statement: the
// old value of p is dead once the call starts, so the owned parameter
// takes it with no copy, as hand Go writes m[k] = v for
// m = maps.Update(m, k, f).
func (w *valueWalk) consumable(call *syntax.CallExpr, arg syntax.Expr) bool {
	parent, child := w.unparenParent(call)
	if t, ok := parent.(*syntax.TryExpr); ok && t.X == child {
		parent, child = w.unparenParent(t)
	}
	caught := false
	if c, ok := parent.(*syntax.CatchExpr); ok && c.X == child {
		if !w.catchTaken() {
			return false
		}
		parent, child = w.unparenParent(c)
		caught = true
	}
	s, ok := parent.(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != child {
		return false
	}
	lid, aid := pathRoot(s.Lhs[0]), pathRoot(arg)
	// A reslice of the place, p = f(…, p[a:b], …), hands over the array p
	// alone holds, as p = p[a:b] keeps it (moves.go): p's old value is dead
	// from the call on (LWR-INLINE-OWNED: buf = AppendX(…, buf[:0])).
	place := cutPlace(arg)
	if lid == nil || aid == nil || !samePlace(s.Lhs[0], place) {
		return false
	}
	v, ok := w.info.Uses[aid].(*check.Var)
	if !ok || w.info.Uses[lid] != check.Object(v) {
		return false
	}
	vv := w.varOf(v)
	// A borrow is the only path to its target (Borrows 2): through one,
	// b.f = f(…, b.f, …) hands b.f over as a let mut's place does.
	if !(vv.local && vv.mut && !vv.ptr || vv.ptr && lid != s.Lhs[0]) {
		return false
	}
	frame := vv.frame
	if frame == nil {
		frame = w.root // a parameter's frame is the body's
	}
	for _, o := range vv.occs {
		if o.frame != frame {
			if valueMutant == "consume-captured" {
				continue
			}
			return false
		}
		if caught && o.pre > w.postOf(call) && o.pre < w.postOf(s) {
			// An arm runs only when f failed, and f fails before it takes
			// its owned parameter (ownedBind, consumedOcc, fieldOwned:
			// failsAfter), so an arm reads p's old value unwritten.
			continue
		}
		if o.id != lid && o.id != aid && o.pre > w.preOf(s) && o.pre < w.postOf(s) && !w.plainReadIn(vv, o, call) {
			return false
		}
	}
	return true
}

// catchTaken is own-catch's admission (REUSE RU2): a rebinding
// p = f(…, p, …) catch { … } hands p over to f's owned parameter as
// p = try f(…) does. An owned parameter is taken only where f cannot fail
// after the taking, so on the failure route p is as it was, and an arm
// may read it or yield it; and the catch's value stored back into p
// copies none of its arms that yield p itself (catchJoin).
func (w *valueWalk) catchTaken() bool {
	if !w.l.takes("own-catch") {
		return false
	}
	w.l.took("own-catch")
	return true
}

// catchJoin freshens the value of p = f(…) catch { arms } as it enters
// p: the call's result, and each arm's value but one that is the place p
// itself, which the store puts back where it was. It reports false where
// the value is no such catch, or own-catch is not taken.
func (w *valueWalk) catchJoin(value syntax.Expr) bool {
	c, place := w.catchStore(value)
	if c == nil || !w.catchTaken() {
		return false
	}
	w.freshen(c.X, whyEnters)
	for _, tail := range catchTails(c) {
		if samePlace(place, tail) {
			continue
		}
		w.freshen(tail, whyEnters)
	}
	return true
}

// catchStore is the catch that value is, when it is the whole right side
// of a plain assignment p = … catch { … }, and the place p; or nil.
func (w *valueWalk) catchStore(value syntax.Expr) (*syntax.CatchExpr, syntax.Expr) {
	c, ok := unparen(value).(*syntax.CatchExpr)
	if !ok {
		return nil, nil
	}
	parent, child := w.unparenParent(c)
	s, ok := parent.(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != child {
		return nil, nil
	}
	return c, s.Lhs[0]
}

// catchTails is the values c's arms yield, in source order.
func catchTails(c *syntax.CatchExpr) []syntax.Expr {
	var out []syntax.Expr
	for _, arm := range c.Arms {
		tails := map[syntax.Expr]bool{}
		tailsOf(arm.Body, tails)
		var each []syntax.Expr
		for tail := range tails {
			each = append(each, tail)
		}
		sort.Slice(each, func(i, j int) bool { return each[i].Pos() < each[j].Pos() })
		out = append(out, each...)
	}
	return out
}

// catchSelf reports an arm's value that is the place its catch is stored
// back into, p = f(…) catch { case E: p }: the store puts p back where
// it was, so the read escapes nowhere (own-catch).
func (w *valueWalk) catchSelf(e syntax.Expr) bool {
	if w.selves == nil {
		w.selves = map[syntax.Expr]bool{}
		for _, o := range w.owns {
			if c, place := w.catchStore(o.value); c != nil {
				for _, tail := range catchTails(c) {
					if samePlace(place, tail) {
						w.selves[unparen(tail)] = true
					}
				}
			}
		}
	}
	return w.selves[e] && w.catchTaken()
}

// cutPlace is the place x slices (any depth of slicing), or x.
func cutPlace(x syntax.Expr) syntax.Expr {
	for {
		s, ok := unparen(x).(*syntax.SliceExpr)
		if !ok {
			return x
		}
		x = s.X
	}
}

// plainReadIn reports whether the use o of vv is an operand read inside
// call of a part that holds no Map or slice (u.next in
// u = touch(k, ready{stamp: u.next}, u); LWR-REF-GENERIC, std cache's
// begin and keep): Go evaluates it before the call runs, and its value,
// a copy, shares no storage with the value the call takes over, so the
// old value is still dead from the call to the store-back. A write (an
// assignment, a borrow, a method that takes the part by borrow) is no
// plain read.
func (w *valueWalk) plainReadIn(vv *vvar, o *vocc, call *syntax.CallExpr) bool {
	if sharingLowering == "params" || o.pre <= w.preOf(call) || o.pre >= w.postOf(call) {
		return false
	}
	saved := *o
	w.classify(vv, o)
	path, kind := o.path, o.kind
	*o = saved
	if kind == occWrite || path == nil {
		return false
	}
	t := w.typeOf(path)
	if _, ptr := t.(*check.Pointer); ptr || t == nil {
		return false
	}
	return !w.l.holdsValue(t)
}

// handedArg is the argument that the rebinding p = f(…, p, …) hands over
// to f with no copy (V7), or nil. The fact is general; it names no
// function and no package:
//
//   - the call is the whole right side of a plain assignment to one place
//     p, rooted at a let mut local or a borrow, and one operand is that
//     same place: an argument, or a dot call's receiver that f takes by
//     value (p = p.m(…));
//   - the statement names p's root nowhere else, nor does a frame other
//     than the statement's, so p's old value is dead from the call to the
//     store-back: nothing reads it but f;
//   - f is pure and has no failure row, so nothing f runs can observe p's
//     old value, and the store-back always follows the call;
//   - f writes no storage of its argument that p's length covers, and its
//     result shares no storage but p's: for an Ergo body, f is pure
//     (isPure), owns no parameter (V5 hands those over itself), and its
//     results are fresh when that parameter holds an unshared value
//     (freshFrom), so a parameter it binds to a let mut is copied in its
//     body and no other argument, package let or capture reaches its
//     result; for an extern bound to a pure Go body, the body keeps
//     nothing of its arguments but its result and writes no element of
//     them that their length covers (stdlib.md, Pure bodies), and every
//     other argument holds no slice or Map.
//
// So nothing reads the storage f received but f's result, which is the
// new value of p: p = f(p, s) through a borrow appends in place, as
// p = p.Append(x) does (V3), where a copy of p at each call would make a
// loop of them quadratic. The copying lowering ("copy") hands nothing
// over, and is the reference of TestValueRuleAgrees.
func (w *valueWalk) handedArg(call *syntax.CallExpr) syntax.Expr {
	if !valueOwned() {
		return nil
	}
	c := w.info.Calls[call]
	if c == nil || c.Func == nil || c.Func.Sig == nil || len(rowElems(c.Func.Sig.Errors)) > 0 || call.Trailing != nil {
		return nil
	}
	if c.Recv != nil {
		// A dot call p = p.m(…): the receiver is the first operand, unless
		// m takes it by borrow.
		if len(c.Func.Sig.Params) == 0 {
			return nil
		}
		if _, ptr := c.Func.Sig.Params[0].Type.(*check.Pointer); ptr {
			return nil
		}
	}
	ergo := c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil
	if ergo && (w.vf.ownCall[call] || !w.l.isPure(c.Func)) {
		return nil
	}
	parent, child := w.unparenParent(call)
	s, ok := parent.(*syntax.AssignStmt)
	if !ok || s.Tok != syntax.ASSIGN || len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Rhs[0] != child {
		return nil
	}
	lid := pathRoot(s.Lhs[0])
	if lid == nil {
		return nil
	}
	v, ok := w.info.Uses[lid].(*check.Var)
	if !ok {
		return nil
	}
	vv := w.varOf(v)
	if !vv.root() {
		return nil
	}
	var arg syntax.Expr
	at := -1
	ops := callOperands(call, c)
	for i, a := range ops {
		if aid := pathRoot(a); aid != nil && w.info.Uses[aid] == check.Object(v) {
			if arg != nil || !samePlace(s.Lhs[0], a) {
				return nil
			}
			arg, at = a, i
		} else if !ergo && w.l.holdsValue(w.typeOf(a)) {
			return nil // the result could share another argument's storage
		}
	}
	if arg == nil {
		return nil
	}
	if ergo && valueMutant != "hand-shared" && !w.l.freshFrom(c.Func, at, len(ops)) {
		return nil
	}
	aid := pathRoot(arg)
	var frame *vframe
	for _, o := range vv.occs {
		if o.id == lid {
			frame = o.frame
		}
	}
	for _, o := range vv.occs {
		if o.frame != frame {
			return nil
		}
		if o.id != lid && o.id != aid && o.pre > w.preOf(s) && o.pre < w.postOf(s) {
			return nil
		}
	}
	return arg
}

// ownedParams are the owned parameters of f (V5), in order: a parameter
// whose value holds a slice or Map and that f's body takes into a let mut
// local once, at a bind in the body's own frame and outside any loop, and
// otherwise only reads, in the body's own frame, in uses that keep
// nothing or as the value of a let or let mut (`let s = m.S`), before the
// bind or after it in a statement that ends before the first use of the
// local (beforeWrites), so the parameter and the local hold one value at
// each use. A let that keeps a part is decided as a kept read of the
// local (ownedKept): copied when a write through it can follow. After
// the bind no failure can leave f: no call that can fail at its site, and
// no returned value of an error type, so a caller never sees a value f
// wrote and then failed on. f lowers as a Go function, not a method.
//
// Such an f lowers twice (emitFuncBody): its body, under the name
// ownedName, whose bind copies nothing, and a wrapper under f's own name
// that copies each owned argument and calls it. A call this analysis
// decided (ownedCall) calls the body and passes each owned argument
// unshared: a copy unless it is fresh, a dead local, or the old value a
// rebinding hands over (consumable). Every other route to f (a function
// value, an operations conversion, a witness, a context protocol) reaches
// the wrapper, which copies as before. The copy moves from the callee to
// the caller that shares, and a rebinding needs none.
func (l *lowerer) ownedParams(fn *check.Func) []ownedParam {
	if !valueOwned() || fn == nil || fn.Decl == nil {
		return nil
	}
	d := fn.Decl
	vf := l.valueFacts()
	if p, ok := vf.owned[d]; ok {
		return p
	}
	vf.owned[d] = nil
	if d.Body == nil || d.Extern != nil || fn.Pkg == nil || fn.Pkg.Info == nil || d.Type == nil || d.Type.Params == nil {
		return nil
	}
	info := fn.Pkg.Info
	f, ok := info.Defs[d.Name].(*check.Func)
	if !ok || f.Sig == nil {
		return nil
	}
	saved := l.info
	l.info = info
	defer func() { l.info = saved }()
	w := newValueWalk(l, info, vf, d)
	w.root = &vframe{node: d, tails: map[syntax.Expr]bool{}}
	tailsOf(d.Body.List, w.root.tails)
	w.walk(d, nil)
	var out []ownedParam
	i := 0
	for _, field := range d.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			names = []*syntax.Ident{nil}
		}
		for _, id := range names {
			if id != nil {
				if v, ok := info.Defs[id].(*check.Var); ok && w.vars[v] != nil && l.holdsValue(v.Type) {
					if bind := w.ownedBind(f, w.vars[v]); bind != nil {
						out = append(out, ownedParam{index: i, bind: bind, v: v})
					} else if use := w.consumedUse(f, w.vars[v]); use != nil && valueMutant != "consume-blind" {
						out = append(out, ownedParam{index: i, bind: use, consumed: true, v: v})
					} else if w.fieldOwned(f, w.vars[v]) {
						// Taken over through its fields (handover.go).
						out = append(out, ownedParam{index: i, consumed: true, v: v})
					} else if w.cutOwned(f, w.vars[v]) {
						// Taken over as a cut of itself (cut.go).
						out = append(out, ownedParam{index: i, consumed: true, v: v})
					}
				}
			}
			i++
		}
	}
	vf.owned[d] = out
	return out
}

// consumedParams makes each consumed owned parameter (consumedUse) of the
// function root a local of the root frame that holds an unshared value:
// its callers pass it unshared (V5), and its body reads it once.
func (w *valueWalk) consumedParams(root syntax.Node) {
	d, ok := root.(*syntax.FuncDecl)
	if !ok || d.Name == nil {
		return
	}
	f, ok := w.info.Defs[d.Name].(*check.Func)
	if !ok {
		return
	}
	for _, p := range w.l.ownedParams(f) {
		if vv := w.vars[p.v]; p.consumed && vv != nil {
			vv.local, vv.handed, vv.frame, vv.loops = true, true, w.root, nil
		}
	}
}

// consumes decides the calls of a runtime/v1 body's owned form: the first
// operand is consumed when it is fresh, so no binding reads its storage
// after the call (a local dead after it and never shared, a consumed
// parameter, a literal, a fresh result or a copy).
func (w *valueWalk) consumes() {
	if !valueOwned() {
		return
	}
	for _, call := range w.pure {
		c := w.info.Calls[call]
		if c == nil || call.Trailing != nil || w.l.ownedForm(c.Func) == "" {
			continue
		}
		if ops := callOperands(call, c); len(ops) > 0 && (valueMutant == "consume-any" || w.fresh(ops[0])) {
			w.vf.consumes[call] = true
		}
	}
}

// consumedUse is the use of the parameter vv of f that a call consumes
// (types.md, Bindings 2, consumed arguments), or nil: vv is read once, or
// once in each arm of a match or an if (exclusive: one run of the
// statement runs one of them; GP2b's apply passes b on in each arm), in
// the body's own frame and outside any loop, as an operand of a call,
// itself or a field path of it, at a position the call consumes
// (consumedAt); and f cannot fail after that read. Such a parameter is
// owned (V5): its callers pass it unshared, so the call it reaches takes
// it over. The use given is the first.
func (w *valueWalk) consumedUse(f *check.Func, vv *vvar) syntax.Expr {
	if vv.ptr || vv.local || len(vv.occs) == 0 {
		return nil
	}
	var first syntax.Expr
	for i, o := range vv.occs {
		for _, prev := range vv.occs[:i] {
			if !w.exclusive(prev.id, o.id) {
				return nil
			}
		}
		use := w.consumedOcc(f, o)
		if use == nil {
			return nil
		}
		if first == nil {
			first = use
		}
	}
	return first
}

// consumedOcc is the operand a call consumes at the use o of a parameter
// of f (consumedUse), or nil.
func (w *valueWalk) consumedOcc(f *check.Func, o *vocc) syntax.Expr {
	if o.frame != w.root || len(o.loops) > 0 {
		return nil
	}
	x := syntax.Expr(o.id)
	var use syntax.Expr
	for use == nil {
		switch p := w.parentOf(x).(type) {
		case *syntax.ParenExpr:
			x = p
		case *syntax.SelectorExpr:
			if p.X != x {
				return nil
			}
			if call, ok := w.parentOf(p).(*syntax.CallExpr); ok && call.Fun == syntax.Expr(p) {
				// A dot call: x is its receiver.
				use = w.consumedAt(call, x)
				if use == nil {
					return nil
				}
				continue
			}
			if _, field := w.typeOf(p.X).(*check.Named); !field {
				return nil
			}
			x = p
		case *syntax.CallExpr:
			if use = w.consumedAt(p, x); use == nil {
				return nil
			}
		default:
			return nil
		}
	}
	if len(rowElems(f.Sig.Errors)) > 0 && w.failsAfter(f, o.pre) {
		return nil
	}
	return use
}

// consumedAt is x when it is the operand of call at a position the call
// consumes, or nil: the first operand of a std function whose runtime/v1
// body has an owned form (v1.Body.Owned), or an owned parameter (V5) of a
// function with an Ergo body.
func (w *valueWalk) consumedAt(call *syntax.CallExpr, x syntax.Expr) syntax.Expr {
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || call.Trailing != nil {
		return nil
	}
	ops := callOperands(call, c)
	at := -1
	for i, op := range ops {
		if op == x {
			at = i
		}
	}
	switch {
	case at < 0:
		return nil
	case at == 0 && w.l.ownedForm(c.Func) != "":
		return x
	}
	for _, p := range w.l.ownedParams(c.Func) {
		if p.index == at {
			return x
		}
	}
	return nil
}

// ownedForm is the owned form of the runtime/v1 body that the std
// function f binds (v1.Body.Owned), or "".
func (l *lowerer) ownedForm(f *check.Func) string {
	if !l.pureV1(f) {
		return ""
	}
	_, name := f.Decl.Extern.Split()
	body, err := v1.BodyOf(name)
	if err != nil {
		return ""
	}
	return body.Owned
}

// ownedBind is the value of the one bind that takes the parameter vv of f
// (ownedParams), or nil.
func (w *valueWalk) ownedBind(f *check.Func, vv *vvar) syntax.Expr {
	if vv.ptr || vv.local {
		return nil
	}
	var bind *vocc
	var value syntax.Expr
	var into *vvar
	for _, o := range w.owns {
		id, ok := unparen(o.value).(*syntax.Ident)
		if !ok || w.info.Uses[id] != check.Object(vv.v) {
			continue
		}
		if bind != nil || !o.root.local || !o.root.mut || o.root.ptr {
			return nil
		}
		for _, oc := range vv.occs {
			if oc.id == id {
				bind = oc
			}
		}
		value, into = o.value, o.root
	}
	if bind == nil || bind.frame != w.root || len(bind.loops) > 0 {
		return nil
	}
	for _, o := range vv.occs {
		if o == bind {
			continue
		}
		if w.returnsBefore(o, bind) {
			continue
		}
		if o.frame != w.root && !w.joinedBefore(o.frame) {
			return nil
		}
		if o.pre > bind.pre && valueMutant != "own-live" && (o.frame != w.root || !w.beforeWrites(o, bind, into)) {
			return nil
		}
		w.classify(vv, o)
		if o.kind != occRead && !w.letValue(o) {
			return nil
		}
	}
	if len(rowElems(f.Sig.Errors)) > 0 && w.failsAfter(f, bind.pre) {
		return nil
	}
	return value
}

// joinedBefore reports whether the frame f, nested in the root, has ended
// once the call that opened it returns: f and each frame between it and
// the root is a function literal or trailing block that is an operand of
// a call (openingCall), and either cannot outlive that call (opens: not
// escaping) or is passed to a std function whose runtime/v1 body joins
// the children it starts (joinsChildren: tasks.Transform). Then nothing
// runs f after that call returns, so what f captures is no longer read:
// a task's captures end at its join. An occurrence in f that comes before
// a bind (ownedBind) is then a use that ends before the bind, as one in
// the root frame is: bindIn's let mut out = s after tasks.Transform's
// callback read s.
func (w *valueWalk) joinedBefore(f *vframe) bool {
	for ; f != nil && f != w.root; f = f.parent {
		call := w.openingCall(f.node)
		if call == nil || f.escaping && !w.joinsChildren(call) {
			return false
		}
	}
	return f == w.root
}

// openingCall is the call whose operand, callee or trailing block the
// frame node n is, or nil: a let-bound literal may run at any later call.
func (w *valueWalk) openingCall(n syntax.Node) *syntax.CallExpr {
	if b, ok := n.(*syntax.BlockStmt); ok {
		if call, ok := w.parentOf(b).(*syntax.CallExpr); ok && call.Trailing == b {
			return call
		}
		return nil
	}
	parent, child := w.unparenParent(n)
	call, ok := parent.(*syntax.CallExpr)
	if !ok {
		return nil
	}
	if call.Fun == child {
		return call
	}
	for _, a := range call.Args {
		if a == child {
			return call
		}
	}
	return nil
}

// joinsChildren reports a call of an extern whose runtime body (its body
// contract, check.BodyOf) states the child waiting policy (v1.WaitKinds()):
// the caller is the parent of the tasks the body starts, and the body
// joins them before it returns, running its function operands only on
// itself and on them. So an escaping literal it is given runs no more once
// the call returns.
func (w *valueWalk) joinsChildren(call *syntax.CallExpr) bool {
	c := w.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc {
		return false
	}
	name := check.BodyOf(c.Func).Runtime
	if name == "" {
		return false
	}
	body, err := runtimeBody(name)
	return err == nil && body.Wait == "child"
}

// letValue reports the use o as the whole value of a one-name let or let
// mut: a part of the parameter that a local keeps.
func (w *valueWalk) letValue(o *vocc) bool {
	parent, child := w.unparenParent(o.path)
	spec, ok := parent.(*syntax.VarSpec)
	return ok && spec.Pattern == nil && len(spec.Names) == 1 && len(spec.Values) == 1 && syntax.Node(spec.Values[0]) == child
}

// beforeWrites reports the use o, after the bind of an owned parameter to
// the let mut into, as one whose statement ends before the first use of
// into: up to there the parameter and the local hold one value, which no
// write has changed. o is outside any loop, in a statement of the body's
// own block. The declared name of into precedes bind, and so does no
// use.
func (w *valueWalk) beforeWrites(o, bind *vocc, into *vvar) bool {
	d, ok := w.root.node.(*syntax.FuncDecl)
	if !ok || d.Body == nil || len(o.loops) > 0 {
		return false
	}
	stmt := syntax.Node(o.id)
	for w.parentOf(stmt) != syntax.Node(d.Body) {
		if stmt = w.parentOf(stmt); stmt == nil {
			return false
		}
	}
	for _, u := range into.occs {
		if u.pre > bind.pre && u.pre < w.postOf(stmt) {
			return false
		}
	}
	return true
}

// ownedKept decides the copies of the parts of the body's owned
// parameters that a let keeps (ownedBind): the parameter and the local
// its bind takes it into hold one value, so such a let reads the local's
// value and is copied as a kept read of the local is (V2), when a write
// through the local can follow it.
func (w *valueWalk) ownedKept() {
	for _, b := range w.ownedBinds() {
		for _, o := range b.param.occs {
			if o.path == b.bind || o.kind != occEscape || !w.letValue(o) || w.vf.moved[o.path] {
				continue
			}
			if w.needsCopy(b.into, o) {
				w.vf.clone[o.path] = true
			}
		}
	}
}

// ownedBound is an owned parameter of the body (param) that its bind
// (bind, the let mut's value) takes into the let mut into.
type ownedBound struct {
	param, into *vvar
	bind        syntax.Expr
}

// ownedBinds are the body's owned parameters that a let mut takes whole
// (ownedBind).
func (w *valueWalk) ownedBinds() []ownedBound {
	d, ok := w.root.node.(*syntax.FuncDecl)
	if !ok || d.Name == nil {
		return nil
	}
	f, ok := w.info.Defs[d.Name].(*check.Func)
	if !ok {
		return nil
	}
	var out []ownedBound
	for _, p := range w.l.ownedParams(f) {
		vv := w.vars[p.v]
		if p.consumed || p.bind == nil || vv == nil {
			continue
		}
		for _, o := range w.owns {
			if o.value == p.bind {
				out = append(out, ownedBound{vv, o.root, p.bind})
			}
		}
	}
	return out
}

// failsAfter reports whether f's body can fail after the point at: a call
// that can fail at its site, or a returned value whose type is not f's
// result (an error value converts to the row).
func (w *valueWalk) failsAfter(f *check.Func, at int) bool {
	fails := false
	result := check.Type(nil)
	if len(f.Sig.Results) == 1 {
		result = f.Sig.Results[0]
	}
	returned := func(x syntax.Expr) {
		if w.preOf(x) <= at {
			return
		}
		switch t := w.typeOf(x); t.(type) {
		case *check.Named, *check.TypeParam:
			// An error type is named, or a type parameter of the row.
			if t != result {
				fails = true
			}
		}
	}
	for x := range w.root.tails {
		returned(x)
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			if c := w.info.Calls[n]; c != nil && w.preOf(n) > at && failsHere(c) {
				fails = true
			}
		case *syntax.ReturnStmt:
			if w.frameOfReturn(n) == w.root {
				for _, x := range n.Results {
					returned(x)
				}
			}
		}
		return true
	})
	return fails
}

// ownedKey is the package-scope key of the body of f, which has owned
// parameters (ownedParams); f's own key names its wrapper.
func ownedKey(f *check.Func) pkgKey { return pkgKey{"owned", f} }

// reserveOwned gives, in the naming pass, the name of the body of each
// function of pkgs that has owned parameters.
func (l *lowerer) reserveOwned(pkgs []*check.Package) {
	if !valueOwned() {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if len(l.ownedParams(f)) > 0 {
				l.pkgName(ownedKey(f), f.Name+"Owned")
			}
		}
	}
}

// ownedWrapper is the Go declaration of f's wrapper (ownedParams): head is
// f's Go head up to its parameters' opening parenthesis, params its
// parameter list, results its result list, typeArgs the names of its type
// parameters, and args its Go operands in order (the environment, the
// destination and the witnesses first, then one per parameter; the wrapper
// copies each owned one). It calls body, the owned body. For a method,
// recv names the receiver, the first parameter, which args leaves out,
// and body is a method of it.
func (fc *funcCtx) ownedWrapper(f *check.Func, head, params, results, body string, typeArgs []string, args []string, recv string, site syntax.Node) string {
	owned := map[int]bool{}
	for _, p := range fc.l.ownedParams(f) {
		owned[p.index] = true
	}
	first := 0
	if recv != "" {
		first = 1
		if owned[0] {
			recv = fc.reportCopy(site, fc.cloneValue(f.Sig.Params[0].Type, recv, site), whyWrapper)
		}
	}
	lead := len(args) - (len(f.Sig.Params) - first)
	var ops []string
	for i, a := range args {
		if j := i - lead + first; i >= lead && owned[j] {
			a = fc.reportCopy(site, fc.cloneValue(f.Sig.Params[j].Type, a, site), whyWrapper)
		}
		ops = append(ops, a)
	}
	if len(typeArgs) > 0 {
		body += "[" + strings.Join(typeArgs, ", ") + "]"
	}
	if recv != "" {
		body = "(" + recv + ")." + body
	}
	call := body + "(" + strings.Join(ops, ", ") + ")"
	if strings.TrimSpace(results) != "" {
		call = "return " + call
	}
	return head + params + results + " {\n\t" + call + "\n}\n\n"
}

// samePlace reports whether a and b are one place: a variable and its
// fields, through parentheses and indirections.
func samePlace(a, b syntax.Expr) bool {
	a, b = unparen(a), unparen(b)
	if sa, ok := a.(*syntax.StarExpr); ok {
		a = unparen(sa.X)
	}
	if sb, ok := b.(*syntax.StarExpr); ok {
		b = unparen(sb.X)
	}
	switch x := a.(type) {
	case *syntax.Ident:
		y, ok := b.(*syntax.Ident)
		return ok && x.Name == y.Name
	case *syntax.SelectorExpr:
		y, ok := b.(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && samePlace(x.X, y.X)
	}
	return false
}

// sameElem reports a and b as one element place of a slice, p[k] (or a
// field path under one, p[k].f), whose index is the same value at both:
// a literal, or a path of fields from an immutable variable
// (LWR-ELEM-APPEND, REUSE RU8). An element holds its array alone, as a
// root does (V1: what enters a root is fresh or copied), so
// gs[j] = gs[j].Append(x) extends it in place.
func (w *valueWalk) sameElem(a, b syntax.Expr) bool {
	if !entryRule() {
		return false
	}
	a, b = unparen(a), unparen(b)
	switch x := a.(type) {
	case *syntax.SelectorExpr:
		y, ok := b.(*syntax.SelectorExpr)
		return ok && x.Sel.Name == y.Sel.Name && (samePlace(x.X, y.X) || w.sameElem(x.X, y.X))
	case *syntax.IndexExpr:
		y, ok := b.(*syntax.IndexExpr)
		if !ok || len(x.Indices) != 1 || len(y.Indices) != 1 || !w.stableKey(x.Indices[0]) || !sameKey(w.info, x.Indices[0], y.Indices[0]) {
			return false
		}
		switch underEqual(w.typeOf(x.X)).(type) {
		case *check.Slice, *check.Array:
		default:
			return false
		}
		return samePlace(x.X, y.X) || w.sameElem(x.X, y.X)
	}
	return false
}

// stableKey reports a key that names the same value wherever the body
// reads it: a literal, or a path of fields from an immutable variable.
func (w *valueWalk) stableKey(k syntax.Expr) bool {
	switch k := unparen(k).(type) {
	case *syntax.BasicLit:
		return true
	case *syntax.Ident, *syntax.SelectorExpr:
		kv, ok := w.info.Uses[pathRoot(k)].(*check.Var)
		return ok && fieldPath(k) && !w.varOf(kv).mut && !w.varOf(kv).ptr
	}
	return false
}

// slicesOf reports x = x[a:b] (any depth of slicing), which keeps x's own
// array: the old value is overwritten, so nothing else shares it.
func (w *valueWalk) slicesOf(e syntax.Expr, root *vvar) bool {
	s, ok := unparen(e).(*syntax.SliceExpr)
	if !ok {
		return false
	}
	for {
		switch x := unparen(s.X).(type) {
		case *syntax.Ident:
			v, ok := w.info.Uses[x].(*check.Var)
			return ok && w.vars[v] == root
		case *syntax.SliceExpr:
			s = x
		default:
			return false
		}
	}
}

// classify sets o's path and kind.
func (w *valueWalk) classify(vv *vvar, o *vocc) {
	cur := syntax.Node(o.id)
	for {
		p := w.parentOf(cur)
		switch p := p.(type) {
		case *syntax.ParenExpr:
			cur = p
			continue
		case *syntax.SelectorExpr:
			if p.X == cur && w.dotCall(cur) == nil {
				cur = p
				continue
			}
		case *syntax.IndexExpr:
			if p.X == cur && len(p.Indices) == 1 {
				cur = p
				continue
			}
		case *syntax.SliceExpr:
			if p.X == cur {
				cur = p
				continue
			}
		case *syntax.StarExpr:
			if p.X == cur {
				cur = p
				continue
			}
		case *syntax.CallExpr:
			if c := w.info.Calls[p]; c != nil && c.Kind == check.CallConversion && len(p.Args) == 1 && p.Args[0] == cur {
				cur = p
				continue
			}
		}
		break
	}
	o.path = cur.(syntax.Expr)
	parent, child := w.unparenParent(cur)
	if call := w.dotCall(cur); call != nil {
		// The receiver of a dot call x.f(…) is its first operand.
		parent, child = call, cur
	}
	o.kind = occEscape
	switch p := parent.(type) {
	case *syntax.AssignStmt:
		for _, l := range p.Lhs {
			if l == child {
				o.kind, o.at = occWrite, w.postOf(p)
				return
			}
		}
	case *syntax.UnaryExpr:
		if p.Op == syntax.AND {
			o.kind, o.at = occWrite, w.postOf(w.enclosingCall(p))
			return
		}
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c != nil && c.Kind == check.CallFunc && c.Recv == child && c.Func.Ptr {
			if _, isPtr := w.typeOf(o.path).(*check.Pointer); !isPtr {
				o.kind, o.at = occWrite, w.postOf(p)
				return
			}
		}
		if c != nil && w.vf.inPlace[p] && w.recvOf(p) == child {
			o.kind = occPart
			return
		}
		if x, ok := child.(syntax.Expr); ok && w.vf.consumed[x] {
			o.kind = occPart
			return
		}
	}
	t := w.typeOf(o.path)
	if !w.l.holdsValue(t) {
		o.kind = occRead
		return
	}
	switch p := parent.(type) {
	case *syntax.BinaryExpr:
		if p.Op == syntax.EQL || p.Op == syntax.NEQ {
			o.kind = occRead
		}
	case *syntax.InterpolatedString:
		o.kind = occRead
	case *syntax.RangeStmt:
		if p.X == child {
			if l := w.info.Loops[p]; l != nil && !w.l.holdsValue(l.Elem) {
				o.kind = occRead
			}
		}
	case *syntax.ReturnStmt:
		if o.frame.node != nil && w.frameOfReturn(p) == o.frame {
			o.kind = occReturn
		}
	case *syntax.ExprStmt:
		if f := w.frameOfTail(p.X); f != nil && f == o.frame {
			o.kind = occReturn
		}
	case *syntax.TupleExpr, *syntax.CompositeLit, *syntax.KeyValueExpr:
		// A part of the value its frame returns leaves with the frame, as
		// the value itself does (V6: write's (Changed, u)).
		if w.returnNode(o, o.path) != nil {
			o.kind = occReturn
		}
	case *syntax.CallExpr:
		c := w.info.Calls[p]
		if c == nil {
			break
		}
		switch {
		case c.Kind == check.CallLen:
			o.kind = occRead
		case c.Kind == check.CallFunc && (check.GoBuiltin(c.Func) == "len" || check.GoBuiltin(c.Func) == "cap"):
			// A binding of Go's len or cap reads only the slice header
			// (SL2b: a copy would change cap's answer).
			o.kind = occRead
		case c.Kind == check.CallFunc && preludeGet(c.Func):
			// The entry rule (V8): no write through the root reaches the
			// entry Get keeps, so the receiver is a read, copied only
			// when a later operand writes the root (inFlight).
			// A Map whose entries the program writes in place (V9) gives
			// an entry a later write can reach: a Get whose result is
			// kept copies it, when a match takes the result apart, or
			// else its receiver (moveout.go).
			if w.recvOf(p) != child {
				break
			}
			switch {
			case !w.l.holdsValue(elemOf(t)) || entryRule() && (!w.l.entryWritten(t) || w.keepsNothing(p)):
				// The receiver is still copied when a later operand
				// writes the root, under the reference too: Go reads
				// r.m after bump(&r, k) in Get(r.m, bump(&r, k)).
				o.kind, o.call = occRead, p
			case entryRule() && w.matchedGet(p):
				o.call, o.entry = p, true
			}
		case c.Kind == check.CallFunc && (appendsSource(c.Func) || writesMapSource(c.Func) || takesMapSource(c.Func)):
			// A bulk append's ys is read like its source: its elements
			// are copied into the result (SL2).
			if (w.recvOf(p) == child || check.AppendSpreads(c.Func)) && !w.l.holdsValue(elemOf(t)) {
				o.kind = occRead
			}
		case c.Kind == check.CallFunc && c.Func != nil && basicResult(w.typeOf(p)) && w.l.isPure(c.Func):
			o.kind, o.call = occRead, p
		case c.Kind == check.CallFunc && c.Func != nil && (w.l.isPure(c.Func) || pureStdBody(c.Func)) && (valueMutant == "share-blind" || !w.l.mayShare(w.typeOf(p), t)):
			// A pure call keeps nothing of its argument past its result,
			// and a result that reaches no Map or slice type the argument
			// reaches shares none of it (V6: find(t, path) is a place).
			o.kind, o.call = occRead, p
		case aliasLowering != "type" && c.Kind == check.CallFunc && viewExtern(c.Func) && w.recvOrFirst(p, c) == child:
			// A view (gobody.go, goBodyView) keeps its operand only in
			// its result, a window: the window is copied, not the operand
			// (wf10 DF1). A window a call reads and keeps nothing of is
			// not copied at all, unless a write to the root runs before
			// that call ends (inFlight, by o.call).
			if outer := w.windowRead(p); outer != nil {
				o.kind, o.call = occRead, outer
			} else {
				// A window read at once (readonly.go) copies nothing
				// either (MAPCOPY, slices.Slice(r.s, 0, 2)[1]).
				o.call, o.deps, o.result = p, true, true
				w.readAtOnce(o, p)
			}
		case writtenOperand(p, c) == child:
			// The slice a Go call writes in place (GBX2) is no escape: Go
			// keeps no reference to it (v1.GoWrites). Whether Go gets it
			// or a copy is the boundary rule's (goArgs: freshen), which
			// reads its liveness after the call (S2-COPY).
			o.kind, o.call = occRead, p
		case c.Kind == check.CallFunc && c.Func != nil && goReadsAt(p, c, child):
			// An operand Go only reads (a reads fact, LF1) is no escape
			// either: Go neither writes nor keeps it (S2-COPY: Write(b[:n])
			// leaves b fresh for `buf = b`).
			o.kind, o.call = occRead, p
		case w.unkept(p, c, child) && !w.l.isPure(c.Func):
			// The callee keeps nothing of it (keeps.go), unless it sees a
			// write to the root during the call (decide, callWrites).
			o.kind, o.call, o.unkept = occRead, p, true
		case aliasLowering != "type" && c.Kind == check.CallFunc && c.Func != nil && w.l.isPure(c.Func):
			// The result-alias summary (alias.go): a result that does not
			// depend on the argument keeps nothing of it; one that does
			// keeps it only in the result, which is copied instead.
			if j := operandIndex(p, c, child); j >= 0 && !w.l.ownedAt(c.Func, j) {
				o.call, o.deps = p, true
				if w.l.resultDeps(c.Func)[j] && valueMutant != "deps-blind" {
					// A function or operation value in the result hides
					// what it captured: a copy of the result copies none of
					// it, so the argument is copied (`let g = hold(xs)`).
					o.result = !hidesParts(w.typeOf(p))
					w.readAtOnce(o, p)
				} else {
					o.kind = occRead
				}
			}
		case entryRule() && aliasLowering != "type" && c.Kind == check.CallFunc && pureStdBody(c.Func) && w.l.copyCovers(w.typeOf(p)):
			// A pure std body keeps its argument only in its result, as
			// a pure Ergo body does: the result is copied instead (V8).
			// Its Go body has no summary, so every argument counts.
			o.call, o.result = p, true
			w.readAtOnce(o, p)
		}
	}
}

// writtenOperand is the operand of the Go call x that Go writes in place
// (check.GoWrites; a method's receiver is operand 0), or nil.
func writtenOperand(x *syntax.CallExpr, c *check.Call) syntax.Node {
	if c.Kind != check.CallFunc || c.Func == nil || c.Func.Decl == nil || (c.Recv == nil) != (c.Func.Decl.Recv == nil) {
		return nil
	}
	i, ok := check.GoWrites(c.Func)
	ops := callOperands(x, c)
	if !ok || i >= len(ops) {
		return nil
	}
	return ops[i]
}

// goReadsAt reports a reads fact, checked or validated, of the operand
// child of the Go call x.
func goReadsAt(x *syntax.CallExpr, c *check.Call, child syntax.Node) bool {
	j := operandIndex(x, c, child)
	if j < 0 || check.GoFacts(c.Func) == nil {
		return false
	}
	ok, _ := goReads(x, c.Func, j)
	return ok
}

// recvOrFirst is the first operand of the call x: a dot call's receiver,
// else its first argument.
func (w *valueWalk) recvOrFirst(x *syntax.CallExpr, c *check.Call) syntax.Node {
	ops := callOperands(x, c)
	if len(ops) == 0 {
		return nil
	}
	return ops[0]
}

// windowRead is the call that reads the window x, a view's result, and
// keeps nothing of it, or nil: x is an operand of len, or of a pure call,
// Ergo or a std runtime body, whose result is a basic value or reaches no
// Map or slice type x reaches, at a parameter it does not own (V5).
func (w *valueWalk) windowRead(x *syntax.CallExpr) *syntax.CallExpr {
	parent, child := w.unparenParent(x)
	call, ok := parent.(*syntax.CallExpr)
	if !ok || call.Fun == child {
		return nil
	}
	c := w.info.Calls[call]
	if c == nil {
		return nil
	}
	if c.Kind == check.CallLen {
		return call
	}
	if c.Kind != check.CallFunc || c.Func == nil || w.vf.inPlace[call] || w.vf.consumed[x] {
		return nil
	}
	ergo := c.Func.Decl != nil && c.Func.Decl.Body != nil && c.Func.Decl.Extern == nil
	if !(ergo && w.l.isPure(c.Func)) && !(pureStdBody(c.Func) && !c.Func.Pkg.Host() && !appendsSource(c.Func) && !writesMapSource(c.Func)) {
		return nil
	}
	if j := operandIndex(call, c, child); j < 0 || ergo && w.l.ownedAt(c.Func, j) {
		return nil
	}
	if basicResult(w.typeOf(call)) || !w.l.mayShare(w.typeOf(call), w.typeOf(x)) {
		return call
	}
	return nil
}

// entryShared reports, under the entry rule (V8), a type through whose
// parts a Map whose entries hold a Map or slice is reached: two values of
// it may share such an entry, which only cloneDeep copies.
func (l *lowerer) entryShared(t check.Type) bool {
	if !entryRule() {
		return false
	}
	var walk func(t check.Type, seen map[check.Type]bool) bool
	walk = func(t check.Type, seen map[check.Type]bool) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		switch u := underEqual(t).(type) {
		case *check.Map:
			return l.holdsValue(u.Elem) || walk(u.Elem, seen)
		case *check.Slice:
			return walk(u.Elem, seen)
		case *check.Array:
			return walk(u.Elem, seen)
		case *check.Tuple:
			for _, e := range u.Types {
				if walk(e, seen) {
					return true
				}
			}
		case *check.Struct:
			for _, f := range u.Fields {
				if walk(f.Type, seen) {
					return true
				}
			}
		}
		return false
	}
	return walk(t, map[check.Type]bool{})
}

// copyCovers reports whether cloneValue's copy of a value of type t copies
// every Map and slice of it that a write path of another value can reach:
// its parts are reached through fields, tuple parts and the elements of
// arrays and slices, down to a Map, whose entries have no write path
// (V8), or to a basic value. A union, an Option, a type parameter, a
// function and an interface hide their parts from cloneValue, so a copy of
// a value that holds one is not taken to copy what it shares.
func (l *lowerer) copyCovers(t check.Type) bool {
	return copyCoversSeen(t, map[check.Type]bool{})
}

func copyCoversSeen(t check.Type, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return true
	}
	seen[t] = true
	switch u := underEqual(t).(type) {
	case *check.Basic, *check.Map:
		return true
	case *check.Slice:
		return copyCoversSeen(u.Elem, seen)
	case *check.Array:
		return copyCoversSeen(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if !copyCoversSeen(e, seen) {
				return false
			}
		}
		return true
	case *check.Struct:
		for _, f := range u.Fields {
			if !copyCoversSeen(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// dotCall is the call whose receiver is x, a dot call x.f(…) or an
// operation call, or nil.
func (w *valueWalk) dotCall(x syntax.Node) *syntax.CallExpr {
	sel, ok := w.parentOf(x).(*syntax.SelectorExpr)
	if !ok || syntax.Node(sel.X) != x {
		return nil
	}
	parent, child := w.unparenParent(sel)
	call, ok := parent.(*syntax.CallExpr)
	if !ok || call.Fun != child {
		return nil
	}
	if c := w.info.Calls[call]; c != nil && c.Recv != nil && syntax.Node(c.Recv) == x {
		return call
	}
	return nil
}

// basicResult reports a result that can keep no part of an argument: a
// basic type, or no value.
func basicResult(t check.Type) bool {
	if t == nil {
		return true
	}
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	switch u := t.(type) {
	case *check.Basic:
		return true
	case *check.Tuple:
		return len(u.Types) == 0
	}
	return false
}

// enclosingCall is the nearest call that holds n, or n.
func (w *valueWalk) enclosingCall(n syntax.Node) syntax.Node {
	for p := w.parentOf(n); p != nil; p = w.parentOf(p) {
		if _, ok := p.(*syntax.CallExpr); ok {
			return p
		}
	}
	return n
}

// frameOfReturn is the frame a return statement leaves.
func (w *valueWalk) frameOfReturn(n syntax.Node) *vframe {
	for p := n; p != nil; p = w.parentOf(p) {
		if f := w.frameOf[p]; f != nil {
			return f
		}
	}
	return w.root
}

// frameOfTail is the frame whose result x is, or nil.
func (w *valueWalk) frameOfTail(x syntax.Expr) *vframe {
	f := w.frameOfReturn(x)
	if f.tails[x] {
		return f
	}
	return nil
}

// inFlight reports whether a write to vv runs between the use o, an
// operand of the call o.call, and the call itself: the write's place lies
// in a later operand of the call (f(c, bump(&c)), or a literal there that
// writes c). A read of c that keeps nothing past the call still must not
// share c with the callee then: the callee would see the write.
func (w *valueWalk) inFlight(vv *vvar, o *vocc) bool {
	end := w.postOf(o.call)
	for _, wr := range vv.writes {
		if wr.pre > o.pre && wr.pre < end {
			return true
		}
	}
	// A borrow passed on whole to a call (bump(r, k), r.bump(k)) may be
	// written through by that call (V8: a Get's receiver is read, not
	// copied, so this case must copy it; a scalar entry's Get and a pure
	// call's operand under the reference too).
	// Such a call that writes no storage o's value reaches is apart
	// (keeps.go, own-call-pass).
	if vv.ptr {
		for _, oc := range vv.occs {
			if oc != o && oc.pre > o.pre && oc.pre < end && oc.path == syntax.Expr(oc.id) {
				if _, isCall := w.parentOf(oc.id).(*syntax.CallExpr); isCall || w.dotCall(oc.id) != nil {
					return !w.passedApart(vv, o, o.pre+1, end)
				}
			}
		}
	}
	return false
}

// operandIndex is the index of the operand child of the call x among its
// parameters, a dot call's receiver first, or -1.
func operandIndex(x *syntax.CallExpr, c *check.Call, child syntax.Node) int {
	for i, a := range callOperands(x, c) {
		if syntax.Node(a) == child {
			return i
		}
	}
	return -1
}

// ownedAt reports whether parameter i of f is owned (V5): its argument is
// copied by the caller whatever the call keeps.
func (l *lowerer) ownedAt(f *check.Func, i int) bool {
	for _, p := range l.ownedParams(f) {
		if p.index == i {
			return true
		}
	}
	return false
}

// later reports whether the write wr can run after the use o of vv.
func (w *valueWalk) later(vv *vvar, o, wr *vocc) bool {
	if wr.frame != o.frame {
		return true
	}
	if valueMutant == "elide-escape" {
		return false
	}
	if wr.at > o.pre && !(valueOwned() && w.exclusive(o.id, wr.id)) {
		return true
	}
	for _, l := range o.loops {
		if !containsNode(vv.loops, l) && containsNode(wr.loops, l) {
			return true
		}
	}
	return false
}

// exclusive reports whether a and b lie in two arms of one match or of
// one if: in one run of the statement at most one of them runs, so
// neither follows the other (wf10 OWN1 arms; pinata's Change, whose
// Denied arm binds `let mut opened = all` beside the other arm's
// `let mut next = all`). A loop around the statement runs both, in two
// passes; the callers check loops apart (freshPath, later).
func (w *valueWalk) exclusive(a, b syntax.Node) bool {
	if a == nil || b == nil {
		return false
	}
	// up maps each ancestor of a to its child on the path from a.
	up := map[syntax.Node]syntax.Node{}
	for c, p := a, w.parentOf(a); p != nil; c, p = p, w.parentOf(p) {
		up[p] = c
	}
	for cb, p := b, w.parentOf(b); p != nil; cb, p = p, w.parentOf(p) {
		ca, ok := up[p]
		if !ok {
			continue
		}
		if ca == cb {
			return false
		}
		switch s := p.(type) {
		case *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
			_, ka := ca.(*syntax.CaseClause)
			_, kb := cb.(*syntax.CaseClause)
			return ka && kb
		case *syntax.IfStmt:
			arm := func(n syntax.Node) bool { return n == syntax.Node(s.Body) || s.Else != nil && n == syntax.Node(s.Else) }
			return arm(ca) && arm(cb)
		}
		return false
	}
	return false
}

// loopedSince reports whether the use o is in a loop that is not one of
// outer, the loops its binding is made in.
func (w *valueWalk) loopedSince(o *vocc, outer []syntax.Node) bool {
	for _, l := range o.loops {
		if !containsNode(outer, l) {
			return true
		}
	}
	return false
}

func containsNode(list []syntax.Node, n syntax.Node) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// needsCopy reports whether the kept use o of the root vv must copy.
func (w *valueWalk) needsCopy(vv *vvar, o *vocc) bool {
	if vv.ptr {
		// The caller writes after the call, unless the value never
		// outlives the body (storeread.go, own-read).
		return !w.keptRead(vv, o)
	}
	if !vv.mut {
		return false
	}
	if w.snapshotted(vv, o.frame) {
		return false
	}
	if o.kind == occReturn && o.frame == vv.frame {
		// The binding's last use (KEPT-MOVE): no write can run after the
		// return ends its frame. A write in that frame came before it, and
		// a frame that writes a let mut of an enclosing frame is never
		// escaping (the checker refuses the write in one that may outlive
		// its call, types.md Function values 3), so it ran inside the
		// frame, which has returned. The value moves: kidsOf's
		// `change(f, r, func(n) { kids = n.kids }); kids` returns kids
		// with no copy. A return from another frame (a literal's) may
		// run while a write is still to come, and copies below.
		return returnLowering == "copy" && w.writtenElsewhere(vv)
	}
	if w.writtenElsewhere(vv) {
		return true // a write in another frame can run at any time
	}
	if o.frame != vv.frame {
		return len(vv.writes) > 0
	}
	for _, wr := range vv.writes {
		if w.later(vv, o, wr) {
			return !w.keptRead(vv, o) && !w.apartRead(vv, o)
		}
	}
	return false
}

// returnLowering "copy" selects the reference of the return rule
// (KEPT-MOVE, needsCopy): a return of a root that another frame writes
// copies. Empty in every build; a build may set it with -X.
var returnLowering string

// writtenElsewhere reports whether a frame nested in vv's writes it.
func (w *valueWalk) writtenElsewhere(vv *vvar) bool {
	for _, wr := range vv.writes {
		if wr.frame != vv.frame {
			return true
		}
	}
	return false
}

// snapshotted reports whether frame f sees a copy of vv taken when an
// enclosing escaping frame was made (the checker's value captures,
// Info.ValueCaptures; types.md, Function values 3): no write to vv
// follows that capture, so a read of the copy keeps a value no write
// changes.
func (w *valueWalk) snapshotted(vv *vvar, f *vframe) bool {
	for ; f != nil && f != vv.frame; f = f.parent {
		for _, s := range w.info.ValueCaptures[f.node] {
			if s == vv.v {
				return true
			}
		}
	}
	return false
}

// freshen makes the value e, which enters a root, unshared: nothing when
// it is fresh, its parts when it is a literal, else a copy.
func (w *valueWalk) freshen(e syntax.Expr, why string) {
	if e == nil || !w.l.holdsValue(w.typeOf(e)) || w.fresh(e) {
		return
	}
	switch x := e.(type) {
	case *syntax.ParenExpr:
		w.freshen(x.X, why)
		return
	case *syntax.CompositeLit:
		if x.Base != nil {
			w.freshen(x.Base, why)
		}
		for _, el := range x.Elts {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				w.freshen(kv.Value, why)
			} else {
				w.freshen(el, why)
			}
		}
		return
	case *syntax.TupleExpr:
		for _, el := range x.Elts {
			w.freshen(el, why)
		}
		return
	}
	w.copyAt(e, why)
}

// fresh reports whether no other live binding reaches a Map or slice of
// e's value.
func (w *valueWalk) fresh(e syntax.Expr) bool {
	if e == nil {
		return true
	}
	if !w.l.holdsValue(w.typeOf(e)) || w.vf.clone[e] || w.vf.moved[e] || w.vf.moveOut[e] {
		return true
	}
	if call, ok := e.(*syntax.CallExpr); ok && w.handed[call] {
		return true
	}
	switch x := e.(type) {
	case *syntax.ParenExpr:
		return w.fresh(x.X)
	case *syntax.Ident:
		if w.vf.self[x] {
			return true
		}
		if v, ok := w.info.Uses[x].(*check.Var); ok {
			return w.copiedOnce(v) || w.freshIdent(w.varOf(v), x, false)
		}
		return false
	case *syntax.SelectorExpr, *syntax.IndexExpr, *syntax.SliceExpr, *syntax.StarExpr:
		if id := pathRoot(x); id != nil && w.parentIsPath(id, x) {
			if v, ok := w.info.Uses[id].(*check.Var); ok {
				return w.copiedOnce(v) || w.freshPath(w.varOf(v), id, x)
			}
		}
		if s, ok := x.(*syntax.SliceExpr); ok && pathRoot(s) == nil {
			// A cut of a fresh value (cut.go).
			return w.freshCut(s)
		}
		return false
	case *syntax.CompositeLit:
		if x.Base != nil && !w.fresh(x.Base) {
			return false
		}
		for _, el := range x.Elts {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				el = kv.Value
			}
			if !w.fresh(el) {
				return false
			}
		}
		return true
	case *syntax.TupleExpr:
		for _, el := range x.Elts {
			if !w.fresh(el) {
				return false
			}
		}
		return true
	case *syntax.TryExpr:
		return w.fresh(x.X)
	case *syntax.CallExpr:
		c := w.info.Calls[x]
		if c == nil {
			return false
		}
		switch c.Kind {
		case check.CallConversion:
			return len(x.Args) == 1 && w.fresh(x.Args[0])
		case check.CallFunc:
			switch {
			case takesMapSource(c.Func):
				// Its rest (PRIM-3): the source's own storage when the
				// Take is in place, which the pattern hands back to the
				// source's root, else a copy of the source.
				if w.vf.inPlace[x] {
					return w.takeRebound(x) || w.fresh(w.recvOf(x))
				}
				return !w.l.holdsValue(elemOf(w.typeOf(w.recvOf(x))))
			case appendsSource(c.Func) || writesMapSource(c.Func):
				if w.vf.lineage[x] {
					// A one-extender site may extend its result in place
					// later (handover.go): never fresh, unless the analysis
					// reads its parameters as unshared (freshFrom,
					// freshAll) and the source is a dead field of one: the
					// site then writes only that parameter's array, which
					// no one else holds.
					return (w.owner != nil || w.owners != nil) && w.deadField(x)
				}
				if w.vf.inPlace[x] {
					return w.fresh(w.recvOf(x))
				}
				return !w.l.holdsValue(elemOf(w.typeOf(x)))
			}
			if w.typeArgShared(c, x) {
				return false
			}
			if w.l.freshResult(c.Func) {
				return true
			}
			if x.Trailing == nil && w.freshCall(x, c) {
				// An Ergo body fresh from unshared arguments (handover.go).
				return true
			}
			if x.Trailing == nil && w.l.pureV1(c.Func) {
				// A pure runtime/v1 body keeps nothing of its arguments
				// but its result, and reaches no other storage (stdlib.md,
				// Pure bodies): its result is fresh when every argument
				// is.
				for _, a := range callOperands(x, c) {
					if !w.fresh(a) {
						return false
					}
				}
				return true
			}
			return false
		}
	}
	return false
}

// typeArgShared reports a call x of a generic function whose result may
// share a value of a type argument below its top level: some type
// argument holds a Map or slice, and the result's copy reaches below its
// top level (a generated copy function). The function's body was decided
// over its type parameters, where a copy stops (types.md, Bindings 2), so
// its fresh result may hold an argument's value of a type parameter:
// set[T](xs []T, x T) stores x in its fresh copy of xs. A Go call's
// result is copied at the call (goResult), so it stays fresh.
func (w *valueWalk) typeArgShared(c *check.Call, x syntax.Expr) bool {
	if c.Func == nil || c.Func.Pkg == nil || c.Func.Pkg.Host() {
		return false
	}
	held := false
	for _, t := range c.TypeArgs {
		if w.l.holdsValue(t) {
			held = true
		}
	}
	if !held || !w.l.needsCloneFunc(w.typeOf(x), !entryRule()) {
		return false
	}
	return !entryRule() || !w.argsUnshared(c, x)
}

// argsUnshared reports a generic call x whose every operand that may
// carry a value of a type argument holding a Map or slice brings none
// another binding reaches: the operand is consumed (its old value is
// dead, so the result is its storage alone), fresh, or a function literal
// whose results are all fresh (litFresh). Then the result shares no
// argument's value: m = maps.Update(m, k, func(o) { … }) on an
// entry-written type stays unshared (V9).
func (w *valueWalk) argsUnshared(c *check.Call, x syntax.Expr) bool {
	call, ok := x.(*syntax.CallExpr)
	sig := c.Func.Sig
	if !ok || sig == nil || len(c.TypeArgs) != len(sig.TParams) {
		return false
	}
	held := map[*check.TypeParam]bool{}
	for i, tp := range sig.TParams {
		held[tp] = w.l.holdsValue(c.TypeArgs[i])
	}
	ops := callOperands(call, c)
	if len(ops) != len(sig.Params) {
		return false
	}
	var settle []*syntax.CallExpr
	for i, op := range ops {
		carries := false
		for _, tp := range freeTypeParams(sig.Params[i].Type) {
			carries = carries || held[tp]
		}
		switch lit, isLit := unparen(op).(*syntax.FuncLit); {
		case !carries || w.vf.consumed[op]:
		case isLit:
			ok, sites := w.litFresh(lit)
			if !ok {
				return false
			}
			settle = append(settle, sites...)
		case w.namedFunc(op) != nil:
			// A top-level function passed by name, analysed as a
			// literal is: its every result fresh.
			if !w.l.freshResult(w.namedFunc(op)) {
				return false
			}
		default:
			if _, isFunc := underEqual(w.typeOf(op)).(*check.Signature); isFunc || !w.fresh(op) {
				return false
			}
		}
	}
	for _, call := range settle {
		delete(w.vf.lineage, call)
	}
	return true
}

// namedFunc is the function op names when op is a top-level function
// written by name (f, or pkg.f) with an Ergo body, or nil. Its results are
// known (freshResult) as a literal's are (litFresh). A generic function
// is written instantiated, f[T], an index expression: it is not taken, as
// its body is decided over its type parameters, where a copy stops; nor
// is a Go function, which has no Ergo body.
func (w *valueWalk) namedFunc(op syntax.Expr) *check.Func {
	f := funcNamed(w.info, op)
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil {
		return nil
	}
	return f
}

// funcNamed is the function x names, f or pkg.f, or nil. The checker
// records no Uses entry for a qualified name's selector, so pkg.f is the
// function of that name in the imported package. It is the only one: two
// functions share a name only when their first parameters are distinct
// types of the package (196m rule 3), and the checker refuses a reference
// to such a name other than as x.f(…) (S108, overloadedText).
func funcNamed(info *check.Info, x syntax.Expr) *check.Func {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		f, _ := info.Uses[x].(*check.Func)
		return f
	case *syntax.SelectorExpr:
		if f, ok := info.Uses[x.Sel].(*check.Func); ok {
			return f
		}
		id, ok := unparen(x.X).(*syntax.Ident)
		if !ok {
			return nil
		}
		pn, ok := info.Uses[id].(*check.PkgName)
		if !ok || pn.Pkg == nil {
			return nil
		}
		for _, f := range pn.Pkg.Funcs {
			if f.Name == x.Sel.Name {
				return f
			}
		}
	}
	return nil
}

// litFresh reports a function literal whose every result, each value its
// body ends in and each a return statement gives, is fresh, and the
// one-extender Append sites (handover.go, own-lineage) among them: such a
// result is fresh once the site takes the clipped Append, whose new array
// no one else holds when its elements hold no Map or slice (as
// settleLineage does). argsUnshared settles them when the call's result
// is unshared.
func (w *valueWalk) litFresh(lit *syntax.FuncLit) (bool, []*syntax.CallExpr) {
	f := w.frameOf[lit]
	if f == nil || len(f.tails) == 0 {
		return false, nil
	}
	var sites []*syntax.CallExpr
	result := func(r syntax.Expr) bool {
		if w.fresh(r) {
			return true
		}
		call, ok := unparen(r).(*syntax.CallExpr)
		if !ok || !w.vf.lineage[call] || w.l.holdsValue(elemOf(w.typeOf(call))) {
			return false
		}
		sites = append(sites, call)
		return true
	}
	for t := range f.tails {
		if !result(t) {
			return false, nil
		}
	}
	ok := true
	syntax.Inspect(lit.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ReturnStmt:
			for _, r := range n.Results {
				ok = ok && result(r)
			}
		}
		return ok
	})
	if !ok {
		return false, nil
	}
	return true, sites
}

// parentIsPath reports whether x is the path its root id heads.
func (w *valueWalk) parentIsPath(id *syntax.Ident, x syntax.Expr) bool {
	return pathRoot(x) == id
}

// freshIdent reports whether the use id of vv may take its value without
// a copy: vv is a local of this frame, read nowhere else after this use
// and never shared before it, captured by no other frame, and its value
// is unshared (a let mut always is; a let when its value was fresh). At a
// return the frame ends, so later uses do not count.
func (w *valueWalk) freshIdent(vv *vvar, id *syntax.Ident, _ bool) bool {
	return w.freshPath(vv, id, nil)
}

// freshPath is freshIdent for the field path path that the use id heads
// (nil: id itself). Another use of vv that reads a field disjoint from
// path whose value holds no Map or slice (r.head beside r.items) keeps
// nothing and cannot see a write through path, so it does not count
// (handover.go, disjointRead).
func (w *valueWalk) freshPath(vv *vvar, id *syntax.Ident, path syntax.Expr) bool {
	if valueMutant == "elide-live" && vv.local {
		return true
	}
	local, frame, loops, unshared := vv.local, vv.frame, vv.loops, vv.mut || vv.handed
	if w.owner != nil && vv.v == w.owner || w.owners[vv.v] {
		// freshFrom's premise: the parameter holds an unshared value, as
		// a let mut of the body's own frame does.
		local, frame, loops, unshared = true, w.root, nil, true
	}
	if !local || vv.ptr {
		return false
	}
	var use *vocc
	for _, o := range vv.occs {
		if o.id == id {
			use = o
		}
	}
	if use == nil || use.frame != frame {
		return false
	}
	// A use that is the value, or a part of the value, its frame returns
	// ends the frame: a use after it never runs, nor does one in another
	// return of the frame, which ends it too. A use in the same returned
	// value counts (the two parts would share).
	ret := w.returnNode(use, use.id)
	returning := ret != nil
	// A let mut stored whole as an entry of a Map whose entries have no
	// write path (V8; not entry-written, V9) shares with the entry only
	// storage neither writes: the entry is never written, and the let mut
	// is not when every later use reads it (p.paths[id] = path, then
	// len(path)). Its later reads do not end its freshness.
	stored := vv.mut && w.storedAsEntry(use)
	// A later rebinding of the whole of vv that every run of the use runs
	// next (killOf, S2-COPY) ends the value the use reads: a use after it,
	// or one before the use that a loop runs again after it, reads the new
	// value. A loop that runs the use again runs the kill first only when
	// the kill is in it.
	var kill *vocc
	var killStmt syntax.Node
	if !returning {
		kill, killStmt = w.killOf(vv, use)
		for _, l := range use.loops {
			if containsNode(loops, l) {
				continue
			}
			if kill == nil || !(w.preOf(l) <= w.preOf(killStmt) && w.preOf(killStmt) < w.postOf(l)) && valueMutant != "kill-loop-blind" {
				return false
			}
		}
	}
	for _, o := range vv.occs {
		if o == use || path != nil && valueOwned() && w.disjointRead(o.id, path) {
			continue
		}
		// After the kill a use reads, keeps or writes the new value, which
		// the kill stored as a let mut's every store is: a copy unless
		// fresh. So it shares no storage with the value the use reads,
		// even in a loop that runs the use again.
		if kill != nil && (o == kill || o.pre >= w.postOf(killStmt) || valueMutant == "kill-between-blind" && o.pre > use.pre) {
			continue
		}
		if valueOwned() && (w.baseCovers(use.id, o.id) || w.baseCovers(o.id, use.id)) || w.baseDisjoint(o.id, use.id, unshared) || w.baseDisjoint(use.id, o.id, unshared) {
			// An update's base and a path under a field it names (parts.go),
			// or under a field it sets when the value is unshared (update.go).
			continue
		}
		if o.frame != frame {
			return false
		}
		if valueOwned() && w.exclusive(o.id, use.id) && !w.loopedSince(use, loops) {
			// A use in another arm of the match or if runs instead of
			// this one, never after it nor before it, unless a loop the
			// binding is outside runs the statement twice.
			continue
		}
		// An earlier use in a returned value that does not hold use ends
		// the frame when it runs, so use does not run after it.
		if other := w.returnNode(o, o.id); other != nil && o.pre < use.pre {
			if !(w.preOf(other) <= use.pre && use.pre < w.postOf(other)) || valueMutant == "return-blind" {
				continue
			}
		}
		if o.pre < use.pre && valueOwned() {
			if end := w.endingReturn(o); end != nil && !(w.preOf(end) <= use.pre && use.pre < w.postOf(end)) {
				// So does one anywhere in a return statement or a last
				// value that does not hold use (handover.go).
				continue
			}
		}
		if returning {
			inRet := w.preOf(ret) <= o.pre && o.pre < w.postOf(ret)
			if !inRet && o.pre > use.pre || valueMutant == "return-blind" && inRet {
				continue
			}
			if inRet && o.pre > use.pre {
				return false
			}
		} else if o.pre > use.pre && !(o.kind == occRead && stored) {
			if end := w.endingReturn(use); valueOwned() && end != nil && (o.pre < w.preOf(end) || o.pre >= w.postOf(end)) {
				// The use's return statement ends the frame: o, outside
				// it, never runs after the use (handover.go).
				continue
			}
			return false
		}
		if o.kind == occEscape || o.kind == occReturn {
			// A copied read shares nothing; nor does a handed one (V7),
			// whose storage only the binding's new value holds.
			// So does a pure call's whose result is copied (alias.go,
			// moveOver).
			if !w.vf.clone[o.path] && !w.vf.handed[o.path] && !(o.result && o.call != nil && w.vf.clone[o.call]) {
				return false
			}
		}
	}
	if unshared {
		return true
	}
	if vv.whole != nil {
		return w.fresh(vv.whole)
	}
	return vv.init != nil && w.fresh(vv.init)
}

// killOf is the rebinding `v = e` of the whole of the let mut vv that ends
// the value the use reads (S2-COPY), and its statement, or nil: a plain
// assignment in the block that holds the use's statement, after that
// statement, such that every run of the use runs it next: no break or
// continue between the two leaves the block. A return, or a failure,
// leaves the frame and ends vv too: a catch covers no failure inside a
// block of its operand (rows.md, Catch), so none resumes after the kill.
// The uses between the two, e's among them, still read the old value.
//
// The read loop `let (b, n) = try f.Read(buf); …; buf = b` hands Go its
// buffer at each call, as hand Go does, where the value rule copied it.
func (w *valueWalk) killOf(vv *vvar, use *vocc) (*vocc, syntax.Node) {
	if !vv.mut || !valueOwned() {
		return nil, nil
	}
	var stmt syntax.Node = use.id
	var block *syntax.BlockStmt
	for n := w.parentOf(use.id); n != nil; stmt, n = n, w.parentOf(n) {
		if b, ok := n.(*syntax.BlockStmt); ok {
			block = b
			break
		}
		if w.frameOf[n] != nil {
			return nil, nil
		}
	}
	if block == nil {
		return nil, nil
	}
	at := slices.IndexFunc(block.List, func(s syntax.Stmt) bool { return syntax.Node(s) == stmt })
	if at < 0 {
		return nil, nil
	}
	for i, s := range block.List[at:] {
		if i > 0 {
			if a, ok := s.(*syntax.AssignStmt); ok && a.Tok == syntax.ASSIGN && len(a.Lhs) == 1 && len(a.Rhs) == 1 {
				if id, ok := unparen(a.Lhs[0]).(*syntax.Ident); ok && w.info.Uses[id] == check.Object(vv.v) {
					for _, o := range vv.occs {
						if o.id == id {
							return o, a
						}
					}
					return nil, nil
				}
			}
		}
		if w.leavesBlock(s) && valueMutant != "kill-branch-blind" {
			return nil, nil
		}
	}
	return nil, nil
}

// leavesBlock reports a break or continue in the statement s that is not
// inside a loop of s: it leaves the block that holds s.
func (w *valueWalk) leavesBlock(s syntax.Stmt) bool {
	leaves := false
	syntax.Inspect(s, func(n syntax.Node) bool {
		if leaves {
			return false
		}
		if _, ok := n.(*syntax.FuncLit); ok {
			return false
		}
		b, ok := n.(*syntax.BranchStmt)
		if !ok {
			return true
		}
		for p := w.parentOf(b); p != nil; p = w.parentOf(p) {
			switch p.(type) {
			case *syntax.ForStmt, *syntax.RangeStmt:
				return true
			}
			if p == syntax.Node(s) {
				break
			}
		}
		leaves = true
		return false
	})
	return leaves
}

// fromKey names a parameter of a function: freshFrom's question.
type fromKey struct {
	f *check.Func
	i int
}

// freshFrom reports whether every result of the Ergo body f, called with
// n arguments, is fresh when its parameter i holds an unshared value
// (V7): the analysis of f's body, read with that parameter as a let mut
// of the body's frame (freshIdent), proves each returned value fresh. So
// a result shares no storage but that parameter's: not another
// parameter's, a package let's or a capture's. A function being decided
// is not fresh, as in freshResult.
func (l *lowerer) freshFrom(f *check.Func, i, n int) bool {
	vf := l.valueFacts()
	k := fromKey{f, i}
	switch vf.from[k] {
	case 1, 3:
		return false
	case 2:
		return true
	}
	vf.from[k] = 3
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Decl.Type == nil || f.Decl.Type.Params == nil {
		return false
	}
	var params []*syntax.Ident
	for _, field := range f.Decl.Type.Params.List {
		params = append(params, field.Names...)
	}
	if len(params) != n || i < 0 || i >= n {
		return false
	}
	p, ok := f.Pkg.Info.Defs[params[i]].(*check.Var)
	if !ok {
		return false
	}
	// Decide f's own freshness first: a call of f inside its body then
	// reads that answer, never one made under this premise.
	if l.freshResult(f) {
		vf.from[k] = 2
		return true
	}
	vf.from[k] = 1
	saved := l.info
	l.info = f.Pkg.Info
	w := l.analyzeValues(f.Decl, f.Pkg.Info)
	fresh := false
	if w.root != nil && w.done {
		w.owner = p
		fresh = w.resultsFresh()
		w.owner = nil
	}
	l.info = saved
	vf.from[k] = 3
	if fresh {
		vf.from[k] = 2
	}
	return fresh
}

// freshResult reports whether every result of f is fresh: an Ergo body
// whose every returned value is fresh in it. A function being decided is
// not fresh (a recursive result is shared until proved otherwise).
func (l *lowerer) freshResult(f *check.Func) bool {
	vf := l.valueFacts()
	switch vf.fresh[f] {
	case 1, 3:
		return false
	case 2:
		return true
	}
	if f != nil && f.Pkg.Host() {
		// A Go call's Map or slice result is new (goFresh) or
		// copied at the call (goResult).
		return true
	}
	if l.madeResult(f) {
		// A runtime/v1 body that makes its result (gobody.go).
		vf.fresh[f] = 2
		return true
	}
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		vf.fresh[f] = 3
		return false
	}
	vf.fresh[f] = 1
	saved := l.info
	l.info = f.Pkg.Info
	w := l.analyzeValues(f.Decl, f.Pkg.Info)
	ok := w.root != nil && w.resultsFresh()
	l.info = saved
	vf.fresh[f] = 3
	if ok {
		vf.fresh[f] = 2
	}
	return ok
}

// resultsFresh reports whether every value the root frame returns is
// fresh.
func (w *valueWalk) resultsFresh() bool {
	ok := true
	for x := range w.root.tails {
		if !w.fresh(x) {
			ok = false
		}
	}
	syntax.Inspect(w.root.node, func(n syntax.Node) bool {
		if w.frameOf[n] != nil && w.frameOf[n] != w.root {
			return false
		}
		if r, isRet := n.(*syntax.ReturnStmt); isRet {
			for _, x := range r.Results {
				if !w.fresh(x) {
					ok = false
				}
			}
		}
		return true
	})
	return ok
}

// copyElemSource is the type of src in x, a call of an extern bound to
// Go's copy(dst, src), when src's elements hold a slice or Map, else nil.
// Go's copy stores each element in dst's array, so an element's slice or
// Map would be shared by dst and src, and a write through one would reach
// the other (a []Box copied into a fresh array shared each Box's S with
// the source, and `d[0].S[0] = 99` wrote the source's; found by NB-CUT's
// TestCutAgrees). The value walk reads src as a read of a pure call whose
// int result shares nothing with it, so the copy is here, where the
// builtin is lowered, the text's and the IR's alike. A source whose
// elements hold no slice or Map (the []int push) copies nothing.
// TestGoCopyKeepsValues is its property.
func copyElemSource(l *lowerer, x *syntax.CallExpr, c *check.Call, typeOf func(syntax.Expr) check.Type) check.Type {
	if c == nil || c.Func == nil || check.GoBuiltin(c.Func) != "copy" {
		return nil
	}
	ops := callOperands(x, c)
	if len(ops) != 2 {
		return nil
	}
	t := typeOf(ops[1])
	if !l.holdsValue(elemOf(t)) {
		return nil
	}
	return t
}

// valueSource lowers the source operand of a call of the prelude's Append,
// Delete or Put that must not write it (V3): Append takes the source
// clipped to its length, so Go's append makes a new array, and Delete and
// Put take a copy of the Map. vals are the call's lowered operands, the source first.
// Go's copy(dst, src) takes a copy of src when its elements hold a slice
// or Map (copyElemSource).
func (fc *funcCtx) valueSource(x *syntax.CallExpr, c *check.Call, vals []string) []string {
	if !valueRule() || len(vals) == 0 || fc.l.valueFacts().inPlace[x] || fc.l.valueFacts().lineage[x] || valueMutant == "in-place" {
		return vals
	}
	switch {
	case appendsSource(c.Func):
		vals[0] = fc.l.goPkg("slices") + ".Clip(" + vals[0] + ")"
	case writesMapSource(c.Func) || takesMapSource(c.Func):
		vals[0] = fc.reportCopy(x, fc.l.goPkg("maps")+".Clone("+vals[0]+")", whyMapWrite)
	case len(vals) == 2:
		if t := copyElemSource(fc.l, x, c, fc.typeOf); t != nil {
			vals[1] = fc.reportCopy(x, fc.cloneValue(t, vals[1], x), whyCopyElem)
		}
	}
	return vals
}

// cloneValue is the Go text of a copy of val, a value of type t: Go's
// maps.Clone and slices.Clone when no element holds a Map or slice, else
// t's generated copy function, which copies each element.
//
// A Map's entries have no write path in Ergo (V8), so its copy is
// maps.Clone whatever its entries hold; cloneDeep copies them too, for a
// value Go may write.
func (tc *typeCtx) cloneValue(t check.Type, val string, site syntax.Node) string {
	return tc.cloneOf(t, val, site, !entryRule())
}

// cloneDeep is cloneValue's copy that also copies each Map's entries: the
// copy of a value that goes to Go or comes from it, since Go may write an
// entry's slice or Map (V8).
func (tc *typeCtx) cloneDeep(t check.Type, val string, site syntax.Node) string {
	return tc.cloneOf(t, val, site, true)
}

func (tc *typeCtx) cloneOf(t check.Type, val string, site syntax.Node, deep bool) string {
	if !tc.l.holdsValue(t) {
		return val
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		if !deep && !tc.l.entryWritten(t) || !tc.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			if _, plain := t.(*check.Map); plain && tc.l.intMap(u) { // intmap.go: the table's own copy
				return val + ".Clone()"
			}
			tc.l.intMapConflict(u) // a defined Map type: maps.Clone
			return tc.l.goPkg("maps") + ".Clone(" + val + ")"
		}
	case *check.Slice:
		if !tc.l.holdsValue(u.Elem) || valueMutant == "shallow" {
			return tc.l.goPkg("slices") + ".Clone(" + val + ")"
		}
	}
	name, free := tc.l.cloneFunc(t, site, deep)
	if len(free) > 0 {
		args := make([]string, len(free))
		for i, tp := range free {
			args[i] = tc.goType(tp)
		}
		name += "[" + strings.Join(args, ", ") + "]"
	}
	return name + "(" + val + ")"
}

// goPkg is the name in main.go of the Go standard package path, which the
// naming pass reserves (nameAll) and main.go imports.
func (l *lowerer) goPkg(path string) string {
	if l.hostImports == nil {
		l.hostImports = map[string]bool{}
	}
	l.hostImports[path] = true
	return l.pkgName(pkgKey{"import", path}, path)
}

// cloneKey is the package-scope key of t's copy function: deep, a copy of
// each Map's entries too (cloneDeep), or the build's own copy
// (cloneValue). The reference "whole" copies entries in both, under one
// key.
func (l *lowerer) cloneKey(t check.Type, deep bool) pkgKey {
	if deep && entryRule() {
		return pkgKey{"clonedeep", l.typeKey(t)}
	}
	return pkgKey{"clone", l.typeKey(t)}
}

// cloneBase is the preferred Go name of t's copy function.
func cloneBase(t check.Type) string {
	switch u := t.(type) {
	case *check.Named:
		return "ergoClone" + u.Obj.Name
	case *check.Slice:
		return "ergoCloneSlice"
	case *check.Array:
		return "ergoCloneArray"
	case *check.Map:
		return "ergoCloneMap"
	case *check.Tuple:
		return "ergoCloneTuple"
	}
	return "ergoClone"
}

// needsCloneFunc reports a type whose copy is a generated function: one
// that holds a Map or slice and is not a Map or slice of elements that
// hold none. A copy that is not deep copies a Map with maps.Clone (V8).
func (l *lowerer) needsCloneFunc(t check.Type, deep bool) bool {
	if !l.holdsValue(t) {
		return false
	}
	if _, named := t.(*check.Named); named {
		return true
	}
	switch u := underEqual(t).(type) {
	case *check.Map:
		return (deep || l.entryWritten(t)) && l.holdsValue(u.Elem)
	case *check.Slice:
		return l.holdsValue(u.Elem)
	}
	return true
}

// cloneFunc is the copy function of t, and the type parameters of the
// enclosing declaration that t mentions, emitted once at first use, as
// equality functions are (equal.go).
func (l *lowerer) cloneFunc(t check.Type, site syntax.Node, deep bool) (string, []*check.TypeParam) {
	key := l.cloneKey(t, deep)
	name := l.pkgName(key, cloneBase(t))
	free := freeTypeParams(t)
	if l.clones[key] {
		return name, free
	}
	if l.clones == nil {
		l.clones = map[pkgKey]bool{}
	}
	l.clones[key] = true
	scope := newNamer(l.names)
	list, names := l.typeParams(free, scope, site)
	tc := &typeCtx{l: l, params: names, site: site}
	goType := tc.goType(t)
	v, c := scope.alloc("v"), scope.alloc("c")
	var body strings.Builder
	switch u := underEqual(t).(type) {
	case *check.Slice:
		i, e := scope.alloc("i"), scope.alloc("e")
		body.WriteString("\tif " + v + " == nil {\n\t\treturn nil\n\t}\n")
		body.WriteString("\t" + c + " := make(" + goType + ", len(" + v + "))\n")
		body.WriteString("\tfor " + i + ", " + e + " := range " + v + " {\n")
		body.WriteString("\t\t" + c + "[" + i + "] = " + tc.cloneOf(u.Elem, e, site, deep) + "\n\t}\n\treturn " + c + "\n")
	case *check.Map:
		l.intMapConflict(u) // intmap.go: a deep copy over Go's map
		k, e := scope.alloc("k"), scope.alloc("e")
		body.WriteString("\t" + c + " := make(" + goType + ", len(" + v + "))\n")
		body.WriteString("\tfor " + k + ", " + e + " := range " + v + " {\n")
		body.WriteString("\t\t" + c + "[" + k + "] = " + tc.cloneOf(u.Elem, e, site, deep) + "\n\t}\n\treturn " + c + "\n")
	case *check.Array:
		// Go copied the array; each element that holds a slice or a Map
		// is copied in turn.
		i := scope.alloc("i")
		body.WriteString("\t" + c + " := " + v + "\n")
		body.WriteString("\tfor " + i + " := range " + c + " {\n")
		body.WriteString("\t\t" + c + "[" + i + "] = " + tc.cloneOf(u.Elem, c+"["+i+"]", site, deep) + "\n\t}\n\treturn " + c + "\n")
	case *check.Tuple:
		body.WriteString("\t" + c + " := " + v + "\n")
		for i, et := range u.Types {
			if l.holdsValue(et) {
				f := tupleField(i)
				body.WriteString("\t" + c + "." + f + " = " + tc.cloneOf(et, v+"."+f, site, deep) + "\n")
			}
		}
		body.WriteString("\treturn " + c + "\n")
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			ice(site, "the record type %s has no name", t)
		}
		td := l.typeDecl(n.Obj, site)
		body.WriteString("\t" + c + " := " + v + "\n")
		for _, f := range u.Fields {
			if !l.holdsValue(f.Type) {
				continue
			}
			goName, ok := td.fieldName[f.Name]
			if !ok {
				ice(site, "%s has no field %s", td.name, f.Name)
			}
			body.WriteString("\t" + c + "." + goName + " = " + tc.cloneOf(f.Type, v+"."+goName, site, deep) + "\n")
		}
		body.WriteString("\treturn " + c + "\n")
	default:
		ice(site, "the type %s needs a copy function but holds no Map or slice", t)
	}
	l.gen.WriteString("func " + name + list + "(" + v + " " + goType + ") " + goType + " {\n" + body.String() + "}\n\n")
	return name, free
}

// reserveClones gives, in the naming pass, the key and preferred name of
// every copy function the lowering can emit: one for each type of an
// expression of the build that may be copied, and each such type it reaches
// through its elements and fields. Reserving one the lowering does not
// emit only renames a local.
func (l *lowerer) reserveClones(pkgs []*check.Package, reserve func(key pkgKey, t check.Type)) {
	if !valueRule() {
		return
	}
	walked := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if s, ok := t.(*check.Signature); ok && !walked[t] {
			// A callback's Go operands and result are copied at the
			// boundary (gocallback.go): testing/slogtest.TestHandler's
			// func() []Map[string, go.Any] (GB2).
			walked[t] = true
			for _, p := range s.Params {
				walk(p.Type)
			}
			for _, r := range s.Results {
				walk(r)
			}
			return
		}
		if t == nil || walked[t] || !l.holdsValue(t) {
			return
		}
		walked[t] = true
		if l.needsCloneFunc(t, !entryRule()) {
			reserve(l.cloneKey(t, !entryRule()), t)
		}
		if entryRule() && l.needsCloneFunc(t, true) {
			reserve(l.cloneKey(t, true), t) // the copy Go is given (V8, cloneDeep)
		}
		switch u := underEqual(t).(type) {
		case *check.Slice:
			walk(u.Elem)
		case *check.Array:
			walk(u.Elem)
		case *check.Map:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Struct:
			for _, f := range u.Fields {
				walk(f.Type)
			}
		case *check.Union:
			for _, v := range u.Variants {
				walk(v)
			}
		}
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for _, tn := range l.canon.types(p) {
			walk(tn.Type)
		}
		for _, f := range l.canon.funcs(p) {
			if f.Sig != nil {
				walk(f.Sig)
			}
		}
		for id, o := range p.Info.Defs {
			if v, ok := o.(*check.Var); ok && l.readsFact(p, id) {
				walk(v.Type)
			}
		}
		for x, t := range p.Info.Types {
			if l.readsFact(p, x) {
				walk(t)
			}
		}
	}
}
