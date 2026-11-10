package lower

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestGraphLoweringsTraceAlike holds the static machine graphs (machine.go,
// binsize BS7) against the reference registry (graphLowering "registry",
// the lowering before BS7): generated programs, each with machines whose
// state and signal unions hold records and instances of generic records
// over every form goType writes (basic types, duration, records, slices,
// arrays, Maps, Options, tuples, function types, nested instances), a step
// of whole, partial and multi-pattern arms, timers, and at times a second
// machine over the same state and signal types, run each machine as a
// traced actor under both lowerings. Each actor's machine events must be
// the same under both, and must name an arm for every decision and the
// timer's signal of every armed timer, except the actors of a shared pair,
// which name none. The arm a decision names must give that decision's
// kind, and no event may name a union instead of its variant (a flat
// signal union; two instances of one generic in one union, which have one
// declaration). ERGO_GRAPH_SEED replays one seed; a failure prints the
// seed, the Ergo source and both traces.
//
// It builds Go because the names under test are the Go runtime's type
// strings of the variants, which only a running program gives, and the
// graph reaches the runner only through the built program's hidden
// argument. It builds one binary per program with one go build, not the
// dispatcher: the dispatcher renames package main, and the runtime spells
// a type argument with its package's path. Every run is bounded in memory
// (runProgram).
func TestGraphLoweringsTraceAlike(t *testing.T) {
	needsBuild(t)
	seeds := []int64{1, 2, 3, 4, 5, 6}
	if s := os.Getenv("ERGO_GRAPH_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []int64{n}
	}
	before := graphLowering
	defer func() { graphLowering = before }()
	type run struct {
		seed int64
		src  string
		gen  graphProgram
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, gen := genGraphProgram(rand.New(rand.NewSource(seed)), 3*int(seed-1))
		runs = append(runs, run{seed, src, gen})
		for _, mode := range []string{"", "registry"} {
			graphLowering = mode
			programs = append(programs, mustLower(t, fmt.Sprintf("graph%d%s", seed, mode), src))
		}
	}
	m := newGoModuleDir(t)
	for _, p := range programs {
		m.program(p, p.mainGo())
	}
	bin := filepath.Join(m.root, "bin")
	if out, ok := m.goCommand("build", "-trimpath", "-o", bin+string(filepath.Separator), "./p/..."); !ok {
		m.report("build", out)
		t.FailNow()
	}
	exes := map[string]string{}
	for _, p := range programs {
		exes[p.name] = filepath.Join(bin, p.name)
	}
seeds:
	for i, r := range runs {
		var traces [2]map[int][]string
		var order [2][]int
		for j := range 2 {
			traces[j], order[j] = graphTrace(t, exes[programs[2*i+j].name])
		}
		fail := func(format string, args ...any) {
			t.Errorf("seed %d: %s\nreplay: ERGO_GRAPH_SEED=%d go test -run TestGraphLoweringsTraceAlike ./internal/sketch/lower\nsource:\n%s\nstatic:\n%s\nregistry:\n%s",
				r.seed, fmt.Sprintf(format, args...), r.seed, r.src, showTrace(traces[0], order[0]), showTrace(traces[1], order[1]))
		}
		if showTrace(traces[0], order[0]) != showTrace(traces[1], order[1]) {
			fail("the static graphs' trace is not the registry's")
			continue
		}
		shared := r.gen.shared
		if len(order[0]) != len(shared) {
			fail("%d actors ran, want %d", len(order[0]), len(shared))
			continue
		}
		for k, task := range order[0] {
			for _, line := range traces[0][task] {
				f := fields(line)
				// A value is named by its variant, never by its union
				// (a flat signal union's Go type is the union's).
				for _, key := range []string{"state", "signal", "next"} {
					if unionName.MatchString(f[key]) {
						fail("actor %d names the union %s, not a variant: %q", k, f[key], line)
						continue seeds
					}
				}
				named := ""
				switch f["what"] {
				case "decide":
					named = armOf(line)
					// The arm named is one whose decision is the one taken:
					// a misnamed variant names another arm, or none.
					if named != "" && !r.gen.armGives(k, named, f["decision"]) {
						fail("actor %d: the arm %s gives no %s: %q", k, named, f["decision"], line)
						continue seeds
					}
				case "timer-armed":
					named = f["signal"]
				default:
					continue
				}
				if (named == "") != shared[k] {
					fail("actor %d (shared pair %t): %q", k, shared[k], line)
					continue seeds
				}
			}
		}
	}
}

// graphTrace runs exe with ERGO_TRACE and returns its machine events by
// task, each task's in order, and the tasks in the order they started. A
// stop event is left out: its caller raises it, so it may come before or
// after the runner's last events.
func graphTrace(t *testing.T, exe string) (map[int][]string, []int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "ERGO_TRACE="+path)
	if err := runProgram(t, cmd); err != nil {
		t.Fatalf("%s: %v", exe, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byTask := map[int][]string{}
	var order []int
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var l struct {
			Task    int
			Machine string
		}
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatal(err)
		}
		if l.Machine == "" || strings.HasPrefix(l.Machine, "stop ") {
			continue
		}
		if strings.HasPrefix(l.Machine, "start ") {
			order = append(order, l.Task)
		}
		byTask[l.Task] = append(byTask[l.Task], l.Machine)
	}
	return byTask, order
}

// unionName is the name of a generated state or signal union.
var unionName = regexp.MustCompile(`^(St|Sg)[0-9]+$`)

// armOf is the arm a decide event names: the text after "arm=" up to the
// next field, since an arm after a partial one is "f:3 or f:5".
func armOf(line string) string {
	_, rest, ok := strings.Cut(line, " arm=")
	if !ok {
		return ""
	}
	arm, _, _ := strings.Cut(rest, " next=")
	return arm
}

// graphProgram is what the generator knows of a program's actors, in the
// order they start: whether each one's machine shares its types with
// another, and the decision kind of each arm of its step, by the arm's
// line.
type graphProgram struct {
	shared []bool
	arms   []map[int]string
}

// armGives reports whether one of the arms of actor k that arm names
// ("f:3", or "f:3 or f:5") gives the decision kind.
func (p graphProgram) armGives(k int, arm, kind string) bool {
	for _, site := range strings.Split(arm, " or ") {
		i := strings.LastIndexByte(site, ':')
		line, err := strconv.Atoi(site[i+1:])
		if err == nil && p.arms[k][line] == kind {
			return true
		}
	}
	return false
}

func showTrace(byTask map[int][]string, order []int) string {
	var b strings.Builder
	for k, task := range order {
		fmt.Fprintf(&b, "actor %d:\n", k)
		for _, l := range byTask[task] {
			b.WriteString("  " + l + "\n")
		}
	}
	return b.String()
}

// graphArg is a type argument of a generated instance and a value of it.
type graphArg struct{ typ, val string }

// graphArgs are the type arguments the generator draws: one per form
// goType writes for a value type.
var graphArgs = []graphArg{
	{"int", "1"},
	{"string", `"s"`},
	{"bool", "true"},
	{"float64", "1.5"},
	{"duration", "2s"},
	{"Item", "Item{N: 1}"},
	{"[]int", "[]int{1}"},
	{"[2]string", `[2]string{"a", "b"}`},
	{"Map[string, int]", "Map[string, int]{}"},
	{"Option[int]", "None"},
	{"(int, string)", `(1, "t")`},
	{"func(x int) int", "func(x int) int { x }"},
	{"func(x int)", "func(x int) {}"},
	{"queue.Queue[int]", "queue.New[int](1)"},
	{"Box[Item]", "Box[Item]{V: Item{N: 2}}"},
	{"Pair[[]Item, Box[int]]", "Pair[[]Item, Box[int]]{L: []Item{}, R: Box[int]{V: 3}}"},
}

// graphVariant is one variant of a generated union: its type, a value of
// it, its pattern, and a partial pattern (one that tests a field), "" for
// none.
type graphVariant struct{ typ, val, pat, partial string }

// genGraphProgram writes a program of three machines, and at times a
// fourth with a twin over the same types, each run as an actor that asks
// it signals, and reports for each actor whether its machine shares its
// state and signal types with another machine, and its arms' decision
// kinds. A union may hold two instances of one generic record (Dup), and a
// signal union may hold no generic record, so that it lowers flat. Machine
// k starts in Box of
// graphArgs[first+k], a state with a timer, so the seeds 1 to 6 (first
// 3*(seed-1)) name every argument form at the start of an actor whose
// graph is its own; the other instances draw their arguments.
func genGraphProgram(r *rand.Rand, first int) (string, graphProgram) {
	var b strings.Builder
	b.WriteString("import actor\nimport queue\nimport time\n\n")
	b.WriteString("type Item { N int }\ntype Box[T] { V T }\ntype Pair[A, B] { L A, R B }\ntype Wrap[T] { W T }\ntype Dup[T] { D T }\n\n")
	b.WriteString("// queue is imported for its Queue, which a variant may hold.\nfunc queued() queue.Queue[int] { queue.New[int](1) }\n\n")
	generics := []func(graphArg, graphArg) graphVariant{
		func(a, _ graphArg) graphVariant {
			typ := "Box[" + a.typ + "]"
			return graphVariant{typ, typ + "{V: " + a.val + "}", typ, ""}
		},
		func(a, c graphArg) graphVariant {
			typ := "Pair[" + a.typ + ", " + c.typ + "]"
			return graphVariant{typ, typ + "{L: " + a.val + ", R: " + c.val + "}", typ, ""}
		},
		func(a, _ graphArg) graphVariant {
			typ := "Wrap[" + a.typ + "]"
			return graphVariant{typ, typ + "{W: " + a.val + "}", typ, ""}
		},
	}
	union := func(k int, prefix string) []graphVariant {
		vs := []graphVariant{
			{fmt.Sprintf("%s%d{N int}", prefix, k), fmt.Sprintf("%s%d{N: 1}", prefix, k), fmt.Sprintf("%s%d", prefix, k), fmt.Sprintf("%s%d{N: 1}", prefix, k)},
			{fmt.Sprintf("%sb%d{}", prefix, k), fmt.Sprintf("%sb%d", prefix, k), fmt.Sprintf("%sb%d", prefix, k), ""},
		}
		// A signal union with no generic variant lowers flat
		// (flatunion.go): a Go struct named after the union, whose value
		// must still be named by its variant.
		from, more := 0, r.Intn(3)
		if prefix == "A" {
			vs = append(vs, generics[0](graphArgs[((first+k)%len(graphArgs)+len(graphArgs))%len(graphArgs)], graphArg{}))
			from, more = 1, r.Intn(3)
		}
		for _, i := range r.Perm(len(generics) - from)[:more] {
			vs = append(vs, generics[from+i](graphArgs[r.Intn(len(graphArgs))], graphArgs[r.Intn(len(graphArgs))]))
		}
		if more > 0 && r.Intn(2) == 0 {
			// Two instances of one generic record: their variants have
			// one declaration, so only their type arguments tell them apart.
			i := r.Perm(len(graphArgs))
			for _, a := range []graphArg{graphArgs[i[0]], graphArgs[i[1]]} {
				typ := "Dup[" + a.typ + "]"
				vs = append(vs, graphVariant{typ, typ + "{D: " + a.val + "}", typ, ""})
			}
		}
		return vs
	}
	decl := func(vs []graphVariant) string {
		var out []string
		for _, v := range vs {
			out = append(out, v.typ)
		}
		return strings.Join(out, " | ")
	}
	// Machines 0 to 2 have types of their own; machine 3, when there is
	// one, shares its types with a twin.
	n := 3 + r.Intn(2)
	var p graphProgram
	var mains []string
	for k := range n {
		st, sg := fmt.Sprintf("St%d", k), fmt.Sprintf("Sg%d", k)
		states, signals := union(k, "A"), union(k, "X")
		fmt.Fprintf(&b, "type %s = %s\ntype %s = %s\n\n", st, decl(states), sg, decl(signals))
		dec := func() string {
			s := states[r.Intn(len(states))].val
			switch r.Intn(6) {
			case 0:
				return "Enter[" + st + "]{Next: " + s + "}"
			case 1:
				return "Stay[" + st + "]{Next: " + s + "}"
			case 2:
				return "Emit[" + st + ", int]{Next: " + s + ", Output: 1}"
			case 3:
				return "Reject"
			}
			return "Ignore"
		}
		pat := func(vs []graphVariant, i int, partial bool) string {
			switch {
			case i < 0:
				return "_"
			case partial && vs[i].partial != "":
				return vs[i].partial
			}
			return vs[i].pat
		}
		fmt.Fprintf(&b, "func step%d(s %s, e %s) Decision[%s, int] {\n\tmatch (s, e) {\n", k, st, sg, st)
		kinds := map[int]string{}
		// arm writes a case of the patterns pats and records its line's
		// decision kind.
		arm := func(pats string) {
			d := dec()
			kind, _, _ := strings.Cut(d, "[")
			kinds[strings.Count(b.String(), "\n")+1] = kind
			fmt.Fprintf(&b, "\tcase %s:\n\t\t%s\n", pats, d)
		}
		// covered holds the cells a pattern took, a cell a variant and
		// whether its field is the partial patterns' literal: the checker
		// refuses a pattern whose cells are all covered.
		covered := map[[2]int]bool{}
		cells := func(vs []graphVariant, i int, partial bool) []int {
			switch {
			case i < 0:
				var all []int
				for x := range 2 * len(vs) {
					all = append(all, x)
				}
				return all
			case partial && vs[i].partial != "":
				return []int{2*i + 1}
			}
			return []int{2 * i, 2*i + 1}
		}
		for range r.Intn(5) + 1 {
			var alts []string
			for range r.Intn(2) + 1 {
				i, j := r.Intn(len(states)+1)-1, r.Intn(len(signals)+1)-1
				ip, jp := r.Intn(3) == 0, r.Intn(3) == 0
				var ps [][2]int
				fresh := false
				for _, x := range cells(states, i, ip) {
					for _, y := range cells(signals, j, jp) {
						ps = append(ps, [2]int{x, y})
						fresh = fresh || !covered[[2]int{x, y}]
					}
				}
				if !fresh {
					continue
				}
				for _, p := range ps {
					covered[p] = true
				}
				alts = append(alts, "("+pat(states, i, ip)+", "+pat(signals, j, jp)+")")
			}
			if len(alts) > 0 {
				arm(strings.Join(alts, ", "))
			}
		}
		if len(covered) < 4*len(states)*len(signals) {
			arm("(_, _)")
		}
		b.WriteString("\t}\n}\n\n")
		fmt.Fprintf(&b, "func after%d(s %s) Option[Timeout[%s]] {\n\tmatch s {\n", k, st, sg)
		// The Box state, the first state, always has a timer, so the start
		// names its variant and its timer's signal from the graph.
		for i, v := range states {
			if i == 2 || r.Intn(2) == 0 {
				fmt.Fprintf(&b, "\tcase %s:\n\t\tSome[Timeout[%s]]{Value: Timeout[%s]{In: 5s, Signal: %s}}\n", v.pat, sg, sg, signals[r.Intn(len(signals))].val)
			} else {
				fmt.Fprintf(&b, "\tcase %s:\n\t\tNone\n", v.pat)
			}
		}
		b.WriteString("\t}\n}\n\n")
		fmt.Fprintf(&b, "type M%d machine[%s, %s] { initial: %s, step: step%d, after: after%d }\n\n", k, st, sg, states[2].val, k, k)
		var asks strings.Builder
		for range r.Intn(6) + 2 {
			fmt.Fprintf(&asks, "\t\ta%d.Ask(%s) catch {\n\t\tcase actor.Stopped:\n\t\t}\n", k, signals[r.Intn(len(signals))].val)
		}
		run := func(m string) string {
			return fmt.Sprintf("\t\tlet a%d = actor.StartMachine(%s{}, 0, func(o int) {})\n%s\t\ta%d.Stop()\n", k, m, asks.String(), k)
		}
		twin := k == 3
		mains = append(mains, run(fmt.Sprintf("M%d", k)))
		p.shared = append(p.shared, twin)
		p.arms = append(p.arms, kinds)
		if twin {
			// A second machine over the same types: its own step, whose one
			// arm the runtime could not tell from the first machine's.
			fmt.Fprintf(&b, "func twin%d(s %s, e %s) Decision[%s, int] {\n\tmatch (s, e) {\n\tcase (_, _):\n\t\tIgnore\n\t}\n}\n\n", k, st, sg, st)
			fmt.Fprintf(&b, "type T%d machine[%s, %s] { initial: %s, step: twin%d, after: after%d }\n\n", k, st, sg, states[0].val, k, k)
			mains = append(mains, strings.ReplaceAll(run(fmt.Sprintf("T%d", k)), fmt.Sprintf("a%d", k), fmt.Sprintf("t%d", k)))
			p.shared = append(p.shared, true)
			p.arms = append(p.arms, nil)
		}
	}
	b.WriteString("func main() {\n\twith context { Scheduler: time.NewManual(0) } {\n")
	b.WriteString(strings.Join(mains, ""))
	b.WriteString("\t}\n}\n")
	return b.String(), p
}
