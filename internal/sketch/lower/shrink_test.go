package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

var integerShrinkSeed = flag.Uint64("integer-shrink.seed", 0, "replay one integer boundary property seed")

// TestIntegerShrinksReachBoundary holds monotone integer predicates against
// their boundary, for both signs, every integer width, and nested components.
// It catches integer-branch-tape: a branch selector can strand n < 5 at 127.
// It builds Go because the generated runner's replay and printed values are
// the observation; typechecking alone cannot tell where shrinking stops.
func TestIntegerShrinksReachBoundary(t *testing.T) {
	needsBuild(t)
	first, last := uint64(1), uint64(8)
	if *integerShrinkSeed != 0 {
		first, last = *integerShrinkSeed, *integerShrinkSeed
	}
	type property struct {
		seed uint64
		src  string
		want map[string]string
	}
	var props []property
	var programs []*lowered
	for seed := first; seed <= last; seed++ {
		r := rand.New(rand.NewPCG(seed, 0xb0a7d))
		var src strings.Builder
		src.WriteString("type Point { X int, Y int }\ntype Nested { P Point, Tail (int, string) }\n")
		want := map[string]string{}
		for k, typ := range []string{"int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "duration"} {
			boundary := 5 + r.IntN(90)
			if seed == 1 && k == 0 {
				boundary = 5 // the reported n < 5 bug
			}
			for sign := 0; sign < 2; sign++ {
				if sign == 1 && strings.HasPrefix(typ, "u") {
					continue
				}
				value, op := strconv.Itoa(boundary), "<"
				if sign == 1 {
					value, op = "-"+value, ">"
				}
				if typ == "duration" {
					value += "ns"
				}
				name := fmt.Sprintf("B%dS%d", k, sign)
				fmt.Fprintf(&src, "test %s(n %s, label string) { assert n %s %s || len(label) > 100 }\n", name, typ, op, value)
				want[name] = "n = " + value + ", label = \"\""
			}
		}
		b := 5 + r.IntN(90)
		fmt.Fprintf(&src, "test Composite(v Nested) { let (tail, _) = v.Tail; assert (v.P.X > -%d && v.P.X < %d) || (v.P.Y > -%d && v.P.Y < %d) || (tail > -%d && tail < %d) }\n", b, b, b, b, b, b)
		want["Composite"] = fmt.Sprintf("v = Nested{P: Point{X: %d, Y: %d}, Tail: (%d, \"\")}", b, b, b)
		props = append(props, property{seed, src.String(), want})
		programs = append(programs, lowerTestBuild(t, fmt.Sprintf("boundary%d", seed), src.String()))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for _, p := range props {
		stdout, stderr, code := runExeArgs(t, exes[fmt.Sprintf("boundary%d", p.seed)], "--seed", fmt.Sprint(p.seed))
		fail := func(why string) {
			t.Fatalf("seed %d: %s\nErgo source:\n%s\noutput (exit %d):\n%s\n%s\nreference boundaries: %v\nreplay: go test -run '^TestIntegerShrinksReachBoundary$' ./internal/sketch/lower -args -integer-shrink.seed=%d", p.seed, why, p.src, code, stdout, stderr, p.want, p.seed)
		}
		if code != 1 {
			fail("monotone predicates must be refuted")
		}
		seen := map[string]bool{}
		for _, line := range lines(stdout) {
			for name, want := range p.want {
				if !strings.HasPrefix(line, "FAIL "+name+" (") {
					continue
				}
				seen[name] = true
				if !strings.Contains(line, ": "+want+";") && !strings.Contains(line, ": "+want+"):") {
					fail("shrunk " + name + " is not its boundary " + want)
				}
			}
		}
		for name := range p.want {
			if !seen[name] {
				fail("no refutation for " + name)
			}
		}
	}
}

// TestLawContextStartsEachCaseFresh is the smallest repro of db043a7d:
// a law under a context must not see the preceding case's built world.
// It uniquely kills law-context-keeps-world; ordinary properties, direct
// provider laws and shrink replays reset their worlds by other paths.
// It builds Go because freshness is an observation of the generated law
// runner and its mutable fstest provider, not a typing or Go-shape fact.
func TestLawContextStartsEachCaseFresh(t *testing.T) {
	needsBuild(t)
	const src = `import fs
import fstest

type Disk { files fs.Files, writes fs.Writes }

type Probe {
	Start() bool,
	law Fresh(d Disk) {
		let before = try d.files.ReadDir(".")
		try d.writes.WriteFile("case.txt", "seen")
		Start() && len(before) == 0
	},
}

type Unit {}
func Start(_ Unit) bool { true }
let TestWorld = context { Probe: Unit }

func main() { let _ = fstest.NewMemory() }
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "lawfresh", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["lawfresh"])
	if code != 0 || !strings.Contains(stdout, "ok law Probe.Fresh under TestWorld (100 cases)\n") {
		t.Fatalf("a law saw another case's world: exit %d\nsource:\n%s\nstdout:\n%s\nstderr:\n%s", code, src, stdout, stderr)
	}
}
