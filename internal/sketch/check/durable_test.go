package check_test

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// durableSource is a package with a counter machine whose step body is
// step, more declarations helpers, and run's body call, which starts the
// machine with a durable runner.
func durableSource(step, helpers, call string) string {
	return `import actor
import journal
import journal/memory
import machines
import tasks

type State = Idle{} | Busy{N int}
type Signal = Go{} | Bump{}

func step(s State, e Signal) Decision[State, int] ` + step + `

type Counter machine[State, Signal] { initial: Idle, step: step }

func pick(n int) State { Busy{N: n} }

type Counted machine[State, Signal] { initial: pick, step: step }

// uses names each import, so no row reports one unused.
func uses(_ actor.Stopped, _ machines.Rejected, _ journal.Failed, _ tasks.Restart) {}
` + helpers + `
func run() needs Console | Scheduler {
	let j = memory.New[State, Signal]()
	let a = ` + call + ` catch {
	case _:
		return
	}
	a.Stop()
}

func main() needs Console | Ambient {}
`
}

const pureStep = `{
	match (s, e) {
	case (Idle, Go):
		Enter[State]{Next: Busy{N: 0}}
	case (Busy as b, Bump):
		Stay[State]{Next: Busy{N: b.N + 1}}
	case (_, _):
		Ignore
	}
}`

// TestDurableStepIsPure holds the durable rule's text (MU9, MQ5): a
// machine given to journal.Start or actor.StartDurable is a machine
// declaration's value whose step is pure, and the diagnostic names the
// first impure call of the step with its position. Mutant: without the
// rule each refused row is accepted.
func TestDurableStepIsPure(t *testing.T) {
	const start = "journal.Start(Counter{}, j, 2, 1, func(o int) {})"
	const literal = "Machine[State, Signal, int]{Initial: Idle, Step: step, After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal))] { None }}"
	for _, c := range []struct {
		name, step, helpers, call, want string
	}{
		{name: "pure", step: pureStep, call: start},
		{name: "input machine", step: pureStep, call: "journal.Start(machines.Start(Counted{}, 3), j, 2, 1, func(o int) {})"},
		{name: "hooks", step: pureStep, call: "actor.StartDurable(Counter{}, 1, func(o int) {}, func() Option[State] fails journal.Failed { None }, func(p State, n State, e Signal) fails journal.Failed { try j.Append(e) })"},
		{
			name: "capability", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}", call: start,
			want: "journal.Start requires its parameter m to be pure: step step calls the capability operation Console.Println at 11:2",
		},
		{
			name: "helper", step: "needs Console {\n\tlog()\n\tIgnore\n}", helpers: "\nfunc log() needs Console { Console.Println(\"x\") }\n", call: start,
			want: "step step calls log, which is not pure at 11:2",
		},
		{
			// Mutant dh1-checker-newhost-refused (impureWhy without its
			// MakesHost case): the text names NewHost, not the capability.
			name: "host then capability", step: "needs Console {\n\tlet _ = actor.NewHost[actor.Actor[State, Signal, Decision[State, int]]]()\n\tConsole.Println(\"x\")\n\tIgnore\n}", call: start,
			want: "step step calls the capability operation Console.Println at 12:2",
		},
		{
			name: "input machine impure", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
			call: "journal.Start(machines.Start(Counted{}, 3), j, 2, 1, func(o int) {})",
			want: "step step calls the capability operation Console.Println at 11:2",
		},
		{
			name: "hooks impure", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
			call: "actor.StartDurable(Counter{}, 1, func(o int) {}, func() Option[State] fails journal.Failed { None }, func(p State, n State, e Signal) fails journal.Failed { try j.Append(e) })",
			want: "actor.StartDurable requires its parameter m to be pure: step step calls",
		},
		{
			name: "supervised impure", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
			call: "actor.SuperviseDurable(Counter{}, 1, func(o int) {}, tasks.Restart{Limit: 1}, func() Option[State] fails journal.Failed { None }, func(p State, n State, e Signal) fails journal.Failed { try j.Append(e) })",
			want: "actor.SuperviseDurable requires its parameter m to be pure: step step calls",
		},
		{name: "literal", step: pureStep, call: "journal.Start(" + literal + ", j, 2, 1, func(o int) {})"},
		{
			name: "generic step", step: pureStep, helpers: "\nfunc keepStep[S, E](s S, e E) Decision[S, int] { Ignore }\n",
			call: "journal.Start(Machine[State, Signal, int]{Initial: Idle, Step: keepStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal))] { None }}, j, 2, 1, func(o int) {})",
		},
		{
			name: "generic step impure", step: pureStep, helpers: "\ntype Loud {}\n\nfunc loudStep[S, E](s S, e E) Decision[S, int] fails Loud needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}\n",
			call: "journal.Start(Machine[State, Signal, int, Loud, Console, Console]{Initial: Idle, Step: loudStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal)) needs Console] { None }}, j, 2, 1, func(o int) {})",
			want: "step loudStep calls the capability operation Console.Println",
		},
		{
			// Mutant pure-forward-any-param: a parameter not written pure
			// forwards nothing, so its use is refused in the wrapper.
			name: "parameter not pure", step: pureStep, helpers: strings.ReplaceAll(opaqueHelper, "RUNNER", "journal.Start"),
			call: "opaque(Counter{}, j, 2, 1, func(o int) {})",
			want: "journal.Start requires its parameter m to be pure, so the checker must see its step",
		},
		{
			// A body of two statements is not seen through.
			name: "value", step: pureStep, helpers: "\nfunc counter() Machine[State, Signal, int] {\n\tlet m = Counter{}\n\tm\n}\n",
			call: "journal.Start(counter(), j, 2, 1, func(o int) {})",
			want: "journal.Start requires its parameter m to be pure, so the checker must see its step: pass a machine declaration's value X{}, a Machine literal whose Step names a function, a call of a function whose body is one of these, or a parameter written pure (rows.md, Footprints 6)",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, diags := check.CheckSource(durableSource(c.step, c.helpers, c.call))
			if c.want == "" {
				if len(diags) != 0 {
					t.Fatalf("want clean, got\n\t%s", describe(diags))
				}
				return
			}
			if len(diags) != 1 || diags[0].Code != "S108" || !strings.Contains(diags[0].Message, c.want) {
				t.Fatalf("want one S108 containing %q, got\n\t%s", c.want, describe(diags))
			}
		})
	}
}

// pureRunnerHelpers are the declarations of every program of
// TestPureRunnerAnyPackage: steps, machines a call gives (counter, seen
// through; hidden, not), the user's durable runner durableRunner, which
// writes pure on its machine as journal.Start does, and two wrappers that
// pass their machine to RUNNER: forward, whose machine is pure, and
// opaque, whose machine is not.
const pureRunnerHelpers = `
type Loud {}

func keepStep[S, E](s S, e E) Decision[S, int] { Ignore }

func loudStep[S, E](s S, e E) Decision[S, int] fails Loud needs Console {
	Console.Println("x")
	Ignore
}

func log() needs Console { Console.Println("x") }

func counter() Machine[State, Signal, int] { Counter{} }

func hidden() Machine[State, Signal, int] {
	let m = Counter{}
	m
}

func durableRunner[S, E, O, F, R, W](m pure Machine[S, E, O, F, R, W], j journal.Journal[S, E], every int, capacity int, sink escaping func(o O) needs R) actor.Actor[S, E, Decision[S, O], F] fails journal.Failed | journal.Diverged needs R | W | Scheduler {
	try journal.Start(m, j, every, capacity, sink)
}

func forward[S, E, O, F, R, W](m pure Machine[S, E, O, F, R, W], j journal.Journal[S, E], every int, capacity int, sink escaping func(o O) needs R) actor.Actor[S, E, Decision[S, O], F] fails journal.Failed | journal.Diverged needs R | W | Scheduler {
	try RUNNER(m, j, every, capacity, sink)
}
`

// opaqueHelper is opaque, declared only in the programs that call it: its
// own call of RUNNER is refused.
const opaqueHelper = `
func opaque[S, E, O, F, R, W](m Machine[S, E, O, F, R, W], j journal.Journal[S, E], every int, capacity int, sink escaping func(o O) needs R) actor.Actor[S, E, Decision[S, O], F] fails journal.Failed | journal.Diverged needs R | W | Scheduler {
	try RUNNER(m, j, every, capacity, sink)
}
`

// pureRunnerSteps are the step bodies: pure, a capability, an impure
// helper, a host made (allowed), and a host made then a capability.
var pureRunnerSteps = []string{
	pureStep,
	"needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
	"needs Console {\n\tlog()\n\tIgnore\n}",
	"{\n\tlet _ = actor.NewHost[actor.Actor[State, Signal, Decision[State, int]]]()\n\tIgnore\n}",
	"needs Console {\n\tlet _ = actor.NewHost[actor.Actor[State, Signal, Decision[State, int]]]()\n\tConsole.Println(\"x\")\n\tIgnore\n}",
}

// pureRunnerMachines are the machine arguments: a declaration's value, an
// input machine started, a call seen through, a call not seen through,
// and literals whose Step names a pure or an impure generic function.
var pureRunnerMachines = []string{
	"Counter{}",
	"machines.Start(Counted{}, 3)",
	"counter()",
	"hidden()",
	"Machine[State, Signal, int]{Initial: Idle, Step: keepStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal))] { None }}",
	"Machine[State, Signal, int, Loud, Console, Console]{Initial: Idle, Step: loudStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal)) needs Console] { None }}",
}

// pureRunnerProgram is case n of TestPureRunnerAnyPackage with runner as
// the durable runner: a step, a machine, and whether the call passes the
// machine to runner itself or through forward or opaque.
func pureRunnerProgram(n int, runner string) string {
	step := pureRunnerSteps[n%len(pureRunnerSteps)]
	n /= len(pureRunnerSteps)
	machine := pureRunnerMachines[n%len(pureRunnerMachines)]
	n /= len(pureRunnerMachines)
	callee := []string{runner, "forward", "opaque"}[n]
	call := callee + "(" + machine + ", j, 2, 1, func(o int) {})"
	helpers := pureRunnerHelpers
	if callee == "opaque" {
		helpers += opaqueHelper
	}
	return durableSource(step, strings.ReplaceAll(helpers, "RUNNER", runner), call)
}

// TestPureRunnerAnyPackage is NP3a's acceptance (STD-NOPRIV; rows.md,
// Footprints 6): a durable runner a user declares with the clause pure
// gets the check std's journal.Start gets. Over every step, machine and
// wrapper, the program that calls the user's durableRunner reports what
// the one that calls journal.Start reports, the same codes at the same
// positions, with the runner's name the only difference in the text. The
// names have one length, so the positions agree. Replay a case with
// ERGO_PURE_RUNNER_CASE=n. Mutant durable-clause-ignored-outside-std (the
// clause read only on a std function): the user runner's calls report
// nothing. Mutant pure-see-through-off (no call seen through): std's
// journal/memory, which every program imports, passes tally() to
// journal.Start in its laws, so the import fails (S104 here).
func TestPureRunnerAnyPackage(t *testing.T) {
	if len("durableRunner") != len("journal.Start") {
		t.Fatal("the runner names differ in length, so the positions cannot agree")
	}
	cases := len(pureRunnerSteps) * len(pureRunnerMachines) * 3
	first, last := 0, cases
	if s := os.Getenv("ERGO_PURE_RUNNER_CASE"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n >= cases {
			t.Fatalf("ERGO_PURE_RUNNER_CASE=%q: want a case in [0, %d)", s, cases)
		}
		first, last = n, n+1
	}
	text := func(ds []check.Diagnostic, runner string) []string {
		var out []string
		for _, d := range ds {
			out = append(out, fmt.Sprintf("%s@%d:%d %s", d.Code, d.Line, d.Column, strings.ReplaceAll(d.Message, runner, "RUNNER")))
		}
		return out
	}
	clean, refused := 0, 0
	for n := first; n < last; n++ {
		std, user := pureRunnerProgram(n, "journal.Start"), pureRunnerProgram(n, "durableRunner")
		_, ds := check.CheckSource(std)
		_, du := check.CheckSource(user)
		want, got := text(ds, "journal.Start"), text(du, "durableRunner")
		if !slices.Equal(got, want) {
			t.Fatalf("case %d (ERGO_PURE_RUNNER_CASE=%d): the user's runner reports\n\t%s\nstd's journal.Start reports\n\t%s\n--- user program\n%s\n--- std program\n%s", n, n, strings.Join(got, "\n\t"), strings.Join(want, "\n\t"), user, std)
		}
		for _, d := range ds {
			if d.Code != "S108" {
				t.Fatalf("case %d (ERGO_PURE_RUNNER_CASE=%d): %s: only the pure rule refuses here\n%s", n, n, d, std)
			}
		}
		if len(ds) == 0 {
			clean++
		} else {
			refused++
		}
	}
	if last-first > 1 && (clean == 0 || refused == 0) {
		t.Fatalf("%d cases clean, %d refused: the property compares no diagnostic or no clean program", clean, refused)
	}
}

// TestPureClauseRefusals pins the text and position of the clause's own
// diagnostics (rows.md, Footprints 6): pure where no caller's argument
// meets it, pure before a type that runs no function, and a function
// parameter's argument not seen or impure. Mutant pure-placement-unchecked
// (checkPureClauses reads no FieldList but a declaration's): the first
// row is accepted. Mutant pure-kind-unchecked: the second is. Mutant
// pure-value-taken (no refusal of a use that is no call): the row value
// is accepted, and its call through a passes any function.
func TestPureClauseRefusals(t *testing.T) {
	const apply = "func apply(f pure func(x int) int, x int) int { f(x) }\n\nfunc twice(x int) int { x * 2 }\n\nfunc loud(x int) int {\n\tlet g = twice\n\tg(x)\n}\n\n"
	for _, c := range []struct{ name, src, want string }{
		{"literal", "func run() {\n\tlet g = func(m pure func() int) int { m() }\n\tlet _ = g\n}\n", "S108@2:17 pure is written on a parameter of a function declaration, which its callers' arguments must meet (rows.md, Footprints 6)"},
		{"kind", "func f(n pure int) int { n }\n", "S108@1:10 pure requires a function the checker sees, so it is written before a function type or a machine, not int (rows.md, Footprints 6)"},
		{"function", apply + "func run() int { apply(twice, 1) }\n", ""},
		{"value", apply + "func run() int {\n\tlet a = apply\n\ta(twice, 1)\n}\n", "S108@11:10 apply has a pure parameter, so it is only called: a call checks its argument, and a call of a value could not (rows.md, Footprints 6)"},
		{"function impure", apply + "func run() int { apply(loud, 1) }\n", "S108@10:24 apply requires its parameter f to be pure: function loud calls a function value at 7:2 (rows.md, Footprints 6)"},
		{"function unseen", apply + "func run() int { apply(func(x int) int { x }, 1) }\n", "S108@10:24 apply requires its parameter f to be pure, so the checker must see its function: pass a function's name, a call of a function whose body is one, or a parameter written pure (rows.md, Footprints 6)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, ds := check.CheckSource(c.src)
			var got []string
			for _, d := range ds {
				got = append(got, fmt.Sprintf("%s@%d:%d %s", d.Code, d.Line, d.Column, d.Message))
			}
			if strings.Join(got, "\n") != c.want {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), c.want)
			}
		})
	}
}
