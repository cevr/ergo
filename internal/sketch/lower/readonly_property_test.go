package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of read-only uses (readonly.go). Each seed writes a
// program of functions over a store {s []int, out [][]int, m Map[int,
// []int]}, read through a borrow (r *ro) or a let mut local. Each function
// takes a call's result that shares the store (a pure Ergo call's result,
// part(*r); a pure std body's, slices.Slice(r.s, 0, 2); a generic result
// through a borrow, deref(&r.s); a Go call's result; a struct, wrap(*r),
// and a named slice, nums(*r)) into a read-only context (len, an index
// read, an index read under len, a comparison, a field read, a for loop
// whose body writes only a local, a call or a method call that
// returns a basic value or a result that does not depend on it (lens,
// beside a fresh slice)), then writes the store and returns the value
// read with a digest of the store. The model: the lowering copies
// nothing in such a function. TestReadOnlyUsesCopyNothing counts the
// copies in each function's Go.
//
// The adversaries (agreement only) are uses that are not read at once: an
// index or a comparison operand that writes the store (part(*r)[bump(r)],
// part(*r) == grow(r)), an element or a field kept past a write (let e =
// outs(*r)[0], let e = wrap(*r).xs, k = o in a loop), a call whose later
// operand writes the store (part2(*r, bumpv(r))[0], total2(part(*r),
// bumpv(r))) or that writes it (total3(part(*r), r), nums(*r).Sum3(r)), a call that keeps
// its operand (ident(part(*r))), a loop body that writes the store, calls
// or returns an element (firsto), and & of an element
// (setv(&part(*r)[1]), setv(&wrap(*r).xs[1])).
// TestReadOnlyUsesAgree builds every program under the rules and under
// the reference (copyLowering "whole", which copies each such result) and
// holds their outputs equal.
//
// Replay: go test -run '^TestReadOnlyUses' ./internal/sketch/lower -args -copy.seed=N

const roPrelude = `import slices
import go strings

type ro { s []int, out [][]int, m Map[int, []int] }

func newro() ro { ro{s: []int{1, 2, 3}, out: [][]int{[]int{4}}, m: Map[int, []int]{1: []int{1}}} }

func dro(r ro) int {
	let mut t = len(r.m) * 1000 + len(r.out) * 100
	for x in r.s {
		t = t * 3 + x
	}
	for o in r.out {
		for x in o {
			t = t * 5 + x
		}
	}
	for k, v in r.m {
		t = t + k * 7 + len(v)
	}
	t
}

func part(r ro) []int { r.s }

func part2(r ro, k int) []int { r.s }

func outs(r ro) [][]int { r.out }

func tab(r ro) Map[int, []int] { r.m }

func deref[T](p mut T) T { *p }

type bx { xs []int }

type Nums []int

func wrap(r ro) bx { bx{xs: r.s} }

func nums(r ro) Nums { Nums(r.s) }

func total(xs []int) int {
	let mut t = 0
	for x in xs {
		t = t * 3 + x
	}
	t
}

func Sum(n Nums) int {
	let mut t = 1
	for x in n {
		t = t * 7 + x
	}
	t
}

func glen(xs []string) int { len(xs) * 10 + len(xs[0]) }

func total2(xs []int, k int) int { total(xs) + k }

func total3(xs []int, r mut ro) int {
	r.s[1] = 70
	total(xs)
}

func Sum3(n Nums, r mut ro) int {
	r.s[1] = 70
	Sum(n)
}

func ident(xs []int) []int { xs }

func firsto(r mut ro) []int {
	for o in outs(*r) {
		return o
	}
	[]int{0}
}

func bump(r mut ro) int {
	r.s[1] = 70
	1
}

func grow(r mut ro) []int {
	r.s[0] = 50
	[]int{50, 2, 3}
}

func bumpv(r mut ro) int {
	r.s[0] = 50
	0
}

func setv(p mut int) int {
	*p = 4
	0
}

func lens(xs []int, out []int) []int { out }

`

// roProducer is a call whose result shares the store: its Ergo text over
// the root R (*r or r) and its result's kind ("s" []int, "o" [][]int,
// "m" a Map, "g" []string).
type roProducer struct {
	name, text, kind string
}

var roProducers = []roProducer{
	{"pure", "part(R)", "s"},
	{"pure nested", "outs(R)", "o"},
	{"pure map", "tab(R)", "m"},
	{"std", "slices.Slice(r.s, 0, 2)", "s"},
	{"generic borrow", "deref(&r.s)", "s"},
	{"go", "strings.SplitAfterN(\"a,b\", \",\", -1)", "g"},
	{"pure struct", "wrap(R)", "w"},
	{"pure named", "nums(R)", "n"},
}

// roLoop sums a slice X in a for loop whose body writes only n.
const roLoop = "let mut n = 0\n\tfor v in X {\n\t\tn = n * 3 + v\n\t}"

// roContexts are the read-only contexts of a result X of each kind.
var roContexts = map[string][][2]string{
	"s": {{"len", "len(X)"}, {"index", "X[1]"}, {"compare", "if X == []int{1, 2, 3} { 1 } else { 2 }"}, {"loop", roLoop}, {"call", "total(X)"}, {"call out", "len(lens(X, []int{1}))"}},
	"o": {{"len", "len(X)"}, {"index len", "len(X[0])"}, {"compare", "if X == [][]int{[]int{4}} { 1 } else { 2 }"}, {"loop len", "let mut n = 0\n\tfor o in X {\n\t\tn = n + len(o)\n\t}"}},
	"m": {{"len", "len(X)"}, {"loop", "let mut n = 0\n\tfor k, v in X {\n\t\tn = n + k + len(v)\n\t}"}},
	"g": {{"len", "len(X)"}, {"index len", "len(X[0])"}, {"compare", "if X == []string{\"a,\", \"b\"} { 1 } else { 2 }"}, {"call", "glen(X)"}},
	"w": {{"field len", "len(X.xs)"}, {"field index", "X.xs[1]"}, {"field loop", strings.ReplaceAll(roLoop, "X", "X.xs")}, {"field call", "total(X.xs)"}},
	"n": {{"method", "X.Sum()"}, {"call", "Sum(X)"}, {"loop", roLoop}},
}

var roAdversaries = []struct{ name, value string }{
	{"index call", "part(*r)[bump(r)]"},
	{"compare call", "if part(*r) == grow(r) { 1 } else { 2 }"},
	{"kept index", "let e = outs(*r)[0]\n\tr.out[0][0] = 7\n\tlet n = e[0]"},
	{"in flight", "part2(*r, bumpv(r))[0]"},
	{"address", "let z = setv(&part(*r)[1])\n\tlet n = z + r.s[1]"},
	{"loop write", "let mut n = 0\n\tfor v in part(*r) {\n\t\tr.s[1] = 5\n\t\tn = n * 3 + v\n\t}"},
	{"loop call", "let mut n = 0\n\tfor v in part(*r) {\n\t\tn = n * 3 + v + bump(r)\n\t}"},
	{"loop keeps", "let mut k = []int{}\n\tfor o in outs(*r) {\n\t\tk = o\n\t}\n\tr.out[0][0] = 7\n\tlet n = k[0]"},
	{"call operand", "total2(part(*r), bumpv(r))"},
	{"impure call", "total3(part(*r), r)"},
	{"impure method", "nums(*r).Sum3(r)"},
	{"call result kept", "let e = ident(part(*r))\n\tr.s[1] = 7\n\tlet n = e[1]"},
	{"loop return", "let e = firsto(r)\n\tr.out[0][0] = 7\n\tlet n = e[0]"},
	{"kept field", "let e = wrap(*r).xs\n\tr.s[1] = 7\n\tlet n = e[1]"},
	{"field address", "let z = setv(&wrap(*r).xs[1])\n\tlet n = z + r.s[1]"},
}

// roProgram writes the program of seed: every producer, context and
// access, and every adversary, in the seed's order. model maps each
// function to the copies its Go makes, -1 for an adversary.
func roProgram(seed uint64) (src string, model map[string]int, order []string) {
	r := rand.New(rand.NewPCG(seed, 0x726f))
	var fns []string
	model = map[string]int{}
	add := func(value string, ptr bool, want int) {
		name := fmt.Sprintf("f%d", len(fns)+1)
		head := fmt.Sprintf("func %s(r mut ro) int {\n", name)
		digest := "dro(*r)"
		if !ptr {
			head = fmt.Sprintf("func %s() int {\n\tlet mut r = newro()\n", name)
			digest = "dro(r)"
		}
		stmt := "\tlet n = " + value
		if strings.HasPrefix(value, "let ") {
			stmt = "\t" + value
		}
		fn := head + stmt + "\n\tr.s[0] = 9\n\tr.out[0][0] = 8\n\tr.m[1] = []int{6, 6}\n\tn * 100000 + " + digest + "\n}\n"
		fns = append(fns, fn)
		model[name] = want
	}
	for _, p := range roProducers {
		for _, c := range roContexts[p.kind] {
			for _, ptr := range []bool{true, false} {
				root := "*r"
				if !ptr {
					root = "r"
				}
				add(strings.ReplaceAll(c[1], "X", strings.ReplaceAll(p.text, "R", root)), ptr, 0)
			}
		}
	}
	for _, a := range roAdversaries {
		add(a.value, true, -1)
	}
	perm := r.Perm(len(fns))
	var b strings.Builder
	b.WriteString(roPrelude)
	var main strings.Builder
	for _, i := range perm {
		name := fmt.Sprintf("f%d", i+1)
		b.WriteString(fns[i] + "\n")
		order = append(order, name)
		if strings.HasPrefix(fns[i], "func "+name+"(r mut ro)") {
			main.WriteString(fmt.Sprintf("\tlet mut r%d = newro()\n\tConsole.Println(\"\\(%s(&r%d)) \\(dro(r%d))\")\n", i+1, name, i+1, i+1))
		} else {
			main.WriteString(fmt.Sprintf("\tConsole.Println(\"\\(%s())\")\n", name))
		}
	}
	b.WriteString("func main() needs Console | go.Foreign {\n" + main.String() + "}\n")
	return b.String(), model, order
}

// TestReadOnlyUsesCopyNothing is the count half of the property (above),
// in process on the lowered Go.
func TestReadOnlyUsesCopyNothing(t *testing.T) {
	for _, seed := range copySeeds() {
		src, model, order := roProgram(seed)
		mainGo := mustLower(t, fmt.Sprintf("readonly%d", seed), src).mainGo()
		for _, name := range order {
			if model[name] < 0 {
				continue
			}
			text := goFuncsOf(t, mainGo, name)
			if got := len(copyOps.FindAllString(text, -1)); got != model[name] {
				t.Errorf("seed %d, %s: the lowering makes %d copies, the model %d\nReplay: go test -run '^TestReadOnlyUsesCopyNothing$' ./internal/sketch/lower -args -copy.seed=%d\n--- Go\n%s\n--- source\n%s", seed, name, got, model[name], seed, text, src)
			}
		}
	}
}

// TestReadOnlyUsesAgree runs the read-only programs under the rules and
// under the reference (copyLowering "whole"): both print the same lines.
//
// It builds Go because the question is what the programs print at run
// time, which only a run of both lowerings shows.
func TestReadOnlyUsesAgree(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed      uint64
		src       string
		rule, ref *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range copySeeds() {
		src, _, _ := roProgram(seed)
		rule := mustLower(t, fmt.Sprintf("readonlyrule%d", seed), src)
		saved := copyLowering
		copyLowering = "whole"
		ref := mustLower(t, fmt.Sprintf("readonlywhole%d", seed), src)
		copyLowering = saved
		runs = append(runs, run{seed, src, rule, ref})
		programs = append(programs, rule, ref)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the read-only programs do not build")
	}
	for _, r := range runs {
		got, gerr, gcode := runExe(t, exes[r.rule.name])
		want, werr, wcode := runExe(t, exes[r.ref.name])
		if got != want || gcode != wcode || gcode != 0 {
			t.Errorf("seed %d: the rule and the reference differ\nReplay: go test -run '^TestReadOnlyUsesAgree$' ./internal/sketch/lower -args -copy.seed=%d\n%s\n--- rule (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- diff\n%s", r.seed, r.seed, r.src, gcode, got, gerr, wcode, want, werr, diffLines(got, want))
		}
	}
}
