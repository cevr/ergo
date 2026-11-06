package check

import (
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Linear work of the body checks (round 8 review M1). Every check of a
// body costs its size: c.workNodes adds the body's syntax nodes at each
// check, in every pass (the typing from empty rows, settling, the
// initialization discovery, the silent pass of a cycle, converge and the
// reporting pass). The tests bound that work by workFactor times the
// source's syntax nodes, and the worklist's graph steps by workFactor times
// (bodies + edges), from n = 250 to n = 4000. They never bound the time;
// they log it.

const workFactor = 8

// chainSource is the reviewer's star: f0 calls f1…f(n-1) and uses C, and
// each fi calls f(i-1), so every fi is in one component whose capability
// row grows one body at a time.
func chainSource(b *strings.Builder, n int) {
	b.WriteString("type C { Use() }\nfunc main() {}\n")
	chainBodies(b, n, nil)
}

// chainBodies writes the star's bodies, f0 first or in the order given.
func chainBodies(b *strings.Builder, n int, order []int) {
	if order == nil {
		order = make([]int, n)
		for i := range order {
			order[i] = i
		}
	}
	for _, i := range order {
		if i == 0 {
			b.WriteString("func f0() {\n\tC.Use()\n")
			for j := 1; j < n; j++ {
				fmt.Fprintf(b, "\tf%d()\n", j)
			}
			b.WriteString("}\n")
			continue
		}
		fmt.Fprintf(b, "func f%d() { f%d() }\n", i, i-1)
	}
}

// rowTail is the reviewer's row-dependent receiver: m fails E (inferred),
// so a catch of E around a call of a function whose row is inferred types
// its receiver.
const rowTail = "type E {}\ntype B {}\nfunc load() B fails E { return B }\nfunc w() B { return try load() }\nfunc m(b B) { _ = try load() }\n"

// kchain: k0 fails E (inferred), and each ki catches E around k(i-1)
// and calls b.m() on the value, so ki's row is inferred too.
func kchain(b *strings.Builder, n int) {
	b.WriteString(rowTail + "func main() {}\nfunc k0() B { return try load() }\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(b, "func k%d() B {\n\tlet b = k%d() catch {\n\tcase E:\n\t\tB\n\t}\n\ttry b.m()\n\treturn b\n}\n", i, i-1)
	}
}

type workCase struct {
	name string
	src  func(n int) string
	// diags is the verdict at every n: the codes, in order; codes, when
	// set, gives it for each n instead.
	diags string
	codes func(n int) string
}

var workCases = []workCase{
	{"r7: an unknown receiver beside the star", func(n int) string {
		var b strings.Builder
		chainSource(&b, n)
		b.WriteString("type R {}\nfunc m(_ R) {}\nfunc bad() { nope.m() }\n")
		return b.String()
	}, "S104", nil},
	{"star: a local bound to an unknown name in a caller of the star", func(n int) string {
		var b strings.Builder
		chainSource(&b, n)
		b.WriteString("type R {}\nfunc m(_ R) {}\nfunc bad() { f0(); let x = nope; x.m() }\n")
		return b.String()
	}, "S104", nil},
	{"star: a receiver that waits for a row in a caller of the star", func(n int) string {
		var b strings.Builder
		chainSource(&b, n)
		b.WriteString(rowTail + `func k() {
	f0()
	let b = w() catch {
	case E:
		B
	}
	try b.m()
}
`)
		return b.String()
	}, "", nil},
	{"star: declared from the middle", func(n int) string {
		var b strings.Builder
		b.WriteString("type C { Use() }\nfunc main() {}\n")
		var order []int
		for i := n / 2; i < n; i++ {
			order = append(order, i)
		}
		for i := 0; i < n/2; i++ {
			order = append(order, i)
		}
		chainBodies(&b, n, order)
		return b.String()
	}, "", nil},
	{"star: a heavier body declared first reaches the middle", func(n int) string {
		var b strings.Builder
		b.WriteString("type C { Use() }\nfunc main() {}\n")
		fmt.Fprintf(&b, "func h() {\n\tf%d()\n", n/2)
		for i := 0; i <= n; i++ {
			fmt.Fprintf(&b, "\tC.Use()\n")
		}
		b.WriteString("}\n")
		chainBodies(&b, n, nil)
		fmt.Fprintf(&b, "func g() { h() }\n")
		return b.String()
	}, "", nil},
	{"r7 generator: receivers through locals bound to calls", func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "type R%d {}\nfunc mk%d() R%d { return R%d }\nfunc m(_ R%d) {}\nfunc f%d() {\n\tlet x = mk%d()\n\tx.m()\n}\n", i, i, i, i, i, i, i)
		}
		b.WriteString("func main() {}\n")
		return b.String()
	}, "", nil},
	{"wideWaiting: one wide body over receivers that wait in waves", func(n int) string {
		var b strings.Builder
		kchain(&b, n)
		b.WriteString("func wide() {\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "\tlet b%d = k%d() catch {\n\tcase E:\n\t\tB\n\t}\n\ttry b%d.m()\n", i, i, i)
		}
		b.WriteString("}\n")
		return b.String()
	}, "", nil},
	{"one body whose receivers each wait for the previous call's row", func(n int) string {
		var b strings.Builder
		b.WriteString("type E {}\ntype B {}\nfunc load() B fails E { return B }\nfunc main() {}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "func m%d(b B) B { return try load() }\n", i)
		}
		b.WriteString("func deep() {\n\tlet c0 = load() catch {\n\tcase E:\n\t\tB\n\t}\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "\tlet c%d = c%d.m%d() catch {\n\tcase E:\n\t\tB\n\t}\n", i, i-1, i)
		}
		fmt.Fprintf(&b, "\t_ = c%d\n}\n", n-1)
		return b.String()
	}, "", nil},
	// The round 13 review's M2 witness (gen-taint2.ts): the b4 component
	// with its one arm error, n readers of walk, and one literal that
	// calls every reader and is called n times. Each `try` there reads
	// walk's pinned row, which is empty: 3n follow-ons that base17 gives
	// too (round 13 ruling), with linear work. From stage D1 round 4 a
	// reader's `try walk()` reads the refused member, whose row holds
	// invalid (rows.md, Inference 6): every f%d and lit can fail with an
	// unknown row, so no "try on an expression that cannot fail" holds,
	// and the arm's own error is the only diagnostic, still with linear
	// work.
	{"M2 witness: one arm error read by n readers through one literal", func(n int) string {
		var b strings.Builder
		b.WriteString("type E {}\ntype G[T] {}\ntype H[T] {}\n")
		b.WriteString("func load() fails G[int] { return G[int] }\nfunc boom() fails E { return E }\n")
		b.WriteString("func keep[T, F](g H[T], f F) {}\n")
		b.WriteString("func walk() {\n\ttry boom()\n\ttry w() catch {\n\tcase G[int]:\n\tcase H as h:\n\t\th.keep(func() { walk() })\n\t}\n}\n")
		b.WriteString("func w() {\n\ttry load()\n\ttry walk()\n}\n")
		for i := range n {
			fmt.Fprintf(&b, "func f%d() { try walk() }\n", i)
		}
		b.WriteString("func outer() {\n\tlet lit = func() {\n")
		for i := range n {
			fmt.Fprintf(&b, "\t\ttry f%d()\n", i)
		}
		b.WriteString("\t}\n")
		for range n {
			b.WriteString("\ttry lit()\n")
		}
		b.WriteString("}\nfunc main() {}\n")
		return b.String()
	}, "S108", nil},
}

// checkSettling is checkFor with the typing from empty rows skipped when
// settleOnly is set, so the worklist alone resolves the calls.
func checkSettling(src string, settleOnly bool) (*checker, []Diagnostic) {
	file, err := syntax.Parse(src)
	if err != nil {
		return nil, []Diagnostic{{Code: CodeSyntax, Message: err.Error()}}
	}
	c := newChecker("main", Std, prelude().scope)
	c.settleOnly = settleOnly
	c.run([]*syntax.File{file})
	return c, c.sorted()
}

// sourceNodes counts the syntax nodes of src.
func sourceNodes(t *testing.T, src string) int {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	syntax.Inspect(file, func(syntax.Node) bool {
		count++
		return true
	})
	return count
}

// selfCallCase is one work case in one mode at one size.
type selfCallCase struct {
	wc         workCase
	settleOnly bool
	n          int
}

// Each work case in each mode at each size is a case of a split (gate-fast
// T1): part k checks every fourth case from case k, as a top-level test of
// its own, dealt to four parallel subtests. Under -short a part keeps
// every fourth case of its share.
var selfCallWorkLinear = NewSplit(Split[selfCallCase]{
	Name: "TestSelfCallWorkLinear", Parts: 4, Short: 4, Workers: 4,
	Count: 90, Digest: "c32edc13c2f76eca624d8f8b8f48e9ce41368bac60021125ed552a4286b48bea",
	Cases: SeqOf(func() []selfCallCase {
		var out []selfCallCase
		for _, settleOnly := range []bool{false, true} {
			for _, wc := range workCases {
				for _, n := range []int{250, 500, 1000, 2000, 4000} {
					out = append(out, selfCallCase{wc, settleOnly, n})
				}
			}
		}
		return out
	}),
	Key: func(c selfCallCase) string {
		return fmt.Sprintf("%s settle-only=%v n=%d\n%s", c.wc.name, c.settleOnly, c.n, c.wc.src(c.n))
	},
	Run: runSelfCallWorkLinear,
})

func TestSelfCallWorkLinearPart0(t *testing.T) { selfCallWorkLinear.Part(t, 0) }
func TestSelfCallWorkLinearPart1(t *testing.T) { selfCallWorkLinear.Part(t, 1) }
func TestSelfCallWorkLinearPart2(t *testing.T) { selfCallWorkLinear.Part(t, 2) }
func TestSelfCallWorkLinearPart3(t *testing.T) { selfCallWorkLinear.Part(t, 3) }

func runSelfCallWorkLinear(t *testing.T, cases iter.Seq[selfCallCase]) {
	for sc := range cases {
		wc, settleOnly, n := sc.wc, sc.settleOnly, sc.n
		t.Run(fmt.Sprintf("%s settle-only=%v/n=%d", wc.name, settleOnly, n), func(t *testing.T) {
			src := wc.src(n)
			nodes := sourceNodes(t, src)
			start := time.Now()
			c, diags := checkSettling(src, settleOnly)
			elapsed := time.Since(start)
			var codes []string
			for _, d := range diags {
				codes = append(codes, d.Code)
			}
			want := wc.diags
			if wc.codes != nil {
				want = wc.codes(n)
			}
			if got := strings.Join(codes, " "); got != want {
				t.Fatalf("n = %d: want %q; got %v", n, want, diags)
			}
			graph := c.bodyNodes + c.bodyEdges
			t.Logf("n = %d: %d source nodes, %d bodies, %d edges; work %d nodes (%.2f per source node); %d settling checks, %d worklist steps, %d fixpoint checks; %s",
				n, nodes, c.bodyNodes, c.bodyEdges, c.workNodes, float64(c.workNodes)/float64(nodes), c.selfChecks, c.worklistSteps, c.convergeChecks, elapsed)
			if c.workNodes > workFactor*nodes {
				t.Errorf("n = %d: the body checks visited %d nodes for %d source nodes (bound %d)", n, c.workNodes, nodes, workFactor*nodes)
			}
			if c.worklistSteps > workFactor*graph {
				t.Errorf("n = %d: %d worklist steps for %d bodies and %d edges (bound %d)", n, c.worklistSteps, c.bodyNodes, c.bodyEdges, workFactor*graph)
			}
		})
	}
}

// armsSource is the round 14 review's gen-arms.ts: n error types T0…T(n-1),
// and main catches f() with the arms E, T0…T(n-1). In mode "out" f fails E
// alone, so every Ti is a Catch 2; in "written" f's written row holds them
// all; in "inferred" f's inferred row does, from n tries. With catch unset,
// main is empty: the same declarations without the catch.
func armsSource(n int, mode string, catch bool) string {
	var b strings.Builder
	b.WriteString("type E {}\n")
	for i := range n {
		fmt.Fprintf(&b, "type T%d {}\n", i)
	}
	b.WriteString("func e() fails E { return E }\n")
	switch mode {
	case "written":
		b.WriteString("func f() fails E")
		for i := range n {
			fmt.Fprintf(&b, " | T%d", i)
		}
		b.WriteString(" { return E }\n")
	case "inferred":
		for i := range n {
			fmt.Fprintf(&b, "func g%d() fails T%d { return T%d }\n", i, i, i)
		}
		b.WriteString("func f() {\n\ttry e()\n")
		for i := range n {
			fmt.Fprintf(&b, "\ttry g%d()\n", i)
		}
		b.WriteString("}\n")
	default:
		b.WriteString("func f() { try e() }\n")
	}
	if !catch {
		b.WriteString("func main() {}\n")
		return b.String()
	}
	b.WriteString("func main() {\n\tf() catch {\n\tcase E:\n")
	for i := range n {
		fmt.Fprintf(&b, "\tcase T%d:\n", i)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// TestCatchArmsWorkLinear: a catch with n arms costs identity work linear
// in its arms (round 14 review M1: a Row copied per arm made it O(n²)). The
// catch's share is the table's charged work (typeTable.work) of the source
// minus that of the same declarations without the catch, bounded by
// workFactor times the catch's syntax nodes, from n = 1000 to n = 32000. A
// written or inferred row of n types costs its own work outside the catch,
// which the difference leaves out. The test logs the time.
//
// That row building is O(n²) (rowExpr's Row.with per element; the inferred
// row grows one try at a time), so the written and inferred modes stop at
// n = 4000 to keep the gate fast. The check-v1 follow-up (196r) that makes
// row building linear restores n = 32000 for them.
func TestCatchArmsWorkLinear(t *testing.T) {
	for _, mode := range []string{"out", "written", "inferred"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			for _, n := range []int{1000, 2000, 4000, 8000, 16000, 32000} {
				if mode != "out" && n > 4000 {
					break
				}
				src, base := armsSource(n, mode, true), armsSource(n, mode, false)
				start := time.Now()
				c, diags := checkSettling(src, false)
				elapsed := time.Since(start)
				cb, baseDiags := checkSettling(base, false)
				if len(baseDiags) != 0 {
					t.Fatalf("n = %d: the source without the catch: %v", n, baseDiags[0])
				}
				want := 0
				if mode == "out" {
					want = n
				}
				if len(diags) != want {
					t.Fatalf("n = %d: want %d diagnostics; got %d (first %v)", n, want, len(diags), diags[:min(len(diags), 1)])
				}
				for _, d := range diags {
					if d.Code != CodeType || !strings.Contains(d.Message, "is not in the caught row") {
						t.Fatalf("n = %d: want only Catch 2 on a type the row lacks; got %v", n, d)
					}
				}
				nodes := sourceNodes(t, src) - sourceNodes(t, base)
				work := c.types.work - cb.types.work
				t.Logf("n = %d: %d catch nodes; catch work %d (%.2f per node); total work %d; %s", n, nodes, work, float64(work)/float64(nodes), c.types.work, elapsed)
				if work > workFactor*nodes {
					t.Errorf("n = %d: the catch charged %d steps for %d syntax nodes (bound %d)", n, work, nodes, workFactor*nodes)
				}
			}
		})
	}
}

// gwaitSource is a call that typing from empty rows cannot resolve: the
// catch arm `case G as g` takes G's instance from w's row, which is empty
// until w settles, so g.m() waits for the worklist.
const gwaitSource = `type G[T] {}
func m[T](g G[T]) {}
func load() fails G[int] { return G[int] }
func w() { try load() }
func k() {
	w() catch {
	case G as g:
		g.m()
	}
}
func main() {}
`

// TestSelfCallWaitsForRow: a call whose receiver's type comes from a
// callee's row is resolved to its function before the bodies are ordered,
// by the worklist, and the source is clean in both modes. The receiver of
// the reviewer's catch (`let b = w() catch { case E: B }`) does not wait:
// a catch keeps its value's type whatever the row (handlers.go catch), so
// the typing from empty rows resolves b.m().
func TestSelfCallWaitsForRow(t *testing.T) {
	cases := []struct {
		name, src, call, fn string
		waits               bool
	}{
		{"the instance of a generic catch arm", gwaitSource, "g.m", "m", true},
		{"the value of a catch", workCases[2].src(3), "b.m", "m", false},
	}
	for _, tc := range cases {
		for _, settleOnly := range []bool{false, true} {
			var got map[*syntax.SelectorExpr]*Func
			selfTrace = func(_ *checker, _ []*syntax.SelectorExpr, resolved map[*syntax.SelectorExpr]*Func) {
				got = resolved
			}
			c, diags := checkSettling(tc.src, settleOnly)
			selfTrace = nil
			if len(diags) != 0 {
				t.Fatalf("%s, settle-only=%v: %v", tc.name, settleOnly, diags)
			}
			resolved := false
			for x, f := range got {
				if exprText(x) == tc.call && f != nil && f.Name == tc.fn {
					resolved = true
				}
			}
			if !resolved {
				t.Errorf("%s, settle-only=%v: %s() was not resolved before the bodies were ordered: %v", tc.name, settleOnly, tc.call, got)
			}
			if ran := c.selfChecks > 0; ran != (tc.waits || settleOnly) {
				t.Errorf("%s, settle-only=%v: the worklist ran: %v", tc.name, settleOnly, ran)
			}
			if p := RefWalkProblems(tc.src); len(p) > 0 {
				t.Errorf("%s, settle-only=%v: %s", tc.name, settleOnly, strings.Join(p, "; "))
			}
		}
	}
}
