package check_test

import (
	"fmt"
	"iter"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestMatchArmsLinear counts all work at the source boundary, including
// pattern construction and irrefutable let proofs. The square matrices can
// exhaust their proof budget; they must stop within a constant per source
// cell and never admit an unproved match. Rect keeps 100 rows as width
// grows; wide grows both dimensions. No host-clock assertion is used.
//
// The union shape and each other shape at each size are the cases of a
// split (gate-fast T1): part k checks every seventh case from case k, as a
// top-level test of its own, so the two largest cases (rect at 4000, tupv
// at 320) fall in different parts. Under -short a part keeps its first
// case.
var matchArmsLinear = check.NewSplit(check.Split[matchArmsCase]{
	Name: "TestMatchArmsLinear", Parts: 7, Short: 2,
	Count: 14, Digest: "c9c71172e18f9b697e5f2e7e26b1087a3ab51b0c1bb91f01aceec66a521e354f",
	Cases: check.SeqOf(matchArmsCases),
	Key: func(c matchArmsCase) string {
		if c.src == nil {
			return c.name
		}
		return fmt.Sprintf("%s n=%d\n%s", c.name, c.n, c.src(c.n))
	},
	Run: runMatchArmsLinear,
})

func TestMatchArmsLinearPart0(t *testing.T) { matchArmsLinear.Part(t, 0) }
func TestMatchArmsLinearPart1(t *testing.T) { matchArmsLinear.Part(t, 1) }
func TestMatchArmsLinearPart2(t *testing.T) { matchArmsLinear.Part(t, 2) }
func TestMatchArmsLinearPart3(t *testing.T) { matchArmsLinear.Part(t, 3) }
func TestMatchArmsLinearPart4(t *testing.T) { matchArmsLinear.Part(t, 4) }
func TestMatchArmsLinearPart5(t *testing.T) { matchArmsLinear.Part(t, 5) }
func TestMatchArmsLinearPart6(t *testing.T) { matchArmsLinear.Part(t, 6) }

// matchArmsCase is one shape at one size; the union shape (src nil) is
// one case at its own sizes.
type matchArmsCase struct {
	name string
	n    int
	src  func(int) string
}

func matchArmsCases() []matchArmsCase {
	out := []matchArmsCase{{name: "union"}}
	for _, shape := range []struct {
		name  string
		sizes []int
		src   func(int) string
	}{
		{"rect", []int{1000, 4000}, func(n int) string { return wideBoolMatch(n, 100) }},
		{"wide", []int{80, 320}, func(n int) string { return wideBoolMatch(n, n) }},
		{"tupv", []int{80, 320}, wideTupleValueMatch},
		{"odd", []int{41}, oddCycleSwitch},
		{"let", []int{8000, 32000}, wideLetPattern},
		{"records", []int{100, 400}, nestedRecordPattern},
		{"unions", []int{15, 60}, nestedUnionPatterns},
	} {
		for _, n := range shape.sizes {
			out = append(out, matchArmsCase{shape.name, n, shape.src})
		}
	}
	return out
}

func runMatchArmsLinear(t *testing.T, cases iter.Seq[matchArmsCase]) {
	for shape := range cases {
		if shape.src == nil {
			t.Run(shape.name, testUnionArmsLinear)
			continue
		}
		t.Run(fmt.Sprintf("%s/n=%d", shape.name, shape.n), func(t *testing.T) {
			n := shape.n
			src := shape.src(n)
			file, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("n=%d: parse diagnostics %v", n, err)
			}
			cells := 0
			syntax.Inspect(file, func(syntax.Node) bool { cells++; return true })
			costs, d := check.SolverCosts(src)
			if len(costs) != 1 || costs[0].TotalSteps+costs[0].UsefulSteps == 0 {
				t.Fatalf("n=%d: want one counted proof, got %v; %s", n, costs, describe(d))
			}
			c := costs[0]
			if shape.name == "odd" && c.Written != 2*n*n+3*n {
				t.Fatalf("odd cycle written size: %+v", c)
			}
			for _, x := range d {
				if (shape.name != "wide" && shape.name != "tupv") || x.Code != check.CodeNotTotal || !strings.Contains(x.Message, "too many cases") {
					t.Fatalf("n=%d: unexpected diagnostics %s", n, describe(d))
				}
			}
			work := c.TotalSteps + c.UsefulSteps
			t.Logf("n=%d source cells=%d W=%d steps=%d/%d allocated=%d/%d workLimit=%d cellLimit=%d diagnostics=%s", n, cells, c.Written, c.TotalSteps, c.UsefulSteps, c.TotalCells, c.UsefulCells, c.Budget, c.CellBudget, describe(d))
			if c.Written > cells || c.Budget != max(200000, 512*c.Written) || c.CellBudget != max(200000, 1024*c.Written) || c.TotalCells+c.UsefulCells <= 0 || c.TotalCells+c.UsefulCells > c.CellBudget || work > c.Budget {
				t.Errorf("n=%d: source budget or allocation bound violated: %+v", n, c)
			}
			if work > 512*cells {
				t.Errorf("n=%d: %d steps exceed 512 per source cell (%d)", n, work, 512*cells)
			}
		})
	}
}

// The reviewer's sparse-wide record writes one field per arm, then one
// all-false arm. Unwritten cells must not buy a quadratic proof budget.
func TestSparseMatchResources(t *testing.T) {
	for _, n := range []int{200, 800, 1600, 2400} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			costs, d := check.SolverCosts(sparseFieldMatch(n))
			if len(costs) != 1 {
				t.Fatalf("want one proof, got %v: %s", costs, describe(d))
			}
			c := costs[0]
			// Per arm: record, type, key/value, key, bool = 5 nodes.
			// Last arm: record + type + 3*n nodes; one subject.
			written := 8*n + 3
			bound := max(200000, 512*written)
			if c.Written != written || c.Budget != bound || c.CellBudget != max(200000, 1024*written) || c.TotalSteps+c.UsefulSteps > bound || c.TotalCells+c.UsefulCells > c.CellBudget || c.TotalCells+c.UsefulCells <= 0 {
				t.Fatalf("n=%d: source/steps/allocation bound %d violated: %+v", n, bound, c)
			}
			for _, x := range d {
				if x.Code != check.CodeNotTotal || !strings.Contains(x.Message, "too many cases") {
					t.Fatalf("unexpected diagnostics %s", describe(d))
				}
			}
			t.Logf("n=%d W=%d steps=%d/%d allocated=%d/%d workLimit=%d cellLimit=%d: %s", n, written, c.TotalSteps, c.UsefulSteps, c.TotalCells, c.UsefulCells, bound, c.CellBudget, describe(d))
		})
	}
}

func TestUnwrittenRecordFieldsCostNothing(t *testing.T) {
	var first check.PatternCost
	for _, n := range []int{10, 2400} {
		var b strings.Builder
		b.WriteString("type R { ")
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "F%d bool", i)
		}
		b.WriteString(" }\nfunc F(r R) int { match r { case R{F0: true}: return 1\ncase _: return 0 } }\n")
		costs, d := check.SolverCosts(b.String())
		if len(d) != 0 || len(costs) != 1 || costs[0].Written != 7 {
			t.Fatalf("n=%d: costs %v, diagnostics %s", n, costs, describe(d))
		}
		if n == 10 {
			first = costs[0]
		} else if costs[0] != first {
			t.Fatalf("unwritten fields changed proof cost: %v -> %v", first, costs[0])
		}
		t.Logf("n=%d: %+v", n, costs[0])
	}
}

func wideTupleValueMatch(n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "type T (%s)\nfunc F(t T) int { match t {\n", strings.TrimSuffix(strings.Repeat("bool, ", n), ", "))
	for i := range n {
		b.WriteString("case (")
		for j := range n {
			if j > 0 {
				b.WriteString(", ")
			}
			if i == j {
				b.WriteString("_")
			} else {
				b.WriteString("true")
			}
		}
		fmt.Fprintf(&b, "): return %d\n", i)
	}
	b.WriteString("case _: return -1\n} }\n")
	return b.String()
}

func sparseFieldMatch(n int) string {
	var b strings.Builder
	b.WriteString("type R { ")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "F%d bool", i)
	}
	b.WriteString(" }\nfunc F(r R) int { match r {\n")
	for i := range n {
		fmt.Fprintf(&b, "case R{F%d: true}: return %d\n", i, i)
	}
	b.WriteString("case R{")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "F%d: false", i)
	}
	b.WriteString("}: return -1\n} }\n")
	return b.String()
}

func wideBoolMatch(width, rows int) string {
	var b strings.Builder
	b.WriteString("func F(")
	var subjects []string
	for j := range width {
		if j > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "b%d bool", j)
		subjects = append(subjects, fmt.Sprintf("b%d", j))
	}
	fmt.Fprintf(&b, ") int { match (%s) {\n", strings.Join(subjects, ", "))
	for i := range rows {
		var cells []string
		for j := range width {
			p := "true"
			if j == i {
				p = "_"
			}
			cells = append(cells, p)
		}
		fmt.Fprintf(&b, "case (%s): return %d\n", strings.Join(cells, ", "), i)
	}
	b.WriteString("case _: return -1\n} }\n")
	return b.String()
}

func wideLetPattern(n int) string {
	var names, values []string
	for i := range n {
		names = append(names, fmt.Sprintf("a%d", i))
		values = append(values, fmt.Sprint(i))
	}
	return fmt.Sprintf("func F() { let (%s) = (%s)\n_ = (%s)\n}\n", strings.Join(names, ", "), strings.Join(values, ", "), strings.Join(names, ", "))
}

func nestedRecordPattern(n int) string {
	var b strings.Builder
	for i := range n {
		if i == n-1 {
			fmt.Fprintf(&b, "type R%d { On bool }\n", i)
		} else {
			fmt.Fprintf(&b, "type R%d { Child R%d }\n", i, i+1)
		}
	}
	b.WriteString("func F(r R0) int { match r {\ncase ")
	for i := range n - 1 {
		fmt.Fprintf(&b, "R%d{Child: ", i)
	}
	fmt.Fprintf(&b, "R%d{On: true}%s: return 1\ncase _: return 0\n} }\n", n-1, strings.Repeat("}", n-1))
	return b.String()
}

func nestedUnionPatterns(n int) string {
	var b strings.Builder
	for i := range n {
		if i == n-1 {
			fmt.Fprintf(&b, "type U%d = A%d{} | B%d{}\n", i, i, i)
		} else {
			fmt.Fprintf(&b, "type U%d = A%d{Child U%d} | B%d{}\n", i, i, i+1, i)
		}
	}
	b.WriteString("func F(u U0) int { match u {\n")
	for depth := range n {
		b.WriteString("case ")
		for i := range depth {
			fmt.Fprintf(&b, "A%d{Child: ", i)
		}
		fmt.Fprintf(&b, "B%d%s: return %d\n", depth, strings.Repeat("}", depth), depth)
	}
	b.WriteString("case _: return 0\n} }\n")
	return b.String()
}
