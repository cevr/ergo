package check_test

import (
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

type State = Idle{} | Busy{N int}
type Signal = Go{} | Bump{}

func step(s State, e Signal) Decision[State, int] ` + step + `

type Counter machine[State, Signal] { initial: Idle, step: step }

func pick(n int) State { Busy{N: n} }

type Counted machine[State, Signal] { initial: pick, step: step }

// uses names each import, so no row reports one unused.
func uses(_ actor.Stopped, _ machines.Rejected, _ journal.Failed) {}
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
			want: "journal.Start replays the saved signals with the machine's step, so the step must be pure: step step calls the capability operation Console.Println at 10:2",
		},
		{
			name: "helper", step: "needs Console {\n\tlog()\n\tIgnore\n}", helpers: "\nfunc log() needs Console { Console.Println(\"x\") }\n", call: start,
			want: "step step calls log, which is not pure at 10:2",
		},
		{
			name: "input machine impure", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
			call: "journal.Start(machines.Start(Counted{}, 3), j, 2, 1, func(o int) {})",
			want: "step step calls the capability operation Console.Println at 10:2",
		},
		{
			name: "hooks impure", step: "needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}",
			call: "actor.StartDurable(Counter{}, 1, func(o int) {}, func() Option[State] fails journal.Failed { None }, func(p State, n State, e Signal) fails journal.Failed { try j.Append(e) })",
			want: "actor.StartDurable replays the saved signals with the machine's step, so the step must be pure: step step calls",
		},
		{name: "literal", step: pureStep, call: "journal.Start(" + literal + ", j, 2, 1, func(o int) {})"},
		{
			name: "generic step", step: pureStep, helpers: "\nfunc keepStep[S, E](s S, e E) Decision[S, int] { Ignore }\n",
			call: "journal.Start(Machine[State, Signal, int]{Initial: Idle, Step: keepStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal))] { None }}, j, 2, 1, func(o int) {})",
		},
		{
			name: "generic step impure", step: pureStep, helpers: "\ntype Loud {}\n\nfunc loudStep[S, E](s S, e E) Decision[S, int] fails Loud needs Console {\n\tConsole.Println(\"x\")\n\tIgnore\n}\n",
			call: "journal.Start(Machine[State, Signal, int, Loud, Console]{Initial: Idle, Step: loudStep[State, Signal], After: func(s State) Option[Timeout[Signal]] { None }, Work: func(s State) Option[func(send func(e Signal)) needs Console] { None }}, j, 2, 1, func(o int) {})",
			want: "step loudStep calls the capability operation Console.Println",
		},
		{
			name: "value", step: pureStep, helpers: "\nfunc counter() Machine[State, Signal, int] { Counter{} }\n",
			call: "journal.Start(counter(), j, 2, 1, func(o int) {})",
			want: "journal.Start replays the saved signals with the machine's step, so the checker must see that the step is pure: pass a machine declaration's value X{}, machines.Start(X{}, input), or a Machine literal whose Step names a function",
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
