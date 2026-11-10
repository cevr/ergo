package lower

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestRandRuns (std-align S2c): std rand lowers and runs, and its draws
// are Go's. Under Seeded(7), Shuffle of ten values, then Perm(5), then a
// Choice give the order Go's r.Shuffle, r.Perm and r.IntN give on
// rand.New(rand.NewPCG(7, v1.RandSeedHi)), the reference model; Shuffle
// leaves its argument; a Choice of an empty slice is None and draws
// nothing. A copy of a PCG shares its stream (b draws the second value),
// and a second Seeded(3) replays it. The host's draws are in range. An
// IntN(0) is a defect with Go's message.
func TestRandRuns(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{
		{name: "rand", src: stdRandProgram, stdout: stdRandOutput()},
		{name: "randdefect", src: stdRandDefect, stdout: "before\n", exit: 2, stderr: []string{"invalid argument to IntN"}},
	})
}

const stdRandProgram = `import rand

func show(xs []int) string {
	let mut s = ""
	for i, x in xs {
		if i > 0 {
			s = s + " "
		}
		s = s + "\(x)"
	}
	s
}

func pick(xs []int) string needs Random {
	match rand.Choice(xs) {
	case Some[int] as v:
		"\(v.Value)"
	case None:
		"none"
	}
}

func deal(xs []int) []int needs Random { rand.Shuffle(xs) }

func main() needs Console | Ambient {
	let xs = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	let r = rand.Seeded(7)
	with context { Random: r } {
		Console.Println(show(deal(xs)))
		Console.Println(show(rand.Perm(5)))
		Console.Println(pick([]int{}) + " " + pick(xs))
	}
	Console.Println(show(xs))

	let a = rand.Seeded(3)
	let b = a
	let first = a.Uint64()
	let second = b.Uint64()
	let c = rand.Seeded(3)
	Console.Println("\(first) \(second) \(c.Uint64() == first)")
	let f = c.Float64()
	Console.Println("\(c.IntN(100)) \(f >= 0.0 && f < 1.0)")

	let h = rand.NewHost()
	let n = h.IntN(10)
	let g = h.Float64()
	Console.Println("\(n >= 0 && n < 10) \(g >= 0.0 && g < 1.0)")
	with context { Random: h } {
		Console.Println("\(len(rand.Shuffle(xs))) \(len(rand.Perm(3)))")
	}
}
`

const stdRandDefect = `import rand

func main() needs Console {
	let r = rand.Seeded(1)
	Console.Println("before")
	Console.Println("\(r.IntN(0))")
}
`

// stdRandOutput is stdRandProgram's output by the reference model: Go's
// math/rand/v2 on the seeds the program names.
func stdRandOutput() string {
	show := func(xs []int) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = strconv.Itoa(x)
		}
		return strings.Join(parts, " ")
	}
	var out strings.Builder
	r := rand.New(rand.NewPCG(7, v1.RandSeedHi))
	xs := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	dealt := slices.Clone(xs)
	r.Shuffle(len(dealt), func(i, j int) { dealt[i], dealt[j] = dealt[j], dealt[i] })
	out.WriteString(show(dealt) + "\n")
	out.WriteString(show(r.Perm(5)) + "\n")
	out.WriteString("none " + strconv.Itoa(xs[r.IntN(len(xs))]) + "\n")
	out.WriteString(show(xs) + "\n")
	s := rand.New(rand.NewPCG(3, v1.RandSeedHi))
	first, second := s.Uint64(), s.Uint64()
	out.WriteString(strconv.FormatUint(first, 10) + " " + strconv.FormatUint(second, 10) + " true\n")
	c := rand.New(rand.NewPCG(3, v1.RandSeedHi))
	c.Uint64()
	c.Float64()
	out.WriteString(strconv.Itoa(c.IntN(100)) + " true\n")
	out.WriteString("true true\n")
	out.WriteString("10 3\n")
	return out.String()
}

// stdRandLawsProbe restates the rand laws whose runner is
// TestStdRandLawsRun as Ergo tests over rand.Seeded. RandPermPermutes
// checks every n in 0..200 for each seed, where the law takes one n.
const stdRandLawsProbe = `import rand

func within(n int, lo int, hi int) int {
	let span = hi - lo + 1
	let r = n % span
	if r < 0 {
		lo + (r + span)
	} else {
		lo + r
	}
}

func isPermutation(p []int, n int) bool {
	if len(p) != n {
		return false
	}
	let mut seen = Map[int, bool]{}
	for x in p {
		if x < 0 || x >= n || seen.Get(x) != None {
			return false
		}
		seen[x] = true
	}
	true
}

func tally(xs []int) Map[int, int] {
	let mut m = Map[int, int]{}
	for x in xs {
		match m.Get(x) {
		case Some[int] as c:
			m[x] = c.Value + 1
		case None:
			m[x] = 1
		}
	}
	m
}

test RandSeedReplays(seed uint64, n int) {
	let a = rand.Seeded(seed)
	let b = rand.Seeded(seed)
	let k = within(n, 1, 1000000)
	assert a.Uint64() == b.Uint64() && a.IntN(k) == b.IntN(k) && a.Float64() == b.Float64()
}

test RandPermPermutes(seed uint64) {
	for size in 0..201 {
		let p = with context { Random: rand.Seeded(seed) } { rand.Perm(size) }
		assert isPermutation(p, size)
	}
}

test RandShufflePermutes(seed uint64, xs []int) {
	let mut kept = []int{}
	for x in xs {
		kept = kept.Append(x)
	}
	let before = tally(xs)
	let s = with context { Random: rand.Seeded(seed) } { rand.Shuffle(xs) }
	let after = tally(s)
	assert len(s) == len(xs) && len(after) == len(before)
	for i, x in xs {
		assert after.Get(x) == before.Get(x) && kept[i] == x
	}
}
`

// stdRandLawStatements is the statement of each law the probe restates,
// the law's source text as the ledger writes it, by ledger key;
// lawledger.Run holds it equal to the ledger (fu-ledger-runner).
var stdRandLawStatements = map[string]string{
	"rand PermPermutes":    `PermPermutes(seed uint64, n int) { let size = within(n, 0, 200) let p = with context { Random: Seeded(seed) } { Perm(size) } isPermutation(p, size) }`,
	"rand SeedReplays":     `SeedReplays(seed uint64, n int) { let a = Seeded(seed) let b = Seeded(seed) let k = within(n, 1, 1000000) a.Uint64() == b.Uint64() && a.IntN(k) == b.IntN(k) && a.Float64() == b.Float64() }`,
	"rand ShufflePermutes": `ShufflePermutes(seed uint64, xs []int) { let mut kept = []int{} for x in xs { kept = kept.Append(x) } let before = tally(xs) let s = with context { Random: Seeded(seed) } { Shuffle(xs) } let after = tally(s) let mut same = len(s) == len(xs) && len(after) == len(before) for i, x in xs { same = same && after.Get(x) == before.Get(x) && kept[i] == x } same }`,
}

// TestStdRandLawsRun runs the rand probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdRandLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdrand", stdRandLawsProbe, stdRandLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// TestRandEffectCases ports cases of Effect's Random tests
// (packages/effect/test/Random.test.ts at the conformance pin 460272d3) to
// std rand: each prints its output after a line "== <case id>", and each
// is a subtest named after its case id in conformance/effect-cases.json.
// Ergo's seeds are uint64, so each case seeds with its upstream line.
// Where Effect checks only that two runs of one seed agree, the subtest
// also holds the draws equal to the reference model, Go's math/rand/v2 on
// rand.New(rand.NewPCG(seed, v1.RandSeedHi)). Effect's choice of an empty
// collection fails; Ergo's is None and draws nothing (a difference row).
//
// It builds Go because it is the evidence of Effect conformance cases
// (conformance/effect-cases.json): what the built program prints is each
// case's outcome. Restored at the batch-13 integration: T5 cut it, which
// left its cases with no evidence test.
func TestRandEffectCases(t *testing.T) {
	needsBuild(t)
	const src = `import rand
import slices

func show(xs []int) string {
	let mut s = ""
	for i, x in xs {
		if i > 0 {
			s = s + " "
		}
		s = s + "\(x)"
	}
	s
}

func pick(xs []int) string needs Random {
	match rand.Choice(xs) {
	case Some[int] as v:
		"\(v.Value)"
	case None:
		"none"
	}
}

func inUnit(n int) bool needs Random {
	let mut ok = true
	let mut i = 0
	for i < n {
		let f = Random.Float64()
		if f < 0.0 || f >= 1.0 {
			ok = false
		}
		i = i + 1
	}
	ok
}

// bools draws Effect's nextBoolean three times: Float64() > 0.5.
func bools() string needs Random {
	let a = Random.Float64() > 0.5
	let b = Random.Float64() > 0.5
	let c = Random.Float64() > 0.5
	"\(a) \(b) \(c)"
}

func choices() string needs Random {
	let xs = []int{1, 2, 3, 4, 5}
	let a = pick(xs)
	let b = pick(xs)
	let c = pick(xs)
	a + " " + b + " " + c
}

func main() needs Console | Ambient {
	// Random.test.ts:8, next - generates a number between 0 inclusive and 1 exclusive
	Console.Println("== effect.random.next-in-unit")
	let host = with context { Random: rand.NewHost() } { inUnit(1000) }
	let seeded = with context { Random: rand.Seeded(8) } { inUnit(1000) }
	Console.Println("\(host) \(seeded)")

	// Random.test.ts:25, nextBoolean - is deterministic with the same seed
	Console.Println("== effect.random.next-boolean-seed")
	let b1 = with context { Random: rand.Seeded(25) } { bools() }
	let b2 = with context { Random: rand.Seeded(25) } { bools() }
	Console.Println(b1)
	Console.Println(b2)

	// Random.test.ts:175, shuffle - returns a shuffled copy of all values
	Console.Println("== effect.random.shuffle-copy")
	let input = []int{1, 2, 3, 4, 5}
	let out = with context { Random: rand.Seeded(175) } { rand.Shuffle(input) }
	Console.Println(show(input) + " | " + show(slices.Sort(out)))

	// Random.test.ts:185, shuffle - is deterministic with the same seed
	Console.Println("== effect.random.shuffle-seed")
	let s1 = with context { Random: rand.Seeded(185) } { rand.Shuffle([]int{1, 2, 3, 4, 5}) }
	let s2 = with context { Random: rand.Seeded(185) } { rand.Shuffle([]int{1, 2, 3, 4, 5}) }
	Console.Println(show(s1))
	Console.Println(show(s2))

	// Random.test.ts:209, choice - fails with NoSuchElementError for an
	// empty iterable: here None, and the stream is not drawn.
	Console.Println("== effect.random.choice-empty")
	let e = rand.Seeded(209)
	let empty = with context { Random: e } { pick([]int{}) }
	Console.Println(empty + " \(e.Uint64())")

	// Random.test.ts:217, choice - is deterministic with the same seed
	Console.Println("== effect.random.choice-seed")
	let c1 = with context { Random: rand.Seeded(217) } { choices() }
	let c2 = with context { Random: rand.Seeded(217) } { choices() }
	Console.Println(c1)
	Console.Println(c2)
}
`
	ref := func(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, v1.RandSeedHi)) }
	r := ref(25)
	var drawn []string
	for range 3 {
		drawn = append(drawn, strconv.FormatBool(r.Float64() > 0.5))
	}
	booleans := strings.Join(drawn, " ") + "\n"
	shuffled := []string{"1", "2", "3", "4", "5"}
	ref(185).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	order := strings.Join(shuffled, " ") + "\n"
	r = ref(217)
	drawn = drawn[:0]
	for range 3 {
		drawn = append(drawn, strconv.Itoa(r.IntN(5)+1))
	}
	picked := strings.Join(drawn, " ") + "\n"

	run := mustLower(t, "randcases", src)
	exes := buildAll(t, []*lowered{run}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["randcases"])
	head, sections, _ := strings.Cut(stdout, "== ")
	if code != 0 || head != "" {
		t.Errorf("exit %d\nstdout %q\nstderr %s", code, stdout, firstLines(stderr, 10))
	}
	t.Run("effect.random.next-in-unit", func(t *testing.T) {
		effectCaseSection(t, sections, "true true\n")
	})
	t.Run("effect.random.next-boolean-seed", func(t *testing.T) {
		effectCaseSection(t, sections, booleans+booleans)
	})
	t.Run("effect.random.shuffle-copy", func(t *testing.T) {
		effectCaseSection(t, sections, "1 2 3 4 5 | 1 2 3 4 5\n")
	})
	t.Run("effect.random.shuffle-seed", func(t *testing.T) {
		effectCaseSection(t, sections, order+order)
	})
	t.Run("effect.random.choice-empty", func(t *testing.T) {
		effectCaseSection(t, sections, "none "+strconv.FormatUint(ref(209).Uint64(), 10)+"\n")
	})
	t.Run("effect.random.choice-seed", func(t *testing.T) {
		effectCaseSection(t, sections, picked+picked)
	})
}
