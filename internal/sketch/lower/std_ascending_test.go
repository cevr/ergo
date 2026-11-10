package lower

import (
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A sorted slice is a type (std-strengths SS5; stdlib.md, Slices and Maps
// 8): slices.BinarySearch takes a slices.Ascending[T], which only
// slices.Ascend builds, so an unsorted slice is a check error.

// TestAscendingIsTheOnlyWayIn: outside slices, every way to make an
// Ascending other than Ascend is refused, and the ways that make no new
// value check clean. The check fixture
// internal/sketch/check/testdata/fixtures/negative/ascending-is-the-only-way-in.ergo
// mirrors it for TestFixtures.
func TestAscendingIsTheOnlyWayIn(t *testing.T) {
	const head = "import slices\n\ntype Mine slices.Ascending[int]\n\n"
	const tail = "\nfunc main() {}\n"
	for _, c := range []struct{ name, src, code, message string }{
		{"unsorted slice", "func f() Option[int] { slices.BinarySearch([]int{3, 1, 2}, 1) }\n", "S108", "the argument has type []int, which does not match slices.Ascending[T]"},
		{"empty literal", "func f() slices.Ascending[int] { slices.Ascending[int]{} }\n", "S114", "a literal of slices.Ascending[int] builds a new value: slices.Ascending has an unexported field, so only package slices builds it; use slices.Ascend"},
		{"keyed literal", "func f() slices.Ascending[int] { slices.Ascending[int]{xs: []int{2, 1}} }\n", "S114", "a literal of slices.Ascending[int] builds a new value"},
		{"zero value", "func f() int {\n\tlet a slices.Ascending[int]\n\tlen(a.Items())\n}\n", "S114", "let a of type slices.Ascending[int] needs a value: its zero value builds a new value of slices.Ascending"},
		{"defined type literal", "func f() Mine { Mine{} }\n", "S114", "a literal of Mine, defined over slices.Ascending, builds a new value"},
		{"record update", "func f(a slices.Ascending[int]) slices.Ascending[int] { slices.Ascending[int]{..a, xs: []int{2, 1}} }\n", "S114", "a record update of slices.Ascending[int] builds a new value"},
		{"property parameter", "test Generated(a slices.Ascending[int]) {\n\tassert len(a.Items()) >= 0\n}\n", "S108", "a property parameter is generated, and slices.Ascending[int] cannot be"},
		{"ways that make no new value", "func f(xs []int) Option[int] {\n\tlet a = slices.Ascend(xs)\n\tlet b = a\n\tlet m = Mine(b)\n\tlet back = slices.Ascending[int](m)\n\tslices.BinarySearch(back, 1)\n}\n", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := lowerSource("ascending", head+c.src+tail, true)
			var got []string
			for _, d := range o.diags {
				got = append(got, d.Code+" "+d.Message)
			}
			if c.code == "" {
				if len(got) != 0 {
					t.Errorf("want no diagnostic, got %q", got)
				}
				return
			}
			// A literal or an update that names the unexported field xs is
			// also S104 at the field: the field is not visible outside.
			want := 1
			if strings.Contains(c.src, "xs: ") {
				want = 2
			}
			if len(o.diags) != want || o.diags[0].Code != c.code || !strings.Contains(o.diags[0].Message, c.message) {
				t.Errorf("want %d diagnostics, the first %s with %q; got %q", want, c.code, c.message, got)
			}
			if want == 2 && (o.diags[1].Code != "S104" || !strings.Contains(o.diags[1].Message, "xs is not an exported field of slices.Ascending")) {
				t.Errorf("want S104 at the unexported field xs second; got %q", got)
			}
		})
	}
}

// ascendingProgram reads cases from standard input, one per line, "x|a,b,c",
// and prints the index BinarySearch(Ascend(xs), x) finds, or none.
const ascendingProgram = `import io
import slices
import strconv
import strings

func toInt(s string) int {
	let n = strconv.Atoi(s) catch {
	case strconv.Malformed, strconv.OutOfRange:
		-1
	}
	n
}

func main() fails io.ReadFailed needs Console | Ambient {
	let all = with context { io.Stdin: io.NewStdin() } { try io.Stdin.ReadAll() }
	for line in strings.Lines(all) {
		let fields = strings.Split(strings.TrimSuffix(line, "\n"), "|")
		let x = toInt(fields[0])
		let mut xs = []int{}
		if fields[1] != "" {
			for f in strings.Split(fields[1], ",") {
				xs = xs.Append(toInt(f))
			}
		}
		match slices.BinarySearch(slices.Ascend(xs), x) {
		case Some[int] as i:
			Console.Println("\(i.Value)")
		case None:
			Console.Println("none")
		}
	}
}
`

// ascendingCases are n seeded ([]int, int) cases: empty and one-element
// slices, duplicate-heavy slices over a small range, the extremes of int,
// and a target that is in the slice, next to it, or an extreme.
func ascendingCases(seed uint64, n int) []struct {
	xs []int
	x  int
} {
	r := rand.New(rand.NewPCG(seed, 0xa5c))
	extremes := []int{math.MinInt64, math.MinInt64 + 1, -1, 0, 1, math.MaxInt64 - 1, math.MaxInt64}
	value := func() int {
		switch r.IntN(4) {
		case 0:
			return extremes[r.IntN(len(extremes))]
		case 1:
			return r.IntN(5) - 2
		default:
			return r.IntN(40) - 20
		}
	}
	out := []struct {
		xs []int
		x  int
	}{{nil, 0}, {[]int{1}, 1}, {[]int{1}, 0}, {[]int{2, 2, 2}, 2}, {[]int{math.MaxInt64, math.MinInt64}, math.MinInt64}, {[]int{3, 1, 2}, 1}}
	for len(out) < n {
		size := r.IntN(12)
		if r.IntN(10) == 0 {
			size = 50 + r.IntN(150)
		}
		xs := make([]int, size)
		for i := range xs {
			xs[i] = value()
		}
		x := value()
		if size > 0 && r.IntN(2) == 0 {
			x = xs[r.IntN(size)] + r.IntN(3) - 1
			if r.IntN(2) == 0 {
				x = xs[r.IntN(size)]
			}
		}
		out = append(out, struct {
			xs []int
			x  int
		}{xs, x})
	}
	return out
}

// TestStdAscendingMatchesGo: on 10,000 seeded cases, BinarySearch(Ascend(xs),
// x) is Some(i) exactly when Go's slices.BinarySearch of the sorted xs gives
// (i, true), i the first index of x. The seed is 1, or ERGO_SEED; a failure
// prints the seed, the case and both answers.
//
// It builds Go because it compares the runs of built programs with a
// reference (another lowering, a fold, or Go's own package), and the
// question is behaviour at run time; no in-process check observes that, and
// Ergo has no reference interpreter yet (test-speed T8).
func TestStdAscendingMatchesGo(t *testing.T) {
	needsBuild(t)
	seed := uint64(1)
	if s := os.Getenv("ERGO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SEED=%q: %v", s, err)
		}
		seed = v
	}
	cases := ascendingCases(seed, 10000)
	exes := tableExes(t, "ascending")
	if exes == nil {
		return
	}
	var in strings.Builder
	for _, c := range cases {
		parts := make([]string, len(c.xs))
		for i, v := range c.xs {
			parts[i] = strconv.Itoa(v)
		}
		in.WriteString(strconv.Itoa(c.x) + "|" + strings.Join(parts, ",") + "\n")
	}
	stdout, stderr, code := runExeInput(t, exes["ascending"], in.String())
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s", seed, code, firstLines(stderr, 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("seed %d: %d output lines for %d cases", seed, len(got), len(cases))
	}
	failed := 0
	for i, c := range cases {
		sorted := slices.Clone(c.xs)
		slices.Sort(sorted)
		want := "none"
		if j, ok := slices.BinarySearch(sorted, c.x); ok {
			want = strconv.Itoa(j)
		}
		if got[i] != want && failed < 5 {
			failed++
			t.Errorf("seed %d, case %d: BinarySearch(Ascend(%v), %d) is %s, Go's slices.BinarySearch of %v gives %s (replay with ERGO_SEED=%d)", seed, i, c.xs, c.x, got[i], sorted, want, seed)
		}
	}
}

// BenchmarkAscendSearch (SS5): Ascend of 100,000 ints, then 1,000
// BinarySearch calls, against hand Go slices.Clone, slices.Sort and 1,000
// slices.BinarySearch calls. Both sum the found indices.
func BenchmarkAscendSearch(b *testing.B) {
	const src = `import slices

func SearchAll(xs []int, probes []int) int {
	let a = slices.Ascend(xs)
	let mut n = 0
	for p in probes {
		match a.BinarySearch(p) {
		case Some[int] as i:
			n = n + i.Value
		case None:
			n = n - 1
		}
	}
	n
}

func main() needs Console {
	Console.Println("\(SearchAll([]int{3, 1, 2}, []int{1}))")
}
`
	emittedBench(b, "ascendsearch", src, func(mainGo string) string {
		return `package main

import (
	"math/rand/v2"
	"slices"
	"testing"
)

var searchXs, searchProbes = func() ([]int, []int) {
	r := rand.New(rand.NewPCG(1, 2))
	xs := make([]int, 100000)
	for i := range xs {
		xs[i] = r.IntN(1 << 20)
	}
	probes := make([]int, 1000)
	for i := range probes {
		probes[i] = r.IntN(1 << 20)
	}
	return xs, probes
}()
var sink int

func BenchmarkAscendSearchErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "SearchAll", "searchXs, searchProbes") + `
	}
}

func BenchmarkAscendSearchGo(b *testing.B) {
	for range b.N {
		s := slices.Clone(searchXs)
		slices.Sort(s)
		n := 0
		for _, p := range searchProbes {
			if i, ok := slices.BinarySearch(s, p); ok {
				n += i
			} else {
				n--
			}
		}
		sink = n
	}
}
`
	}, "AscendSearchErgo", "AscendSearchGo")
}
