package check

import (
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// An opCount counts the context pass's data operations through contextOp, apart
// from the meter: each operation where it touches the data, by class
// (repeatable or single-pass) and by kind, per meter and per root meter
// (an allowance's operations count toward its parent too). A repeatable
// operation done while its meter had charged less is recorded in early
// (round 20: the two-part bound).
type opCount struct {
	mu             sync.Mutex
	repeat, single map[*contextMeter]int
	kinds          map[string]int
	early          []string
}

func countOps(t *testing.T) *opCount {
	t.Helper()
	oc := &opCount{repeat: map[*contextMeter]int{}, single: map[*contextMeter]int{}, kinds: map[string]int{}}
	contextOp = func(m *contextMeter, kind string, repeat bool, n int) {
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
	t.Cleanup(func() { contextOp = nil })
	return oc
}

// contextNodes counts the context source of src, independently of contextSource:
// every context declaration and literal, every entry, for an entry that is
// not an embedding one more and one per key, every name of a needs row,
// and one.
func contextNodes(t *testing.T, src string) int {
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
// single-pass operations at most contextSinglePerUnit per context node; so all
// its operations at most the charge plus that; and every body finished
// at most once.
func (oc *opCount) bounded(t *testing.T, what, src string, c *checker) {
	t.Helper()
	m := c.contexts
	nodes := contextNodes(t, src)
	if c.contextLimit == nil && m.limit != contextBudgetPerUnit*nodes {
		t.Errorf("%s: budget %d, want %d × %d context nodes", what, m.limit, contextBudgetPerUnit, nodes)
	}
	if oc.repeat[m] > m.used || len(oc.early) > 0 {
		t.Errorf("%s: %d repeatable operations, %d charged (%v)", what, oc.repeat[m], m.used, oc.early)
	}
	if oc.single[m] > contextSinglePerUnit*nodes {
		t.Errorf("%s: %d single-pass operations for %d context nodes, more than %d each", what, oc.single[m], nodes, contextSinglePerUnit)
	}
	if oc.repeat[m]+oc.single[m] > m.used+contextSinglePerUnit*nodes {
		t.Errorf("%s: %d operations, more than %d charged and %d × %d nodes", what, oc.repeat[m]+oc.single[m], m.used, contextSinglePerUnit, nodes)
	}
	for _, s := range c.contextBodies {
		if s.finishes > 1 {
			t.Errorf("%s: a body finished %d times", what, s.finishes)
		}
	}
}

// checkLimited checks src as one package, with its context budget replaced by
// limit when limit is not nil.
func checkLimited(t *testing.T, src string, limit func(int) int) ([]Diagnostic, *checker) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.contextLimit = limit
	c.run([]*syntax.File{file})
	return c.sorted(), c
}

func allBudget(diags []Diagnostic) bool {
	for _, d := range diags {
		if d.Code != CodeDuplicateCap || d.Message != contextBudgetMessage {
			return false
		}
	}
	return len(diags) > 0
}

var budgetShapes = []string{"entry chain", "wide fan-in", "large-base chain", "replace diamond", "disjoint", "empty independent", "inline fan-in", "multi-key", "inline embeddings"}

// TestContextBudgetPrefixes: for every shape, at every budget from zero to
// the work the full check charges, the two-part bound holds (bounded),
// the meter stays within its budget, and the verdict is the full check's
// at its exact cost and only budget refusals below it: every prefix of the
// work that ends at a refusal, a zero and a small budget among them.
func TestContextBudgetPrefixes(t *testing.T) {
	oc := countOps(t)
	for _, shape := range budgetShapes {
		src := contextFamily(shape, 4)
		full, c := checkLimited(t, src, nil)
		cost := c.contexts.used
		oc.bounded(t, shape, src, c)
		for limit := 0; limit <= cost; limit++ {
			diags, c := checkLimited(t, src, func(int) int { return limit })
			if c.contexts.used > limit {
				t.Fatalf("%s at %d: %d charged", shape, limit, c.contexts.used)
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

// TestContextSinglePass: single-pass work stays within contextSinglePerUnit per
// context node on every shape at sizes 1 to 8000, at the real budget, and the
// repeatable work within the charge.
func TestContextSinglePass(t *testing.T) {
	oc := countOps(t)
	worst, worstWhat := 0.0, ""
	for _, shape := range budgetShapes {
		for _, n := range []int{1, 2, 7, 64, 1000} {
			src := contextFamily(shape, n)
			_, c := checkLimited(t, src, nil)
			what := fmt.Sprintf("%s n=%d", shape, n)
			oc.bounded(t, what, src, c)
			if r := float64(oc.single[c.contexts]) / float64(contextNodes(t, src)); r > worst {
				worst, worstWhat = r, what
			}
		}
	}
	t.Logf("the most single-pass operations per context node: %.2f (%s), bound %d", worst, worstWhat, contextSinglePerUnit)
}

// TestContextContractMultiKey (the round-20 reviewer's contract, under the
// two-part bound): one entry of 1000 and of 8000 keys, at a budget of 3
// and at the real one, reads its keys once each, uncopied, and charges
// every set node before it reads it.
func TestContextContractMultiKey(t *testing.T) {
	oc := countOps(t)
	for _, n := range []int{1000, 8000} {
		src := contextFamily("multi-key", n)
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

// TestContextContractInline (the round-20 reviewer's contract, under the
// two-part bound): an inline context of 1 to 8000 empty embeddings and no
// entry reads each embedding a fixed number of times, finishes once, and
// its finish does no repeatable work past its allowance.
func TestContextContractInline(t *testing.T) {
	oc := countOps(t)
	for _, n := range []int{1, 4, 1000, 8000} {
		src := contextFamily("inline embeddings", n)
		diags, c := checkLimited(t, src, nil)
		if len(diags) > 0 {
			t.Fatalf("n=%d: %v", n, diags)
		}
		oc.bounded(t, fmt.Sprintf("inline embeddings n=%d", n), src, c)
		finished := 0
		for _, s := range c.contextBodies {
			finished += s.finishes
		}
		if finished != 2 { // the named Empty and the inline context
			t.Errorf("n=%d: %d bodies finished, want 2", n, finished)
		}
		for m, r := range oc.repeat {
			if m.parent == c.contexts && r > m.used {
				t.Errorf("n=%d: an allowance of %d did %d repeatable operations", n, m.limit, r)
			}
		}
	}
	if strings.Count(contextFamily("inline embeddings", 3), "\t\tEmpty,\n") != 3 {
		t.Fatal("the family does not write one embedding per line")
	}
}

// budgetOrderCase is one case of TestContextBudgetOrder: the saved pair,
// one rung n of the disjoint ladder, one shape at one budget (pct of its
// cost), or one shape's seeded permutations.
type budgetOrderCase struct {
	kind  string // "pair", "disjoint", "budget" or "seeds"
	n     int
	shape string
	pct   int
}

// budgetOrderLadder is the disjoint ladder of TestContextBudgetOrder: it
// crosses the refusal boundary at the real budget.
func budgetOrderLadder() []int {
	var out []int
	for n := 16; n <= 96; n += 4 {
		out = append(out, n)
	}
	return out
}

// budgetOrderCases is the domain of TestContextBudgetOrder at 0a8e52fd,
// in its order: the saved pair, the ladder, then for each shape its
// budgets and its seeds.
func budgetOrderCases() []budgetOrderCase {
	out := []budgetOrderCase{{kind: "pair"}}
	for _, n := range budgetOrderLadder() {
		out = append(out, budgetOrderCase{kind: "disjoint", n: n})
	}
	for _, shape := range budgetShapes {
		for _, pct := range []int{90, 95, 99, 100, 101, 105, 110} {
			out = append(out, budgetOrderCase{kind: "budget", n: 12, shape: shape, pct: pct})
		}
		out = append(out, budgetOrderCase{kind: "seeds", n: 12, shape: shape})
	}
	return out
}

// TestContextBudgetOrder: the budget verdict, the charged work and the place
// of a refusal do not depend on the order of the declarations (round 19
// M2), for the reviewer's saved pair, the disjoint ladder across the
// refusal boundary at the real budget, and every shape at budgets within
// 10% of its cost, for whole-file and context-only permutations. The
// cases took 6 to 12.2 s of one test under load (check-split round 1), so
// they are a Split (check-split-2, batch 9) with the domain of the unsplit
// test at 0a8e52fd; the check that the ladder crosses the refusal
// boundary reads every rung, so it is a test of its own
// (TestContextBudgetOrderLadder). The parts run in the full tier only
// (Short 0), as the unsplit test did (it was over the size cutoff).
var contextBudgetOrder = NewSplit(Split[budgetOrderCase]{
	Name: "TestContextBudgetOrder", Parts: 3,
	Count: 94, Digest: "9319c4ad3e9b94686dc7823aee65790a36230f75d534074074d51f157cc41e46",
	Cases: SeqOf(budgetOrderCases),
	Key: func(c budgetOrderCase) string {
		return fmt.Sprintf("%s n=%d shape=%q pct=%d", c.kind, c.n, c.shape, c.pct)
	},
	Run: func(t *testing.T, cases iter.Seq[budgetOrderCase]) {
		for c := range cases {
			budgetOrder(t, c)
		}
	},
})

func TestContextBudgetOrderPart0(t *testing.T) { contextBudgetOrder.Part(t, 0) }
func TestContextBudgetOrderPart1(t *testing.T) { contextBudgetOrder.Part(t, 1) }
func TestContextBudgetOrderPart2(t *testing.T) { contextBudgetOrder.Part(t, 2) }

// budgetPermutations are the two permutations TestContextBudgetOrder
// checks: of the whole file and of the contexts only.
var budgetPermutations = []func(string, uint64) (string, func(int) (int, int), func(int) (int, int), bool){PermuteDecls, PermuteContexts}

// budgetOrder checks one case of TestContextBudgetOrder.
func budgetOrder(t *testing.T, c budgetOrderCase) {
	t.Helper()
	check := func(limit func(int) int) func(string) []Diagnostic {
		return func(s string) []Diagnostic { d, _ := checkLimited(t, s, limit); return d }
	}
	switch c.kind {
	case "pair":
		var used [2]int
		var verdict [2]string
		for i, name := range []string{"order-original.ergo", "order-permuted.ergo"} {
			data, err := os.ReadFile("testdata/contextorder/" + name)
			if err != nil {
				t.Fatal(err)
			}
			diags, c := checkLimited(t, string(data), nil)
			used[i], verdict[i] = c.contexts.used, fmt.Sprint(len(diags) == 0)
			if p := OrderProblems(string(data), 4); len(p) > 0 {
				t.Errorf("%s: %s", name, p[0])
			}
		}
		if used[0] != used[1] || verdict[0] != verdict[1] {
			t.Errorf("the saved pair: %d steps (accepted %s) and %d steps (accepted %s)", used[0], verdict[0], used[1], verdict[1])
		}
		t.Logf("saved pair: %d steps each, accepted %s", used[0], verdict[0])
	case "disjoint":
		src := contextFamily("disjoint", c.n)
		for _, permute := range budgetPermutations {
			if p := orderProblems(src, 3, permute, check(nil)); len(p) > 0 {
				t.Fatalf("disjoint n=%d: %s", c.n, p[0])
			}
		}
	case "budget":
		src := contextFamily(c.shape, c.n)
		_, checked := checkLimited(t, src, nil)
		cost := checked.contexts.used
		limit := cost * c.pct / 100
		if c.pct == 99 {
			limit = cost - 1
		}
		if c.pct == 101 {
			limit = cost + 1
		}
		for _, permute := range budgetPermutations {
			if p := orderProblems(src, 3, permute, check(func(int) int { return limit })); len(p) > 0 {
				t.Fatalf("%s n=%d at %d%% of %d: %s", c.shape, c.n, c.pct, cost, p[0])
			}
		}
	case "seeds":
		src := contextFamily(c.shape, c.n)
		_, checked := checkLimited(t, src, nil)
		cost := checked.contexts.used
		for seed := uint64(1); seed <= 3; seed++ {
			permuted, _, _, ok := PermuteDecls(src, seed)
			if !ok {
				t.Fatalf("%s: no permutation", c.shape)
			}
			if _, pc := checkLimited(t, permuted, nil); pc.contexts.used != cost {
				t.Errorf("%s n=%d seed %d: %d steps, the original %d", c.shape, c.n, seed, pc.contexts.used, cost)
			}
		}
	default:
		t.Fatalf("budgetOrder: no case kind %q", c.kind)
	}
}

// TestContextBudgetOrderLadder: the disjoint ladder of
// TestContextBudgetOrder crosses the refusal boundary at the real budget,
// so its rungs check the order of both verdicts.
func TestContextBudgetOrderLadder(t *testing.T) {
	refused, ladder := 0, budgetOrderLadder()
	for _, n := range ladder {
		if d, _ := checkLimited(t, contextFamily("disjoint", n), nil); len(d) > 0 {
			refused++
		}
	}
	if refused == 0 || refused == len(ladder) {
		t.Errorf("the ladder from 16 to 96 should cross the refusal boundary; %d of %d refused", refused, len(ladder))
	}
}

// TestContextBudgetGroups: two declarations of one name (`let _`) form one
// inline context group, refused or kept as a whole, so their order does not
// choose which of them is refused.
func TestContextBudgetGroups(t *testing.T) {
	src := contextFamily("wide fan-in", 6) +
		"let _ = with context {\n\tX0,\n\tX1,\n\tX2,\n} { 1 }\nlet _ = with context {\n\tX3,\n\tX4,\n\tX5,\n} { 2 }\n"
	full, c := checkLimited(t, src, nil)
	if len(full) > 0 {
		t.Fatalf("want acceptance, got %v", full)
	}
	cost := c.contexts.used
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
