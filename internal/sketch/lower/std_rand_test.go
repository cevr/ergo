package lower

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

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

// TestStdRandLawsRun runs the rand probe's tests with --seed 1, and holds
// that the probe restates exactly the ledger's laws whose runner is this
// test.
func TestStdRandLawsRun(t *testing.T) {
	want := ledgerRunnerLaws(t, "TestStdRandLawsRun")
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)\(`).FindAllStringSubmatch(stdRandLawsProbe, -1) {
		got = append(got, m[1])
	}
	slices.Sort(want)
	slices.Sort(got)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("the probe restates %v; the ledger runs %v in TestStdRandLawsRun", got, want)
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "stdrand", stdRandLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdrand"], "--seed", "1")
	if code != 0 {
		t.Fatalf("the rand law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, name := range want {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(100 cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
}
