package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Context summaries under a counted budget (D16 as amended in rounds 18 and
// 20).
//
// Context checking asks batch reachability questions over the embedding
// graph: which capabilities each context reaches, and whether two entries for
// one capability reach one context (S111). Their answers can be quadratic in
// the source (a ladder of disjoint chains), and batch reachability has no
// known linear algorithm, so no exact checker is linear on every input.
// The pass's work therefore has a two-part bound (round 20):
//
//  1. Repeatable work, whose count can exceed a fixed multiple of the context
//     source it reads (set nodes read, made or copied, union elements,
//     probes against a summary, a sort), is charged before it runs against
//     one deterministic budget per package, contextBudgetPerUnit × contextSize
//     (capset.go contextMeter).
//  2. Single-pass work reads each context node a fixed number of times
//     (entries, keys, embeddings, the graph walk's steps, each component's
//     members, each root): it is not charged, and it is at most
//     contextSinglePerUnit × contextSize.
//
// So the pass does at most (contextBudgetPerUnit + contextSinglePerUnit) ×
// contextSize work. When the next repeatable step would pass the budget, the
// checker stops: the context or the inline context it was working on gets S111
// (contextBudgetMessage), and every context not summarised yet binds an unknown
// set and adds no diagnostic of its own, so a with of it discharges
// everything (D3). The checker never accepts a context it has not proved, as
// S103 never accepts a switch it has not proved total.
//
// The verdict does not depend on the declarations' order (round 19 M2):
// the pass runs once, before any body, over the named contexts in name order
// and then over the inline contexts grouped by their declaration's name;
// sets are keyed by capability names (capset.go); and every union plan
// follows the contexts' names and their entries' order. The charged total is
// a function of the context graph and its names, and so is the place of the
// refusal.
// Prior art: TypeScript meters instantiations (TS2589, 5,000,000) and
// depth (100), and rustc meters recursion depth (recursion_limit), not
// every node they read; both are deterministic, source-only budgets that
// refuse and leave linear reading to the size of the input. Elixir's typer
// sorts definitions before its recursive traversal (module/types.ex), so
// its traversal does not depend on their order either.

// contextBudgetPerUnit is K, the repeatable work allowed per unit of context
// source: 128. One step is a set node read, copied or created, an element
// enumerated, a probe against a summary, an override recorded, or a sort's
// comparison or move. A context costs 3⌈log₂ n⌉ for the name sort, and a
// capability added along a chain about 2·log₂ n + 2 for its path, a few times over (its context's own
// set, its component's union, a body), so a chain, a fan-in or a large
// base with a chain of 8000 contexts uses under a third of the budget
// (TestContextBudget, measured). A small program pays a constant per context,
// so K must also leave the corpus room: the corpus, the fixtures and the
// examples use under 10% of their budget (TestContextBudgetHeadroom).
const contextBudgetPerUnit = 128

// contextSinglePerUnit is C, the single-pass work per unit of context source: 12.
// The most a unit costs is a context declaration with no entry, which the
// pass collects, makes a graph node for, enters, pops, reads as a
// component member, reads its own set for the union, publishes, probes as
// a root, checks for a summary, loops over for its body, sets up a body
// for and checks for a body: twelve operations. An embedding costs at most
// seven (its entry read three times, a graph edge, a member edge, an
// embedding read and a union set read), an entry of k keys, which is
// 1 + k units, 3 + 4k (its entry read three times, each key read twice,
// probed and added once), and an inline context at most six (its group's
// loop, its read, its store, its body set up and checked, a refusal).
// TestContextBudgetPrefixes and TestContextSinglePass count it on every shape.
const contextSinglePerUnit = 12

// contextBudgetMessage is the S111 of a context whose summary passed the budget.
const contextBudgetMessage = "this wire's embedding graph is too large to check for duplicate entries; split it"

// A contextGroup is the inline contexts of the top-level declarations of one
// name, in source order. The pass handles a group as one unit, so two
// declarations of one name (`var _`, a duplicate) need no order between
// them.
type contextGroup struct {
	key  string
	lits []*syntax.ContextLit
}

// contextSource reads the package's context source once: its size, which fixes
// the budget, and its inline contexts by declaration. The size counts every
// context declaration and literal, its entries and embeddings, the capability
// names written in context keys and in needs rows, and one. This walk defines
// the budget, so it is not charged to it; it is linear in the source.
func (c *checker) contextSource() (size int, groups []*contextGroup, hasContexts bool) {
	size = 1
	byKey := map[string]*contextGroup{}
	owners := map[*syntax.FuncDecl]*Named{}
	for _, f := range c.funcs {
		if f.owner != nil {
			owners[f.Decl] = f.owner
		}
	}
	for _, f := range c.files {
		for _, d := range f.syntax.Decls {
			key := declKey(d)
			if fd, ok := d.(*syntax.FuncDecl); ok && owners[fd] != nil {
				// Functions of one name on distinct types (196m) are
				// distinct declarations, keyed as methods were.
				key = "func " + owners[fd].Obj.Name + "." + fd.Name.Name
			}
			syntax.Inspect(d, func(n syntax.Node) bool {
				switch n := n.(type) {
				case *syntax.ContextDecl:
					size++
					hasContexts = true
				case *syntax.ContextLit:
					size++
					hasContexts = true
					g := byKey[key]
					if g == nil {
						g = &contextGroup{key: key}
						byKey[key] = g
						groups = append(groups, g)
					}
					g.lits = append(g.lits, n)
				case *syntax.ContextEntry:
					size++
					if n.Kind != syntax.ContextEmbed {
						size += 1 + len(n.Keys)
					}
				case *syntax.NeedsClause:
					size += len(n.Names)
				}
				return true
			})
		}
	}
	return size, groups, hasContexts
}

// declKey names a top-level declaration independently of its place.
func declKey(d syntax.Decl) string {
	switch d := d.(type) {
	case *syntax.FuncDecl:
		return "func " + d.Name.Name
	case *syntax.VarDecl:
		var names []string
		for _, s := range d.Specs {
			for _, n := range s.Names {
				names = append(names, n.Name)
			}
		}
		return "let " + strings.Join(names, ",")
	case *syntax.ContextDecl:
		return "wire " + d.Name.Name
	case *syntax.TypeDecl:
		return "type " + d.Name.Name
	}
	return "decl"
}

// contextBinds returns the capabilities a named context binds: its entries and
// those of every context its embeddings reach (contextSummaries); nil when its
// summary was refused.
func (c *checker) contextBinds(w *Context) *capSet {
	c.contextSummaries()
	return w.binds
}

// A contextComp is a strongly connected component of the embedding graph and
// its binding summary. Two contexts of one component lie on an embedding
// cycle, and so does a context that embeds itself.
type contextComp struct {
	binds   *capSet // nil when refused
	unknown bool
	done    bool
}

// A bodySum is a context body's summary: what it binds, whether that is
// known, and its S111 and replace findings, which contextBody reports on
// every pass. An inline context's body is prepared by the pass (its
// embeddings' union and their collisions) and finished at its first check,
// where its keys resolve in the enclosing body's scope, within the
// allowance the pass charged for it (finishBody).
type bodySum struct {
	binds    *capSet
	known    bool
	findings []contextFinding

	// overrides are a named context's own replace overrides (Context.Overrides).
	overrides []Override

	pending         bool
	embedded, cycle *capSet
	collisions      []collision
	allowance       int
	finishes        int // finishBody runs, at most one (TestContextBudgetPrefixes)
}

type contextFinding struct {
	at        syntax.Node
	code, msg string
}

// A collision is a capability two embeddings of one body bind (Context 7):
// entry is the provider the embeddings' union did not keep.
type collision struct {
	cap   Type
	key   capKey
	at    syntax.Expr
	entry *ContextEntry
}

// contextSummaries runs the context pass once, before any body (decl.go run):
//  1. the named contexts are ordered by name, and the inline context groups by
//     their declaration's name;
//  2. each named context's own entries are collected, in name order;
//  3. the strongly connected components of the embedding graph (Tarjan,
//     roots in name order, embeddings in entry order) get their summaries
//     in reverse topological order: the union of the members' own entries
//     and the successors' summaries, an own entry over a successor's (a
//     replace), shared by every member, every embedding and every with
//     of them (round 17 M1, round 18, round-4 review M1);
//  4. each named context's body is summarised, in name order, with what
//     its replaces override (Context.Overrides);
//  5. each inline context is prepared, group by group, and the repeatable
//     work of finishing it is charged as its allowance.
//
// Every repeatable step is charged before it runs. Past the budget, the unit being
// worked on is refused: a named context gets Context.budget (reported by
// checkContexts), an inline context group gets the S111 at each of its contexts,
// and nothing after it is summarised.
func (c *checker) contextSummaries() {
	if c.contextsDone {
		return
	}
	c.contextsDone = true
	c.contextComp = map[*Context]*contextComp{}
	c.contextBodies = map[*syntax.ContextBody]*bodySum{}
	c.contextEntries = map[*syntax.ContextEntry]*ContextEntry{}
	size, groups, hasContexts := c.contextSource()
	if !hasContexts {
		return
	}
	limit := contextBudgetPerUnit * size
	if c.contextLimit != nil {
		limit = c.contextLimit(size)
	}
	m := &contextMeter{limit: limit}
	c.contexts = m
	savedCur, savedFn := c.cur, c.fn
	c.fn = nil
	c.silent++
	defer func() { c.silent--; c.cur, c.fn = savedCur, savedFn }()

	p := &contextPass{c: c, m: m}
	contexts, ok := p.order(groups)
	if !ok {
		// Not even the contexts' order fits: every context is refused.
		for _, w := range c.pkg.Contexts {
			op(m, "refuse", singlePass, 1)
			w.budget = true
		}
		for _, g := range groups {
			p.refuseGroup(g)
		}
		p.stopped = true
	}
	if !p.stopped {
		p.own(contexts)
	}
	if !p.stopped {
		p.components(contexts)
	}
	// Every context without a summary binds an unknown set.
	refused := &contextComp{unknown: true}
	for _, w := range c.pkg.Contexts {
		op(m, "summary check", singlePass, 1)
		if comp := c.contextComp[w]; comp == nil || !comp.done {
			c.contextComp[w] = refused
			w.binds, w.unknown = nil, true
		}
	}
	for _, w := range contexts {
		if p.stopped {
			break
		}
		op(m, "body loop", singlePass, 1)
		p.namedBody(w)
	}
	for _, g := range groups {
		if p.stopped {
			break
		}
		op(m, "group loop", singlePass, 1)
		p.inline(g)
	}
	// Every body not summarised binds an unknown set and has no finding.
	for _, w := range c.pkg.Contexts {
		op(m, "body check", singlePass, 1)
		if c.contextBodies[w.Decl.Body] == nil {
			c.contextBodies[w.Decl.Body] = &bodySum{}
		}
	}
	for _, g := range groups {
		for _, lit := range g.lits {
			op(m, "body check", singlePass, 1)
			if c.contextBodies[lit.Body] == nil {
				c.contextBodies[lit.Body] = &bodySum{}
			}
		}
	}
}

// A contextPass is the state of one run of contextSummaries.
type contextPass struct {
	c       *checker
	m       *contextMeter
	stopped bool
	nodes   map[*Context]*contextNode
}

type contextNode struct {
	index, low int
	onStack    bool
	embeds     []*Context
}

// refuse marks the named context being summarised and stops the pass.
func (p *contextPass) refuse(w *Context) {
	if !p.stopped {
		p.stopped, w.budget = true, true
	}
}

func (p *contextPass) refuseGroup(g *contextGroup) {
	for _, lit := range g.lits {
		op(p.m, "refuse", singlePass, 1)
		p.c.contextBodies[lit.Body] = &bodySum{findings: []contextFinding{{lit, CodeDuplicateCap, contextBudgetMessage}}}
	}
}

// order builds the named contexts' name order and the groups' order. Reading
// the contexts is single-pass; each sort is repeatable and reserves its bound
// first (sortContexts).
func (p *contextPass) order(groups []*contextGroup) ([]*Context, bool) {
	c, m := p.c, p.m
	contexts := make([]*Context, 0, len(c.pkg.Contexts))
	for _, w := range c.pkg.Contexts {
		op(m, "collect wire", singlePass, 1)
		contexts = append(contexts, w)
	}
	if !sortContexts(m, contexts, func(a, b *Context) bool { return a.Name < b.Name }) {
		return nil, false
	}
	if !sortContexts(m, groups, func(a, b *contextGroup) bool { return a.key < b.key }) {
		return nil, false
	}
	return contexts, true
}

// eachKey calls f on an entry's keys in order, without copying them.
func eachKey(e *syntax.ContextEntry, f func(k syntax.Expr) bool) bool {
	if !f(e.Key) {
		return false
	}
	for _, k := range e.Keys {
		if !f(k) {
			return false
		}
	}
	return true
}

// own collects each named context's own entries and its embeddings. Reading
// them is single-pass; adding a capability to the context's set is
// repeatable and charged (capInsert).
func (p *contextPass) own(contexts []*Context) {
	c, m := p.c, p.m
	p.nodes = make(map[*Context]*contextNode, len(contexts))
	for _, w := range contexts {
		op(m, "graph node", singlePass, 1)
		n := &contextNode{index: -1}
		p.nodes[w] = n
		// Defensive: context summaries collect facts silently; checkContexts reports.
		c.cur = w.file
		set, known := &capSet{}, true
		for _, e := range w.Decl.Body.Entries {
			op(m, "entry read", singlePass, 1)
			if e.Kind == syntax.ContextEmbed {
				inner := c.embeddedContext(e)
				if inner == nil {
					known = false
					continue
				}
				n.embeds = append(n.embeds, inner)
				continue
			}
			// The entry's Info record, made here once, so the summary's
			// nodes carry it (checkContexts fills it in).
			entry := &ContextEntry{Provider: e.Value, Replace: e.Replace.IsValid(), seq: len(c.contextEntries)}
			c.contextEntries[e] = entry
			if !eachKey(e, func(k syntax.Expr) bool {
				op(m, "key read", singlePass, 1)
				t := c.rowElem(k, KindUses, nil)
				if t == nil {
					return true
				}
				var ok bool
				set, ok = set.with(m, t, entry)
				return ok
			}) {
				p.refuse(w)
				return
			}
		}
		w.own, w.unknown = set, !known
	}
}

// components summarises the embedding graph's strongly connected
// components (Tarjan). The walk is single-pass: it enters each context once,
// follows each embedding once, pops each context once, reads each member and
// its embeddings once and probes each root once. The unions are repeatable
// and charged (capUnion).
func (p *contextPass) components(contexts []*Context) {
	c, m := p.c, p.m
	index := 0
	var stack []*Context
	var connect func(w *Context)
	connect = func(w *Context) {
		op(m, "graph enter", singlePass, 1)
		n := p.nodes[w]
		n.index, n.low = index, index
		index++
		stack = append(stack, w)
		n.onStack = true
		for _, e := range n.embeds {
			op(m, "graph edge", singlePass, 1)
			d := p.nodes[e]
			if d.index < 0 {
				connect(e)
				if p.stopped {
					return
				}
				n.low = min(n.low, d.low)
			} else if d.onStack {
				n.low = min(n.low, d.index)
			}
		}
		if n.low != n.index {
			return
		}
		comp := &contextComp{}
		var members []*Context
		for {
			op(m, "graph pop", singlePass, 1)
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			p.nodes[v].onStack = false
			c.contextComp[v] = comp
			members = append(members, v)
			if v == w {
				break
			}
		}
		// The summary joins the members' entries and each successor
		// component's summary once, into the largest of them (capUnion):
		// the members' own sets first, then the successors in the order the
		// walk met them. An own entry provides its capability over a
		// successor's (a replace), so the summary is the resolved bindings
		// a with reads (Context.Provider).
		sets := make([]*capSet, len(members))
		var succs []*capSet
		succ := map[*contextComp]bool{}
		for i, v := range members {
			op(m, "member read", singlePass, 1)
			sets[i] = v.own
			comp.unknown = comp.unknown || v.unknown
			for _, e := range p.nodes[v].embeds {
				op(m, "member edge", singlePass, 1)
				if d := c.contextComp[e]; d != comp && !succ[d] {
					succ[d] = true
					succs = append(succs, d.binds)
					comp.unknown = comp.unknown || d.unknown
				}
			}
		}
		binds, ok := capUnion(m, append(sets, succs...), len(members))
		if !ok {
			p.refuse(w)
			return
		}
		comp.binds, comp.done = binds, true
		for _, v := range members {
			op(m, "member publish", singlePass, 1)
			v.binds, v.unknown = comp.binds, comp.unknown
		}
	}
	for _, w := range contexts {
		op(m, "root probe", singlePass, 1)
		if p.nodes[w].index < 0 {
			connect(w)
			if p.stopped {
				return
			}
		}
	}
}

// namedBody summarises a named context's body in its own closed context, where
// its keys resolve as checkContexts resolves them.
func (p *contextPass) namedBody(w *Context) {
	c, m := p.c, p.m
	// Defensive: body summaries run silently; checkContexts reports their refusals.
	c.cur = w.file
	c.fn = &funcCtx{scope: newScope(c.pkg.Scope)}
	defer func() { c.fn = nil }()
	s := &bodySum{}
	if !c.prepareBody(s, w.Decl.Body, w, m) || !c.finishBody(s, w.Decl.Body, m) {
		p.refuse(w)
		s = &bodySum{}
	}
	c.contextBodies[w.Decl.Body] = s
	w.Overrides = s.overrides
}

// inline prepares a group's inline contexts and charges their allowances.
func (p *contextPass) inline(g *contextGroup) {
	c, m := p.c, p.m
	sums := make([]*bodySum, len(g.lits))
	for i, lit := range g.lits {
		op(m, "inline read", singlePass, 1)
		s := &bodySum{pending: true}
		if !c.prepareBody(s, lit.Body, nil, m) || !m.step(s.allowance) {
			p.stopped = true
			p.refuseGroup(g)
			return
		}
		sums[i] = s
	}
	for i, lit := range g.lits {
		op(m, "inline store", singlePass, 1)
		c.contextBodies[lit.Body] = sums[i]
	}
}

// embeddedBinds returns what the embedding of inner contributes to the
// body of the named context self (nil for an inline context), whether every context
// it reaches exists and was summarised, and whether inner is on self's own
// embedding cycle. Such an embedding, on a reported cycle (S108, Context 5),
// contributes the component's summary, and the body counts none of it as
// a second entry, against the context's own entries or its other embeddings:
// what it brings depends on how the cycle is fixed (D3; round 16 m3, round
// 18). A replace may still name it.
func (c *checker) embeddedBinds(inner, self *Context) (binds *capSet, known, onCycle bool) {
	comp := c.contextComp[inner]
	return comp.binds, !comp.unknown, self != nil && comp == c.contextComp[self]
}

// bodySummary returns the summary of a named or inline context body, which
// the pass decided; an inline body is finished here, at its first check,
// within its allowance. After a refusal a body binds an unknown set and
// has no finding of its own (D3).
func (c *checker) bodySummary(body *syntax.ContextBody) *bodySum {
	c.contextSummaries()
	s := c.contextBodies[body]
	if s == nil {
		panic("check: a wire body outside the wire pass")
	}
	if s.pending {
		s.pending = false
		allowance := &contextMeter{limit: s.allowance, parent: c.contexts}
		if !c.finishBody(s, body, allowance) {
			panic("check: a wire body passed the allowance charged for it")
		}
	}
	return s
}

// prepareBody joins a body's embeddings on the meter, and reports false
// when the budget ran out. Two embedded contexts that bind one capability
// collide (S111, finishBody) unless a replace entry of this body overrides
// it (rows.md, Context 7). The embeddings' summaries are joined into the
// largest, the first of equal sizes in entry order, which is shared, not
// copied, and a collision is found against it or among the others, at the
// later embedding. Reading the entries and choosing the largest is
// single-pass; enumerating the other summaries, probing for collisions
// and adding to the union are repeatable and charged. It sets the
// allowance of finishBody's repeatable work: per key, a lookup in the
// union and one in the cycle's summary (at most their heights), and one
// step per collision.
func (c *checker) prepareBody(s *bodySum, body *syntax.ContextBody, self *Context, m *contextMeter) bool {
	s.known = true
	var reaches []*capSet
	var keys []syntax.Expr
	names := 0
	for _, e := range body.Entries {
		op(m, "entry read", singlePass, 1)
		if e.Kind != syntax.ContextEmbed {
			names += 1 + len(e.Keys)
			continue
		}
		inner := c.embeddedContext(e)
		if inner == nil {
			s.known = false
			continue
		}
		reach, known, onCycle := c.embeddedBinds(inner, self)
		if !known {
			s.known = false
		}
		if reach == nil {
			reach = &capSet{}
		}
		if onCycle {
			s.cycle = reach // the same summary for every such embedding
			continue
		}
		reaches = append(reaches, reach)
		keys = append(keys, e.Key)
	}
	largest := -1
	for i, r := range reaches {
		op(m, "embedding read", singlePass, 1)
		if largest < 0 || r.size > reaches[largest].size {
			largest = i
		}
	}
	embedded := &capSet{}
	if largest >= 0 {
		embedded = reaches[largest]
	}
	seen := map[capKey]bool{} // the other embeddings' capabilities so far
	var early []collision     // those met before the largest
	for j, r := range reaches {
		if j == largest {
			for _, e := range early {
				if !m.step(1) {
					return false
				}
				op(m, "collision probe", repeatable, 1)
				found, ok := r.lookup(m, e.key)
				if !ok {
					return false
				}
				if found != nil {
					s.collisions = append(s.collisions, collision{e.cap, e.key, keys[j], e.entry})
				}
			}
			continue
		}
		if !r.each(m, func(n *capNode) bool {
			k, t := n.key, n.t
			if !m.step(1) {
				return false
			}
			op(m, "collision probe", repeatable, 1)
			collide := seen[k]
			if !collide && largest >= 0 && j > largest {
				found, ok := reaches[largest].lookup(m, k)
				if !ok {
					return false
				}
				collide = found != nil
			}
			if collide {
				s.collisions = append(s.collisions, collision{t, k, keys[j], n.entry})
			}
			if !seen[k] {
				seen[k] = true
				if j < largest {
					early = append(early, collision{t, k, nil, n.entry})
				}
			}
			var ok bool
			embedded, ok = embedded.with(m, t, n.entry)
			return ok
		}) {
			return false
		}
	}
	s.embedded = embedded
	s.allowance = names*(embedded.height()+s.cycle.height()) + len(s.collisions)
	return true
}

// finishBody decides a body's entries on the meter, and reports false when
// it ran out: an entry for a capability already bound, other than a
// replace, is S111; a replace of a capability no embedding binds is S108
// (rows.md, Context 1 and 7); a collision of two embeddings is S111 unless a
// replace names it. It runs once per body (finishes). Reading the entries
// and keys and the body's own key map are single-pass; the lookups in the
// summaries and the collisions are repeatable and charged, within the
// allowance prepareBody set.
//
// A named context's replace also records what it overrides (s.overrides,
// Context.Overrides), from the same lookups: the entry the embeddings' union
// binds for its capability and each colliding embedded entry, ordered by
// the context pass's entry order (ContextEntry.seq) and each once (a diamond
// brings one entry twice). Sorting them is repeatable and charged; an
// inline context records none (its entries are stage E's, 196x), so its
// allowance is unchanged.
func (c *checker) finishBody(s *bodySum, body *syntax.ContextBody, m *contextMeter) bool {
	s.finishes++
	op(m, "body setup", singlePass, 1)
	own := map[capKey]*capNode{}
	type place struct{ entry, key int } // 1-based
	ownAt := map[capKey]place{}         // where each capability is first bound
	replaced := map[capKey]bool{}
	type override struct {
		cap   Type
		by    *ContextEntry
		cands []*ContextEntry
	}
	var overrides []*override
	overrideOf := map[capKey]*override{}
	c.silent++
	defer func() { c.silent-- }()
	for i, e := range body.Entries {
		op(m, "entry read", singlePass, 1)
		if e.Kind == syntax.ContextEmbed {
			continue
		}
		constructor := c.isConstructor(e.Value)
		by := c.contextEntries[e]
		named := by != nil
		if by == nil {
			// Inline summaries keep the very same entry subsequently filled
			// by contextBody and read by With.Acquired.
			by = &ContextEntry{Provider: e.Value, Replace: e.Replace.IsValid(), seq: len(c.contextEntries)}
			c.contextEntries[e] = by
		}
		at := 0
		if !eachKey(e, func(k syntax.Expr) bool {
			op(m, "key read", singlePass, 1)
			at++
			t := c.contextEntryKey(k)
			if t == nil {
				return true
			}
			key, isKey := keyOf(t)
			if !isKey {
				return true
			}
			// One context binds a capability once: a second own entry, replace
			// or not, is S111 naming both (rows.md, Context 1), since Info
			// holds one binding per capability (D-A4).
			op(m, "own probe", singlePass, 1)
			if first, dup := ownAt[key]; dup {
				msg := fmt.Sprintf("%s has a second entry in this wire, entry %d; the first is entry %d, and a wire binds a capability once (rows.md, Wire 1)", t, i+1, first.entry)
				if first.entry == i+1 {
					msg = fmt.Sprintf("%s is listed twice in entry %d, as its keys %d and %d, and a wire binds a capability once (rows.md, Wire 1)", t, i+1, first.key, at)
				}
				s.findings = append(s.findings, contextFinding{k, CodeDuplicateCap, msg})
				return true
			}
			ownAt[key] = place{i + 1, at}
			switch {
			case e.Kind == syntax.ContextReplace:
				replaced[key] = true
				if constructor {
					break
				}
				inEmbedded, ok := s.embedded.lookup(m, key)
				if !ok {
					return false
				}
				// A replace may name what an embedding on the context's own
				// cycle brings.
				inCycle, ok := s.cycle.lookup(m, key)
				if !ok {
					return false
				}
				if inEmbedded == nil && inCycle == nil {
					s.findings = append(s.findings, contextFinding{k, CodeType, fmt.Sprintf("replace %s names no entry of an embedded wire (rows.md, Wire 7)", t)})
				}
				if named && inEmbedded != nil && inEmbedded.entry != nil {
					o := &override{cap: t, by: by, cands: []*ContextEntry{inEmbedded.entry}}
					overrides = append(overrides, o)
					overrideOf[key] = o
				}
			case !constructor:
				found, ok := s.embedded.lookup(m, key)
				if !ok {
					return false
				}
				if found != nil {
					s.findings = append(s.findings, contextFinding{k, CodeDuplicateCap, fmt.Sprintf("%s has a second entry in this wire; only replace overrides an embedded entry (rows.md, Wire 1; README Gap 28)", t)})
				}
			}
			op(m, "own insert", singlePass, 1)
			own[key] = &capNode{key: key, t: t, entry: by}
			return true
		}) {
			return false
		}
	}
	for _, k := range s.collisions {
		if !m.step(1) {
			return false
		}
		op(m, "collision", repeatable, 1)
		if !replaced[k.key] {
			s.findings = append(s.findings, contextFinding{k.at, CodeDuplicateCap, fmt.Sprintf("%s is bound by two embedded wires; replace one entry (rows.md, Wire 7)", k.cap)})
		} else if o := overrideOf[k.key]; o != nil && k.entry != nil {
			o.cands = append(o.cands, k.entry)
		}
	}
	for _, o := range overrides {
		if !sortContexts(m, o.cands, func(a, b *ContextEntry) bool { return a.seq < b.seq }) {
			return false
		}
		for i, e := range o.cands {
			if !m.step(1) {
				return false
			}
			op(m, "override", repeatable, 1)
			if i > 0 && o.cands[i-1] == e {
				continue // one entry two embeddings bring (a diamond)
			}
			s.overrides = append(s.overrides, Override{Cap: o.cap, Entry: e, By: o.by})
		}
	}
	s.binds = &capSet{root: s.embedded.root, size: s.embedded.size, extra: own}
	s.embedded, s.cycle, s.collisions = nil, nil, nil
	return true
}
