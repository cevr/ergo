package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The seeded property of std slices.SortBy over a cmp.Order provider
// (CONSTRAINTS C3). Each seed writes one program: a record
// R { A int, B string, C int } with small value ranges, so ties are
// common; two providers, each a generated order (a list of R's fields,
// each forward or reversed, chained with cmp.Then), one with a field that
// picks the direction at run time; and a provider Chaos whose Compare is
// no order at all. main sorts seeded slices by each and prints them. The
// model is an insertion sort in this test with the same compare, and
// since SortBy keeps no order among equal elements, a sorted line prints
// only the fields its order reads. For Chaos, the program prints the
// sorted multiset of what SortBy returned, which must be the input's: an
// unlawful order gives some order of the same elements, ends and stays
// in xs. It runs under the known-witness lowering and under the reference
// (witnessLowering "dynamic"); stdout must equal the model's under both. A
// failing seed prints the Ergo source, both outputs and its replay
// command.
//
// It builds Go because SortBy's copies for a provider and the reference's
// witness are what it compares, and only the built program runs them.
//
// Mutants it alone catches (std testdata/mutants.json):
// c3-sortby-unguarded (the partition's bounds guard dropped: Chaos reads
// outside xs). The others, c3-sortby-last-unsorted, c3-partition-le,
// c3-sortby-short-len, c3-reverse-forward and c3-then-next-first, are also
// killed by TestStdSlicesLawsRun.

var sortSeed = flag.Uint64("sortby.seed", 0, "run TestSortByAgrees on this seed alone")

type sortKey struct {
	field byte // 'A', 'B' or 'C'
	rev   bool
}

type sortRec struct {
	A int
	B string
	C int
}

func (r sortRec) cmp(k sortKey, s sortRec) int {
	var c int
	switch k.field {
	case 'A':
		c = cmpOrdered(r.A, s.A)
	case 'B':
		c = cmpOrdered(r.B, s.B)
	default:
		c = cmpOrdered(r.C, s.C)
	}
	if k.rev {
		return -c
	}
	return c
}

func cmpOrdered[T int | string](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (r sortRec) show(keys []sortKey) string {
	var parts []string
	for _, k := range keys {
		switch k.field {
		case 'A':
			parts = append(parts, fmt.Sprint(r.A))
		case 'B':
			parts = append(parts, r.B)
		default:
			parts = append(parts, fmt.Sprint(r.C))
		}
	}
	return strings.Join(parts, "/")
}

func sortKeys(r *rand.Rand) []sortKey {
	fields := []byte{'A', 'B', 'C'}
	r.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	keys := make([]sortKey, 1+r.IntN(3))
	for i := range keys {
		keys[i] = sortKey{fields[i], r.IntN(2) == 0}
	}
	return keys
}

// sortCompareSrc is the Ergo body of a compare by keys, of a and b.
func sortCompareSrc(keys []sortKey, flip string) string {
	var terms []string
	for _, k := range keys {
		a, b := "a", "b"
		if k.rev {
			a, b = b, a
		}
		terms = append(terms, fmt.Sprintf("cmp.Compare(%s.%c, %s.%c)", a, k.field, b, k.field))
	}
	body := terms[len(terms)-1]
	for i := len(terms) - 2; i >= 0; i-- {
		body = "cmp.Then(" + terms[i] + ", " + body + ")"
	}
	if flip != "" {
		return "let c = " + body + "\n\tif " + flip + " {\n\t\treturn -c\n\t}\n\tc"
	}
	return body
}

func sortByProgram(seed uint64) (src, want string) {
	r := rand.New(rand.NewPCG(seed, 0x50b7))
	k1, k2 := sortKeys(r), sortKeys(r)
	var b, out strings.Builder
	b.WriteString("import cmp\nimport slices\nimport strings\n\ntype R { A int, B string, C int }\n\n")
	fmt.Fprintf(&b, "type First {} implements cmp.Order[R]\n\nfunc Compare(_ First, a R, b R) int {\n\t%s\n}\n\n", sortCompareSrc(k1, ""))
	fmt.Fprintf(&b, "type Second { Down bool } implements cmp.Order[R]\n\nfunc Compare(s Second, a R, b R) int {\n\t%s\n}\n\n", sortCompareSrc(k2, "s.Down"))
	b.WriteString("type Chaos { K int } implements cmp.Order[R]\n\nfunc Compare(o Chaos, a R, b R) int { ((a.A*7 + b.C*3 + o.K) % 3 + 3) % 3 - 1 }\n\n")
	// showN prints the fields order N reads, in its order.
	for i, keys := range [][]sortKey{k1, k2} {
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("\"\\(x.%c)\"", k.field))
		}
		fmt.Fprintf(&b, "func show%d(xs []R) string {\n\tstrings.Join(slices.Transform(xs, func(x R) string { strings.Join([]string{%s}, \"/\") }), \" \")\n}\n\n", i+1, strings.Join(parts, ", "))
	}
	b.WriteString("func items(xs []R) string {\n\tstrings.Join(slices.Sort(slices.Transform(xs, func(x R) string { \"\\(x.A)/\\(x.B)/\\(x.C)\" })), \" \")\n}\n\n")
	b.WriteString("func main() needs Console {\n")
	for c := range 4 {
		n := r.IntN(40)
		if c == 0 {
			n = r.IntN(3)
		}
		xs := make([]sortRec, n)
		var lits []string
		for i := range xs {
			xs[i] = sortRec{r.IntN(4), string(rune('p' + r.IntN(3))), r.IntN(3)}
			lits = append(lits, fmt.Sprintf("R{A: %d, B: %q, C: %d}", xs[i].A, xs[i].B, xs[i].C))
		}
		fmt.Fprintf(&b, "\tlet xs%d = []R{%s}\n", c, strings.Join(lits, ", "))
		down := r.IntN(2) == 0
		k := r.IntN(5)
		fmt.Fprintf(&b, "\tConsole.Println(show1(slices.SortBy(xs%d, First)))\n", c)
		fmt.Fprintf(&b, "\tConsole.Println(show2(slices.SortBy(xs%d, Second{Down: %v})))\n", c, down)
		fmt.Fprintf(&b, "\tConsole.Println(\"\\(items(slices.SortBy(xs%d, Chaos{K: %d})) == items(xs%d))\")\n", c, k, c)
		model := func(less func(a, b sortRec) int, keys []sortKey) {
			ys := slices.Clone(xs)
			for i := 1; i < len(ys); i++ {
				for j := i; j > 0 && less(ys[j], ys[j-1]) < 0; j-- {
					ys[j], ys[j-1] = ys[j-1], ys[j]
				}
			}
			var parts []string
			for _, y := range ys {
				parts = append(parts, y.show(keys))
			}
			out.WriteString(strings.Join(parts, " ") + "\n")
		}
		byKeys := func(keys []sortKey, flip bool) func(a, b sortRec) int {
			return func(a, b sortRec) int {
				c := 0
				for _, k := range keys {
					if c = a.cmp(k, b); c != 0 {
						break
					}
				}
				if flip {
					return -c
				}
				return c
			}
		}
		model(byKeys(k1, false), k1)
		model(byKeys(k2, down), k2)
		out.WriteString("true\n")
	}
	b.WriteString("}\n")
	return b.String(), out.String()
}

// TestSortByAgrees is the property above. It builds Go because SortBy's
// copies for a provider and the reference's witness are what it
// compares, and only the built program runs them.
func TestSortByAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *sortSeed != 0 {
		seeds = []uint64{*sortSeed}
	}
	srcs, wants := map[uint64]string{}, map[uint64]string{}
	var programs []*lowered
	for _, seed := range seeds {
		srcs[seed], wants[seed] = sortByProgram(seed)
		for _, mode := range []string{"", "dynamic"} {
			saved := witnessLowering
			witnessLowering = mode
			programs = append(programs, mustLower(t, fmt.Sprintf("sortby%d%s", seed, mode), srcs[seed]))
			witnessLowering = saved
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the SortBy programs do not build (seeds %v)", seeds)
	}
	for _, seed := range seeds {
		on, onErr, onCode := runExe(t, exes[fmt.Sprintf("sortby%d", seed)])
		ref, refErr, refCode := runExe(t, exes[fmt.Sprintf("sortby%ddynamic", seed)])
		if on != wants[seed] || ref != wants[seed] || onCode != 0 || refCode != 0 {
			t.Errorf("seed %d: SortBy and the model differ\nReplay: go test -run '^TestSortByAgrees$' ./internal/sketch/lower -args -sortby.seed=%d\n%s\n--- copies (exit %d)\n%s%s\n--- reference (exit %d)\n%s%s\n--- model\n%s", seed, seed, srcs[seed], onCode, on, onErr, refCode, ref, refErr, wants[seed])
		}
	}
}
