package syntax

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var skipNameSeed = flag.Uint64("skipname.seed", 0, "run TestSkipIsAName at this seed alone")

// skipNamePlaces are the places a name stands, each with $ for the name:
// declarations, binders, uses, a call and an assignment in a test body, a
// selector, a field key and a method. The program of a seed takes some of
// them, in order.
var skipNamePlaces = []string{
	"func $(n int) int { n }\n",
	"func f(x int, $ int) int { x }\n",
	"type T { $ int, k int }\n",
	"type $ { k int }\n",
	"type Op {\n\t$() int,\n}\n",
	"let $ = 1\n",
	"test T {\n\t$(1)\n}\n",
	"test T {\n\tlet mut $ = 1\n\t$ = 2\n\tassert $ == 2\n}\n",
	"test T {\n\tlet x = R{$: 1}\n\tassert x.$ == 1\n}\n",
	"law L(n int) {\n\tlet $ = n\n\t$ == n\n}\n",
	"func g() {\n\t$(\"a\", 1)\n}\n",
	"func h(f func() int) int {\n\tlet $ = f\n\t$()\n}\n",
}

// TestSkipIsAName: skip is a contextual keyword (runtime.md, Tests 4), a
// statement only where the name skip is followed directly by a string
// literal, so everywhere else it is a name like any other. A seeded
// property: a program over skipNamePlaces, each place's name drawn as skip
// or another name, parses to the tree of the same program with every skip
// spelled skap (the same length, so every position agrees). A scanner or
// parser that reserved skip, or read skip(…) as the statement, breaks it.
// It catches tskip-scanner-reserves and tskip-call-is-skip.
func TestSkipIsAName(t *testing.T) {
	seeds := []uint64{*skipNameSeed}
	if *skipNameSeed == 0 {
		seeds = nil
		for s := uint64(1); s <= 200; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x736b))
		var b strings.Builder
		for i, place := range skipNamePlaces {
			if r.IntN(2) == 0 {
				continue
			}
			name := "skip"
			if r.IntN(3) == 0 {
				name = fmt.Sprintf("n%03d", i)
			}
			b.WriteString(strings.ReplaceAll(place, "$", name) + "\n")
		}
		src := b.String()
		other := strings.ReplaceAll(src, "skip", "skap")
		got, want := skipNameTree(src), skipNameTree(other)
		if got != want {
			t.Fatalf("seed %d: the program with skip parses apart from the one with skap\n%s\nwith skip:\n%s\nwith skap:\n%s\nreplay: go test ./internal/sketch/syntax -run TestSkipIsAName -skipname.seed=%d", seed, src, got, want, seed)
		}
	}
}

// skipNameTree is a source's parse: its nodes' kinds and positions, or
// its error.
func skipNameTree(src string) string {
	f, err := Parse(src)
	if err != nil {
		return "error: " + err.Error()
	}
	var b strings.Builder
	Inspect(f, func(n Node) bool {
		if n != nil {
			fmt.Fprintf(&b, "%T@%d ", n, n.Pos())
		}
		return true
	})
	return b.String()
}
