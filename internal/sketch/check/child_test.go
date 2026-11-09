package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// childRuleSource is a package whose machine's Open entry has a Listen
// work; body is that work's body (send is its parameter), helpers are more
// package declarations, and inMain is a statement of main. Each child
// call is marked by `@`, the position its diagnostic must have.
func childRuleSource(body, helpers, inMain string) string {
	return `import actor
import machines

type Screen = Editing{N int}
type Edit = Type{}

func editStep(s Screen, e Edit) Decision[Screen, int] { Emit[Screen, int]{Next: s, Output: 1} }

type Editor machine[Screen, Edit] { initial: Editing{N: 0}, step: editStep }

type Phase = Open{} | Closed{}
type Nav = Saved{N int}

func toNav(n int) Nav { Saved{N: n} }

func routerWork(s Phase) Option[machines.Work[Nav, Scheduler]] {
	match s {
	case Open:
		Some{machines.Work{Listen: func(send func(e Nav)) needs Scheduler {
` + body + `
		}}}
	case Closed:
		None
	}
}

func routerStep(s Phase, e Nav) Decision[Phase, string] { Ignore }

type Router machine[Phase, Nav] { initial: Open, step: routerStep, work: routerWork }
` + helpers + `
func main() needs Scheduler {
	let r = actor.StartMachine(Router{}, 1, func(o string) {})
	r.Stop()
` + inMain + `
}
`
}

const childCall = "actor.Child(@, Editor{}, 1, toNav)"

// TestChildOnlyInWork holds the child rule's text and position (mu5b): a
// call of actor.Child is accepted only with the send of the Listen work
// that encloses it, and every other call is one S108 at its first
// argument. Mutants: without the rule each refused row is accepted; with
// no send marked each accepted row is refused.
func TestChildOnlyInWork(t *testing.T) {
	const text = "S108 actor.Child starts a child of the entry whose work calls it, so it takes that work's send: call it in the function of a Listen work, with its send parameter as the first argument (runtime.md, Machines and actors 14)"
	rows := []struct {
		name, body, helpers, inMain string
		refused                     bool
	}{
		{name: "in the work", body: "\t\t\tlet _ = " + strings.Replace(childCall, "@", "send", 1)},
		{name: "a copy of send", body: "\t\t\tlet k = send\n\t\t\tlet _ = " + strings.Replace(childCall, "@", "k", 1), refused: true},
		{name: "a helper's send", helpers: "\nfunc open(send func(e Nav)) needs Scheduler { let _ = " + strings.Replace(childCall, "@", "send", 1) + " }\n", body: "\t\t\topen(send)", refused: true},
		{name: "in main", inMain: "\tlet _ = " + strings.Replace(childCall, "@", "func(e Nav) {}", 1), refused: true},
	}
	for _, r := range rows {
		src := childRuleSource(r.body, r.helpers, r.inMain)
		_, diags := check.CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+d.Message)
		}
		if !r.refused {
			if len(got) != 0 {
				t.Errorf("%s: diagnostics %q, want none\n%s", r.name, got, src)
			}
			continue
		}
		if len(got) != 1 || got[0] != text {
			t.Errorf("%s: diagnostics %q, want [%q]\n%s", r.name, got, text, src)
			continue
		}
		// The position: the first argument of the Child call.
		if want := strings.LastIndex(src, "actor.Child(") + len("actor.Child("); diags[0].Offset != want {
			t.Errorf("%s: diagnostic at offset %d, want %d\n%s", r.name, diags[0].Offset, want, src)
		}
	}
}
