package check

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The overlap rule's seeded property (#197 C2; types.md, Borrows 2). Each
// seed writes a main whose calls take two borrows: of two elements of one
// slice (each index a constant, an immutable let, or a let mut), of the
// same or two fields of one record, of a record and its field, of places
// in two roots, by `&` or by a dot call's implicit borrow of its receiver.
// A call is bare or under a guard: in the body of `if a != b` (alone or
// after a conjunct), in the else of `if a == b`, or after
// `if a == b { continue }` in a loop. The reference decides each call:
// two places apart (two roots, two fields, two different constants),
// overlapping (one place, a prefix, one index), or "may overlap" (two
// indexes that are neither), which a guard on the two indexes, each a
// constant or an immutable let, makes apart. The checker's S108 lines and
// kinds must be the reference's.
//
// Replay: go test -run '^TestBorrowOverlapModel$' ./internal/sketch/check -args -borrow.seed=N

var borrowSeed = flag.Uint64("borrow.seed", 0, "run TestBorrowOverlapModel on this seed alone")

const borrowPrelude = `type C { n int }

type P { a C, b C }

func two(x *C, y *C) {
	x.n += 1
	y.n += 2
}

func whole(x *P, y *C) {
	x.a.n += 1
	y.n += 2
}

func main() needs Console {
	let mut xs = []C{C{n: 0}, C{n: 0}, C{n: 0}}
	let mut p = P{a: C{n: 0}, b: C{n: 0}}
	let mut q = P{a: C{n: 0}, b: C{n: 0}}
	let i = len(xs) - 3
	let j = len(xs) - 2
	let mut k = 0
	k = len(xs) - 1
	xs[0].n = 1
	p.a.n = 1
	q.a.n = 1
`

// borrowIndexes are the index operands: two constants, two lets, a let mut.
var borrowIndexes = []string{"0", "1", "i", "j", "k"}

func isConst(x string) bool { return x == "0" || x == "1" }

// borrowVerdict is the reference: "" apart, "one" overlapping, "may" may
// overlap, for two indexes a and b of one slice under a guard on guard (a
// pair of indexes, or nil).
func borrowVerdict(a, b string, guard []string) string {
	switch {
	case isConst(a) && isConst(b):
		if a == b {
			return "one"
		}
		return ""
	case a == b:
		return "one"
	}
	if guard != nil && a != "k" && b != "k" && ((guard[0] == a && guard[1] == b) || (guard[0] == b && guard[1] == a)) {
		return ""
	}
	return "may"
}

// borrowProgram is seed's source and the verdict of each call's line.
func borrowProgram(seed uint64) (string, map[int]string) {
	r := rand.New(rand.NewPCG(seed, 0xb0220))
	var b strings.Builder
	b.WriteString(borrowPrelude)
	line := strings.Count(borrowPrelude, "\n")
	emit := func(depth int, format string, args ...any) int {
		line++
		b.WriteString(strings.Repeat("\t", depth))
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\n")
		return line
	}
	want := map[int]string{}
	for range 2 + r.IntN(5) {
		x := borrowIndexes[r.IntN(len(borrowIndexes))]
		y := borrowIndexes[r.IntN(len(borrowIndexes))]
		g := borrowIndexes[r.IntN(len(borrowIndexes))]
		h := borrowIndexes[r.IntN(len(borrowIndexes))]
		var call, verdict string
		guarded := true
		switch r.IntN(6) {
		case 0, 1:
			call = fmt.Sprintf("two(&xs[%s], &xs[%s])", x, y)
		case 2:
			call = fmt.Sprintf("xs[%s].two(&xs[%s])", x, y)
		case 3:
			guarded = false
			fields := []string{"p.a", "p.b", "q.a"}
			f1, f2 := fields[r.IntN(3)], fields[r.IntN(3)]
			call = fmt.Sprintf("two(&%s, &%s)", f1, f2)
			if f1 == f2 {
				verdict = "one"
			}
		case 4:
			guarded = false
			roots := []string{"p", "q"}
			r1, r2 := roots[r.IntN(2)], roots[r.IntN(2)]
			call = fmt.Sprintf("whole(&%s, &%s.%s)", r1, r2, []string{"a", "b"}[r.IntN(2)])
			if r1 == r2 {
				verdict = "one"
			}
		case 5:
			guarded = false
			call = fmt.Sprintf("two(&xs[%s], &p.a)", x)
		}
		at := 0
		if !guarded {
			at = emit(1, "%s", call)
			if verdict != "" {
				want[at] = verdict
			}
			continue
		}
		var guard []string
		switch r.IntN(5) {
		case 0:
			at = emit(1, "%s", call)
		case 1:
			emit(1, "if %s != %s {", g, h)
			at = emit(2, "%s", call)
			emit(1, "}")
			guard = []string{g, h}
		case 2:
			emit(1, "if len(xs) > 0 && %s != %s {", g, h)
			at = emit(2, "%s", call)
			emit(1, "}")
			guard = []string{g, h}
		case 3:
			emit(1, "if %s == %s {", g, h)
			emit(1, "} else {")
			at = emit(2, "%s", call)
			emit(1, "}")
			guard = []string{g, h}
		case 4:
			emit(1, "for _ in 0..1 {")
			emit(2, "if %s == %s {", g, h)
			emit(3, "continue")
			emit(2, "}")
			at = emit(2, "%s", call)
			emit(1, "}")
			guard = []string{g, h}
		}
		if v := borrowVerdict(x, y, guard); v != "" {
			want[at] = v
		}
	}
	b.WriteString("\tConsole.Println(\"\\(xs[0].n) \\(p.a.n) \\(q.a.n) \\(i) \\(j) \\(k)\")\n}\n")
	return b.String(), want
}

// TestBorrowOverlapModel is the property (see above).
func TestBorrowOverlapModel(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(300) {
		seeds = append(seeds, s+1)
	}
	if *borrowSeed != 0 {
		seeds = []uint64{*borrowSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, want := borrowProgram(seed)
		_, diags := CheckSource(src)
		got := map[int]string{}
		for _, d := range diags {
			switch {
			case d.Code == "S108" && strings.Contains(d.Message, "may be borrows of one place"):
				got[d.Line] = "may"
			case d.Code == "S108" && strings.Contains(d.Message, "are borrows of one place"):
				got[d.Line] = "one"
			default:
				got[d.Line] = d.String()
			}
		}
		if !mapsEqual(got, want) {
			t.Errorf("seed %d: the checker says %v, the reference %v\nReplay: go test -run '^TestBorrowOverlapModel$' ./internal/sketch/check -args -borrow.seed=%d\n%s", seed, got, want, seed, src)
		}
		for _, v := range want {
			reached[v] = true
		}
		if len(want) == 0 {
			reached["admitted"] = true
		}
	}
	for _, v := range []string{"one", "may", "admitted"} {
		if *borrowSeed == 0 && !reached[v] {
			t.Errorf("no seed reaches %q", v)
		}
	}
}

func mapsEqual(a, b map[int]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]int, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if b[k] != a[k] {
			return false
		}
	}
	return true
}
