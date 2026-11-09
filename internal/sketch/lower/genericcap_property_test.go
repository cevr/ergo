package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The generic capabilities' seeded property (L4, #196 item 10; types.md,
// Capabilities 7). Each seed writes a program over k record types E0…Ek-1
// and two generic operation types, Store[T] and Tally[T], which one
// provider Box[T] implements. Functions need a random set of instances
// (Store[E1] | Tally[E0] | …) and call each once; main binds every
// instance in an outer with, rebinds random ones in nested withs, some
// entries with two keys (`Store[E0], Tally[E0]: &b`), and calls the
// functions in each frame. The generator is the model: it tracks which box
// each instance is bound to in each frame, so it knows each box's count.
// A lowering that gave two instances one environment field, a checker
// that let one instance's binding discharge another, or a parser that read
// a bracketed key as the end of a key list, prints other counts or does
// not build.
//
// Replay: go test -run '^TestGenericCapabilitiesAgree$' ./internal/sketch/lower -args -gcap.seed=N

var gcapSeed = flag.Uint64("gcap.seed", 0, "run TestGenericCapabilitiesAgree on this seed alone")

const gcapHelpers = `// law-exempt: the property's operation types.
type Store[T] {
	Put(v T),
}

// law-exempt: the property's operation types.
type Tally[T] {
	Bump(v T),
}

type Box[T] { n int }

func Put[T](b *Box[T], v T) { b.n = b.n + 1 }

func Bump[T](b *Box[T], v T) { b.n = b.n + 100 }
`

// gcapInstance is one capability instance: Store (0) or Tally (1) of Ek.
type gcapInstance struct{ op, elem int }

func (g gcapInstance) name() string {
	return fmt.Sprintf("%s[E%d]", []string{"Store", "Tally"}[g.op], g.elem)
}

// gcapProgram writes the program of seed and the output its model predicts.
func gcapProgram(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x6ca9))
	k := 2 + r.IntN(3)
	var b strings.Builder
	b.WriteString(gcapHelpers)
	var all []gcapInstance
	for e := 0; e < k; e++ {
		fmt.Fprintf(&b, "\ntype E%d { V int }\n", e)
		all = append(all, gcapInstance{0, e}, gcapInstance{1, e})
	}
	// Functions: each needs a random nonempty set of instances and calls
	// each once.
	nf := 2 + r.IntN(3)
	needs := make([][]gcapInstance, nf)
	for f := range needs {
		for _, g := range all {
			if r.IntN(3) == 0 {
				needs[f] = append(needs[f], g)
			}
		}
		if len(needs[f]) == 0 {
			needs[f] = []gcapInstance{all[r.IntN(len(all))]}
		}
		names := make([]string, len(needs[f]))
		for i, g := range needs[f] {
			names[i] = g.name()
		}
		fmt.Fprintf(&b, "\nfunc use%d() needs %s {\n", f, strings.Join(names, " | "))
		for _, g := range needs[f] {
			fmt.Fprintf(&b, "\t%s.%s(E%d{})\n", g.name(), []string{"Put", "Bump"}[g.op], g.elem)
		}
		b.WriteString("}\n")
	}
	// main: the boxes, then nested withs. A box is of one element type and
	// provides that element's Store and Tally.
	var boxes []int // box index -> element
	counts := []int{}
	newBox := func(e int) int {
		boxes = append(boxes, e)
		counts = append(counts, 0)
		return len(boxes) - 1
	}
	var body, decls strings.Builder
	var frame func(depth int, bound map[gcapInstance]int, rebind []gcapInstance)
	frame = func(depth int, bound map[gcapInstance]int, rebind []gcapInstance) {
		indent := strings.Repeat("\t", depth)
		inner := map[gcapInstance]int{}
		for g, x := range bound {
			inner[g] = x
		}
		// Entries: group the instances to rebind by element; an element
		// whose Store and Tally are both rebound may share one box in one
		// two-key entry.
		var entries []string
		done := map[gcapInstance]bool{}
		for _, g := range rebind {
			if done[g] {
				continue
			}
			x := newBox(g.elem)
			pair := gcapInstance{1 - g.op, g.elem}
			keys := []string{g.name()}
			done[g], inner[g] = true, x
			if gcapHas(rebind, pair) && !done[pair] && r.IntN(2) == 0 {
				keys = append(keys, pair.name())
				done[pair], inner[pair] = true, x
			}
			fmt.Fprintf(&decls, "\tlet mut b%d = Box[E%d]{}\n", x, g.elem)
			entries = append(entries, strings.Join(keys, ", ")+fmt.Sprintf(": &b%d", x))
		}
		fmt.Fprintf(&body, "%swith context { %s } {\n", indent, strings.Join(entries, ", "))
		calls := 1 + r.IntN(3)
		for i := 0; i < calls; i++ {
			if depth < 3 && r.IntN(3) == 0 {
				var sub []gcapInstance
				for _, g := range all {
					if r.IntN(3) == 0 {
						sub = append(sub, g)
					}
				}
				if len(sub) == 0 {
					sub = []gcapInstance{all[r.IntN(len(all))]}
				}
				frame(depth+1, inner, sub)
				continue
			}
			f := r.IntN(nf)
			fmt.Fprintf(&body, "%s\tuse%d()\n", indent, f)
			for _, g := range needs[f] {
				counts[inner[g]] += []int{1, 100}[g.op]
			}
		}
		fmt.Fprintf(&body, "%s}\n", indent)
	}
	frame(1, map[gcapInstance]int{}, all)
	b.WriteString("\nfunc main() needs Console {\n")
	b.WriteString(decls.String() + body.String())
	parts := make([]string, len(boxes))
	want := make([]string, len(boxes))
	for x := range boxes {
		parts[x] = fmt.Sprintf("\\(b%d.n)", x)
		want[x] = fmt.Sprint(counts[x])
	}
	b.WriteString("\tConsole.Println(\"" + strings.Join(parts, " ") + "\")\n}\n")
	return b.String(), strings.Join(want, " ") + "\n"
}

func gcapHas(gs []gcapInstance, g gcapInstance) bool {
	for _, x := range gs {
		if x == g {
			return true
		}
	}
	return false
}

// TestGenericCapabilitiesAgree is the seeded property (see above).
// It builds Go because the property is about which provider a call reaches
// when the program runs: the checker records the rows, and only the run
// shows that each instance's environment field holds its own provider.
func TestGenericCapabilitiesAgree(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if *gcapSeed != 0 {
		seeds = []uint64{*gcapSeed}
	}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := gcapProgram(seed)
		programs = append(programs, mustLower(t, fmt.Sprintf("gcap%d", seed), src))
		runs = append(runs, run{seed, src, want})
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the generic-capability programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[fmt.Sprintf("gcap%d", r.seed)])
		if out != r.want || code != 0 {
			t.Errorf("seed %d: the program and the binding model differ\nReplay: go test -run '^TestGenericCapabilitiesAgree$' ./internal/sketch/lower -args -gcap.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, r.want)
		}
	}
}
