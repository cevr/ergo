package check

import (
	"fmt"
	"iter"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// The with-site work (#229, E5 slice B). A charge reads the target's with
// scope once (withScope), and a with's footprint joins its context's node
// (footprinter.context), so the work at with sites is linear in the source
// on every shape below. withOp counts it, deterministically.

// withShape generates a clean program of size n for one shape of
// TestWithSiteWorkLinear.
func withShape(shape string, n int) string {
	var b strings.Builder
	caps := func() {
		for i := range n {
			fmt.Fprintf(&b, "type C%d { Get() string }\n", i)
		}
		b.WriteString("type Store { Get() string }\ntype Mem { V string }\n\nfunc Get(m Mem) string { return m.V }\n\n")
	}
	// all needs every capability C0 to Cn-1.
	all := func() {
		b.WriteString("func all() string needs C0")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, " | C%d", i)
		}
		b.WriteString(" {\n\tlet mut s = \"\"\n")
		for i := range n {
			fmt.Fprintf(&b, "\ts = s + C%d.Get()\n", i)
		}
		b.WriteString("\ts\n}\n\n")
	}
	switch shape {
	case "wowned": // the accept shape: Wi embeds W(i-1) and adds its own capability; one with per context
		caps()
		b.WriteString("func Run() string needs Store {\n\treturn Store.Get()\n}\n\n")
		b.WriteString("let W0 = context { Store: Mem{V: \"w0\"}, C0: Mem{V: \"c0\"} }\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let W%d = context { C%d: Mem{V: \"c\"}, W%d }\n", i, i, i-1)
		}
		for i := range n {
			fmt.Fprintf(&b, "func f%d() string {\n\treturn with W%d { Run() }\n}\n\n", i, i)
		}
	case "nested": // n nested inline withs, each binding its own capability; one innermost call needs them all
		caps()
		all()
		b.WriteString("func f() string {\n")
		for i := range n {
			fmt.Fprintf(&b, "%swith context { C%d: Mem{V: \"c\"} } {\n", strings.Repeat("\t", i+1), i)
		}
		fmt.Fprintf(&b, "%sall()\n", strings.Repeat("\t", n+1))
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&b, "%s}\n", strings.Repeat("\t", i+1))
		}
		b.WriteString("}\n")
	case "several": // with A0, …, An-1 of n named contexts; one call needs every capability
		caps()
		all()
		for i := range n {
			fmt.Fprintf(&b, "let A%d = context { C%d: Mem{V: \"c\"} }\n", i, i)
		}
		b.WriteString("func f() string {\n\treturn with A0")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, ", A%d", i)
		}
		b.WriteString(" { all() }\n}\n")
	case "stack": // with W0, …, Wn-1 of a chain of contexts, each embedding the one before; n charges
		caps()
		b.WriteString("let W0 = context { Store: Mem{V: \"w0\"}, C0: Mem{V: \"c0\"} }\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "let W%d = context { C%d: Mem{V: \"c\"}, W%d }\n", i, i, i-1)
		}
		b.WriteString("func f() string {\n\treturn with W0")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, ", W%d", i)
		}
		b.WriteString(" {\n\t\tlet mut s = \"\"\n")
		for i := range n {
			fmt.Fprintf(&b, "\t\ts = s + C%d.Get()\n", i)
		}
		b.WriteString("\t\ts\n\t}\n}\n")
	case "local many": // one local context named by n withs
		caps()
		b.WriteString("func f() string {\n\tlet w = context { Store: Mem{V: \"c\"} }\n\tlet mut s = \"\"\n")
		for range n {
			b.WriteString("\ts = s + with w { Store.Get() }\n")
		}
		b.WriteString("\ts\n}\n")
	case "local chain": // a chain of n local contexts, each embedding the one before; one call needs every capability
		caps()
		all()
		b.WriteString("func f() string {\n\tlet w0 = context { C0: Mem{V: \"c\"} }\n")
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "\tlet w%d = context { C%d: Mem{V: \"c\"}, w%d }\n", i, i, i-1)
		}
		fmt.Fprintf(&b, "\twith w%d { all() }\n}\n", n-1)
	case "alternating", "local alternating": // with W0, V0, W1, V1, … of two chains that bind the same capabilities (E5 r1 M1)
		// Wi and Vi each embed the one before and bind C0 to Ci, from
		// different entries: each frame's summary has the keys of the scope
		// below it, and one more, but none of its nodes.
		caps()
		w, v, indent := "W", "V", ""
		if shape == "local alternating" {
			w, v, indent = "w", "v", "\t"
			b.WriteString("func f() string {\n")
		}
		fmt.Fprintf(&b, "%slet %s0 = context { C0: Mem{V: \"w\"} }\n", indent, w)
		fmt.Fprintf(&b, "%slet %s0 = context { C0: Mem{V: \"v\"} }\n", indent, v)
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, "%slet %s%d = context { C%d: Mem{V: \"w\"}, %s%d }\n", indent, w, i, i, w, i-1)
			fmt.Fprintf(&b, "%slet %s%d = context { C%d: Mem{V: \"v\"}, %s%d }\n", indent, v, i, i, v, i-1)
		}
		if shape == "alternating" {
			b.WriteString("func f() string {\n")
		}
		fmt.Fprintf(&b, "\treturn with %s0, %s0", w, v)
		for i := 1; i < n; i++ {
			fmt.Fprintf(&b, ", %s%d, %s%d", w, i, v, i)
		}
		fmt.Fprintf(&b, " { C0.Get() + C%d.Get() }\n}\n", n-1)
	case "pairs", "fan": // the with budget's shapes (withbudget.go): each push changes the binder of n capabilities
		// pairs: with A, B, A, B, … (n of each) of two contexts that bind
		// n capabilities each, whose names interleave. fan: with Z0, …,
		// Zn-1, where each Zi embeds B and binds one capability of its own.
		for i := range n {
			fmt.Fprintf(&b, "type K%da { Get() string }\ntype K%db { Get() string }\n", i, i)
		}
		b.WriteString("type Mem { V string }\n\nfunc Get(m Mem) string { return m.V }\n\n")
		// one declares context name, binding K0s to Kn-1s.
		one := func(name, s string) {
			fmt.Fprintf(&b, "let %s = context { K0%s: Mem{V: \"c\"}", name, s)
			for i := 1; i < n; i++ {
				fmt.Fprintf(&b, ", K%d%s: Mem{V: \"c\"}", i, s)
			}
			b.WriteString(" }\n")
		}
		if shape == "pairs" {
			one("A", "a")
			one("B", "b")
		} else {
			one("B", "a")
		}
		if shape == "fan" {
			for i := range n {
				fmt.Fprintf(&b, "let Z%d = context { K%db: Mem{V: \"z\"}, B }\n", i, i)
			}
			b.WriteString("func f() string {\n\treturn with Z0")
			for i := 1; i < n; i++ {
				fmt.Fprintf(&b, ", Z%d", i)
			}
		} else {
			b.WriteString("func f() string {\n\treturn with A, B")
			for i := 1; i < n; i++ {
				b.WriteString(", A, B")
			}
		}
		b.WriteString(" { K0a.Get() + K0b.Get() }\n}\n")
	default:
		panic(shape)
	}
	return b.String()
}

// withWork checks src and returns the with-site steps it counted.
func withWork(t *testing.T, name, src string) int {
	t.Helper()
	steps := 0
	withOp = func(n int) { steps += n }
	defer func() { withOp = nil }()
	_, diags := checkFor(src)
	if len(diags) > 0 {
		t.Fatalf("%s: %d diagnostics, the first %v", name, len(diags), diags[0])
	}
	return steps
}

// withStepsPerByte is the bound of TestWithSiteWorkLinear: the with-site
// steps of a check are at most this many per byte of its source. The most
// measured is under 2 per byte (several, n = 4000); a walk of every
// enclosing frame per charge, or a membership test that scans its set,
// passes it by a factor of tens at 4n.
const withStepsPerByte = 4

// withSiteShape is one case of TestWithSiteWorkLinear: a shape.
type withSiteShape struct {
	shape string
}

// withSiteShapes are the shapes of TestWithSiteWorkLinear, in its order.
func withSiteShapes() []withSiteShape {
	var out []withSiteShape
	for _, shape := range []string{"wowned", "nested", "several", "stack", "local many", "local chain", "alternating", "local alternating"} {
		out = append(out, withSiteShape{shape})
	}
	return out
}

// TestWithSiteWorkLinear (#229 done-when 2 and 3; E5 Design B3): for each
// shape, at three sizes up to GrowthMaxNodes source nodes (test-speed plan
// T4), the with-site steps are at most withStepsPerByte per byte of the
// source, and the steps per byte and per log2 of the size at the largest
// size at most GrowthRatio times those at the smallest (linear, with a
// logarithmic factor for the treaps; a quadratic count grows about four
// times). The shapes are a Split (check-split-2, batch 9). withOp is one
// package hook, so a part checks its shapes one after another. Under
// -short a part keeps its first shape.
var withSiteWorkLinear = NewSplit(Split[withSiteShape]{
	Name: "TestWithSiteWorkLinear", Parts: 4, Short: 2,
	Cases: SeqOf(withSiteShapes),
	Key:   func(c withSiteShape) string { return c.shape },
	Run: func(t *testing.T, cases iter.Seq[withSiteShape]) {
		for c := range cases {
			withSiteWork(t, c.shape)
		}
	},
})

func TestWithSiteWorkLinearPart0(t *testing.T) { withSiteWorkLinear.Part(t, 0) }
func TestWithSiteWorkLinearPart1(t *testing.T) { withSiteWorkLinear.Part(t, 1) }
func TestWithSiteWorkLinearPart2(t *testing.T) { withSiteWorkLinear.Part(t, 2) }
func TestWithSiteWorkLinearPart3(t *testing.T) { withSiteWorkLinear.Part(t, 3) }

// withSiteWork checks one shape of TestWithSiteWorkLinear at its sizes.
func withSiteWork(t *testing.T, shape string) {
	t.Helper()
	sizes := GrowthSizes(t, GrowthMaxNodes, func(n int) int { return sourceNodes(t, withShape(shape, n)) })
	// The treaps cost a logarithmic factor, which at these small sizes
	// grows as much as the size does, so the ratio is of the steps per
	// byte and per log2 of the size.
	var perLog []float64
	for _, size := range sizes {
		src := withShape(shape, size)
		steps := withWork(t, fmt.Sprintf("%s n=%d", shape, size), src)
		per := float64(steps) / float64(len(src))
		perLog = append(perLog, per/math.Log2(float64(size)))
		t.Logf("%s n=%d: %d with-site steps, %d bytes, %.2f per byte", shape, size, steps, len(src), per)
		if steps > withStepsPerByte*len(src) {
			t.Errorf("%s n=%d: %d with-site steps for %d bytes, want at most %d per byte", shape, size, steps, len(src), withStepsPerByte)
		}
	}
	GrowthBounded(t, shape+" with-site steps per byte and log2 n", sizes, perLog)
}

// TestWithScopeIsTheWalk: the with scope answers each charge as the walk of
// the frames it replaced does (rows.go before #229): the innermost frame
// that binds the capability discharges it, unless an unknown frame is
// inside it. Random stacks of frames over shared summaries (chains of
// insertions, as the context summaries share embedded sets), replaces,
// inline contexts' own entries, value and unknown frames, with every push
// and pop, against a reference walk.
func TestWithScopeIsTheWalk(t *testing.T) {
	c, _ := checkFor("func main() {}")
	tab := c.types
	const keys = 24
	caps := make([]Type, keys)
	for i := range caps {
		caps[i] = &Named{Obj: &TypeName{Name: fmt.Sprintf("K%d", i)}}
	}
	m := &contextMeter{limit: 1 << 40}
	for seed := int64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewSource(seed))
		// A pool of summaries: chains that share every node but a path.
		var pool []*capSet
		for range 6 {
			s := &capSet{}
			for range 1 + r.Intn(keys) {
				e := &ContextEntry{}
				if r.Intn(4) == 0 {
					s, _ = s.put(m, caps[r.Intn(keys)], e)
				} else {
					s, _ = s.with(m, caps[r.Intn(keys)], e)
				}
				if r.Intn(3) == 0 {
					pool = append(pool, s)
				}
			}
			pool = append(pool, s)
		}
		frame := func() *withFrame {
			switch r.Intn(10) {
			case 0:
				return &withFrame{unknown: true}
			case 1:
				return &withFrame{} // a context value: binds nothing
			case 2, 3:
				// An inline context: own entries over its embeddings.
				base := pool[r.Intn(len(pool))]
				own := map[capKey]*capNode{}
				for range 1 + r.Intn(3) {
					k, _ := keyOf(caps[r.Intn(keys)])
					own[k] = &capNode{key: k, t: caps[0], entry: &ContextEntry{}}
				}
				return &withFrame{binds: &capSet{root: base.root, size: base.size, extra: own}}
			}
			return &withFrame{binds: pool[r.Intn(len(pool))]}
		}
		var stack []*withFrame
		var scopes []withScope
		var scope withScope
		for range 400 {
			if len(stack) > 0 && r.Intn(3) == 0 {
				stack, scope = stack[:len(stack)-1], scopes[len(scopes)-1]
				scopes = scopes[:len(scopes)-1]
			} else {
				f := frame()
				scopes = append(scopes, scope)
				stack = append(stack, f)
				scope, _ = scope.push(nil, f)
			}
			for _, cap := range caps {
				var want *withFrame
				wantUnknown := false
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].unknown {
						wantUnknown = true
						break
					}
					if stack[i].binds.has(tab, cap) {
						want = stack[i]
						break
					}
				}
				got, unknown := scope.binder(tab, cap)
				if unknown != wantUnknown || !unknown && got != want {
					t.Fatalf("seed %d, depth %d, %s: scope gives %p (unknown %v), the walk %p (unknown %v)", seed, len(stack), cap, got, unknown, want, wantUnknown)
				}
			}
		}
	}
}

// TestContextNodeIsItsProviders (E5 Design B1): a named context's
// footprint node, built from its own entries and one edge per embedded
// context, reaches exactly the entries its builder runs
// (Context.EachProvider): a replace hides the entry it overrides, at any
// depth, a replace of a replace, a diamond and an entry of several
// capabilities, one of which a replace overrides, included.
func TestContextNodeIsItsProviders(t *testing.T) {
	src := `type A { Use() }
type B { Use() }
type C { Use() }
type D { Use() }
type PX {}
type PY {}
type PZ {}
func Use(_ PX) {}
func Use(_ PY) {}
func Use(_ PZ) {}
let W0 = context { A: PX, B: PY }
let W1 = context { replace A: PZ, W0 }
let W2 = context { replace A: PY, W1 }
let W3 = context { W2 }
let W4 = context { C: PX, W3 }
let D1 = context { C: PX, W0 }
let D2 = context { replace B: PZ, W0 }
let Dia = context { replace A: PY, replace B: PX, D1, D2 }
let DiaTop = context { D: PX, Dia }
let M0 = context { A, B: PX, C: PY }
let M1 = context { replace A: PZ, M0 }
let M2 = context { replace B: PY, M1 }
let M3 = context { D: PZ, M2 }
func main() {}
`
	c, diags := checkFor(src)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	fp := &footprinter{pkg: c.pkg, info: c.pkg.Info, nodes: map[any]*fpNode{}, owners: &c.contextOwners}
	entryOf := func() map[*fpNode]*ContextEntry {
		m := map[*fpNode]*ContextEntry{}
		for k, n := range fp.nodes {
			if e, ok := k.(*ContextEntry); ok {
				m[n] = e
			}
		}
		return m
	}
	names := make([]string, 0, len(c.pkg.Contexts))
	for name := range c.pkg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := c.pkg.Contexts[name]
		root := fp.context(w)
		entries := entryOf()
		got := map[*ContextEntry]bool{}
		seen := map[*fpNode]bool{}
		var reach func(n *fpNode)
		reach = func(n *fpNode) {
			if seen[n] {
				return
			}
			seen[n] = true
			if e := entries[n]; e != nil {
				got[e] = true
				return // an entry node's edges are its provider's calls
			}
			if n.opaque {
				t.Errorf("%s: an opaque node", name)
			}
			for _, s := range n.edges {
				reach(s)
			}
		}
		reach(root)
		want := map[*ContextEntry]bool{}
		w.EachProvider(func(_ Type, e *ContextEntry) bool {
			want[e] = true
			return true
		})
		if len(want) == 0 {
			t.Fatalf("%s binds nothing", name)
		}
		for e := range want {
			if !got[e] {
				t.Errorf("%s: the node misses the entry of %v", name, e.Caps)
			}
		}
		for e := range got {
			if !want[e] {
				t.Errorf("%s: the node runs the entry of %v, which its builder does not run", name, e.Caps)
			}
		}
	}
}
