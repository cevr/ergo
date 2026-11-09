package lower

import (
	"slices"
	"strings"
	"testing"
)

// law-run-1 (#162; types.md, Laws 10): ergo test runs every top-level law
// over generated values of its parameters, and every member law against
// each provider of its operation type that the package declares, with a
// generated value of the provider. Inside a member law the operation type is
// in the law's row and bound to the provider under test, so a helper that
// needs it runs (G-ex-b-13). A false law fails the run, naming the provider
// and the seed.

// lawRunB13 is the b13 repro of the examples wave with a provider: the law
// calls a helper that needs its own operation type. The law relates two
// reads, not a write and a read: under the value rule (owner q26) a
// provider's write to its own Map is not seen by the next call, and a
// provider whose state is a sync.Ref has no generator, so a write law has
// no generated provider to run against (batch-12 integration, V1 x value
// rule).
const lawRunB13 = `type Store {
	Count() int,
	Doubled() int,
	law PutCountsOne(n int) {
		twice() + n == Doubled() + n
	},
}

func twice() int needs Store { Store.Count() * 2 }

type Tally { counts Map[string, int] }

func Count(t Tally) int { len(t.counts) }

func Doubled(t Tally) int { 2 * len(t.counts) }

type Stuck { at int }

func Count(s Stuck) int { s.at }

func Doubled(s Stuck) int { 2*s.at + 1 }

func main() needs Console {
	Console.Println("x")
}
`

// TestLawRunB13: the b13 law checks and runs against both providers: it
// holds for Tally and fails for Stuck, whose report names Stuck, the seed
// and the case's provider value.
func TestLawRunB13(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_b13", lawRunB13)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["lawrun_b13"], "--seed", "7")
	got := lines(stdout)
	if code != 1 || len(got) != 3 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if got[0] != "ok law Store.PutCountsOne for Tally (100 cases)" {
		t.Errorf("line 1 is %q", got[0])
	}
	if !strings.HasPrefix(got[1], "FAIL law Store.PutCountsOne for Stuck (case 1 of 100, seed 7, size 0: provider = Stuck{at: ") ||
		!strings.HasSuffix(got[1], "law refuted at main.ergo:4:2: the law is false") {
		t.Errorf("line 2 is %q", got[1])
	}
	if got[2] != "FAIL (1 of 2 tests)" {
		t.Errorf("the summary is %q", got[2:])
	}
}

// TestLawRunSkips: a law that cannot run says why, and is not counted.
func TestLawRunSkips(t *testing.T) {
	needsBuild(t)
	src := `type Shape {
	Area() int,
	law Positive() { Area() >= 0 },
}

type Box[T] {
	Put(v T),
	law Keeps(v T) { true },
}

type Fn { f func(int) int }

func Area(_ Fn) int { 1 }

law Calls(f func(int) int) { f(1) == f(1) }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_skips", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_skips"])
	want := []string{
		"skip law Shape.Positive for Fn: its values have no generator: func(int) int is a function type",
		"skip law Box.Keeps: its parameter v has no generator: T is not a generatable type",
		"skip law Calls: its parameter f has no generator: func(int) int is a function type",
		"PASS (0 tests)",
	}
	if code != 0 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}
