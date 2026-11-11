package main

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// TestTryParensIsIdempotent: generated programs hold (try …) in operand
// position, in arguments, under a unary operator, before a selector and
// around a binary expression inside a tighter operator. tryparens must
// leave the lowered Go the same (the program's meaning), and a second
// run must change nothing. The domain must reach both a removal and a
// kept parenthesis. ERGO_TRYPARENS_SEED=<seed> replays one seed.
func TestTryParensIsIdempotent(t *testing.T) {
	first, seeds := int64(1), 30
	if s := os.Getenv("ERGO_TRYPARENS_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = v, 1
	}
	removed, kept := 0, 0
	for seed := first; seed < first+int64(seeds); seed++ {
		src := tryParensProgram(rand.New(rand.NewSource(seed)))
		want, ok := oneText(src)
		if !ok {
			t.Fatalf("seed %d: the generated program does not check or lower:\n%s", seed, src)
		}
		once, n := rewriteTryParens(src, oneText)
		got, ok := oneText(once)
		replay := fmt.Sprintf("replay: ERGO_TRYPARENS_SEED=%d go test -run TestTryParensIsIdempotent ./scripts/langmigrate", seed)
		if !ok || got != want {
			t.Fatalf("seed %d: the rewrite changed the lowered Go\nbefore:\n%s\nafter:\n%s\n%s", seed, src, once, replay)
		}
		if triedParens(src)-triedParens(once) != n || strings.Count(src, "(")-strings.Count(once, "(") != n {
			t.Fatalf("seed %d: the rewrite removed other parentheses than the %d (try …) it counts\nbefore:\n%s\nafter:\n%s\n%s", seed, n, src, once, replay)
		}
		twice, m := rewriteTryParens(once, oneText)
		if m != 0 || twice != once {
			t.Fatalf("seed %d: a second run removed %d more\nfirst:\n%s\nsecond:\n%s\n%s", seed, m, once, twice, replay)
		}
		removed += n
		kept += len(tryParens(once))
	}
	if seeds > 1 && (removed == 0 || kept == 0) {
		t.Fatalf("the domain is starved: %d removed, %d kept", removed, kept)
	}
	t.Logf("%d removed, %d kept", removed, kept)
}

// triedParens counts the parenthesized trys of src, independently of
// tryParens.
func triedParens(src string) int {
	f, err := syntax.Parse(src)
	if err != nil {
		return -1
	}
	n := 0
	syntax.Inspect(f, func(x syntax.Node) bool {
		if p, ok := x.(*syntax.ParenExpr); ok {
			if _, ok := p.X.(*syntax.TryExpr); ok {
				n++
			}
		}
		return true
	})
	return n
}

// oneText is the lowered Go of a program of one source text.
func oneText(src string) (string, bool) {
	return loweredText(sketchfront.Check(src), &src)
}

const tryParensPrelude = `type Bad { At string }

type R { N int }

func f(v int) int fails Bad {
	if v < 0 {
		return Bad{At: "f"}
	}
	v
}

func g(v int) int { v + 1 }

func r(v int) R fails Bad { R{N: try f(v)} }

func main() {}
`

// tryParensProgram is a prelude and a few functions whose let values
// join generated atoms with + - *.
func tryParensProgram(rng *rand.Rand) string {
	atoms := []string{
		"(try f(%d))",
		"g(%d)",
		"(try r(%d)).N",
		"-(try f(%d))",
		"g((try f(%d)))",
		"(try f(%d) + g(1))",
		"(try f(%d)) * 2",
		"(g(%d) * 2)",
	}
	var b strings.Builder
	b.WriteString(tryParensPrelude)
	for k := 1; k <= 4; k++ {
		fmt.Fprintf(&b, "\nfunc case%d() int fails Bad {\n\tlet x = ", k)
		for i, n := 0, 1+rng.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteString([]string{" + ", " - ", " * "}[rng.Intn(3)])
			}
			fmt.Fprintf(&b, atoms[rng.Intn(len(atoms))], rng.Intn(9))
		}
		b.WriteString("\n\tx\n}\n")
	}
	return b.String()
}
