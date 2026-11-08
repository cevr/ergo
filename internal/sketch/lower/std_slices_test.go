package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestStdSlicesRuns (std-align S2a): each function of std slices, maps and
// cmp lowers and runs. A program sorts records by three keys with
// SortStableFunc, prints slices.Sorted(maps.Keys(m)), and runs
// Filter(Transform(xs, f), p); a comparator is a function literal, a
// capturing closure and a named function; a generic function over an
// ordered T calls Sort, Max and BinarySearch with its own T; a fallible
// Transform callback fails through Transform's row into a catch.
func TestStdSlicesRuns(t *testing.T) {
	runBehaviors(t, []behavior{
		{name: "slices", src: stdSlicesProgram, stdout: stdSlicesOutput},
	})
}

const stdSlicesProgram = `import slices
import maps
import cmp
import strings

type Repo { Name string, Stars int, Registry string }

type TooBig { N int }

func byStars(a Repo, b Repo) int { cmp.Compare(b.Stars, a.Stars) }

func show(rs []Repo) string {
	strings.Join(slices.Transform(rs, func(r Repo) string { "\(r.Registry)/\(r.Name):\(r.Stars)" }), " ")
}

func ints(xs []int) string {
	strings.Join(slices.Transform(xs, func(x int) string { "\(x)" }), ",")
}

func opt(o Option[int]) string {
	match o {
	case Some[int] as s:
		"\(s.Value)"
	case None:
		"none"
	}
}

func top[T ordered](xs []T) Option[T] { slices.Max(slices.Sort(xs)) }

func where[T ordered](xs []T, x T) Option[int] { slices.BinarySearch(slices.Sort(xs), x) }

func halve(x int) int fails TooBig {
	if x > 100 {
		return TooBig{N: x}
	}
	x / 2
}

func main() needs Console {
	let repos = []Repo{
		Repo{Name: "b", Stars: 5, Registry: "npm"},
		Repo{Name: "a", Stars: 5, Registry: "github"},
		Repo{Name: "c", Stars: 9, Registry: "npm"},
		Repo{Name: "a", Stars: 5, Registry: "npm"},
		Repo{Name: "d", Stars: 1, Registry: "github"},
	}
	// Three keys, least significant first, each sort stable.
	let byName = slices.SortStableFunc(repos, func(a Repo, b Repo) int { cmp.Compare(a.Name, b.Name) })
	let byRegistry = slices.SortStableFunc(byName, func(a Repo, b Repo) int { cmp.Compare(a.Registry, b.Registry) })
	Console.Println(show(slices.SortStableFunc(byRegistry, byStars)))
	let reverse = -1
	Console.Println(show(slices.SortFunc(repos, func(a Repo, b Repo) int { reverse * cmp.Compare(a.Name + a.Registry, b.Name + b.Registry) })))
	Console.Println(show(repos))

	let m = Map[string, int]{"pear": 3, "apple": 1, "fig": 2}
	Console.Println(strings.Join(slices.Sorted(maps.Keys(m)), ","))
	Console.Println(ints(slices.Sorted(maps.Values(m))))

	let xs = []int{5, 2, 8, 2, 9, 1}
	let evens = slices.Filter(slices.Transform(xs, func(x int) int { x * 3 }), func(x int) bool { x % 2 == 0 })
	Console.Println(ints(evens))
	Console.Println(ints(slices.Sort(xs)) + " " + ints(xs))
	Console.Println(ints(slices.Reverse(xs)))
	Console.Println("\(slices.Contains(xs, 8)) \(slices.Contains(xs, 7)) \(slices.Index(xs, 2)) \(slices.Index(xs, 7))")
	Console.Println("\(slices.IndexFunc(xs, func(x int) bool { x > 6 })) \(slices.IndexFunc(xs, func(x int) bool { x > 60 }))")
	Console.Println(opt(slices.Max(xs)) + " " + opt(slices.Min(xs)) + " " + opt(slices.Max([]int{})) + " " + opt(slices.Min([]int{})))
	Console.Println(opt(slices.BinarySearch(slices.Sort(xs), 2)) + " " + opt(slices.BinarySearch(slices.Sort(xs), 3)))
	Console.Println("\(slices.Equal(xs, []int{5, 2, 8, 2, 9, 1})) \(slices.Equal(xs, slices.Sort(xs)))")
	Console.Println(ints(slices.Collect(slices.Values(xs))))
	Console.Println("\(slices.Fold(xs, 0, func(acc int, x int) int { acc + x }))")
	let mut seen = 0
	for x in slices.Values(xs) {
		if x == 8 {
			break
		}
		seen = seen + 1
	}
	Console.Println("\(seen)")
	Console.Println(opt(top([]int{3, 7, 4})) + " " + opt(where([]string{"b", "a", "c"}, "c")))
	match top([]duration{2s, 1h, 3ms}) {
	case Some[duration] as d:
		Console.Println("\(d.Value == 1h)")
	case None:
		Console.Println("none")
	}
	Console.Println("\(cmp.Compare(1, 2)) \(cmp.Compare("b", "a")) \(cmp.Compare(2.5, 2.5)) \(cmp.Less(1, 2)) \(cmp.Less(2, 1))")
	let halves = slices.Transform([]int{4, 6}, halve) catch {
	case TooBig as e:
		[]int{e.N}
	}
	let failed = slices.Transform([]int{4, 600, 8}, halve) catch {
	case TooBig as e:
		[]int{e.N}
	}
	Console.Println(ints(halves) + " " + ints(failed))
	let w = slices.WithCapacity[string](4)
	Console.Println("\(len(w))")
}
`

const stdSlicesOutput = "npm/c:9 github/a:5 npm/a:5 npm/b:5 github/d:1\n" +
	"github/d:1 npm/c:9 npm/b:5 npm/a:5 github/a:5\n" +
	"npm/b:5 github/a:5 npm/c:9 npm/a:5 github/d:1\n" +
	"apple,fig,pear\n" +
	"1,2,3\n" +
	"6,24,6\n" +
	"1,2,2,5,8,9 5,2,8,2,9,1\n" +
	"1,9,2,8,2,5\n" +
	"true false 1 -1\n" +
	"2 -1\n" +
	"9 1 none none\n" +
	"1 none\n" +
	"true false\n" +
	"5,2,8,2,9,1\n" +
	"27\n" +
	"2\n" +
	"7 2\n" +
	"true\n" +
	"-1 1 0 true false\n" +
	"2,3 600\n" +
	"0\n"

// TestStdSlicesEffectCases ports the cases of Effect's Array tests
// (packages/effect/test/Array.test.ts at the conformance pin 460272d3)
// that std slices covers: reverse, sort, contains, map, filter, reduce,
// min, max and sortBy with two keys. Effect's min and max take a
// NonEmptyArray, so an empty one is a type error there; here it is None
// (conformance/effect-cases.json records the difference).
func TestStdSlicesEffectCases(t *testing.T) {
	runBehaviors(t, []behavior{{name: "effectcases", src: `import slices
import cmp
import strings

type X { A string, B int }

func ints(xs []int) string {
	"[" + strings.Join(slices.Transform(xs, func(x int) string { "\(x)" }), ",") + "]"
}

func xs(v []X) string {
	strings.Join(slices.Transform(v, func(x X) string { "\(x.A)\(x.B)" }), " ")
}

func opt(o Option[int]) string {
	match o {
	case Some[int] as s:
		"\(s.Value)"
	case None:
		"none"
	}
}

func main() needs Console {
	// reverse (Array.test.ts:523)
	Console.Println(ints(slices.Reverse([]int{})) + ints(slices.Reverse([]int{1})) + ints(slices.Reverse([]int{1, 2, 3})))
	// sort (Array.test.ts:533)
	Console.Println(ints(slices.Sort([]int{})) + ints(slices.Sort([]int{1, 3, 2})))
	// contains (Array.test.ts:645)
	Console.Println("\(slices.Contains([]int{1, 2, 3}, 2)) \(slices.Contains([]int{1, 2, 3}, 0))")
	// map (Array.test.ts:834)
	Console.Println(ints(slices.Transform([]int{1, 2, 3}, func(n int) int { n * 2 })))
	// filter (Array.test.ts:886)
	Console.Println(ints(slices.Filter([]int{1, 2, 3}, func(n int) bool { n % 2 == 1 })))
	// reduce (Array.test.ts:924)
	Console.Println(slices.Fold([]string{"a", "b", "c"}, "", func(b string, a string) string { b + a }))
	// min and max (Array.test.ts:1052, 1057), and an empty slice
	Console.Println(opt(slices.Min([]int{2, 1, 3})) + " " + opt(slices.Min([]int{3})) + " " + opt(slices.Max([]int{1, 2, 3})) + " " + opt(slices.Max([]int{1})) + " " + opt(slices.Max([]int{})))
	// sortBy(byName, byAge) (Array.test.ts:1120): a stable sort by the
	// last key, then by the first.
	let v = []X{X{A: "a", B: 1}, X{A: "b", B: 3}, X{A: "c", B: 2}, X{A: "b", B: 2}}
	let byAge = slices.SortStableFunc(v, func(p X, q X) int { cmp.Compare(p.B, q.B) })
	Console.Println(xs(slices.SortStableFunc(byAge, func(p X, q X) int { cmp.Compare(p.A, q.A) })))
	Console.Println(xs(slices.SortFunc(v, func(p X, q X) int {
		let c = cmp.Compare(p.A, q.A)
		if c != 0 {
			return c
		}
		cmp.Compare(p.B, q.B)
	})))
}
`, stdout: "[][1][3,2,1]\n[][1,2,3]\ntrue false\n[2,4,6]\n[1,3]\nabc\n1 3 3 1 none\na1 b2 b3 c2\na1 b2 b3 c2\n"}})
}

// stdSlicesLawsProbe restates each law of slices, maps and cmp whose
// ledger runner is TestStdSlicesLawsRun, as a property test of its own
// name (stdLawsTestName), its parameters drawn by the property runner.
const stdSlicesLawsProbe = `import slices
import maps
import cmp

type tagged { Key int, At int }

func isSorted(xs []int) bool {
	for i in 1..len(xs) {
		if xs[i - 1] > xs[i] {
			return false
		}
	}
	true
}

func count(xs []int, x int) int {
	len(slices.Filter(xs, func(y int) bool { y == x }))
}

func byKey(a tagged, b tagged) int { cmp.Compare(a.Key, b.Key) }

test SlicesSortOrders(xs []int) { assert isSorted(slices.Sort(xs)) }

test SlicesSortPermutes(xs []int) {
	let sorted = slices.Sort(xs)
	assert len(sorted) == len(xs)
	for x in xs {
		assert count(sorted, x) == count(xs, x)
	}
}

test SlicesSortIsIdempotent(xs []int) {
	assert slices.Equal(slices.Sort(slices.Sort(xs)), slices.Sort(xs))
}

test SlicesSortLeavesItsArgument(xs []int) {
	let before = slices.Transform(xs, func(x int) int { x })
	let sorted = slices.Sort(xs)
	assert len(sorted) == len(xs) && slices.Equal(xs, before)
	let byDesc = slices.SortFunc(xs, func(a int, b int) int { cmp.Compare(b, a) })
	assert len(byDesc) == len(xs) && slices.Equal(xs, before)
	let reversed = slices.Reverse(xs)
	assert len(reversed) == len(xs) && slices.Equal(xs, before)
}

test SlicesSortStableKeepsTies(xs []int) {
	let mut tags = []tagged{}
	for i, x in xs {
		tags = tags.Append(tagged{Key: x % 3, At: i})
	}
	let sorted = slices.SortStableFunc(tags, byKey)
	assert len(sorted) == len(xs)
	for i in 1..len(sorted) {
		let a = sorted[i - 1]
		let b = sorted[i]
		assert a.Key < b.Key || a.Key == b.Key && a.At < b.At
	}
}

test SlicesReverseTwiceIsSame(xs []int) {
	assert slices.Equal(slices.Reverse(slices.Reverse(xs)), xs)
}

test SlicesContainsIffIndex(xs []int, x int) {
	let i = slices.Index(xs, x)
	assert slices.Contains(xs, x) == (i >= 0) && (i < 0 || xs[i] == x)
	for y in xs {
		assert slices.Contains(xs, y) && xs[slices.Index(xs, y)] == y
	}
}

test SlicesMaxIsLastSorted(xs []int) {
	let s = slices.Sort(xs)
	if len(s) == 0 {
		assert slices.Max(xs) == None && slices.Min(xs) == None
		return
	}
	assert slices.Max(xs) == Some[int]{Value: s[len(s) - 1]} && slices.Min(xs) == Some[int]{Value: s[0]}
}

test SlicesBinarySearchFindsTheFirst(xs []int, x int) {
	let s = slices.Sort(xs)
	for y in s.Append(x) {
		match slices.BinarySearch(s, y) {
		case Some[int] as found:
			assert s[found.Value] == y && (found.Value == 0 || s[found.Value - 1] < y)
		case None:
			assert !slices.Contains(s, y)
		}
	}
}

test SlicesCollectInvertsValues(xs []int) {
	assert slices.Equal(slices.Collect(slices.Values(xs)), xs)
}

test SlicesTransformIdentity(xs []int) {
	assert slices.Equal(slices.Transform(xs, func(x int) int { x }), xs)
}

test SlicesTransformComposes(xs []int) {
	let f = func(x int) int { x * 3 }
	let g = func(x int) int { x - 7 }
	assert slices.Equal(slices.Transform(slices.Transform(xs, f), g), slices.Transform(xs, func(x int) int { g(f(x)) }))
}

test SlicesFilterIsIdempotent(xs []int) {
	let even = func(x int) bool { x % 2 == 0 }
	assert slices.Equal(slices.Filter(slices.Filter(xs, even), even), slices.Filter(xs, even))
}

test SlicesFilterKeepsTheAccepted(xs []int) {
	let even = func(x int) bool { x%2 == 0 }
	let kept = slices.Filter(xs, even)
	let mut i = 0
	for x in xs {
		if even(x) {
			assert i < len(kept) && kept[i] == x
			i = i + 1
		}
	}
	assert i == len(kept)
}

test SlicesFoldSumMatchesLoop(xs []int) {
	let mut sum = 0
	for x in xs {
		sum = sum + x
	}
	assert slices.Fold(xs, 0, func(acc int, x int) int { acc + x }) == sum
}

test MapsKeysVisitEachKeyOnce(m Map[string, int]) {
	let mut seen = Map[string, bool]{}
	for k in maps.Keys(m) {
		assert seen.Get(k) == None && m.Get(k) != None
		seen[k] = true
	}
	assert len(seen) == len(m)
}

test MapsValuesMatchKeys(m Map[string, int]) {
	let mut total = 0
	for k in maps.Keys(m) {
		match m.Get(k) {
		case Some[int] as v:
			total = total + v.Value
		case None:
			assert false
		}
	}
	let mut sum = 0
	let mut n = 0
	for v in maps.Values(m) {
		sum = sum + v
		n = n + 1
	}
	assert n == len(m) && sum == total
}

test CmpCompareIsAntisymmetric(a int, b int) {
	assert cmp.Compare(a, b) == -cmp.Compare(b, a)
	assert cmp.Compare(a, a) == 0
}

test CmpCompareIsTransitive(a int, b int, c int) {
	if cmp.Compare(a, b) <= 0 && cmp.Compare(b, c) <= 0 {
		assert cmp.Compare(a, c) <= 0
	}
}

test CmpLessIsCompareBelowZero(a string, b string) {
	assert cmp.Less(a, b) == (cmp.Compare(a, b) < 0)
	assert !cmp.Less(a, a)
}
`

// stdSlicesLawStatements is the statement of each law the probe restates,
// the law's source text as the ledger writes it, by ledger key;
// lawledger.Run holds it equal to the ledger (fu-ledger-runner).
var stdSlicesLawStatements = map[string]string{
	"cmp CompareIsAntisymmetric":       `CompareIsAntisymmetric(a int, b int) { Compare(a, b) == -Compare(b, a) }`,
	"cmp CompareIsTransitive":          `CompareIsTransitive(a int, b int, c int) { if Compare(a, b) <= 0 && Compare(b, c) <= 0 { return Compare(a, c) <= 0 } true }`,
	"cmp LessIsCompareBelowZero":       `LessIsCompareBelowZero(a string, b string) { Less(a, b) == (Compare(a, b) < 0) }`,
	"maps KeysVisitEachKeyOnce":        `KeysVisitEachKeyOnce(m Map[string, int]) { let mut seen = Map[string, bool]{} for k in Keys(m) { if seen.Get(k) != None || m.Get(k) == None { return false } seen[k] = true } len(seen) == len(m) }`,
	"maps ValuesMatchKeys":             `ValuesMatchKeys(m Map[string, int]) { let mut total = 0 for k in Keys(m) { match m.Get(k) { case Some[int] as v: total = total + v.Value case None: return false } } let mut sum = 0 let mut n = 0 for v in Values(m) { sum = sum + v n = n + 1 } n == len(m) && sum == total }`,
	"slices BinarySearchFindsTheFirst": `BinarySearchFindsTheFirst(xs []int, x int) { let s = Sort(xs) match BinarySearch(s, x) { case Some[int] as found: s[found.Value] == x && (found.Value == 0 || s[found.Value-1] < x) case None: !Contains(s, x) } }`,
	"slices CollectInvertsValues":      `CollectInvertsValues(xs []int) { Equal(Collect(Values(xs)), xs) }`,
	"slices ContainsIffIndex":          `ContainsIffIndex(xs []int, x int) { let i = Index(xs, x) Contains(xs, x) == (i >= 0) && (i < 0 || xs[i] == x) }`,
	"slices FilterIsIdempotent":        `FilterIsIdempotent(xs []int) { let even = func(x int) bool { x%2 == 0 } Equal(Filter(Filter(xs, even), even), Filter(xs, even)) }`,
	"slices FilterKeepsTheAccepted":    `FilterKeepsTheAccepted(xs []int) { let even = func(x int) bool { x%2 == 0 } let kept = Filter(xs, even) let mut i = 0 for x in xs { if even(x) { if i >= len(kept) || kept[i] != x { return false } i = i + 1 } } i == len(kept) }`,
	"slices FoldSumMatchesLoop":        `FoldSumMatchesLoop(xs []int) { let mut sum = 0 for x in xs { sum = sum + x } Fold(xs, 0, func(acc int, x int) int { acc + x }) == sum }`,
	"slices MaxIsLastSorted":           `MaxIsLastSorted(xs []int) { let s = Sort(xs) if len(s) == 0 { return Max(xs) == None && Min(xs) == None } Max(xs) == Some[int]{Value: s[len(s)-1]} && Min(xs) == Some[int]{Value: s[0]} }`,
	"slices ReverseTwiceIsSame":        `ReverseTwiceIsSame(xs []int) { Equal(Reverse(Reverse(xs)), xs) }`,
	"slices SortIsIdempotent":          `SortIsIdempotent(xs []int) { Equal(Sort(Sort(xs)), Sort(xs)) }`,
	"slices SortLeavesItsArgument":     `SortLeavesItsArgument(xs []int) { let before = Transform(xs, func(x int) int { x }) let sorted = Sort(xs) len(sorted) == len(xs) && Equal(xs, before) }`,
	"slices SortOrders":                `SortOrders(xs []int) { isSorted(Sort(xs)) }`,
	"slices SortPermutes":              `SortPermutes(xs []int) { let sorted = Sort(xs) let mut same = len(sorted) == len(xs) for x in xs { same = same && count(sorted, x) == count(xs, x) } same }`,
	"slices SortStableKeepsTies":       `SortStableKeepsTies(xs []int) { let mut tags = []tagged{} for i, x in xs { tags = tags.Append(tagged{Key: x % 3, At: i}) } let sorted = SortStableFunc(tags, byKey) let mut ok = len(sorted) == len(xs) for i in 1..len(sorted) { let a = sorted[i-1] let b = sorted[i] ok = ok && (a.Key < b.Key || a.Key == b.Key && a.At < b.At) } ok }`,
	"slices TransformComposes":         `TransformComposes(xs []int) { let f = func(x int) int { x * 3 } let g = func(x int) int { x - 7 } Equal(Transform(Transform(xs, f), g), Transform(xs, func(x int) int { g(f(x)) })) }`,
	"slices TransformIdentity":         `TransformIdentity(xs []int) { Equal(Transform(xs, func(x int) int { x }), xs) }`,
}

// TestStdSlicesLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed.
func TestStdSlicesLawsRun(t *testing.T) {
	if laws := lawProbe(t, "stdslices", stdSlicesLawsProbe, stdSlicesLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
