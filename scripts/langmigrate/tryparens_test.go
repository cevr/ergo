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
// position, in arguments, under a unary operator, before a selector or
// a method call, in an assert (a shown comparison of ints or of slices,
// a negation, a selector, a method call), as the
// receiver of a matched Get, and around a binary expression inside a
// tighter operator. tryparens must leave the lowered Go the same (the
// program's meaning), a second run must change nothing, and every pair
// goes but the ones the parse needs (q105: parentheses carry no
// meaning): here, the pairs round a binary expression. The domain must
// reach both a removal and a kept parenthesis.
// ERGO_TRYPARENS_SEED=<seed> replays one seed.
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
		src, needed := tryParensProgram(rand.New(rand.NewSource(seed)))
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
		if k := len(tryParens(once)); k != needed {
			t.Fatalf("seed %d: %d (try …) kept, the parse needs %d\nbefore:\n%s\nafter:\n%s\n%s", seed, k, needed, src, once, replay)
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

func h(v int) bool fails Bad { try f(v) > 3 }

func twice(x R) int { x.N * 2 }

func s(v int) []int fails Bad { []int{try f(v), v} }

func m(v int) Map[string, int] fails Bad { Map[string, int]{"a": try f(v)} }

func main() {}
`

// tryParensProgram is a prelude, a few functions whose let values join
// generated atoms with + - *, a test of generated asserts and a match on
// a Map's Get; needed is the number of (try …) the parse needs (the
// atoms round a binary expression).
func tryParensProgram(rng *rand.Rand) (src string, needed int) {
	atoms := []struct {
		text   string
		needed bool
	}{
		{"(try f(%d))", false},
		{"g(%d)", false},
		{"(try r(%d)).N", false},
		{"(try r(%d)).twice()", false},
		{"-(try f(%d))", false},
		{"g((try f(%d)))", false},
		{"(try f(%d) + g(1)) * 2", true},
		{"(try f(%d)) * 2", false},
		{"(g(%d) * 2)", false},
	}
	asserts := []string{
		"(try f(%d)) == 2",
		"!(try h(%d))",
		"(try r(%d)).N == 2",
		"(try r(%d)).N == 2 && g(1) > 0",
		"g(1) == (try f(%d))",
		"(try s(%d)) == []int{1, 2}",
		"(try r(%d)).twice() == 2",
	}
	var b strings.Builder
	b.WriteString(tryParensPrelude)
	for k := 1; k <= 4; k++ {
		fmt.Fprintf(&b, "\nfunc case%d() int fails Bad {\n\tlet x = ", k)
		for i, n := 0, 1+rng.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteString([]string{" + ", " - ", " * "}[rng.Intn(3)])
			}
			a := atoms[rng.Intn(len(atoms))]
			fmt.Fprintf(&b, a.text, rng.Intn(9))
			if a.needed {
				needed++
			}
		}
		b.WriteString("\n\tx\n}\n")
	}
	b.WriteString("\ntest Generated {\n")
	for i, n := 0, 1+rng.Intn(3); i < n; i++ {
		b.WriteString("\tassert ")
		fmt.Fprintf(&b, asserts[rng.Intn(len(asserts))], rng.Intn(9))
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "\nfunc look(k string) int fails Bad {\n\tmatch (try m(%d)).Get(k) {\n\tcase Some[int] as v:\n\t\tv.Value\n\tcase None:\n\t\t0\n\t}\n}\n", rng.Intn(9))
	return b.String(), needed
}
