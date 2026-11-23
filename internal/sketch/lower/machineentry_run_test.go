package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// genEntryProgram is a program with two twin machines over one step: A
// names its work and exit functions, and B writes each as an expression
// drawn by entryForm (MACHINE-EXPR; runtime.md, Machines and actors 1).
// main plays each generated act list on an actor of each machine through
// a testing.Stage, and prints, for each machine, the states the stage
// shows and the exit actions' log after the stop.
func genEntryProgram(r *rand.Rand, k int, seen map[string]bool) (name, src string, runs int) {
	var b strings.Builder
	b.WriteString(`import actor
import machines
import sync
import testing
import time

type Phase = Open{N int} | Saving{N int} | Saved{N int}
type Sig = Add{N int} | Save{} | Stored{}

func step(s Phase, e Sig) Decision[Phase, int] {
	match (s, e) {
	case (Open as o, Add as a):
		Stay[Phase]{Next: Open{N: o.N + a.N}}
	case (Open as o, Save):
		Enter[Phase]{Next: Saving{N: o.N}}
	case (Saving as v, Stored):
		Enter[Phase]{Next: Saved{N: v.N}}
	case (Saved as d, Add as a):
		Enter[Phase]{Next: Open{N: d.N + a.N}}
	case (_, _):
		Ignore
	}
}

func work(s Phase) Option[machines.Work[Sig, Scheduler]] {
	match s {
	case Saving:
		Some[machines.Work[Sig, Scheduler]]{Value: machines.Do[Sig, Scheduler](func() Sig { Stored })}
	case Open, Saved:
		None
	}
}

type seen {
	Left(n int),
}

type logOf { got sync.Ref[[]int] } implements seen

func Left(l logOf, n int) {
	l.got.Update(func(v []int) []int { v.Append(n) })
}

func nOf(s Phase) int {
	match s {
	case Open as o:
		o.N
	case Saving as v:
		v.N
	case Saved as d:
		d.N
	}
}

func leave(s Phase) Option[func() needs seen] {
	let n = nOf(s)
	if ((n % 3) + 3) % 3 == 2 {
		return None
	}
	Some{func() needs seen { seen.Left(n) }}
}

func show(s Phase) string {
	match s {
	case Open as o:
		"open \(o.N)"
	case Saving as v:
		"saving \(v.N)"
	case Saved as d:
		"saved \(d.N)"
	}
}

func stepOf(k int) testing.Step[Sig] {
	match ((k % 3) + 3) % 3 {
	case 0:
		testing.Do[Sig]{Act: Add{N: k}}
	case 1:
		testing.Do[Sig]{Act: Save}
	case _:
		testing.Settle
	}
}

func ints(xs []int) string {
	let mut t = "["
	for i, x in xs {
		if i > 0 {
			t = t + " "
		}
		t = t + "\(x)"
	}
	t + "]"
}

`)
	workExpr := entryForm(&b, r, seen, "work", "work", "func(s Phase) Option[machines.Work[Sig, Scheduler]]", "s Phase", "s", false)
	exitExpr := entryForm(&b, r, seen, "exit", "leave", "func(s Phase) Option[func() needs seen]", "s Phase", "s", false)
	fmt.Fprintf(&b, "type A machine[Phase, Sig] { initial: Open{N: 0}, step: step, work: work, exit: leave }\n\n")
	// The step is pure here, so its wrap form is a known builder call: the
	// entry's copy (entrycopy.go) runs it.
	stepExpr := entryForm(&b, r, seen, "step", "step", "func(s Phase, e Sig) Decision[Phase, int]", "s Phase, e Sig", "s, e", false)
	fmt.Fprintf(&b, "type B machine[Phase, Sig] { initial: Open{N: 0}, step: %s, work: %s, exit: %s }\n\n", stepExpr, workExpr, exitExpr)
	for _, m := range []string{"A", "B"} {
		fmt.Fprintf(&b, `func run%[1]s(acts []int) string {
	let log = logOf{got: sync.NewRef[[]int]([]int{})}
	let clock = time.NewManual(0)
	let mut steps = []testing.Step[Sig]{}
	for k in acts {
		steps = steps.Append(stepOf(k)).Append(testing.Settle)
	}
	let mut out = ""
	with context { Scheduler: clock, seen: log } {
		let a = actor.StartMachine(%[1]s{}, 8, func(o int) {})
		let shows = testing.Play(actor.NewStage(a, clock), steps) catch {
		case testing.Refused:
			return "refused"
		}
		a.Stop()
		for s in shows {
			out = out + show(s) + ", "
		}
	}
	out + "exits " + ints(log.got.Get())
}

`, m)
	}
	b.WriteString("func main() needs Console {\n")
	runs = 4 + r.Intn(4)
	for range runs {
		acts := make([]string, r.Intn(10))
		for i := range acts {
			acts[i] = strconv.Itoa(r.Intn(13) - 4)
		}
		fmt.Fprintf(&b, "\tConsole.Println(runA([]int{%[1]s}))\n\tConsole.Println(runB([]int{%[1]s}))\n", strings.Join(acts, ", "))
	}
	b.WriteString("}\n")
	return fmt.Sprintf("machineentry%d", k), b.String(), runs
}

// TestMachineEntryFormsAgree is the property of the work and exit entries
// (MACHINE-EXPR): over generated act lists, an actor of a machine whose
// work and exit are expressions (entryForm: a call that gives the function
// back, a literal calling it, or a pick by a value) shows the states and
// runs the exit actions the twin that names them does. Its step is pure,
// so the step's wrap form is a known builder call whose entry copy
// (entrycopy.go) the actor runs; TestMachineRunAgreesWithFold has the
// step, after and start forms of failing steps. The generated
// programs must write each entry in every form, reach the saved state and
// log an exit. ERGO_MACHINEENTRY_SEED replays another seed.
//
// It builds Go because the forms differ only in the lowered conversion
// (a computed entry's package variable against a direct call) and the
// actor's runner runs only in the built program; no in-process check
// observes a run.
func TestMachineEntryFormsAgree(t *testing.T) {
	needsBuild(t)
	seed := int64(3)
	if s := os.Getenv("ERGO_MACHINEENTRY_SEED"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_MACHINEENTRY_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewSource(seed))
	type prog struct {
		name, src string
		runs      int
	}
	seen := map[string]bool{}
	cases := []string{"a step written as via", "a step written as wrap", "a step written as pick",
		"a work written as via", "a work written as wrap", "a work written as pick",
		"a exit written as via", "a exit written as wrap", "a exit written as pick"}
	missing := func() string {
		for _, c := range cases {
			if !seen[c] {
				return "no generated program has " + strconv.Quote(c)
			}
		}
		return ""
	}
	var progs []prog
	var builds []*lowered
	for k := 0; k < 4 || missing() != "" && k < 32; k++ {
		name, src, runs := genEntryProgram(r, k, seen)
		progs = append(progs, prog{name, src, runs})
		builds = append(builds, mustLower(t, name, src))
	}
	if m := missing(); m != "" {
		t.Fatalf("seed %d, %d programs: %s", seed, len(progs), m)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	saved, exited := false, false
	for _, p := range progs {
		stdout, stderr, code, ended := runMachineBounded(t, exes[p.name])
		if !ended {
			t.Fatalf("seed %d, %s: still running at the test's deadline\nsource:\n%s", seed, p.name, p.src)
		}
		got := lines(stdout)
		if code != 0 || len(got) != 2*p.runs {
			t.Fatalf("seed %d, %s: exit %d, %d lines, want %d\nstderr:\n%s\nsource:\n%s", seed, p.name, code, len(got), 2*p.runs, firstLines(stderr, 8), p.src)
		}
		for i := 0; i < len(got); i += 2 {
			if got[i] != got[i+1] {
				t.Errorf("seed %d, %s, run %d: the named machine gives\n\t%s\nthe computed one\n\t%s\nsource:\n%s", seed, p.name, i/2, got[i], got[i+1], p.src)
			}
			saved = saved || strings.Contains(got[i], "saved ")
			exited = exited || !strings.HasSuffix(got[i], "exits []")
		}
	}
	if !saved || !exited {
		t.Errorf("seed %d: no run reached the saved state (%v) or logged an exit (%v)", seed, saved, exited)
	}
}
