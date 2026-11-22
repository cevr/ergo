package lower

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property of the cut rule (NB-CUT, cut.go; types.md,
// Bindings 2, V5 own-cut): a cut keeps uniqueness. Each seed writes one
// function of each shape, in a shuffled order, with drawn constants:
//
//   - own: a push in plain Ergo over cap, copy, a cut and an index write,
//     on a parameter: owned, so the cut of its room and the cut of a
//     fresh array are taken with no copy (2 copies fewer than the
//     reference);
//   - ownbox: the same over records that hold a slice, whose other arm's
//     copy shares the elements: owned as well (2 fewer);
//   - ownlive: the parameter is read after its cut: not owned, the cut
//     copied under both (0);
//   - ownkept: a let keeps the parameter before its cut: not owned (an
//     earlier use that keeps the value is no read), the cut copied (0);
//   - fresh: a cut of MakeSlice's fresh array (1 fewer);
//   - root: a cut of a let mut that is dead after it (0: the value rule's
//     own freshPath decides it under both);
//   - shared: a cut of a let mut written and read after it (0).
//
// main calls each function with a source that stays live (its caller
// copies it) and in a loop that rebinds the result, writes through every
// result and prints every result, source and alias, so a cut that shares
// storage it should not shows. TestCutKeepsUniqueness holds each shape's
// copies to the model in process, on the Go text and on the IR;
// TestCutAgrees runs the programs on the text path under the rule and the
// reference (cutLowering "copy") and on the IR through ergo run, and all
// three print the same. Replay: go test -run '^TestCut' ./internal/sketch/lower -args -cut.seed=N

var cutSeed = flag.Uint64("cut.seed", 0, "run TestCut* on this seed only")

func cutSeeds() []uint64 {
	if *cutSeed != 0 {
		return []uint64{*cutSeed}
	}
	out := []uint64{}
	for s := uint64(1); s <= uint64(max(*cutSeedCount, 1)); s++ {
		out = append(out, s)
	}
	return out
}

// cutSeedCount is the seed count a receipt widens (-cut.seeds=50).
var cutSeedCount = flag.Int("cut.seeds", 3, "run TestCut* on seeds 1..N")

var cutShapes = []string{"own", "ownbox", "ownlive", "ownkept", "fresh", "root", "shared"}

// cutSaved is each shape's copies the rule saves (above).
var cutSaved = map[string]int{"own": 2, "ownbox": 2, "ownlive": 0, "ownkept": 0, "fresh": 1, "root": 0, "shared": 0}

// cutOwnedShapes are the shapes whose function the rule makes owned (V5):
// it has an owned form under the rule and none under the reference.
var cutOwnedShapes = map[string]bool{"own": true, "ownbox": true}

// cutProgram is seed's program and its functions' names and shapes, in
// order.
func cutProgram(seed uint64) (src string, names, shapes []string) {
	r := rand.New(rand.NewPCG(seed, seed^0x6375746b)) // "cutk"
	var fns, main strings.Builder
	fns.WriteString("extern go cap func room[T](xs []T) int\nextern go copy func copyInto[T](dst *[]T, src []T) int\n\n")
	fns.WriteString("type Box { V int, S []int }\n\n")
	fns.WriteString("func dig(xs []int) int {\n\tlet mut s = 0\n\tfor i in 0..len(xs) {\n\t\ts = s + (i + 1) * xs[i]\n\t}\n\ts\n}\n\n")
	fns.WriteString("func digB(bs []Box) int {\n\tlet mut s = 0\n\tfor i in 0..len(bs) {\n\t\ts = s + (i + 1) * (bs[i].V + dig(bs[i].S))\n\t}\n\ts\n}\n\n")
	shapes = append([]string(nil), cutShapes...)
	r.Shuffle(len(shapes), func(i, j int) { shapes[i], shapes[j] = shapes[j], shapes[i] })
	for k, shape := range shapes {
		name := fmt.Sprintf("cut%d", k)
		names = append(names, name)
		a, b, c := 1+r.IntN(9), 10+r.IntN(9), 20+r.IntN(9)
		n := 3 + r.IntN(4) // 3 pushes from empty reach the room branch under the rule
		switch shape {
		case "own":
			fmt.Fprintf(&fns, "func %s(xs []int, x int) []int {\n\tlet n = len(xs)\n\tif n < room(xs) {\n\t\tlet mut ys = xs[0:n + 1]\n\t\tys[n] = x\n\t\tys\n\t} else {\n\t\tlet mut ys = MakeSlice[int](2 * n + 1)[0:n + 1]\n\t\tlet _ = copyInto(&ys, xs)\n\t\tys[n] = x\n\t\tys\n\t}\n}\n\n", name)
			fmt.Fprintf(&main, "\tlet mut src%d = []int{%d, %d}\n\tsrc%d = src%d.Append(%d)\n\tlet keep%d = src%d\n", k, a, b, k, k, c, k, k)
			fmt.Fprintf(&main, "\tlet mut r%d = %s(src%d, %d)\n\tr%d[0] = r%d[0] + 1000\n\tlet r%db = %s(keep%d, %d)\n", k, name, k, c+1, k, k, k, name, k, c+2)
			fmt.Fprintf(&main, "\tlet mut acc%d = []int{}\n\tfor i in 0..%d {\n\t\tacc%d = %s(acc%d, i + %d)\n\t}\n", k, n, k, name, k, a)
			fmt.Fprintf(&main, "\tConsole.Println(\"%s own \\(len(r%d)) \\(dig(r%d)) \\(dig(r%db)) \\(dig(src%d)) \\(dig(keep%d)) \\(dig(acc%d))\")\n", name, k, k, k, k, k, k)
		case "ownbox":
			fmt.Fprintf(&fns, "func %s(xs []Box, x Box) []Box {\n\tlet n = len(xs)\n\tif n < room(xs) {\n\t\tlet mut ys = xs[0:n + 1]\n\t\tys[n] = x\n\t\tys\n\t} else {\n\t\tlet mut ys = MakeSlice[Box](2 * n + 1)[0:n + 1]\n\t\tlet _ = copyInto(&ys, xs)\n\t\tys[n] = x\n\t\tys\n\t}\n}\n\n", name)
			fmt.Fprintf(&main, "\tlet mut src%d = []Box{Box{V: %d, S: []int{%d}}}\n\tsrc%d = src%d.Append(Box{V: %d, S: []int{%d, %d}})\n", k, a, b, k, k, b, c, a)
			fmt.Fprintf(&main, "\tlet mut r%d = %s(src%d, Box{V: %d, S: []int{}})\n\tr%d[0].S[0] = r%d[0].S[0] + 1000\n\tr%d[0].V = 7\n", k, name, k, c, k, k, k)
			fmt.Fprintf(&main, "\tlet mut acc%d = []Box{}\n\tfor i in 0..%d {\n\t\tacc%d = %s(acc%d, Box{V: i, S: []int{i}})\n\t}\n\tif len(acc%d) > 0 {\n\t\tacc%d[0].S[0] = 99\n\t}\n", k, n, k, name, k, k, k)
			fmt.Fprintf(&main, "\tConsole.Println(\"%s ownbox \\(len(r%d)) \\(digB(r%d)) \\(digB(src%d)) \\(digB(acc%d))\")\n", name, k, k, k, k)
		case "ownkept":
			fmt.Fprintf(&fns, "func %s(xs []int, x int) int {\n\tlet k = xs\n\tlet mut ys = xs[0:2]\n\tys[0] = x\n\tdig(k) * 1000 + dig(ys)\n}\n\n", name)
			fmt.Fprintf(&main, "\tlet src%d = []int{%d, %d, %d}\n\tConsole.Println(\"%s ownkept \\(%s(src%d, %d)) \\(dig(src%d))\")\n", k, a, b, c, name, name, k, n, k)
		case "ownlive":
			fmt.Fprintf(&fns, "func %s(xs []int, k int) int {\n\tlet mut ys = xs[0:k]\n\tys[0] = ys[0] + 100\n\tdig(ys) + xs[0]\n}\n\n", name)
			fmt.Fprintf(&main, "\tlet src%d = []int{%d, %d, %d}\n\tConsole.Println(\"%s ownlive \\(%s(src%d, 2)) \\(dig(src%d))\")\n", k, a, b, c, name, name, k, k)
		case "fresh":
			fmt.Fprintf(&fns, "func %s(m int, x int) []int {\n\tlet mut ys = MakeSlice[int](m)[0:m - 1]\n\tys[0] = x\n\tys\n}\n\n", name)
			fmt.Fprintf(&main, "\tlet mut r%d = %s(%d, %d)\n\tr%d[1] = %d\n\tConsole.Println(\"%s fresh \\(len(r%d)) \\(dig(r%d)) \\(dig(%s(%d, %d)))\")\n", k, name, n+1, a, k, b, name, k, k, name, n+1, c)
		case "root":
			fmt.Fprintf(&fns, "func %s(x int) []int {\n\tlet mut a = []int{%d, %d, %d}\n\ta[2] = x\n\tlet mut b = a[0:2]\n\tb[0] = b[0] + x\n\tb\n}\n\n", name, a, b, c)
			fmt.Fprintf(&main, "\tConsole.Println(\"%s root \\(dig(%s(%d)))\")\n", name, name, n)
		case "shared":
			fmt.Fprintf(&fns, "func %s(x int) int {\n\tlet mut a = []int{%d, %d, %d}\n\tlet mut b = a[0:2]\n\tb[0] = x\n\ta[1] = x + 1\n\tdig(a) * 1000 + dig(b)\n}\n\n", name, a, b, c)
			fmt.Fprintf(&main, "\tConsole.Println(\"%s shared \\(%s(%d))\")\n", name, name, n)
		}
	}
	return fns.String() + "func main() needs Console {\n" + main.String() + "}\n", names, shapes
}

// cutLower lowers src on the text path under the rule, or under the
// reference (cutLowering "copy").
func cutLower(t *testing.T, name, src string, ref bool) *lowered {
	t.Helper()
	keep := cutLowering
	defer func() { cutLowering = keep }()
	cutLowering = ""
	if ref {
		cutLowering = "copy"
	}
	return mustLower(t, name, src)
}

// cutNoRoom is src with room, Go's cap, replaced by a function that
// answers 0: every push takes its copy branch.
func cutNoRoom(src string) string {
	const ext = "extern go cap func room[T](xs []T) int\n"
	if !strings.Contains(src, ext) {
		panic("cutNoRoom: the program declares no room")
	}
	return strings.Replace(src, ext, "func room[T](xs []T) int {\n\tlen(xs) - len(xs)\n}\n", 1)
}

// cutLowerCopying is src lowered under the copying value rule
// (valueLowering "copy"), the reference that owns nothing and copies
// every shared value: TestCutAgrees's second reference, since the cut
// reference keeps every other owned form of main's rule.
func cutLowerCopying(t *testing.T, name, src string) *lowered {
	t.Helper()
	keep := valueLowering
	defer func() { valueLowering = keep }()
	valueLowering = "copy"
	return mustLower(t, name, src)
}

// cutRunBody is the Go text of the body a direct call of name runs: its
// owned form (V5) when it has one, else its own.
func cutRunBody(t *testing.T, mainGo, name string) string {
	t.Helper()
	for _, owned := range []string{name + "Owned", "ergo__" + name + "Owned"} {
		if regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + regexp.QuoteMeta(owned) + `\(`).MatchString(mainGo) {
			return goFuncBody(t, mainGo, owned)
		}
	}
	return goFuncBody(t, mainGo, name)
}

// cutHasOwned reports whether mainGo holds an owned form of name (V5).
func cutHasOwned(mainGo, name string) bool {
	for _, owned := range []string{name + "Owned", "ergo__" + name + "Owned"} {
		if regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + regexp.QuoteMeta(owned) + `\(`).MatchString(mainGo) {
			return true
		}
	}
	return false
}

// cutIRCopies is, for each function of src's IR, the copies its body
// makes (a Clone call or an ergoClone helper), under the rule or the
// reference; a function that keeps its source body is absent.
func cutIRCopies(t *testing.T, src string, ref bool) map[string]int {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	keep := cutLowering
	defer func() { cutLowering = keep }()
	cutLowering = ""
	if ref {
		cutLowering = "copy"
	}
	files, _, err := LowerIR(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	clone := regexp.MustCompile(`"Clone"|"(ergo__)?ergoClone\w*"|"ergo__Clone\w*"`)
	for _, f := range files {
		if f.Path != IRFileName {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			data, err := json.Marshal(fn.Body)
			if err != nil {
				t.Fatal(err)
			}
			out[fn.Name] = len(clone.FindAll(data, -1))
		}
	}
	return out
}

// cutIRRun is the copies of the IR body a direct call of name runs (its
// owned form when it has one), and whether the IR holds it.
func cutIRRun(copies map[string]int, name string) (int, bool) {
	for _, n := range []string{name + "Owned", "ergo__" + name + "Owned", name} {
		if c, ok := copies[n]; ok {
			return c, true
		}
	}
	return 0, false
}

// TestCutKeepsUniqueness is the count model (above), on the Go text and
// on the IR: the rule makes cutSaved[shape] copies fewer than the
// reference in the body each function's direct call runs.
//
// Mutants (lower ledger): cut-fresh-blind (a cut of a fresh value is not
// fresh), cut-read-after (a parameter read after its cut is owned),
// cut-arm-blind (a use in another arm keeps the parameter unowned),
// cut-earlier-kept (an earlier use that keeps the value is a read),
// cut-off (no parameter is owned through a cut).
func TestCutKeepsUniqueness(t *testing.T) {
	for _, seed := range cutSeeds() {
		src, names, shapes := cutProgram(seed)
		replay := fmt.Sprintf("Replay: go test -run '^TestCutKeepsUniqueness$' ./internal/sketch/lower -args -cut.seed=%d\n%s", seed, src)
		rule := cutLower(t, fmt.Sprintf("cut%d", seed), src, false).mainGo()
		ref := cutLower(t, fmt.Sprintf("cut%dref", seed), src, true).mainGo()
		irRule, irRef := cutIRCopies(t, src, false), cutIRCopies(t, src, true)
		for i, name := range names {
			a, b := cutRunBody(t, rule, name), cutRunBody(t, ref, name)
			if owned, refOwned := cutHasOwned(rule, name), cutHasOwned(ref, name); owned != cutOwnedShapes[shapes[i]] || refOwned {
				t.Errorf("seed %d, %s (%s): owned form under the rule %v (the model %v), under the reference %v (want false)\n%s", seed, name, shapes[i], owned, cutOwnedShapes[shapes[i]], refOwned, replay)
			}
			if got := len(copyOps.FindAllString(b, -1)) - len(copyOps.FindAllString(a, -1)); got != cutSaved[shapes[i]] {
				t.Errorf("seed %d, %s (%s): on the Go text the rule makes %d copies fewer than the reference, the model %d\n--- rule\n%s\n--- reference\n%s\n%s", seed, name, shapes[i], got, cutSaved[shapes[i]], a, b, replay)
			}
			ca, okA := cutIRRun(irRule, name)
			cb, okB := cutIRRun(irRef, name)
			if !okA || !okB {
				t.Errorf("seed %d, %s (%s): the IR holds no body of it (rule %v, reference %v): %v\n%s", seed, name, shapes[i], okA, okB, irRule, replay)
				continue
			}
			if got := cb - ca; got != cutSaved[shapes[i]] {
				t.Errorf("seed %d, %s (%s): on the IR the rule makes %d copies fewer than the reference, the model %d\n%s", seed, name, shapes[i], got, cutSaved[shapes[i]], replay)
			}
		}
	}
}

// TestCutAgrees runs each seed's program on the text path under the rule,
// the cut reference and the copying value rule, under the rule again with
// a room that is always 0 (cutNoRoom), and on the IR through ergo run:
// all five print the same lines.
//
// The pushes branch on cap, which the spec leaves unspecified
// (interop.md, Lowering 13): two builds may give one slice different
// room and so take different branches (r1's seed 1: the cut reference
// reserved room for src3 and took the in-place branch, the copying rule
// did not and took the copy branch, which shared each Box's S through Go's
// copy; COPY-ELEMS). So agreement is not left to which branch each build
// happens to take: the no-room run takes the copy branch at every push,
// and under the rule each push loop from empty (3 pushes or more) reaches
// the in-place branch, whose room the rule's fresh cut of MakeSlice keeps.
// Every branch must print the same.
//
// It builds Go because the question is what the programs print at run
// time, which only a run shows; the IR's run needs ergo's IR compiler.
func TestCutAgrees(t *testing.T) {
	needsBuild(t)
	type run struct {
		seed                  uint64
		src                   string
		rule, ref, cpy, nocap *lowered
	}
	var runs []run
	var programs []*lowered
	for _, seed := range cutSeeds() {
		src, _, _ := cutProgram(seed)
		rule := cutLower(t, fmt.Sprintf("cut%d", seed), src, false)
		ref := cutLower(t, fmt.Sprintf("cut%dref", seed), src, true)
		cpy := cutLowerCopying(t, fmt.Sprintf("cut%dcopy", seed), src)
		nocap := cutLower(t, fmt.Sprintf("cut%dnocap", seed), cutNoRoom(src), false)
		runs = append(runs, run{seed, src, rule, ref, cpy, nocap})
		programs = append(programs, rule, ref, cpy, nocap)
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the cut programs do not build")
	}
	for _, r := range runs {
		replay := fmt.Sprintf("Replay: go test -run '^TestCutAgrees$' ./internal/sketch/lower -args -cut.seed=%d\n%s", r.seed, r.src)
		got, gerr, gcode := runExe(t, exes[r.rule.name])
		want, werr, wcode := runExe(t, exes[r.ref.name])
		if got != want || gcode != wcode || gcode != 0 {
			t.Errorf("seed %d: the rule and the reference differ\n--- rule (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- diff\n%s\n%s", r.seed, gcode, got, gerr, wcode, want, werr, diffLines(got, want), replay)
		}
		if cp, cerr, ccode := runExe(t, exes[r.cpy.name]); cp != want || ccode != wcode {
			t.Errorf("seed %d: the copying value rule and the reference differ\n--- copying (exit %d)\n%s%s\n--- reference\n%s\n--- diff\n%s\n%s", r.seed, ccode, cp, cerr, want, diffLines(cp, want), replay)
		}
		if nc, nerr, ncode := runExe(t, exes[r.nocap.name]); nc != want || ncode != wcode {
			t.Errorf("seed %d: the rule with no room (the copy branch everywhere) and the reference differ\n--- no room (exit %d)\n%s%s\n--- reference\n%s\n--- diff\n%s\n%s", r.seed, ncode, nc, nerr, want, diffLines(nc, want), replay)
		}
		ir, err := ergoRun(t, r.src)
		if err != nil || ir != want {
			t.Errorf("seed %d: the IR (ergo run) and the reference differ: %v\n--- IR\n%s\n--- reference\n%s\n--- diff\n%s\n%s", r.seed, err, ir, want, diffLines(ir, want), replay)
		}
	}
}
