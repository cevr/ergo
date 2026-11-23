package lower

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// monoLawProgram is a seeded program whose generic operation type Pair[T]
// has member laws run by two providers of different instances: Ints
// (Pair[int]) clamps, so Idem holds for it; Strs (Pair[string]) appends,
// so Idem is refuted. Taken's catch depends on the seed's moduli. The
// seed varies the constants only.
func monoLawProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x4c41574d))
	var b strings.Builder
	b.WriteString("type Bad { N int }\n\n")
	b.WriteString("type Pair[T comparable] {\n\tPut(v T) T,\n\tTake(v T) T fails Bad,\n\tSame(a T, c T) bool,\n\n\tlaw Idem(v T) {\n\t\tlet w = Put(v)\n\t\tSame(Put(w), w)\n\t},\n\n\tlaw Taken(v T) {\n\t\tlet w = Take(v) catch {\n\t\tcase Bad:\n\t\t\treturn true\n\t\t}\n\t\tSame(w, v)\n\t},\n}\n\n")
	k := 3 + r.IntN(5)
	fmt.Fprintf(&b, "type Ints {} implements Pair[int]\n\nfunc Put(_ Ints, v int) int {\n\tif v > %d {\n\t\treturn %d\n\t}\n\tv\n}\n\n", k, k)
	fmt.Fprintf(&b, "func Take(_ Ints, v int) int fails Bad {\n\tif v %% %d == 0 {\n\t\treturn Bad{N: v}\n\t}\n\tv\n}\n\n", 2+r.IntN(4))
	b.WriteString("func Same(_ Ints, a int, c int) bool {\n\ta == c\n}\n\n")
	b.WriteString("type Strs {} implements Pair[string]\n\nfunc Put(_ Strs, v string) string {\n\tv + \"!\"\n}\n\n")
	fmt.Fprintf(&b, "func Take(_ Strs, v string) string fails Bad {\n\tif len(v) %% %d == 0 {\n\t\treturn Bad{N: len(v)}\n\t}\n\tv\n}\n\n", 2+r.IntN(3))
	b.WriteString("func Same(_ Strs, a string, c string) bool {\n\ta == c\n}\n\n")
	b.WriteString("func main() {}\n")
	return b.String()
}

// TestGenericLawLoweringsAgree is TestGenericLoweringsAgree's law case
// (G6-GENERIC-LAW): a law of a generic operation type runs a copy at each
// provider's instance (mono.go lawMonoFor), and under the reference
// (genericLowering "stencil") the Go generic law at the instance. Both
// test builds report the same verdicts, with the same sites, for one
// provider that keeps Idem and one that does not; the new lowering makes
// a copy per law and instance, the reference none. A failing seed prints
// the Ergo source and both reports. Mutants (testdata/mutants.json):
// mono-law-shares-instances (the two instances share one copy).
//
// It builds Go because a law runs only in the built test binary.
func TestGenericLawLoweringsAgree(t *testing.T) {
	needsBuild(t)
	for _, seed := range []uint64{1, 2} {
		src := monoLawProgram(seed)
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, ds := check.Check("main", []*syntax.File{f}, nil)
		if len(ds) != 0 {
			t.Fatalf("seed %d: %v\n%s", seed, ds, src)
		}
		var programs []*lowered
		var copies [2]int
		for i, ref := range []bool{false, true} {
			saved, savedHook := genericLowering, statsHook
			genericLowering = ""
			if ref {
				genericLowering = "stencil"
			}
			var stats knownStats
			statsHook = func(s knownStats) { stats = s }
			files, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}})
			genericLowering, statsHook = saved, savedHook
			if err != nil {
				t.Fatalf("seed %d (reference %v): %v\n%s", seed, ref, err, src)
			}
			copies[i] = stats.Taken["mono-instance"]
			programs = append(programs, &lowered{name: fmt.Sprintf("monolaw%d_%d", seed, i), files: files, src: src})
		}
		if copies[0] < 4 || copies[1] != 0 {
			t.Fatalf("seed %d: copies %d (want 4 or more: 2 laws at 2 instances) and %d under the reference (want 0)", seed, copies[0], copies[1])
		}
		exes := buildAll(t, programs, true)
		if exes == nil {
			return
		}
		var outs [2]string
		for i, p := range programs {
			stdout, stderr, code := runExeArgs(t, exes[p.name], "--seed", "7")
			outs[i] = fmt.Sprintf("exit %d\n%s", code, strings.Join(verdicts(stdout), "\n"))
			if code != 1 {
				t.Fatalf("seed %d: %s exit %d, stdout:\n%s\nstderr:\n%s\n%s", seed, p.name, code, stdout, firstLines(stderr, 8), src)
			}
		}
		if outs[0] != outs[1] {
			t.Fatalf("seed %d: the lowerings differ\ncopies:\n%s\nreference:\n%s\n%s", seed, outs[0], outs[1], src)
		}
		if !strings.Contains(outs[0], "ok law Pair.Idem for Ints") || !strings.Contains(outs[0], "FAIL law Pair.Idem for Strs") {
			t.Fatalf("seed %d: the report lacks Idem's two verdicts:\n%s\n%s", seed, outs[0], src)
		}
	}
}

// TestLawErrorRowInstances is the smallest repro of a member law skipped
// despite providers that fix its error row. Two rows have the same Go
// receiver shape; the law still has separate monomorphic instances.
// It kills mono-law-merges-error-rows.
func TestLawErrorRowInstances(t *testing.T) {
	const src = `type A {}
type B { Flag bool }
type Probe[E] {
	Raise() fails E,
	law Raises(e E) {
		Raise() catch {
		case _:
			return true
		}
		false
	},
}
type AP {} implements Probe[A]
func Raise(_ AP) fails A { return A }
type BP {} implements Probe[B]
func Raise(_ BP) fails B { return B{Flag: false} }
let both = context { Probe[A]: AP, Probe[B]: BP }
func main() {}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("check: %v", ds)
	}
	r := pkg.LawRuns[len(pkg.LawRuns)-1]
	if r.Law.Unrun != "" || len(r.Bindings) != 2 || len(r.Contexts) != 2 {
		t.Fatalf("both instances must run, by declaration and under the same context: %+v; %s", r, r.Law.Unrun)
	}
	if r.Contexts[0].Title() == r.Contexts[1].Title() {
		t.Fatal("the two context runs would share a verdict and cache key")
	}
	if r.Bindings[0][0].Params[0].String() == r.Bindings[1][0].Params[0].String() ||
		r.Contexts[0].Params[0].String() == r.Contexts[1].Params[0].String() {
		t.Fatal("the law's generated argument lost the concrete row")
	}
	var programs []*lowered
	for _, mode := range []string{"mono", "stencil"} {
		saved, hook := genericLowering, statsHook
		genericLowering = ""
		if mode == "stencil" {
			genericLowering = "stencil"
		}
		var stats knownStats
		statsHook = func(s knownStats) { stats = s }
		files, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}})
		genericLowering, statsHook = saved, hook
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, src)
		}
		if got := stats.Taken["mono-instance"]; mode == "mono" && got != 2 {
			t.Fatalf("made %d copies; the two closed rows must not share a copy", got)
		}
		programs = append(programs, &lowered{name: "rowlaw_" + mode, files: files, src: src})
		// Both closed capabilities must keep a distinct field even
		// though both fields have exactly the same Go type.
		if got := strings.Count(programs[len(programs)-1].mainGo(), "\n\tProbe_1 "); got != 1 {
			t.Fatalf("%s: the two closed capabilities share an environment slot", mode)
		}
	}
	typecheckGo(t, programs)
}
