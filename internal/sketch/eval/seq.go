package eval

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// Seq values (ORACLE O9; prelude Seq). A Seq is a function of a yield:
// Seq[T](f) keeps f, and a for over a Seq that is a function value calls
// it with a yield that runs one iteration of the loop's body, as Go's
// range over a function does (the lowering's adapter, lower/body.go):
// yield returns true to go on, and false once the body ended the loop (a
// break, a return, a failure leaving it), after which the Seq should
// return. A defect, an interruption or a stop of the run in the body
// leaves through the Seq's frames, as a Go panic in the body does. A
// yield called again after it returned false, or after the loop ended,
// is Go's runtime error.

// seqLoop runs `for v in seq` over seq, a function value.
func (in *interp) seqLoop(fr *frame, s *syntax.RangeStmt, seq Value) *abrupt {
	var exit *abrupt
	stopped, over := false, false
	yield := &nativeFunc{name: "yield", call: func(in *interp, yfr *frame, at syntax.Node, args []Value) (Value, *abrupt) {
		switch {
		case over:
			return nil, yfr.defect(at, "runtime error: range function continued iteration after whole loop exit")
		case stopped:
			return nil, yfr.defect(at, "runtime error: range function continued iteration after function for loop body returned false")
		}
		ab := in.seqIteration(fr, s, args[0])
		if ab == nil {
			return true, nil
		}
		stopped = true
		exit = ab
		switch ab.kind {
		case ctlBreak, ctlReturn, ctlFail, ctlEscape:
			return false, nil
		}
		return nil, ab // it unwinds the Seq's frames
	}}
	_, ab := in.callValue(fr, s, seq, []Value{yield})
	over = true
	if exit != nil {
		if exit.kind == ctlBreak {
			exit = nil
		}
		if ab == nil || ab == exit {
			return exit
		}
	}
	return ab
}

// seqIteration is one iteration of s's body with v bound: nil to go on,
// a ctlBreak when the body broke out, else the body's exit.
func (in *interp) seqIteration(fr *frame, s *syntax.RangeStmt, v Value) *abrupt {
	if ab := in.charge(fr, s, 1); ab != nil {
		return ab
	}
	if ab := in.loopBind(fr, s.Value, v); ab != nil {
		return ab
	}
	if ab := in.iteration(fr, s.Body); ab != nil && ab.kind != ctlContinue {
		return ab
	}
	return in.backEdge(fr, s)
}
