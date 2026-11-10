package format

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The trailing function literal's seeded property (L-tb, owner q68/q72;
// grammar.md, Expressions 9). Each case writes one call two ways: with a
// function literal after the `)`, `f(a, b) func(x int) int { … }`, and
// with the same literal as the last argument, `f(a, b, func(x int) int {
// … })`. It varies the arguments before the literal (none to three), the
// literal's parameters (none, typed, untyped), its result, its body (a
// tail, or an early return), where the call stands (a let, a statement, a
// return, an argument of another call, a chained selector base) and a
// comment after it. The property:
//
//   - both spellings parse to one tree, node for node (the trailing
//     literal is the call's last argument);
//   - each spelling is canonical: the formatter prints the trailing
//     spelling back with the literal after the `)`, and the argument
//     spelling unchanged;
//   - in a control clause header, where a `{` after the call opens the
//     body, the trailing spelling is refused and the argument spelling
//     parses.
//
// Replay: go test -run '^TestTrailingFuncAgrees$' ./internal/sketch/format -args -tb.seed=N

var tbSeed = flag.Uint64("tb.seed", 0, "run TestTrailingFuncAgrees on this seed alone")

// tbCase is one generated call in both spellings, and whether it stands
// in a control clause header.
type tbCase struct {
	trailing, inline string
	header           bool
}

func tbProgram(seed uint64) tbCase {
	r := rand.New(rand.NewPCG(seed, 0x7b))
	var args []string
	for i := r.IntN(4); i > 0; i-- {
		args = append(args, []string{"a", "1", "xs", "\"s\"", "g(2)"}[r.IntN(5)])
	}
	var lit string
	switch r.IntN(4) {
	case 0:
		lit = "func() {\n\t\twork()\n\t}"
	case 1:
		lit = "func(x) {\n\t\tx * 2\n\t}"
	case 2:
		lit = "func(x int, y string) int {\n\t\tif x > 0 {\n\t\t\treturn x\n\t\t}\n\t\tlen(y)\n\t}"
	default:
		lit = "func(t Item) Node {\n\t\trow(t)\n\t}"
	}
	call := "each(" + strings.Join(args, ", ") + ")"
	inline := "each(" + strings.Join(append(slices(args), lit), ", ") + ")"
	trailing := call + " " + lit
	comment := ""
	if r.IntN(2) == 0 {
		comment = " // after"
	}
	header := r.IntN(6) == 0
	var wrap func(string) string
	switch {
	case header:
		wrap = func(c string) string { return "\tif " + c + " {\n\t\twork()\n\t}" }
	default:
		switch r.IntN(5) {
		case 0:
			wrap = func(c string) string { return "\tlet v = " + c + comment + "\n\tuse(v)" }
		case 1:
			wrap = func(c string) string { return "\t" + c + comment }
		case 2:
			wrap = func(c string) string { return "\treturn " + c + comment }
		case 3:
			wrap = func(c string) string { return "\tuse(" + c + ")" + comment }
		default:
			wrap = func(c string) string { return "\tlet v = " + c + ".Len()" + comment + "\n\tuse(v)" }
		}
	}
	src := func(c string) string { return "func main() {\n" + wrap(c) + "\n}\n" }
	return tbCase{trailing: src(trailing), inline: src(inline), header: header}
}

func slices(xs []string) []string { return append([]string(nil), xs...) }

// tbShape is a tree's nodes in walk order, with names and literal values
// and without positions or the TrailingFunc mark.
func tbShape(f *syntax.File) string {
	var b strings.Builder
	syntax.Inspect(f, func(n syntax.Node) bool {
		if n == nil {
			return true
		}
		fmt.Fprintf(&b, "%T", n)
		switch n := n.(type) {
		case *syntax.Ident:
			b.WriteString(" " + n.Name)
		case *syntax.BasicLit:
			b.WriteString(" " + n.Value)
		}
		b.WriteString("\n")
		return true
	})
	return b.String()
}

// TestTrailingFuncAgrees is the seeded property (see above).
func TestTrailingFuncAgrees(t *testing.T) {
	n := uint64(200)
	first := uint64(1)
	if *tbSeed != 0 {
		first, n = *tbSeed, 1
	}
	reached := map[string]bool{}
	for seed := first; seed < first+n; seed++ {
		c := tbProgram(seed)
		replay := fmt.Sprintf("Replay: go test -run '^TestTrailingFuncAgrees$' ./internal/sketch/format -args -tb.seed=%d", seed)
		inl, err := syntax.Parse(c.inline)
		if err != nil {
			t.Fatalf("seed %d: the argument spelling does not parse: %v\n%s\n%s", seed, err, replay, c.inline)
		}
		tr, err := syntax.Parse(c.trailing)
		if c.header {
			reached["header"] = true
			if err == nil {
				t.Fatalf("seed %d: a trailing function literal in a control clause header parsed\n%s\n%s", seed, replay, c.trailing)
			}
			continue
		}
		if err != nil {
			t.Fatalf("seed %d: the trailing spelling does not parse: %v\n%s\n%s", seed, err, replay, c.trailing)
		}
		reached["trailing"] = true
		if a, b := tbShape(tr), tbShape(inl); a != b {
			t.Fatalf("seed %d: the two spellings parse to different trees\n%s\n--- trailing\n%s--- argument\n%s", seed, replay, a, b)
		}
		for _, s := range []string{c.trailing, c.inline} {
			out, err := Source(s)
			if err != nil || out != s {
				t.Fatalf("seed %d: not canonical (%v)\n%s\n--- source\n%s--- formatted\n%s", seed, err, replay, s, out)
			}
		}
	}
	if *tbSeed == 0 && (!reached["header"] || !reached["trailing"]) {
		t.Errorf("the seeds never reach every context: %v", reached)
	}
}
