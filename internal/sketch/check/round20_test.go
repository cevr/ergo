package check

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// An opCount counts the wire pass's data operations through wireOp, apart
// from the meter: each operation where it touches the data, by class
// (repeatable or single-pass) and by kind, per meter and per root meter
// (an allowance's operations count toward its parent too). A repeatable
// operation done while its meter had charged less is recorded in early
// (round 20: the two-part bound).
type opCount struct {
	mu             sync.Mutex
	repeat, single map[*wireMeter]int
	kinds          map[string]int
	early          []string
}

func countOps(t *testing.T) *opCount {
	t.Helper()
	oc := &opCount{repeat: map[*wireMeter]int{}, single: map[*wireMeter]int{}, kinds: map[string]int{}}
	wireOp = func(m *wireMeter, kind string, repeat bool, n int) {
		oc.mu.Lock()
		defer oc.mu.Unlock()
		oc.kinds[kind] += n
		for r := m; r != nil; r = r.parent {
			if !repeat {
				oc.single[r] += n
				continue
			}
			oc.repeat[r] += n
			if oc.repeat[r] > r.used && len(oc.early) < 5 {
				oc.early = append(oc.early, fmt.Sprintf("%s: %d repeatable operations with %d of %d charged", kind, oc.repeat[r], r.used, r.limit))
			}
		}
	}
	t.Cleanup(func() { wireOp = nil })
	return oc
}

// wireNodes counts the wire source of src, independently of wireSource:
// every wire declaration and literal, every entry, for an entry that is
// not an embedding one more and one per key, every name of a needs row,
// and one.
func wireNodes(t *testing.T, src string) int {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	n := 1
	syntax.Inspect(file, func(x syntax.Node) bool {
		switch x := x.(type) {
		case *syntax.ContextDecl, *syntax.ContextLit:
			n++
		case *syntax.ContextEntry:
			n++
			if x.Kind != syntax.ContextEmbed {
				n += 1 + len(x.Keys)
			}
		case *syntax.NeedsClause:
			n += len(x.Names)
		}
		return true
	})
	return n
}

// bounded checks the two-part bound of one check of src: its repeatable
// operations at most what the meter charged, each before it ran; its
// single-pass operations at most wireSinglePerUnit per wire node; so all
// its operations at most the charge plus that; and every body finished
// at most once.
func (oc *opCount) bounded(t *testing.T, what, src string, c *checker) {
	t.Helper()
	m := c.wires
	nodes := wireNodes(t, src)
	if c.wireLimit == nil && m.limit != wireBudgetPerUnit*nodes {
		t.Errorf("%s: budget %d, want %d × %d wire nodes", what, m.limit, wireBudgetPerUnit, nodes)
	}
	if oc.repeat[m] > m.used || len(oc.early) > 0 {
		t.Errorf("%s: %d repeatable operations, %d charged (%v)", what, oc.repeat[m], m.used, oc.early)
	}
	if oc.single[m] > wireSinglePerUnit*nodes {
		t.Errorf("%s: %d single-pass operations for %d wire nodes, more than %d each", what, oc.single[m], nodes, wireSinglePerUnit)
	}
	if oc.repeat[m]+oc.single[m] > m.used+wireSinglePerUnit*nodes {
		t.Errorf("%s: %d operations, more than %d charged and %d × %d nodes", what, oc.repeat[m]+oc.single[m], m.used, wireSinglePerUnit, nodes)
	}
	for _, s := range c.wireBodies {
		if s.finishes > 1 {
			t.Errorf("%s: a body finished %d times", what, s.finishes)
		}
	}
}

// checkLimited checks src as one package, with its wire budget replaced by
// limit when limit is not nil.
func checkLimited(t *testing.T, src string, limit func(int) int) ([]Diagnostic, *checker) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.wireLimit = limit
	c.run([]*syntax.File{file})
	return c.sorted(), c
}

func allBudget(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Code != CodeDuplicateCap || d.Message != wireBudgetMessage {
			return false
		}
	}
	return len(diags) > 0
}

var budgetShapes = []string{"entry chain", "wide fan-in", "large-base chain", "replace diamond", "disjoint", "empty independent", "inline fan-in", "multi-key", "inline embeddings"}

// TestWireBudgetPrefixes: for every shape, at every budget from zero to
// the work the full check charges, the two-part bound holds (bounded),
// the meter stays within its budget, and the verdict is the full check's
// at its exact cost and only budget refusals below it: every prefix of the
// work that ends at a refusal, a zero and a small budget among them.
func TestWireBudgetPrefixes(t *testing.T) {
	oc := countOps(t)
	for _, shape := range budgetShapes {
		src := wireFamily(shape, 4)
		full, c := checkLimited(t, src, nil)
		cost := c.wires.used
		oc.bounded(t, shape, src, c)
		for limit := 0; limit <= cost; limit++ {
			diags, c := checkLimited(t, src, func(int) int { return limit })
			if c.wires.used > limit {
				t.Fatalf("%s at %d: %d charged", shape, limit, c.wires.used)
			}
			oc.bounded(t, fmt.Sprintf("%s at %d", shape, limit), src, c)
			switch {
			case limit == cost:
				if fmt.Sprint(diags) != fmt.Sprint(full) {
					t.Errorf("%s at its cost %d: %v, want %v", shape, cost, diags, full)
				}
			case !allBudget(diags):
				t.Errorf("%s at %d of %d: want only budget refusals, got %v", shape, limit, cost, diags)
			}
		}
		t.Logf("%s n=4: %d steps, %d budgets checked", shape, cost, cost+1)
	}
}

// TestWireSinglePass: single-pass work stays within wireSinglePerUnit per
// wire node on every shape at sizes 1 to 8000, at the real budget, and the
// repeatable work within the charge.
func TestWireSinglePass(t *testing.T) {
	oc := countOps(t)
	worst, worstWhat := 0.0, ""
	for _, shape := range budgetShapes {
		for _, n := range []int{1, 2, 7, 64, 1000} {
			src := wireFamily(shape, n)
			_, c := checkLimited(t, src, nil)
			what := fmt.Sprintf("%s n=%d", shape, n)
			oc.bounded(t, what, src, c)
			if r := float64(oc.single[c.wires]) / float64(wireNodes(t, src)); r > worst {
				worst, worstWhat = r, what
			}
		}
	}
	t.Logf("the most single-pass operations per wire node: %.2f (%s), bound %d", worst, worstWhat, wireSinglePerUnit)
}

// TestWireContractMultiKey (the round-20 reviewer's contract, under the
// two-part bound): one entry of 1000 and of 8000 keys, at a budget of 3
// and at the real one, reads its keys once each, uncopied, and charges
// every set node before it reads it.
func TestWireContractMultiKey(t *testing.T) {
	oc := countOps(t)
	for _, n := range []int{1000, 8000} {
		src := wireFamily("multi-key", n)
		for _, limit := range []func(int) int{func(int) int { return 3 }, nil} {
			diags, c := checkLimited(t, src, limit)
			oc.bounded(t, fmt.Sprintf("multi-key n=%d", n), src, c)
			if limit != nil && !allBudget(diags) {
				t.Errorf("multi-key n=%d at 3: %v", n, diags)
			}
			if limit == nil && len(diags) > 0 {
				t.Errorf("multi-key n=%d: %v", n, diags[0])
			}
		}
	}
}

// TestWireContractInline (the round-20 reviewer's contract, under the
// two-part bound): an inline wire of 1 to 8000 empty embeddings and no
// entry reads each embedding a fixed number of times, finishes once, and
// its finish does no repeatable work past its allowance.
func TestWireContractInline(t *testing.T) {
	oc := countOps(t)
	for _, n := range []int{1, 4, 1000, 8000} {
		src := wireFamily("inline embeddings", n)
		diags, c := checkLimited(t, src, nil)
		if len(diags) > 0 {
			t.Fatalf("n=%d: %v", n, diags)
		}
		oc.bounded(t, fmt.Sprintf("inline embeddings n=%d", n), src, c)
		finished := 0
		for _, s := range c.wireBodies {
			finished += s.finishes
		}
		if finished != 2 { // the named Empty and the inline wire
			t.Errorf("n=%d: %d bodies finished, want 2", n, finished)
		}
		for m, r := range oc.repeat {
			if m.parent == c.wires && r > m.used {
				t.Errorf("n=%d: an allowance of %d did %d repeatable operations", n, m.limit, r)
			}
		}
	}
	if strings.Count(wireFamily("inline embeddings", 3), "\t\tEmpty,\n") != 3 {
		t.Fatal("the family does not write one embedding per line")
	}
}

// TestWireBudgetOrder: the budget verdict, the charged work and the place
// of a refusal do not depend on the order of the declarations (round 19
// M2), for the reviewer's saved pair, the disjoint ladder across the
// refusal boundary at the real budget, and every shape at budgets within
// 10% of its cost, for whole-file and wire-only permutations.
func TestWireBudgetOrder(t *testing.T) {
	var used [2]int
	var verdict [2]string
	for i, name := range []string{"order-original.ergo", "order-permuted.ergo"} {
		data, err := os.ReadFile("testdata/wireorder/" + name)
		if err != nil {
			t.Fatal(err)
		}
		diags, c := checkLimited(t, string(data), nil)
		used[i], verdict[i] = c.wires.used, fmt.Sprint(len(diags) == 0)
		if p := OrderProblems(string(data), 4); len(p) > 0 {
			t.Errorf("%s: %s", name, p[0])
		}
	}
	if used[0] != used[1] || verdict[0] != verdict[1] {
		t.Errorf("the saved pair: %d steps (accepted %s) and %d steps (accepted %s)", used[0], verdict[0], used[1], verdict[1])
	}
	t.Logf("saved pair: %d steps each, accepted %s", used[0], verdict[0])
	check := func(limit func(int) int) func(string) []Diagnostic {
		return func(s string) []Diagnostic { d, _ := checkLimited(t, s, limit); return d }
	}
	refused := 0
	for n := 16; n <= 96; n += 4 {
		src := wireFamily("disjoint", n)
		if d, _ := checkLimited(t, src, nil); len(d) > 0 {
			refused++
		}
		for _, permute := range []func(string, uint64) (string, func(int) (int, int), func(int) (int, int), bool){PermuteDecls, PermuteWires} {
			if p := orderProblems(src, 3, permute, check(nil)); len(p) > 0 {
				t.Fatalf("disjoint n=%d: %s", n, p[0])
			}
		}
	}
	if refused == 0 || refused == 21 {
		t.Errorf("the ladder from 16 to 96 should cross the refusal boundary; %d of 21 refused", refused)
	}
	for _, shape := range budgetShapes {
		src := wireFamily(shape, 12)
		_, c := checkLimited(t, src, nil)
		cost := c.wires.used
		for _, pct := range []int{90, 95, 99, 100, 101, 105, 110} {
			limit := cost * pct / 100
			if pct == 99 {
				limit = cost - 1
			}
			if pct == 101 {
				limit = cost + 1
			}
			for _, permute := range []func(string, uint64) (string, func(int) (int, int), func(int) (int, int), bool){PermuteDecls, PermuteWires} {
				if p := orderProblems(src, 3, permute, check(func(int) int { return limit })); len(p) > 0 {
					t.Fatalf("%s n=12 at %d%% of %d: %s", shape, pct, cost, p[0])
				}
			}
		}
		for seed := uint64(1); seed <= 3; seed++ {
			permuted, _, _, ok := PermuteDecls(src, seed)
			if !ok {
				t.Fatalf("%s: no permutation", shape)
			}
			if _, pc := checkLimited(t, permuted, nil); pc.wires.used != cost {
				t.Errorf("%s n=12 seed %d: %d steps, the original %d", shape, seed, pc.wires.used, cost)
			}
		}
	}
}

// TestWireBudgetGroups: two declarations of one name (`let _`) form one
// inline wire group, refused or kept as a whole, so their order does not
// choose which of them is refused.
func TestWireBudgetGroups(t *testing.T) {
	src := wireFamily("wide fan-in", 6) +
		"let _ = with wire {\n\tX0,\n\tX1,\n\tX2,\n} { 1 }\nlet _ = with wire {\n\tX3,\n\tX4,\n\tX5,\n} { 2 }\n"
	full, c := checkLimited(t, src, nil)
	if len(full) > 0 {
		t.Fatalf("want acceptance, got %v", full)
	}
	cost := c.wires.used
	for limit := 0; limit < cost; limit++ {
		diags, _ := checkLimited(t, src, func(int) int { return limit })
		if !allBudget(diags) {
			t.Fatalf("at %d: %v", limit, diags)
		}
		check := func(s string) []Diagnostic { d, _ := checkLimited(t, s, func(int) int { return limit }); return d }
		if p := orderProblems(src, 2, PermuteDecls, check); len(p) > 0 {
			t.Fatalf("at %d of %d: %s", limit, cost, p[0])
		}
	}
}
