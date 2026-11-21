package lint

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// testWaitsOnTime is the rule for tests that wait by letting time pass
// (owner, 2026-11-17, WAIT ON EVENTS; common.md): in a test or a law, a
// sleep in the test's own frame (the Scheduler's Sleep, or a clock's)
// or an advance of a manual clock by more than zero, when nothing else the
// test runs reads or waits on time. A manual clock is a type whose
// implements clause names the Scheduler (std time.Manual,
// testing.VirtualClock, or a module's own); its advance is a method no
// operation type of that clause declares that gives no result. Then the time passed is no input of the code under
// test: it only gives other tasks a chance to finish, which a slower run
// does not. Time is the input when the test runs other time-driven code
// (a function whose row needs the Scheduler or an operation type a clock
// of the test implements, such as std time's Clock; an operation of
// one; a clock's operation or a method that gives a result, as Now,
// Elapsed, Sleep or Sleepers; a
// sleep in a task the test starts): a law of a timeout or a debounce that
// advances to d and asserts what the code did at d is no finding. An
// advance by a constant zero is a settle, a wait on the scheduler's idle
// (an advance returns once every task it woke waits again), so it is no
// finding either. The check reads calls in the test's own body: a helper
// that sleeps for it is not seen. Code under test may also read a manual
// clock it holds as a value, not through its row: so a wait on a clock the
// test did not declare (a world's field, a parameter), or one the test
// passes on or stores, is no finding.
var testWaitsOnTime = &Rule{
	Name:    "test-waits-on-time",
	Code:    "L125",
	Doc:     "A test that sleeps or advances a clock while no code it runs reads time waits for work to settle; wait on the event.",
	Instead: "wait on the event itself: the task's Wait, a deferred, a latch, a queue the work fills, or the scheduler's idle (Advance(0s), a Stage's Settle)",
	Default: Warning,
	Check:   checkTestWaitsOnTime,
}

func checkTestWaitsOnTime(p *Pass) {
	syntax.Inspect(p.File, func(n syntax.Node) bool {
		var body *syntax.BlockStmt
		switch d := n.(type) {
		case *syntax.TestDecl:
			body = d.Body
		case *syntax.LawDecl:
			body = d.Body
		default:
			return true
		}
		if body == nil {
			return false
		}
		caps := check.ClockCaps(clocksIn(p.Info, body))
		var waits []*syntax.CallExpr
		timed := false
		// own reports whether the walk is in the test's own frame, not in
		// a function literal or a call's trailing block (a task's body, a
		// callback).
		var walk func(n syntax.Node, own bool)
		walk = func(n syntax.Node, own bool) {
			syntax.Inspect(n, func(n syntax.Node) bool {
				switch x := n.(type) {
				case *syntax.FuncLit:
					if own {
						walk(x.Body, false)
						return false
					}
				case *syntax.CallExpr:
					switch check.TimeWaitOf(p.Info, x) {
					case check.WaitAdvance:
						waits = append(waits, x)
					case check.WaitSleep:
						if own {
							waits = append(waits, x)
						} else {
							timed = true
						}
					default:
						if check.TimeDriven(p.Info, x, caps) {
							timed = true
						}
					}
					// a trailing block is a function the call runs (a
					// task's body), except a with's, which is this frame
					if own && x.Trailing != nil && p.Info.Withs[x] == nil {
						walk(x.Fun, true)
						for _, a := range x.Args {
							walk(a, true)
						}
						walk(x.Trailing, false)
						return false
					}
				}
				return true
			})
		}
		walk(body, true)
		if timed || sharedClock(p.Info, body) {
			return false
		}
		for _, w := range waits {
			p.Report(w.Pos(), w.End(), "this test lets time pass while nothing it runs reads time, so it waits for work to settle and a slower run fails")
		}
		return false
	})
}

// clocksIn is the manual clocks the test names: the types of its
// variables that check.IsManualClock holds.
func clocksIn(info *check.Info, body *syntax.BlockStmt) []*check.Named {
	var clocks []*check.Named
	syntax.Inspect(body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			o := info.Uses[id]
			if o == nil {
				o = info.Defs[id]
			}
			if v, ok := o.(*check.Var); ok && check.IsManualClock(v.Type) {
				t := v.Type
				if ptr, ok := t.(*check.Pointer); ok {
					t = ptr.Elem
				}
				if n, ok := t.(*check.Named); ok && !slices.Contains(clocks, n) {
					clocks = append(clocks, n)
				}
			}
		}
		return true
	})
	return clocks
}

// sharedClock reports whether code the test runs may read a manual clock
// through a value, not a row: the test reaches a clock it did not declare
// (a world's field, a parameter), or uses one it declared other than as a
// manual clock call's receiver or a context's provider (it passes it on or
// stores it, so the code that receives it can read it).
func sharedClock(info *check.Info, body *syntax.BlockStmt) bool {
	declared := map[check.Object]bool{}
	// receivers are the uses as a manual clock call's receiver, and the
	// names a let declares (the checker records a let's name as a use too)
	receivers := map[*syntax.Ident]bool{}
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.VarSpec:
			for _, id := range x.Names {
				o := info.Defs[id]
				if o == nil {
					o = info.Uses[id]
				}
				if v, ok := o.(*check.Var); ok && check.IsManualClock(v.Type) {
					declared[v] = true
					receivers[id] = true
				}
			}
		case *syntax.CallExpr:
			if c, ok := check.ManualClockCall(info, x); ok {
				if r, ok := unparen(c).(*syntax.Ident); ok {
					receivers[r] = true
				}
			}
		}
		return true
	})
	shared := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.ContextLit:
			return false
		case *syntax.SelectorExpr:
			if check.IsManualClock(info.Types[x]) {
				shared = true
			}
		case *syntax.Ident:
			if v, ok := info.Uses[x].(*check.Var); ok && check.IsManualClock(v.Type) && (!declared[v] || !receivers[x]) {
				shared = true
			}
		}
		return true
	})
	return shared
}
