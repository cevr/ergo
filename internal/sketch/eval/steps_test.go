package eval

import (
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStepsCountTheNodesThatWork is the seeded property of the step's
// unit (FU-eval-step-per-op): an expression costs one step per node that
// does work of its own, a binary or unary operation, a parenthesis, a
// field read; a leaf (a local, a literal) and a folded constant cost
// none, as Go's build reads them as operands of the node that uses them.
// So the steps of `let r = E` less the steps of `let r = a` are the
// model's count of E's working nodes (stepModel), whatever E's shape.
//
// Each seed draws expressions over two int locals, a record local's field
// and small literals. A failing seed prints both programs, both counts and
// the replay command.
//
// Mutants it alone catches (testdata/mutants.json): stepop-leaf-charged
// (a leaf costs a step again), stepop-root-charged (a field path's root
// local costs a step), stepop-binary-free (an operation costs none).
func TestStepsCountTheNodesThatWork(t *testing.T) {
	first, last := uint64(1), uint64(64)
	if s := os.Getenv("ERGO_STEPOP_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	base := stepProgram("a")
	baseSteps := stepsOf(t, base)
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x57e9))
		var e stepExpr
		for {
			e = drawStepExpr(r, 5)
			if !e.constant() {
				break
			}
		}
		src := stepProgram(e.text)
		// The obligation's preflight reads each node of the body once
		// (preflight.go), a cost of the source, not of the run.
		read := nodesOf(t, src) - nodesOf(t, base)
		got := stepsOf(t, src) - baseSteps - read
		if want := e.cost(); got != want {
			t.Fatalf("seed %d: E = %s costs %d steps beside its %d preflight reads, model %d\n%s\nbase (%d steps):\n%s\nreplay: ERGO_STEPOP_SEED=%d go test -run '^TestStepsCountTheNodesThatWork$' ./internal/sketch/eval", seed, e.text, got, read, want, src, baseSteps, base, seed)
		}
	}
}

// TestStepsCountBytesByTheWord is the seeded property of a string's
// copies and comparisons (FU-eval-step-per-op): Go's build moves and
// compares bytes a word at a time (memmove, memequal), so a concatenation,
// an interpolation and an equality of n bytes cost their node's step and
// ceil(n/8) more, never n. Each seed draws two strings' lengths. A
// failing seed prints the program, both counts and the replay command.
//
// Mutants it alone catches (testdata/mutants.json): stepop-bytes-by-byte
// (a step a byte again), stepop-equal-by-byte (an equality reads a step a
// byte).
func TestStepsCountBytesByTheWord(t *testing.T) {
	first, last := uint64(1), uint64(32)
	if s := os.Getenv("ERGO_STEPOP_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	words := func(n int) int64 { return int64((n + 7) / 8) }
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0xb17e))
		m, n := r.IntN(200), r.IntN(200)
		prog := func(e string) string {
			return "test T {\n\tlet s = \"" + strings.Repeat("s", m) + "\"\n\tlet u = \"" + strings.Repeat("s", m) + "\"\n\tlet t = \"" + strings.Repeat("t", n) + "\"\n\tlet r = " + e + "\n\tassert r == r || len(u) > len(t)\n}\n"
		}
		base := prog("s")
		for _, c := range []struct {
			e    string
			want int64
		}{
			{"s + t", 1 + words(m+n)},
			{`"\(s)\(t)"`, 1 + words(m+n)},
			{"s == u", 1 + words(m)},
		} {
			src := prog(c.e)
			// r == r reads r's bytes: the base's r is s.
			rr := int64(0)
			switch c.e {
			case "s == u":
				rr = -words(m)
			default:
				rr = words(m+n) - words(m)
			}
			read := nodesOf(t, src) - nodesOf(t, base)
			got := stepsOf(t, src) - stepsOf(t, base) - read - rr
			if got != c.want {
				t.Fatalf("seed %d: %s over %d and %d bytes costs %d steps, model %d\n%s\nreplay: ERGO_STEPOP_SEED=%d go test -run '^TestStepsCountBytesByTheWord$' ./internal/sketch/eval", seed, c.e, m, n, got, c.want, src, seed)
			}
		}
	}
}

// stepProgram is a test whose one varying expression is e.
func stepProgram(e string) string {
	return `type P { X int }

test T {
	let a = 3
	let b = 4
	let p = P{X: 5}
	let r = ` + e + `
	assert r == r || a+b+p.X > 0
}
`
}

// nodesOf is the count of src's syntax nodes.
func nodesOf(t *testing.T, src string) int64 {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	syntax.Inspect(f, func(syntax.Node) bool { n++; return true })
	return n
}

// stepsOf is the steps the test of src used; it must pass.
func stepsOf(t *testing.T, src string) int64 {
	t.Helper()
	rep, err := Run(checked(t, src), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range rep.Results {
		if res.Name == "T" {
			if res.Status != Passed {
				t.Fatalf("T: %s (%s)\n%s", res.Status, res.Reason, src)
			}
			return res.Steps
		}
	}
	t.Fatalf("T did not report\n%s", src)
	return 0
}

// stepExpr is a drawn expression: its text, whether it is a leaf, and
// its operands.
type stepExpr struct {
	text string
	kind byte // 'v' a local, 'l' a literal, 'f' a field, 'b' binary, 'u' unary, 'p' paren
	kids []stepExpr
}

func drawStepExpr(r *rand.Rand, depth int) stepExpr {
	if depth == 0 || r.IntN(3) == 0 {
		switch r.IntN(4) {
		case 0:
			return stepExpr{text: "a", kind: 'v'}
		case 1:
			return stepExpr{text: "b", kind: 'v'}
		case 2:
			return stepExpr{text: "p.X", kind: 'f'}
		}
		return stepExpr{text: strconv.Itoa(1 + r.IntN(9)), kind: 'l'}
	}
	switch r.IntN(4) {
	case 0:
		x := drawStepExpr(r, depth-1)
		if x.kind == 'u' || x.kind == 'b' {
			x = stepExpr{text: "(" + x.text + ")", kind: 'p', kids: []stepExpr{x}}
		}
		return stepExpr{text: "-" + x.text, kind: 'u', kids: []stepExpr{x}}
	case 1:
		x := drawStepExpr(r, depth-1)
		return stepExpr{text: "(" + x.text + ")", kind: 'p', kids: []stepExpr{x}}
	}
	x, y := drawStepExpr(r, depth-1), drawStepExpr(r, depth-1)
	op := []string{" + ", " - "}[r.IntN(2)]
	if y.kind == 'u' || y.kind == 'b' {
		// keep the drawn tree the parsed one: a right operand that is an
		// operation goes in parentheses.
		y = stepExpr{text: "(" + y.text + ")", kind: 'p', kids: []stepExpr{y}}
	}
	return stepExpr{text: x.text + op + y.text, kind: 'b', kids: []stepExpr{x, y}}
}

// constant is whether e reads no local: the checker folds it.
func (e stepExpr) constant() bool {
	switch e.kind {
	case 'v', 'f':
		return false
	case 'l':
		return true
	}
	for _, k := range e.kids {
		if !k.constant() {
			return false
		}
	}
	return true
}

// cost is the model's steps of e: one per node that works, none for a
// leaf or a folded constant.
func (e stepExpr) cost() int64 {
	if e.constant() || e.kind == 'v' {
		return 0
	}
	n := int64(1)
	for _, k := range e.kids {
		n += k.cost()
	}
	return n
}
