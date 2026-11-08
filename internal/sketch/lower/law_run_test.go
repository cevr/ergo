package lower

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
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
// calls a helper that needs its own operation type.
const lawRunB13 = `type Store {
	Put(n int),
	Count() int,
	law PutCountsOne(n int) {
		let before = twice()
		Put(n)
		twice() == before + 2
	},
}

func twice() int needs Store { Store.Count() * 2 }

type Tally { counts Map[string, int] }

func Put(t Tally, n int) {
	let mut w = t
	w.counts["n"] = t.Count() + 1
}

func Count(t Tally) int {
	match t.counts.Get("n") {
	case Some as s:
		s.Value
	case None:
		0
	}
}

type Stuck { at int }

func Put(_ Stuck, n int) {}

func Count(s Stuck) int { s.at }

func main() needs Console {
	Console.Println("x")
}
`

// TestLawRunB13: the b13 law checks and runs against both providers: it
// holds for Tally and fails for Stuck, whose report names Stuck, the seed
// and the case's provider value.
func TestLawRunB13(t *testing.T) {
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

// TestLawRunDefinedBool: a law's result may be of a type over bool, at the
// tail and at a return (types.md, Laws 8); its run converts it.
func TestLawRunDefinedBool(t *testing.T) {
	src := `type Flag bool

func yes() Flag { Flag(true) }

law FlagTail(n int) { yes() }

law FlagReturn(n int) {
	if n > 0 {
		return yes()
	}
	true
}

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_flag", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_flag"])
	want := []string{"ok law FlagTail (100 cases)", "ok law FlagReturn (100 cases)", "PASS (2 tests)"}
	if code != 0 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
}

// TestLawRunNoParams: a top-level law with no parameters draws nothing and
// runs once, as an example test does; a false one fails the run.
func TestLawRunNoParams(t *testing.T) {
	src := `law Holds() { 1 + 1 == 2 }

law Breaks() { 1 + 1 == 3 }

func main() {}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawrun_noparams", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawrun_noparams"])
	want := []string{"ok law Holds", "FAIL law Breaks: law refuted at main.ergo:3:1: the law is false", "FAIL (1 of 2 tests)"}
	if code != 1 || !slices.Equal(lines(stdout), want) {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\nstderr:\n%s", code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}

// lawRunProgram is a generated program of the property below: one
// operation type with a member law and 1-3 providers, of which at most one
// is broken: it breaks the law for n above a threshold, so it fails at some
// case. Half the laws reach the operation through a helper that needs the
// type (the b13 binding); each program also has a top-level law, true or
// false. want are the lines the test binary prints, in order, and exit its
// status.
type lawRunProgram struct {
	name, src string
	want      []string
	exit      int
}

func genLawRunProgram(r *rand.Rand, i int) lawRunProgram {
	var b strings.Builder
	k, c := r.Intn(5)+1, r.Intn(7)-3
	helper := r.Intn(2) == 0
	call := "F(n)"
	if helper {
		call = "viaHelper(n)"
	}
	fmt.Fprintf(&b, "type Op {\n\tF(n int) int,\n\tlaw Linear(n int) { %s == n * %d + %d },\n}\n\n", call, k, c)
	if helper {
		b.WriteString("func viaHelper(n int) int needs Op { Op.F(n) }\n\n")
	}
	providers := r.Intn(3) + 1
	broken := r.Intn(providers + 1) // == providers: none is broken
	var want []string
	failed := 0
	for p := 0; p < providers; p++ {
		name := fmt.Sprintf("P%d", p)
		body := fmt.Sprintf("n * %d + %d", k, c)
		if p == broken {
			body = fmt.Sprintf("if n > %d { n * %d + %d } else { n * %d + %d }", r.Intn(20)-10, k, c+1, k, c)
		}
		fmt.Fprintf(&b, "type %s { tag int }\n\nfunc F(_ %s, n int) int { %s }\n\n", name, name, body)
		line := "ok law Op.Linear for " + name + " (100 cases)"
		if p == broken {
			line = "FAIL law Op.Linear for " + name + " (case "
			failed++
		}
		want = append(want, line)
	}
	top := r.Intn(2) == 0
	if top {
		b.WriteString("law Top(a int, b int) { a + b == b + a }\n\n")
		want = append(want, "ok law Top (100 cases)")
	} else {
		b.WriteString("law Top(a int, b int) { a - b == b - a }\n\n")
		want = append(want, "FAIL law Top (case ")
		failed++
	}
	b.WriteString("func main() {}\n")
	total := providers + 1
	exit := 0
	if failed > 0 {
		exit = 1
		want = append(want, fmt.Sprintf("FAIL (%d of %d tests)", failed, total))
	} else {
		want = append(want, fmt.Sprintf("PASS (%d tests)", total))
	}
	return lawRunProgram{name: fmt.Sprintf("lawrun_gen_%02d", i), src: b.String(), want: want, exit: exit}
}

// TestLawRunFindsTheBrokenProvider is the property: over generated
// operation types with 1-3 providers, one of them broken at random (or
// none), exactly the broken provider fails, with its name and the seed, and
// the top-level law runs as well. A failure prints the program's source.
// ERGO_LAWRUN_SEED replays another seed.
func TestLawRunFindsTheBrokenProvider(t *testing.T) {
	seed := int64(162)
	if s := os.Getenv("ERGO_LAWRUN_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_LAWRUN_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	var progs []lawRunProgram
	var builds []*lowered
	brokenAt := map[int]bool{}
	for i := 0; i < 16; i++ {
		p := genLawRunProgram(r, i)
		progs = append(progs, p)
		builds = append(builds, lowerTestBuild(t, p.name, p.src))
		for k, w := range p.want {
			if strings.HasPrefix(w, "FAIL law Op.") {
				brokenAt[k] = true
			}
		}
	}
	// The generator reaches a broken provider in every position.
	for k := 0; k < 3; k++ {
		if !brokenAt[k] {
			t.Fatalf("seed %d: no program has a broken provider at position %d", seed, k+1)
		}
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, p := range progs {
		stdout, stderr, code := runExe(t, exes[p.name])
		got := lines(stdout)
		ok := code == p.exit && len(got) == len(p.want)
		for k := 0; ok && k < len(got); k++ {
			w := p.want[k]
			switch {
			case strings.HasSuffix(w, "(case "):
				ok = strings.HasPrefix(got[k], w) && strings.Contains(got[k], ", seed 1, ") && strings.HasSuffix(got[k], ": the law is false")
			default:
				ok = got[k] == w
			}
		}
		if !ok {
			t.Errorf("seed %d, %s: exit %d (want %d)\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", seed, p.name, code, p.exit, stdout, strings.Join(p.want, "\n"), firstLines(stderr, 8), p.src)
		}
	}
}
