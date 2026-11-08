package lower

import (
	"strings"
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

func where[T ordered](xs []T, x T) Option[int] { slices.BinarySearch(slices.Ascend(xs), x) }

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
	Console.Println(opt(slices.BinarySearch(slices.Ascend(xs), 2)) + " " + opt(slices.Ascend(xs).BinarySearch(3)) + " " + ints(slices.Ascend(xs).Items()))
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
	"1 none 1,2,2,5,8,9\n" +
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
// NonEmptyArray, so an empty one is a type error there; here it is None.
// The cases that conformance/effect-cases.json records (sort, map, filter,
// reduce) print their output after a line "== <case id>", and each is a
// subtest named after its case id (scripts/conformance: a row's evidence
// test runs the subtest of its case). Effect's index forms (map, filter
// and reduce with a callback that takes the index) and its Set inputs
// have no Ergo form: Transform, Filter and Fold take the element only.
func TestStdSlicesEffectCases(t *testing.T) {
	const src = `import slices
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
	// contains (Array.test.ts:645)
	Console.Println("\(slices.Contains([]int{1, 2, 3}, 2)) \(slices.Contains([]int{1, 2, 3}, 0))")
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

	// The recorded cases. sort (Array.test.ts:533): a sorted copy; the
	// input keeps its order.
	Console.Println("== effect.array.sort")
	let unsorted = []int{1, 3, 2}
	Console.Println(ints(slices.Sort([]int{})) + ints(slices.Sort(unsorted)) + ints(unsorted))
	// map (Array.test.ts:834)
	Console.Println("== effect.array.map")
	Console.Println(ints(slices.Transform([]int{1, 2, 3}, func(n int) int { n * 2 })))
	// filter (Array.test.ts:886), with a predicate on numbers and on
	// options (Option.isSome)
	Console.Println("== effect.array.filter")
	Console.Println(ints(slices.Filter([]int{1, 2, 3}, func(n int) bool { n % 2 == 1 })))
	let isSome = func(o Option[int]) bool {
		match o {
		case Some[int]:
			true
		case None:
			false
		}
	}
	let some = slices.Filter([]Option[int]{Some[int]{Value: 3}, Some[int]{Value: 2}, Some[int]{Value: 1}}, isSome)
	let gaps = slices.Filter([]Option[int]{Some[int]{Value: 3}, None, Some[int]{Value: 1}}, isSome)
	Console.Println(strings.Join(slices.Transform(some, opt), ",") + " " + strings.Join(slices.Transform(gaps, opt), ","))
	// reduce (Array.test.ts:924)
	Console.Println("== effect.array.reduce")
	Console.Println(slices.Fold([]string{"a", "b", "c"}, "", func(b string, a string) string { b + a }))
}
`
	run := mustLower(t, "effectcases", src)
	exes := buildAll(t, []*lowered{run}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["effectcases"])
	head, sections, _ := strings.Cut(stdout, "== ")
	if want := "[][1][3,2,1]\ntrue false\n1 3 3 1 none\na1 b2 b3 c2\na1 b2 b3 c2\n"; code != 0 || head != want {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %s", code, head, want, firstLines(stderr, 10))
	}
	t.Run("effect.array.sort", func(t *testing.T) {
		effectCaseSection(t, sections, "[][1,2,3][1,3,2]\n")
	})
	t.Run("effect.array.map", func(t *testing.T) {
		effectCaseSection(t, sections, "[2,4,6]\n")
	})
	t.Run("effect.array.filter", func(t *testing.T) {
		effectCaseSection(t, sections, "[1,3]\n3,2,1 3,1\n")
	})
	t.Run("effect.array.reduce", func(t *testing.T) {
		effectCaseSection(t, sections, "abc\n")
	})
}

// effectCaseSection checks the output section of the subtest's
// conformance case: in sections, the lines after "== <case id>\n" up to
// the next "== ". The subtest's name is the case id.
func effectCaseSection(t *testing.T, sections, want string) {
	t.Helper()
	id := t.Name()[strings.LastIndexByte(t.Name(), '/')+1:]
	for section := range strings.SplitSeq("== "+sections, "== ") {
		if name, got, ok := strings.Cut(section, "\n"); ok && name == id {
			if got != want {
				t.Errorf("case %s: output %q, want %q", id, got, want)
			}
			return
		}
	}
	t.Errorf("case %s: no output section in %q", id, sections)
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

test SlicesAscendOrders(xs []int) { assert isSorted(slices.Items(slices.Ascend(xs))) }

test SlicesAscendPermutes(xs []int) {
	let items = slices.Items(slices.Ascend(xs))
	assert len(items) == len(xs)
	for x in xs {
		assert count(items, x) == count(xs, x)
	}
}

test SlicesAscendLeavesItsArgument(xs []int) {
	let before = slices.Transform(xs, func(x int) int { x })
	let a = slices.Ascend(xs)
	assert len(a.Items()) == len(xs) && slices.Equal(xs, before)
}

test SlicesItemsIsACopy(xs []int) {
	let a = slices.Ascend(xs)
	let mut changed = a.Items()
	for i in 0..len(changed) {
		changed[i] = len(changed) - i
	}
	assert slices.Equal(a.Items(), slices.Sort(xs))
	let mut first = a.Items()
	if len(first) > 0 {
		first[0] = first[len(first) - 1] + 1
		assert slices.Equal(a.Items(), slices.Sort(xs)) && isSorted(a.Items())
	}
}

test SlicesBinarySearchFindsTheFirst(xs []int, x int) {
	let a = slices.Ascend(xs)
	let s = a.Items()
	for y in s.Append(x) {
		match slices.BinarySearch(a, y) {
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

// stops is how often s calls a yield that returns false at its k-th call.
func stops(s Seq[int], k int) int {
	let mut calls = 0
	s(func(v int) bool {
		calls = calls + 1
		calls < k
	})
	calls
}

// stopsKeys is stops for a Seq of keys.
func stopsKeys(s Seq[string], k int) int {
	let mut calls = 0
	s(func(key string) bool {
		calls = calls + 1
		calls < k
	})
	calls
}

// least is the smaller of a and b.
func least(a int, b int) int {
	if a < b {
		a
	} else {
		b
	}
}

test SlicesValuesStops(xs []int) {
	let s = slices.Values(xs)
	for k in 1..len(xs)+2 {
		assert stops(s, k) == least(k, len(xs))
	}
	assert stops(slices.Values([]int{}), 1) == 0
}

test MapsKeysStops(m Map[string, int]) {
	let s = maps.Keys(m)
	for k in 1..len(m)+2 {
		assert stopsKeys(s, k) == least(k, len(m))
	}
}

test MapsValuesStops(m Map[string, int]) {
	let s = maps.Values(m)
	for k in 1..len(m)+2 {
		assert stops(s, k) == least(k, len(m))
	}
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

test SlicesCloneCopies(xs []int) {
	let c = slices.Clone(xs)
	let mut d = slices.Clone(xs)
	for i in 0..len(d) {
		d[i] = d[i] + 1
	}
	assert slices.Equal(c, xs) && (len(xs) == 0 || !slices.Equal(d, xs))
}

test SlicesConcatJoins(a []int, b []int) {
	let c = slices.Concat(a, b)
	assert len(c) == len(a)+len(b) && slices.Equal(slices.Slice(c, 0, len(a)), a) && slices.Equal(slices.Slice(c, len(a), len(c)), b)
}

test SlicesConcatIsIsolated(a []int, b []int, x int) {
	let mut roomy = slices.WithCapacity[int](len(a) + len(b) + 1)
	for v in a {
		roomy = roomy.Append(v)
	}
	let c = slices.Concat(roomy, b)
	let before = slices.Clone(c)
	let grown = roomy.Append(x)
	assert slices.Equal(c, before) && len(grown) == len(a)+1 && slices.Equal(slices.Slice(c, len(a), len(c)), b)
}

test SlicesRepeatRepeats(xs []int, n int) {
	let k = (n%8 + 8) % 8
	let r = slices.Repeat(xs, k)
	assert len(r) == len(xs)*k
	for i, v in r {
		assert v == xs[i%len(xs)]
	}
}

test SlicesSliceHasNoRoom(xs []int, i int, x int) {
	if len(xs) == 0 {
		return
	}
	let j = (i%len(xs) + len(xs)) % len(xs)
	let before = slices.Clone(xs)
	let grown = slices.Slice(xs, 0, j).Append(x)
	assert slices.Equal(xs, before) && slices.Equal(slices.Slice(grown, 0, j), slices.Slice(xs, 0, j)) && grown[j] == x
}

test SlicesLenAndAtReadItems(xs []int) {
	let a = slices.Ascend(xs)
	let items = a.Items()
	assert a.Len() == len(items)
	for i, v in items {
		assert a.At(i) == v
	}
}

test MapsCloneIsIndependent(m Map[string, int], k string) {
	let size = len(m)
	let had = m.Get(k)
	let mut c = maps.Clone(m)
	assert len(c) == size
	for key, v in m {
		assert c.Get(key) == Some[int]{Value: v}
	}
	c[k] = 7
	assert len(m) == size && m.Get(k) == had && c.Get(k) == Some[int]{Value: 7}
}

test MapsUpdateSeesTheOldValue(m Map[string, int], k string, v int) {
	let before = m.Get(k)
	let mut seen Option[int] = Some[int]{Value: v + 1}
	let out = maps.Update(maps.Clone(m), k, func(old Option[int]) int {
		seen = old
		v
	})
	let grew = if before == None { 1 } else { 0 }
	assert seen == before && out.Get(k) == Some[int]{Value: v} && len(out) == len(m)+grew
	// A missing key, made so: Update sees None.
	let mut missing = maps.Clone(m)
	missing = missing.Delete(k)
	let mut sawNone = false
	missing = maps.Update(missing, k, func(old Option[int]) int {
		sawNone = old == None
		v
	})
	assert sawNone && missing.Get(k) == Some[int]{Value: v}
}

test MapsGetOrDefaults(m Map[string, int], k string, def int) {
	match m.Get(k) {
	case Some[int] as v:
		assert maps.GetOr(m, k, def) == v.Value
	case None:
		assert maps.GetOr(m, k, def) == def
	}
	for key, v in m {
		assert maps.GetOr(m, key, def) == v
	}
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
	"maps KeysStops":                   `KeysStops(m Map[string, int]) { let s = Keys(m) let n = len(m) for k in 1..n+2 { let mut calls = 0 s(func(key string) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"maps ValuesStops":                 `ValuesStops(m Map[string, int]) { let s = Values(m) let n = len(m) for k in 1..n+2 { let mut calls = 0 s(func(v int) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"slices ValuesStops":               `ValuesStops(xs []int) { let s = Values(xs) let n = len(xs) for k in 1..n+2 { let mut calls = 0 s(func(v int) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"maps ValuesMatchKeys":             `ValuesMatchKeys(m Map[string, int]) { let mut total = 0 for k in Keys(m) { match m.Get(k) { case Some[int] as v: total = total + v.Value case None: return false } } let mut sum = 0 let mut n = 0 for v in Values(m) { sum = sum + v n = n + 1 } n == len(m) && sum == total }`,
	"slices AscendLeavesItsArgument":   `AscendLeavesItsArgument(xs []int) { let before = Transform(xs, func(x int) int { x }) let a = Ascend(xs) len(Items(a)) == len(xs) && Equal(xs, before) }`,
	"slices AscendOrders":              `AscendOrders(xs []int) { isSorted(Items(Ascend(xs))) }`,
	"slices AscendPermutes":            `AscendPermutes(xs []int) { let items = Items(Ascend(xs)) let mut same = len(items) == len(xs) for x in xs { same = same && count(items, x) == count(xs, x) } same }`,
	"slices ItemsIsACopy":              `ItemsIsACopy(xs []int) { let a = Ascend(xs) let mut changed = Items(a) for i in 0..len(changed) { changed[i] = len(changed) - i } Equal(Items(a), Sort(xs)) }`,
	"slices BinarySearchFindsTheFirst": `BinarySearchFindsTheFirst(xs []int, x int) { let a = Ascend(xs) let s = Items(a) match BinarySearch(a, x) { case Some[int] as found: s[found.Value] == x && (found.Value == 0 || s[found.Value-1] < x) case None: !Contains(s, x) } }`,
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
	"slices CloneCopies":               `CloneCopies(xs []int) { let c = Clone(xs) let mut d = Clone(xs) for i in 0..len(d) { d[i] = d[i] + 1 } Equal(c, xs) && (len(xs) == 0 || !Equal(d, xs)) }`,
	"slices ConcatJoins":               `ConcatJoins(a []int, b []int) { let c = Concat(a, b) len(c) == len(a)+len(b) && Equal(Slice(c, 0, len(a)), a) && Equal(Slice(c, len(a), len(c)), b) }`,
	"slices ConcatIsIsolated":          `ConcatIsIsolated(a []int, b []int, x int) { let mut roomy = WithCapacity[int](len(a) + len(b) + 1) for v in a { roomy = roomy.Append(v) } let c = Concat(roomy, b) let before = Clone(c) let grown = roomy.Append(x) Equal(c, before) && len(grown) == len(a)+1 && Equal(Slice(c, len(a), len(c)), b) }`,
	"slices RepeatRepeats":             `RepeatRepeats(xs []int, n int) { let k = (n%8 + 8) % 8 let r = Repeat(xs, k) let mut ok = len(r) == len(xs)*k for i, v in r { ok = ok && v == xs[i%len(xs)] } ok }`,
	"slices SliceHasNoRoom":            `SliceHasNoRoom(xs []int, i int, x int) { if len(xs) == 0 { return true } let j = (i%len(xs) + len(xs)) % len(xs) let before = Clone(xs) let grown = Slice(xs, 0, j).Append(x) Equal(xs, before) && Equal(Slice(grown, 0, j), Slice(xs, 0, j)) && grown[j] == x }`,
	"slices LenAndAtReadItems":         `LenAndAtReadItems(xs []int) { let a = Ascend(xs) let items = Items(a) let mut ok = Len(a) == len(items) for i, v in items { ok = ok && At(a, i) == v } ok }`,
	"maps CloneIsIndependent":          `CloneIsIndependent(m Map[string, int], k string) { let size = len(m) let had = m.Get(k) let mut c = Clone(m) let mut same = len(c) == size for key, v in m { same = same && c.Get(key) == Some[int]{Value: v} } c[k] = 7 same && len(m) == size && m.Get(k) == had }`,
	"maps UpdateSeesTheOldValue":       `UpdateSeesTheOldValue(m Map[string, int], k string, v int) { let before = m.Get(k) let mut seen Option[int] = Some[int]{Value: v + 1} let out = Update(Clone(m), k, func(old Option[int]) int { seen = old v }) let grew = if before == None { 1 } else { 0 } seen == before && out.Get(k) == Some[int]{Value: v} && len(out) == len(m)+grew }`,
	"maps GetOrDefaults":               `GetOrDefaults(m Map[string, int], k string, def int) { match m.Get(k) { case Some[int] as v: GetOr(m, k, def) == v.Value case None: GetOr(m, k, def) == def } }`,
}

// TestStdSlicesLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed.
func TestStdSlicesLawsRun(t *testing.T) {
	if laws := lawProbe(t, "stdslices", stdSlicesLawsProbe, stdSlicesLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
