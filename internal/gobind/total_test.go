package gobind_test

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// totalForms are bodies of `func F(a, b int64, u uint64) int64` and
// whether the binder proves them total (total.go): a form it proves
// cannot panic by Go's semantics; a refused form may be safe too (an
// index, a string conversion), the rule being narrow.
var totalForms = []struct {
	name, body string
	total      bool
}{
	{"Arith", "return a + b*3 - (a^b)&^5 | -a", true},
	{"DivConst", "return a / 3 % -7", true},
	{"DivVar", "return a / b", false},
	{"RemVar", "return a % b", false},
	{"DivFloat", "return int64(float64(a) / float64(b))", true},
	{"ShiftConst", "return a<<3 | a>>70", true},
	{"ShiftUnsigned", "return a << u", true},
	{"ShiftSigned", "return a << b", false},
	{"Convert", "return int64(int32(a)) + int64(uint8(u)) + int64(Code(b))", true},
	{"ConvertString", "return int64(len(string(rune(a))))", false},
	{"Index", "return tab[a&3]", false},
	{"Composite", "x := [2]int64{a, b}\n\treturn x[0]", false},
	{"Control", "s := a\n\tfor i := 0; i < 4; i++ {\n\t\ts += b\n\t}\n\tswitch {\n\tcase s > 0:\n\t\treturn s\n\t}\n\treturn -s", true},
	{"LoopOpen", "for a > 0 {\n\t\ta /= 2\n\t}\n\treturn a", true}, // non-termination is no panic
	{"RangeInt", "s := int64(0)\n\tfor i := range 8 {\n\t\ts += int64(i)\n\t}\n\treturn s", true},
	{"RangeSlice", "for _, x := range []int64{a} {\n\t\treturn x\n\t}\n\treturn 0", false},
	{"Panic", "if a > 1 {\n\t\tpanic(\"x\")\n\t}\n\treturn a", false},
	{"CallTotal", "return helper(a)", true},
	{"CallRisky", "return risky(a, b)", false},
	{"CallRecursive", "return rec(a)", false},
	{"CallOther", "return int64(bits.OnesCount64(u))", false},
	{"CallGeneric", "return gen(a)", false},
	{"CallValue", "f := helper\n\treturn f(a)", false},
	{"FuncValue", "f := helper\n\t_ = f\n\treturn a", false}, // a function value is refused, called or not
	{"Closure", "f := func() int64 { return a }\n\treturn f()", false},
	{"GlobalRead", "return global + a", true},
	{"GlobalWrite", "global = a\n\treturn a", false},
	{"Deref", "p := &a\n\treturn *p", false},
	{"Defer", "defer helper(a)\n\treturn a", false},
	{"Goto", "goto end\nend:\n\treturn a", false},
	{"MinMax", "return min(a, b, 3) + max(a, 1)", true},
	{"LenString", "s := \"abc\"\n\treturn int64(len(s))", true},
	{"Assert", "var x any = a\n\treturn x.(int64)", false},
	{"Map", "m := map[int64]int64{}\n\tm[a] = b\n\treturn m[a]", false},
	{"MethodValue", "return V(a).Double()", true},
	{"MethodPointer", "w := W(a)\n\treturn w.Get()", false},
	{"MethodRisky", "return V(a).Halve(b)", false},
	{"MethodPointerZero", "w := W(a)\n\treturn w.Zero()", false}, // a pointer receiver is refused
	{"GlobalPointer", "p := gp\n\t_ = p\n\treturn a", false},     // only a basic package variable is read
	{"RangeVar", "var xs []int64\n\tfor range xs {\n\t\ta++\n\t}\n\treturn a", false},
	{"LenSlice", "var xs []int64\n\treturn int64(len(xs))", false},
}

const totalPrelude = `type Code int16

var tab [4]int64

var global int64

func helper(x int64) int64 { return x*2 + 1 }

func risky(x, y int64) int64 { return x / y }

func rec(x int64) int64 {
	if x > 0 {
		return rec(x - 1)
	}
	return 0
}

func gen[T int64](x T) T { return x }

// V's methods take a value receiver, W's a pointer one.
type V int64

func (v V) Double() int64 { return int64(v) * 2 }

func (v V) Halve(y int64) int64 { return int64(v) / y }

type W int64

func (w *W) Get() int64 { return int64(*w) }

func (w *W) Zero() int64 { return 0 }

var gp *int64

var _ = bits.Len
`

// TestTotalProperty binds, on every platform with ergo bind's own path,
// a package of the fixed forms above and packages of generated
// functions of integer operators, divisions and shifts by constants and
// variables, conversions, table indexes, panic guards, bounded loops and
// calls between them. Each generated function runs, in this process, as
// the same Go operations on edge and random inputs: one that panics on any
// input must not be total (a total line on a panicking function is a
// miscompile, GO-NOPANIC), and a function binds total exactly where the
// model, built beside the source, says its parts are all total.
// ERGO_TOTAL_SEED replays one seed; a failure prints the Go source.
//
// Mutants it alone catches: the gobind-total-* rows (refused forms, the
// least fixed point, platforms).
func TestTotalProperty(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_TOTAL_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_TOTAL_SEED: %v", err)
		}
		seeds = []uint64{n}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.test/total\n\ngo 1.27\n")
	var forms strings.Builder
	forms.WriteString("package forms\n\nimport \"math/bits\"\n\n" + totalPrelude)
	want := map[string]bool{"V.Double": true, "V.Halve": false, "W.Get": false, "W.Zero": false}
	for _, f := range totalForms {
		fmt.Fprintf(&forms, "\nfunc %s(a, b int64, u uint64) int64 {\n\t%s\n}\n", f.name, f.body)
		want[f.name] = f.total
	}
	write(t, filepath.Join(dir, "forms", "forms.go"), forms.String())
	patterns := []string{"./forms"}
	type genPkg struct {
		path, src string
		fns       []*genFn
	}
	var gens []genPkg
	for _, seed := range seeds {
		name := fmt.Sprintf("g%d", seed)
		src, fns := genTotal(name, rand.New(rand.NewPCG(seed, 0x707a1)))
		write(t, filepath.Join(dir, name, name+".go"), src)
		gens = append(gens, genPkg{"example.test/total/" + name, src, fns})
		patterns = append(patterns, "./"+name)
	}
	files, err := gobind.Bind(dir, os.Environ(), "go1.27", patterns...)
	if err != nil {
		t.Fatal(err)
	}
	got := totalLines("example.test/total/forms", files["example.test/total/forms"])
	var bad []string
	for name, w := range want {
		if got[name] != w {
			bad = append(bad, fmt.Sprintf("%s: total %v, want %v", name, got[name], w))
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		t.Errorf("forms:\n%s\n--- binding:\n%s", strings.Join(bad, "\n"), files["example.test/total/forms"])
	}
	var proven, panicked int
	for _, g := range gens {
		got := totalLines(g.path, files[g.path])
		var bad []string
		for _, fn := range g.fns {
			p := fn.panics()
			if p {
				panicked++
			}
			if got[fn.name] {
				proven++
			}
			switch {
			case got[fn.name] && p:
				bad = append(bad, fn.name+": total, but it panics (a miscompile)")
			case got[fn.name] != fn.total:
				bad = append(bad, fmt.Sprintf("%s: total %v, the model says %v", fn.name, got[fn.name], fn.total))
			}
		}
		if len(bad) > 0 {
			t.Errorf("seed package %s (replay: ERGO_TOTAL_SEED=%s):\n%s\n--- Go source:\n%s", g.path, strings.TrimPrefix(filepath.Base(g.path), "g"), strings.Join(bad, "\n"), g.src)
		}
	}
	if len(seeds) > 1 && (proven == 0 || panicked == 0) {
		t.Fatalf("the seeds prove %d functions total and run %d that panic: the property needs both", proven, panicked)
	}
}

// totalLines reads, by binding name (F, or T.M), the functions whose
// extern carries an `// ergo:total` line.
func totalLines(path, text string) map[string]bool {
	out := map[string]bool{}
	marked := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "// ergo:go "):
			marked = false
		case line == "// ergo:total":
			marked = true
		case strings.HasPrefix(line, "extern go "):
			name := strings.TrimPrefix(strings.Fields(line)[2], path+".")
			out[name] = marked
			marked = false
		}
	}
	return out
}

// genEval is a generated expression's value on the function's operands.
type genEval func(a, b int64, u uint64) int64

// genFn is one generated function: its source name, its run, and the
// model's verdict.
type genFn struct {
	name  string
	eval  genEval
	total bool
}

// panics runs fn on edge and random operands and reports a panic.
func (fn *genFn) panics() bool {
	edges := []int64{0, 1, -1, 2, 7, 8, -9, math.MinInt64, math.MaxInt64}
	uedges := []uint64{0, 1, 63, 64, math.MaxUint64}
	r := rand.New(rand.NewPCG(9, 9))
	run := func(a, b int64, u uint64) (p bool) {
		defer func() { p = recover() != nil }()
		fn.eval(a, b, u)
		return false
	}
	for _, a := range edges {
		for _, b := range edges {
			for _, u := range uedges {
				if run(a, b, u) {
					return true
				}
			}
		}
	}
	for range 200 {
		if run(r.Int64(), r.Int64(), r.Uint64()) {
			return true
		}
	}
	return false
}

// genTab is the table generated code indexes; an index outside it panics.
var genTab [8]int64

// totalGen writes one package's functions.
type totalGen struct {
	r   *rand.Rand
	fns []*genFn
}

// expr is an expression's source, its run, whether it is a constant (Go
// folds constants and refuses a constant overflow or a constant zero
// divisor, so the generator keeps constants at the leaves), and whether
// the model holds it total.
func (g *totalGen) expr(d int) (src string, eval genEval, isConst, total bool) {
	leaf := func() (string, genEval, bool, bool) {
		switch g.r.IntN(4) {
		case 0:
			return "a", func(a, _ int64, _ uint64) int64 { return a }, false, true
		case 1:
			return "b", func(_, b int64, _ uint64) int64 { return b }, false, true
		case 2:
			return "int64(u)", func(_, _ int64, u uint64) int64 { return int64(u) }, false, true
		}
		c := int64(g.r.IntN(19) - 9)
		return strconv.FormatInt(c, 10), func(int64, int64, uint64) int64 { return c }, true, true
	}
	if d == 0 || g.r.IntN(4) == 0 {
		return leaf()
	}
	// nonConst is a sub-expression that is no constant.
	nonConst := func() (string, genEval, bool) {
		s, e, c, ok := g.expr(d - 1)
		if c {
			return "a", func(a, _ int64, _ uint64) int64 { return a }, true
		}
		return s, e, ok
	}
	switch g.r.IntN(10) {
	case 0, 1: // + - * & | ^ &^
		x, ex, cx, tx := g.expr(d - 1)
		y, ey, ty := nonConst()
		_ = cx
		ops := []string{"+", "-", "*", "&", "|", "^", "&^"}
		op := ops[g.r.IntN(len(ops))]
		f := map[string]func(x, y int64) int64{
			"+": func(x, y int64) int64 { return x + y }, "-": func(x, y int64) int64 { return x - y },
			"*": func(x, y int64) int64 { return x * y }, "&": func(x, y int64) int64 { return x & y },
			"|": func(x, y int64) int64 { return x | y }, "^": func(x, y int64) int64 { return x ^ y },
			"&^": func(x, y int64) int64 { return x &^ y },
		}[op]
		return "(" + x + " " + op + " " + y + ")", func(a, b int64, u uint64) int64 { return f(ex(a, b, u), ey(a, b, u)) }, false, tx && ty
	case 2: // / or % by a non-zero constant
		x, ex, tx := nonConst()
		c := int64([]int{1, 2, 3, 7, -1, -5}[g.r.IntN(6)])
		if g.r.IntN(2) == 0 {
			return fmt.Sprintf("(%s / %d)", x, c), func(a, b int64, u uint64) int64 { return ex(a, b, u) / c }, false, tx
		}
		return fmt.Sprintf("(%s %% %d)", x, c), func(a, b int64, u uint64) int64 { return ex(a, b, u) % c }, false, tx
	case 3: // / or % by a variable
		x, ex, _, _ := g.expr(d - 1)
		y, ey, _ := nonConst()
		if g.r.IntN(2) == 0 {
			return "(" + x + " / " + y + ")", func(a, b int64, u uint64) int64 { return ex(a, b, u) / ey(a, b, u) }, false, false
		}
		return "(" + x + " % " + y + ")", func(a, b int64, u uint64) int64 { return ex(a, b, u) % ey(a, b, u) }, false, false
	case 4: // a shift by a constant
		x, ex, tx := nonConst()
		k := uint(g.r.IntN(70))
		return fmt.Sprintf("(%s << %d)", x, k), func(a, b int64, u uint64) int64 { return ex(a, b, u) << k }, false, tx
	case 5: // a shift by an unsigned count
		x, ex, tx := nonConst()
		y, ey, ty := nonConst()
		return "(" + x + " >> uint64(" + y + "))", func(a, b int64, u uint64) int64 { return ex(a, b, u) >> uint64(ey(a, b, u)) }, false, tx && ty
	case 6: // a shift by a signed count: a negative one panics
		x, ex, _ := nonConst()
		y, ey, _ := nonConst()
		return "(" + x + " << " + y + ")", func(a, b int64, u uint64) int64 { return ex(a, b, u) << ey(a, b, u) }, false, false
	case 7: // a conversion
		x, ex, tx := nonConst()
		if g.r.IntN(2) == 0 {
			return "int64(int8(" + x + "))", func(a, b int64, u uint64) int64 { return int64(int8(ex(a, b, u))) }, false, tx
		}
		return "int64(uint32(" + x + "))", func(a, b int64, u uint64) int64 { return int64(uint32(ex(a, b, u))) }, false, tx
	case 8: // an index: out of the table it panics
		x, ex, _ := nonConst()
		return "tab[" + x + "%9]", func(a, b int64, u uint64) int64 { return genTab[ex(a, b, u)%9] }, false, false
	}
	// A call of an earlier function, or a negation.
	if len(g.fns) > 0 {
		fn := g.fns[g.r.IntN(len(g.fns))]
		x, ex, tx := nonConst()
		y, ey, ty := nonConst()
		return fn.name + "(" + x + ", " + y + ", u)", func(a, b int64, u uint64) int64 { return fn.eval(ex(a, b, u), ey(a, b, u), u) }, false, fn.total && tx && ty
	}
	x, ex, tx := nonConst()
	return "-" + x, func(a, b int64, u uint64) int64 { return -ex(a, b, u) }, false, tx
}

// genTotal writes a package of functions F0… and their models. Each body
// may guard an early return or a panic, run a bounded loop, and returns
// an expression.
func genTotal(name string, r *rand.Rand) (string, []*genFn) {
	g := &totalGen{r: r}
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nvar tab [8]int64\n", name)
	for i := range 24 {
		fn := &genFn{name: fmt.Sprintf("F%d", i), total: true}
		var body []string
		var steps []func(a, b int64, u uint64) (int64, int64, bool)
		if r.IntN(3) == 0 { // a guarded early return
			c, ec, _, tc := g.expr(2)
			v, ev, _, tv := g.expr(2)
			body = append(body, fmt.Sprintf("if %s > 0 {\n\t\treturn %s\n\t}", c, v))
			fn.total = fn.total && tc && tv
			steps = append(steps, func(a, b int64, u uint64) (int64, int64, bool) {
				if ec(a, b, u) > 0 {
					return ev(a, b, u), b, true
				}
				return a, b, false
			})
		}
		if r.IntN(5) == 0 { // a panic guard
			c := int64(r.IntN(3) - 1)
			body = append(body, fmt.Sprintf("if a == %d {\n\t\tpanic(\"guard\")\n\t}", c))
			fn.total = false
			steps = append(steps, func(a, b int64, u uint64) (int64, int64, bool) {
				if a == c {
					panic("guard")
				}
				return a, b, false
			})
		}
		if r.IntN(3) == 0 { // a bounded loop
			n := r.IntN(4) + 1
			e, ee, _, te := g.expr(2)
			body = append(body, fmt.Sprintf("s := int64(0)\n\tfor k := 0; k < %d; k++ {\n\t\ts += %s\n\t}\n\ta += s", n, e))
			fn.total = fn.total && te
			steps = append(steps, func(a, b int64, u uint64) (int64, int64, bool) {
				s := int64(0)
				for k := 0; k < n; k++ {
					s += ee(a, b, u)
				}
				return a + s, b, false
			})
		}
		e, ee, _, te := g.expr(3)
		body = append(body, "return "+e)
		fn.total = fn.total && te
		fn.eval = func(a, b int64, u uint64) int64 {
			for _, s := range steps {
				v, nb, done := s(a, b, u)
				if done {
					return v
				}
				a, b = v, nb
			}
			return ee(a, b, u)
		}
		fmt.Fprintf(&b, "\nfunc %s(a, b int64, u uint64) int64 {\n\t%s\n}\n", fn.name, strings.Join(body, "\n\t"))
		g.fns = append(g.fns, fn)
	}
	return b.String(), g.fns
}
