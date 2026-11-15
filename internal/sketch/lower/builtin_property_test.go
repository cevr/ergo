package lower

import (
	"flag"
	"fmt"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The seeded property of Go builtin bindings (check/builtin.go; calls.go,
// builtinCall). Each seed writes one program over a let mut []int that
// calls externs binding append, len, cap, copy, clear, min and max, and
// the prelude's Append (which binds append too): appends kept and dropped,
// an append whose source is read again (the value rule clips it: the new
// element is not seen through the old slice's spare capacity), a copy into
// a borrow and a clear through one; and over a let mut Map[int, int], the
// prelude's Put and Delete (Delete binds Go's delete, OPT7) and a user
// extern of delete, in place and with the source read again (the value
// rule writes a copy, so the source keeps the key). It runs under the builtin lowering and
// under the reference (builtinLowering "call", a Go function per builtin);
// stdout must equal the model's, a Go fold of the same statements, under
// both. A failing seed prints the Ergo source, both outputs and its replay
// command. The fixed seeds reach every path in builtinPaths.

var builtinSeed = flag.Uint64("builtin.seed", 0, "run TestBuiltinsAgree on this seed alone")

var builtinPaths = []string{"push", "keep", "alias", "prelude", "size", "copy", "clear", "minmax", "put", "delete", "deleteKeep", "unset"}

const builtinPrelude = `extern go append func push[T](xs []T, x T) []T
extern go len func size[T](xs []T) int
extern go cap func room[T](xs []T) int
extern go copy func copyInto[T](dst *[]T, src []T) int
extern go clear func wipe[T](xs *[]T)
extern go min func least(a int, b int) int
extern go max func most(a int, b int) int
extern go delete func unset[K comparable, V](m Map[K, V], k K) Map[K, V]

func showMap(m Map[int, int]) string {
	let mut s = "{\(len(m))"
	for k in []int{0, 1, 2, 3, 4, 5, 6, 7} {
		match m.Get(k) {
		case Some as v:
			s = s + " \(k):\(v.Value)"
		case None:
		}
	}
	s + "}"
}

func show(xs []int) string {
	let mut s = ""
	for x in xs {
		s = s + " \(x)"
	}
	"[\(size(xs))\(s)]"
}

`

type builtinGen struct {
	r       *rand.Rand
	b, want strings.Builder
	xs      []int
	m       map[int]int
	names   int
	reached map[string]bool
}

func (g *builtinGen) line(s string) { g.b.WriteString("\t" + s + "\n") }

func (g *builtinGen) show(xs []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%d", len(xs))
	for _, x := range xs {
		fmt.Fprintf(&b, " %d", x)
	}
	return b.String() + "]"
}

func (g *builtinGen) showMap(m map[int]int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "{%d", len(m))
	for k := range 8 {
		if v, ok := m[k]; ok {
			fmt.Fprintf(&b, " %d:%d", k, v)
		}
	}
	return b.String() + "}"
}

func (g *builtinGen) name(base string) string {
	g.names++
	return fmt.Sprintf("%s%d", base, g.names)
}

func (g *builtinGen) stmt() {
	k := g.r.IntN(50)
	switch g.r.IntN(12) {
	case 8:
		g.line(fmt.Sprintf("m = m.Put(%d, %d)", k%8, k))
		g.m[k%8] = k
		g.reached["put"] = true
	case 9:
		g.line(fmt.Sprintf("m = m.Delete(%d)", k%8))
		delete(g.m, k%8)
		g.reached["delete"] = true
	case 10:
		// A delete whose source is read again: the value rule writes a
		// copy, so m keeps the key.
		n := g.name("n")
		g.line(fmt.Sprintf("let %s = m.Delete(%d)", n, k%8))
		g.line(fmt.Sprintf("Console.Println(showMap(%s) + showMap(m))", n))
		kept := maps.Clone(g.m)
		delete(kept, k%8)
		g.want.WriteString(g.showMap(kept) + g.showMap(g.m) + "\n")
		g.reached["deleteKeep"] = true
	case 11:
		// A user extern of Go's delete keeps the same value rule.
		n := g.name("n")
		g.line(fmt.Sprintf("let %s = unset(m, %d)", n, k%8))
		g.line(fmt.Sprintf("Console.Println(showMap(%s) + showMap(m))", n))
		kept := maps.Clone(g.m)
		delete(kept, k%8)
		g.want.WriteString(g.showMap(kept) + g.showMap(g.m) + "\n")
		g.reached["unset"] = true
	case 0:
		g.line(fmt.Sprintf("xs = push(xs, %d)", k))
		g.xs = append(g.xs, k)
		g.reached["push"] = true
	case 1:
		// A new slice from xs; xs is read again after it.
		y := g.name("y")
		g.line(fmt.Sprintf("let %s = push(xs, %d)", y, k))
		g.line(fmt.Sprintf("Console.Println(show(%s) + show(xs))", y))
		g.want.WriteString(g.show(append(slices.Clone(g.xs), k)) + g.show(g.xs) + "\n")
		g.reached["keep"] = true
	case 2:
		// Two appends to one source: neither sees the other's element.
		y, z := g.name("y"), g.name("z")
		g.line(fmt.Sprintf("let %s = push(xs, %d)", y, k))
		g.line(fmt.Sprintf("let %s = push(xs, %d)", z, k+100))
		g.line(fmt.Sprintf("Console.Println(show(%s) + show(%s))", y, z))
		g.want.WriteString(g.show(append(slices.Clone(g.xs), k)) + g.show(append(slices.Clone(g.xs), k+100)) + "\n")
		g.reached["alias"] = true
	case 3:
		g.line(fmt.Sprintf("xs = xs.Append(%d)", k))
		g.xs = append(g.xs, k)
		g.reached["prelude"] = true
	case 4:
		g.line("Console.Println(\"\\(size(xs)) \\(room(xs) >= size(xs))\")")
		g.want.WriteString(fmt.Sprintf("%d true\n", len(g.xs)))
		g.reached["size"] = true
	case 5:
		// A copy into a borrow of a fresh slice of n zeros.
		n := g.r.IntN(4)
		d := g.name("d")
		g.line(fmt.Sprintf("let mut %s = []int{%s}", d, strings.TrimSuffix(strings.Repeat("0, ", n), ", ")))
		g.line(fmt.Sprintf("let n%s = copyInto(&%s, xs)", d, d))
		g.line(fmt.Sprintf("Console.Println(\"\\(n%s)\" + show(%s) + show(xs))", d, d))
		dst := make([]int, n)
		c := copy(dst, g.xs)
		g.want.WriteString(fmt.Sprintf("%d", c) + g.show(dst) + g.show(g.xs) + "\n")
		g.reached["copy"] = true
	case 6:
		// A clear through a borrow of a copy; xs keeps its elements.
		w := g.name("w")
		g.line(fmt.Sprintf("let mut %s = xs", w))
		g.line(fmt.Sprintf("wipe(&%s)", w))
		g.line(fmt.Sprintf("Console.Println(show(%s) + show(xs))", w))
		g.want.WriteString(g.show(make([]int, len(g.xs))) + g.show(g.xs) + "\n")
		g.reached["clear"] = true
	default:
		g.line(fmt.Sprintf("Console.Println(\"\\(least(size(xs), %d)) \\(most(size(xs), %d))\")", k%7, k%7))
		g.want.WriteString(fmt.Sprintf("%d %d\n", min(len(g.xs), k%7), max(len(g.xs), k%7)))
		g.reached["minmax"] = true
	}
}

// builtinProgram is the program of seed and the model's stdout.
func builtinProgram(seed uint64, reached map[string]bool) (src, want string) {
	g := &builtinGen{r: rand.New(rand.NewPCG(seed, 0x6275696c74)), m: map[int]int{}, reached: reached}
	g.line("let mut xs = []int{}")
	g.line("let mut m = Map[int, int]{}")
	// Each let mut is written once, so no seed leaves one unwritten (S110).
	g.line("xs = push(xs, 0)")
	g.xs = append(g.xs, 0)
	g.line("m = m.Put(0, 0)")
	g.m[0] = 0
	for range 20 {
		g.stmt()
	}
	g.line("Console.Println(show(xs) + showMap(m))")
	g.want.WriteString(g.show(g.xs) + g.showMap(g.m) + "\n")
	return builtinPrelude + "func main() needs Console {\n" + g.b.String() + "}\n", g.want.String()
}

// lowerBuiltinMode lowers src under the builtin lowering, or under the
// reference (ref: builtinLowering "call").
func lowerBuiltinMode(t *testing.T, name, src string, ref bool) *lowered {
	t.Helper()
	saved := builtinLowering
	defer func() { builtinLowering = saved }()
	builtinLowering = ""
	if ref {
		builtinLowering = "call"
	}
	return mustLower(t, name, src)
}

// directDelete matches Go's delete as a statement of main's Go (after a
// tab or a /*line*/ directive), not the reference's helper or its name.
var directDelete = regexp.MustCompile(`[\t/]delete\(`)

// TestBuiltinsAgree is the seeded property (see above).
//
// It builds Go because the question is the run of each program: the
// value rule's clip of an append is seen only in the elements a run
// prints, which no in-process check gives.
func TestBuiltinsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *builtinSeed != 0 {
		seeds = []uint64{*builtinSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := builtinProgram(seed, reached)
		runs = append(runs, run{seed, src, want})
		for _, ref := range []bool{false, true} {
			name := fmt.Sprintf("bi%d", seed)
			if ref {
				name += "ref"
			}
			p := lowerBuiltinMode(t, name, src, ref)
			// The builtin lowering writes each builtin at its call; the
			// reference writes none there.
			if direct := strings.Contains(p.mainGo(), "= append("); direct == ref {
				t.Errorf("seed %d (reference %v): main's Go calls append directly: %v", seed, ref, direct)
			}
			// The prelude's Delete binds Go's delete (OPT7): a statement at
			// its call under the builtin lowering, a Go function in the
			// reference.
			if strings.Contains(src, ".Delete(") {
				if direct := directDelete.MatchString(p.mainGo()); direct == ref {
					t.Errorf("seed %d (reference %v): main's Go calls delete directly: %v", seed, ref, direct)
				}
			}
			programs = append(programs, p)
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the builtin programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		name := fmt.Sprintf("bi%d", r.seed)
		out, stderr, code := runExe(t, exes[name])
		refOut, refErr, refCode := runExe(t, exes[name+"ref"])
		if out != r.want || refOut != r.want || code != 0 || refCode != 0 {
			t.Errorf("seed %d: the lowerings and the model differ\nReplay: go test -run '^TestBuiltinsAgree$' ./internal/sketch/lower -args -builtin.seed=%d\n%s\n--- builtin (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, refCode, refOut, refErr, r.want)
		}
	}
	if *builtinSeed != 0 {
		return
	}
	for _, path := range builtinPaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q: %v", path, reached)
		}
	}
}
