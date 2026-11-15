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
// through a borrow, deref(&r.s); a Go call's result) into a read-only
// context (len, an index read, an index read under len, a comparison),
// then writes the store and returns the value read with a digest of the
// store. The model: the lowering copies nothing in such a function.
// TestReadOnlyUsesCopyNothing counts the copies in each function's Go.
//
// The adversaries (agreement only) are uses that are not read at once: an
// index or a comparison operand that writes the store (part(*r)[bump(r)],
// part(*r) == grow(r)), an element kept past a write (let e =
// outs(*r)[0]), a call whose later operand writes the store
// (part2(*r, bumpv(r))[0]), and & of an element (setv(&part(*r)[1])).
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

func deref[T](p *T) T { *p }

func bump(r *ro) int {
	r.s[1] = 70
	1
}

func grow(r *ro) []int {
	r.s[0] = 50
	[]int{50, 2, 3}
}

func bumpv(r *ro) int {
	r.s[0] = 50
	0
}

func setv(p *int) int {
	*p = 4
	0
}

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
}

// roContexts are the read-only contexts of a result X of each kind.
var roContexts = map[string][][2]string{
	"s": {{"len", "len(X)"}, {"index", "X[1]"}, {"compare", "if X == []int{1, 2, 3} { 1 } else { 2 }"}},
	"o": {{"len", "len(X)"}, {"index len", "len(X[0])"}, {"compare", "if X == [][]int{[]int{4}} { 1 } else { 2 }"}},
	"m": {{"len", "len(X)"}},
	"g": {{"len", "len(X)"}, {"index len", "len(X[0])"}, {"compare", "if X == []string{\"a,\", \"b\"} { 1 } else { 2 }"}},
}

var roAdversaries = []struct{ name, value string }{
	{"index call", "part(*r)[bump(r)]"},
	{"compare call", "if part(*r) == grow(r) { 1 } else { 2 }"},
	{"kept index", "let e = outs(*r)[0]\n\tr.out[0][0] = 7\n\tlet n = e[0]"},
	{"in flight", "part2(*r, bumpv(r))[0]"},
	{"address", "let z = setv(&part(*r)[1])\n\tlet n = z + r.s[1]"},
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
		head := fmt.Sprintf("func %s(r *ro) int {\n", name)
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
		if strings.HasPrefix(fns[i], "func "+name+"(r *ro)") {
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
