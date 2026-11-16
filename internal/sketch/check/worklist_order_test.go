package check

import (
	"slices"
	"testing"
)

// The worklist's documented order (worklist.go): callees first, on demand,
// and the smallest body first among the roots. The work bound
// (TestSelfCallWorkLinear) holds on its witnesses under either order alone
// (round 14 mutant survey, W1 and W2), so this test states the order
// itself.
func TestWorklistOrder(t *testing.T) {
	node := func(size, post int) *bodyNode { return &bodyNode{size: size, post: post, slot: -1} }
	run := func(nodes map[string]*bodyNode, queue ...string) []string {
		names := map[*bodyNode]string{}
		for k, n := range nodes {
			names[n] = k
		}
		w := &worklist{member: func(n *bodyNode) bool { _, ok := names[n]; return ok }}
		for _, q := range queue {
			w.push(nodes[q])
		}
		var order []string
		w.run(func(n *bodyNode) {
			order = append(order, names[n])
		})
		return order
	}
	for _, tc := range []struct {
		name  string
		nodes map[string]*bodyNode
		edges [][2]string
		want  []string
	}{
		{
			// caller is the smaller body, so it is the first root, but its
			// queued callee is checked before it.
			name:  "callees first, on demand",
			nodes: map[string]*bodyNode{"caller": node(1, 0), "callee": node(5, 1)},
			edges: [][2]string{{"caller", "callee"}},
			want:  []string{"callee", "caller"},
		},
		{
			// Two unrelated roots: the smaller one first, whatever the
			// postorder.
			name:  "smallest first among the roots",
			nodes: map[string]*bodyNode{"big": node(9, 0), "small": node(2, 1)},
			want:  []string{"small", "big"},
		},
		{
			// Equal sizes fall back to the postorder.
			name:  "postorder among equal sizes",
			nodes: map[string]*bodyNode{"late": node(3, 1), "early": node(3, 0)},
			want:  []string{"early", "late"},
		},
	} {
		for _, e := range tc.edges {
			from := tc.nodes[e[0]]
			from.edges = append(from.edges, tc.nodes[e[1]])
		}
		var queue []string
		for k := range tc.nodes {
			queue = append(queue, k)
		}
		slices.Sort(queue)
		if got := run(tc.nodes, queue...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: order %v, want %v", tc.name, got, tc.want)
		}
	}
}
