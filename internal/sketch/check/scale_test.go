package check_test

import (
	"fmt"
	"iter"
	"math"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
)

// TestCheckerScale fails when the checker's cost grows faster than linearly
// with the size of its input. Each shape below generates a program at sizes
// n, 2n and 4n (check.GrowthSizes: 4n has at most check.GrowthMaxNodes
// syntax nodes) whose source grows linearly in n, and the test measures the
// bytes check.CheckSource allocates for it: the least of three runs, less
// what an empty program allocates. Allocation counts are deterministic,
// unlike time. A shape fails when the fitted exponent
// log2(bytes(4n)/bytes(n))/2 passes 1.3, so 4n may cost at most about 6x n
// (linear is 4x, quadratic 16x).
//
// Shapes that measure non-allocating linear bounds (such as capability name
// hashing and comparison) also assert deterministic step or byte counters
// through check.GrowthBounded; time is logged only, never asserted.
//
// Every shape comes from a review finding of run wf10 about memory or
// quadratic cost; the finding is named at the shape. A valid shape must
// check with no diagnostic, so a checker that stops early on an error
// cannot pass for linear.
//
// The test is not parallel: TotalAlloc counts every goroutine, so it runs
// while no other test of the package does.
//
// The shapes are a split (gate-fast T1): part k checks every fourth shape
// from shape k, as a top-level test of its own. Under -short the parts
// skip, as the whole test did.
var checkerScale = check.NewSplit(check.Split[scaleShape]{
	Name: "TestCheckerScale", Parts: 4,
	Cases: check.SeqOf(func() []scaleShape { return scaleShapes }),
	Key:   scaleKey,
	Run:   runCheckerScale,
})

func TestCheckerScalePart0(t *testing.T) { checkerScale.Part(t, 0) }
func TestCheckerScalePart1(t *testing.T) { checkerScale.Part(t, 1) }
func TestCheckerScalePart2(t *testing.T) { checkerScale.Part(t, 2) }
func TestCheckerScalePart3(t *testing.T) { checkerScale.Part(t, 3) }

// scaleKey is what a shape checks: its fields and its sources at every
// size it is measured at.
func scaleKey(s scaleShape) string {
	return s.name + "\x00" + s.finding + "\x00" + s.skip + fmt.Sprintf("\x00diagnostics=%d\x00capNameBytes=%v", s.diagnostics, s.countCapNameBytes) + "\x00" + s.source(3)
}

func runCheckerScale(t *testing.T, shapes iter.Seq[scaleShape]) {
	empty, _ := measure("func main() {}\n")
	start := time.Now()
	for shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			if shape.skip != "" {
				t.Skip(shape.skip)
			}
			sizes := []int{shape.maxN / 4, shape.maxN / 2, shape.maxN}
			if shape.maxN == 0 {
				sizes = check.GrowthSizes(t, check.GrowthMaxNodes, func(n int) int { return check.SourceNodes(t, shape.source(n)) })
			}
			n := sizes[0]
			var bytes [3]float64
			var took [3]time.Duration
			var namePer []float64
			for i, size := range sizes {
				src := shape.source(size)
				nameBefore := check.CapNameBytes()
				_, diags := check.CheckSource(src)
				nameUsed := check.CapNameBytes() - nameBefore
				if len(diags) != shape.diagnostics {
					t.Fatalf("n=%d: want %d diagnostics, got %d:\n%s", size, shape.diagnostics, len(diags), listDiagnostics(diags))
				}
				if shape.countCapNameBytes {
					nodes := check.SourceNodes(t, src)
					namePer = append(namePer, float64(nameUsed)/float64(nodes))
					t.Logf("n=%d: cap name bytes %d (%d nodes, %.2f/node)", size, nameUsed, nodes, float64(nameUsed)/float64(nodes))
				}
				b, d := measure(src)
				bytes[i] = math.Max(float64(b-empty), 1)
				took[i] = d
			}
			span := math.Log2(float64(sizes[2]) / float64(sizes[0]))
			exponent := math.Log2(bytes[2]/bytes[0]) / span
			timeExponent := math.Log2(float64(took[2])/float64(took[0])) / span
			t.Logf("n=%d: bytes %.0f, %.0f, %.0f (exponent %.2f); time %v, %v, %v (exponent %.2f)",
				n, bytes[0], bytes[1], bytes[2], exponent, took[0], took[1], took[2], timeExponent)
			if exponent > maxScaleExponent {
				t.Errorf("4x the input allocates %.1fx the bytes (exponent %.2f > %.1f): the checker is superlinear on %s (%s)",
					bytes[2]/bytes[0], exponent, maxScaleExponent, shape.name, shape.finding)
			}
			if shape.countCapNameBytes {
				check.GrowthBounded(t, "cap name bytes per node", sizes, namePer)
			}
		})
	}
	t.Logf("scale test took %v", time.Since(start))
}

// maxScaleExponent is the largest fitted exponent a shape may show.
const maxScaleExponent = 1.3

// measure is the least number of bytes check.CheckSource allocates for src
// over three runs, and the least time a run takes; the first run also warms
// the checker.
func measure(src string) (uint64, time.Duration) {
	// The collector stays off during a run, so the time is the checker's
	// own; GOMEMLIMIT still bounds the heap.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	best, fastest := uint64(math.MaxUint64), time.Duration(math.MaxInt64)
	for range 3 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		began := time.Now()
		check.CheckSource(src)
		took := time.Since(began)
		runtime.ReadMemStats(&after)
		best = min(best, after.TotalAlloc-before.TotalAlloc)
		fastest = min(fastest, took)
	}
	return best, fastest
}

func listDiagnostics(diags []check.Diagnostic) string {
	var b strings.Builder
	for i, d := range diags {
		if i == 5 {
			fmt.Fprintf(&b, "\t... %d more\n", len(diags)-i)
			break
		}
		fmt.Fprintf(&b, "\t%s\n", d)
	}
	return b.String()
}

// scaleShape is one family of programs whose source grows linearly in n.
type scaleShape struct {
	name string
	// finding names the review finding the shape comes from.
	finding string
	// maxN, when set, is the largest size (under a parser limit) instead
	// of GrowthSizes'.
	maxN int
	// diagnostics is the number of diagnostics at every size: 0 for a
	// valid program.
	diagnostics       int
	countCapNameBytes bool
	source            func(n int) string
	// skip, when set, says what makes a shape superlinear on main; the case
	// is skipped until that is fixed, and the fix removes the skip.
	skip string
}

// longNames writes a capability whose name grows with n, named three times (its
// declaration, P's clause and context B), and n contexts that each embed B: the source grows
// linearly, and hashing or comparing the name per context would grow with n².
func longNames(n int) string {
	var g gen
	long := strings.Repeat("x", 400*n)
	name := "C" + long
	g.line("type %s { Label() string }", name)
	// P provides every capability, so it declares each one: the name
	// is written a third time.
	ys := make([]string, n)
	for i := range n {
		ys[i] = fmt.Sprintf(", Y%d", i)
	}
	g.line("type P {} implements %s%s", name, strings.Join(ys, ""))
	g.line("func Label(_ P) string { return \"p\" }")
	g.line("let B = context { %s: P }", name)
	for i := range n {
		g.line("type Y%d { Label() string }", i)
		g.line("let W%d = context { Y%d: P, B }", i, i)
	}
	g.line("func main() {}")
	return g.String()
}

// gen builds a source with fmt-style lines.
type gen struct{ strings.Builder }

func (g *gen) line(format string, args ...any) {
	fmt.Fprintf(&g.Builder, format, args...)
	g.WriteByte('\n')
}

var scaleShapes = []scaleShape{
	{
		name:    "declarations",
		finding: "checkquad r1 and r2: three passes quadratic in the declarations (13.4 GB); checker-196 r3 M5: the method dependency graph",
		source: func(n int) string {
			var g gen
			for i := range n {
				g.line("type T%d { V int64 }", i)
				g.line("func f%d(t T%d) int64 { return t.V }", i, i)
			}
			g.line("func main() {")
			for i := range n {
				g.line("\tlet _ = f%d(T%d{V: %d})", i, i, i)
			}
			g.line("}")
			return g.String()
		},
	},
	{
		name:    "providers",
		finding: "checkquad r2 follow-up: the provider-method matching loop; parse-v1 r6 M1 and r7 M1: same-named receiver-first functions made the body graph quadratic",
		source: func(n int) string {
			var g gen
			for i := range n {
				g.line("type S%d { Label() string }", i)
				g.line("type M%d { Text string } implements S%d", i, i)
				g.line("func Label(m M%d) string { return m.Text }", i)
			}
			g.line("let W = context {")
			for i := range n {
				g.line("\tS%d: M%d{Text: \"%d\"},", i, i, i)
			}
			g.line("}")
			for i := range n {
				g.line("func use%d() string needs S%d { return S%d.Label() }", i, i, i)
			}
			g.line("func main() {")
			for i := range n {
				g.line("\tlet _ = with W { use%d() }", i)
			}
			g.line("}")
			return g.String()
		},
	},
	{
		name:    "wide record",
		finding: "check-v1 stage-B r1 M2: uncharged work superlinear in pattern width",
		source: func(n int) string {
			var g gen
			g.line("type R {")
			for i := range n {
				g.line("\tF%d int64,", i)
			}
			g.line("}")
			g.line("func make() R {")
			g.line("\treturn R{")
			for i := range n {
				g.line("\t\tF%d: %d,", i, i)
			}
			g.line("\t}")
			g.line("}")
			g.line("func sum(r R) int64 {")
			g.line("\tlet s0 = r.F0")
			for i := 1; i < n; i++ {
				g.line("\tlet s%d = s%d + r.F%d", i, i-1, i)
			}
			g.line("\treturn s%d", n-1)
			g.line("}")
			g.line("func first(r R) int64 {")
			g.line("\tmatch r {")
			g.line("\tcase R{F0: 0}:")
			g.line("\t\treturn 0")
			g.line("\tcase _:")
			g.line("\t\treturn 1")
			g.line("\t}")
			g.line("}")
			g.line("func main() { let _ = sum(make()) + first(make()) }")
			return g.String()
		},
	},
	{
		name:    "sparse wide match",
		finding: "check-v1 stage-B r2 M1: a sparse wide match costs work and memory quadratic in the source before its S103 refusal",
		// main refuses this shape with S103 from about n = 100 on; the
		// refusal itself must come at linear cost.
		diagnostics: 1,
		source: func(n int) string {
			var g gen
			g.line("type R {")
			for i := range n {
				g.line("\tF%d bool,", i)
			}
			g.line("}")
			g.line("func pick(r R) int64 {")
			g.line("\tmatch r {")
			for i := range n {
				g.line("\tcase R{F%d: true}:", i)
				g.line("\t\treturn %d", i)
			}
			g.line("\tcase _:")
			g.line("\t\treturn -1")
			g.line("\t}")
			g.line("}")
			g.line("func use(r R) int64 { pick(r) }")
			return g.String()
		},
	},
	{
		name:    "long body",
		finding: "check-v1 r4 M1: bindings held per node, square of the source",
		source: func(n int) string {
			var g gen
			g.line("func f(a int64) int64 {")
			g.line("\tlet x0 = a")
			for i := 1; i < n; i++ {
				g.line("\tlet x%d = x%d + %d", i, i-1, i)
			}
			g.line("\treturn x%d", n-1)
			g.line("}")
			g.line("func main() { let _ = f(1) }")
			return g.String()
		},
	},
	{
		name:    "deep nesting",
		finding: "spec item 7: deep nesting of blocks and calls; check-v1 r5 m1: work per level of a deep block nest",
		// 200 nested ifs around 200 nested calls stays under the parser's
		// limit of 1000 levels.
		maxN: 200,
		source: func(n int) string {
			var g gen
			g.line("func g(a int64) int64 { return a + 1 }")
			g.line("func f(a int64) int64 {")
			// No indentation: it would grow with the depth, and the source
			// must grow linearly in n.
			for i := range n {
				g.line("if a > %d {", i)
			}
			g.line("return %s0%s", strings.Repeat("g(", n), strings.Repeat(")", n))
			for range n {
				g.line("}")
			}
			g.line("\treturn 0")
			g.line("}")
			g.line("func main() { let _ = f(1) }")
			return g.String()
		},
	},
	{
		name:    "nested literals",
		finding: "spec item 7: deep nesting of literals",
		// 4n nested record literals stays under the parser's limit of 1000
		// levels.
		source: func(n int) string {
			var g gen
			g.line("type L0 { V int64 }")
			for i := 1; i <= n; i++ {
				g.line("type L%d { In L%d }", i, i-1)
			}
			var lit strings.Builder
			for i := n; i >= 1; i-- {
				fmt.Fprintf(&lit, "L%d{In: ", i)
			}
			lit.WriteString("L0{V: 1}")
			lit.WriteString(strings.Repeat("}", n))
			g.line("func main() { let _ = %s }", lit.String())
			return g.String()
		},
	},
	{
		name:    "selector chain",
		finding: "parser-195 r1 M3: selector chains escape the nesting charge",
		source: func(n int) string {
			var g gen
			g.line("type S0 { V int64 }")
			for i := 1; i <= n; i++ {
				g.line("type S%d { N S%d }", i, i-1)
			}
			g.line("func f(s S%d) int64 { return s%s.V }", n, strings.Repeat(".N", n))
			g.line("func main() {}")
			return g.String()
		},
	},
	{
		name:    "binary chain",
		finding: "parser-195 r1 M3: binary chains escape the nesting charge",
		source: func(n int) string {
			var g gen
			g.line("func f(a int64) int64 { return a%s }", strings.Repeat(" + a", n))
			g.line("func main() { let _ = f(1) }")
			return g.String()
		},
	},
	{
		name:    "int match arms",
		finding: "pattern_oracle match shape; checker-196 r1 M8: exponential totality",
		source: func(n int) string {
			var g gen
			g.line("func f(x int64) int64 {")
			g.line("\tmatch x {")
			for i := range n {
				g.line("\tcase %d:", i)
				g.line("\t\treturn %d", i+1)
			}
			g.line("\tcase _:")
			g.line("\t\treturn 0")
			g.line("\t}")
			g.line("}")
			g.line("func main() { let _ = f(1) }")
			return g.String()
		},
	},
	{
		name:    "union match arms",
		finding: "checker-196 r4 M4, r5 M2 and r7 M4: variant canonicalization; check-v1 stage-B r1 M2",
		source: func(n int) string {
			var g gen
			var vs []string
			for i := range n {
				g.line("type V%d { On bool }", i)
				vs = append(vs, fmt.Sprintf("V%d", i))
			}
			g.line("type U = %s", strings.Join(vs, " | "))
			g.line("func f(u U) int64 {")
			g.line("\tmatch u {")
			for i := range n {
				g.line("\tcase V%d{On: true}:", i)
				g.line("\t\treturn %d", i)
				g.line("\tcase V%d{On: false}:", i)
				g.line("\t\treturn 0")
			}
			g.line("\t}")
			g.line("}")
			g.line("func main() { let _ = f(V0{On: true}) }")
			return g.String()
		},
	},
	{
		name:    "variable match arms",
		finding: "checker-196 r8 M3: variable edges restore quadratic index work",
		source: func(n int) string {
			var g gen
			var params, variants, args []string
			for i := range n {
				g.line("type A%d {}", i)
				params = append(params, fmt.Sprintf("T%d", i))
				variants = append(variants, fmt.Sprintf("V[T%d]", i))
				args = append(args, fmt.Sprintf("A%d", i))
			}
			g.line("type V[T] {}")
			g.line("type U[%s] = %s", strings.Join(params, ", "), strings.Join(variants, " | "))
			g.line("func f(x U[%s]) int64 {", strings.Join(args, ", "))
			g.line("\tmatch x {")
			g.line("\tcase V[A%d]:", n-1)
			g.line("\t\treturn 1")
			g.line("\tcase _:")
			g.line("\t\treturn 0")
			g.line("\t}")
			g.line("}")
			g.line("func main() {}")
			return g.String()
		},
	},
	{
		name:    "wildcard match arms",
		finding: "checker-196 r8 M3: wildcard (row) edges restore quadratic index work",
		source: func(n int) string {
			var g gen
			var variants []string
			for i := range n {
				g.line("type A%d {}", i)
				variants = append(variants, fmt.Sprintf("V[func(A%d) fails E]", i))
			}
			g.line("type V[T] {}")
			g.line("type Fault {}")
			g.line("type U[E] = %s", strings.Join(variants, " | "))
			g.line("func f(x U[Fault]) int64 {")
			g.line("\tmatch x {")
			for i := range n - 1 { // Leave a variant for the default arm.
				g.line("\tcase V[func(A%d) fails Fault]:", i)
				g.line("\t\treturn %d", i)
			}
			g.line("\tcase _:")
			g.line("\t\treturn 0")
			g.line("\t}")
			g.line("}")
			g.line("func main() {}")
			return g.String()
		},
	},
	{
		name:    "callback literals",
		finding: "parse-v1 r13 M2: callTaint filters a literal's flow again on every call",
		source: func(n int) string {
			var g gen
			g.line("type Fault {}")
			g.line("func walk() int64 fails Fault { return 1 }")
			g.line("func apply(f func() int64 fails Fault) int64 fails Fault { return f() }")
			for i := range n {
				g.line("func f%d() int64 fails Fault { return try walk() }", i)
			}
			g.line("func Run() int64 fails Fault {")
			g.line("\tlet outer = func() int64 fails Fault {")
			g.line("\t\tlet mut s int64 = 0")
			for i := range n {
				g.line("\t\tlet v%d = try f%d()", i, i)
				g.line("\t\ts = s + v%d", i)
			}
			g.line("\t\treturn s")
			g.line("\t}")
			g.line("\tlet mut t int64 = 0")
			for i := range n {
				g.line("\tlet a%d = try apply(outer)", i)
				g.line("\tlet b%d = try apply(func() int64 fails Fault { return try f%d() })", i, i)
				g.line("\tt = t + a%d + b%d", i, i)
			}
			g.line("\treturn t")
			g.line("}")
			g.line("func main() { let _ = Run() catch { case Fault: 0 } }")
			return g.String()
		},
	},
	{
		name:        "callback literals, one error",
		finding:     "parse-v1 r13 M2: a one-error program of 2000 functions took 31 s",
		diagnostics: 1,
		source: func(n int) string {
			var g gen
			g.line("type Fault {}")
			g.line("func walk() int64 fails Fault { return missing }")
			for i := range n {
				g.line("func f%d() int64 fails Fault { return try walk() }", i)
			}
			g.line("func Run() int64 fails Fault {")
			g.line("\tlet outer = func() int64 fails Fault {")
			g.line("\t\tlet mut s int64 = 0")
			for i := range n {
				g.line("\t\tlet v%d = try f%d()", i, i)
				g.line("\t\ts = s + v%d", i)
			}
			g.line("\t\treturn s")
			g.line("\t}")
			g.line("\tlet mut t int64 = 0")
			for i := range n {
				g.line("\tlet a%d = try outer()", i)
				g.line("\tt = t + a%d", i)
			}
			g.line("\treturn t")
			g.line("}")
			g.line("func main() { let _ = Run() catch { case Fault: 0 } }")
			return g.String()
		},
	},
	{
		name:    "duplicate names",
		finding: "parse-v1 r6 M1 and r7 M1: a self call on a receiver with no written type got an edge to every function of its name, so the body graph was quadratic",
		source: func(n int) string {
			var g gen
			for i := range n {
				g.line("type B%d { V int64 }", i)
				g.line("func Get(b B%d) int64 { return b.V }", i)
				g.line("func make%d() B%d { return B%d{V: %d} }", i, i, i, i)
			}
			g.line("func f() int64 {")
			g.line("\tlet mut s int64 = 0")
			for i := range n {
				g.line("\tlet b%d = make%d()", i, i)
				g.line("\ts = s + b%d.Get()", i)
			}
			g.line("\treturn s")
			g.line("}")
			g.line("func main() { let _ = f() }")
			return g.String()
		},
	},
	{
		name:    "call chain",
		finding: "parse-v1 r8 M1: the FIFO settling worklist rechecks a waiting body once per wave, quadratic in an acyclic graph",
		source: func(n int) string {
			var g gen
			g.line("type Fault {}")
			g.line("type Store { Get() string }")
			g.line("type Mem {} implements Store")
			g.line("func Get(_ Mem) string { return \"m\" }")
			g.line("func f%d(k int64) string fails Fault needs Store {", n)
			g.line("\tif k == 0 {")
			g.line("\t\treturn Fault")
			g.line("\t}")
			g.line("\treturn Store.Get()")
			g.line("}")
			for i := n - 1; i >= 0; i-- {
				g.line("func f%d(k int64) string fails Fault needs Store { return try f%d(k - 1) }", i, i+1)
			}
			g.line("func Run(k int64) string fails Fault needs Store { return try f0(k) }")
			g.line("func main() {")
			g.line("\tlet _ = with context { Store: Mem } { Run(3) } catch { case Fault: \"fault\" }")
			g.line("}")
			return g.String()
		},
	},
	{
		name:    "context chain",
		finding: "check-v1 r3 M1 and r4 M1: resolving the bindings of n withs over a context chain is quadratic (3.1 GB at 8000); checker-196 r17 M1 and r18 M1: the context embedding chain",
		source: func(n int) string {
			var g gen
			g.line("type Store { Label() string }")
			g.line("type Memory { Text string } implements Store")
			g.line("func Label(m Memory) string { return m.Text }")
			g.line("func run() string needs Store { return Store.Label() }")
			g.line("let W0 = context { Store: Memory{Text: \"live\"} }")
			for i := 1; i < n; i++ {
				g.line("let W%d = context { W%d }", i, i-1)
			}
			for i := range n {
				g.line("func run%d() string { return with W%d { run() } }", i, i)
			}
			g.line("func main() {")
			for i := range n {
				g.line("\tlet _ = run%d()", i)
			}
			g.line("}")
			return g.String()
		},
	},
	{
		name:              "long names",
		finding:           "checker-196 r21 m1: each context that reaches a capability hashes its whole name again, so the cost is the name length times the context count",
		countCapNameBytes: true,
		source:            longNames,
	},
	{
		name:    "catch arms",
		finding: "parse-v1 r14 M1: Row.with and Row.minus made wide catches quadratic in arm count",
		source: func(n int) string {
			var g gen
			var row []string
			for i := range n {
				g.line("type E%d {}", i)
				row = append(row, fmt.Sprintf("E%d", i))
			}
			g.line("func f(k int64) int64 fails %s {", strings.Join(row, " | "))
			for i := range n {
				g.line("\tif k == %d {", i)
				g.line("\t\treturn E%d", i)
				g.line("\t}")
			}
			g.line("\treturn k")
			g.line("}")
			g.line("func g(k int64) int64 {")
			g.line("\treturn f(k) catch {")
			for i := range n {
				g.line("\tcase E%d:", i)
				g.line("\t\t%d", i)
			}
			g.line("\t}")
			g.line("}")
			g.line("func main() { let _ = g(1) }")
			return g.String()
		},
	},
	{
		name:    "generic arity",
		finding: "checker-196 r6 M5: default-only generic subjects",
		source: func(n int) string {
			var g gen
			var params, fields, args, values []string
			for i := range n {
				params = append(params, fmt.Sprintf("T%d", i))
				fields = append(fields, fmt.Sprintf("\tF%d T%d,", i, i))
				args = append(args, "int64")
				values = append(values, fmt.Sprintf("F%d: %d", i, i))
			}
			g.line("type Many[%s] {", strings.Join(params, ", "))
			g.WriteString(strings.Join(fields, "\n"))
			g.line("")
			g.line("}")
			g.line("func first(m Many[%s]) int64 { return m.F0 }", strings.Join(args, ", "))
			g.line("func main() {")
			g.line("\tlet m = Many[%s]{%s}", strings.Join(args, ", "), strings.Join(values, ", "))
			g.line("\tlet _ = first(m)")
			g.line("}")
			return g.String()
		},
	},
	{
		name:    "generic call",
		finding: "checker-196 r8 M4: the constraint-refusal guard visits every constraint per constant-bound parameter",
		source: func(n int) string {
			var g gen
			var params, args, ones []string
			for i := range n {
				params = append(params, fmt.Sprintf("T%d", i))
				args = append(args, fmt.Sprintf("x%d T%d", i, i))
				ones = append(ones, "1")
			}
			g.line("func many[%s](%s) {}", strings.Join(params, ", "), strings.Join(args, ", "))
			g.line("func main() { many(%s) }", strings.Join(ones, ", "))
			return g.String()
		},
	},
	{
		name:    "row build",
		finding: "TYPETABLE-BOUND: sequential Row.with and Row.minus must grow near-linearly, never quadratically",
		source: func(n int) string {
			var g gen
			var row []string
			for i := range n {
				g.line("type E%d {}", i)
				row = append(row, fmt.Sprintf("E%d", i))
			}
			for i := range n {
				g.line("func step%d() fails E%d { return E%d }", i, i, i)
			}
			g.line("func runAll() fails %s {", strings.Join(row, " | "))
			for i := range n {
				g.line("\ttry step%d()", i)
			}
			g.line("}")
			g.line("func main() { runAll() catch { case _: } }")
			return g.String()
		},
	},
}
