package machinegraph

import (
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The graph against the generator's model: generated machines (a step
// that matches on its state and signal with arms of any variant pairs,
// whose values are Decision literals, if/else and helpers; an after
// function per state) are written as Ergo source and checked, and the
// graph read from them must be the model's: the arms in order with their
// variants, each arm's edges (kind, target, line), the timers, and the
// pairs Reachable lists with their arms. ERGO_GRAPH_SEED replays a seed; a
// failure prints the source.

// garm is one generated arm: its state and signal (-1 for any), and its
// edges as the model expects them, with lines filled in when written.
type garm struct {
	state, sig int
	edges      []Edge
	line       int
}

type gmachine struct {
	ns, ne     int
	arms       []garm
	timers     map[int]int // state -> timer signal
	src        string
	timerLines map[int]int
}

func sv(i int) string { return "S" + strconv.Itoa(i) }
func ev(i int) string { return "E" + strconv.Itoa(i) }

// gen writes a machine and its model.
func gen(r *rand.Rand) *gmachine {
	m := &gmachine{ns: r.Intn(4) + 1, ne: r.Intn(3) + 1, timers: map[int]int{}, timerLines: map[int]int{}}
	var b strings.Builder
	line := 1
	w := func(format string, args ...any) {
		s := fmt.Sprintf(format, args...)
		b.WriteString(s)
		line += strings.Count(s, "\n")
	}
	var states, sigs []string
	for i := range m.ns {
		states = append(states, sv(i)+"{N int}")
	}
	for j := range m.ne {
		sigs = append(sigs, ev(j)+"{}")
	}
	w("type State = %s\n", strings.Join(states, " | "))
	w("type Signal = %s\n", strings.Join(sigs, " | "))
	w("func flip() bool { true }\n")
	// helpers: one per kind, read through (empty footprint).
	helperLine := line
	w("func rejected() Decision[State, int] {\n\tReject\n}\n")
	w("func step(s State, e Signal) Decision[State, int] {\n\tmatch (s, e) {\n")
	covered := map[[2]int]bool{}
	takes := func(a garm) [][2]int {
		var out [][2]int
		for i := range m.ns {
			for j := range m.ne {
				if (a.state < 0 || a.state == i) && (a.sig < 0 || a.sig == j) {
					out = append(out, [2]int{i, j})
				}
			}
		}
		return out
	}
	value := func(indent string) []Edge {
		at := line
		switch r.Intn(8) {
		case 0:
			k := r.Intn(m.ns)
			w("%sEnter[State]{Next: %s{N: 1}}\n", indent, sv(k))
			return []Edge{{Decision: "Enter", To: sv(k), Site: Site{"gen.ergo", at}}}
		case 1:
			k := r.Intn(m.ns)
			w("%sStay[State]{Next: %s{N: 2}}\n", indent, sv(k))
			return []Edge{{Decision: "Stay", To: sv(k), Site: Site{"gen.ergo", at}}}
		case 2:
			w("%sStay[State]{Next: s}\n", indent)
			return []Edge{{Decision: "Stay", To: Any, Site: Site{"gen.ergo", at}}}
		case 3:
			k := r.Intn(m.ns)
			w("%sEmit[State, int]{Next: %s{N: 0}, Output: 3}\n", indent, sv(k))
			return []Edge{{Decision: "Emit", To: sv(k), Site: Site{"gen.ergo", at}}}
		case 4:
			w("%sIgnore\n", indent)
			return []Edge{{Decision: "Ignore", Site: Site{"gen.ergo", at}}}
		case 5:
			w("%sDone[int]{Output: 2}\n", indent)
			return []Edge{{Decision: "Done", Site: Site{"gen.ergo", at}}}
		case 6:
			w("%srejected()\n", indent)
			return []Edge{{Decision: "Reject", Site: Site{"gen.ergo", helperLine + 1}}}
		}
		w("%sReject\n", indent)
		return []Edge{{Decision: "Reject", Site: Site{"gen.ergo", at}}}
	}
	pat := func(i int, name func(int) string) string {
		if i < 0 {
			return "_"
		}
		return name(i)
	}
	for range r.Intn(m.ns*m.ne+2) + 1 {
		a := garm{state: r.Intn(m.ns+1) - 1, sig: r.Intn(m.ne+1) - 1}
		fresh := false
		for _, p := range takes(a) {
			if !covered[p] {
				fresh = true
			}
		}
		if !fresh {
			continue
		}
		for _, p := range takes(a) {
			covered[p] = true
		}
		a.line = line
		w("\tcase (%s, %s):\n", pat(a.state, sv), pat(a.sig, ev))
		if r.Intn(4) == 0 {
			w("\t\tif flip() {\n")
			a.edges = append(a.edges, value("\t\t\t")...)
			w("\t\t} else {\n")
			a.edges = append(a.edges, value("\t\t\t")...)
			w("\t\t}\n")
		} else {
			a.edges = value("\t\t")
		}
		m.arms = append(m.arms, a)
	}
	if len(covered) < m.ns*m.ne {
		a := garm{state: -1, sig: -1, line: line}
		w("\tcase (_, _):\n")
		a.edges = value("\t\t")
		m.arms = append(m.arms, a)
	}
	w("\t}\n}\n")
	w("func after(s State) Option[Timeout[Signal]] {\n\tmatch s {\n")
	for i := range m.ns {
		w("\tcase %s:\n", sv(i))
		if r.Intn(2) == 0 {
			k := r.Intn(m.ne)
			m.timers[i] = k
			m.timerLines[i] = line
			w("\t\tSome[Timeout[Signal]]{Value: Timeout[Signal]{In: 1s, Signal: %s}}\n", ev(k))
		} else {
			w("\t\tNone\n")
		}
	}
	w("\t}\n}\n")
	w("type M machine[State, Signal] {\n\tinitial: S0{N: 0},\n\tstep: step,\n\tafter: after,\n}\n")
	w("func main() {}\n")
	m.src = b.String()
	return m
}

// want is the model's graph.
func (m *gmachine) want() *Graph {
	g := &Graph{Machine: "M", Initial: "S0", Timers: map[string]Timer{}, Work: map[string]Site{}}
	for i := range m.ns {
		g.States = append(g.States, Variant{Name: sv(i)})
	}
	for j := range m.ne {
		g.Signals = append(g.Signals, Variant{Name: ev(j)})
	}
	for _, a := range m.arms {
		arm := Arm{Site: Site{"gen.ergo", a.line}, Edges: dedupe(a.edges)}
		if a.state >= 0 {
			arm.States = []string{sv(a.state)}
		}
		if a.sig >= 0 {
			arm.Signals = []string{ev(a.sig)}
		}
		g.Arms = append(g.Arms, arm)
	}
	for i, k := range m.timers {
		g.Timers[sv(i)] = Timer{Signal: ev(k), Site: Site{"gen.ergo", m.timerLines[i]}}
	}
	return g
}

// reachable is the model's coverage: a breadth-first walk of its edges.
func (m *gmachine) reachable() []Pair {
	reached := map[int]bool{0: true}
	queue := []int{0}
	armOf := func(i, j int) int {
		for k, a := range m.arms {
			if (a.state < 0 || a.state == i) && (a.sig < 0 || a.sig == j) {
				return k
			}
		}
		return -1
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		for j := range m.ne {
			for _, e := range m.arms[armOf(i, j)].edges {
				var next []int
				switch {
				case e.To == Any:
					for k := range m.ns {
						next = append(next, k)
					}
				case e.To != "":
					k, _ := strconv.Atoi(strings.TrimPrefix(e.To, "S"))
					next = append(next, k)
				}
				for _, k := range next {
					if !reached[k] {
						reached[k] = true
						queue = append(queue, k)
					}
				}
			}
		}
	}
	var out []Pair
	for i := range m.ns {
		if !reached[i] {
			continue
		}
		for j := range m.ne {
			out = append(out, Pair{State: sv(i), Signal: ev(j), Arms: []Site{{"gen.ergo", m.arms[armOf(i, j)].line}}})
		}
	}
	return out
}

// read checks src and reads its machine's graph.
func read(src string) (*Graph, error) {
	file, err := syntax.Parse(src)
	if err != nil {
		return nil, err
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	for _, d := range diags {
		return nil, fmt.Errorf("%s %s at %d:%d", d.Code, d.Message, d.Line, d.Column)
	}
	gs := Machines(pkg, func(_ syntax.Node, pos syntax.Pos) Site { return Site{"gen.ergo", file.Line(pos)} })
	if len(gs) != 1 {
		return nil, fmt.Errorf("%d machines", len(gs))
	}
	return gs[0], nil
}

// TestGraphMatchesTheModel is the graph's acceptance property (MU15).
func TestGraphMatchesTheModel(t *testing.T) {
	seed := int64(1)
	if s := os.Getenv("ERGO_GRAPH_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	seen := map[string]bool{}
	for n := 0; n < 150; n++ {
		m := gen(r)
		g, err := read(m.src)
		if err != nil {
			t.Fatalf("seed %d, case %d: %v\n%s", seed, n, err, m.src)
		}
		want := m.want()
		if diff := compare(g, want); diff != "" {
			t.Fatalf("seed %d, case %d: %s\ngot:\n%s\nwant:\n%s\nsource:\n%s", seed, n, diff, g.Text(), want.Text(), m.src)
		}
		if got, want := g.Reachable(), m.reachable(); !slices.EqualFunc(got, want, func(a, b Pair) bool {
			return a.State == b.State && a.Signal == b.Signal && slices.Equal(a.Arms, b.Arms)
		}) {
			t.Fatalf("seed %d, case %d: reachable\n got %v\nwant %v\nsource:\n%s", seed, n, got, want, m.src)
		}
		for _, a := range g.Arms {
			for _, e := range a.Edges {
				seen[e.Decision+" "+fmt.Sprint(e.To == Any)] = true
			}
		}
	}
	for _, k := range []string{"Enter false", "Stay false", "Stay true", "Emit false", "Ignore false", "Reject false", "Done false"} {
		if !seen[k] {
			t.Errorf("seed %d: no case reached %q", seed, k)
		}
	}
}

// compare is "" when g is want, with variant types ignored.
func compare(g, want *Graph) string {
	names := func(vs []Variant) []string {
		var out []string
		for _, v := range vs {
			out = append(out, v.Name)
		}
		return out
	}
	switch {
	case g.Machine != want.Machine || g.Initial != want.Initial:
		return "machine or initial"
	case !slices.Equal(names(g.States), names(want.States)) || !slices.Equal(names(g.Signals), names(want.Signals)):
		return "states or signals"
	case len(g.Arms) != len(want.Arms):
		return "arm count"
	}
	for i := range g.Arms {
		a, b := g.Arms[i], want.Arms[i]
		if a.Site != b.Site || !slices.Equal(a.States, b.States) || !slices.Equal(a.Signals, b.Signals) || a.Partial || !slices.Equal(a.Edges, b.Edges) {
			return fmt.Sprintf("arm %d", i)
		}
	}
	if len(g.Timers) != len(want.Timers) || len(g.Unsure) > 0 {
		return "timers"
	}
	for k, v := range want.Timers {
		if g.Timers[k] != v {
			return "timer of " + k
		}
	}
	return ""
}
