package lower

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
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
// which name none. ERGO_GRAPH_SEED replays one seed; a failure prints the
// seed, the Ergo source and both traces.
//
// It builds Go because the names under test are the Go runtime's type
// strings of the variants, which only a running program gives, and the
// graph reaches the runner only through the built program's hidden
// argument. It builds one binary per program, not the dispatcher: the
// dispatcher renames package main, and the runtime spells a type argument
// with its package's path.
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
		seed   int64
		src    string
		shared []bool
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, shared := genGraphProgram(rand.New(rand.NewSource(seed)), 3*int(seed-1))
		runs = append(runs, run{seed, src, shared})
		for _, mode := range []string{"", "registry"} {
			graphLowering = mode
			programs = append(programs, mustLower(t, fmt.Sprintf("graph%d%s", seed, mode), src))
		}
	}
	exes := buildSeparate(t, programs)
	if exes == nil {
		t.FailNow()
	}
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
		if len(order[0]) != len(r.shared) {
			fail("%d actors ran, want %d", len(order[0]), len(r.shared))
			continue
		}
		for k, task := range order[0] {
			for _, line := range traces[0][task] {
				f := fields(line)
				named := ""
				switch f["what"] {
				case "decide":
					named = f["arm"]
				case "timer-armed":
					named = f["signal"]
				default:
					continue
				}
				if (named == "") != r.shared[k] {
					fail("actor %d (shared pair %t): %q", k, r.shared[k], line)
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

// genGraphProgram writes a program of three machines, each run as an
// actor that asks it signals, and reports for each actor whether its
// machine shares its state and signal types with another machine. Machine
// k's states hold Box of graphArgs[first+k], so the seeds 1 to 6 (first
// 3*(seed-1)) hold every argument form; the other instances draw theirs.
func genGraphProgram(r *rand.Rand, first int) (string, []bool) {
	var b strings.Builder
	b.WriteString("import actor\nimport queue\nimport time\n\n")
	b.WriteString("type Item { N int }\ntype Box[T] { V T }\ntype Pair[A, B] { L A, R B }\ntype Wrap[T] { W T }\n\n")
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
		from := 0
		if prefix == "A" {
			vs = append(vs, generics[0](graphArgs[((first+k)%len(graphArgs)+len(graphArgs))%len(graphArgs)], graphArg{}))
			from = 1
		}
		for _, i := range r.Perm(len(generics) - from)[:r.Intn(3-from)] {
			vs = append(vs, generics[from+i](graphArgs[r.Intn(len(graphArgs))], graphArgs[r.Intn(len(graphArgs))]))
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
	n := 3
	var shared []bool
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
				fmt.Fprintf(&b, "\tcase %s:\n\t\t%s\n", strings.Join(alts, ", "), dec())
			}
		}
		if len(covered) < 4*len(states)*len(signals) {
			fmt.Fprintf(&b, "\tcase (_, _):\n\t\t%s\n", dec())
		}
		b.WriteString("\t}\n}\n\n")
		fmt.Fprintf(&b, "func after%d(s %s) Option[Timeout[%s]] {\n\tmatch s {\n", k, st, sg)
		for _, v := range states {
			if r.Intn(2) == 0 {
				fmt.Fprintf(&b, "\tcase %s:\n\t\tSome[Timeout[%s]]{Value: Timeout[%s]{In: 5s, Signal: %s}}\n", v.pat, sg, sg, signals[r.Intn(len(signals))].val)
			} else {
				fmt.Fprintf(&b, "\tcase %s:\n\t\tNone\n", v.pat)
			}
		}
		b.WriteString("\t}\n}\n\n")
		fmt.Fprintf(&b, "type M%d machine[%s, %s] { initial: %s, step: step%d, after: after%d }\n\n", k, st, sg, states[r.Intn(len(states))].val, k, k)
		var asks strings.Builder
		for range r.Intn(6) + 2 {
			fmt.Fprintf(&asks, "\t\ta%d.Ask(%s) catch {\n\t\tcase actor.Stopped:\n\t\t}\n", k, signals[r.Intn(len(signals))].val)
		}
		run := func(m string) string {
			return fmt.Sprintf("\t\tlet a%d = actor.StartMachine(%s{}, 0, func(o int) {})\n%s\t\ta%d.Stop()\n", k, m, asks.String(), k)
		}
		twin := r.Intn(2) == 0
		mains = append(mains, run(fmt.Sprintf("M%d", k)))
		shared = append(shared, twin)
		if twin {
			// A second machine over the same types: its own step, whose one
			// arm the runtime could not tell from the first machine's.
			fmt.Fprintf(&b, "func twin%d(s %s, e %s) Decision[%s, int] {\n\tmatch (s, e) {\n\tcase (_, _):\n\t\tIgnore\n\t}\n}\n\n", k, st, sg, st)
			fmt.Fprintf(&b, "type T%d machine[%s, %s] { initial: %s, step: twin%d, after: after%d }\n\n", k, st, sg, states[0].val, k, k)
			mains = append(mains, strings.ReplaceAll(run(fmt.Sprintf("T%d", k)), fmt.Sprintf("a%d", k), fmt.Sprintf("t%d", k)))
			shared = append(shared, true)
		}
	}
	b.WriteString("func main() {\n\twith context { Scheduler: time.NewManual(0) } {\n")
	b.WriteString(strings.Join(mains, ""))
	b.WriteString("\t}\n}\n")
	return b.String(), shared
}
