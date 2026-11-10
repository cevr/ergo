package lower

import "testing"

// TestSchedulerEntryNeedsExplicitProvider catches gs-init-refusal: checking
// that an entry calls package initializers after the entry was refused can
// add an internal compiler error instead of the promised S109.
// Existing entry refusal rows had no initializer; scheduler examples bind it.
// The explicit-provider control is type-checked in process, not executed.
func TestSchedulerEntryNeedsExplicitProvider(t *testing.T) {
	const src = "let Values = []int{1}\nfunc main() needs Scheduler { Scheduler.Sleep(0s) }\n"
	o := lowerSource("scheduler_entry", src, true)
	want := "main needs Scheduler, and the host provides only Console and Ambient; bind it with a with block inside main"
	if o.err != nil || len(o.diags) != 0 || o.panicked != nil || len(o.refused) != 1 || o.refused[0].Message != want || o.refused[0].Line != 2 || o.refused[0].Column != 6 {
		t.Fatalf("want one entry refusal %q at 2:6; got err=%v diagnostics=%v refusals=%v panic=%v\n%s", want, o.err, o.diags, o.refused, o.panicked, src)
	}
	const bound = `let Values = []int{1}
func main() needs Ambient {
	with context { Scheduler: NewLiveScheduler() } { Scheduler.Sleep(0s) }
	let _ = Values[0]
}
`
	p := lowerSource("scheduler_bound", bound, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("explicit provider: err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, bound)
	}
	typecheckGo(t, []*lowered{p})
}
