package check

import (
	"math/bits"
	"strings"
)

// A capSet is a persistent set of capabilities, keyed by their qualified
// names (package path, then name): a treap whose priorities hash the
// names, so its shape depends on the names alone, never on the order in
// which the checker met the types (#196 round 19 M2). Adding an element
// copies one path and leaves every earlier version intact, so a context's
// binding summary shares the largest of its parts instead of copying it
// (round 18). Each node keeps its height, so the cost of a lookup is known
// before it runs (finishBody's allowance).
//
// Each node also carries the entry that provides its capability, so a
// named context's summary is its resolved bindings too (rows.md, Context 5 and
// 7): the union of a component puts its members' own entries over what
// its embeddings bring (a replace), and a with points to that shared
// summary instead of copying it (Context.Provider; round-4 review M1).
//
// Every operation of the context summaries runs on a contextMeter: each node is
// charged before it is read, each node copied or created before it is
// made, each element enumerated before it is passed on, and the operation
// stops when the package's budget would be passed (D16 as amended in
// round 18; contextsum.go). Only the membership test of a with scope while a
// body is checked (has, find), which charges the body's own budget like
// Row.has, and the lowering's lookups (Context.Provider) are not context-summary
// work and are not metered.
// Prior art: OCaml's Set and Erlang maps share structure between
// versions; TypeScript (TS2589) and rustc (recursion_limit) refuse past a
// deterministic, source-only budget instead of accepting unproven.
type capSet struct {
	root *capNode
	size int
	// extra holds an inline context's own entries over the union of its
	// embeddings in root (finishBody), with the same provider identities
	// that the named-context treap carries.
	extra map[capKey]*capNode
}

type capNode struct {
	key capKey
	t   Type
	// entry is the provider entry that binds the capability: a named
	// context's own entry (contextPass.own), nil for an inline context's.
	entry       *ContextEntry
	pri         uint32
	h           int32 // nodes on the longest path down from here
	left, right *capNode
}

// A capKey is a capability's qualified name. A capability is a named,
// non-generic interface declared at package level (isCapability), so its
// package path and name identify it in every package that names it.
type capKey struct{ path, name string }

// keyOf returns t's key; ok is false when t is not a declared named type.
func keyOf(t Type) (k capKey, ok bool) {
	n, isNamed := t.(*Named)
	if !isNamed || n.Obj == nil || n.Orig != nil {
		return capKey{}, false
	}
	if n.Obj.Pkg != nil {
		k.path = n.Obj.Pkg.Path
	}
	k.name = n.Obj.Name
	return k, true
}

func (a capKey) compare(b capKey) int {
	if c := strings.Compare(a.path, b.path); c != 0 {
		return c
	}
	return strings.Compare(a.name, b.name)
}

// priority hashes a key (FNV-1a), so a set's shape is a function of its
// elements' names.
func (a capKey) priority() uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(a.path); i++ {
		h = (h ^ uint32(a.path[i])) * 16777619
	}
	h = (h ^ 0xff) * 16777619
	for i := 0; i < len(a.name); i++ {
		h = (h ^ uint32(a.name[i])) * 16777619
	}
	h ^= h >> 15
	h *= 0x2c1b3c6d
	h ^= h >> 12
	return h
}

func height(n *capNode) int32 {
	if n == nil {
		return 0
	}
	return n.h
}

// height is the number of nodes on the set's longest path: a lookup reads
// at most that many.
func (s *capSet) height() int {
	if s == nil {
		return 0
	}
	return int(height(s.root))
}

// A contextMeter counts the context pass's repeatable work against the
// package's budget (D16 as amended in round 20: contextsum.go). step reports
// whether n more units fit and counts them; once a step does not fit, out
// is set and every later step fails, so no repeatable work is done past
// the budget. An allowance (finishBody) is a meter whose whole limit its
// parent was charged in advance.
type contextMeter struct {
	used, limit int
	out         bool
	parent      *contextMeter
}

func (m *contextMeter) step(n int) bool {
	if m.out || n < 0 || m.used+n > m.limit {
		m.out = true
		return false
	}
	m.used += n
	return true
}

// The two classes of the context pass's work (round 20). Repeatable work can
// run more often than a fixed multiple of the context source it reads (set
// nodes, union elements, probes against a summary, a sort): the meter
// charges it before it runs. Single-pass work reads each context node a
// fixed number of times (an entry, a key, an embedding, a graph step, a
// component member, a root): contextSinglePerUnit bounds it, uncharged.
const (
	repeatable = true
	singlePass = false
)

// contextOp, set only by tests, is called at each data operation of the context
// pass as it is done: a set node read, made or copied, a sort comparison
// or move, an entry, key or embedding read, a graph step, a probe. kind
// names it and repeat gives its class. The calls sit at the data, not at
// the meter's charges, so a test counts each class apart from the meter.
var contextOp func(m *contextMeter, kind string, repeat bool, n int)

func op(m *contextMeter, kind string, repeat bool, n int) {
	if contextOp != nil {
		contextOp(m, kind, repeat, n)
	}
}

// has reports whether t is an element: a with scope's membership test
// while a body is checked. Like Row.has, it charges one lookup step.
func (s *capSet) has(tab *typeTable, t Type) bool {
	found, _ := s.find(tab, t)
	return found
}

// find is has, and also returns the node that holds t. A with's discharge test reads the
// providing entry from it, so a with's Info costs no lookup of its own
// (recordWith).
func (s *capSet) find(tab *typeTable, t Type) (found bool, n *capNode) {
	if s == nil || s.size == 0 && len(s.extra) == 0 {
		return false, nil
	}
	tab.charge(1)
	k, ok := keyOf(t)
	if !ok {
		return false, nil
	}
	if n := s.node(k); n != nil {
		return true, n
	}
	return false, nil
}

// node is the node of k, or nil, read without a meter: the with test
// (find) and the lowering's lookups (Context.Provider), which are not
// context-summary work.
func (s *capSet) node(k capKey) *capNode {
	if s == nil {
		return nil
	}
	if n := s.extra[k]; n != nil {
		return n
	}
	for n := s.root; n != nil; {
		switch c := k.compare(n.key); {
		case c < 0:
			n = n.left
		case c > 0:
			n = n.right
		default:
			return n
		}
	}
	return nil
}

// lookup is has for the context summaries: one step per node, charged before
// the node is read. It returns the node of k, or nil; ok is false when the
// budget ran out.
func (s *capSet) lookup(m *contextMeter, k capKey) (found *capNode, ok bool) {
	if s == nil {
		return nil, true
	}
	for n := s.root; n != nil; {
		if !m.step(1) {
			return nil, false
		}
		op(m, "lookup node", repeatable, 1)
		switch c := k.compare(n.key); {
		case c < 0:
			n = n.left
		case c > 0:
			n = n.right
		default:
			return n, true
		}
	}
	return nil, true
}

// with returns s with t added, provided by e; s is unchanged. An element
// already there keeps its entry. ok is false when the budget ran out; s is
// then returned.
func (s *capSet) with(m *contextMeter, t Type, e *ContextEntry) (out *capSet, ok bool) {
	return s.insert(m, t, e, false)
}

// put is with, except that an element already there is now provided by e:
// a context's own entry over what its embeddings bring (a replace; rows.md,
// Context 7). The path to it is copied, as an insertion's is.
func (s *capSet) put(m *contextMeter, t Type, e *ContextEntry) (out *capSet, ok bool) {
	return s.insert(m, t, e, true)
}

func (s *capSet) insert(m *contextMeter, t Type, e *ContextEntry, over bool) (out *capSet, ok bool) {
	if s == nil {
		s = &capSet{}
	}
	k, isKey := keyOf(t)
	if !isKey {
		return s, true
	}
	root, added, changed, ok := capInsert(m, s.root, k, t, e, k.priority(), over)
	if !ok {
		return s, false
	}
	switch {
	case added:
		return &capSet{root: root, size: s.size + 1}, true
	case changed:
		return &capSet{root: root, size: s.size}, true
	}
	return s, true
}

// capInsert returns n with (k, t) added, provided by e, and whether it was
// added; when over is set, an element already there whose entry is not e
// is copied with e (changed). The nodes on the path are copies, so a
// returned node is never shared and a rotation may change it. A visit and
// a copy are each one step, charged before the node is read or made.
func capInsert(m *contextMeter, n *capNode, k capKey, t Type, e *ContextEntry, pri uint32, over bool) (out *capNode, added, changed, ok bool) {
	if n == nil {
		if !m.step(1) {
			return nil, false, false, false
		}
		op(m, "node made", repeatable, 1)
		return &capNode{key: k, t: t, entry: e, pri: pri, h: 1}, true, false, true
	}
	if !m.step(1) {
		return n, false, false, false
	}
	op(m, "insert node", repeatable, 1)
	cmp := k.compare(n.key)
	if cmp == 0 {
		if !over || n.entry == e {
			return n, false, false, true
		}
		if !m.step(1) {
			return n, false, false, false
		}
		op(m, "node copied", repeatable, 1)
		c := *n
		c.entry = e
		return &c, false, true, true
	}
	var child *capNode
	if cmp < 0 {
		child, added, changed, ok = capInsert(m, n.left, k, t, e, pri, over)
	} else {
		child, added, changed, ok = capInsert(m, n.right, k, t, e, pri, over)
	}
	if !ok || !added && !changed {
		return n, false, false, ok
	}
	if !m.step(1) {
		return n, false, false, false
	}
	op(m, "node copied", repeatable, 1)
	c := *n
	if cmp < 0 {
		c.left = child
		if child.pri > c.pri {
			c.left, child.right = child.right, &c
			c.h = 1 + max(height(c.left), height(c.right))
			child.h = 1 + max(height(child.left), height(child.right))
			return child, added, changed, true
		}
	} else {
		c.right = child
		if child.pri > c.pri {
			c.right, child.left = child.left, &c
			c.h = 1 + max(height(c.left), height(c.right))
			child.h = 1 + max(height(child.left), height(child.right))
			return child, added, changed, true
		}
	}
	c.h = 1 + max(height(c.left), height(c.right))
	return &c, added, changed, true
}

// each calls f on the elements in key order, one step per node, charged
// before the node or its children are read, until f returns false. It
// returns false when the budget ran out or f stopped.
func (s *capSet) each(m *contextMeter, f func(n *capNode) bool) bool {
	if s == nil {
		return true
	}
	var walk func(n *capNode) bool
	walk = func(n *capNode) bool {
		for n != nil {
			if !m.step(1) {
				return false
			}
			op(m, "each node", repeatable, 1)
			if !walk(n.left) {
				return false
			}
			if !f(n) {
				return false
			}
			n = n.right
		}
		return true
	}
	return walk(s.root)
}

// capUnion returns the union of sets, adding the elements of all but the
// largest to the largest: each union costs its smaller sides, counted.
// Reading the sets is single-pass work: a component's sets are its
// members and its successors, each read once.
// The plan depends on the sizes and the order of sets alone, the first of
// two equal sizes being kept, and every caller passes sets in an order
// fixed by the contexts' names and entries (contextsum.go), so the charged work
// does not depend on the declarations' order. ok is false when the budget
// ran out.
//
// The first owns sets are a component's own entries: their elements are
// put, so an own entry provides its capability over what an embedding
// brings (a replace; rows.md, Context 7), whichever side is the largest. The
// others are added, so an element already there keeps its entry.
func capUnion(m *contextMeter, sets []*capSet, owns int) (out *capSet, ok bool) {
	largest := -1
	for i, s := range sets {
		op(m, "union set read", singlePass, 1)
		if s != nil && s.size > 0 && (largest < 0 || s.size > sets[largest].size) {
			largest = i
		}
	}
	if largest < 0 {
		return &capSet{}, true
	}
	out = sets[largest]
	for i, s := range sets {
		if i == largest {
			continue
		}
		add := (*capSet).with
		if i < owns {
			add = (*capSet).put
		}
		if !s.each(m, func(n *capNode) bool { out, ok = add(out, m, n.t, n.entry); return ok }) {
			return nil, false
		}
	}
	return out, true
}

// sortReserve is the most work a top-down merge sort of n elements does:
// n⌈log₂ n⌉ comparisons and, on each of its ⌈log₂ n⌉ levels, two moves of
// each element.
func sortReserve(n int) int {
	if n < 2 {
		return 0
	}
	return 3 * n * bits.Len(uint(n-1))
}

// sortContexts sorts xs by less after it charges sortReserve(len(xs)); it
// does not start when that does not fit, and reports false.
func sortContexts[T any](m *contextMeter, xs []T, less func(a, b T) bool) bool {
	if !m.step(sortReserve(len(xs))) {
		return false
	}
	if len(xs) > 1 {
		mergeSort(m, xs, make([]T, len(xs)), less)
	}
	return true
}

func mergeSort[T any](m *contextMeter, xs, buf []T, less func(a, b T) bool) {
	if len(xs) < 2 {
		return
	}
	mid := len(xs) / 2
	mergeSort(m, xs[:mid], buf[:mid], less)
	mergeSort(m, xs[mid:], buf[mid:], less)
	for i := range xs {
		op(m, "sort move", repeatable, 1)
		buf[i] = xs[i]
	}
	i, j := 0, mid
	for k := range xs {
		op(m, "sort move", repeatable, 1)
		switch {
		case i == mid:
			xs[k] = buf[j]
			j++
		case j == len(xs):
			xs[k] = buf[i]
			i++
		default:
			op(m, "sort comparison", repeatable, 1)
			if less(buf[j], buf[i]) {
				xs[k] = buf[j]
				j++
			} else {
				xs[k] = buf[i]
				i++
			}
		}
	}
}
