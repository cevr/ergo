package lower

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Stage 4 (spec D5): machine plan values, against a reference model.

// planDecision is one cell of a generated step table.
type planDecision struct {
	kind   string // Enter, Stay, Ignore, Reject, Done or Fail
	next   int    // Enter and Stay: the next state's index
	output int    // Done: the output
}

// planMachine is one generated machine: its state and signal variants, its
// step table, its initial state, its rows and how its initial value is
// written.
type planMachine struct {
	k, states, signals int
	table              [][]planDecision
	initial            int
	fails, needs       bool
	initVia            string // "value", "let" or "func"
	seqs               [][]int
}

func (m planMachine) pure() bool { return !m.fails && !m.needs }

// fold is the reference model: start at the initial state; Enter and Stay
// move to their next state, Ignore and Reject keep it, Done ends the fold
// with its output, and a failure ends it.
func (m planMachine) fold(seq []int) string {
	s := m.initial
	for i, e := range seq {
		d := m.table[s][e]
		switch d.kind {
		case "Enter", "Stay":
			s = d.next
		case "Done":
			return fmt.Sprintf("done %d at %d", d.output, i)
		case "Fail":
			return fmt.Sprintf("fail at %d", i)
		}
	}
	return fmt.Sprintf("main.M%dS%d", m.k, s)
}

func genPlanMachine(rng *rand.Rand, k int) planMachine {
	m := planMachine{k: k, states: 1 + rng.Intn(4), signals: 1 + rng.Intn(3)}
	switch rng.Intn(4) {
	case 1:
		m.fails = true
	case 2:
		m.needs = true
	case 3:
		m.fails, m.needs = true, true
	}
	m.initial = rng.Intn(m.states)
	m.initVia = []string{"value", "let", "func"}[rng.Intn(3)]
	kinds := []string{"Enter", "Stay", "Ignore", "Reject", "Done"}
	if m.fails {
		kinds = append(kinds, "Fail")
	}
	m.table = make([][]planDecision, m.states)
	for s := range m.table {
		m.table[s] = make([]planDecision, m.signals)
		for e := range m.table[s] {
			m.table[s][e] = planDecision{kind: kinds[rng.Intn(len(kinds))], next: rng.Intn(m.states), output: rng.Intn(100) - 50}
		}
	}
	for n := 0; n <= 8; n++ {
		for r := 0; r < 3; r++ {
			seq := make([]int, n)
			for i := range seq {
				seq[i] = rng.Intn(m.signals)
			}
			m.seqs = append(m.seqs, seq)
		}
	}
	return m
}

// source is the machine's Ergo declarations.
func (m planMachine) source() string {
	var b strings.Builder
	k := m.k
	variants := func(prefix string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf("M%d%s%d{}", k, prefix, i)
		}
		return strings.Join(parts, " | ")
	}
	fmt.Fprintf(&b, "type M%dS = %s\n\ntype M%dE = %s\n\n", k, variants("S", m.states), k, variants("E", m.signals))
	head := fmt.Sprintf("func step%d(s M%dS, e M%dE) Decision[M%dS, int]", k, k, k, k)
	if m.fails {
		head += " fails Bad"
	}
	if m.needs {
		head += " needs Scheduler"
	}
	b.WriteString(head + " {\n")
	if m.needs {
		b.WriteString("\tScheduler.Sleep(0s)\n")
	}
	b.WriteString("\tmatch (s, e) {\n")
	for s, row := range m.table {
		for e, d := range row {
			fmt.Fprintf(&b, "\tcase (M%dS%d, M%dE%d):\n", k, s, k, e)
			switch d.kind {
			case "Enter", "Stay":
				fmt.Fprintf(&b, "\t\t%s[M%dS]{Next: M%dS%d}\n", d.kind, k, k, d.next)
			case "Ignore", "Reject":
				b.WriteString("\t\t" + d.kind + "\n")
			case "Done":
				fmt.Fprintf(&b, "\t\tDone[int]{Output: %d}\n", d.output)
			case "Fail":
				b.WriteString("\t\ttry bad()\n\t\tIgnore\n")
			}
		}
	}
	b.WriteString("\t}\n}\n\n")
	initial := fmt.Sprintf("M%dS%d", k, m.initial)
	switch m.initVia {
	case "let":
		fmt.Fprintf(&b, "let start%d M%dS = %s\n\n", k, k, initial)
		initial = fmt.Sprintf("start%d", k)
	case "func":
		fmt.Fprintf(&b, "let start%d M%dS = %s\n\nfunc first%d() M%dS { start%d }\n\n", k, k, initial, k, k, k)
		initial = fmt.Sprintf("first%d()", k)
	}
	fmt.Fprintf(&b, "type M%d machine[M%dS, M%dE] {\n\tinitial: %s,\n\tstep:    step%d,\n}\n\n", k, k, k, initial, k)
	return b.String()
}

// driver is the Go that folds the machine's plan over each sequence and
// compares the result with the model's, checks the plan's Go signature,
// and, for a pure step, that the plan's call allocates no more than the
// direct call.
func (m planMachine) driver() string {
	var b strings.Builder
	k := m.k
	fmt.Fprintf(&b, "func fold%d(env *Env, seq []M%dE) string {\n\tvar plan M%d\n\ts := plan.Initial()\n\tfor i, e := range seq {\n", k, k, k)
	switch {
	case m.pure():
		b.WriteString("\t\td := plan.Step(s, e)\n")
	case m.fails:
		b.WriteString("\t\td, err := plan.Step(env, s, e)\n\t\tif err != nil {\n\t\t\treturn fmt.Sprintf(\"fail at %d\", i)\n\t\t}\n")
	default:
		b.WriteString("\t\td := plan.Step(env, s, e)\n")
	}
	fmt.Fprintf(&b, "\t\tswitch d := d.(type) {\n\t\tcase Enter[M%dS]:\n\t\t\ts = d.Next\n\t\tcase Stay[M%dS]:\n\t\t\ts = d.Next\n\t\tcase Done[int]:\n\t\t\treturn fmt.Sprintf(\"done %%d at %%d\", d.Output, i)\n\t\t}\n\t}\n\treturn fmt.Sprintf(\"%%T\", s)\n}\n\n", k, k)
	fmt.Fprintf(&b, "func check%d(t *testing.T, env *Env) {\n", k)
	signals := make([]string, m.signals)
	for i := range signals {
		signals[i] = fmt.Sprintf("M%dE%d{}", k, i)
	}
	fmt.Fprintf(&b, "\tsignals := []M%dE{%s}\n", k, strings.Join(signals, ", "))
	for _, seq := range m.seqs {
		idx := make([]string, len(seq))
		for i, e := range seq {
			idx[i] = fmt.Sprint(e)
		}
		fmt.Fprintf(&b, "\t{\n\t\tvar seq []M%dE\n\t\tfor _, i := range []int{%s} {\n\t\t\tseq = append(seq, signals[i])\n\t\t}\n", k, strings.Join(idx, ", "))
		fmt.Fprintf(&b, "\t\tif got, want := fold%d(env, seq), %q; got != want {\n\t\t\tt.Errorf(\"M%d over %v: %%s, want %%s\", got, want)\n\t\t}\n\t}\n", k, m.fold(seq), k, seq)
	}
	steps := 4
	if m.pure() {
		steps = 3
	}
	fmt.Fprintf(&b, "\tif n := reflect.TypeOf(M%d.Step).NumIn(); n != %d {\n\t\tt.Errorf(\"M%d.Step takes %%d arguments with its receiver, want %d\", n)\n\t}\n", k, steps, k, steps)
	if m.pure() {
		fmt.Fprintf(&b, "\tvar plan M%d\n\ts := plan.Initial()\n\tfor _, e := range signals {\n", k)
		fmt.Fprintf(&b, "\t\tviaPlan := testing.AllocsPerRun(200, func() { sink%d = plan.Step(s, e) })\n", k)
		fmt.Fprintf(&b, "\t\tdirect := testing.AllocsPerRun(200, func() { sink%d = step%d(nil, s, e) })\n", k, k)
		fmt.Fprintf(&b, "\t\tif viaPlan > direct {\n\t\t\tt.Errorf(\"M%d.Step allocates %%v per call, the direct call %%v\", viaPlan, direct)\n\t\t}\n\t}\n", k)
	}
	b.WriteString("}\n\n")
	// The sink keeps the allocation check's results alive.
	fmt.Fprintf(&b, "var sink%d Decision[M%dS, int]\n\n", k, k)
	return b.String()
}

// TestMachinePlanValues generates 24 machines (state unions of 1 to 4
// variants, signal unions of 1 to 3, total step tables over Enter, Stay,
// Ignore, Reject and Done; some steps have a fails row whose cells may
// fail, and some have a needs row; each initial value is written as a
// value, a package let or a function that reads one) and sequences of 0 to
// 8 signals. A Go test in
// the emitted module folds each plan's Initial and Step over each sequence;
// the result must equal the reference model's. A pure step's plan takes no
// environment and allocates no more than the direct call of the step. The
// program also holds a machine over a generic union, an unused machine,
// and user functions named Initial and Step, which do not collide with the
// plan's methods.
func TestMachinePlanValues(t *testing.T) {
	rng := rand.New(rand.NewSource(57))
	var machines []planMachine
	var src strings.Builder
	src.WriteString("type Bad {}\n\nfunc bad() fails Bad { Bad }\n\n")
	src.WriteString("type Slot[T] = Full[T]{V T} | Hole{}\n\ntype Poke = Put{} | Take{}\n\n")
	src.WriteString("func slot(s Slot[int], p Poke) Decision[Slot[int], int] {\n\tmatch p {\n\tcase Put:\n\t\tEnter[Slot[int]]{Next: Full[int]{V: 7}}\n\tcase Take:\n\t\tEnter[Slot[int]]{Next: Hole}\n\t}\n}\n\n")
	src.WriteString("type G machine[Slot[int], Poke] {\n\tinitial: Hole,\n\tstep:    slot,\n}\n\n")
	src.WriteString("type Unused machine[Slot[int], Poke] {\n\tinitial: Full[int]{V: 1},\n\tstep:    slot,\n}\n\n")
	src.WriteString("func Initial(g G) int { 1 }\n\nfunc Step(g G, n int) int { n }\n\n")
	src.WriteString("func main() needs Console {\n\tlet gs = []G{G{}, G{}}\n\tConsole.Println(\"\\(Initial(gs[0]) + Step(gs[1], 2))\")\n}\n\n")
	for k := 0; k < 24; k++ {
		m := genPlanMachine(rng, k)
		machines = append(machines, m)
		src.WriteString(m.source())
	}
	prog := mustLower(t, "plans", src.String())
	var driver strings.Builder
	// The driver's Scheduler is its own Go provider, so a step with a
	// needs row runs with the environment the driver builds.
	driver.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"reflect\"\n\t\"testing\"\n\t\"time\"\n\n\trt \"ergo.local/prototype/runtime/rt\"\n)\n\n")
	driver.WriteString("type driverScheduler struct{ sleeps *int }\n\nfunc (d driverScheduler) Sleep(*rt.Task, time.Duration) { *d.sleeps++ }\n\n")
	driver.WriteString("func TestPlans(t *testing.T) {\n\tsleeps := 0\n\t_, err := rt.Run(rt.Root(\"plans\"), func(task *rt.Task) (struct{}, error) {\n\t\tenv := &Env{task: task, Scheduler: driverScheduler{&sleeps}}\n\t\tergoInit(env)\n")
	driver.WriteString("\t\tvar g G\n\t\tif _, ok := g.Initial().(Hole); !ok {\n\t\t\tt.Errorf(\"G starts at %T\", g.Initial())\n\t\t}\n\t\tif full, ok := g.Step(g.Initial(), Put{}).(Enter[Slot[int]]); !ok || full.Next != (Full[int]{V: 7}) {\n\t\t\tt.Errorf(\"G.Step(Hole, Put) is %v\", g.Step(g.Initial(), Put{}))\n\t\t}\n")
	driver.WriteString("\t\tif u, ok := (Unused{}).Initial().(Full[int]); !ok || u.V != 1 {\n\t\t\tt.Errorf(\"Unused starts at %v\", (Unused{}).Initial())\n\t\t}\n")
	for _, m := range machines {
		fmt.Fprintf(&driver, "\t\tcheck%d(t, env)\n", m.k)
	}
	needs := 0
	for _, m := range machines {
		if m.needs {
			needs++
		}
	}
	// A step with a needs row sleeps once per step: a fold that ran the
	// plan's Step reaches the driver's provider.
	fmt.Fprintf(&driver, "\t\treturn struct{}{}, nil\n\t})\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n\tif %d > 0 && sleeps == 0 {\n\t\tt.Error(\"no step with a needs row reached the provider\")\n\t}\n}\n\n", needs)
	for _, m := range machines {
		driver.WriteString(m.driver())
	}
	text := driver.String()
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/plans/main.go", []byte(prog.mainGo()))
	write("p/plans/plans_test.go", []byte(text))
	cmd := exec.Command("go", "test", "-count=1", "./p/plans")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("the plans' Go test failed: %v\n%s", err, firstLines(out.String(), 60))
	}
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	if stdout, stderr, code := runExe(t, exes["plans"]); code != 0 || stdout != "3\n" {
		t.Errorf("the program with machines: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}
