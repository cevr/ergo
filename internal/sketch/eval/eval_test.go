package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEvalAgreesWithReference is the evaluator's seeded property against
// an independent reference model (AGENTS.md, Tests: a property against a
// reference model). Each seed generates a well-typed program over one
// integer width: a function of three parameters whose body nests
// arithmetic, bitwise operators, shifts, comparisons, && and ||, value
// if, a literal match and `try` of a function that fails with a typed
// error; a test catches that failure and asserts the result. The
// reference computes the same tree with Go's own operators on the Go type
// of that width, in Go's left-to-right order, with the defects Go panics
// on. The evaluator must report the same verdict: passed (or refuted, for
// a deliberately wrong expectation), or a defect. A failing seed prints
// the Ergo source, both results and the replay command.
//
// Mutants it kills (testdata/mutants.json): short-circuit-rhs (&& or ||
// evaluates its right operand when the left decides), int8-as-int64 (a
// narrow constant widens), wrap-lost (a narrow sum computed wider), defect-
// caught (a catch handles a defect), escape-caught (a catch handles a
// failure already leaving the frame), divide-by-zero-ok, negative-shift-ok,
// match-default-first (a default arm taken before a matching case).
func TestEvalAgreesWithReference(t *testing.T) {
	n := 400
	if testing.Short() {
		n = 120
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	kinds := map[string]bool{}
	verdicts := map[string]int{}
	for seed := first; seed < first+uint64(n); seed++ {
		g := newGen(seed)
		kinds[g.kind.name] = true
		src, want := g.program()
		verdicts[want]++
		if g.caught {
			verdicts["caught"]++
		}
		got := runSource(t, src)
		if got != want {
			t.Fatalf("seed %d: the evaluator says %s, the reference %s\n%s\nreplay: ERGO_EVAL_SEED=%d go test -run TestEvalAgreesWithReference ./internal/sketch/eval", seed, got, want, src, seed)
		}
	}
	if len(kinds) != len(widths) && n > 1 {
		t.Fatalf("the seeds reached only the widths %v", kinds)
	}
	if n > 1 && (verdicts["passed"] == 0 || verdicts["refuted"] == 0 || verdicts["defect"] == 0 || verdicts["caught"] == 0) {
		t.Fatalf("the seeds reached only the verdicts %v", verdicts)
	}
	t.Logf("verdicts %v", verdicts)
}

// runSource checks src and evaluates it; the verdict of its one test.
func runSource(t *testing.T, src string) string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v\n%s", diags, src)
	}
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("results %+v\n%s", rep.Results, src)
	}
	return string(rep.Results[0].Status)
}

// width is an Ergo integer type and its Go reference.
type width struct {
	name   string
	bits   uint
	signed bool
}

var widths = []width{
	{"int8", 8, true}, {"int16", 16, true}, {"int32", 32, true}, {"int", 64, true}, {"int64", 64, true},
	{"uint8", 8, false}, {"uint16", 16, false}, {"uint32", 32, false}, {"uint", 64, false}, {"uint64", 64, false},
}

// node is a generated expression: an integer one (op an arithmetic
// operator, "x" a parameter, "lit", "neg", "not", "if", "match", "try"),
// or a boolean one (op a comparison, "&&", "||", "!").
type node struct {
	op       string
	kids     []*node
	param    int
	lit      uint64 // the literal's bits at the width
	cond     *node  // if's condition
	caseLits []uint64
}

type gen struct {
	r    *rand.Rand
	kind width
	bad  uint64 // the value check fails on
	cut  uint64 // classify's bound between Small and Big
	// binds numbers the names a union or pair node binds.
	binds int
	// caught: the reference failed and the test caught the failure.
	caught bool
}

func newGen(seed uint64) *gen {
	r := rand.New(rand.NewPCG(seed, 0x6576616c))
	return &gen{r: r, kind: widths[r.IntN(len(widths))]}
}

// value is an edge-biased value at the width, as its bits.
func (g *gen) value() uint64 {
	edges := []uint64{0, 1, ^uint64(0), 2, 3, 7}
	switch g.r.IntN(4) {
	case 0:
		return g.trunc(edges[g.r.IntN(len(edges))])
	case 1:
		// The width's bounds.
		max := uint64(1)<<(g.kind.bits-1) - 1
		if !g.kind.signed && g.kind.bits < 64 {
			max = uint64(1)<<g.kind.bits - 1
		}
		if !g.kind.signed && g.kind.bits == 64 {
			max = ^uint64(0)
		}
		return g.trunc([]uint64{max, max + 1, max - 1}[g.r.IntN(3)])
	case 2:
		return g.trunc(uint64(g.r.IntN(9)))
	}
	return g.trunc(g.r.Uint64())
}

func (g *gen) trunc(v uint64) uint64 {
	if g.kind.bits == 64 {
		return v
	}
	return v & (uint64(1)<<g.kind.bits - 1)
}

// sext is v's value as an int64 for a signed width.
func (g *gen) sext(v uint64) int64 {
	s := 64 - g.kind.bits
	return int64(v<<s) >> s
}

// literal is v as a literal that takes its type from where it stands (a
// conversion's literal would be an int), in parentheses when negative;
// bare is v as a case writes it.
func (g *gen) literal(v uint64) string {
	if s := g.bare(v); s[0] != '-' {
		return s
	} else {
		return "(" + s + ")"
	}
}

func (g *gen) bare(v uint64) string {
	if g.kind.signed {
		return strconv.FormatInt(g.sext(v), 10)
	}
	return strconv.FormatUint(v, 10)
}

var intOps = []string{"+", "-", "*", "/", "%", "&", "|", "^", "&^", "<<", ">>"}

// intExpr is an integer expression of depth at most d. A literal never
// stands alone on both sides, so the checker folds no operator (a constant
// division by zero or overflow would be a static error).
func (g *gen) intExpr(d int) *node {
	if d == 0 || g.r.IntN(5) == 0 {
		return &node{op: "x", param: g.r.IntN(3)}
	}
	c := g.r.IntN(14)
	switch {
	case c < 6:
		op := intOps[g.r.IntN(len(intOps))]
		x, y := g.intExpr(d-1), g.intExpr(d-1)
		if g.r.IntN(3) == 0 && op != "/" && op != "%" && op != "<<" && op != ">>" {
			y = &node{op: "lit", lit: g.value()}
		}
		return &node{op: op, kids: []*node{x, y}}
	case c == 6:
		return &node{op: "neg", kids: []*node{g.intExpr(d - 1)}}
	case c == 7:
		return &node{op: "not", kids: []*node{g.intExpr(d - 1)}}
	case c < 10:
		return &node{op: "if", cond: g.boolExpr(d - 1), kids: []*node{g.intExpr(d - 1), g.intExpr(d - 1)}}
	case c == 10:
		n := &node{op: "match", kids: []*node{g.intExpr(d - 1)}}
		seen := map[uint64]bool{}
		for range 1 + g.r.IntN(3) {
			v := g.value()
			if seen[v] {
				continue
			}
			seen[v] = true
			n.caseLits = append(n.caseLits, v)
			n.kids = append(n.kids, g.intExpr(d-1))
		}
		n.kids = append(n.kids, g.intExpr(d-1)) // the default arm
		return n
	}
	if c < 13 {
		return &node{op: "try", kids: []*node{g.intExpr(d - 1)}}
	}
	if g.r.IntN(2) == 0 {
		// A union value built by an implicit variant conversion, matched
		// by an as pattern and by a record pattern that binds a field.
		g.binds++
		id := g.binds
		return &node{op: "union", lit: g.value(), param: id, kids: []*node{g.intExpr(d - 1), g.intExpr(d - 1)}}
	}
	// A tuple matched by tuple patterns, a constant and a binding.
	g.binds++
	id := g.binds
	return &node{op: "pair", lit: g.value(), param: id, kids: []*node{g.intExpr(d - 1), g.intExpr(d - 1), g.intExpr(d - 1), g.intExpr(d - 1)}}
}

var cmpOps = []string{"==", "!=", "<", "<=", ">", ">="}

func (g *gen) boolExpr(d int) *node {
	if d > 0 {
		switch g.r.IntN(5) {
		case 0:
			return &node{op: "&&", kids: []*node{g.boolExpr(d - 1), g.boolExpr(d - 1)}}
		case 1:
			return &node{op: "||", kids: []*node{g.boolExpr(d - 1), g.boolExpr(d - 1)}}
		case 2:
			return &node{op: "!", kids: []*node{g.boolExpr(d - 1)}}
		}
	}
	// A comparison takes operands of depth 1 at least, so that one under
	// && or || can defect or fail: only short-circuit evaluation keeps it
	// from running.
	k := max(d-1, 1)
	return &node{op: cmpOps[g.r.IntN(len(cmpOps))], kids: []*node{g.intExpr(k), g.intExpr(k)}}
}

// src writes n as Ergo source.
func (g *gen) src(n *node) string {
	switch n.op {
	case "x":
		return "x" + strconv.Itoa(n.param)
	case "lit":
		return g.literal(n.lit)
	case "neg":
		return "(-" + g.src(n.kids[0]) + ")"
	case "not":
		return "(^" + g.src(n.kids[0]) + ")"
	case "!":
		return "(!" + g.src(n.kids[0]) + ")"
	case "if":
		return "(if " + g.src(n.cond) + " { " + g.src(n.kids[0]) + " } else { " + g.src(n.kids[1]) + " })"
	case "match":
		var b strings.Builder
		b.WriteString("(match " + g.src(n.kids[0]) + " {\n")
		for i, v := range n.caseLits {
			b.WriteString("\tcase " + g.bare(v) + ":\n\t\t" + g.src(n.kids[i+1]) + "\n")
		}
		b.WriteString("\tcase _:\n\t\t" + g.src(n.kids[len(n.kids)-1]) + "\n\t})")
		return b.String()
	case "try":
		return "(try check(" + g.src(n.kids[0]) + "))"
	case "union":
		s, w := "s"+strconv.Itoa(n.param), "w"+strconv.Itoa(n.param)
		return "(match classify(" + g.src(n.kids[0]) + ") {\n\tcase Small as " + s + ":\n\t\t(" + s + ".V ^ " + g.literal(n.lit) +
			")\n\tcase Big{V: " + w + "}:\n\t\t(" + w + " + " + g.src(n.kids[1]) + ")\n\t})"
	case "pair":
		w := "p" + strconv.Itoa(n.param)
		return "(match (" + g.src(n.kids[0]) + ", " + g.src(n.kids[1]) + ") {\n\tcase (" + g.bare(n.lit) + ", _):\n\t\t" + g.src(n.kids[2]) +
			"\n\tcase (_, " + w + "):\n\t\t(" + w + " - " + g.src(n.kids[3]) + ")\n\t})"
	}
	return "(" + g.src(n.kids[0]) + " " + n.op + " " + g.src(n.kids[1]) + ")"
}

// program is a generated program and the reference's verdict.
func (g *gen) program() (string, string) {
	args := [3]uint64{g.value(), g.value(), g.value()}
	g.bad, g.cut = g.value(), g.value()
	if g.r.IntN(3) == 0 {
		g.bad = args[g.r.IntN(3)] // so that try often fails
	}
	body := g.intExpr(3)
	const mask = 77
	res, out := refEval(g, body, args)
	want := "passed"
	expect := res
	switch out.kind {
	case refDefect:
		want = "defect"
	case refFail:
		expect, g.caught = out.code^g.trunc(mask), true
	}
	if want == "passed" && g.r.IntN(4) == 0 {
		expect, want = g.trunc(expect+1), "refuted"
	}
	k := g.kind.name
	var b strings.Builder
	fmt.Fprintf(&b, "type Bad { Code %s }\n\n", k)
	fmt.Fprintf(&b, "type Size = Small{ V %s } | Big{ V %s }\n\n", k, k)
	fmt.Fprintf(&b, "func classify(v %s) Size {\n\tif v < %s {\n\t\tSmall{V: v}\n\t} else {\n\t\tBig{V: v}\n\t}\n}\n\n", k, g.literal(g.cut))
	fmt.Fprintf(&b, "func check(x %s) %s fails Bad {\n\tif x == %s {\n\t\treturn Bad{Code: x}\n\t}\n\tx\n}\n\n", k, k, g.literal(g.bad))
	fmt.Fprintf(&b, "func f(x0 %s, x1 %s, x2 %s) %s fails Bad {\n\t%s\n}\n\n", k, k, k, k, g.src(body))
	fmt.Fprintf(&b, "test T {\n\tlet r = f(%s, %s, %s) catch {\n\tcase Bad as e:\n\t\te.Code ^ %s\n\t}\n\tassert r == %s\n}\n",
		g.literal(args[0]), g.literal(args[1]), g.literal(args[2]), g.literal(g.trunc(mask)), g.literal(expect))
	return b.String(), want
}

type refKind int

const (
	refOK refKind = iota
	refDefect
	refFail
)

type refOut struct {
	kind refKind
	code uint64
}

// refEval is the reference: the tree computed with Go's operators on the
// width's Go type.
func refEval(g *gen, n *node, args [3]uint64) (uint64, refOut) {
	switch g.kind.name {
	case "int8":
		return refAt[int8](g, n, args)
	case "int16":
		return refAt[int16](g, n, args)
	case "int32":
		return refAt[int32](g, n, args)
	case "int", "int64":
		return refAt[int64](g, n, args)
	case "uint8":
		return refAt[uint8](g, n, args)
	case "uint16":
		return refAt[uint16](g, n, args)
	case "uint32":
		return refAt[uint32](g, n, args)
	}
	return refAt[uint64](g, n, args)
}

func refAt[T integer](g *gen, root *node, args [3]uint64) (uint64, refOut) {
	var ev func(n *node) (T, refOut)
	var cond func(n *node) (bool, refOut)
	ev = func(n *node) (T, refOut) {
		switch n.op {
		case "x":
			return T(args[n.param]), refOut{}
		case "lit":
			return T(n.lit), refOut{}
		case "neg", "not":
			v, o := ev(n.kids[0])
			if o.kind != refOK {
				return 0, o
			}
			if n.op == "neg" {
				return -v, o
			}
			return ^v, o
		case "if":
			c, o := cond(n.cond)
			if o.kind != refOK {
				return 0, o
			}
			if c {
				return ev(n.kids[0])
			}
			return ev(n.kids[1])
		case "match":
			v, o := ev(n.kids[0])
			if o.kind != refOK {
				return 0, o
			}
			for i, l := range n.caseLits {
				if v == T(l) {
					return ev(n.kids[i+1])
				}
			}
			return ev(n.kids[len(n.kids)-1])
		case "try":
			v, o := ev(n.kids[0])
			if o.kind != refOK {
				return 0, o
			}
			if v == T(g.bad) {
				return 0, refOut{kind: refFail, code: uint64(v) & mask64(g.kind.bits)}
			}
			return v, o
		case "union":
			v, o := ev(n.kids[0])
			if o.kind != refOK {
				return 0, o
			}
			if v < T(g.cut) {
				return v ^ T(n.lit), o
			}
			b, o := ev(n.kids[1])
			return v + b, o
		case "pair":
			a, o := ev(n.kids[0])
			if o.kind != refOK {
				return 0, o
			}
			b, o := ev(n.kids[1])
			if o.kind != refOK {
				return 0, o
			}
			if a == T(n.lit) {
				return ev(n.kids[2])
			}
			c, o := ev(n.kids[3])
			return b - c, o
		}
		a, o := ev(n.kids[0])
		if o.kind != refOK {
			return 0, o
		}
		b, o := ev(n.kids[1])
		if o.kind != refOK {
			return 0, o
		}
		switch n.op {
		case "+":
			return a + b, o
		case "-":
			return a - b, o
		case "*":
			return a * b, o
		case "/", "%":
			if b == 0 {
				return 0, refOut{kind: refDefect}
			}
			if n.op == "/" {
				return a / b, o
			}
			return a % b, o
		case "&":
			return a & b, o
		case "|":
			return a | b, o
		case "^":
			return a ^ b, o
		case "&^":
			return a &^ b, o
		case "<<", ">>":
			if b < 0 {
				return 0, refOut{kind: refDefect}
			}
			if n.op == "<<" {
				return a << b, o
			}
			return a >> b, o
		}
		panic("op " + n.op)
	}
	cond = func(n *node) (bool, refOut) {
		switch n.op {
		case "&&", "||":
			a, o := cond(n.kids[0])
			if o.kind != refOK || a == (n.op == "||") {
				return a, o
			}
			return cond(n.kids[1])
		case "!":
			a, o := cond(n.kids[0])
			return !a, o
		}
		a, o := ev(n.kids[0])
		if o.kind != refOK {
			return false, o
		}
		b, o := ev(n.kids[1])
		if o.kind != refOK {
			return false, o
		}
		switch n.op {
		case "==":
			return a == b, o
		case "!=":
			return a != b, o
		case "<":
			return a < b, o
		case "<=":
			return a <= b, o
		case ">":
			return a > b, o
		}
		return a >= b, o
	}
	v, o := ev(root)
	return uint64(v) & mask64(g.kind.bits), o
}

func mask64(bits uint) uint64 {
	if bits == 64 {
		return ^uint64(0)
	}
	return uint64(1)<<bits - 1
}
