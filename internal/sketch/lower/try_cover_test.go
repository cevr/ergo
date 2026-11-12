package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTryCoversToTheRight: q105. A try may start any operand; it adds no
// grouping, and it covers every call evaluated after it, to the end of the
// enclosing expression (an argument, an index or a parenthesis is one).
// Generated expressions over + - * == && ||, unary - and !, traced calls
// (fallible or not, each writing its tag), indexes, literals, parentheses
// and nested calls carry try at a random set of operands. The model in
// this file gives, for each:
//
//   - the verdict: S105 for a fallible call that no earlier try of its
//     enclosing expressions covers; S108 at each try that no failure
//     belongs to (a failure belongs to the nearest try that covers it);
//   - for an accepted case, the trace up to the first failure, and the
//     value.
//
// The checker's verdict is compared in process (the parse tree is
// syntax.TestTryAddsNoGrouping's), and the lowered Go of the accepted
// cases is type-checked (typecheckGo) and evaluated at check time
// (tryEvaluates). A failing seed prints the Ergo
// source, both outputs and the replay command; ERGO_TRY_SEED=<seed>
// replays one seed.
func TestTryCoversToTheRight(t *testing.T) {
	seeds := tryCoverSeeds(40)
	var programs []*lowered
	for _, seed := range seeds {
		if p := tryCoverSeed(t, seed); p != nil {
			programs = append(programs, p.lowered)
		}
	}
	typecheckGo(t, programs)
	t.Logf("verdicts: %v", tryVerdicts)
}

// tryVerdicts counts the model's verdicts, logged so a change of the
// generator that starves a verdict shows.
var tryVerdicts = map[string]int{}

// TestTryCoversToTheRightBuilt runs the accepted cases of the same seeds.
// It builds Go because the claim is the evaluation order of the lowered
// program: a covered call is hoisted into a temporary with its failure
// check, after the operands on its left and inside the branch of a && or
// ||, which only the running program shows (there is no in-process
// evaluator on this base).
func TestTryCoversToTheRightBuilt(t *testing.T) {
	needsBuild(t)
	var programs []*tryProgram
	var built []*lowered
	for _, seed := range tryCoverSeeds(40) {
		if p := tryCoverSeed(t, seed); p != nil {
			programs = append(programs, p)
			built = append(built, p.lowered)
		}
	}
	exes := buildAll(t, built, true)
	if exes == nil {
		return
	}
	for _, p := range programs {
		stdout, stderr, code := runExe(t, exes[p.lowered.name])
		if code != 0 || stdout != p.want {
			t.Errorf("seed %d: exit %d, stderr %q\n%s\nsource:\n%s\nreplay: ERGO_TRY_SEED=%d go test -run 'TestTryCoversToTheRight' ./internal/sketch/lower", p.seed, code, stderr, diffLines(stdout, p.want), p.lowered.src, p.seed)
		}
	}
}

func tryCoverSeeds(n int) []int64 {
	if s := os.Getenv("ERGO_TRY_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			panic(err)
		}
		return []int64{v}
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(7000 + i)
	}
	return out
}

const tryCoverCases = 40

// tryProgram is the program of a seed's accepted cases and its expected
// output.
type tryProgram struct {
	seed    int64
	lowered *lowered
	want    string
}

// tryCoverSeed checks one seed's cases against the model and lowers the
// accepted ones into one program.
func tryCoverSeed(t *testing.T, seed int64) *tryProgram {
	t.Helper()
	g := &tryGen{rng: rand.New(rand.NewSource(seed))}
	var src, main, want, pure strings.Builder
	src.WriteString(tryPrelude)
	pure.WriteString(tryPurePrelude)
	main.WriteString("\nfunc main() needs Console {\n\tlet xs = []int{10, 20, 30}\n")
	accepted := 0
	for k := 1; k <= tryCoverCases; k++ {
		root := g.root()
		text := render(root)
		fn := tryCaseFunc(k, root, text)
		replay := fmt.Sprintf("replay: ERGO_TRY_SEED=%d go test -run 'TestTryCoversToTheRight' ./internal/sketch/lower", seed)
		// The checker's verdict is the model's (the tree is
		// syntax.TestTryAddsNoGrouping's).
		m := model(root)
		file, err := syntax.Parse(tryPrelude + fn)
		if err != nil {
			t.Fatalf("seed %d case %d: %v\n%s\n%s", seed, k, err, fn, replay)
		}
		_, diags := check.Check("main", []*syntax.File{file}, nil)
		got := verdictOf(diags, len(tryPrelude)+strings.Index(fn, "let r = ")+len("let r = "))
		if got != m.verdict() {
			t.Fatalf("seed %d case %d:\nsource:\n%s\nchecker: %s\nmodel:   %s\ndiagnostics: %v\n%s", seed, k, fn, got, m.verdict(), diags, replay)
		}
		switch {
		case m.s105:
			tryVerdicts["S105"]++
		case len(m.redundant) > 0:
			tryVerdicts["S108"]++
		case strings.Contains(text[1:], "try "):
			tryVerdicts["accepted, a try past the start"]++
		default:
			tryVerdicts["accepted"]++
		}
		if m.s105 || len(m.redundant) > 0 {
			continue
		}
		accepted++
		src.WriteString(fn)
		in := &interp{}
		result := func() (r string) {
			defer func() {
				if p := recover(); p != nil {
					if _, ok := p.(errFailed); !ok {
						panic(p)
					}
					r = root.failText()
				}
			}()
			return root.text(root.eval(in))
		}()
		shown := result
		if in.failed != "" {
			shown = "failed at " + in.failed
		}
		pure.WriteString(tryEvalCase(k, root, strings.Replace(fn, " needs Console {", " {", 1), text, shown))
		fmt.Fprintf(&want, "case %d\n", k)
		for _, line := range in.trace {
			want.WriteString(line + "\n")
		}
		if in.failed != "" {
			fmt.Fprintf(&want, "failed at %s\n", in.failed)
		}
		want.WriteString(result + "\n")
		fmt.Fprintf(&main, "\tConsole.Println(%q)\n", fmt.Sprintf("case %d", k))
		fmt.Fprintf(&main, "\tlet r%d = case%d(xs) catch {\n\tcase Bad as e:\n\t\tConsole.Println(\"failed at \" + e.At)\n\t\t%s\n\t}\n", k, k, root.failText())
		fmt.Fprintf(&main, "\tConsole.Println(\"\\(r%d)\")\n", k)
	}
	if accepted == 0 {
		return nil
	}
	tryEvaluates(t, seed, pure.String())
	main.WriteString("}\n")
	src.WriteString(main.String())
	name := fmt.Sprintf("trycover%d", seed)
	o := mustLower(t, name, src.String())
	return &tryProgram{seed: seed, lowered: o, want: want.String()}
}

// tryEvaluates is the evaluator's leg (internal/sketch/eval, the same
// order): the seed's accepted cases, with pure calls, run at check time,
// and each test must pass. A case's shown value is the model's value, or
// "failed at" the tag of the model's first failure. Each case runs twice:
// as its own function, and as the argument of a fallible call that a catch
// handles: a failure that a try in the case covers still leaves the frame
// past the catch (q105; the catch handles the call's row). It kills eval's
// try1-eval-cover-ignored, which lets the catch take the failure of an
// operand that a try on its left covers.
func tryEvaluates(t *testing.T, seed int64, src string) {
	t.Helper()
	rep, err := evaluate(src)
	if err != nil {
		t.Fatalf("seed %d: the evaluator: %v\nsource:\n%s\nreplay: ERGO_TRY_SEED=%d go test -run 'TestTryCoversToTheRight$' ./internal/sketch/lower", seed, err, src, seed)
	}
	for _, r := range rep.Results {
		if r.Status != eval.Passed {
			t.Fatalf("seed %d: the evaluator: %s %s: %s at %d:%d\nsource:\n%s\nreplay: ERGO_TRY_SEED=%d go test -run 'TestTryCoversToTheRight$' ./internal/sketch/lower", seed, r.Name, r.Status, r.Reason, r.Site.Line, r.Site.Column, src, seed)
		}
	}
}

// tryEvalCase is case k of the evaluator's leg: fn (pure), the same case
// under a catch, and their tests.
func tryEvalCase(k int, root *tx, fn, text, shown string) string {
	result, pass, sentinel := "int", "pass", "-1"
	if root.isBool {
		result, pass, sentinel = "bool", "passb", "false"
	}
	var b strings.Builder
	b.WriteString(fn)
	fmt.Fprintf(&b, "\nfunc caught%d(xs []int) %s fails Bad {\n\tlet r = %s(%s) catch {\n\tcase Bad:\n\t\t%s\n\t}\n\treturn r\n}\n", k, result, pass, text, sentinel)
	for _, name := range []string{"case", "caught"} {
		fmt.Fprintf(&b, "\nfunc show%s%d(xs []int) string {\n\tlet r = %s%d(xs) catch {\n\tcase Bad as e:\n\t\treturn \"failed at \" + e.At\n\t}\n\treturn \"\\(r)\"\n}\n", name, k, name, k)
		fmt.Fprintf(&b, "\ntest %s%d {\n\tassert show%s%d([]int{10, 20, 30}) == %q\n}\n", strings.ToUpper(name[:1])+name[1:], k, name, k, shown)
	}
	return b.String()
}

const tryPurePrelude = `type Bad { At string }

func t(tag string, v int) int {
	return v
}

func f(tag string, v int, fail bool) int fails Bad {
	if fail {
		return Bad{At: tag}
	}
	return v
}

func tb(tag string, v bool) bool {
	return v
}

func fb(tag string, v bool, fail bool) bool fails Bad {
	if fail {
		return Bad{At: tag}
	}
	return v
}

func pass(v int) int fails Bad {
	if v == -12345 {
		return Bad{At: "pass"}
	}
	return v
}

func passb(v bool) bool fails Bad {
	if v && !v {
		return Bad{At: "pass"}
	}
	return v
}
`

func tryCaseFunc(k int, root *tx, text string) string {
	result := "int"
	if root.isBool {
		result = "bool"
	}
	return fmt.Sprintf("\nfunc case%d(xs []int) %s fails Bad needs Console {\n\tlet r = %s\n\treturn r\n}\n", k, result, text)
}

const tryPrelude = `type Bad { At string }

func t(tag string, v int) int needs Console {
	Console.Println(tag)
	return v
}

func f(tag string, v int, fail bool) int fails Bad needs Console {
	Console.Println(tag)
	if fail {
		return Bad{At: tag}
	}
	return v
}

func tb(tag string, v bool) bool needs Console {
	Console.Println(tag)
	return v
}

func fb(tag string, v bool, fail bool) bool fails Bad needs Console {
	Console.Println(tag)
	if fail {
		return Bad{At: tag}
	}
	return v
}
`

// tx is a node of the generated language.
type tx struct {
	kind   txKind
	isBool bool
	try    bool // a try starts this operand
	// lit
	n int
	b bool
	// call: t, f, tb or fb with one argument; an index's subscript
	tag            string
	fallible, fail bool
	// bin, unary, paren, call argument, index subscript
	op   string
	x, y *tx
	// pos is the byte offset of the try (render)
	tryAt int
}

type txKind int

const (
	txLit txKind = iota
	txCall
	txBin
	txUnary
	txParen
	txIndex
)

type tryGen struct {
	rng  *rand.Rand
	tags int
}

func (g *tryGen) tag() string {
	g.tags++
	return "e" + strconv.Itoa(g.tags)
}

func (g *tryGen) root() *tx {
	g.tags = 0
	return g.expr(g.rng.Intn(2) == 0, 2+g.rng.Intn(3))
}

var txPrec = map[string]int{"||": 1, "&&": 2, "==": 3, "+": 4, "-": 4, "*": 5}

func prec(n *tx) int {
	if n.kind == txBin {
		return txPrec[n.op]
	}
	return 9
}

// expr is an expression of the given type and depth; an operand of a
// binary expression is parenthesized where its precedence is looser, so
// the tree is the one the parser builds.
func (g *tryGen) expr(isBool bool, depth int) *tx {
	var n *tx
	if depth <= 0 || g.rng.Intn(4) == 0 {
		n = g.operand(isBool, depth)
	} else {
		var op string
		var operandBool bool
		if isBool {
			op = []string{"&&", "||", "=="}[g.rng.Intn(3)]
			operandBool = op != "=="
		} else {
			op = []string{"+", "-", "*"}[g.rng.Intn(3)]
		}
		x, y := g.expr(operandBool, depth-1), g.expr(operandBool, depth-1)
		if prec(x) < txPrec[op] {
			x = g.paren(x)
		}
		if prec(y) <= txPrec[op] {
			y = g.paren(y)
		}
		n = &tx{kind: txBin, isBool: isBool, op: op, x: x, y: y}
	}
	return n
}

func (g *tryGen) paren(x *tx) *tx {
	return g.tried(&tx{kind: txParen, isBool: x.isBool, x: x})
}

// tried puts a try before an operand, about one time in four.
func (g *tryGen) tried(n *tx) *tx {
	n.try = g.rng.Intn(4) == 0
	return n
}

func (g *tryGen) operand(isBool bool, depth int) *tx {
	switch r := g.rng.Intn(10); {
	case r < 2:
		if isBool {
			return g.tried(&tx{kind: txLit, isBool: true, b: g.rng.Intn(2) == 0})
		}
		return g.tried(&tx{kind: txLit, n: g.rng.Intn(10)})
	case r < 3 && depth > 0:
		x := g.operand(isBool, depth-1)
		for x.kind == txUnary {
			x = g.operand(isBool, depth-1) // no "--": one sign per operand
		}
		op := "-"
		if isBool {
			op = "!"
		}
		return g.tried(&tx{kind: txUnary, isBool: isBool, op: op, x: x})
	case r < 4 && depth > 0:
		return g.paren(g.expr(isBool, depth-1))
	case r < 5 && !isBool:
		sub := g.call(false, 0)
		sub.n = g.rng.Intn(3)
		sub.x = &tx{kind: txLit, n: sub.n}
		return g.tried(&tx{kind: txIndex, x: sub})
	}
	return g.call(isBool, depth-1)
}

// call is t, f, tb or fb: its argument is a literal or, past depth 0, an
// expression of its own.
func (g *tryGen) call(isBool bool, depth int) *tx {
	c := &tx{kind: txCall, isBool: isBool, tag: g.tag(), fallible: g.rng.Intn(2) == 0}
	c.fail = c.fallible && g.rng.Intn(3) == 0
	if depth > 0 && g.rng.Intn(3) == 0 {
		c.x = g.expr(isBool, depth-1)
	} else if isBool {
		c.x = &tx{kind: txLit, isBool: true, b: g.rng.Intn(2) == 0}
	} else {
		c.x = &tx{kind: txLit, n: g.rng.Intn(10)}
	}
	return g.tried(c)
}

// render writes n's Ergo text and records each try's offset in the case's
// expression.
func render(n *tx) string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n *tx) write(b *strings.Builder) {
	if n.try {
		n.tryAt = b.Len()
		b.WriteString("try ")
	}
	switch n.kind {
	case txLit:
		if n.isBool {
			b.WriteString(strconv.FormatBool(n.b))
		} else {
			b.WriteString(strconv.Itoa(n.n))
		}
	case txCall:
		name := "t"
		switch {
		case n.isBool && n.fallible:
			name = "fb"
		case n.isBool:
			name = "tb"
		case n.fallible:
			name = "f"
		}
		fmt.Fprintf(b, "%s(%q, ", name, n.tag)
		n.x.write(b)
		if n.fallible {
			fmt.Fprintf(b, ", %v", n.fail)
		}
		b.WriteString(")")
	case txBin:
		n.x.write(b)
		b.WriteString(" " + n.op + " ")
		n.y.write(b)
	case txUnary:
		b.WriteString(n.op)
		n.x.write(b)
	case txParen:
		b.WriteString("(")
		n.x.write(b)
		b.WriteString(")")
	case txIndex:
		b.WriteString("xs[")
		n.x.write(b)
		b.WriteString("]")
	}
}

// coverModel is the model's verdict on one case.
type coverModel struct {
	s105      bool
	redundant []int // offsets of the trys no failure belongs to
}

func (m coverModel) verdict() string {
	if m.s105 {
		return "S105" // an unhandled failure; S108s beside it are not compared
	}
	if len(m.redundant) == 0 {
		return "ok"
	}
	parts := make([]string, len(m.redundant))
	for i, at := range m.redundant {
		parts[i] = "S108@" + strconv.Itoa(at)
	}
	return strings.Join(parts, " ")
}

// model walks n in text order. Each enclosing expression (the case's
// expression, a parenthesis, an argument, a subscript) is a scope; a try
// covers what follows it in its scope, nested scopes included. A
// fallible call's failure belongs to the nearest try that covers it.
func model(n *tx) coverModel {
	owned := map[*tx]int{}
	var trys []*tx
	s105 := false
	var walk func(n *tx, scopes [][]*tx) [][]*tx
	walk = func(n *tx, scopes [][]*tx) [][]*tx {
		if n.try {
			trys = append(trys, n)
			last := len(scopes) - 1
			scopes[last] = append(slices.Clip(scopes[last]), n)
		}
		inner := func(x *tx) {
			walk(x, append(slices.Clip(scopes), nil))
		}
		switch n.kind {
		case txCall:
			inner(n.x)
			if n.fallible {
				var nearest *tx
				for i := len(scopes) - 1; i >= 0 && nearest == nil; i-- {
					if s := scopes[i]; len(s) > 0 {
						nearest = s[len(s)-1]
					}
				}
				if nearest == nil {
					s105 = true
				} else {
					owned[nearest]++
				}
			}
		case txBin:
			scopes = walk(n.x, scopes)
			scopes = walk(n.y, scopes)
		case txUnary:
			scopes = walk(n.x, scopes)
		case txParen, txIndex:
			inner(n.x)
		}
		return scopes
	}
	walk(n, [][]*tx{nil})
	m := coverModel{s105: s105}
	for _, t := range trys {
		if owned[t] == 0 {
			m.redundant = append(m.redundant, t.tryAt)
		}
	}
	slices.Sort(m.redundant)
	return m
}

// verdictOf is the checker's verdict in the model's terms; base is the
// offset of the case's return expression in the checked file.
func verdictOf(diags []check.Diagnostic, base int) string {
	var s108 []int
	for _, d := range diags {
		switch d.Code {
		case "S105":
			return "S105"
		case "S108":
			s108 = append(s108, d.Offset)
		default:
			return "unexpected " + d.String()
		}
	}
	if len(s108) == 0 {
		return "ok"
	}
	slices.Sort(s108)
	parts := make([]string, len(s108))
	for i, at := range s108 {
		parts[i] = "S108@" + strconv.Itoa(at-base)
	}
	return strings.Join(parts, " ")
}

// eval runs n: operands left to right, each once; the right operand of a
// decided && or || never. A failing call unwinds with errFailed.
func (n *tx) eval(in *interp) int {
	switch n.kind {
	case txLit:
		if n.isBool {
			return b2i(n.b)
		}
		return n.n
	case txCall:
		v := n.x.eval(in)
		in.trace = append(in.trace, n.tag)
		if n.fail {
			in.failed = n.tag
			panic(errFailed{})
		}
		return v
	case txBin:
		x := n.x.eval(in)
		switch n.op {
		case "&&":
			if x == 0 {
				return 0
			}
			return n.y.eval(in)
		case "||":
			if x == 1 {
				return 1
			}
			return n.y.eval(in)
		}
		y := n.y.eval(in)
		switch n.op {
		case "+":
			return x + y
		case "-":
			return x - y
		case "*":
			return x * y
		}
		return b2i(x == y)
	case txUnary:
		if n.op == "!" {
			return 1 - n.x.eval(in)
		}
		return -n.x.eval(in)
	case txParen:
		return n.x.eval(in)
	case txIndex:
		return []int{10, 20, 30}[n.x.eval(in)]
	}
	panic("tx kind")
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// text is the printed value; failText the catch's value, printed after
// "failed at".
func (n *tx) text(v int) string {
	if n.isBool {
		return strconv.FormatBool(v == 1)
	}
	return strconv.Itoa(v)
}

func (n *tx) failText() string {
	if n.isBool {
		return "false"
	}
	return "-1000"
}
