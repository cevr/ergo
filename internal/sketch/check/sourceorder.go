package check

import (
	"cmp"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// sourceOrder is the order a walk of the package's files meets each
// syntax node (D16: the check's results depend on the source alone). A
// pass that ranges over a map keyed by nodes and builds identities (the
// type table's ids, a row) visits the keys in this order, never in Go's
// map order, so two checks of the same source intern in one order and
// export the same bytes (export_order.go, Law ExportIsDeterministic).
func (c *checker) sourceOrder() map[syntax.Node]int {
	if c.nodeOrder == nil {
		c.nodeOrder = map[syntax.Node]int{}
		for _, f := range c.pkg.files {
			syntax.Inspect(f.syntax, func(n syntax.Node) bool {
				if _, seen := c.nodeOrder[n]; !seen {
					c.nodeOrder[n] = len(c.nodeOrder)
				}
				return true
			})
		}
	}
	return c.nodeOrder
}

// inSourceOrder sorts keys, nodes of this package, in source order; a node
// no walk of the files meets (one the checker made) follows them, by
// position.
func inSourceOrder[K syntax.Node](c *checker, keys []K) []K {
	order := c.sourceOrder()
	rank := func(k K) (int, syntax.Pos, syntax.Pos) {
		if i, ok := order[k]; ok {
			return i, 0, 0
		}
		return len(order), k.Pos(), k.End()
	}
	slices.SortStableFunc(keys, func(a, b K) int {
		ia, pa, ea := rank(a)
		ib, pb, eb := rank(b)
		return cmp.Or(cmp.Compare(ia, ib), cmp.Compare(pa, pb), cmp.Compare(ea, eb))
	})
	return keys
}
