package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestEvalEffectsAgreeWithModel is the effect property of ORACLE O1
// (AGENTS.md, Tests: a seeded property against an independent reference
// model). Each seed generates a test whose body runs a sequence of
// effects through capabilities: Put, Get and Delete of an operation type
// Store whose provider keeps its cells in a sync.Ref; functions that need
// Store and io.Stdout and so take the providers of their caller; an
// operation value (a Table passed as a Store parameter); nested withs that
// rebind Store to a new provider, to the outer one again, to a named
// context's provider and to a local context's; and draws of a seeded
// Random. Every line is written through io.Stdout to an io.Recorder. The
// model is a Go interpreter of the same steps over Go maps: a stack of
// stores, the scale each store multiplies by, and a PCG of the same seed.
// The test asserts the recorder's text equals the model's; the member law
// DeleteAfterPut runs against Table and under the named context Doubled
// (types.md, Laws 10). The evaluator must pass all three obligations. A failing seed prints the Ergo source, the
// result and the replay command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o1-skips-effect,
// oracle-o1-outer-binding-wins, oracle-o1-with-binding-leaks,
// oracle-o1-callee-loses-caps, oracle-o1-closure-caps-late,
// oracle-o1-member-law-unbound.
func TestEvalEffectsAgreeWithModel(t *testing.T) {
	n := 120
	if testing.Short() {
		n = 40
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_EFFECT_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	reached := map[string]bool{}
	for seed := first; seed < first+uint64(n); seed++ {
		g := &effectGen{r: rand.New(rand.NewPCG(seed, 0x0e)), seed: seed, reached: reached}
		src := g.program()
		pkg := checked(t, src)
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		for _, res := range rep.Results {
			if res.Status != Passed {
				t.Fatalf("seed %d: %s is %s (%s at %s:%d)\n%s\nreplay: ERGO_EVAL_EFFECT_SEED=%d go test -run TestEvalEffectsAgreeWithModel ./internal/sketch/eval",
					seed, res.Name, res.Status, res.Reason, res.Site.File, res.Site.Line, src, seed)
			}
		}
		if len(rep.Results) != 3 {
			t.Fatalf("seed %d: results %+v", seed, rep.Results)
		}
	}
	if n > 1 {
		for _, k := range effectForms {
			if !reached[k] {
				t.Fatalf("the seeds never reached the form %s (reached %v)", k, reached)
			}
		}
	}
}

// effectForms lists every step a run of seeds must reach.
var effectForms = []string{"put", "show", "bump", "drop", "via", "draw", "with-inline", "with-outer", "with-named", "with-local", "missing"}

const effectPrelude = `import io
import rand
import strconv
import sync

type Missing { Key string }

type Store {
	Put(k string, v int),
	Get(k string) int fails Missing,
	Delete(k string) fails Missing,

	// DeleteAfterPut: a key put is there to delete.
	law DeleteAfterPut(k string, v int) { DeleteAfterPutHolds(k, v) },
}

type Table { cells sync.Ref[Map[string, int]], scale int }

func NewTable() Table { Scaled(1) }

func Scaled(by int) Table { Table{cells: sync.NewRef[Map[string, int]](Map[string, int]{}), scale: by} }

func Put(t Table, k string, v int) {
	t.cells.Update(func(m Map[string, int]) Map[string, int] { m.Put(k, v*t.scale) })
}

func Get(t Table, k string) int fails Missing {
	match t.cells.Get().Get(k) {
	case Some[int] as s:
		s.Value
	case None:
		Missing{Key: k}
	}
}

func Delete(t Table, k string) fails Missing {
	let found = sync.Modify(t.cells, func(m Map[string, int]) (bool, Map[string, int]) {
		if m.Get(k) == None {
			return (false, m)
		}
		(true, m.Delete(k))
	})
	if !found {
		return Missing{Key: k}
	}
}

func DeleteAfterPutHolds(k string, v int) bool needs Store {
	Store.Put(k, v)
	Store.Delete(k) catch {
	case Missing:
		return false
	}
	true
}

let Doubled = context { Store: Scaled(2) }

func say(line string) needs io.Stdout { io.Stdout.Println(line) }

func show(k string) needs Store | io.Stdout {
	let v = Store.Get(k) catch {
	case Missing as m:
		say("missing " + m.Key)
		return
	}
	say(k + "=" + strconv.Itoa(v))
}

func bump(k string) needs Store {
	let v = Store.Get(k) catch {
	case Missing:
		0
	}
	Store.Put(k, v+1)
}

func drop(k string) needs Store | io.Stdout {
	Store.Delete(k) catch {
	case Missing as m:
		say("no " + m.Key)
	}
}

func via(s Store, k string, v int) { s.Put(k, v) }
`

// effectStore is the model of one Table: its cells and its scale.
type effectStore struct {
	cells map[string]int
	scale int
}

type effectGen struct {
	r       *rand.Rand
	seed    uint64
	b       strings.Builder
	out     []string
	stack   []*effectStore // the Store bound now, last
	outer   *effectStore   // the test's Table t
	draws   *rand.Rand     // the model of rand.Seeded
	reached map[string]bool
}

var effectKeys = []string{"a", "b", "c"}

func (g *effectGen) program() string {
	g.outer = &effectStore{cells: map[string]int{}, scale: 1}
	g.stack = []*effectStore{g.outer}
	rseed := g.r.Uint64N(1000)
	g.draws = rand.New(rand.NewPCG(rseed, randSeedHi))
	g.b.WriteString(effectPrelude)
	g.b.WriteString("\ntest Effects {\n\tlet out = io.NewRecorder()\n\tlet t = NewTable()\n")
	g.b.WriteString("\tlet local = context { Store: Scaled(5) }\n")
	fmt.Fprintf(&g.b, "\twith context { Store: t, io.Stdout: out, Random: rand.Seeded(%d) } {\n", rseed)
	g.steps(2, 4+g.r.IntN(8))
	g.b.WriteString("\t}\n")
	text := strings.Join(g.out, "")
	fmt.Fprintf(&g.b, "\tassert out.Text() == %s\n}\n", strconv.Quote(text))
	return g.b.String()
}

func (g *effectGen) top() *effectStore { return g.stack[len(g.stack)-1] }

func (g *effectGen) line(depth int, format string, args ...any) {
	g.b.WriteString(strings.Repeat("\t", depth))
	fmt.Fprintf(&g.b, format, args...)
	g.b.WriteByte('\n')
}

func (g *effectGen) say(s string) { g.out = append(g.out, s+"\n") }

func (g *effectGen) steps(depth, n int) {
	for i := 0; i < n; i++ {
		g.step(depth)
	}
}

func (g *effectGen) step(depth int) {
	k := effectKeys[g.r.IntN(len(effectKeys))]
	v := g.r.IntN(20) - 5
	kinds := []string{"put", "put", "show", "show", "bump", "drop", "via", "draw"}
	if depth < 4 {
		kinds = append(kinds, "with-inline", "with-outer", "with-named", "with-local")
	}
	kind := kinds[g.r.IntN(len(kinds))]
	g.reached[kind] = true
	s := g.top()
	switch kind {
	case "put":
		g.line(depth, "Store.Put(%q, %d)", k, v)
		s.cells[k] = v * s.scale
	case "show":
		g.line(depth, "show(%q)", k)
		if x, ok := s.cells[k]; ok {
			g.say(k + "=" + strconv.Itoa(x))
		} else {
			g.reached["missing"] = true
			g.say("missing " + k)
		}
	case "bump":
		g.line(depth, "bump(%q)", k)
		s.cells[k] = (s.cells[k] + 1) * s.scale
	case "drop":
		g.line(depth, "drop(%q)", k)
		if _, ok := s.cells[k]; ok {
			delete(s.cells, k)
		} else {
			g.reached["missing"] = true
			g.say("no " + k)
		}
	case "via":
		g.line(depth, "via(t, %q, %d)", k, v)
		g.outer.cells[k] = v
	case "draw":
		g.line(depth, "say(strconv.Itoa(Random.IntN(%d)))", v+10)
		g.say(strconv.Itoa(g.draws.IntN(v + 10)))
	default:
		var inner *effectStore
		switch kind {
		case "with-inline":
			scale := 2 + g.r.IntN(3)
			g.line(depth, "with context { Store: Scaled(%d) } {", scale)
			inner = &effectStore{cells: map[string]int{}, scale: scale}
		case "with-outer":
			g.line(depth, "with context { Store: t } {")
			inner = g.outer
		case "with-named":
			g.line(depth, "with Doubled {")
			inner = &effectStore{cells: map[string]int{}, scale: 2}
		case "with-local":
			g.line(depth, "with local {")
			inner = &effectStore{cells: map[string]int{}, scale: 5}
		}
		g.stack = append(g.stack, inner)
		g.steps(depth+1, 1+g.r.IntN(4))
		g.stack = g.stack[:len(g.stack)-1]
		g.line(depth, "}")
		// After the block the outer binding is back: show it.
		g.line(depth, "show(%q)", k)
		if x, ok := g.top().cells[k]; ok {
			g.say(k + "=" + strconv.Itoa(x))
		} else {
			g.say("missing " + k)
		}
	}
}
