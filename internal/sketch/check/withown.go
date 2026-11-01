package check

import (
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Wire ownership (resources.md, Wire ownership; runtime-199 stage 3b). A
// with of a named wire builds the provider entries its block reaches, owns
// each one it builds, and releases each resource among them once, in
// reverse acquisition order, when the block ends, on every exit. This pass
// records that fact once, per with site, after the bodies, the wires and
// the Close identities are known (With.Acquired):
//
//  1. Acquisition. The entries of With.Entries are acquired, and each
//     entry whose capability an acquired provider uses while it is built
//     (WireEntry.Uses), resolved through the with's wire (Wire.Provider).
//     An entry of several capabilities is one acquisition. An overridden
//     entry binds nothing Provider returns, and an entry nothing reaches
//     is not visited, so neither is acquired. The order is the one the
//     builder follows: each entry after the providers it uses, the first
//     ready entry first in the wire pass's entry order (wires by name,
//     then entries in source order; WireEntry.seq).
//  2. Release identity. An acquired provider whose type resolves a
//     checked Close (Info.Closes) is a resource; its Close is recorded.
//  3. Release needs. A release runs when the block ends, in the
//     environment outside the with, so each need of each recorded Close
//     is charged at the with site against that environment: an outer with
//     of the same target that binds it discharges it (and builds its
//     provider), else the target's row must hold it. The pass runs after
//     the rows converged, so it widens no row: a need that no outer with
//     and no row holds is S102 at the with.
//  4. Refusals, each located. A resource provider that names an existing
//     value (a variable, a field, an element) is borrowed or reused: this
//     bounded ownership owns only a value the provider makes (S109, #197).
//     An exit action registered in the block runs when the function
//     returns (runtime.md, Exit actions 2), after the with closed its
//     providers, so one that needs a capability this with provides is
//     S108. A with's value that holds a Task leaves the block that drains
//     it before its providers close (resources.md, Release before the
//     end), S108.
//
// Sites are processed inner before outer, so an inner with's release
// needs reach an outer with's acquisitions before the outer one is
// recorded.
type withSite struct {
	x     *syntax.CallExpr
	file  *fileInfo
	t     *target
	outer []*withFrame // the frames of t around x, outermost first
}

func (c *checker) withOwnership() {
	last := make(map[*syntax.CallExpr]int, len(c.withSites))
	for i, s := range c.withSites {
		last[s.x] = i
	}
	frames := map[*syntax.CallExpr]*withSite{}
	refused := map[*WireEntry]bool{}
	for i := range c.withSites {
		s := &c.withSites[i]
		if last[s.x] != i {
			continue
		}
		frames[s.x] = s
		w := c.pkg.Info.Withs[s.x]
		if w == nil || len(w.Wires) == 0 || len(w.Entries) == 0 {
			continue
		}
		w.Acquired = c.acquire(w.Wires[0], w.Entries)
		owning := false
		for _, a := range w.Acquired {
			if a.Close == nil {
				continue
			}
			owning = true
			if !refused[a.Entry] && !c.freshProvider(a.Entry.Provider) {
				refused[a.Entry] = true
				if wf := c.entryFile(w.Wires[0], a.Entry); wf != nil {
					c.cur = wf
					c.errorf(a.Entry.Provider, CodeOutside, "this resource provider names an existing value; a with owns and releases only a provider it makes (a call, a composite literal or a unit value), and a borrowed or reused resource is #197's (resources.md, Wire ownership)")
				}
			}
			c.cur = s.file
			for _, cap := range c.releaseNeeds(a.Close) {
				c.chargeRelease(s, a, cap)
			}
		}
		if owning {
			c.cur = s.file
			c.ownedBlock(s.x, w)
		}
	}
	c.withSites = nil
}

// acquire is the acquisition list of a with of w whose block reaches the
// entries reached (rule 1).
func (c *checker) acquire(w *Wire, reached []*WireEntry) []*Acquisition {
	want := map[*WireEntry]bool{}
	var queue []*WireEntry
	add := func(e *WireEntry) {
		if e != nil && !want[e] {
			want[e] = true
			queue = append(queue, e)
		}
	}
	for _, e := range reached {
		add(e)
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		for _, cap := range e.Uses {
			add(w.Provider(cap))
		}
	}
	// The wire pass's entry order, not a walk of the embeddings: a with
	// reads only what it acquires (the wire chain scale case).
	order := make([]*WireEntry, 0, len(want))
	for e := range want {
		order = append(order, e)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].seq < order[j].seq })
	built := map[*WireEntry]bool{}
	done := make([]bool, len(order))
	var list []*Acquisition
	for range order {
		next := -1
		for i, e := range order {
			if done[i] {
				continue
			}
			ready := true
			for _, cap := range e.Uses {
				if p := w.Provider(cap); p != nil && p != e && !built[p] {
					ready = false
					break
				}
			}
			if ready {
				next = i
				break
			}
		}
		if next < 0 {
			break // an entry cycle, reported by checkEntryCycles
		}
		done[next] = true
		e := order[next]
		built[e] = true
		a := &Acquisition{Entry: e}
		for i, cap := range e.Caps {
			if w.Provider(cap) == e {
				a.Caps = append(a.Caps, i)
			}
		}
		a.Close = c.providerClose(c.pkg.Info.Types[e.Provider])
		list = append(list, a)
	}
	return list
}

// providerClose is the checked Close a provider of type p releases with
// (rule 2), or nil.
func (c *checker) providerClose(p Type) *Close {
	if p == nil {
		return nil
	}
	if _, f := c.methodOf(p, "Close"); f != nil {
		if f.Decl == nil || f.Pkg == nil || f.Pkg.Info == nil || f.Pkg.Info.Closes[f.Decl] == nil {
			return nil
		}
		return &Close{Receiver: p, Func: f}
	}
	return c.closeOperation(p, nil, "Close")
}

// releaseNeeds are the capabilities a release by cl requires.
func (c *checker) releaseNeeds(cl *Close) []Type {
	var row *Row
	switch {
	case cl.Func != nil:
		row = c.funcSig(cl.Func).Uses
	case cl.Op != nil && cl.Op.Sig != nil:
		row = cl.Op.Sig.Uses
	}
	return c.types.resolve(row).Elems()
}

// chargeRelease charges cap, a need of a's release, at s against the
// environment outside the with (rule 3).
func (c *checker) chargeRelease(s *withSite, a *Acquisition, cap Type) {
	for i := len(s.outer) - 1; i >= 0; i-- {
		f := s.outer[i]
		if f.unknown {
			return
		}
		if found, node := f.binds.find(c.types, cap); found {
			if f.site != nil {
				var e *WireEntry
				if node != nil {
					e = node.entry
				}
				c.pkg.Info.Withs[f.site].discharge(cap, e)
			}
			return
		}
	}
	t := s.t
	switch {
	case t.declUses != nil && (t.declUses.has(c.types, cap) || t.declUses.unknown(c.types)):
		return
	case t.declUses == nil && t.uses.has(c.types, cap):
		return
	case t.declUses == nil && t.uses.unknown(c.types):
		return
	}
	what := c.pkg.Info.Types[a.Entry.Provider]
	if t.declUses == nil {
		c.errorf(s.x, CodeMissingCap, "the release of %s when this with ends requires %s, which is not in the inferred capability row of %s and no outer with provides it; a release runs outside the with and widens no inferred row, so write the row of %s (resources.md, Wire ownership)", what, cap, t.name, t.name)
		return
	}
	c.errorf(s.x, CodeMissingCap, "the release of %s when this with ends requires %s, which is not in the capability row of %s and no outer with provides it; a release runs outside the with (resources.md, Wire ownership)", what, cap, t.name)
}

// discharge adds cap, provided by e, to the with's discharged list, in its
// sorted place, once.
func (w *With) discharge(cap Type, e *WireEntry) {
	k, ok := keyOf(cap)
	if !ok {
		return
	}
	for _, d := range w.Discharged {
		if dk, _ := keyOf(d); dk.compare(k) == 0 {
			return
		}
	}
	i := sort.Search(len(w.Discharged), func(i int) bool {
		dk, _ := keyOf(w.Discharged[i])
		return dk.compare(k) > 0
	})
	w.Discharged = append(w.Discharged, nil)
	copy(w.Discharged[i+1:], w.Discharged[i:])
	w.Discharged[i] = cap
	if w.Wires != nil {
		w.Entries = append(w.Entries, nil)
		copy(w.Entries[i+1:], w.Entries[i:])
		w.Entries[i] = e
	}
}

// freshProvider reports a provider expression that makes its value: a
// call (a conversion of a fresh value), a composite literal or the unit
// value of a type name (rule 4). A call's result is the callee's to give.
func (c *checker) freshProvider(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.CompositeLit:
		return true
	case *syntax.CallExpr:
		if call := c.pkg.Info.Calls[x]; call != nil && call.Kind == CallConversion && len(x.Args) == 1 {
			return c.freshProvider(x.Args[0])
		}
		return true
	case *syntax.Ident:
		_, ok := c.pkg.Info.Uses[x].(*TypeName)
		return ok
	case *syntax.SelectorExpr:
		_, ok := c.pkg.Info.Uses[x.Sel].(*TypeName)
		return ok
	}
	return false
}

// entryFile is the file of the package wire, reached from w through
// embeddings, that declares e; nil for an imported wire's entry.
func (c *checker) entryFile(w *Wire, e *WireEntry) *fileInfo {
	visited := map[*Wire]bool{}
	var find func(v *Wire) *fileInfo
	find = func(v *Wire) *fileInfo {
		if visited[v] {
			return nil
		}
		visited[v] = true
		for _, x := range v.Entries {
			if x == e {
				if v.pkg != c.pkg {
					return nil
				}
				return v.file
			}
			if x.Embedded != nil {
				if f := find(x.Embedded); f != nil {
					return f
				}
			}
		}
		return nil
	}
	return find(w)
}

// ownedBlock reports, in the block of x, an owning with, the exit actions
// that need a capability x provides and a value of x that holds a Task
// (rule 4). A function literal and the trailing block of a call other than
// with are frames of their own: what they register ends with them, before
// the block ends.
func (c *checker) ownedBlock(x *syntax.CallExpr, w *With) {
	if t := c.pkg.Info.Types[x]; t != nil && c.holdsTask(t) {
		c.errorf(x, CodeType, "the value of this with holds a Task, which leaves the block that stops it before its providers close; wait on the Task inside the block (resources.md, Release before the end)")
	}
	var stack []*syntax.CallExpr // the withs between x and the node, innermost last
	var visit func(n syntax.Node) bool
	visit = func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CallExpr:
			call := c.pkg.Info.Calls[n]
			if call != nil && call.Kind == CallWith {
				for _, a := range n.Args {
					syntax.Inspect(a, visit)
				}
				if n.Trailing != nil {
					stack = append(stack, n)
					syntax.Inspect(n.Trailing, visit)
					stack = stack[:len(stack)-1]
				}
				return false
			}
			if isOnExit(call) {
				c.exitAction(n, x, w, stack)
			}
			if n.Trailing != nil {
				syntax.Inspect(n.Fun, visit)
				for _, a := range n.Args {
					syntax.Inspect(a, visit)
				}
				return false
			}
		}
		return true
	}
	for _, s := range x.Trailing.List {
		syntax.Inspect(s, visit)
	}
}

// isOnExit reports a call of std scope.OnExit.
func isOnExit(call *Call) bool {
	return call != nil && call.Kind == CallFunc && call.Func != nil && call.Func.Name == "OnExit" &&
		call.Func.Pkg != nil && call.Func.Pkg.Path == "scope"
}

// exitAction reports the exit action registered by the call n inside the
// owning with x when its block needs a capability that x, not a with
// between them, discharges.
func (c *checker) exitAction(n, x *syntax.CallExpr, w *With, between []*syntax.CallExpr) {
	if n.Trailing == nil {
		return
	}
	sig := c.pkg.Info.Literals[n.Trailing]
	if sig == nil {
		return
	}
	for _, cap := range c.types.resolve(sig.Uses).Elems() {
		inner := false
		for _, y := range between {
			if v := c.pkg.Info.Withs[y]; v != nil && len(v.Wires) > 0 && v.Wires[0].Provider(cap) != nil {
				inner = true
				break
			}
		}
		if inner || w.Wires[0].Provider(cap) == nil {
			continue
		}
		c.errorf(n, CodeType, "this exit action runs when the function returns (runtime.md, Exit actions 2), after the with around it closed its providers, but it needs %s, which that with provides; register it outside the with, or release the value in the block (resources.md, Wire ownership)", cap)
		return
	}
}
