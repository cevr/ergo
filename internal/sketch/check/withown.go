package check

import (
	"fmt"
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Context ownership (resources.md, Context ownership; runtime-199 stage 3b). A
// with of a named context builds the provider entries its block reaches, owns
// each one it builds, and releases each resource among them once, in
// reverse acquisition order, when the block ends, on every exit. This pass
// records that fact once, per with site, after the bodies, the contexts and
// the Close identities are known (With.Acquired):
//
//  1. Acquisition. The entries of With.Entries are acquired, and each
//     entry whose capability an acquired provider uses while it is built
//     (ContextEntry.Uses), resolved through the with's context (Context.Provider).
//     An entry of several capabilities is one acquisition. An overridden
//     entry binds nothing Provider returns, and an entry nothing reaches
//     is not visited, so neither is acquired. The order is the one the
//     builder follows: each entry after the providers it uses, the first
//     ready entry first in the context pass's entry order (contexts by name,
//     then entries in source order; ContextEntry.seq).
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
//     Every checked Close releases: an operation of the provider's type, a
//     generic Close and a Close that takes a pointer to a provider that is
//     a value included (C29). A generic Close whose type parameter is
//     constrained by operations would pass witnesses that no fact
//     records: S109 naming #197 at the provider (closeWitnessed). An exit action registered in the block runs when the function
//     returns (runtime.md, Exit actions 2), after the with closed its
//     providers, so one that needs a provider that depends on a resource
//     acquired by this with is S108. A with's value that holds a Task leaves the block that drains
//     it before its providers close (resources.md, Release before the
//     end), S108.
//
// Sites are processed inner before outer, so an inner with's release
// needs reach an outer with's acquisitions before the outer one is
// recorded.
type withSite struct {
	x       *syntax.CallExpr
	operand int   // the operand's index in x (rows.md, With 7)
	w       *With // the operand's fact
	file    *fileInfo
	t       *target
	outer   withScope // the with scope of t around the operand
}

// contextOwners maps each entry to the context that declares it, through
// embeddings: an entry's expression and type facts belong to its declaring
// context's package. It indexes each context once, so many withs over one
// embedding chain stay linear. The ownership pass fills it from the with
// sites and the footprint pass reads it (runtime-199 stage 3c r3 o1).
type contextOwners struct {
	of   map[*ContextEntry]*Context
	seen map[*Context]bool
}

func (o *contextOwners) add(w *Context) {
	if w == nil || o.seen[w] {
		return
	}
	if o.seen == nil {
		o.of, o.seen = map[*ContextEntry]*Context{}, map[*Context]bool{}
	}
	o.seen[w] = true
	for _, e := range w.Entries {
		o.of[e] = w
		o.add(e.Embedded)
	}
}

func (c *checker) withOwnership() {
	for _, s := range c.withSites {
		for _, context := range s.w.Contexts {
			c.contextOwners.add(context)
		}
	}
	owners := c.contextOwners.of
	// A body checked twice records its sites twice: the last record of each
	// operand is the one Info holds.
	type operandKey struct {
		x *syntax.CallExpr
		i int
	}
	last := make(map[operandKey]int, len(c.withSites))
	for i, s := range c.withSites {
		last[operandKey{s.x, s.operand}] = i
	}
	refused := map[*ContextEntry]bool{}
	taskHeld := map[*syntax.CallExpr]bool{} // a with whose Task value is reported, once
	for i := range c.withSites {
		s := &c.withSites[i]
		if last[operandKey{s.x, s.operand}] != i {
			continue
		}
		w := s.w
		if len(w.Contexts) == 0 || len(w.Entries) == 0 {
			continue
		}
		w.Acquired = c.acquire(w.Contexts[0], w.Entries, owners)
		owning := false
		for _, a := range w.Acquired {
			owner := owners[a.Entry]
			info := providerInfo(owner)
			if info != nil && info.Types[a.Entry.Provider] == nil && len(c.diags) > 0 {
				// The provider failed to type, and its error is reported
				// already: the program is refused, and an ownership verdict
				// on an untyped provider would only be a cascade worded for
				// a missing package (runtime-199 stage 3b r4 m1).
				continue
			}
			if info == nil || info.Types[a.Entry.Provider] == nil {
				c.cur = s.file
				c.errorf(s.x, CodeOutside, "this context's provider ownership cannot be proved without its defining package's facts; further resource ownership is #197's (resources.md, Context ownership)")
				continue
			}
			if !a.Releases() {
				continue
			}
			owning = true
			if msg := closeWitnessed(a.Close); msg != "" {
				// The release would pass witnesses this with records no
				// instance for: refused here, at the provider, once.
				if owner.pkg != c.pkg {
					c.cur = s.file
					c.errorf(s.x, CodeOutside, "%s", msg)
				} else if !refused[a.Entry] {
					refused[a.Entry] = true
					c.cur = owner.file
					c.errorf(a.Entry.Provider, CodeOutside, "%s", msg)
				}
				continue
			}
			if name := packageRoot(info, a.Entry.Provider); name != "" && (owner.pkg != c.pkg || !refused[a.Entry]) {
				// R11: a package let has no owner to give it to the with;
				// a local moves into it (ownership.go), a made value is
				// the with's own.
				var at syntax.Node = a.Entry.Provider
				c.cur = owner.file
				if owner.pkg != c.pkg {
					at, c.cur = s.x, s.file
				} else {
					refused[a.Entry] = true
				}
				c.errorf(at, CodeType, "this resource provider names the package let %s, which has no owner to hand it to the with; a with owns and releases the providers it builds (resources R11); make the value in the provider", name)
			}
			c.cur = s.file
			for _, cap := range c.releaseNeeds(a.Close) {
				c.chargeRelease(s, a, cap)
			}
		}
		if owning {
			c.ownedBlock(s.x, w, taskHeld)
		}
	}
	c.withSites = nil
	c.capabilityCloses()
}

// capabilityCloses refuses a call of a capability's checked Close (rule
// 4). Every provider of a capability whose operation type has a Close is a
// resource, and the with that built it releases it when its block ends
// (resources.md R24), so a Close through the capability would release it a
// second time, and later uses in the block would reach a closed provider
// (S108). A capability method value is refused already (types.md,
// Capabilities 2), so the call is the one path. It walks every file
// declaration, so a law's with is held to it too (a law is checked as if it
// ran).
func (c *checker) capabilityCloses() {
	for _, fi := range c.files {
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			if call := c.pkg.Info.Calls[x]; call != nil && call.Kind == CallProviderOp && c.pkg.Info.Closes[x] != nil {
				c.cur = fi
				c.errorf(x, CodeType, "%s releases the capability's provider, which the with that built it releases when its block ends (resources.md R24, Context ownership); a capability's Close is not called", exprText(unparen(x.Fun)))
			}
			return true
		})
	}
}

// acquire is the acquisition list of a with of w whose block reaches the
// entries reached (rule 1).
func (c *checker) acquire(w *Context, reached []*ContextEntry, owners map[*ContextEntry]*Context) []*Acquisition {
	want := map[*ContextEntry]bool{}
	var queue []*ContextEntry
	add := func(e *ContextEntry) {
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
	// The context pass's entry order, not a walk of the embeddings: a with
	// reads only what it acquires (the context chain scale case).
	order := make([]*ContextEntry, 0, len(want))
	for e := range want {
		order = append(order, e)
	}
	sort.Slice(order, func(i, j int) bool { return order[i].seq < order[j].seq })
	built := map[*ContextEntry]bool{}
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
		if info := providerInfo(owners[e]); info != nil {
			a.Close = c.providerClose(info.Types[e.Provider])
			if d, _ := c.dropOf(info.Types[e.Provider]); d.Glue() {
				a.Drop = d
			}
		}
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

// closeWitnessed is the S109 of a provider whose generic Close has a type
// parameter constrained by operations, or "". Its release would pass the
// Close's witnesses (a struct of the type argument's operations), and an
// Acquisition records no instance of its Close: a release beyond the
// bounded with, which #197 owns with generated and aggregate Close (C29).
func closeWitnessed(cl *Close) string {
	if cl == nil || cl.Func == nil || cl.Func.Sig == nil {
		return ""
	}
	for _, tp := range cl.Func.Sig.TParams {
		if tp.Kind != KindType {
			continue
		}
		var it *Interface
		switch k := tp.Constraint.(type) {
		case *Interface:
			it = k
		case *Named:
			it, _ = k.Underlying().(*Interface)
		}
		if it != nil && len(it.Methods) > 0 {
			return fmt.Sprintf("a provider whose generic Close has the type parameter %s, constrained by operations, is released with #197: the release passes its witnesses, and a with records no instance of its Close", tp.Name)
		}
	}
	return ""
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
	if f, unknown := s.outer.binder(c.types, cap); unknown {
		return
	} else if f != nil {
		if f.with != nil {
			var e *ContextEntry
			if _, node := f.binds.find(c.types, cap); node != nil {
				e = node.entry
			}
			f.with.discharge(cap, e)
		}
		return
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
	what := a.Close.Receiver
	if t.declUses == nil {
		c.errorf(s.x, CodeMissingCap, "the release of %s when this with ends requires %s, which is not in the inferred capability row of %s and no outer with provides it; a release runs outside the with and widens no inferred row, so write the row of %s (resources.md, Context ownership)", what, cap, t.name, t.name)
		return
	}
	c.errorf(s.x, CodeMissingCap, "the release of %s when this with ends requires %s, which is not in the capability row of %s and no outer with provides it; a release runs outside the with (resources.md, Context ownership)", what, cap, t.name)
}

// discharge adds cap, provided by e, to the with's discharged list, in its
// sorted place, once.
func (w *With) discharge(cap Type, e *ContextEntry) {
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
	if w.Contexts != nil {
		w.Entries = append(w.Entries, nil)
		copy(w.Entries[i+1:], w.Entries[i:])
		w.Entries[i] = e
	}
}

// packageRoot is the package let a provider expression names, itself or
// through a field or an element (R11: it has no owner to hand it to the
// with), or "". Any other provider is made by it (a call's result is the
// caller's, q62) or is a local, which the ownership pass moves into the
// with.
func packageRoot(info *Info, x syntax.Expr) string {
	for {
		switch e := unparen(x).(type) {
		case *syntax.TryExpr:
			x = e.X
		case *syntax.SelectorExpr:
			if _, ok := info.Uses[e.Sel].(*Var); ok && info.Types[e.X] == nil {
				return e.Sel.Name // pkg.v, an imported package let
			}
			x = e.X
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.Ident:
			if v, ok := info.Uses[e].(*Var); ok && v.pkg {
				return v.Name
			}
			return ""
		default:
			return ""
		}
	}
}

// providerInfo is the semantic model of an entry's defining context, never
// the importing package's model of that context's use.
func providerInfo(w *Context) *Info {
	if w == nil || w.pkg == nil {
		return nil
	}
	return w.pkg.Info
}

// ownedBlock reports, in the block of x, an owning with, the exit actions
// that need a capability x provides and a value of x that holds a Task
// (rule 4). A function literal and the trailing block of a call other than
// with are frames of their own: what they register ends with them, before
// the block ends. w is the fact of one operand of x: the operands after it
// are withs between it and its block (rows.md, With 7). taskHeld records
// the withs whose Task value is reported, so a with of several owning
// operands is reported once.
func (c *checker) ownedBlock(x *syntax.CallExpr, w *With, taskHeld map[*syntax.CallExpr]bool) {
	if t := c.pkg.Info.Types[x]; t != nil && c.holdsTask(t) && !taskHeld[x] {
		taskHeld[x] = true
		c.errorf(x, CodeType, "the value of this with holds a Task, which leaves the block that stops it before its providers close; wait on the Task inside the block (resources.md, Release before the end)")
	}
	// Acquisitions are topological: every dependency is visited before
	// its user. A plain provider independent of resources stays usable by
	// an exit action; a provider built from a resource does not.
	closed := map[*ContextEntry]bool{}
	for _, a := range w.Acquired {
		closed[a.Entry] = a.Releases()
		for _, cap := range a.Entry.Uses {
			closed[a.Entry] = closed[a.Entry] || closed[w.Contexts[0].Provider(cap)]
		}
	}
	inspectFrame(c.pkg.Info, x.Trailing, func(n syntax.Node, between []*syntax.CallExpr) bool {
		if n, ok := n.(*syntax.CallExpr); ok && isOnExit(c.pkg.Info.Calls[n]) {
			c.exitAction(n, x, w, between, closed)
		}
		return true
	})
}

// isOnExit reports a call of std scope.OnExit.
func isOnExit(call *Call) bool {
	return call != nil && call.Kind == CallFunc && call.Func != nil && call.Func.Name == "OnExit" &&
		call.Func.Pkg != nil && call.Func.Pkg.Path == "scope"
}

// exitAction reports the exit action registered by the call n inside the
// owning with x when its block needs a capability that x, not a with
// between them, discharges.
func (c *checker) exitAction(n, x *syntax.CallExpr, w *With, between []*syntax.CallExpr, closed map[*ContextEntry]bool) {
	if n.Trailing == nil {
		return
	}
	sig := c.pkg.Info.Literals[n.Trailing]
	if sig == nil {
		return
	}
	// binds reports whether an operand from v on, in its with, binds cap.
	binds := func(v *With, cap Type) bool {
		for ; v != nil; v = v.Next {
			if len(v.Contexts) > 0 && v.Contexts[0].Provider(cap) != nil {
				return true
			}
		}
		return false
	}
	for _, cap := range c.types.resolve(sig.Uses).Elems() {
		inner := binds(w.Next, cap)
		for _, y := range between {
			if inner {
				break
			}
			inner = binds(c.pkg.Info.Withs[y], cap)
		}
		if inner || !closed[w.Contexts[0].Provider(cap)] {
			continue
		}
		c.errorf(n, CodeType, "this exit action runs when the function returns (runtime.md, Exit actions 2), after the with around it closed its providers, but it needs %s, whose provider is or uses an acquired resource of that with; register it outside the with, or release the value in the block (resources.md, Context ownership)", cap)
		return
	}
}
