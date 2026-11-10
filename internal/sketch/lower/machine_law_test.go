package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Machine laws (MU6; types.md, Laws 13) and listed laws (MU6b; Laws 10).
// A reachability law is decided on the machine's graph when its starts and
// signals can be listed; any law with a body, in a machine, at top level
// or in an operation type, runs over every value when its run can be
// listed; either is then reported proved. Otherwise a law runs over
// generated cases and runs and is reported tested.
//
// These tests build and run the test binary: the explorer is the test
// build's harness Go ($Explore), which only a built binary runs; no
// in-process interpreter runs a test build.

// verdicts are the lines of a test binary's stdout less the indented ones:
// a refuted never law's counterexample test and replay command follow its
// FAIL line, indented by a tab.
func verdicts(stdout string) []string {
	var out []string
	for _, line := range lines(stdout) {
		if !strings.HasPrefix(line, "\t") {
			out = append(out, line)
		}
	}
	return out
}

// blocks are the indented lines that follow each verdict line of stdout,
// joined, in the order of verdicts(stdout): a counterexample's or a
// witness's test, "" for a verdict with none.
func blocks(stdout string) []string {
	var out []string
	for _, line := range lines(stdout) {
		switch {
		case !strings.HasPrefix(line, "\t"):
			out = append(out, "")
		case len(out) > 0:
			out[len(out)-1] += line + "\n"
		}
	}
	return out
}

// blockRuns reports whether the test in block runs the run that line
// names, "from S with []E{…}": its machines.From call takes the same start
// and signals; "" when it does, else what is wrong.
func blockRuns(line, block string) string {
	i := strings.Index(line, "run from ")
	j := strings.Index(line, " with []")
	if i < 0 || j < i {
		return "the line names no run"
	}
	end := strings.Index(line[j:], "}")
	if end < 0 {
		return "the line names no run"
	}
	call := ", " + line[i+len("run from "):j] + ", " + line[j+len(" with "):j+end+1] + ") {"
	if !strings.Contains(block, call) {
		return "its test does not run " + call
	}
	return ""
}

// runLength is the count of signals of the run a verdict line names, "from
// S with []E{e1, e2}", whose signals are fieldless; -1 when it names none.
func runLength(line string) int {
	i := strings.Index(line, " with []")
	if i < 0 {
		return -1
	}
	rest := line[i:]
	open, end := strings.Index(rest, "{"), strings.Index(rest, "}")
	if open < 0 || end < open {
		return -1
	}
	if list := rest[open+1 : end]; list != "" {
		return strings.Count(list, ", ") + 1
	}
	return 0
}

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
	kind    string // reaches, never, path, always or sometimes
	targets []int
	pred    []bool // an invariant law's predicate, by state (X2)
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
	dist := m.dist()
	switch l.kind {
	case "reaches":
		_, ok := dist[l.targets[0]]
		return ok
	case "always", "sometimes":
		_, found := m.shortest(l)
		return found == (l.kind == "sometimes")
	}
	_, ok := dist[l.targets[0]]
	return !ok
}

// dist is the reference's breadth-first search over the table: the length
// of a shortest run to each reachable state.
func (m *tableMachine) dist() map[int]int {
	dist := map[int]int{m.initial: 0}
	queue := []int{m.initial}
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for e := range m.signals {
			if n, ok := m.edge(s, e); ok {
				if _, seen := dist[n]; !seen {
					dist[n] = dist[s] + 1
					queue = append(queue, n)
				}
			}
		}
	}
	return dist
}

// shortest is the length of a shortest run to a state an invariant law
// looks for, and whether one is reachable: a state where the predicate is
// false for always, true for sometimes. The start counts, at length 0.
func (m *tableMachine) shortest(l tableLaw) (int, bool) {
	best, found := 0, false
	for s, d := range m.dist() {
		if l.pred[s] == (l.kind == "sometimes") && (!found || d < best) {
			best, found = d, true
		}
	}
	return best, found
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
	// Invariant laws (X2) over generated predicates: mostly true for
	// always, mostly false for sometimes, so each holds and fails.
	for _, k := range []string{"always", "sometimes"} {
		l := tableLaw{name: strings.ToUpper(k[:1]) + k[1:], kind: k}
		for range m.states {
			l.pred = append(l.pred, (r.Intn(3) == 0) == (k == "sometimes"))
		}
		m.laws = append(m.laws, l)
	}
	return m
}

// predName is the name of the predicate function of the invariant law l.
func (m *tableMachine) predName(l tableLaw) string {
	return strings.ToLower(m.name) + l.name
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
	for _, l := range m.laws {
		if l.pred == nil {
			continue
		}
		fmt.Fprintf(&b, "func %s(s %s) bool {\n\tmatch s {\n", m.predName(l), st)
		for s, v := range l.pred {
			fmt.Fprintf(&b, "\tcase %s:\n\t\t%t\n", m.states[s], v)
		}
		b.WriteString("\t}\n}\n\n")
	}
	fmt.Fprintf(&b, "type %s machine[%s, %s] {\n\tinitial: %s,\n\tstep:    %s,\n\n", m.name, st, sg, m.states[m.initial], step)
	for _, l := range m.laws {
		names := make([]string, len(l.targets))
		for k, t := range l.targets {
			names[k] = m.states[t]
		}
		switch l.kind {
		case "always", "sometimes":
			fmt.Fprintf(&b, "\tlaw %s %s %s,\n", l.name, l.kind, m.predName(l))
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

// listedType is a generated parameter type of a law: its Ergo spelling,
// and its count of values by the reference, -1 when it cannot be listed
// (a type with an unbounded part, or more than refCap values).
type listedType struct {
	name  string
	count int
}

// The reference's bounds (types.md, Laws 10): 256 values a type, 4096
// cases a run.
const refCap, refCases = 256, 4096

// listedLaws is the generated part of the program that holds laws outside
// machines (MU6b): its declarations and its report lines in source order.
type listedLaws struct {
	src            strings.Builder
	tests, want    []string // the property tests' lines, then the laws'
	failed, total  int
	proved, tested int
}

// union declares a union of n values: its variants are records of k bool
// fields, 2^k values each, a fieldless variant for k = 0; the parts are
// the bits of n, some split in two at random, so a count is reached by
// several shapes.
func (g *listedLaws) union(r *rand.Rand, name string, n int) listedType {
	var parts []int
	for p := 1; p <= n; p <<= 1 {
		if n&p != 0 {
			parts = append(parts, p)
		}
	}
	for k := 0; k < 3; k++ {
		if i := r.Intn(len(parts)); parts[i] > 1 {
			parts = append(parts, parts[i]/2)
			parts[i] /= 2
		}
	}
	var vs []string
	for j, p := range parts {
		var fs []string
		for f := 0; 1<<f < p; f++ {
			fs = append(fs, fmt.Sprintf("f%d bool", f))
		}
		vs = append(vs, fmt.Sprintf("%sV%d{%s}", name, j, strings.Join(fs, ", ")))
	}
	fmt.Fprintf(&g.src, "type %s = %s\n\n", name, strings.Join(vs, " | "))
	c := n
	if n > refCap {
		c = -1
	}
	return listedType{name, c}
}

// withBool is ts with a last bool when x.
func withBool(ts []listedType, x bool) []listedType {
	if !x {
		return ts
	}
	return append(ts[:len(ts):len(ts)], listedType{"bool", 2})
}

func refProduct(ts []listedType) int {
	n := 1
	for _, t := range ts {
		if t.count < 0 || n*t.count > refCases {
			return -1
		}
		n *= t.count
	}
	return n
}

// genListedLaws writes top-level laws and the member laws of an operation
// type with three providers, over parameters drawn from listed types (the
// caps' edges among them) and from types that cannot be listed. A law
// that can be falsified ends in a bool parameter x and its body is x; it
// is falsified only when its run can be listed, so a generated run never
// meets a false case.
func genListedLaws(r *rand.Rand) *listedLaws {
	g := &listedLaws{}
	pool := []listedType{{"bool", 2}, {"LFlag", 2}, {"(bool, LFlag)", 4}, {"int", -1}, {"string", -1}, {"LInf", -1}, {"LRec", -1}, {"(bool, int)", -1}}
	g.src.WriteString("type LFlag bool\n\ntype LInf = LInfA{} | LInfB{n int}\n\ntype LRec { s string, b bool }\n\n")
	edge := map[string]listedType{}
	for _, n := range []int{16, 17, 241, 255, 256, 257} {
		t := g.union(r, fmt.Sprintf("LN%d", n), n)
		edge[t.name] = t
		pool = append(pool, t)
	}
	// LR8 is a record at the type cap, LR9 one past it.
	for _, n := range []int{8, 9} {
		var fs []string
		for f := 0; f < n; f++ {
			fs = append(fs, fmt.Sprintf("f%d bool", f))
		}
		fmt.Fprintf(&g.src, "type LR%d { %s }\n\n", n, strings.Join(fs, ", "))
		c := 1 << n
		if c > refCap {
			c = -1
		}
		t := listedType{fmt.Sprintf("LR%d", n), c}
		edge[t.name] = t
		pool = append(pool, t)
	}
	for j := 0; j < 6; j++ {
		pool = append(pool, g.union(r, fmt.Sprintf("LU%d", j), 1+r.Intn(20)))
	}
	pick := func() []listedType {
		ts := make([]listedType, r.Intn(4))
		for k := range ts {
			ts[k] = pool[r.Intn(len(pool))]
		}
		return ts
	}
	// The runs at the caps' edges, each with no x unless falsified: a type
	// of 255, 256 and 257 values, a record of 256 and 512, and runs of
	// 4096 cases and of 4097 = 17 × 241.
	runs := [][]listedType{
		{edge["LN255"]}, {edge["LN256"]}, {edge["LN257"]}, {edge["LR8"]}, {edge["LR9"]},
		{edge["LN256"], edge["LN16"]}, {edge["LN17"], edge["LN241"]},
	}
	edges := len(runs)
	for k := 0; k < 12; k++ {
		runs = append(runs, pick())
	}
	// law writes a law over ts, with a last parameter x when withX.
	law := func(name string, ts []listedType, withX bool, body string) string {
		var ps []string
		for k, t := range ts {
			ps = append(ps, fmt.Sprintf("p%d %s", k, t.name))
		}
		if withX {
			ps = append(ps, "x bool")
		}
		return fmt.Sprintf("law %s(%s) { %s }", name, strings.Join(ps, ", "), body)
	}
	// report is the reference's line for a run of n cases (-1: not
	// listed), false in its first case when falsified.
	report := func(label string, n int, falsified bool) {
		g.total++
		switch {
		case n < 0:
			g.tested++
			g.want = append(g.want, "ok "+label+" (100 cases)")
		case falsified:
			g.failed++
			g.want = append(g.want, fmt.Sprintf("FAIL %s (case 1 of %d, every value", label, n))
		case n == 1:
			g.proved++
			g.want = append(g.want, "ok "+label+" (proved: 1 case)")
		default:
			g.proved++
			g.want = append(g.want, fmt.Sprintf("ok %s (proved: all %d cases)", label, n))
		}
	}
	for k, ts := range runs {
		name := fmt.Sprintf("LT%d", k)
		n := refProduct(ts)
		falsify := n >= 0 && n*2 <= refCases && r.Intn(2) == 0
		withX := falsify || (k >= edges && r.Intn(2) == 0)
		body := "true"
		if falsify {
			body = "x"
		}
		g.src.WriteString(law(name, ts, withX, body) + "\n\n")
		report("law "+name, refProduct(withBool(ts, withX)), falsify)
	}
	// A property test over the same runs follows the same rule (mu6c);
	// its body asserts !x when falsified. The tests report before the
	// laws, so their lines go first. A test always has a parameter: one
	// with none is an example.
	laws := g.want
	g.want = nil
	for k, ts := range runs {
		name := fmt.Sprintf("PT%d", k)
		n := refProduct(ts)
		falsify := n >= 0 && n*2 <= refCases && r.Intn(2) == 0
		withX := falsify || len(ts) == 0 || (k >= edges && r.Intn(2) == 0)
		var ps []string
		for j, t := range ts {
			ps = append(ps, fmt.Sprintf("p%d %s", j, t.name))
		}
		if withX {
			ps = append(ps, "x bool")
		}
		body := "true"
		if falsify {
			body = "!x" // false first in case 2, x = true: a run must go past case 1
		}
		fmt.Fprintf(&g.src, "test %s(%s) {\n\tassert %s\n}\n\n", name, strings.Join(ps, ", "), body)
		report(name, refProduct(withBool(ts, withX)), falsify)
		if falsify {
			g.want[len(g.want)-1] = strings.Replace(g.want[len(g.want)-1], "(case 1 of", "(case 2 of", 1)
		}
	}
	g.tests = g.want
	g.want = laws
	// LOne has one value, LTwo two, LWide cannot be listed; LGet is true
	// but for LTwo{b: false}.
	var members []string
	type member struct {
		ts      []listedType
		falsify bool
	}
	var ms []member
	for k := 0; k < 6; k++ {
		ts := pick()
		n := refProduct(ts)
		falsify := n >= 0 && n*4 <= refCases && r.Intn(2) == 0
		withX := falsify || r.Intn(2) == 0
		body := "LGet() || true"
		if falsify {
			body = "LGet() || x"
		}
		members = append(members, law(fmt.Sprintf("LM%d", k), ts, withX, body))
		ms = append(ms, member{withBool(ts, withX), falsify})
	}
	fmt.Fprintf(&g.src, "type LOp {\n\tLGet() bool,\n\n\t%s,\n}\n\n", strings.Join(members, ",\n\t"))
	g.src.WriteString("type LOne {}\n\nfunc LGet(_ LOne) bool { true }\n\ntype LTwo { b bool }\n\nfunc LGet(p LTwo) bool { p.b }\n\ntype LWide { n int }\n\nfunc LGet(_ LWide) bool { true }\n\n")
	for k, m := range ms {
		label := fmt.Sprintf("law LOp.LM%d for ", k)
		report(label+"LOne", refProduct(append([]listedType{{"LOne", 1}}, m.ts...)), false)
		report(label+"LTwo", refProduct(append([]listedType{{"LTwo", 2}}, m.ts...)), m.falsify)
		report(label+"LWide", -1, false)
	}
	return g
}

// TestMachineLawTables is the property of MU6 and MU6b: for generated step
// tables, a reachability law is proved exactly when a breadth-first search
// over the table says it holds, and refuted otherwise; a law over every
// state and signal is proved exactly when no entry of the table is Done.
// For generated laws outside machines, at top level and in an operation
// type with providers, one rule holds (types.md, Laws 10): a run is
// proved over all its cases exactly when the reference lists every value
// of it within the caps, it fails at its first listed case when its body
// is false there, and any other run is 100 generated cases. A property
// test over the same values follows the same rule (mu6c).
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
// ERGO_MACHINELAW_SEED replays another seed; a failure prints the source.
func TestMachineLawTables(t *testing.T) {
	needsBuild(t)
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
	// runs holds, by the index of a want line, the length of a shortest
	// run the line names: an always law's counterexample, a sometimes
	// law's witness (X2). shapes counts the runs at the start, the runs
	// past it, and sometimes laws with no witness.
	type namedRun struct {
		n      int    // its length
		assert string // its test's check
	}
	runs := map[int]namedRun{}
	var shapes [3]int
	for i := 0; i < 24; i++ {
		m := genTableMachine(r, i)
		src.WriteString(m.source())
		if m.usedSelf {
			self++
		}
		for _, l := range m.laws {
			total++
			label := "law " + m.name + "." + l.name
			if l.pred != nil {
				if n, found := m.shortest(l); found {
					runs[len(want)] = namedRun{n, "\t\t\tassert " + m.predName(l) + "(r.State)\n"}
					shapes[min(n, 1)]++
				} else if l.kind == "sometimes" {
					shapes[2]++
				}
			} else if l.kind != "never" && l.targets[len(l.targets)-1] != m.initial && m.holds(l) {
				beyond++
			}
			if m.holds(l) {
				want = append(want, "ok "+label+" (proved")
				continue
			}
			failed++
			want = append(want, "FAIL "+label+" (")
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
	// An invariant law's run is the start alone, and past it, and a
	// sometimes law finds no witness.
	if shapes[0] == 0 || shapes[1] == 0 || shapes[2] == 0 {
		t.Fatalf("seed %d: the invariant laws have %d runs at the start, %d past it and %d with no witness; want each", seed, shapes[0], shapes[1], shapes[2])
	}
	g := genListedLaws(r)
	src.WriteString(g.src.String())
	shifted := map[int]namedRun{}
	for k, n := range runs {
		shifted[k+len(g.tests)] = n
	}
	runs = shifted
	want = append(append(g.tests, want...), g.want...)
	failed += g.failed
	total += g.total
	if g.proved == 0 || g.tested == 0 || g.failed == 0 {
		t.Fatalf("seed %d: the generated laws are %d proved, %d tested and %d refuted runs; want each", seed, g.proved, g.tested, g.failed)
	}
	for _, kind := range []string{"(proved: all ", "(100 cases)", "FAIL "} {
		if !strings.Contains(strings.Join(g.tests, "\n"), kind) {
			t.Fatalf("seed %d: no generated property test reports %q", seed, kind)
		}
	}
	src.WriteString("func main() {}\n")
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "machinelaw_tables", src.String())}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["machinelaw_tables"])
	got, tests := verdicts(stdout), blocks(stdout)
	want = append(want, fmt.Sprintf("FAIL (%d of %d tests)", failed, total))
	ok := code == 1 && len(got) == len(want)
	for k := 0; ok && k < len(got); k++ {
		ok = strings.HasPrefix(got[k], want[k])
		if !ok {
			t.Errorf("line %d is %q, want a line that begins %q", k+1, got[k], want[k])
		}
		// An invariant law's counterexample or witness is a shortest run,
		// and its test runs that run and checks the predicate.
		if r, named := runs[k]; ok && named {
			if c := runLength(got[k]); c != r.n {
				ok = false
				t.Errorf("line %d is %q, whose run has %d signals; a shortest run has %d", k+1, got[k], c, r.n)
			} else if why := blockRuns(got[k], tests[k]); why != "" || !strings.Contains(tests[k], r.assert) {
				ok = false
				t.Errorf("line %d is %q: %s; its test, which must check %q:\n%s", k+1, got[k], why, r.assert, tests[k])
			}
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

type Lane = At{N int} | Far{} | Gone{}

type Nudge = Go{} | Wait1{} | Wait2{} | Wait3{} | Wait4{} | Wait5{} | Wait6{} | Wait7{}

func walk(p Lane, s Nudge) Decision[Lane, int] {
	match p {
	case At as a:
		match s {
		case Go:
			if a.N >= 15 {
				Enter[Lane]{Next: Far}
			} else {
				Enter[Lane]{Next: At{N: a.N + 1}}
			}
		case _:
			Ignore
		}
	case _:
		Ignore
	}
}

type Road machine[Lane, Nudge] {
	initial: At{N: 0},
	step:    walk,

	law NeverFar never reaches Far,
	law NeverGone never reaches Gone,
	law NotGone always notGone,
	law SomeFar sometimes isFar,
	law SomeGone sometimes isGone,
}

func notGone(l Lane) bool { !isGone(l) }

func isGone(l Lane) bool {
	match l {
	case Gone:
		true
	case _:
		false
	}
}

func isFar(l Lane) bool {
	match l {
	case Far:
		true
	case _:
		false
	}
}

func main() {}
`

// TestMachineLawRuns: a false reachability law over a machine whose graph
// cannot be listed fails with the seed and the run that refutes it; a
// witness proves a reaches law; a law that reads a payload is tested,
// never proved. Past the budget of states (--states) a law is not proved,
// and its generated runs pick only signals that change the state (X1). It builds Go because the laws run in the built test
// binary (ergo test), the only place a law runs.
func TestMachineLawRuns(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "machinelaw_runs", machineLawRuns)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["machinelaw_runs"], "--seed", "7", "--states", "5")
	got := verdicts(stdout)
	if code != 1 || len(got) != 9 {
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
	// Road's graph passes the budget of 5 states. A generated run picks
	// only signals that change the state, so it walks the 16 Go signals to
	// Far past seven Waits that Ignore; a law the budget stopped is not
	// proved.
	if !strings.HasPrefix(got[3], "FAIL law Road.NeverFar (run ") || !strings.HasSuffix(got[3], "} reaches Far") {
		t.Errorf("line 4 is %q", got[3])
	}
	if got[4] != "ok law Road.NeverGone (no counterexample in runs of at most 4 signals; 5 states; 100 runs; not proved)" {
		t.Errorf("line 5 is %q", got[4])
	}
	// The invariant laws (X2) past the budget: always is not proved; a
	// sometimes law holds by a generated witness, which prints as a test,
	// and with none it is not found, which fails.
	if got[5] != "ok law Road.NotGone (no counterexample in runs of at most 4 signals; 5 states; 100 runs; not proved)" {
		t.Errorf("line 6 is %q", got[5])
	}
	tests := blocks(stdout)
	if !strings.HasPrefix(got[6], "ok law Road.SomeFar (proved by the run from At{N: 0} with []Nudge{") || blockRuns(got[6], tests[6]) != "" || !strings.Contains(tests[6], "\t\t\tassert isFar(r.State)\n") {
		t.Errorf("line 7 is %q, its test:\n%s", got[6], tests[6])
	}
	if !strings.HasPrefix(got[7], "FAIL law Road.SomeGone (100 runs, seed 7; no run of at most 4 signals reaches a state where isGone holds; 5 states): law refuted at main.ergo:") ||
		!strings.HasSuffix(got[7], ": not found: no generated run reaches a state where isGone holds") {
		t.Errorf("line 8 is %q", got[7])
	}
	if got[8] != "FAIL (3 of 8 tests)" {
		t.Errorf("the summary is %q", got[8])
	}
}
