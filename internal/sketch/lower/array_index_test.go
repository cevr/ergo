package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestArrayIndexInPlaceAgrees is the seeded property of arrayReadInPlace
// (expr.go): an array index a[i] whose index calls reads a where it is,
// with no copy, when nothing in i can write a. Each seed writes a program
// of reads a[i] of a local array, of an array field through a pointer
// (b.cells[i]) and through a dereference ((*p)[i]), whose index is a call
// that writes nothing (at4), a call that writes the array through a
// pointer parameter (poke, a dot call pokeAt on a pointer receiver), a
// call of a local function value that writes a captured let mut, or a
// named call passed a function literal that writes one (applyK). Ergo
// reads a before i, so a read sees the array as it was before the call.
// The generator is the model: it runs each read on Go arrays in that
// order and writes the line the program must print. The program is
// lowered by default and with the reference lowering arrayLowering
// "spill" (every array bound to a temporary first); both must print the
// model's lines. The fixed seeds reach every read form.
//
// It builds Go because the question is what the lowered program reads at
// run time, after the index's statements; go/types answers only whether
// it compiles, and the check-time evaluator does not run pointers.
//
// Mutants it kills (testdata/mutants.json): array-in-place-pointer-param,
// array-in-place-function-value, array-in-place-literal-argument.
// A failing seed prints the source, both outputs and the replay command.
func TestArrayIndexInPlaceAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_ARRAY_INDEX_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{v}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		name      string
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := arrayIndexProgram(seed, reached)
		for _, mode := range []string{"", "spill"} {
			saved := arrayLowering
			arrayLowering = mode
			name := fmt.Sprintf("arrayindex_%d_%s", seed, mode)
			programs = append(programs, mustLower(t, name, src))
			arrayLowering = saved
			runs = append(runs, run{seed, name, src, want})
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the array index programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[r.name])
		if out != r.want || code != 0 {
			t.Errorf("seed %d, %s: the program and the model differ\nreplay: ERGO_ARRAY_INDEX_SEED=%d go test -run '^TestArrayIndexInPlaceAgrees$' ./internal/sketch/lower\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.name, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	if len(seeds) > 1 {
		for _, form := range arrayIndexForms {
			if !reached[form] {
				t.Errorf("the fixed seeds never reach %q", form)
			}
		}
	}
}

// arrayIndexForms are the read forms the fixed seeds must reach.
var arrayIndexForms = []string{"local pure", "local poke", "local value", "local literal", "field pure", "field dot", "deref pure", "deref poke"}

const arrayIndexPrelude = `type Box { cells [4]int }

func at4(k int) int { (k % 4 + 4) % 4 }

func poke(a *[4]int, k int, v int) int {
	(*a)[k] = v
	k
}

func pokeAt(b *Box, k int, v int) int {
	b.cells[k] = v
	k
}

func applyK(f func() int) int { f() }

func fieldPure(b *Box, k int) int { b.cells[at4(k)] }

func fieldDot(b *Box, k int, v int) int { b.cells[b.pokeAt(k, v)] }

func derefPure(p *[4]int, k int) int { (*p)[at4(k)] }

func derefPoke(p *[4]int, k int, v int) int { (*p)[poke(p, k, v)] }

func sum(a [4]int) int { a[0] + 2 * a[1] + 3 * a[2] + 4 * a[3] }

`

// arrayIndexProgram writes the program of seed and the lines it must
// print.
func arrayIndexProgram(seed uint64, reached map[string]bool) (src, want string) {
	r := rand.New(rand.NewPCG(seed, 0x617272))
	var b, w strings.Builder
	line := func(s string) { b.WriteString("\t" + s + "\n") }
	for i := range 24 {
		var a [4]int
		for j := range a {
			a[j] = r.IntN(100)
		}
		lit := fmt.Sprintf("[4]int{%d, %d, %d, %d}", a[0], a[1], a[2], a[3])
		k, v := r.IntN(4), 100+r.IntN(100)
		x := fmt.Sprintf("x%d", i)
		// got is the read, a the array after it, each as the model says.
		var got int
		form := arrayIndexForms[r.IntN(len(arrayIndexForms))]
		reached[form] = true
		switch form {
		case "local pure":
			kk := r.IntN(40) - 20
			line(fmt.Sprintf("let %s = %s", x, lit))
			line(fmt.Sprintf("let r%d = %s[at4(%d)]", i, x, kk))
			got = a[((kk%4)+4)%4]
		case "local poke":
			line(fmt.Sprintf("let mut %s = %s", x, lit))
			line(fmt.Sprintf("let r%d = %s[poke(&%s, %d, %d)]", i, x, x, k, v))
			got, a[k] = a[k], v
		case "local value":
			line(fmt.Sprintf("let mut %s = %s", x, lit))
			line(fmt.Sprintf("let f%d = func() int {\n\t\t%s[%d] = %d\n\t\t%d\n\t}", i, x, k, v, k))
			line(fmt.Sprintf("let r%d = %s[f%d()]", i, x, i))
			got, a[k] = a[k], v
		case "local literal":
			line(fmt.Sprintf("let mut %s = %s", x, lit))
			line(fmt.Sprintf("let r%d = %s[applyK(func() int {\n\t\t%s[%d] = %d\n\t\t%d\n\t})]", i, x, x, k, v, k))
			got, a[k] = a[k], v
		case "field pure":
			kk := r.IntN(40) - 20
			line(fmt.Sprintf("let mut %s = Box{cells: %s}", x, lit))
			line(fmt.Sprintf("let r%d = fieldPure(&%s, %d)", i, x, kk))
			line(fmt.Sprintf("%s.cells[0] += 0", x))
			got = a[((kk%4)+4)%4]
		case "field dot":
			line(fmt.Sprintf("let mut %s = Box{cells: %s}", x, lit))
			line(fmt.Sprintf("let r%d = fieldDot(&%s, %d, %d)", i, x, k, v))
			got, a[k] = a[k], v
		case "deref pure":
			kk := r.IntN(40) - 20
			line(fmt.Sprintf("let mut %s = %s", x, lit))
			line(fmt.Sprintf("let r%d = derefPure(&%s, %d)", i, x, kk))
			line(fmt.Sprintf("%s[0] += 0", x))
			got = a[((kk%4)+4)%4]
		case "deref poke":
			line(fmt.Sprintf("let mut %s = %s", x, lit))
			line(fmt.Sprintf("let r%d = derefPoke(&%s, %d, %d)", i, x, k, v))
			got, a[k] = a[k], v
		}
		arr := x
		if strings.HasPrefix(form, "field") {
			arr = x + ".cells"
		}
		line(fmt.Sprintf("Console.Println(\"\\(r%d) \\(sum(%s))\")", i, arr))
		fmt.Fprintf(&w, "%d %d\n", got, a[0]+2*a[1]+3*a[2]+4*a[3])
	}
	return arrayIndexPrelude + "func main() needs Console {\n" + b.String() + "}\n", w.String()
}
