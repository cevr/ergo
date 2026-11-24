package lower

// IR builder: amortized back-edge polls (REUSE POLL-AMORT). A loop of a
// polling frame ends each iteration in its interruption point, the
// task's Check (runtime.md, I1; loopPoll, ir_fail.go). A quiet loop, one
// whose iterations make no call that can wait, checks once every
// pollEvery back edges instead:
//
//	pc -= 1
//	if pc == 0 { pc = pollEvery; env.task.Check() }
//
// with pc one counter of the frame, set to pollEvery before the outermost
// quiet loop of a nest and shared by every quiet loop inside it, at each
// back edge and each continue (which polls before it leaves the
// iteration). So a stop is seen within pollEvery back edges of the nest,
// however its loops are nested (the law the counted property
// TestIRPollAmortBound checks): a counter per loop would let an inner
// loop that runs fewer than pollEvery iterations per outer iteration
// never poll.
//
// A loop is quiet when its body meets an env-free function body's rules
// (pure.go, quietLoop): its calls outside function literals take no
// environment, cannot wait or start a task, and have no interruption
// point of their own. A typed failure is a returned outcome, not an
// effect on the task. The body converts no value into a Go interface
// and owns no value whose release it would run through the task.
//
// Any other loop keeps a Check at every back edge, as the reference
// lowering pollAmortLowering "each" gives every loop. The emitter's text body
// polls every iteration; a quiet loop's iterations do no I/O and touch no
// other task's state, so the extra iterations a stop may run before it is
// seen show only in the time they take.
//
// Prior art: the BEAM charges a reduction per call and preempts at a
// count (4000 reductions), not at each loop step; GHC's -fno-omit-yields
// and Go's own pre-1.14 cooperative preemption polled at function entry
// only, so a call-free loop never yielded. Go 1.14 answered that with
// asynchronous preemption by signal, which cooperative cancellation (a
// context) does not get: a hand Go loop checks ctx.Err() per iteration or
// per batch, by hand. This is that batch, decided by the compiler.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/syntax"
)

// pollAmortLowering selects the back-edge poll: "amortized" uses the
// env-free proof, "pure" is the former pure-only admission, and "each"
// writes Check at every back edge. A test sets it; a whole build selects
// it with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.pollAmortLowering=each.
var pollAmortLowering = "amortized"

// SetPollAmortLoweringForTest selects pollAmortLowering and returns a
// function that restores the previous choice.
func SetPollAmortLoweringForTest(mode string) func() {
	old := pollAmortLowering
	pollAmortLowering = mode
	return func() { pollAmortLowering = old }
}

// PollEvery is the back edges a quiet nest runs between two Checks: the
// bound a stop waits at most (TestIRPollAmortBound). 1024 by count
// (POLL-AMORT receipt): the counter's share of a one-statement loop is two
// instructions an iteration, the Check's a thousandth of one.
const PollEvery = pollEvery

const pollEvery = 1024

// pollAmort is the counter of the quiet nest being built (irBuilder.amort).
type pollAmort struct {
	ctr int   // the counter's local, -1 until a loop of the nest polls through it
	pos irPos // the outermost loop's position, where the counter is defined
}

// quietLoop reports whether a loop body meets an env-free function
// body's rules (pure.go): none of its calls, conversions, or releases
// needs the task or the environment.
func (b *irBuilder) quietLoop(body *syntax.BlockStmt) bool {
	info := b.l.info
	callees, ok := pureCalls(info, body)
	if !ok || ownsValues(info, body) {
		return false
	}
	for _, f := range callees {
		if pollAmortLowering == "pure" {
			if !b.l.isPure(f) {
				return false
			}
		} else if !b.l.isEnvFree(f) {
			return false
		}
	}
	return true
}

// amortize rewrites the polls sites, the back-edge and continue polls of
// one quiet loop, in place, into the nest counter's amortized poll.
func (b *irBuilder) amortize(sites []*irStmt) {
	a := b.amort
	if len(sites) > 0 && a.ctr < 0 {
		a.ctr = b.temp("pc", "int", a.pos)
	}
	for _, s := range sites {
		p := s.Pos
		check := *s
		ctr := func() *irExpr { return &irExpr{K: "local", Pos: p, I: a.ctr} }
		*s = irStmt{K: "block", Pos: p, End: p, Body: []*irStmt{
			{K: "set", Pos: p, Local: a.ctr, Op: "-", X: &irExpr{K: "const", Pos: p, T: "int", V: "1"}},
			{K: "if", Pos: p, End: p, X: &irExpr{K: "bin", Pos: p, Op: "==", X: ctr(), Y: &irExpr{K: "const", Pos: p, T: "int", V: "0"}}, Then: []*irStmt{
				{K: "set", Pos: p, Local: a.ctr, X: pollEveryConst(p)},
				&check,
			}},
		}}
	}
}

func pollEveryConst(p irPos) *irExpr {
	return &irExpr{K: "const", Pos: p, T: "int", V: strconv.Itoa(pollEvery)}
}
