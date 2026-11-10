package eval

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// mModel is a generated machine and its reference model: fieldless states
// S0..Sk-1 stepped by a table, and, when float is set, a state F{x
// float64} entered at x = 0.0, whose first signal negates x and whose
// second enters Minus when 1.0/x < 0.0 (so only from x = -0.0), else S0.
// Model states: 0..k-1 are Sk, k is F{x: 0.0}, k+1 F{x: -0.0}, k+2 Minus.
// When payload is set the signal type has a variant P{n int}, which steps
// as the first signal does: the signals cannot be listed, so the laws run
// over generated runs.
type mModel struct {
	name           string
	k, signals     int
	initial        int
	kind           [][]string // Enter, Stay, Emit, Ignore, Reject or Done
	next           [][]int    // a state index, k for F{x: 0.0}
	float, payload bool
	laws           []mLaw
}

type mLaw struct {
	name, kind string
	targets    []string // variant names: Sk, F or Minus
}

func (m *mModel) states() int {
	if m.float {
		return m.k + 3
	}
	return m.k
}

// edge is the reference's edge: the next model state, false for Done.
func (m *mModel) edge(s, e int) (int, bool) {
	switch {
	case s < m.k:
		switch m.kind[s][e] {
		case "Done":
			return 0, false
		case "Ignore", "Reject":
			return s, true
		}
		return m.next[s][e], true
	case s == m.k+2: // Minus
		return s, true
	case e == 0: // F negates x
		return m.k + (s-m.k+1)%2, true
	case e == 1: // F tests the sign of 1/x
		if s == m.k+1 {
			return m.k + 2, true
		}
		return 0, true
	}
	return s, true
}

// in reports whether the model state s is a value of the variant v.
func (m *mModel) in(s int, v string) bool {
	switch v {
	case "F":
		return s == m.k || s == m.k+1
	case "Minus":
		return s == m.k+2
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "S"))
	return s == n
}

// bfs is the reference search from the initial state: each reachable
// state's shortest depth.
func (m *mModel) bfs() map[int]int {
	depth := map[int]int{m.initial: 0}
	queue := []int{m.initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for e := 0; e < m.signals; e++ {
			if n, ok := m.edge(s, e); ok {
				if _, seen := depth[n]; !seen {
					depth[n] = depth[s] + 1
					queue = append(queue, n)
				}
			}
		}
	}
	return depth
}

func (m *mModel) holds(l mLaw) bool {
	if l.kind == "path" {
		layer := map[int]bool{}
		if m.in(m.initial, l.targets[0]) {
			layer[m.initial] = true
		}
		for _, t := range l.targets[1:] {
			next := map[int]bool{}
			for s := range layer {
				for e := 0; e < m.signals; e++ {
					if n, ok := m.edge(s, e); ok && m.in(n, t) {
						next[n] = true
					}
				}
			}
			layer = next
		}
		return len(layer) > 0
	}
	reached := false
	for s := range m.bfs() {
		reached = reached || m.in(s, l.targets[0])
	}
	return reached == (l.kind == "reaches")
}

func genMachine(r *rand.Rand, i int) *mModel {
	m := &mModel{name: fmt.Sprintf("M%d", i), k: 2 + r.Intn(4), signals: 1 + r.Intn(3)}
	m.float = m.signals >= 2 && r.Intn(2) == 0
	m.payload = r.Intn(4) == 0
	m.initial = r.Intn(m.k)
	kinds := []string{"Enter", "Enter", "Stay", "Emit", "Ignore", "Reject", "Reject", "Done"}
	targets := m.k
	if m.float {
		targets++ // F
	}
	for s := 0; s < m.k; s++ {
		m.kind = append(m.kind, make([]string, m.signals))
		m.next = append(m.next, make([]int, m.signals))
		for e := 0; e < m.signals; e++ {
			m.kind[s][e] = kinds[r.Intn(len(kinds))]
			m.next[s][e] = r.Intn(targets)
		}
	}
	variant := func(s int) string {
		switch {
		case s < m.k:
			return fmt.Sprintf("S%d", s)
		case s == m.k+2:
			return "Minus"
		}
		return "F"
	}
	pick := func() string { return variant(r.Intn(m.states())) }
	init := variant(m.initial)
	m.laws = append(m.laws,
		mLaw{"Reaches", "reaches", []string{pick()}},
		mLaw{"Never", "never", []string{pick()}},
		mLaw{"InitReach", "reaches", []string{init}},
		mLaw{"InitNever", "never", []string{init}})
	walk := []string{init}
	for s := m.initial; len(walk) < 2+r.Intn(3); {
		n, ok := m.edge(s, r.Intn(m.signals))
		if !ok {
			break
		}
		walk, s = append(walk, variant(n)), n
	}
	m.laws = append(m.laws, mLaw{"Walk", "path", walk})
	var random []string
	for k := 0; k < 1+r.Intn(4); k++ {
		random = append(random, pick())
	}
	m.laws = append(m.laws, mLaw{"Path", "path", random})
	if m.float {
		m.laws = append(m.laws, mLaw{"MinusReach", "reaches", []string{"Minus"}})
	}
	return m
}

func (m *mModel) source() string {
	var b strings.Builder
	st, sg := m.name+"State", m.name+"Signal"
	var vs []string
	for s := 0; s < m.k; s++ {
		vs = append(vs, fmt.Sprintf("%sS%d{}", m.name, s))
	}
	if m.float {
		vs = append(vs, m.name+"F{x float64}", m.name+"Minus{}")
	}
	fmt.Fprintf(&b, "type %s = %s\n\n", st, strings.Join(vs, " | "))
	var es []string
	for e := 0; e < m.signals; e++ {
		es = append(es, fmt.Sprintf("%sE%d{}", m.name, e))
	}
	if m.payload {
		es = append(es, m.name+"P{n int}")
	}
	fmt.Fprintf(&b, "type %s = %s\n\n", sg, strings.Join(es, " | "))
	state := func(s int) string {
		if s == m.k {
			return m.name + "F{x: 0.0}"
		}
		return fmt.Sprintf("%sS%d", m.name, s)
	}
	decision := func(s, e int) string {
		n := ""
		if m.kind[s][e] != "Done" {
			n = state(m.next[s][e])
		}
		switch m.kind[s][e] {
		case "Enter", "Stay":
			return fmt.Sprintf("%s[%s]{Next: %s}", m.kind[s][e], st, n)
		case "Emit":
			return fmt.Sprintf("Emit[%s, int]{Next: %s, Output: 1}", st, n)
		case "Done":
			return "Done[int]{Output: 0}"
		}
		return m.kind[s][e]
	}
	step := strings.ToLower(m.name) + "Step"
	fmt.Fprintf(&b, "func %s(s %s, e %s) Decision[%s, int] {\n\tmatch s {\n", step, st, sg, st)
	for s := 0; s < m.k; s++ {
		fmt.Fprintf(&b, "\tcase %sS%d:\n\t\tmatch e {\n", m.name, s)
		for e := 0; e < m.signals; e++ {
			fmt.Fprintf(&b, "\t\tcase %sE%d:\n\t\t\t%s\n", m.name, e, decision(s, e))
		}
		if m.payload {
			fmt.Fprintf(&b, "\t\tcase %sP:\n\t\t\t%s\n", m.name, decision(s, 0))
		}
		b.WriteString("\t\t}\n")
	}
	if m.float {
		fmt.Fprintf(&b, "\tcase %sF as f:\n\t\tmatch e {\n", m.name)
		fmt.Fprintf(&b, "\t\tcase %sE0:\n\t\t\tEnter[%s]{Next: %sF{x: -f.x}}\n", m.name, st, m.name)
		fmt.Fprintf(&b, "\t\tcase %sE1:\n\t\t\tif 1.0 / f.x < 0.0 {\n\t\t\t\tEnter[%s]{Next: %sMinus}\n\t\t\t} else {\n\t\t\t\tEnter[%s]{Next: %sS0}\n\t\t\t}\n", m.name, st, m.name, st, m.name)
		if m.signals > 2 || m.payload {
			b.WriteString("\t\tcase _:\n\t\t\tIgnore\n")
		}
		b.WriteString("\t\t}\n")
		fmt.Fprintf(&b, "\tcase %sMinus:\n\t\tIgnore\n", m.name)
	}
	b.WriteString("\t}\n}\n\n")
	fmt.Fprintf(&b, "type %s machine[%s, %s] {\n\tinitial: %sS%d,\n\tstep:    %s,\n\n", m.name, st, sg, m.name, m.initial, step)
	for _, l := range m.laws {
		names := make([]string, len(l.targets))
		for k, t := range l.targets {
			names[k] = m.name + t
		}
		switch l.kind {
		case "reaches":
			fmt.Fprintf(&b, "\tlaw %s reaches %s,\n", l.name, names[0])
		case "never":
			fmt.Fprintf(&b, "\tlaw %s never reaches %s,\n", l.name, names[0])
		default:
			fmt.Fprintf(&b, "\tlaw %s path [%s],\n", l.name, strings.Join(names, ", "))
		}
	}
	b.WriteString("}\n\n")
	return b.String()
}

// TestMachineLawsAgreeWithModel is the property of reachability laws at
// check time (L7 EV4) against a reference model of generated step tables
// (mModel): a breadth-first search of its own, with -0 and +0 apart.
// When the signals can be listed, a law is proved exactly when the model
// says it holds, and refuted otherwise; a whole search reports the
// model's states, edges (Done is no edge) and depth, and its step calls
// are the model's states times the signals plus, for each state, the run
// the explorer replays to rebuild it. When the signals cannot be listed,
// a law runs over generated runs: a verdict never contradicts the model,
// and a law whose target is the initial state is decided by the run's
// first state. A run cut short by the step budget reports the law it was
// in incomplete and the rest not run. A failing seed prints the source and
// the replay command.
//
// Mutants it kills (testdata/mutants.json): machine-done-is-edge,
// machine-initial-unchecked, machine-key-negative-zero,
// machine-fuel-stop-complete, machine-replay-unmetered.
func TestMachineLawsAgreeWithModel(t *testing.T) {
	n := 60
	if testing.Short() {
		n = 25
	}
	first := int64(1)
	if s := os.Getenv("ERGO_EVAL_MACHINE_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	reach := map[string]int{}
	for seed := first; seed < first+int64(n); seed++ {
		r := rand.New(rand.NewSource(seed))
		var src strings.Builder
		var ms []*mModel
		for i := 0; i < 4; i++ {
			m := genMachine(r, i)
			ms = append(ms, m)
			src.WriteString(m.source())
		}
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d: %s\n%s\nreplay: ERGO_EVAL_MACHINE_SEED=%d go test -run TestMachineLawsAgreeWithModel ./internal/sketch/eval", seed, fmt.Sprintf(format, args...), src.String(), seed)
		}
		pkg := checked(t, src.String())
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			fail("%v", err)
		}
		k := 0
		for _, m := range ms {
			depth := m.bfs()
			for _, l := range m.laws {
				if k >= len(rep.Results) {
					fail("%d results", len(rep.Results))
				}
				res := rep.Results[k]
				k++
				label := fmt.Sprintf("law %s.%s", m.name, l.name)
				holds := m.holds(l)
				if res.Name != label || res.Machine == nil {
					fail("result %d is %+v, want %s", k, res, label)
				}
				init := len(l.targets) == 1 && m.in(m.initial, l.targets[0])
				switch {
				case !m.payload:
					want := Refuted
					if holds {
						want = Passed
					}
					if res.Status != want || (want == Passed && res.Evidence != "proved") {
						fail("%s: %s %s (%s), the model says holds=%v", label, res.Status, res.Evidence, res.Reason, holds)
					}
					reach["listed "+string(res.Status)]++
					if res.Machine.Complete && l.kind != "path" {
						var edges int
						var steps int64
						maxDepth := 0
						for s, d := range depth {
							steps += int64(m.signals + d)
							maxDepth = max(maxDepth, d)
							for e := 0; e < m.signals; e++ {
								if _, ok := m.edge(s, e); ok {
									edges++
								}
							}
						}
						got := res.Machine
						if got.States != len(depth) || got.Edges != edges || got.Depth != maxDepth || got.Steps != steps {
							fail("%s: searched %+v; the model has %d states, %d edges, depth %d and %d step calls", label, got, len(depth), edges, maxDepth, steps)
						}
						reach["whole search"]++
					}
				case res.Status == Refuted && l.kind == "never" && holds,
					res.Status == Passed && l.kind != "never" && !holds,
					res.Status != Passed && res.Status != Refuted,
					init && l.kind == "never" && res.Status != Refuted,
					init && l.kind == "reaches" && res.Status != Passed:
					fail("%s over generated runs: %s (%s), the model says holds=%v", label, res.Status, res.Reason, holds)
				default:
					reach["runs "+string(res.Status)]++
				}
				if m.float && l.name == "MinusReach" && holds {
					reach["minus"]++
				}
			}
		}
		if k != len(rep.Results) {
			fail("%d results, %d laws", len(rep.Results), k)
		}
		// A cut budget: the law it stops in is incomplete, the rest not run.
		if rep.Steps > 1 {
			b := DefaultBudget()
			b.Steps = 1 + r.Int63n(rep.Steps-1)
			cut, err := Run(pkg, b)
			if err != nil {
				fail("%v", err)
			}
			stopped := false
			for i, res := range cut.Results {
				switch {
				case stopped && res.Status != NotRun,
					!stopped && res.Status != Incomplete && res.Status != rep.Results[i].Status:
					fail("budget %d of %d: %s is %s (%s), the whole run's %s", b.Steps, rep.Steps, res.Name, res.Status, res.Reason, rep.Results[i].Status)
				}
				stopped = stopped || res.Status == Incomplete
			}
			if !stopped {
				fail("budget %d of %d: no law is incomplete", b.Steps, rep.Steps)
			}
			reach["cut"]++
		}
	}
	if n > 1 {
		for _, want := range []string{"listed passed", "listed refuted", "whole search", "runs passed", "runs refuted", "minus", "cut"} {
			if reach[want] == 0 {
				t.Errorf("no seed reached %q: %v", want, reach)
			}
		}
	}
}
