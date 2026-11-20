package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The seeded property of Go's copy over values (copyElemSource, values.go;
// types.md V3): `extern go copy func copyInto[T](dst *[]T, src []T) int`
// copies src's elements into dst, and the copy is a value: a write
// through dst's elements never reaches src. Each seed writes, for each
// element kind (int, string, []int, a record holding a slice, a Map), a
// source of drawn values, copies it into a fresh array (in a function
// that borrows the source, or in main itself), writes through one copied
// element (an index write into its slice, a field write, a Put) and
// prints every element of the source and of the copy. The model is value
// semantics: the source prints as built, the copy shows the one write.
//
// TestGoCopyKeepsValues runs each seed's program on the text path (the
// default lowering and the copying value rule, valueLowering "copy") and
// on the IR through ergo run, against the model. The evaluator is no
// oracle here: it runs a package's tests and laws, and has no native of
// Go's copy (eval/interp.go names only append among Go's builtins).
//
// It builds Go because the question is what the programs print at run
// time, which only a run shows; the IR's run needs ergo's IR compiler.
//
// Mutant (lower ledger): copy-elems-blind (Go's copy takes its source
// uncopied whatever its elements hold). Replay: go test -run
// '^TestGoCopyKeepsValues$' ./internal/sketch/lower -args -gocopy.seed=N

var goCopySeed = flag.Uint64("gocopy.seed", 0, "run TestGoCopyKeepsValues on this seed only")

// goCopySeedCount is the seed count a receipt widens (-gocopy.seeds=50).
var goCopySeedCount = flag.Int("gocopy.seeds", 4, "run TestGoCopyKeepsValues on seeds 1..N")

func goCopySeeds() []uint64 {
	if *goCopySeed != 0 {
		return []uint64{*goCopySeed}
	}
	var out []uint64
	for s := uint64(1); s <= uint64(max(*goCopySeedCount, 1)); s++ {
		out = append(out, s)
	}
	return out
}

// goCopyKind is one element kind: its Ergo type, an element's literal
// from a drawn int, the write through the copy's element j, what an
// element prints, and the model's print of an element before and after
// the write.
type goCopyKind struct {
	name, typ string
	lit       func(v int) string
	write     func(e string) string
	show      func(x string) string
	model     func(v int, written bool) string
}

var goCopyKinds = []goCopyKind{
	{"int", "int",
		func(v int) string { return fmt.Sprint(v) },
		func(e string) string { return e + " = " + e + " + 100" },
		func(x string) string { return `\(` + x + `)` },
		func(v int, w bool) string { return fmt.Sprint(v + bump(w, 100)) }},
	{"string", "string",
		func(v int) string { return fmt.Sprintf("%q", fmt.Sprint("s", v)) },
		func(e string) string { return e + " = " + e + ` + "x"` },
		func(x string) string { return `\(` + x + `)` },
		func(v int, w bool) string {
			if w {
				return fmt.Sprint("s", v, "x")
			}
			return fmt.Sprint("s", v)
		}},
	{"slice", "[]int",
		func(v int) string { return fmt.Sprintf("[]int{%d, %d}", v, v+1) },
		func(e string) string { return e + "[0] = " + e + "[0] + 100" },
		func(x string) string { return `\(` + x + `[0]) \(` + x + `[1])` },
		func(v int, w bool) string { return fmt.Sprint(v+bump(w, 100), " ", v+1) }},
	{"box", "GcBox",
		func(v int) string { return fmt.Sprintf("GcBox{V: %d, S: []int{%d}}", v, v+2) },
		func(e string) string { return e + ".S[0] = " + e + ".S[0] + 100\n\t" + e + ".V = 7" },
		func(x string) string { return `\(` + x + `.V) \(` + x + `.S[0])` },
		func(v int, w bool) string {
			if w {
				return fmt.Sprint(7, " ", v+2+100)
			}
			return fmt.Sprint(v, " ", v+2)
		}},
	{"map", "Map[string, int]",
		func(v int) string { return fmt.Sprintf(`Map[string, int]{"a": %d}`, v) },
		func(e string) string { return e + ` = ` + e + `.Put("z", 9)` },
		func(x string) string { return `\(len(` + x + `))` },
		func(v int, w bool) string { return fmt.Sprint(1 + bump(w, 1)) }},
}

func bump(w bool, n int) int {
	if w {
		return n
	}
	return 0
}

// goCopyProgram is seed's program and the lines the model says it prints.
func goCopyProgram(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x60c0))
	var b, body, want strings.Builder
	b.WriteString("extern go copy func copyInto[T](dst *[]T, src []T) int\n\ntype GcBox { V int, S []int }\n\n")
	for k, kind := range goCopyKinds {
		n := 1 + r.IntN(3)
		j := r.IntN(n)
		vals := make([]string, n)
		ints := make([]int, n)
		for i := range vals {
			ints[i] = 1 + r.IntN(50)
			vals[i] = kind.lit(ints[i])
		}
		src := fmt.Sprintf("src%d", k)
		fmt.Fprintf(&body, "\tlet %s = []%s{%s}\n", src, kind.typ, strings.Join(vals, ", "))
		if r.IntN(2) == 0 {
			// The copy in a function that borrows its source.
			fmt.Fprintf(&b, "func dup%d(xs []%s) []%s {\n\tlet mut ys = MakeSlice[%s](len(xs))[0:len(xs)]\n\tlet _ = copyInto(&ys, xs)\n\tys\n}\n\n", k, kind.typ, kind.typ, kind.typ)
			fmt.Fprintf(&body, "\tlet mut d%d = dup%d(%s)\n", k, k, src)
		} else {
			fmt.Fprintf(&body, "\tlet mut d%d = MakeSlice[%s](len(%s))[0:len(%s)]\n\tlet _ = copyInto(&d%d, %s)\n", k, kind.typ, src, src, k, src)
		}
		fmt.Fprintf(&body, "\t%s\n", kind.write(fmt.Sprintf("d%d[%d]", k, j)))
		for i := range n {
			fmt.Fprintf(&body, "\tConsole.Println(\"%s src %d %s dup %s\")\n", kind.name, i, kind.show(fmt.Sprintf("%s[%d]", src, i)), kind.show(fmt.Sprintf("d%d[%d]", k, i)))
			fmt.Fprintf(&want, "%s src %d %s dup %s\n", kind.name, i, kind.model(ints[i], false), kind.model(ints[i], i == j))
		}
	}
	b.WriteString("func main() needs Console {\n" + body.String() + "}\n")
	return b.String(), want.String()
}

// TestGoCopyKeepsValues is the property above.
func TestGoCopyKeepsValues(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed      uint64
		src, want string
		def, cpy  *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range goCopySeeds() {
		src, want := goCopyProgram(seed)
		def := mustLower(t, fmt.Sprintf("gocopy%d", seed), src)
		keep := valueLowering
		valueLowering = "copy"
		cpy := mustLower(t, fmt.Sprintf("gocopy%dcopy", seed), src)
		valueLowering = keep
		runs = append(runs, run{seed, src, want, def, cpy})
		programs = append(programs, def, cpy)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the copy programs do not build")
	}
	for _, r := range runs {
		replay := fmt.Sprintf("Replay: go test -run '^TestGoCopyKeepsValues$' ./internal/sketch/lower -args -gocopy.seed=%d\n%s", r.seed, r.src)
		for _, l := range []struct {
			name string
			p    *lowered
		}{{"the default lowering", r.def}, {"the copying value rule", r.cpy}} {
			got, gerr, code := runExe(t, exes[l.p.name])
			if got != r.want || code != 0 {
				t.Errorf("seed %d: %s and the model differ (exit %d)\n%s\n--- diff\n%s\n%s", r.seed, l.name, code, gerr, diffLines(got, r.want), replay)
			}
		}
		ir, err := ergoRun(t, r.src)
		if err != nil || ir != r.want {
			t.Errorf("seed %d: the IR (ergo run) and the model differ: %v\n--- diff\n%s\n%s", r.seed, err, diffLines(ir, r.want), replay)
		}
	}
}
