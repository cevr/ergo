package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// timeout.After(d) { body } (ORACLE O17; runtime.md, Timeouts 1-6), on
// the run's tasks (sched.go): the body runs as a child task, raced
// against a deadline task that sleeps d on the run's Scheduler. The body
// that ends first gives its exit; a deadline that passes first stops the
// body and waits for its end, and the call fails Expired, the body's
// defect re-raised instead. A stop of the caller stops and drains the
// body, and the call's exit is an interruption. The deadline task is
// stopped and drained on every path. A body that returned a value the
// deadline beat holds no store the evaluator releases (Gap 47): a value
// that holds one is a skip.
func init() {
	natives["timeout.After"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if in.sched == nil {
			return nil, fr.unsupported(at, "a timeout outside a run's tasks")
		}
		if in.stoppedNow() {
			return nil, fr.interrupt(at)
		}
		d := args[0].(int64)
		sched, ok := fr.caps.lookup(typeKey(rtype{t: check.PreludeType("Scheduler")}))
		if !ok {
			return nil, fr.unsupported(at, "a timeout whose Scheduler no with of the run binds")
		}
		var run func() (Value, *abrupt)
		if c, ok := args[1].(*closure); ok {
			body, _ := in.taskBody(fr, at, c)
			in.handCaptures(fr, c)
			run = func() (Value, *abrupt) { return body(nil) }
		} else {
			run = func() (Value, *abrupt) { return in.callValue(fr, at, args[1], nil) }
		}
		call, _ := at.(*syntax.CallExpr)
		child := in.start(in.siteOf(fr, at, "timeout.After"), func(t *etask) (Value, *abrupt) { return run() })
		fired := false
		deadline := in.start(in.siteOf(fr, at, "timeout.After (deadline)"), func(t *etask) (Value, *abrupt) {
			if _, ab := in.dispatch(fr, call, sched, "Sleep", []Value{d}); ab != nil {
				return nil, ab
			}
			fired = !t.stopped
			return unit{}, nil
		})
		ab := in.block(fr, at, "a timeout's body or its deadline", func() bool { return child.state == done || fired })
		if stops(ab) && ab.kind != ctlInterrupt {
			return nil, ab
		}
		var out Value
		var exit *abrupt
		switch {
		case ab != nil: // the caller was stopped
			stopTree(child)
			if w := in.waitDone(fr, at, child, "a timeout's body to stop"); w != nil {
				return nil, w
			}
			exit = ab
			if endedByDefect(child.exit) {
				exit = child.exit
			}
		case child.state == done:
			out, exit = child.val, child.exit
		default: // the deadline passed first
			stopTree(child)
			if w := in.waitDone(fr, at, child, "a timeout's body to stop"); w != nil {
				return nil, w
			}
			exit = fr.abrupt(ctlFail, at, stdRecord(fn, "Expired", nil), "")
			if endedByDefect(child.exit) {
				exit = child.exit
			} else if child.ended && !basicValue(child.val) {
				return nil, fr.unsupported(at, "the release of a value a timeout's deadline dropped")
			}
		}
		stopTree(deadline)
		if w := in.waitDone(fr, at, deadline, "a timeout's deadline to stop"); w != nil {
			return nil, w
		}
		if endedByDefect(deadline.exit) && (exit == nil || !endedByDefect(exit)) {
			exit = deadline.exit
		}
		if exit != nil {
			return nil, exit
		}
		return out, nil
	}
}
