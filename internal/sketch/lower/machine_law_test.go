package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Machine laws (MU6; types.md, Laws 13). A law in a machine runs in ergo
// test: a reachability law is decided on the machine's graph when its
// starts and signals can be listed, and a law over listed values runs over
// every value; either is then reported proved. Otherwise a law runs over
// generated cases and runs and is reported tested.
//
// These tests build and run the test binary: the explorer is the test
// build's harness Go ($Explore), which only a built binary runs; no
// in-process interpreter runs a test build.

// tableMachine is a generated machine whose step is a table over fieldless
// states and signals: next[s][e] is the state an edge goes to, and kind is
// the decision.
type tableMachine struct {
	name     string
	states   []string
	signals  []string
	initial  int
	kind     [][]string // Enter, Stay, Emit, Ignore, Reject or Done
	next     [][]int
	laws     []tableLaw
	hasDone  bool
	usedSelf bool // a path law's run takes a Reject edge
}

type tableLaw struct {
	name    string
	kind    string // reaches, never or path
	targets []int
}

// edge is the reference's edge of the table: the next state, and false
// when the decision ends the run (Done). Ignore and Reject keep the state.
func (m *tableMachine) edge(s, e int) (int, bool) {
	switch m.kind[s][e] {
	case "Done":
		return 0, false
	case "Ignore", "Reject":
		return s, true
	}
	return m.next[s][e], true
}

// holds is the reference: a breadth-first search over the table.
func (m *tableMachine) holds(l tableLaw) bool {
	if l.kind == "path" {
		layer := map[int]bool{}
		if m.initial == l.targets[0] {
			layer[m.initial] = true
		}
		for _, t := range l.targets[1:] {
			next := map[int]bool{}
			for s := range layer {
				for e := range m.signals {
					if n, ok := m.edge(s, e); ok && n == t {
						next[n] = true
					}
				}
			}
			layer = next
		}
		return len(layer) > 0
	}
	seen := map[int]bool{m.initial: true}
	queue := []int{m.initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for e := range m.signals {
			if n, ok := m.edge(s, e); ok && !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	if l.kind == "reaches" {
		return seen[l.targets[0]]
	}
	return !seen[l.targets[0]]
}

func genTableMachine(r *rand.Rand, i int) *tableMachine {
	m := &tableMachine{name: fmt.Sprintf("M%d", i)}
	for k := 0; k < 2+r.Intn(4); k++ {
		m.states = append(m.states, fmt.Sprintf("M%dS%d", i, k))
	}
	for k := 0; k < 1+r.Intn(3); k++ {
		m.signals = append(m.signals, fmt.Sprintf("M%dE%d", i, k))
	}
	m.initial = r.Intn(len(m.states))
	kinds := []string{"Enter", "Enter", "Stay", "Emit", "Ignore", "Reject", "Reject", "Done"}
	for s := range m.states {
		m.kind = append(m.kind, make([]string, len(m.signals)))
		m.next = append(m.next, make([]int, len(m.signals)))
		for e := range m.signals {
			m.kind[s][e] = kinds[r.Intn(len(kinds))]
			m.next[s][e] = r.Intn(len(m.states))
			m.hasDone = m.hasDone || m.kind[s][e] == "Done"
		}
	}
	pick := func() int { return r.Intn(len(m.states)) }
	m.laws = append(m.laws, tableLaw{name: "Reaches", kind: "reaches", targets: []int{pick()}})
	m.laws = append(m.laws, tableLaw{name: "Never", kind: "never", targets: []int{pick()}})
	// A path of a random walk, which holds, and a random path.
	walk := []int{m.initial}
	for s := m.initial; len(walk) < 2+r.Intn(3); {
		e := r.Intn(len(m.signals))
		n, ok := m.edge(s, e)
		if !ok {
			break
		}
		m.usedSelf = m.usedSelf || m.kind[s][e] == "Reject"
		walk, s = append(walk, n), n
	}
	m.laws = append(m.laws, tableLaw{name: "Walk", kind: "path", targets: walk})
	var random []int
	for k := 0; k < 1+r.Intn(4); k++ {
		random = append(random, pick())
	}
	m.laws = append(m.laws, tableLaw{name: "Path", kind: "path", targets: random})
	return m
}

func (m *tableMachine) source() string {
	var b strings.Builder
	st, sg := m.name+"State", m.name+"Signal"
	fmt.Fprintf(&b, "type %s = %s{}\n\n", st, strings.Join(m.states, "{} | "))
	fmt.Fprintf(&b, "type %s = %s{}\n\n", sg, strings.Join(m.signals, "{} | "))
	step := strings.ToLower(m.name) + "Step"
	fmt.Fprintf(&b, "func %s(s %s, e %s) Decision[%s, int] {\n\tmatch (s, e) {\n", step, st, sg, st)
	for s := range m.states {
		for e := range m.signals {
			fmt.Fprintf(&b, "\tcase (%s, %s):\n\t\t", m.states[s], m.signals[e])
			n := m.states[m.next[s][e]]
			switch m.kind[s][e] {
			case "Enter", "Stay":
				fmt.Fprintf(&b, "%s[%s]{Next: %s}\n", m.kind[s][e], st, n)
			case "Emit":
				fmt.Fprintf(&b, "Emit[%s, int]{Next: %s, Output: 1}\n", st, n)
			case "Done":
				b.WriteString("Done[int]{Output: 0}\n")
			default:
				b.WriteString(m.kind[s][e] + "\n")
			}
		}
	}
	b.WriteString("\t}\n}\n\n")
	fmt.Fprintf(&b, "type %s machine[%s, %s] {\n\tinitial: %s,\n\tstep:    %s,\n\n", m.name, st, sg, m.states[m.initial], step)
	for _, l := range m.laws {
		names := make([]string, len(l.targets))
		for k, t := range l.targets {
			names[k] = m.states[t]
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
	// A law over one transition, over every state and signal.
	fmt.Fprintf(&b, "\tlaw NoDone(s %s, e %s) {\n\t\tmatch %s(s, e) {\n\t\tcase Done[int]:\n\t\t\tfalse\n", st, sg, step)
	for _, k := range []string{"Enter[" + st + "]", "Stay[" + st + "]", "Emit[" + st + ", int]", "Ignore", "Reject"} {
		fmt.Fprintf(&b, "\t\tcase %s:\n\t\t\ttrue\n", k)
	}
	b.WriteString("\t\t}\n\t},\n}\n\n")
	return b.String()
}

// TestMachineLawTables is the property of MU6: for generated step tables,
// a reachability law is proved exactly when a breadth-first search over
// the table says it holds, and refuted otherwise; a law over every state
// and signal is proved exactly when no entry of the table is Done.
// ERGO_MACHINELAW_SEED replays another seed; a failure prints the source.
func TestMachineLawTables(t *testing.T) {
	seed := int64(57)
	if s := os.Getenv("ERGO_MACHINELAW_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_MACHINELAW_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	var src strings.Builder
	var want []string
	failed, total, self, beyond := 0, 0, 0, 0
	for i := 0; i < 24; i++ {
		m := genTableMachine(r, i)
		src.WriteString(m.source())
		if m.usedSelf {
			self++
		}
		for _, l := range m.laws {
			total++
			label := "law " + m.name + "." + l.name
			if l.kind != "never" && l.targets[len(l.targets)-1] != m.initial && m.holds(l) {
				beyond++
			}
			if m.holds(l) {
				want = append(want, "ok "+label+" (proved")
				continue
			}
			failed++
			want = append(want, "FAIL "+label+" (every run): law refuted at ")
		}
		total++
		cases := strconv.Itoa(len(m.states) * len(m.signals))
		if m.hasDone {
			failed++
			want = append(want, "FAIL law "+m.name+".NoDone (case ")
		} else {
			want = append(want, "ok law "+m.name+".NoDone (proved: all "+cases+" cases)")
		}
	}
	// The generator reaches a path through a Reject edge and a law that
	// holds only past the first state.
	if self == 0 || beyond == 0 {
		t.Fatalf("seed %d: the generated tables have %d paths through Reject and %d laws past the start", seed, self, beyond)
	}
	src.WriteString("func main() {}\n")
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "machinelaw_tables", src.String())}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["machinelaw_tables"])
	got := lines(stdout)
	want = append(want, fmt.Sprintf("FAIL (%d of %d tests)", failed, total))
	ok := code == 1 && len(got) == len(want)
	for k := 0; ok && k < len(got); k++ {
		ok = strings.HasPrefix(got[k], want[k])
		if !ok {
			t.Errorf("line %d is %q, want a line that begins %q", k+1, got[k], want[k])
		}
	}
	if !ok {
		t.Errorf("seed %d: exit %d\nstdout:\n%s\nwant:\n%s\nstderr:\n%s\nsource:\n%s", seed, code, stdout, strings.Join(want, "\n"), firstLines(stderr, 8), src.String())
	}
}

// machineLawRuns is a machine whose states carry a payload, so its graph
// cannot be listed: its reachability laws run over generated runs, and its
// law over a payload runs over generated cases.
const machineLawRuns = `type Pos = Low{N int} | High{}

type Push = Up{By int} | Down{}

func step(p Pos, s Push) Decision[Pos, int] {
	match p {
	case Low as l:
		match s {
		case Up as u:
			if l.N+u.By > 3 && u.By < 9 {
				Enter[Pos]{Next: High}
			} else {
				Stay[Pos]{Next: Low{N: l.N + 1}}
			}
		case Down:
			Reject
		}
	case High:
		Ignore
	}
}

type Meter machine[Pos, Push] {
	initial: Low{N: 0},
	step:    step,

	law NeverHigh never reaches High,
	law ReachesHigh reaches High,
	law UpKeepsLow(n int) {
		match step(Low{N: n}, Down) {
		case Reject:
			true
		case Enter[Pos]:
			false
		case Stay[Pos]:
			false
		case Emit[Pos, int]:
			false
		case Ignore:
			false
		case Done[int]:
			false
		}
	},
}

func main() {}
`

// TestMachineLawRuns: a false reachability law over a machine whose graph
// cannot be listed fails with the seed and the run that refutes it; a
// witness proves a reaches law; a law that reads a payload is tested,
// never proved.
func TestMachineLawRuns(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "machinelaw_runs", machineLawRuns)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["machinelaw_runs"], "--seed", "7")
	got := lines(stdout)
	if code != 1 || len(got) != 4 {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", code, stdout, firstLines(stderr, 8))
	}
	if !strings.HasPrefix(got[0], "FAIL law Meter.NeverHigh (run ") || !strings.Contains(got[0], ", seed 7, size ") ||
		!strings.Contains(got[0], "law refuted at main.ergo:27:2: the run from Low{N: 0} with []Push{") || !strings.HasSuffix(got[0], "} reaches High") {
		t.Errorf("line 1 is %q", got[0])
	}
	if !strings.HasPrefix(got[1], "ok law Meter.ReachesHigh (proved by the run from Low{N: 0} with []Push{") {
		t.Errorf("line 2 is %q", got[1])
	}
	if got[2] != "ok law Meter.UpKeepsLow (100 cases)" {
		t.Errorf("line 3 is %q", got[2])
	}
	if got[3] != "FAIL (1 of 3 tests)" {
		t.Errorf("the summary is %q", got[3])
	}
}
