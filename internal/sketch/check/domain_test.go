package check_test

import (
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The round-2 review found the round-1 classes again on inputs the round-2
// tests did not reach (round-2-findings.md, "Test strength"). These tests
// cover each class's rule domain with generators and tables, not points.

// checkWithin checks src and fails if the checker does not finish in d.
func checkWithin(t *testing.T, src string, d time.Duration) (*check.Package, []check.Diagnostic) {
	t.Helper()
	type result struct {
		pkg   *check.Package
		diags []check.Diagnostic
	}
	done := make(chan result, 1)
	go func() {
		pkg, diags := check.CheckSource(src)
		done <- result{pkg, diags}
	}()
	select {
	case r := <-done:
		return r.pkg, r.diags
	case <-time.After(d):
		t.Fatalf("the checker did not finish within %s", d)
		return nil, nil
	}
}

// ---------------------------------------------------------------- D7 rows

// TestRowGraphDomain: random reference graphs over four kinds of body:
// functions and package vars holding function literals (inferred rows),
// self functions (196m: two names over many first-parameter types, called
// as R{}.m(), MkR().m() or through a local bound to MkR(), which each reach
// the one function of R: typing, not the name, picks it (round 6 review
// M1); inferred rows, like any unexported function) and exported functions
// (closed rows). A body refers to another by a call or by a bare reference
// (_ = f; never to a self function, which has no method value), and may
// call C.M itself. The oracle is independent of the checker:
//   - the initialization graph is every reference; a var in a cyclic
//     strongly connected component of it is on an initialization cycle:
//     one S108 per such component (go/types initorder), and the var is
//     invalid, so a call through it charges nothing (D3; round-2 M2);
//   - an inferred row holds C exactly when the body calls C.M or calls an
//     inferred body whose row holds C (least fixpoint);
//   - a closed body gets one S102 per such call and per C.M of its own;
//   - class G: a function referenced by _ = f inside its own component of
//     the body graph has its row pinned empty; when its body still needs
//     C, it gets one S108 at its declaration.
//   - a parameter or a local of a nested literal spelled as another body's
//     name (lexical shadowing) is no reference: no edge.
//
// Nothing else is reported.
func TestRowGraphDomain(t *testing.T) {
	rng := rand.New(rand.NewSource(1963))
	const (
		kFunc = iota
		kVar
		kMethod
		kExported
	)
	cycles, groups, shadowed, hidden := 0, 0, 0, 0
	shadows := rand.New(rand.NewSource(1964))
	// A call of a self function names its receiver one of three ways: a
	// composite literal (its type is written), a call's result, or a
	// local bound to one (typing alone knows their types; round 6 review
	// M1). The callee is exact either way: the function on R's type.
	forms := rand.New(rand.NewSource(1965))
	for trial := 0; trial < 1500; trial++ {
		n := 2 + rng.Intn(9)
		kind := make([]int, n)
		seed := make([]bool, n)
		type edge struct {
			to   int
			call bool
		}
		edges := make([][]edge, n)
		for i := range n {
			switch r := rng.Intn(8); {
			case r < 3:
				kind[i] = kFunc
			case r < 6:
				kind[i] = kVar
			case r < 7:
				kind[i] = kMethod
			default:
				kind[i] = kExported
			}
			seed[i] = rng.Intn(5) == 0
		}
		for i := range n {
			for j := range n {
				if kind[j] == kExported || rng.Intn(4) != 0 {
					continue
				}
				// A method is always called: the decided language has no
				// method values (196m).
				edges[i] = append(edges[i], edge{to: j, call: rng.Intn(4) != 0 || kind[j] == kMethod})
			}
		}
		name := func(j int) string {
			switch kind[j] {
			case kMethod:
				return fmt.Sprintf("R%d.m%d", j, j%2)
			case kExported:
				return fmt.Sprintf("P%d", j)
			}
			return fmt.Sprintf("f%d", j)
		}
		var b strings.Builder
		b.WriteString("type C { M() }\n")
		for i := range n {
			if kind[i] == kMethod {
				fmt.Fprintf(&b, "type R%d {}\n", i)
				fmt.Fprintf(&b, "func MkR%d() R%d { return R%d }\n", i, i, i)
			}
		}
		for i := range n {
			switch kind[i] {
			case kFunc:
				fmt.Fprintf(&b, "func f%d() {\n", i)
			case kVar:
				fmt.Fprintf(&b, "let f%d = func() {\n", i)
			case kMethod:
				fmt.Fprintf(&b, "func m%d(_ R%d) {\n", i%2, i)
			case kExported:
				fmt.Fprintf(&b, "func P%d() {\n", i)
			}
			for k, e := range edges[i] {
				if e.call && kind[e.to] == kMethod && forms.Intn(3) != 0 {
					hidden++
					if forms.Intn(2) == 0 {
						fmt.Fprintf(&b, "\tMkR%d().m%d()\n", e.to, e.to%2)
					} else {
						fmt.Fprintf(&b, "\tlet x%d = MkR%d()\n\tx%d.m%d()\n", k, e.to, k, e.to%2)
					}
				} else if e.call {
					fmt.Fprintf(&b, "\t%s()\n", name(e.to))
				} else {
					fmt.Fprintf(&b, "\t_ = %s\n", name(e.to))
				}
			}
			// Lexical shadowing (round 14 M1): a parameter or a local of a
			// nested literal spelled as another body's name is not a
			// reference to it, so the oracle adds no edge; a renamed control
			// (local) is emitted the same way.
			if r := shadows.Intn(3); r < 2 {
				j := shadows.Intn(n)
				id := "local"
				if kind[j] != kMethod && shadows.Intn(4) != 0 {
					id = name(j)
				}
				if r == 0 {
					fmt.Fprintf(&b, "\tfunc(%s func()) { %s() }(func() {})\n", id, id)
				} else {
					fmt.Fprintf(&b, "\tfunc() { let _ as %s = func() {}; %s() }()\n", id, id)
				}
				shadowed++
			}
			if seed[i] {
				b.WriteString("\tC.M()\n")
			}
			b.WriteString("}\n")
		}
		src := check.Decided(b.String())

		// The initialization graph's components (Tarjan over every edge).
		comp := make([]int, n)
		index := make([]int, n)
		low := make([]int, n)
		on := make([]bool, n)
		for i := range index {
			index[i] = -1
		}
		var stack []int
		next, ncomp := 0, 0
		var visit func(int)
		visit = func(v int) {
			index[v], low[v] = next, next
			next++
			stack = append(stack, v)
			on[v] = true
			for _, e := range edges[v] {
				if index[e.to] < 0 {
					visit(e.to)
					low[v] = min(low[v], low[e.to])
				} else if on[e.to] {
					low[v] = min(low[v], index[e.to])
				}
			}
			if low[v] == index[v] {
				for {
					w := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[w] = false
					comp[w] = ncomp
					if w == v {
						break
					}
				}
				ncomp++
			}
		}
		for i := range n {
			if index[i] < 0 {
				visit(i)
			}
		}
		size := make([]int, ncomp)
		selfLoop := make([]bool, n)
		for i := range n {
			size[comp[i]]++
			for _, e := range edges[i] {
				if e.to == i {
					selfLoop[i] = true
				}
			}
		}
		cyclicComp := func(i int) bool { return size[comp[i]] > 1 || selfLoop[i] }
		poisoned := make([]bool, n)
		withVar := map[int]bool{}
		for i := range n {
			if kind[i] == kVar && cyclicComp(i) {
				poisoned[i] = true
				withVar[comp[i]] = true
			}
		}
		wantS108 := len(withVar)
		if wantS108 > 0 {
			cycles++
		}

		// Class G: the checker's components are those of the body graph,
		// whose edges reach inferred bodies only (exported functions spell
		// their rows). A function used as a value (_ = f)
		// inside its own component has its rows pinned empty; when its body
		// still needs C, that is one S108 at its declaration. A var on
		// an initialization cycle is invalid: no value use follows from its
		// initializer (D3), and a var in a cyclic component is always on one.
		bcomp := bodyComponents(n, func(i int) []int {
			var out []int
			for _, e := range edges[i] {
				if kind[e.to] != kExported {
					out = append(out, e.to)
				}
			}
			return out
		})
		pinned := make([]bool, n)
		for i := range n {
			for _, e := range edges[i] {
				if !e.call && kind[e.to] == kFunc && bcomp[i] == bcomp[e.to] && !poisoned[i] {
					pinned[e.to] = true
				}
			}
		}

		// Inferred rows: least fixpoint over calls to unpoisoned inferred bodies;
		// a pinned row stays empty, and grew records that its body needs C.
		inferred := func(j int) bool { return kind[j] != kExported }
		hasC := make([]bool, n)
		grew := make([]bool, n)
		for changed := true; changed; {
			changed = false
			for i := range n {
				if !inferred(i) || hasC[i] {
					continue
				}
				c := seed[i]
				for _, e := range edges[i] {
					if e.call && inferred(e.to) && !poisoned[e.to] && hasC[e.to] {
						c = true
					}
				}
				switch {
				case c && pinned[i]:
					grew[i] = true
				case c:
					hasC[i], changed = true, true
				}
			}
		}
		wantS102 := 0
		for i := range n {
			if inferred(i) {
				continue
			}
			if seed[i] {
				wantS102++
			}
			for _, e := range edges[i] {
				if e.call && inferred(e.to) && !poisoned[e.to] && hasC[e.to] {
					wantS102++
				}
			}
		}

		wantG := 0
		for i := range n {
			if grew[i] {
				wantG++
			}
		}
		if wantG > 0 {
			groups++
		}

		pkg, diags := checkWithin(t, src, 5*time.Second)
		gotS108, gotS102, gotG := 0, 0, 0
		for _, d := range diags {
			switch {
			case d.Code == "S108" && strings.Contains(d.Message, "initialization cycle"):
				gotS108++
			case d.Code == "S108" && strings.Contains(d.Message, " inside its own recursive group; "):
				gotG++
			case d.Code == "S102":
				gotS102++
			default:
				t.Fatalf("trial %d: unexpected %s\n%s", trial, describe(diags), src)
			}
		}
		if gotS108 != wantS108 || gotS102 != wantS102 || gotG != wantG {
			t.Fatalf("trial %d: want %d initialization cycles, %d S102 and %d value uses in a group, got %s\n%s", trial, wantS108, wantS102, wantG, describe(diags), src)
		}
		if p := check.RefWalkProblems(src); len(p) > 0 {
			t.Fatalf("trial %d: %s\n%s", trial, strings.Join(p, "; "), src)
		}
		for _, f := range pkg.Funcs {
			var i int
			if _, err := fmt.Sscanf(f.Name, "f%d", &i); err != nil {
				continue
			}
			if got := strings.Contains(f.Sig.String(), "main.C"); got != hasC[i] {
				t.Fatalf("trial %d: %s has row %s, want C=%v\n%s", trial, f.Name, f.Sig, hasC[i], src)
			}
		}
	}
	if cycles < 100 {
		t.Fatalf("only %d trials held an initialization cycle; the generator misses the domain", cycles)
	}
	if shadowed < 500 {
		t.Fatalf("only %d shadowing statements; the generator misses lexical binding", shadowed)
	}
	if groups < 100 {
		t.Fatalf("only %d trials held a value use inside its group needing a row; the generator misses class G", groups)
	}
	if hidden < 500 {
		t.Fatalf("only %d calls on a receiver whose type is not written; the generator misses typed self-call edges", hidden)
	}
	t.Logf("%d initialization-cycle trials, %d class G trials, %d shadowing statements, %d calls on a hidden receiver", cycles, groups, shadowed, hidden)
}

// bodyComponents numbers the strongly connected components of the graph
// over 0…n-1 with the given successors (Tarjan), independent of the checker.
func bodyComponents(n int, succ func(int) []int) []int {
	comp := make([]int, n)
	index := make([]int, n)
	low := make([]int, n)
	on := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	next, ncomp := 0, 0
	var visit func(int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range succ(v) {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				comp[w] = ncomp
				if w == v {
					break
				}
			}
			ncomp++
		}
	}
	for i := range n {
		if index[i] < 0 {
			visit(i)
		}
	}
	return comp
}

// expandingCases are generic functions and types whose instantiation grows
// without end: Go rejects each as an instantiation cycle, and the checker
// reports S108 and terminates (round-2 M1; go/types mono.go).
var expandingCases = []struct{ name, src string }{
	{"self-slice", `func f[T](x T) { f([]T{x}) }`},
	{"self-row", `type Fault[T] { Value T }
func Mark() fails Fault[int] { return Fault[int]{1} }
func f[T](x T) {
	try f([]T{x})
	return Fault[T]{x}
}`},
	{"self-row-map", `type Fault[T] { Value T }
func f[T](x T) fails Fault[T] {
	try f(Map[int, T]{0: x})
	return Fault[T]{x}
}`},
	{"mutual", `type Fault[T] { Value T }
func Mark() fails Fault[int] { return Fault[int]{1} }
func f[T](x T) {
	try g([]T{x})
	return Fault[T]{x}
}
func g[T](x T) { try f([]T{x}) }`},
	{"three", `func f[T](x T) { g(x) }
func g[T](x T) { h([]T{x}) }
func h[T](x T) { f(x) }`},
	{"explicit", `func f[T](x T) {
	let y []T
	f[[]T](y)
}`},
	{"through-type", `type Box[T] { V T }
func f[T](x T) { f(Box[T]{x}) }`},
	{"type-field", `type L[T] { next []L[[]T] }`},
	{"type-by-value", `type L[T] { next L[[]T] }`},
	{"type-mutual", `type A[T] { b []B[[]T] }
type B[T] { a []A[T] }`},
}

func TestExpandingInstantiation(t *testing.T) {
	for _, c := range expandingCases {
		t.Run(c.name, func(t *testing.T) {
			_, diags := checkWithin(t, ""+c.src+"\n", 2*time.Second)
			if len(diags) != 1 || diags[0].Code != "S108" || !strings.Contains(diags[0].Message, "instantiation cycle") {
				t.Fatalf("want one instantiation-cycle S108, got %s", describe(diags))
			}
		})
	}
}

// TestFiniteInstantiation: recursion that does not grow is legal, and its
// rows converge (controls for TestExpandingInstantiation).
func TestFiniteInstantiation(t *testing.T) {
	for _, src := range []string{
		`func f[T](x T) { f(x) }`,
		`func f[T](x T) { f([]int{1}) }`,
		`type Fault[T] { Value T }
func f[T](x T) fails Fault[T] {
	try f(x)
	return Fault[T]{x}
}`,
		`func f[T](x T) { g(x) }
func g[T](x T) { f(x) }`,
		`type L[T] { next []L[T] }`,
		`type L[T] { next []L[int] }`,
	} {
		_, diags := checkWithin(t, ""+src+"\n", 2*time.Second)
		if len(diags) != 0 {
			t.Fatalf("%s: want no diagnostic, got %s", src, describe(diags))
		}
	}
}

// ---------------------------------------------------------------- D8/D16 totality

// cycleSwitch is the odd-cycle family with its columns in a given order:
// order[j] is the cycle vertex column j holds. Cases cover equal neighbours
// around the cycle; an odd cycle is total, an even one is not.
func cycleSwitch(n int, order []int) string {
	col := make([]int, n)
	for j, v := range order {
		col[v] = j
	}
	var b strings.Builder
	b.WriteString("type A {}\ntype B {}\ntype U = A | B\nfunc F(")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d U", i)
	}
	b.WriteString(") int {\nmatch (")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d", i)
	}
	b.WriteString(") {\n")
	for v := 0; v < n; v++ {
		for _, variant := range []string{"A", "B"} {
			cells := make([]string, n)
			for j := range cells {
				cells[j] = "_"
			}
			cells[col[v]] = variant
			cells[col[(v+1)%n]] = variant
			b.WriteString("case (" + strings.Join(cells, ",") + "): return 1\n")
		}
	}
	b.WriteString("}\n}\n")
	return check.Decided(b.String())
}

func columnOrders(n int, rng *rand.Rand) map[string][]int {
	identity := make([]int, n)
	reversed := make([]int, n)
	evensFirst := make([]int, 0, n)
	for i := range n {
		identity[i] = i
		reversed[i] = n - 1 - i
	}
	for i := 0; i < n; i += 2 {
		evensFirst = append(evensFirst, i)
	}
	for i := 1; i < n; i += 2 {
		evensFirst = append(evensFirst, i)
	}
	out := map[string][]int{"identity": identity, "reversed": reversed, "evens-first": evensFirst}
	for k := 0; k < 3; k++ {
		out[fmt.Sprintf("random%d", k)] = rng.Perm(n)
	}
	return out
}

// TestTotalityPermutations: the odd-cycle family in every column order is
// proved total or refused for its size (S103 "too many cases"); an even
// cycle is never accepted (round-2 M3; D16 as amended in round 3). The
// bound is the solver's deterministic step count, not the host's clock:
// every column order of one size takes the same steps (the memo ignores
// column order), and 31 subjects stay under an eighth of the budget. With
// ERGO_TIMING=1 the test also holds the wall-clock bound of decision 1,
// under 50 ms at 31 subjects, best of 3, for a receipt on a quiet host.
func TestTotalityPermutations(t *testing.T) {
	timing := os.Getenv("ERGO_TIMING") == "1"
	rng := rand.New(rand.NewSource(1964))
	for _, n := range []int{5, 7, 11, 15, 19, 25, 29, 31} {
		first := -1
		for name, order := range columnOrders(n, rng) {
			src := cycleSwitch(n, order)
			steps, diags := check.TotalitySteps(src)
			if len(steps) != 1 {
				t.Fatalf("n=%d %s: want one switch, got steps %v", n, name, steps)
			}
			switch {
			case len(diags) == 0:
			case len(diags) == 1 && diags[0].Code == "S103" && strings.Contains(diags[0].Message, "too many cases"):
				if n <= 11 {
					t.Errorf("n=%d %s: a small total switch hit the budget", n, name)
				}
			default:
				t.Errorf("n=%d %s: want total or refused, got %s", n, name, describe(diags))
			}
			budget := check.TotalityBudgets(src)[0]
			if steps[0] > budget/8 {
				t.Errorf("n=%d %s: %d solver steps, want at most %d", n, name, steps[0], budget/8)
			}
			if first < 0 {
				first = steps[0]
			} else if steps[0] != first {
				t.Errorf("n=%d %s: %d solver steps, another order took %d: the proof depends on column order", n, name, steps[0], first)
			}
			if timing {
				best := time.Duration(1 << 62)
				for i := 0; i < 3; i++ {
					start := time.Now()
					check.CheckSource(src)
					if e := time.Since(start); e < best {
						best = e
					}
				}
				t.Logf("n=%d %s: best %s, %d steps", n, name, best, steps[0])
				if best > 50*time.Millisecond {
					t.Errorf("n=%d %s: %s, want under 50ms", n, name, best)
				}
			}
			even := cycleSwitch(n+1, append(append([]int(nil), order...), n))
			_, ediags := check.CheckSource(even)
			if len(ediags) == 0 || ediags[0].Code != "S103" {
				t.Errorf("n=%d %s: an even cycle (not total) was accepted: %s", n+1, name, describe(ediags))
			}
		}
	}
}

// cnfSwitch writes a switch over n binary subjects whose cases are m random
// three-constructor rows: a row is the falsifying assignments of a 3-CNF
// clause, so the switch is total exactly when the formula is unsatisfiable.
// It returns the source and the rows.
func cnfSwitch(rng *rand.Rand, n, m int) (string, [][]int) {
	var b strings.Builder
	b.WriteString("type A {}\ntype B {}\ntype U = A | B\nfunc F(")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d U", i)
	}
	b.WriteString(") int {\nmatch (")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "x%d", i)
	}
	b.WriteString(") {\n")
	var rows [][]int
	seen := map[string]bool{}
	for r := 0; r < m; r++ {
		cells := make([]string, n)
		row := make([]int, n)
		for j := range cells {
			cells[j] = "_"
			row[j] = -1
		}
		for _, v := range rng.Perm(n)[:3] {
			row[v] = rng.Intn(2)
			cells[v] = []string{"A", "B"}[row[v]]
		}
		text := strings.Join(cells, ",")
		if seen[text] {
			continue
		}
		seen[text] = true
		rows = append(rows, row)
		b.WriteString("case (" + text + "): return 1\n")
	}
	b.WriteString("}\nreturn 0\n}\n")
	return check.Decided(b.String()), rows
}

// cnfTotal decides by enumeration whether the rows cover every assignment.
func cnfTotal(n int, rows [][]int) bool {
	v := make([]int, n)
	for mask := 0; mask < 1<<n; mask++ {
		for j := range v {
			v[j] = mask >> j & 1
		}
		covered := false
		for _, r := range rows {
			ok := true
			for j, p := range r {
				if p >= 0 && p != v[j] {
					ok = false
					break
				}
			}
			if ok {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// totalityVerdict reads the switch's verdict from the diagnostics: "total",
// "not total" or "refused".
func totalityVerdict(diags []check.Diagnostic) string {
	for _, d := range diags {
		if d.Code != "S103" {
			continue
		}
		if strings.Contains(d.Message, "too many cases") {
			return "refused"
		}
		return "not total"
	}
	return "total"
}

// TestTotalityBudget: random 3-CNF switches, the hard family of totality.
// Over 16 subjects the verdict matches enumeration exactly; over 60
// subjects the solver ends within its budget, refusing some switches with
// "too many cases" rather than accepting them unproven (D16 as amended).
// TestTotalityEmptySubject: a subject whose union lost every variant to a
// reported error has no value, so the switch is vacuously total: one
// diagnostic, the unknown name, in every subject position (the fuzzer's
// round-3 crasher, testdata/fuzz/FuzzCheck/9c69c36df14c1bf1).
func TestTotalityEmptySubject(t *testing.T) {
	for _, sw := range []string{
		`match c {}`,
		`match c { case _: return A }`,
		`match (c, d) {}`,
		`match (d, c) { case (A, _): return A }`,
	} {
		src := "type A {}\ntype B {}\ntype Choice = A00\ntype Full = A | B\nfunc F(c Choice, d Full) A {" + sw + "}\n"
		_, diags := checkWithin(t, check.Decided(src), 2*time.Second)
		if len(diags) != 1 || diags[0].Code != "S104" {
			t.Errorf("%s: want only the unknown-name S104, got %s", sw, describe(diags))
		}
	}
}

func TestTotalityBudget(t *testing.T) {
	rng := rand.New(rand.NewSource(1965))
	for k := 0; k < 12; k++ {
		src, rows := cnfSwitch(rng, 16, 16*43/10)
		_, diags := checkWithin(t, src, 5*time.Second)
		got := totalityVerdict(diags)
		want := "not total"
		if cnfTotal(16, rows) {
			want = "total"
		}
		if got != want {
			costs, _ := check.SolverCosts(src)
			t.Fatalf("16 subjects, instance %d: verdict %s, enumeration says %s; costs %+v\n%s", k, got, want, costs, src)
		}
	}
	refused := 0
	for k := 0; k < 4; k++ {
		src, _ := cnfSwitch(rng, 60, 60*43/10)
		steps, diags := check.TotalitySteps(src)
		budgets := check.TotalityBudgets(src)
		// One charge may pass the budget by at most one matrix scan.
		if len(steps) != 1 || len(budgets) != 1 || steps[0] > budgets[0]+60*43/10*61 {
			t.Errorf("60 subjects, instance %d: solver steps %v, want at most the budget %v and one matrix", k, steps, budgets)
		}
		if os.Getenv("ERGO_TIMING") == "1" {
			start := time.Now()
			check.CheckSource(src)
			if e := time.Since(start); e > 2*time.Second {
				t.Errorf("60 subjects, instance %d: %s, want under 2s", k, e)
			}
		}
		if totalityVerdict(diags) == "refused" {
			refused++
		}
	}
	if refused == 0 {
		t.Errorf("no 60-subject 3-CNF switch reached the budget; the refusal path is untested")
	}
}

// ---------------------------------------------------------------- D6 kinds and constraints

var kindCases = []struct{ name, want, src string }{
	{"self-recursive-row", "", `type List[T, E] { Value T, Tail []List[T, E], Run func() fails E }`},
	{"self-recursive-row-first", "", `type List[E, T] { Tail []List[E, T], Value T, Run func() fails E }`},
	// E first, its field last: each kind is solved by position.
	{"self-recursive-row-first-value", "", `type List[E, T] { Value T, Tail []List[E, T], Run func() fails E }`},
	{"mutually-recursive-row", "", `type Box[T, E] { Next []Other[T, E], Run func() fails E }
type Other[T, E] { Next []Box[T, E] }`},
	{"mutual-row-declared-later", "", `type Other[T, E] { Next []Box[T, E] }
type Box[T, E] { Next []Other[T, E], Run func() fails E }`},
	{"three-cycle-row", "", `type A[E] { b []B[E] }
type B[E] { c []C[E] }
type C[E] { a []A[E], run func() fails E }`},
	{"recursive-capability-row", "", `type Svc { Ping() }
type Job[R] { next []Job[R], run func() needs R }
func Run(j Job[Svc]) {}`},
	{"recursive-conflict", "S108", `type L[E] { next []L[E], run func() needs E, v E }`},
	{"function-of-recursive", "", `type List[T, E] { Tail []List[T, E], Run func() fails E }
func Head[T, E](l List[T, E]) fails E { return l.Run() }`},
	{"dependent-constraint-literal", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
type Box[T comparable, V I[T]] { X T, Y V }
func F() { _ = Box{1, S} }`},
	{"dependent-constraint-explicit", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
type Box[T comparable, V I[T]] { X T, Y V }
func F() { _ = Box[int, S]{1, S} }`},
	// T is bound only by a literal and V's constraint mentions it: the
	// literal's default binds T (196u rule 6), and S satisfies I[int]; a
	// string literal binds T to string, which S does not satisfy. T is
	// comparable, not any, which the decided language does not spell
	// before a constrained parameter (#196).
	{"dependent-constraint-call", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
func Use[T comparable, V I[T]](x T, v V) {}
func F() { Use(1, S) }`},
	{"dependent-constraint-call-typed", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
func Use[T comparable, V I[T]](x T, v V) {}
func F(n int) { Use(n, S) }`},
	{"dependent-constraint-call-explicit", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
func Use[T comparable, V I[T]](x T, v V) {}
func F() { Use[int](1, S) }`},
	{"dependent-constraint-value", "", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
func Use[T comparable, V I[T]](x T, v V) {}
func F() func(int, S) { return Use[int, S] }`},
	{"dependent-constraint-unsatisfied", "S108", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
type Box[T comparable, V I[T]] { X T, Y V }
func F() { _ = Box{"s", S} }`},
	{"dependent-constraint-call-unsatisfied", "S108", `type I[T] { M(T) }
type S {}
func M(_ S, x int) {}
func Use[T comparable, V I[T]](x T, v V) {}
func F() { Use("s", S) }`},
}

func TestKindAndConstraintDomain(t *testing.T) {
	for _, c := range kindCases {
		t.Run(c.name, func(t *testing.T) {
			_, diags := check.CheckSource("" + c.src + "\n")
			if got := codesOf(diags); got != c.want {
				t.Fatalf("want %q, got %s", c.want, describe(diags))
			}
		})
	}
}

// ---------------------------------------------------------------- D2/D5

// The chains are written in the decided syntax (#196).
func structChain(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "type T%d { Next T%d }\n", i, i+1)
	}
	fmt.Fprintf(&b, "type T%d {}\n", n)
	return b.String()
}

func genericChain(n int) string {
	var b strings.Builder
	b.WriteString("type W[T] { V T }\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "type T%d { Next W[T%d] }\n", i, i+1)
	}
	fmt.Fprintf(&b, "type T%d {}\n", n)
	return b.String()
}

// TestFiniteChains: deep finite type chains are legal at any depth, and
// closing the chain is S108 once (round-2 M5: no depth cutoff). The
// array chain is gone with fixed arrays (#196).
func TestFiniteChains(t *testing.T) {
	for _, gen := range []struct {
		name string
		f    func(int) string
	}{{"struct", structChain}, {"generic", genericChain}} {
		for _, n := range []int{1, 10, 300, 2000} {
			src := gen.f(n)
			_, diags := checkWithin(t, src, 10*time.Second)
			if len(diags) != 0 {
				t.Fatalf("%s n=%d: want no diagnostic, got %s", gen.name, n, describe(diags))
			}
			closed := strings.Replace(src, fmt.Sprintf("type T%d {}", n), fmt.Sprintf("type T%d { Back T0 }", n), 1)
			_, diags = checkWithin(t, closed, 10*time.Second)
			if codesOf(diags) != "S108" {
				t.Fatalf("%s n=%d closed: want one S108, got %s", gen.name, n, describe(diags))
			}
		}
	}
}

var valueCases = []struct{ name, want, src string }{
	// Map keys compare by value. (The cases of interface keys, which
	// compare by dynamic type, went with the any type, #196.)
	{"int64-keys-equal", "S108", `func F() Map[int64, int] { return Map[int64, int]{1: 1, int64(1): 2} }`},
	// Typed integer constants index when representable as int.
	{"typed-index-int64", "", `func F(xs []int) int { return xs[int64(0)] }`},
	{"typed-index-uint8", "", `func F(xs []int) int { return xs[uint8(1)] }`},
	{"typed-index-negative", "S108", `func F(xs []int) int { return xs[int64(-1)] }`},
	{"typed-index-too-large", "S108", `func F(xs []int) int { return xs[uint64(18446744073709551615)] }`},
	{"typed-slice-bound", "", `func F(xs []int) []int { return xs[int32(1):uint(2)] }`},
	{"string-slice-implicit-high", "S108", `func F() string { return "ab"[3:] }`},
}

func TestValueDomain(t *testing.T) {
	for _, c := range valueCases {
		t.Run(c.name, func(t *testing.T) {
			_, diags := check.CheckSource("" + c.src + "\n")
			if got := codesOf(diags); got != c.want {
				t.Fatalf("want %q, got %s", c.want, describe(diags))
			}
		})
	}
}

// ---------------------------------------------------------------- D1 Info.Types

// TestInstantiatedInfo: no expression of a non-generic function has a
// recorded type with an unbound type parameter; an instantiated function
// value records its instantiated type, wrappers included (round-2 M8).
func TestInstantiatedInfo(t *testing.T) {
	for _, src := range []string{
		`func Id[T](x T) T { return x }
func F() func(int) int { return Id[int] }`,
		`func Id[T](x T) T { return x }
func F() func(int) int { return (Id[int]) }`,
		`func Id[T](x T) T { return x }
func Apply(f func(int) int) int { return f(1) }
func F() int { return Apply(Id[int]) + Apply((Id[int])) }`,
		`func Id[T](x T) T { return x }
func F() int {
	let f = Id[int]
	let g = (Id[string])
	_ = g
	return f(1)
}`,
		`func Pair[A, B](a A, b B) A { return a }
func F() func(int, string) int {
	let f func(int, string) int = Pair[int, string]
	return f
}`,
	} {
		full := "" + src + "\n"
		pkg, diags := check.CheckSource(full)
		if len(diags) != 0 {
			t.Fatalf("%s: %s", src, describe(diags))
		}
		start := strings.Index(full, "func F()")
		found := false
		for x, ty := range pkg.Info.Types {
			if int(x.Pos()) < start {
				continue
			}
			if sig, ok := ty.(*check.Signature); ok && len(sig.TParams) > 0 {
				if _, isName := x.(*syntax.Ident); isName {
					continue // a generic function's name has its generic type, as in go/types
				}
			}
			if hasTypeParam(ty) {
				t.Errorf("%s: %s at %d records %s", src, exprKind(x), x.Pos(), ty)
			}
			if _, ok := x.(*syntax.IndexExpr); ok {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no type recorded for the instantiation", src)
		}
	}
}

func exprKind(x syntax.Expr) string { return fmt.Sprintf("%T", x) }

func hasTypeParam(t check.Type) bool {
	switch t := t.(type) {
	case *check.TypeParam:
		return true
	case *check.Signature:
		if len(t.TParams) > 0 {
			return true
		}
		for _, p := range t.Params {
			if hasTypeParam(p.Type) {
				return true
			}
		}
		for _, r := range t.Results {
			if hasTypeParam(r) {
				return true
			}
		}
	case *check.Slice:
		return hasTypeParam(t.Elem)
	case *check.Named:
		for _, a := range t.TArgs {
			if hasTypeParam(a) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- D3 let targets

// TestLetTargetsDomain: every sequence of up to three `let` declarations in
// one block over the names x, y and the blank gives one S108 for each
// declaration of a name already declared in the block, and no S110 follows
// from it: each name is read once after the declarations (round-2 M9).
// Round 2 covered the left sides of :=, which the decided language removed
// (#196); a binding of several names is a tuple's (196t).
func TestLetTargetsDomain(t *testing.T) {
	atoms := []string{"x", "y", "_"}
	var lists [][]string
	var build func(prefix []string)
	build = func(prefix []string) {
		if len(prefix) > 0 {
			lists = append(lists, append([]string(nil), prefix...))
		}
		if len(prefix) == 3 {
			return
		}
		for _, a := range atoms {
			build(append(prefix, a))
		}
	}
	build(nil)
	for _, names := range lists {
		var b strings.Builder
		b.WriteString("func F() {\n")
		seen := map[string]bool{}
		var want []string
		for i, a := range names {
			fmt.Fprintf(&b, "\tlet %s = %d\n", a, i+1)
			if a == "_" {
				continue
			}
			if seen[a] {
				want = append(want, "S108")
			}
			seen[a] = true
		}
		for _, a := range []string{"x", "y"} {
			if seen[a] {
				fmt.Fprintf(&b, "\t_ = %s\n", a)
			}
		}
		b.WriteString("}\n")
		src := b.String()
		_, diags := check.CheckSource(src)
		if got := codesOf(diags); got != strings.Join(want, " ") {
			t.Errorf("%s: want %q, got %s", src, strings.Join(want, " "), describe(diags))
		}
	}
}
