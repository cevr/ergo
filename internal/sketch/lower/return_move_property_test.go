package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded properties of the return rule (KEPT-MOVE, values.go
// needsCopy): a let mut that only nested frames write (function literals,
// which the checker keeps non-escaping when they write it) moves at its
// return from its own frame, since no such frame runs after the return.
// Each seed writes functions of one shape each, over a slice or a record
// of a slice, written through apply's callback:
//
//   - set, append, elem, nested, record: the root (or its field) is
//     returned from its own frame after the callback writes it: the rule
//     makes one copy fewer than the reference (returnLowering "copy");
//   - twofns: a literal returns the root while another literal can still
//     write it (a move where the source is live): the same copies under
//     both, and a move there shows in the run;
//   - inner: the root returned from a literal after the callback: the
//     same copies under both.
//
// TestReturnMoveCounts holds the copy counts of each function to that
// model, in process on the lowered Go; TestReturnMoveAgrees builds each
// program under the rule and the reference, and main writes into every
// result and prints it with its sources, so a result that shares storage
// it should not shows. Replay: go test -run '^TestReturnMove' ./internal/sketch/lower -args -copy.seed=N

var returnShapes = []string{"set", "append", "elem", "nested", "record", "twofns", "inner"}

// returnProgram is seed's program and each function's copies saved by the
// rule (1 or 0), in order.
func returnProgram(seed uint64) (string, []string, map[string]int) {
	r := rand.New(rand.NewPCG(seed, seed^0x6b6d6f7665))
	var fns, main strings.Builder
	var order []string
	saved := map[string]int{}
	fns.WriteString("type Box { Xs []int, N int }\n\n")
	fns.WriteString("func apply(n int, f func(i int)) {\n\tfor i in 0..n {\n\t\tf(i)\n\t}\n}\n\n")
	fns.WriteString("func pick(f func() []int) []int { f() }\n\n")
	fns.WriteString("func pick2(g func() []int, f func(i int)) []int {\n\tlet r = g()\n\tf(0)\n\tr\n}\n\n")
	fns.WriteString("func sum(xs []int) int {\n\tlet mut s = 0\n\tfor x in xs {\n\t\ts = s + x\n\t}\n\ts\n}\n\n")
	shapes := append([]string(nil), returnShapes...)
	r.Shuffle(len(shapes), func(i, j int) { shapes[i], shapes[j] = shapes[j], shapes[i] })
	for k, shape := range shapes {
		name := fmt.Sprintf("ret%d", k)
		order = append(order, name)
		n := 1 + r.IntN(4)
		a, b := r.IntN(9), 10+r.IntN(9)
		fmt.Fprintf(&main, "\tlet src%d = []int{%d, %d}\n", k, a, b)
		saved[name] = 1
		switch shape {
		case "set":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = []int{}\n\tapply(n, func(i int) { v = src })\n\tv\n}\n\n", name)
		case "append":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = src\n\tapply(n, func(i int) { v = v.Append(i) })\n\tv\n}\n\n", name)
		case "elem":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = src\n\tapply(n, func(i int) { v[i %% 2] = v[i %% 2] + i + 1 })\n\tv\n}\n\n", name)
		case "nested":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = src\n\tapply(n, func(i int) { apply(2, func(j int) { v = v.Append(i * 10 + j) }) })\n\tv\n}\n\n", name)
		case "record":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut b = Box{Xs: src, N: 0}\n\tapply(n, func(i int) { b.Xs = b.Xs.Append(i)\n\t\tb.N = b.N + 1 })\n\tb.Xs\n}\n\n", name)
		case "twofns":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = src.Append(n)\n\tpick2(func() []int { v }, func(i int) { v[0] = v[0] + 100 })\n}\n\n", name)
			saved[name] = 0 // a literal returns v while the other can still write it
		case "inner":
			fmt.Fprintf(&fns, "func %s(src []int, n int) []int {\n\tlet mut v = src\n\tapply(n, func(i int) { v = v.Append(i) })\n\tpick(func() []int { v })\n}\n\n", name)
			saved[name] = 0
		}
		fmt.Fprintf(&main, "\tlet mut r%d = %s(src%d, %d)\n", k, name, k, n)
		fmt.Fprintf(&main, "\tif len(r%d) > 0 {\n\t\tr%d[0] = r%d[0] + 1000\n\t}\n", k, k, k)
		fmt.Fprintf(&main, "\tConsole.Println(\"%s %s \\(len(r%d)) \\(sum(r%d)) \\(sum(src%d))\")\n", name, shape, k, k, k)
	}
	return fns.String() + "func main() needs Console {\n" + main.String() + "}\n", order, saved
}

// TestReturnMoveCounts is the return rule's count model (above): the rule
// makes saved[f] copies fewer than the reference in each function f.
func TestReturnMoveCounts(t *testing.T) {
	for _, seed := range copySeeds() {
		src, order, saved := returnProgram(seed)
		rule := mustLower(t, fmt.Sprintf("retmove%d", seed), src).mainGo()
		keep := returnLowering
		returnLowering = "copy"
		ref := mustLower(t, fmt.Sprintf("retcopy%d", seed), src).mainGo()
		returnLowering = keep
		for _, name := range order {
			a, b := goFuncsOf(t, rule, name), goFuncsOf(t, ref, name)
			got := len(copyOps.FindAllString(b, -1)) - len(copyOps.FindAllString(a, -1))
			if got != saved[name] {
				t.Errorf("seed %d, %s: the rule makes %d copies fewer than the reference, the model %d\nReplay: go test -run '^TestReturnMoveCounts$' ./internal/sketch/lower -args -copy.seed=%d\n--- rule\n%s\n--- reference\n%s\n--- source\n%s", seed, name, got, saved[name], seed, a, b, src)
			}
		}
	}
}

// TestReturnMoveAgrees runs each program under the rule and the reference
// (returnLowering "copy"): both print the same lines.
//
// It builds Go because the question is what the programs print at run
// time, which only a run of both lowerings shows.
func TestReturnMoveAgrees(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed      uint64
		src       string
		rule, ref *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range copySeeds() {
		src, _, _ := returnProgram(seed)
		rule := mustLower(t, fmt.Sprintf("retmove%d", seed), src)
		keep := returnLowering
		returnLowering = "copy"
		ref := mustLower(t, fmt.Sprintf("retcopy%d", seed), src)
		returnLowering = keep
		runs = append(runs, run{seed, src, rule, ref})
		programs = append(programs, rule, ref)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the return programs do not build")
	}
	for _, r := range runs {
		got, gerr, gcode := runExe(t, exes[r.rule.name])
		want, werr, wcode := runExe(t, exes[r.ref.name])
		if got != want || gcode != wcode || gcode != 0 {
			t.Errorf("seed %d: the rule and the reference differ\nReplay: go test -run '^TestReturnMoveAgrees$' ./internal/sketch/lower -args -copy.seed=%d\n%s\n--- rule (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- diff\n%s", r.seed, r.seed, r.src, gcode, got, gerr, wcode, want, werr, diffLines(got, want))
		}
	}
}
