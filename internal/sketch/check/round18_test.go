package check

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// opsOf checks src as one package and returns its diagnostics and the
// operation counts of the reference walk's literals and the context
// summaries.
func opsOf(t *testing.T, src string) (diags []Diagnostic, walk, context int) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.run([]*syntax.File{file})
	context = -1
	if c.contexts != nil {
		context = c.contexts.used
	}
	return c.diags, c.walkOps, context
}

// linear asserts that counts measured at sizes 1000, 4000 and 8000 grow
// linearly: each is at most k operations per unit, and the 8000 count is at
// most 8.5 times the 1000 count (a quadratic pass gives 64).
func linear(t *testing.T, what string, counts map[int]int, k int) {
	t.Helper()
	for n, ops := range counts {
		if ops > k*n {
			t.Errorf("%s: %d operations at n=%d, more than %d per unit", what, ops, n, k)
		}
	}
	if r := float64(counts[8000]) / float64(counts[1000]); r > 8.5 {
		t.Errorf("%s: 8000/1000 operation ratio %.1f, want linear growth (%v)", what, r, counts)
	}
	t.Logf("%s: %v", what, counts)
}

// TestContextChainLinear: an acyclic embedding chain of n empty contexts costs
// linear work in the context summaries (round 17 M1: every root and every
// embedding walked the whole chain).
func TestContextChainLinear(t *testing.T) {
	counts := map[int]int{}
	for _, n := range []int{1000, 4000, 8000} {
		var b strings.Builder
		b.WriteString("let W0 = wire {}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let W%d = wire { W%d }\n", i, i-1)
		}
		fmt.Fprintf(&b, "func F() { with W%d {} }\n", n-1)
		diags, _, context := opsOf(t, decided(b.String()))
		if len(diags) > 0 {
			t.Fatalf("n=%d: want acceptance, got %v", n, diags[0])
		}
		counts[n] = context
	}
	// The name sort reserves 3⌈log₂ n⌉ steps per context (capset.go), so the
	// charged count grows by n log n; the rest is linear.
	for n, ops := range counts {
		if sorted := sortReserve(n); ops-sorted > 16*n {
			t.Errorf("wire chain n=%d: %d steps beyond the sort's %d, more than 16 per wire", n, ops-sorted, sorted)
		}
	}
	t.Logf("wire chain: %v", counts)
}

// TestCapSetPersistent: every version of a capSet keeps its elements after
// later inserts, iterates in key order, keeps the treap's order and heap
// invariants and its heights, so lookups stay logarithmic and their cost
// is known in advance.
func TestCapSetPersistent(t *testing.T) {
	m := &contextMeter{limit: 1 << 30}
	key := func(i int) capKey { return capKey{"p", fmt.Sprintf("C%05d", i)} }
	var versions []*capNode
	var sizes []int
	var root *capNode
	size := 0
	for i := range 2000 {
		k := key((i * 7919) % 2003)
		var added bool
		root, added, _, _ = capInsert(m, root, k, nil, nil, k.priority(), false)
		if added {
			size++
		}
		if i%97 == 0 {
			versions = append(versions, root)
			sizes = append(sizes, size)
		}
	}
	lo, hi := capKey{"p", ""}, capKey{"p", "D"}
	var check func(n *capNode, lo, hi capKey) (count, depth int)
	check = func(n *capNode, lo, hi capKey) (int, int) {
		if n == nil {
			return 0, 0
		}
		if n.key.compare(lo) <= 0 || n.key.compare(hi) >= 0 {
			t.Fatalf("key %v outside (%v, %v)", n.key, lo, hi)
		}
		for _, ch := range []*capNode{n.left, n.right} {
			if ch != nil && ch.pri > n.pri {
				t.Fatalf("heap order broken at %v", n.key)
			}
		}
		lc, ld := check(n.left, lo, n.key)
		rc, rd := check(n.right, n.key, hi)
		if int(n.h) != max(ld, rd)+1 {
			t.Fatalf("height %d at %v, want %d", n.h, n.key, max(ld, rd)+1)
		}
		return lc + rc + 1, max(ld, rd) + 1
	}
	for i, v := range versions {
		count, depth := check(v, lo, hi)
		if count != sizes[i] {
			t.Errorf("version %d: %d elements, want %d", i, count, sizes[i])
		}
		if depth > 60 {
			t.Errorf("version %d: depth %d for %d elements", i, depth, count)
		}
	}
	s := &capSet{root: root, size: size}
	prev := lo
	s.each(m, func(n *capNode) bool {
		k := n.key
		if k.compare(prev) <= 0 {
			t.Fatalf("each: %v after %v", k, prev)
		}
		prev = k
		if found, _ := s.lookup(m, k); found == nil {
			t.Fatalf("lookup(%v) false", k)
		}
		return true
	})
	if found, _ := s.lookup(m, key(2003)); found != nil {
		t.Error("lookup(C02003) true")
	}
	// A meter that runs out stops an insert and leaves the set unchanged.
	small := &contextMeter{limit: 3}
	out, _, _, ok := capInsert(small, root, key(99999), nil, nil, key(99999).priority(), false)
	if ok || out != root || !small.out || small.used > small.limit {
		t.Errorf("an insert past the budget: ok=%v, out=%v, used %d of %d", ok, small.out, small.used, small.limit)
	}
	if count, _ := check(root, lo, hi); count != size {
		t.Errorf("the stopped insert changed the set: %d elements, want %d", count, size)
	}
	// The shape is a function of the keys: another insertion order gives
	// the same tree.
	var other *capNode
	for i := 2002; i >= 0; i-- {
		k := key(i)
		if found, _ := s.lookup(m, k); found != nil {
			other, _, _, _ = capInsert(m, other, k, nil, nil, k.priority(), false)
		}
	}
	var same func(a, b *capNode) bool
	same = func(a, b *capNode) bool {
		if a == nil || b == nil {
			return a == b
		}
		return a.key == b.key && same(a.left, b.left) && same(a.right, b.right)
	}
	if !same(root, other) {
		t.Error("two insertion orders of one key set give two shapes")
	}
}

// TestCapSetPut (round-4 review M1): put provides an element already there
// by a new entry and copies only its path, so the earlier version keeps
// its entry and every node off the path is shared; with keeps the entry
// already there; and the shape does not change.
func TestCapSetPut(t *testing.T) {
	m := &contextMeter{limit: 1 << 30}
	var s *capSet
	old, other := &ContextEntry{}, &ContextEntry{}
	types := make([]Type, 500)
	for i := range types {
		types[i] = &Named{Obj: &TypeName{Name: fmt.Sprintf("C%03d", i)}}
		s, _ = s.with(m, types[i], old)
	}
	k, _ := keyOf(types[250])
	kept, _ := s.with(m, types[250], other)
	if kept != s || kept.node(k).entry != old {
		t.Fatal("with replaced an element's entry")
	}
	before := m.used
	put, ok := s.put(m, types[250], other)
	if !ok || put.size != s.size || put.node(k).entry != other || s.node(k).entry != old {
		t.Fatalf("put: size %d of %d, new entry %v, old version's entry kept %v", put.size, s.size, put.node(k).entry == other, s.node(k).entry == old)
	}
	if cost := m.used - before; cost > 2*s.height()+1 {
		t.Errorf("put charged %d steps; its path is at most %d nodes", cost, s.height())
	}
	shared := 0
	var walk func(a, b *capNode)
	walk = func(a, b *capNode) {
		if a == nil || b == nil {
			if a != b {
				t.Fatal("put changed the shape")
			}
			return
		}
		if a == b {
			shared++
			return
		}
		if a.key != b.key || a.h != b.h {
			t.Fatal("put changed the shape")
		}
		walk(a.left, b.left)
		walk(a.right, b.right)
	}
	walk(s.root, put.root)
	if shared == 0 {
		t.Error("put copied the whole set")
	}
}

// TestRecordLiteralLinear: keyed and positional record literals of n
// fields, each a slice literal with elided children, cost linear work in
// the reference walk (round 17 M2: each element scanned the field list).
// Round 17 wrote an anonymous struct literal; the decided language has
// none (#196), so the record is named.
func TestRecordLiteralLinear(t *testing.T) {
	for _, keyed := range []bool{true, false} {
		counts := map[int]int{}
		for _, n := range []int{1000, 4000, 8000} {
			var b strings.Builder
			b.WriteString("type S { a int }\ntype Big {\n")
			for i := range n {
				fmt.Fprintf(&b, " f%d []S,\n", i)
			}
			b.WriteString("}\nfunc F() { _ = Big{")
			for i := range n {
				if keyed {
					fmt.Fprintf(&b, "f%d: ", i)
				}
				b.WriteString("[]S{{a: 1}, {a: 2}}, ")
			}
			b.WriteString("} }\n")
			diags, walk, _ := opsOf(t, decided(b.String()))
			if len(diags) > 0 {
				t.Fatalf("keyed=%v n=%d: want acceptance, got %v", keyed, n, diags[0])
			}
			counts[n] = walk
		}
		linear(t, fmt.Sprintf("record literal (keyed=%v)", keyed), counts, 12)
	}
}

// TestMissingLiteralType: a struct field's composite literal writes its
// type, keyed or positional, as in Go; the elements and keys of a slice or
// map literal may elide it (round 17 C1; the decided language has no fixed
// arrays, #196).
func TestMissingLiteralType(t *testing.T) {
	const decls = "type S { a int }\ntype Wrap[T] { value T }\n" +
		"type Long { alpha int, beta int, gamma int, delta int }\n"
	for _, tc := range []struct{ body, want string }{
		{"_ = Wrap[S]{value: {a: 1}}", "missing type in composite literal; write S{a: 1}"},
		{"_ = Wrap[S]{{a: 1}}", "missing type in composite literal; write S{a: 1}"},
		{"_ = Wrap[[]S]{value: {{a: 1}}}", "missing type in composite literal; write []S{{a: 1}}"},
		{"_ = Wrap[Long]{value: {alpha: 10, beta: 20, gamma: 30, delta: 40}}", "missing type in composite literal; write Long{…}"},
		{"_ = Wrap[S]{value: {\na: 1}}", "missing type in composite literal; write S{…}"},
	} {
		src := decls + "func F() {\n" + tc.body + "\n}\n"
		_, diags := CheckSource(decided(src))
		if len(diags) != 1 || diags[0].Code != CodeType || diags[0].Message != tc.want {
			t.Errorf("%s: want one S108 %q, got %v", tc.body, tc.want, diags)
		}
	}
	for _, ok := range []string{
		"_ = []S{{a: 1}}", "_ = Map[S, S]{{a: 1}: {a: 2}}",
		"_ = Wrap[S]{value: S{a: 1}}", "_ = Wrap[[]S]{value: []S{{a: 1}}}",
	} {
		if _, diags := CheckSource(decided(decls + "func F() {\n" + ok + "\n}\n")); len(diags) > 0 {
			t.Errorf("%s: want acceptance, got %v", ok, diags)
		}
	}
}
