package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// actorLawsProbe restates the laws of std actor as tests of the same name
// (the Std laws ledger, docs/spec/stdlib.md). The laws run a tally actor
// on a time.Manual that no one advances: tally has no timer.
const actorLawsProbe = `import actor
import machines
import sync
import tasks
import time

func tallyStep(s int, e int) Decision[int, int] {
	match ((e % 6) + 6) % 6 {
	case 0:
		Enter[int]{Next: s + e}
	case 1:
		Stay[int]{Next: s + 1}
	case 2:
		Ignore
	case 3:
		Reject
	case 4:
		Emit[int, int]{Next: s - 1, Output: s}
	case _:
		Done[int]{Output: s}
	}
}

func untimed(s int) Option[Timeout[int]] { None }

func unworked(s int) Option[func(send func(e int))] { None }

func tally() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: tallyStep, After: untimed, Work: unworked} }

func folded(signals []int) (int, bool) {
	let mut s = 0
	for e in signals {
		let d = tallyStep(s, e)
		match d {
		case Done[int]:
			return (s, true)
		case _:
			s = machines.Next(s, d)
		}
	}
	(s, false)
}

func sent(signals []int) actor.Actor[int, int, Decision[int, int]] needs Scheduler {
	let a = actor.StartMachine(tally(), 1, func(o int) {})
	for e in signals {
		a.Send(e) catch {
		case actor.Stopped:
		}
	}
	a
}

func inOrder(signals []int) bool needs Scheduler {
	let a = actor.StartMachine(tally(), 1, func(o int) {})
	let mut refused = false
	for e in signals {
		a.Send(e) catch {
		case actor.Stopped:
			refused = true
		}
	}
	let s = a.State()
	a.Stop()
	let (want, done) = folded(signals)
	s == want && (done || !refused)
}

func rejectKeeps(signals []int, n int) bool needs Scheduler {
	let a = sent(signals)
	let before = a.State()
	let (_, done) = folded(signals)
	let d = a.Ask(6*(n%1000) + 3) catch {
	case actor.Stopped:
		a.Stop()
		return done
	}
	let after = a.State()
	a.Stop()
	match d {
	case Reject:
		after == before && !done
	case _:
		false
	}
}

func doneFinal(signals []int, e int) bool needs Scheduler {
	let a = sent(signals)
	let before = a.State()
	let d = a.Ask(5) catch {
	case actor.Stopped:
		a.Stop()
		return true
	}
	let mut final = false
	match d {
	case Done[int] as dn:
		final = dn.Output == before
	case _:
	}
	a.Send(e) catch {
	case actor.Stopped:
		final = final && a.State() == before
		a.Stop()
		return final
	}
	a.Stop()
	false
}

func SendsStepInOrder(signals []int) bool {
	with context { Scheduler: time.NewManual(0) } {
		inOrder(signals)
	}
}

func RejectKeepsState(signals []int, n int) bool {
	with context { Scheduler: time.NewManual(0) } {
		rejectKeeps(signals, n)
	}
}

func DoneIsFinal(signals []int, e int) bool {
	with context { Scheduler: time.NewManual(0) } {
		doneFinal(signals, e)
	}
}

test ActorActorStopIsFinal(signals []int, e int) {
	with context { Scheduler: time.NewManual(0) } {
		let a = sent(signals)
		a.Stop()
		let mut refused = false
		a.Send(e) catch {
		case actor.Stopped:
			refused = true
		}
		assert refused
	}
}

test ActorActorChangesStops(signals []int) {
	with context { Scheduler: time.NewManual(0) } {
		for k in 1..4 {
			let a = sent(signals)
			let want = a.State()
			a.Stop()
			let mut calls = 0
			let mut got = 0 - 1
			let s = a.Changes()
			s(func(v int) bool {
				calls = calls + 1
				got = v
				calls < k
			})
			assert calls == 1 && got == want
		}
	}
}

test ActorSendsStepInOrder(signals []int) { assert SendsStepInOrder(signals) }

test ActorRejectKeepsState(signals []int, n int) { assert RejectKeepsState(signals, n) }

test ActorDoneIsFinal(signals []int, e int) { assert DoneIsFinal(signals, e) }

func exitParts(x actor.Exit[int, int]) (int, int, int) {
	match x {
	case actor.Finished[int, int] as f:
		(0, f.State, f.Output)
	case actor.Halted[int] as h:
		(1, h.State, 0)
	case actor.Defected:
		(2, 0, 0)
	}
}

func drainedExit(signals []int) bool needs Scheduler {
	let a = sent(signals)
	a.Drain()
	let (k1, s1, o1) = exitParts(actor.ExitOf(a))
	let (k2, s2, o2) = exitParts(actor.ExitOf(a))
	let (want, done) = folded(signals)
	let mut kind = 1
	if done {
		kind = 0
	}
	k1 == k2 && s1 == s2 && o1 == o2 && k1 == kind && s1 == want && (!done || o1 == want)
}

func ExitIsShared(signals []int) bool {
	with context { Scheduler: time.NewManual(0) } {
		drainedExit(signals)
	}
}

func SelectOncePerChange(xs []int) bool {
	let mut want = []int{}
	for x in xs {
		if len(want) == 0 || want[len(want)-1] != x % 3 {
			want = want.Append(x % 3)
		}
	}
	let mut got = []int{}
	for v in actor.Select(Seq[int](func(yield func(v int) bool) {
		for x in xs {
			if !yield(x) {
				return
			}
		}
	}), func(x int) int { x % 3 }) {
		got = got.Append(v)
	}
	let mut same = len(got) == len(want)
	for i in 0..len(got) {
		same = same && got[i] == want[i]
	}
	same
}

func source(xs []int) Seq[int] {
	Seq[int](func(yield func(v int) bool) {
		for x in xs {
			if !yield(x) {
				return
			}
		}
	})
}

func selectStops(s Seq[int]) bool {
	let mut n = 0
	s(func(v int) bool {
		n = n + 1
		true
	})
	for k in 1..n+2 {
		let mut calls = 0
		s(func(v int) bool {
			calls = calls + 1
			calls < k
		})
		if calls != (if k < n { k } else { n }) {
			return false
		}
	}
	true
}

test ActorSelectStops(xs []int) { assert selectStops(actor.Select(source(xs), func(x int) int { x % 3 })) }

test ActorSelectByStops(xs []int) {
	assert selectStops(actor.SelectBy(source(xs), func(x int) int { x % 3 }, func(a int, b int) bool { a == b }))
}

test ActorExitIsShared(signals []int) { assert ExitIsShared(signals) }

test ActorSelectOncePerChange(xs []int) { assert SelectOncePerChange(xs) }

func logStep(s []int, e int) Decision[[]int, int] { Stay[[]int]{Next: s.Append(e)} }

func logUntimed(s []int) Option[Timeout[int]] { None }

func logUnworked(s []int) Option[func(send func(e int))] { None }

func fed(xs []int) bool needs Scheduler {
	let a = actor.StartMachine(Machine[[]int, int, int]{Initial: []int{}, Step: logStep, After: logUntimed, Work: logUnworked}, 1, func(o int) {})
	actor.Feed(a, source(xs)) catch {
	case actor.Stopped:
		return false
	}
	let s = actor.WaitFor(a, func(s []int) bool { len(s) >= len(xs) }) catch {
	case actor.Stopped:
		return false
	}
	a.Stop()
	let mut same = len(s) == len(xs)
	for i in 0..len(s) {
		same = same && s[i] == xs[i]
	}
	same
}

func FeedStepsInOrder(xs []int) bool {
	with context { Scheduler: time.NewManual(0) } {
		fed(xs)
	}
}

func can(signals []int, e int) bool needs Scheduler {
	let a = sent(signals)
	let (_, done) = folded(signals)
	let s = a.State()
	let c = actor.Can(a, tallyStep, e)
	let kept = a.State() == s
	a.Stop()
	let after = actor.Can(a, tallyStep, e)
	kept && !after && (if done { !c } else { c == machines.Can(tallyStep, s, e) })
}

func CanIsTheStep(signals []int, e int) bool {
	with context { Scheduler: time.NewManual(0) } {
		can(signals, e)
	}
}

test ActorFeedStepsInOrder(xs []int) { assert FeedStepsInOrder(xs) }

test ActorCanIsTheStep(signals []int, e int) { assert CanIsTheStep(signals, e) }

func crashes(e int) bool { ((e % 5) + 5) % 5 == 4 }

func crashStep(s int, e int) Decision[int, int] {
	if crashes(e) {
		Unreachable("an injected defect")
	}
	tallyStep(s, e)
}

func crashy() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: crashStep, After: untimed, Work: unworked} }

func supervised(a actor.Actor[int, int, Decision[int, int]], signals []int, limit int, kept bool) bool needs Scheduler {
	let mut s = 0
	let mut restarts = 0
	let mut ended = false
	let mut defected = false
	for e in signals {
		let _ = a.Ask(e) catch {
		case actor.Stopped:
			Ignore
		}
		if !ended {
			if crashes(e) {
				if restarts < limit {
					restarts = restarts + 1
					if !kept {
						s = 0
					}
				} else {
					ended = true
					defected = true
				}
			} else {
				let d = tallyStep(s, e)
				match d {
				case Done[int]:
					ended = true
				case _:
					s = machines.Next(s, d)
				}
			}
		}
	}
	let same = a.State() == s && actor.Restarts(a) == restarts
	if defected {
		match actor.ExitOf(a) {
		case actor.Defected:
			return same
		case _:
			return false
		}
	}
	a.Stop()
	same
}

func restarted(signals []int, n int) bool needs Scheduler {
	let limit = ((n % 3) + 3) % 3
	supervised(actor.Supervise(crashy(), 1, func(o int) {}, tasks.Restart{Limit: limit}), signals, limit, false)
}

func recoveredRun(signals []int, n int) bool needs Scheduler {
	let limit = ((n % 3) + 3) % 3
	let cell = sync.NewRef[Option[int]](None)
	let a = actor.SuperviseDurable(Machine[int, int, int]{Initial: 0, Step: crashStep, After: untimed, Work: unworked}, 1, func(o int) {}, tasks.Restart{Limit: limit}, func() Option[int] { cell.Get() }, func(prev int, next int, sig int) {
		cell.Set(Some[int]{Value: next})
	})
	supervised(a, signals, limit, true)
}

func RestartIsFresh(signals []int, n int) bool {
	with context { Scheduler: time.NewManual(0) } {
		restarted(signals, n)
	}
}

func RestartRecovers(signals []int, n int) bool {
	with context { Scheduler: time.NewManual(0) } {
		recoveredRun(signals, n)
	}
}

test ActorRestartIsFresh(signals []int, n int) { assert RestartIsFresh(signals, n) }

test ActorRestartRecovers(signals []int, n int) { assert RestartRecovers(signals, n) }
`

// actorLawStatements is the statement of each actor law, as the ledger
// writes it.
var actorLawStatements = map[string]string{
	"actor Actor.ChangesStops":  `ChangesStops(k int) { if k < 1 { return true } Stop() catch { case _: } let mut calls = 0 let s = Changes() s(func(v S) bool { calls = calls + 1 calls < k }) calls == 1 }`,
	"actor Actor.StopIsFinal":   `StopIsFinal(e E) { Stop() catch { case _: } Send(e) catch { case Stopped: return true } false }`,
	"actor CanIsTheStep":        `CanIsTheStep(signals []int, e int) { with context { Scheduler: time.NewManual(0) } { can(signals, e) } }`,
	"actor FeedStepsInOrder":    `FeedStepsInOrder(xs []int) { with context { Scheduler: time.NewManual(0) } { fed(xs) } }`,
	"actor DoneIsFinal":         `DoneIsFinal(signals []int, e int) { with context { Scheduler: time.NewManual(0) } { doneFinal(signals, e) } }`,
	"actor ExitIsShared":        `ExitIsShared(signals []int) { with context { Scheduler: time.NewManual(0) } { drainedExit(signals) } }`,
	"actor SelectStops":         `SelectStops(xs []int) { selectStops(Select(source(xs), func(x int) int { x % 3 })) }`,
	"actor SelectByStops":       `SelectByStops(xs []int) { selectStops(SelectBy(source(xs), func(x int) int { x % 3 }, func(a int, b int) bool { a == b })) }`,
	"actor SelectOncePerChange": `SelectOncePerChange(xs []int) { let mut want = []int{} for x in xs { if len(want) == 0 || want[len(want)-1] != x % 3 { want = want.Append(x % 3) } } let mut got = []int{} for v in Select(Seq[int](func(yield func(v int) bool) { for x in xs { if !yield(x) { return } } }), func(x int) int { x % 3 }) { got = got.Append(v) } let mut same = len(got) == len(want) for i in 0..len(got) { same = same && got[i] == want[i] } same }`,
	"actor RejectKeepsState":    `RejectKeepsState(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { rejectKeeps(signals, n) } }`,
	"actor RestartIsFresh":      `RestartIsFresh(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { restarted(signals, n) } }`,
	"actor RestartRecovers":     `RestartRecovers(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { recoveredRun(signals, n) } }`,
	"actor SendsStepInOrder":    `SendsStepInOrder(signals []int) { with context { Scheduler: time.NewManual(0) } { inOrder(signals) } }`,
}

// TestStdActorLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It is
// the ledger runner the Std laws rule requires for each std law (stdlib.md,
// Std laws 4); it builds and runs because the laws run over the lowered
// std body and the runtime's actor runner, as ergo test runs them.
func TestStdActorLawsRun(t *testing.T) {
	if laws := lawProbe(t, "stdactor", actorLawsProbe, actorLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
