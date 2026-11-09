package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// TestExternValuesRun is G1's property (gx-extern-values): an extern used
// as a function value, and a user extern that takes a callback, behave as
// Go's own functions do. A seeded generator writes one program of cases:
//
//   - strings.Map through a user extern with a generated callback: rune
//     arithmetic (r + k, r - k, r * 2, nested), a drop (a negative rune,
//     which strings.Map skips) on one rune, and a loop inside the callback;
//   - strings.Map with a method value of an operation type, which reads
//     the caller's environment that the adapter captures;
//   - slices.Transform with a std extern value (strings.ToUpper, ToLower,
//     TrimSpace) and with a user extern value;
//   - a fallible std extern as a value (strconv.Atoi), caught.
//
// The reference is Go itself: the test evaluates each callback in Go
// (genRune.eval) and calls the Go functions on the same inputs. A failure
// prints the replay flag, the case's Ergo source and both outputs.
//
// It builds and runs the program because the unit's subject is the Go the
// lowering emits for a value and for a callback adapter: the checker and
// go/types see only the Ergo program, and only go build proves that the
// adapter has the plain Go shape the target wants, and only a run proves
// that the callback runs on its caller's environment. One program holds
// every case, so the test builds once.
var externValuesSeed = flag.Uint64("extern.seed", 0, "run TestExternValuesRun on this seed alone")

func TestExternValuesRun(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if *externValuesSeed != 0 {
		seeds = []uint64{*externValuesSeed}
	}
	var cases []externCase
	names := 0 // one counter for every seed: a local name is used once
	for _, seed := range seeds {
		g := &externGen{r: rand.New(rand.NewPCG(seed, 0x6731)), seed: seed, n: &names}
		for i := 0; i < 12; i++ {
			cases = append(cases, g.mapCase(), g.transformCase(), g.atoiCase(), g.methodCase())
		}
	}
	var b strings.Builder
	b.WriteString("import slices\nimport strings\nimport strconv\n\n")
	b.WriteString("extern go strings.Map func Mapper(f func(r rune) rune, s string) string\n")
	b.WriteString("extern go strings.ToUpper func Upper(s string) string\n\n")
	b.WriteString("type Shift { Next(r rune) rune }\n\ntype One {}\n\nfunc Next(o One, r rune) rune { r + 1 }\n\n")
	b.WriteString("func main() needs Console {\n\tlet shift Shift = One\n")
	for i, c := range cases {
		fmt.Fprintf(&b, "\t// case %d (seed %d)\n%s", i, c.seed, c.ergo)
	}
	b.WriteString("}\n")
	src := b.String()
	prog := mustLower(t, "extern_values", src)
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["extern_values"])
	if code != 0 {
		t.Fatalf("exit %d, stderr %s\n%s", code, firstLines(stderr, 8), src)
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("%d lines for %d cases:\n%s", len(got), len(cases), stdout)
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("case %d: got %q, Go gives %q; replay with -extern.seed=%d\n%s", i, got[i], c.want, c.seed, c.ergo)
		}
	}
}

// externCase is one generated case: its Ergo statements, which print one
// line, and the line Go's own functions give.
type externCase struct {
	seed       uint64
	ergo, want string
}

type externGen struct {
	r    *rand.Rand
	seed uint64
	n    *int
}

// texts are the runes generated strings are made of: ASCII, two- three-
// and four-byte UTF-8, and a space.
var externRunes = []string{"a", "b", "z", "A", " ", "é", "日", "😀", "7"}

func (g *externGen) text() string {
	var b strings.Builder
	for n := g.r.IntN(7); n > 0; n-- {
		b.WriteString(externRunes[g.r.IntN(len(externRunes))])
	}
	return b.String()
}

// genRune is a generated rune expression over r.
type genRune struct {
	op   byte // 'r', '+', '-', '*'
	k    int32
	kid  *genRune
	loop int32 // the callback adds 1 this many times in a loop
}

func (g *externGen) runeExpr(depth int) *genRune {
	if depth == 0 || g.r.IntN(3) == 0 {
		return &genRune{op: 'r'}
	}
	ops := []byte{'+', '-', '*'}
	return &genRune{op: ops[g.r.IntN(len(ops))], k: int32(g.r.IntN(5)), kid: g.runeExpr(depth - 1)}
}

func (e *genRune) ergo() string {
	switch e.op {
	case '+':
		return fmt.Sprintf("(%s + %d)", e.kid.ergo(), e.k)
	case '-':
		return fmt.Sprintf("(%s - %d)", e.kid.ergo(), e.k)
	case '*':
		return fmt.Sprintf("(%s * 2)", e.kid.ergo())
	}
	return "r"
}

func (e *genRune) eval(r rune) rune {
	switch e.op {
	case '+':
		return e.kid.eval(r) + e.k
	case '-':
		return e.kid.eval(r) - e.k
	case '*':
		return e.kid.eval(r) * 2
	}
	return r
}

// mapCase is strings.Map through the user extern Mapper, with a callback
// that may drop one rune and may loop.
func (g *externGen) mapCase() externCase {
	e := g.runeExpr(3)
	drop := []rune(externRunes[g.r.IntN(len(externRunes))])[0]
	loop := int32(g.r.IntN(3))
	s := g.text()
	*g.n++
	v := fmt.Sprintf("acc%d", *g.n)
	body := fmt.Sprintf("let mut %s = %s\n\t\tfor i in 0..%d {\n\t\t\t_ = i\n\t\t\t%s = %s + 1\n\t\t}\n\t\tif r == %d { -1 } else { %s }", v, e.ergo(), loop, v, v, drop, v)
	ergo := fmt.Sprintf("\tConsole.Println(Mapper(func(r rune) rune {\n\t\t%s\n\t}, %s))\n", body, strconv.Quote(s))
	want := strings.Map(func(r rune) rune {
		if r == drop {
			return -1
		}
		return e.eval(r) + loop
	}, s)
	return externCase{seed: g.seed, ergo: ergo, want: want}
}

// transformCase passes an extern as a value to slices.Transform: a std
// extern, or the user extern Upper.
func (g *externGen) transformCase() externCase {
	fns := []struct {
		ergo string
		goFn func(string) string
	}{
		{"strings.ToUpper", strings.ToUpper},
		{"strings.ToLower", strings.ToLower},
		{"strings.TrimSpace", strings.TrimSpace},
		{"Upper", strings.ToUpper},
	}
	f := fns[g.r.IntN(len(fns))]
	var lits, want []string
	for n := 1 + g.r.IntN(3); n > 0; n-- {
		s := g.text()
		lits = append(lits, strconv.Quote(s))
		want = append(want, f.goFn(s))
	}
	ergo := fmt.Sprintf("\tConsole.Println(strings.Join(slices.Transform([]string{%s}, %s), \"|\"))\n", strings.Join(lits, ", "), f.ergo)
	return externCase{seed: g.seed, ergo: ergo, want: strings.Join(want, "|")}
}

// methodCase passes a method value of the program's operation type Shift
// as strings.Map's callback. Its Go form calls the operation with the task
// of the environment it is called with, so it reads the environment the
// adapter passes: the one callback whose result depends on that binding
// (a row-free literal is pure and reads none).
func (g *externGen) methodCase() externCase {
	s := g.text()
	ergo := fmt.Sprintf("\tConsole.Println(Mapper(shift.Next, %s))\n", strconv.Quote(s))
	return externCase{seed: g.seed, ergo: ergo, want: strings.Map(func(r rune) rune { return r + 1 }, s)}
}

// atoiCase calls a fallible std extern through a value and catches it.
func (g *externGen) atoiCase() externCase {
	inputs := []string{"0", "42", "-7", "x", "", "99999999999999999999", "1e3"}
	s := inputs[g.r.IntN(len(inputs))]
	*g.n++
	v := fmt.Sprintf("atoi%d", *g.n)
	ergo := fmt.Sprintf("\tlet %s = strconv.Atoi\n\tlet n%d = %s(%s) catch {\n\tcase _:\n\t\t-1\n\t}\n\tConsole.Println(\"\\(n%d)\")\n", v, *g.n, v, strconv.Quote(s), *g.n)
	n, err := strconv.Atoi(s)
	if err != nil {
		n = -1
	}
	return externCase{seed: g.seed, ergo: ergo, want: strconv.Itoa(n)}
}
