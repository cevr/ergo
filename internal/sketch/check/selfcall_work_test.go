package check

import (
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Linear work of the body checks (round 8 review M1). Every check of a
// body costs its size: c.workNodes adds the body's syntax nodes at each
// check, in every pass (the typing from empty rows, settling, the
// initialization discovery, the silent pass of a cycle, converge and the
// reporting pass). The tests bound that work by workFactor times the
// source's syntax nodes, and the worklist's graph steps by workFactor times
// (bodies + edges), at three sizes up to GrowthMaxNodes source nodes, and
// the work per unit at the largest size by GrowthRatio times that at the
// smallest. They never bound the time; they log it.

const workFactor = 8

// GrowthMaxNodes is the largest source, in syntax nodes, that a growth test
// checks (test-speed plan T4). A work counter tells linear from quadratic
// work at this size: the complexity mutants of T4 fail their tests here,
// so larger sources only cost time.
const GrowthMaxNodes = 2000

// GrowthRatio bounds a growth test's work per unit at its largest size
// over that at its smallest (a quarter of the size). Linear work keeps the
// ratio near 1 (a log factor and the fixed overhead move it a little);
// quadratic work makes it grow with the size, to about 4 from n to 4n.
const GrowthRatio = 1.5

// GrowthSizes is n/4, n/2 and n for the largest n whose source has at most
// limit units (syntax nodes, cells), from the units of the source at two
// sizes: a growth source grows linearly in n.
func GrowthSizes(t testing.TB, limit int, units func(n int) int) []int {
	t.Helper()
	a, b := units(16), units(32)
	per := float64(b-a) / 16
	if per <= 0 {
		t.Fatalf("the source does not grow with n: %d units at 16, %d at 32", a, b)
	}
	n := int(float64(limit-a)/per) + 16
	for n > 4 && units(n) > limit {
		n = n * 9 / 10
	}
	if n < 8 {
		t.Fatalf("the source at n = 8 is over %d units", limit)
	}
	return []int{n / 4, n / 2, n}
}

// GrowthBounded fails t when the work per unit at the largest size passes
// GrowthRatio times that at the smallest; per holds them by size.
func GrowthBounded(t testing.TB, what string, sizes []int, per []float64) {
	t.Helper()
	first, last := per[0], per[len(per)-1]
	if first > 0 && last > GrowthRatio*first {
		t.Errorf("%s: %.3g per unit at n = %d and %.3g at n = %d, want at most %.1f times: the work grows faster than the source",
			what, first, sizes[0], last, sizes[len(sizes)-1], GrowthRatio)
	}
}

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
	c.opts.Infer = true
	c.settleOnly = settleOnly
	c.run([]*syntax.File{file})
	return c, c.sorted()
}

// SourceNodes counts the syntax nodes of src (sourceNodes, for the
// package's external tests).
func SourceNodes(t testing.TB, src string) int {
	t.Helper()
	return sourceNodes(t, src)
}

// sourceNodes counts the syntax nodes of src.
func sourceNodes(t testing.TB, src string) int {
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

// selfCallCase is one work case in one mode, checked at three sizes.
type selfCallCase struct {
	wc         workCase
	settleOnly bool
}

// Each work case in each mode is a case of a split (gate-fast T1): part k
// checks every fourth case from case k, as a top-level test of its own,
// dealt to four parallel subtests. Under -short a part keeps every second
// case of its share.
var selfCallWorkLinear = NewSplit(Split[selfCallCase]{
	Name: "TestSelfCallWorkLinear", Parts: 4, Short: 2, Workers: 4,
	Cases: SeqOf(func() []selfCallCase {
		var out []selfCallCase
		for _, settleOnly := range []bool{false, true} {
			for _, wc := range workCases {
				out = append(out, selfCallCase{wc, settleOnly})
			}
		}
		return out
	}),
	Key: func(c selfCallCase) string {
		return fmt.Sprintf("%s settle-only=%v\n%s", c.wc.name, c.settleOnly, c.wc.src(3))
	},
	Run: runSelfCallWorkLinear,
})

func TestSelfCallWorkLinearPart0(t *testing.T) { selfCallWorkLinear.Part(t, 0) }
func TestSelfCallWorkLinearPart1(t *testing.T) { selfCallWorkLinear.Part(t, 1) }
func TestSelfCallWorkLinearPart2(t *testing.T) { selfCallWorkLinear.Part(t, 2) }
func TestSelfCallWorkLinearPart3(t *testing.T) { selfCallWorkLinear.Part(t, 3) }

func runSelfCallWorkLinear(t *testing.T, cases iter.Seq[selfCallCase]) {
	for sc := range cases {
		wc, settleOnly := sc.wc, sc.settleOnly
		t.Run(fmt.Sprintf("%s settle-only=%v", wc.name, settleOnly), func(t *testing.T) {
			sizes := GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, wc.src(n)) })
			var perNode, perStep []float64
			for _, n := range sizes {
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
				perNode = append(perNode, float64(c.workNodes)/float64(nodes))
				perStep = append(perStep, float64(c.worklistSteps)/float64(max(graph, 1)))
			}
			GrowthBounded(t, "body-check nodes per source node", sizes, perNode)
			GrowthBounded(t, "worklist steps per body and edge", sizes, perStep)
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
// workFactor times the catch's syntax nodes, at three sizes up to
// GrowthMaxNodes source nodes, and the work per catch node at the largest
// by GrowthRatio times that at the smallest. A written or inferred row of
// n types costs its own work outside the catch, which the difference
// leaves out. The test logs the time.
func TestCatchArmsWorkLinear(t *testing.T) {
	for _, mode := range []string{"out", "written", "inferred"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			sizes := GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, armsSource(n, mode, true)) })
			var per []float64
			for _, n := range sizes {
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
				per = append(per, float64(work)/float64(nodes))
			}
			GrowthBounded(t, "catch work per catch node", sizes, per)
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

func rowBuildSource(n int, inferred bool) string {
	var b strings.Builder
	var row []string
	for i := range n {
		fmt.Fprintf(&b, "type E%d {}\n", i)
		row = append(row, fmt.Sprintf("E%d", i))
	}
	for i := range n {
		fmt.Fprintf(&b, "func step%d() fails E%d { return E%d }\n", i, i, i)
	}
	if inferred {
		b.WriteString("func runAll() {\n")
	} else {
		fmt.Fprintf(&b, "func runAll() fails %s {\n", strings.Join(row, " | "))
	}
	for i := range n {
		fmt.Fprintf(&b, "\ttry step%d()\n", i)
	}
	b.WriteString("}\nfunc main() { runAll() catch { case _: } }\n")
	return b.String()
}

func TestRowBuildWorkLinear(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inferred bool
	}{
		{"written", false},
		{"inferred", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sizes := GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, rowBuildSource(n, tc.inferred)) })
			var per []float64
			for _, n := range sizes {
				src := rowBuildSource(n, tc.inferred)
				nodes := sourceNodes(t, src)
				c, _ := checkSettling(src, false)
				per = append(per, float64(c.types.work)/float64(nodes))
				if c.types.work > workFactor*nodes {
					t.Errorf("n=%d: type work %d for %d syntax nodes (bound %d)", n, c.types.work, nodes, workFactor*nodes)
				}
			}
			GrowthBounded(t, "row build type work per node", sizes, per)
		})
	}
}

func checkWithTypeBudget(t *testing.T, src string, limit *int, infer bool) ([]Diagnostic, *checker) {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	c := newChecker("main", Std, prelude().scope)
	c.opts.Infer = infer
	if limit != nil {
		c.hasTypeLimit = true
		c.typeLimitVal = *limit
	}
	c.run([]*syntax.File{file})
	return c.sorted(), c
}

func typeRefusals(diags []Diagnostic) int {
	n := 0
	for _, d := range diags {
		if d.Code == CodeType && strings.Contains(d.Message, "type budget") {
			n++
		}
	}
	return n
}

func TestTypeBudgetHeadroom(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/classf/*.ergo",
		"testdata/totality/*.ergo",
		"../testdata/*.ergo",
		"../testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
		"../../../internal/sketch/sketchtest/testdata/migrated/*.ergo",
	} {
		m, _ := filepath.Glob(pattern)
		files = append(files, m...)
	}
	if len(files) < 300 {
		t.Fatalf("found %d sources", len(files))
	}
	worst, worstName := 0.0, ""
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := usesKeyword.ReplaceAllString(string(data), "needs")
		pf, err := syntax.Parse(src)
		if err != nil {
			continue
		}
		nodes := 0
		syntax.Inspect(pf, func(syntax.Node) bool { nodes++; return true })
		pkg, diags := Check("main", []*syntax.File{pf}, nil)
		if typeRefusals(diags) > 0 {
			t.Errorf("%s: refused by type budget", f)
		}
		w, ok := pkg.TypeWork()
		if !ok {
			t.Fatalf("%s: TypeWork missing", f)
		}
		lim := TypeLimit(nodes)
		share := float64(w) / float64(lim)
		if share >= 0.125 {
			t.Errorf("%s: %d of %d type steps (%.2f%%), want under 12.5%%", f, w, lim, 100*share)
		}
		if share > worst {
			worst, worstName = share, f
		}
	}
	for _, sp := range stdlib.Packages() {
		srcs, ok := stdlib.Package(sp)
		if !ok {
			continue
		}
		names := make([]string, 0, len(srcs))
		for k := range srcs {
			names = append(names, k)
		}
		sort.Strings(names)
		var pfiles []*syntax.File
		nodes := 0
		for _, nm := range names {
			pf, err := syntax.Parse(srcs[nm])
			if err != nil {
				t.Fatal(err)
			}
			pf.Name = nm
			syntax.Inspect(pf, func(syntax.Node) bool { nodes++; return true })
			pfiles = append(pfiles, pf)
		}
		parent := universe()
		if sp != "core" && sp != "option" && sp != "collections" {
			parent = prelude().scope
		}
		c := newChecker(sp, Std, parent)
		c.pkg.std = true
		c.pkg.trusted = true
		c.pkg.module = StdModule
		c.pkg.testOf = Std.(*stdImporter).testOf(sp)
		c.run(pfiles)
		diags := c.sorted()
		if typeRefusals(diags) > 0 {
			t.Errorf("std/%s: refused by type budget", sp)
		}
		lim := TypeLimit(nodes)
		share := float64(c.types.work) / float64(lim)
		if share >= 0.125 {
			t.Errorf("std/%s: %d of %d type steps (%.2f%%), want under 12.5%%", sp, c.types.work, lim, 100*share)
		}
		if share > worst {
			worst, worstName = share, "std/"+sp
		}
	}
	t.Logf("%d corpus+std checks; worst share is %.2f%% (%s)", len(files)+len(stdlib.Packages()), 100*worst, worstName)
}

func typeOverflowSource(repeatCalls int, withCascades bool) string {
	var b strings.Builder
	const errs = 500
	var row []string
	for i := range errs {
		fmt.Fprintf(&b, "type E%d {}\n", i)
		row = append(row, fmt.Sprintf("E%d", i))
	}
	b.WriteString("func helper() int64 fails ")
	b.WriteString(strings.Join(row, " | "))
	b.WriteString(" { return 0 }\n")
	b.WriteString("func first() {}\n")
	b.WriteString("func overflow() {\n")
	for range repeatCalls {
		b.WriteString("\tlet _ = helper() catch { case _: 0 }\n")
	}
	if withCascades {
		b.WriteString("\tlet bad int64 = \"wrong\"\n\tlet _ = bad\n")
	}
	b.WriteString("}\n")
	b.WriteString("func overflow2() {\n")
	for range repeatCalls {
		b.WriteString("\tlet _ = helper() catch { case _: 0 }\n")
	}
	b.WriteString("}\nfunc main() {}\n")
	return b.String()
}

func TestTypeBudgetRefuses(t *testing.T) {
	small := typeOverflowSource(2, false)
	pkg, diags := CheckSource(small)
	if len(diags) != 0 {
		t.Fatalf("small: unexpected diags: %v", diags)
	}
	if w, ok := pkg.TypeWork(); !ok || w <= 0 {
		t.Fatalf("small TypeWork: (%d, %v)", w, ok)
	}

	// Natural refusal under real TypeLimit(nodes), with cascading error after overflow
	// and a second overflowing function, must report exactly one S108 at overflow.
	huge := typeOverflowSource(500, true)
	bigPkg, ds := CheckSource(huge)
	if len(ds) != 1 || typeRefusals(ds) != 1 {
		t.Fatalf("huge: want exactly 1 type-budget S108 (no cascades), got %d: %v", len(ds), ds)
	}
	if !strings.Contains(ds[0].Message, "function overflow:") {
		t.Fatalf("huge: want S108 naming function overflow, got %v", ds[0].Message)
	}
	nodes := sourceNodes(t, huge)
	lim := TypeLimit(nodes)
	if w, ok := bigPkg.TypeWork(); !ok || w <= lim || w > lim+4096 {
		t.Fatalf("huge: work=%d limit=%d", w, lim)
	}
	_, again := CheckSource(huge)
	if !reflect.DeepEqual(again, ds) {
		t.Fatalf("second check differs: %v vs %v", again, ds)
	}

	// Adding a large comment does not change syntax node count or type budget verdict.
	commentPad := "// " + strings.Repeat("comment ", 4000) + "\n"
	limitVal := 1500
	srcA := rowBuildSource(20, false)
	srcB := commentPad + srcA
	if sourceNodes(t, srcA) != sourceNodes(t, srcB) {
		t.Fatalf("comment changed node count: %d vs %d", sourceNodes(t, srcA), sourceNodes(t, srcB))
	}
	dA, cA := checkWithTypeBudget(t, srcA, &limitVal, false)
	dB, cB := checkWithTypeBudget(t, srcB, &limitVal, false)
	if cA.types.limit != cB.types.limit || typeRefusals(dA) != typeRefusals(dB) {
		t.Fatalf("comment changed limit/verdict: %d/%v vs %d/%v", cA.types.limit, dA, cB.types.limit, dB)
	}

	// Declaration-order S108 even when SCC order checks dependencies before caller.
	var declB strings.Builder
	declB.WriteString("type E0 {}\ntype E1 {}\ntype E2 {}\ntype E3 {}\ntype E4 {}\ntype E5 {}\ntype E6 {}\ntype E7 {}\n")
	declB.WriteString("func caller() {\n\ttry callee()\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&declB, "\ttry g%d()\n", i)
	}
	declB.WriteString("}\nfunc callee() {\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&declB, "\ttry g%d()\n", i)
	}
	declB.WriteString("}\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&declB, "func g%d() fails E%d { return E%d }\n", i, i, i)
	}
	declValid := declB.String()
	fullValid, cFull := checkWithTypeBudget(t, declValid, nil, true)
	if len(fullValid) != 0 {
		t.Fatalf("declValid: %v", fullValid)
	}
	if !cFull.types.outOfOrder {
		t.Fatalf("declValid expected outOfOrder under inference")
	}
	limSplit := cFull.types.preBodyWork + 20
	dsDecl, cDecl := checkWithTypeBudget(t, declValid, &limSplit, true)
	if len(dsDecl) != 1 || typeRefusals(dsDecl) != 1 {
		t.Fatalf("declValid refused: %v", dsDecl)
	}
	if cDecl.types.refusedFn == nil || cDecl.types.refusedFn.Name == "caller" {
		t.Fatalf("expected SCC order not to stop in caller first, got %v", cDecl.types.refusedFn)
	}
	if !strings.Contains(dsDecl[0].Message, "function caller:") {
		t.Fatalf("declValid: expected refusal in caller in declaration order, got %v", dsDecl[0].Message)
	}

	// charge must still enforce the budget even when a lookup counter is attached.
	tab := newTypeTable()
	tab.limitSet = true
	tab.limit = 5
	restore := tab.attach(&lookupSteps{ops: new(int)})
	tab.charge(6)
	restore()
	if !tab.out {
		t.Fatalf("charge skipped budget when lookup counter was attached")
	}
}

func TestTypeBudgetPrefixes(t *testing.T) {
	cascade := `type E0 {}
type E1 {}
func step() fails E0 { return E0 }
func first() {
	try step() catch { case E0: }
}
func second() {
	let _ = step() catch { case E0: 1 }
	let x int64 = "bad"
	let _ = x
}
func main() {}
`
	cases := []struct {
		name  string
		src   string
		infer bool
	}{
		{"row-build", rowBuildSource(12, false), false},
		{"row-infer", rowBuildSource(12, true), true},
		{"catch", rowBuildSource(8, false), false},
		{"cascade", cascade, false},
	}
	for _, tc := range cases {
		full, cFull := checkWithTypeBudget(t, tc.src, nil, tc.infer)
		work := cFull.types.work
		if work == 0 {
			t.Fatalf("%s: no type work charged", tc.name)
		}
		for limit := 0; limit <= work+1; limit++ {
			l := limit
			diags, c := checkWithTypeBudget(t, tc.src, &l, tc.infer)
			if c.types.work > limit+512 {
				t.Fatalf("%s limit %d: charged %d steps (far past limit)", tc.name, limit, c.types.work)
			}
			if limit >= work {
				if !reflect.DeepEqual(diags, full) {
					t.Fatalf("%s limit %d >= work %d: got %v, want %v", tc.name, limit, work, diags, full)
				}
				continue
			}
			if typeRefusals(diags) != 1 {
				t.Fatalf("%s limit %d of %d: got %d type-budget S108 in %v, want 1", tc.name, limit, work, typeRefusals(diags), diags)
			}
			if tc.name != "cascade" && len(diags) != 1 {
				t.Fatalf("%s limit %d of %d: expected single refusal without cascades, got %v", tc.name, limit, work, diags)
			}
		}
	}
}
