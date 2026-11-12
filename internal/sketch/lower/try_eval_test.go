package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestTryCatchEvalAgrees (TRY-eval): a catch handles only the failures its
// subject yields through, and a try covers the caught expression too, so
// in `try h(g()) catch { case X: 0 }` a failure of g() propagates (rows.md,
// Catch 5). Each seed generates cases over calls nested in arguments, in
// receivers (`v.fa(…)`), in operands and in a value if's condition, value
// if branches and function literals called where they are written; each
// case is a partial catch under try, or a total catch whose subject's
// covered calls an outer try propagates. The reference model in this file
// gives each case's value or failure. The check-time evaluator runs every
// case's test in process, and the lowered Go type-checks (typecheckGo);
// TestTryCatchEvalAgreesBuilt runs the lowered programs, so both are held
// to the model and so to each other. A failing seed prints the Ergo
// source, both outputs and the replay command; ERGO_TRY_EVAL_SEED=<seed>
// replays one seed. The seeds must reach every outcome in teOutcomes.
//
// Mutants it kills (eval testdata/mutants.json): try-eval-catch-takes-covered,
// try-eval-branch-not-tail.
func TestTryCatchEvalAgrees(t *testing.T) {
	outcomes := map[string]int{}
	var programs []*lowered
	for _, seed := range teSeeds() {
		p := teGenerate(seed)
		for k, n := range p.outcomes {
			outcomes[k] += n
		}
		rep, err := evaluate(p.src)
		if err != nil {
			t.Fatalf("seed %d: %v\nsource:\n%s\n%s", seed, err, p.src, p.replay())
		}
		for _, r := range rep.Results {
			if r.Status != eval.Passed {
				t.Fatalf("seed %d: the evaluator: test %s %s: %s at line %d\nmodel says: %s\nsource:\n%s\n%s", seed, r.Name, r.Status, r.Reason, r.Site.Line, p.wantOf(r.Name), p.src, p.replay())
			}
		}
		programs = append(programs, mustLower(t, fmt.Sprintf("tryeval%d", seed), p.src))
	}
	typecheckGo(t, programs)
	if os.Getenv("ERGO_TRY_EVAL_SEED") == "" {
		for _, k := range teOutcomes {
			if outcomes[k] == 0 {
				t.Errorf("no case reached the outcome %s (reached: %v)", k, outcomes)
			}
		}
	}
}

// TestTryCatchEvalAgreesBuilt runs the same seeds' programs, under the
// default and the reference lowerings, and compares each printed line with
// the model. It builds Go because the claim is what the lowered program
// does when a covered call fails (the hoisted check returns before the
// catch's switch): go/types says only that it compiles.
func TestTryCatchEvalAgreesBuilt(t *testing.T) {
	needsBuild(t)
	var built []*lowered
	var progs []teProgram
	for _, seed := range teSeeds() {
		p := teGenerate(seed)
		progs = append(progs, p)
		built = append(built, mustLower(t, fmt.Sprintf("tryeval%d", seed), p.src))
		func() {
			defer referenceLowerings()()
			built = append(built, mustLower(t, fmt.Sprintf("tryeval%d_ref", seed), p.src))
		}()
	}
	exes := buildAll(t, built, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		for _, name := range []string{fmt.Sprintf("tryeval%d", p.seed), fmt.Sprintf("tryeval%d_ref", p.seed)} {
			stdout, stderr, code := runExe(t, exes[name])
			if code != 0 || stdout != p.want {
				t.Errorf("seed %d, %s: exit %d, stderr %q\n%s\nsource:\n%s\n%s", p.seed, name, code, stderr, diffLines(stdout, p.want), p.src, p.replay())
			}
		}
	}
}

// teOutcomes lists the outcomes the seeds must reach: a failure the catch
// takes at its subject, at a value if's branch and at a function literal
// called there; a failure it passes on because no arm names its type, or
// because a try covers the call (in an argument, a receiver, an operand or
// a condition, and under a total catch inside an outer try's call).
var teOutcomes = []string{
	"value", "caught-root", "caught-branch", "caught-literal", "passed-unhandled",
	"passed-argument", "passed-receiver", "passed-operand", "passed-condition", "passed-total",
}

func teSeeds() []int64 {
	if s := os.Getenv("ERGO_TRY_EVAL_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			panic(err)
		}
		return []int64{v}
	}
	out := make([]int64, 30)
	for i := range out {
		out[i] = int64(9100 + i)
	}
	return out
}

const teCases = 12

// teProgram is one seed's program and its model output.
type teProgram struct {
	seed     int64
	src      string
	want     string            // main's lines
	wants    map[string]string // test name -> the model's line
	outcomes map[string]int
}

func (p teProgram) replay() string {
	return fmt.Sprintf("replay: ERGO_TRY_EVAL_SEED=%d go test -run 'TestTryCatchEvalAgrees' ./internal/sketch/lower", p.seed)
}

func (p teProgram) wantOf(test string) string { return p.wants[test] }

const tePrelude = `type A { At string }

type B { At string }

func p(v int, tag string) int {
	v + 1
}

func fa(v int, tag string, fail bool) int fails A {
	if fail {
		return A{At: tag}
	}
	v + 2
}

func fb(v int, tag string, fail bool) int fails B {
	if fail {
		return B{At: tag}
	}
	v + 3
}

type Num { V int }

func bx(v int) Num {
	Num{V: v}
}

func ra(n Num, tag string, fail bool) int fails A {
	fa(n.V, tag, fail)
}

func rb(n Num, tag string, fail bool) int fails B {
	fb(n.V, tag, fail)
}
`

// teNode is a node of the generated int expressions.
type teNode struct {
	kind teKind
	n    int    // lit; if: the condition's bound
	fn   string // call: p, fa or fb
	recv bool   // call: written v.fn(…)
	tag  string
	fail bool
	op   string
	x    *teNode // call argument, operand, paren, if condition, literal body
	y    *teNode // operand; if then
	z    *teNode // if else
}

type teKind int

const (
	teLit teKind = iota
	teX
	teCall
	teBin
	teParen
	teIf
	teLitCall
)

type teGen struct {
	rng  *rand.Rand
	tags int
}

func (g *teGen) tag() string {
	g.tags++
	return "c" + strconv.Itoa(g.tags)
}

func (g *teGen) call(d int) *teNode {
	fn := []string{"p", "fa", "fb", "fa", "fb"}[g.rng.Intn(5)]
	return &teNode{kind: teCall, fn: fn, recv: g.rng.Intn(3) == 0, tag: g.tag(), fail: g.rng.Intn(3) == 0, x: g.node(d - 1)}
}

func (g *teGen) node(d int) *teNode {
	if d <= 0 {
		if g.rng.Intn(2) == 0 {
			return &teNode{kind: teX}
		}
		return &teNode{kind: teLit, n: g.rng.Intn(10)}
	}
	switch g.rng.Intn(9) {
	case 0:
		return g.node(0)
	case 1, 2, 3:
		return g.call(d)
	case 4, 5:
		return &teNode{kind: teBin, op: []string{"+", "-"}[g.rng.Intn(2)], x: g.node(d - 1), y: g.node(d - 1)}
	case 6:
		return &teNode{kind: teParen, x: g.node(d - 1)}
	case 7:
		return &teNode{kind: teIf, n: g.rng.Intn(16), x: g.node(d - 1), y: g.node(d - 1), z: g.node(d - 1)}
	}
	// A literal's body is its own failure target: it ends in a fallible
	// call, which its try passes out as the literal call's failure.
	body := g.call(d)
	if body.fn == "p" {
		body.fn = "fb"
	}
	return &teNode{kind: teLitCall, x: body}
}

// text renders n. An if is parenthesised in every position, and so is a
// binary operand that is itself binary. A receiver call is a dot call on
// a record, `bx(v).ra(…)`, the same as `fa(v, …)`.
func (n *teNode) text() string {
	switch n.kind {
	case teLit:
		return strconv.Itoa(n.n)
	case teX:
		return "x"
	case teCall:
		if n.fn == "p" {
			return fmt.Sprintf("p(%s, %q)", n.x.text(), n.tag)
		}
		if n.recv {
			return fmt.Sprintf("bx(%s).r%s(%q, %t)", n.x.text(), n.fn[1:], n.tag, n.fail)
		}
		return fmt.Sprintf("%s(%s, %q, %t)", n.fn, n.x.text(), n.tag, n.fail)
	case teBin:
		l, r := n.x.text(), n.y.text()
		if n.x.kind == teBin {
			l = "(" + l + ")"
		}
		if n.y.kind == teBin {
			r = "(" + r + ")"
		}
		return l + " " + n.op + " " + r
	case teParen:
		return "(" + n.x.text() + ")"
	case teIf:
		return fmt.Sprintf("(if %s > %d { %s } else { %s })", n.x.text(), n.n, n.y.text(), n.z.text())
	case teLitCall:
		return fmt.Sprintf("func() int fails A | B { try %s }()", n.x.text())
	}
	panic("teNode kind")
}

// fallible reports whether evaluating n can fail in its frame.
func (n *teNode) fallible() bool {
	switch n.kind {
	case teCall:
		return n.fn != "p" || n.x.fallible()
	case teBin, teIf:
		return n.x.fallible() || n.y.fallible() || n.z != nil && n.z.fallible()
	case teParen:
		return n.x.fallible()
	case teLitCall:
		return true
	}
	return false
}

// tails adds the error types n yields through (a catch on n handles
// these), and reports whether a fallible call sits in a position that
// only a try covers.
func (n *teNode) tails(types map[string]bool) (covered bool) {
	switch n.kind {
	case teCall:
		switch n.fn {
		case "fa":
			types["A"] = true
		case "fb":
			types["B"] = true
		}
		return n.x.fallible()
	case teBin:
		return n.x.fallible() || n.y.fallible()
	case teParen:
		return n.x.tails(types)
	case teIf:
		a, b := n.y.tails(types), n.z.tails(types)
		return n.x.fallible() || a || b
	case teLitCall:
		types["A"], types["B"] = true, true
	}
	return false
}

// teFail is a failure in the model: its type, its tag, and where the call
// that raised it stands.
type teFail struct {
	typ, tag string
	tail     bool
	pos      string
	literal  bool
}

// eval is the model: left-to-right evaluation with x = 3. tail and pos
// describe n's position in the catch's subject.
func (n *teNode) eval(tail bool, pos string) (int, *teFail) {
	switch n.kind {
	case teLit:
		return n.n, nil
	case teX:
		return 3, nil
	case teCall:
		argPos := "argument"
		if n.recv && n.fn != "p" {
			argPos = "receiver"
		}
		v, f := n.x.eval(false, argPos)
		if f != nil {
			return 0, f
		}
		switch {
		case n.fn == "p":
			return v + 1, nil
		case n.fail:
			return 0, &teFail{typ: strings.ToUpper(n.fn[1:]), tag: n.tag, tail: tail, pos: pos}
		case n.fn == "fa":
			return v + 2, nil
		}
		return v + 3, nil
	case teBin:
		a, f := n.x.eval(false, "operand")
		if f != nil {
			return 0, f
		}
		b, f := n.y.eval(false, "operand")
		if f != nil {
			return 0, f
		}
		if n.op == "+" {
			return a + b, nil
		}
		return a - b, nil
	case teParen:
		return n.x.eval(tail, pos)
	case teIf:
		c, f := n.x.eval(false, "condition")
		if f != nil {
			return 0, f
		}
		branch := pos
		if tail {
			branch = "branch"
		}
		if c > n.n {
			return n.y.eval(tail, branch)
		}
		return n.z.eval(tail, branch)
	case teLitCall:
		v, f := n.x.eval(true, "root")
		if f != nil {
			return 0, &teFail{typ: f.typ, tag: f.tag, tail: tail, pos: pos, literal: true}
		}
		return v, nil
	}
	panic("teNode kind")
}

var teArmValue = map[string]int{"A": -1, "B": -2}

// teGenerate makes seed's program: teCases case functions, each with a
// show function that renders its value or failure, a test per case that
// asserts the model's line, and a main that prints every line.
func teGenerate(seed int64) teProgram {
	g := &teGen{rng: rand.New(rand.NewSource(seed))}
	p := teProgram{seed: seed, wants: map[string]string{}, outcomes: map[string]int{}}
	var src, main, want strings.Builder
	src.WriteString(tePrelude)
	main.WriteString("\nfunc main() needs Console {\n")
	for k := 1; k <= teCases; k++ {
		var e *teNode
		var types map[string]bool
		var covered bool
		for {
			e = g.node(1 + g.rng.Intn(3))
			types = map[string]bool{}
			covered = e.tails(types)
			if len(types) > 0 {
				break
			}
		}
		var names []string
		for ty := range types {
			names = append(names, ty)
		}
		sort.Strings(names)
		// A partial catch handles a nonempty subset of the subject's
		// types and has something to pass on (Catch 5's S108);
		// otherwise the catch is total, inside an outer try's call
		// when a covered call needs one.
		arms := names
		partial := g.rng.Intn(3) != 0
		if partial && len(names) > 1 {
			arms = [][]string{{"A"}, {"B"}, names}[g.rng.Intn(3)]
		}
		if partial && !covered && len(arms) == len(names) {
			partial = false
		}
		var armText strings.Builder
		for _, a := range arms {
			fmt.Fprintf(&armText, "\tcase %s:\n\t\t%d\n", a, teArmValue[a])
		}
		v, f := e.eval(true, "root")
		outcome := "value"
		switch {
		case f == nil:
		case f.tail && slices.Contains(arms, f.typ):
			outcome = "caught-" + f.pos
			if f.literal {
				outcome = "caught-literal"
			}
			v, f = teArmValue[f.typ], nil
		case f.tail:
			outcome = "passed-unhandled"
		case !partial:
			outcome = "passed-total"
		default:
			outcome = "passed-" + f.pos
		}
		var body string
		switch {
		case partial:
			body = fmt.Sprintf("\tlet r = try %s catch {\n%s\t}\n", e.text(), armText.String())
		case covered:
			body = fmt.Sprintf("\tlet r = try p(%s catch {\n%s\t}, \"w\")\n", e.text(), armText.String())
			if f == nil {
				v++
			}
		default:
			body = fmt.Sprintf("\tlet r = %s catch {\n%s\t}\n", e.text(), armText.String())
		}
		p.outcomes[outcome]++
		fmt.Fprintf(&src, "\nfunc case%d(x int) int fails A | B {\n%s\treturn r\n}\n", k, body)
		fmt.Fprintf(&src, "\nfunc show%d(x int) string {\n\tlet r = case%d(x) catch {\n\tcase A as e:\n\t\treturn \"A \" + e.At\n\tcase B as e:\n\t\treturn \"B \" + e.At\n\t}\n\treturn \"\\(r)\"\n}\n", k, k)
		line := strconv.Itoa(v)
		if f != nil {
			line = f.typ + " " + f.tag
		}
		name := fmt.Sprintf("Case%d", k)
		p.wants[name] = line
		fmt.Fprintf(&src, "\ntest %s {\n\tassert show%d(3) == %q\n}\n", name, k, line)
		fmt.Fprintf(&main, "\tConsole.Println(show%d(3))\n", k)
		want.WriteString(line + "\n")
	}
	main.WriteString("}\n")
	src.WriteString(main.String())
	p.src, p.want = src.String(), want.String()
	return p
}
