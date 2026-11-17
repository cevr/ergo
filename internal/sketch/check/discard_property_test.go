package check

import (
	"flag"
	"math/rand/v2"
	"strings"
	"testing"
)

// The dropped-result rule's seeded property (M2-APPEND; discard.go,
// diagnostics.md S110). Each seed writes a main of statements drawn from
// calls that differ in one condition of the rule each: a prelude operation
// (Append, Put, Get), a pure function, a dot call returning its
// receiver's type, a borrow written (&c) or implied (c.Bump()), a
// capability call, a function value it keeps, a fallible call, a call
// that raises a defect by name (Unreachable) directly or through a callee,
// a call with no result, a call that makes a resource and one that reads
// one. Each is a statement, or the discarded tail of an if's arms. The
// reference decides each line: refused with the rebinding (xs = xs.F(…))
// when the call returns its receiver's type, refused with "bind it with
// let" when it does not, or not refused. The checker's S110 lines of the
// rule must be the reference's, under the default pure collections and
// under the reference one (SetPureCollectionsForTest(false)).
//
// Mutants it kills (testdata/mutants.json): the operand borrow test
// removed (lends(&c), a borrow as a lent type argument), the parameter
// borrow test removed (Bump(&c), c.Bump()), the
// operand resource test removed (peek(a)), the result resource test
// removed (open(1)), the footprint test removed (call(one)), the
// failure-row test removed (parse(n)), the Unreachable walk removed
// (must(n)) or made one level deep (must2(n)), the prelude-collection
// clause removed (Append under the reference collections), and the
// rebinding message chosen for any type (m.Get).
//
// Replay: go test -run '^TestDroppedResultModel$' ./internal/sketch/check -args -dropped.seed=N

var droppedSeed = flag.Uint64("dropped.seed", 0, "run TestDroppedResultModel on this seed alone")

const droppedPrelude = `type Ctr { n int }

type Bad { why string }

type Res { id int } implements Closer

func Close(r Res) {}

func open(n int) Res { Res{id: n} }

func peek(r Res) int { r.id }

func twice(n int) int { n * 2 }

func Add(c Ctr, k int) Ctr { Ctr{n: c.n + k} }

func Bump(c *Ctr) int {
	c.n += 1
	c.n
}

func logs(n int) int needs Console {
	Console.Println("logs")
	n
}

func must(n int) int {
	if n >= 0 { n } else { Unreachable("negative") }
}

func must2(n int) int { must(n) + 1 }

func call(f own func() int) int { f() }

func one() int { 1 }

func parse(n int) int fails Bad {
	if n < 0 {
		return Bad{why: "negative"}
	}
	n
}

func noop(n int) {}

func lends[T lent](x T) int { 1 }

func main() fails Bad needs Console {
	let n = 3
	let mut xs = []int{1}
	let mut m = Map[string, int]{}
	let mut c = Ctr{n: 0}
	let a = open(1)
`

// droppedCalls are the statements and the reference's verdict of each:
// "rebind", "let" or "" (not refused by the rule).
var droppedCalls = []struct{ call, verdict string }{
	{"xs.Append(1)", "rebind"},
	{"m.Put(\"a\", 1)", "rebind"},
	{"m.Get(\"a\")", "let"},
	{"twice(n)", "let"},
	{"c.Add(1)", "rebind"},
	{"Bump(&c)", ""},
	{"c.Bump()", ""},
	{"lends(&c)", ""},
	{"logs(n)", ""},
	{"call(one)", ""},
	{"must(n)", ""},
	{"must2(n)", ""},
	{"noop(n)", ""},
	{"open(1)", ""},
	{"peek(a)", ""},
	{"try parse(n)", ""},
	{"parse(n)", ""},
}

// droppedProgram is seed's source and the verdict of each refused line.
func droppedProgram(seed uint64) (string, map[int]string) {
	r := rand.New(rand.NewPCG(seed, 0xd20b))
	var b strings.Builder
	b.WriteString(droppedPrelude)
	line := strings.Count(droppedPrelude, "\n")
	emit := func(depth int, s string) int {
		line++
		b.WriteString(strings.Repeat("\t", depth) + s + "\n")
		return line
	}
	want := map[int]string{}
	for range 2 + r.IntN(6) {
		x := droppedCalls[r.IntN(len(droppedCalls))]
		y := droppedCalls[r.IntN(len(droppedCalls))]
		if r.IntN(3) > 0 || strings.Contains(x.call, "parse") || strings.Contains(y.call, "parse") || x.call == "noop(n)" || y.call == "noop(n)" {
			if at := emit(1, x.call); x.verdict != "" {
				want[at] = x.verdict
			}
			continue
		}
		// The discarded tails of an if statement's arms.
		emit(1, "if n > 2 {")
		if at := emit(2, x.call); x.verdict != "" {
			want[at] = x.verdict
		}
		emit(1, "} else {")
		if at := emit(2, y.call); y.verdict != "" {
			want[at] = y.verdict
		}
		emit(1, "}")
	}
	b.WriteString("\txs = xs.Append(2)\n\tm = m.Put(\"b\", 2)\n\tc = c.Add(1)\n")
	b.WriteString("\tConsole.Println(\"\\(len(xs)) \\(c.n) \\(n) \\(peek(a))\")\n}\n")
	return b.String(), want
}

// droppedVerdicts are the rule's diagnostics by line, and every other
// diagnostic's text.
func droppedVerdicts(diags []Diagnostic) (map[int]string, []string) {
	got := map[int]string{}
	var other []string
	for _, d := range diags {
		switch {
		case d.Code == "S110" && strings.Contains(d.Message, " = ") && strings.Contains(d.Message, "so this call does nothing"):
			got[d.Line] = "rebind"
		case d.Code == "S110" && strings.Contains(d.Message, "the call does nothing else"):
			got[d.Line] = "let"
		case strings.Contains(d.Message, "is discarded, and a resource has one owner"),
			d.Code == "S105":
			// The ownership pass's R42 and the fallible statement's S105.
		default:
			other = append(other, d.String())
		}
	}
	return got, other
}

// TestDroppedResultModel is the property (see above).
func TestDroppedResultModel(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(200) {
		seeds = append(seeds, s+1)
	}
	if *droppedSeed != 0 {
		seeds = []uint64{*droppedSeed}
	}
	for _, pure := range []bool{true, false} {
		restore := SetPureCollectionsForTest(pure)
		reached := map[string]bool{}
		for _, seed := range seeds {
			src, want := droppedProgram(seed)
			_, diags := CheckSource(src)
			got, other := droppedVerdicts(diags)
			if len(other) > 0 || !mapsEqual(got, want) {
				t.Errorf("seed %d (pure collections %v): the checker says %v, the reference %v; other diagnostics %v\nReplay: go test -run '^TestDroppedResultModel$' ./internal/sketch/check -args -dropped.seed=%d\n%s", seed, pure, got, want, other, seed, src)
				break
			}
			for _, v := range want {
				reached[v] = true
			}
		}
		restore()
		if *droppedSeed == 0 && (!reached["rebind"] || !reached["let"]) {
			t.Errorf("pure collections %v: the seeds reached %v, not both refusals", pure, reached)
		}
	}
}
