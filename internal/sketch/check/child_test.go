package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// childRuleSource is a package whose machine's Open entry has a listening
// work; body is that work's body (send is its parameter), helpers are more
// package declarations, and inMain is a statement of main. Each refused
// value is marked by `@`, the position its diagnostic must have.
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
		Some{machines.Work{Start: func(send entry func(e Nav)) needs Scheduler {
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

// TestChildTakesAnEntrySend holds the child rule, which is a type (NP3b;
// runtime.md, Machines and actors 14): actor.Child and ChildIn take an
// `entry func(e P)`, which only the runner gives (machines.Work's Start)
// and a parameter written entry passes on. Every other value is one S108
// at the value, and entry written outside a parameter's type is one S108
// at the word. Mutants: entry-assign-unchecked and entry-not-in-identity
// accept the refused values; entry-from-written-type refuses the send in
// the work; entry-placement-unchecked accepts the field.
func TestChildTakesAnEntrySend(t *testing.T) {
	const refusal = "only an entry's send is one"
	rows := []struct {
		name, body, helpers, inMain string
		want                        string // a part of the one diagnostic, "" for none
	}{
		{name: "in the work", body: "\t\t\tlet _ = " + strings.Replace(childCall, "@", "send", 1)},
		{name: "a local bound to send", body: "\t\t\tlet k = send\n\t\t\tlet _ = " + strings.Replace(childCall, "@", "k", 1)},
		{name: "a helper that takes an entry send", helpers: "\nfunc open(send entry func(e Nav)) needs Scheduler { let _ = " + strings.Replace(childCall, "@", "send", 1) + " }\n", body: "\t\t\topen(send)"},
		{name: "a helper's plain send", helpers: "\nfunc open(send func(e Nav)) needs Scheduler { let _ = " + strings.Replace(childCall, "@", "@send", 1) + " }\n", body: "\t\t\topen(send)", want: refusal},
		{name: "a function literal in main", inMain: "\tlet _ = " + strings.Replace(childCall, "@", "@func(e Nav) {}", 1), want: refusal},
		// mu-host: ChildIn is a child start too.
		{name: "ChildIn in main", inMain: "\tlet _ = actor.ChildIn(@func(e Nav) {}, actor.NewHost[actor.Actor[Screen, Edit, Decision[Screen, int]]](), Editor{}, 1, toNav)", want: refusal},
		// A function that takes an entry send does not cross into a
		// plain function type, whose caller could pass any send.
		{name: "an entry taker as a plain function", helpers: "\nfunc open(send entry func(e Nav)) {}\n", inMain: "\tlet f func(s func(e Nav)) = @open\n\tf(func(e Nav) {})", want: "cannot"},
		{name: "entry on a field", helpers: "\ntype Keep { Send @entry func(e Nav) }\n", want: "entry func(…) is written only as a parameter's type"},
	}
	for _, r := range rows {
		src := childRuleSource(r.body, r.helpers, r.inMain)
		at := strings.Index(src, "@")
		_, diags := check.CheckSource(strings.Replace(src, "@", "", 1))
		var got []string
		for _, d := range diags {
			got = append(got, d.Code+" "+d.Message)
		}
		if r.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: diagnostics %q, want none\n%s", r.name, got, src)
			}
			continue
		}
		if len(got) != 1 || !strings.HasPrefix(got[0], "S108 ") || !strings.Contains(got[0], r.want) {
			t.Errorf("%s: diagnostics %q, want one S108 with %q\n%s", r.name, got, r.want, src)
			continue
		}
		if diags[0].Offset != at {
			t.Errorf("%s: diagnostic at offset %d, want %d\n%s", r.name, diags[0].Offset, at, src)
		}
	}
}
