package lower

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// tailLines are the line shapes a generated inlined body draws from: the
// declarations and tails renameTail looks for, the ends it must refuse,
// and the shapes tailMayAssign reasons about (a tail split over two lines,
// a trailing comment, a //line directive, a raw string, a block comment).
var tailLines = []string{
	"var x int",
	"var x int = f()",
	"x := f()",
	"y := x + 1",
	"res = x",
	"res = y",
	"res =",
	"\tx",
	"res = x // keep",
	"a(); res = x",
	"res == x",
	"if x > 0 {",
	"}",
	"goto end",
	"g(func() { x++ })",
	"//line main.ergo:3:1",
	"// a comment",
	"",
	"s := `raw",
	"text`",
	"/* block",
	"*/",
	"r.x = 1",
	"x:",
	"m := map[string]int{x: 1}",
	"res = x;",
	"res = x }",
}

// tailEnds are the last statements of a body that renameTail may rewrite.
var tailEnds = []string{"res = x", "res =\n\tx", "res = x // keep", "a(); res = x", "res = y"}

// tailTrails follow a body's last statement and hold no token, or only a
// comment's.
var tailTrails = []string{"//line main.ergo:9:1", "// done", "", "/* block\n*/", "/* inline */"}

// TestRenameTailTailCheckAgrees (seeded property): renameTail, which
// reads the last line first, rewrites every generated body exactly as the
// reference, renameTailScan over the whole body, does. It kills the
// mutants lw1b-tail-two-tokens-refused (a tail split as "res =" then "X"
// is refused unscanned), lw1b-tail-block-judged (a body with a block comment
// is judged by its last line) and lw1b-tail-comment-line-read (a last line
// of only a comment is read as the tail). A failing seed prints the body and both
// results; replay with go test -run TestRenameTailTailCheckAgrees.
func TestRenameTailTailCheckAgrees(t *testing.T) {
	renamed, skipped := 0, 0
	for seed := uint64(0); seed < 4000; seed++ {
		r := rand.New(rand.NewPCG(seed, 9))
		var b strings.Builder
		line := func(s string) { b.WriteString(s + "\n") }
		// A declaration of x first, half the time, so renames happen.
		if r.IntN(2) == 0 {
			line(tailLines[r.IntN(3)])
		}
		for range r.IntN(4) {
			line(tailLines[r.IntN(len(tailLines))])
		}
		// A tail, half the time: the shapes a rename ends with.
		if r.IntN(2) == 0 {
			line(tailEnds[r.IntN(len(tailEnds))])
		}
		// Then, sometimes, lines with no token of their own.
		for range r.IntN(3) {
			line(tailTrails[r.IntN(len(tailTrails))])
		}
		body := b.String()
		for _, iface := range []bool{false, true} {
			got, gotOK := renameTail(body, "res", "int", iface)
			want, wantOK := renameTailScan(body, "res", "int", iface)
			if got != want || gotOK != wantOK {
				t.Fatalf("seed %d iface %v: renameTail differs from the whole scan\nbody:\n%s\ngot (%v):\n%s\nwant (%v):\n%s", seed, iface, body, gotOK, got, wantOK, want)
			}
			if wantOK {
				renamed++
			}
			if !tailMayAssign(body, "res") {
				skipped++
			}
		}
	}
	// The generator reaches both a rename and a body the last line refuses.
	if renamed == 0 || skipped == 0 {
		t.Fatalf("renamed %d, refused by the last line %d: the generator misses a side", renamed, skipped)
	}
}
