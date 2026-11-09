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
`

// actorLawStatements is the statement of each actor law, as the ledger
// writes it.
var actorLawStatements = map[string]string{
	"actor Actor.ChangesStops": `ChangesStops(k int) { if k < 1 { return true } Stop() catch { case _: } let mut calls = 0 let s = Changes() s(func(v S) bool { calls = calls + 1 calls < k }) calls == 1 }`,
	"actor Actor.StopIsFinal":  `StopIsFinal(e E) { Stop() catch { case _: } Send(e) catch { case Stopped: return true } false }`,
	"actor DoneIsFinal":        `DoneIsFinal(signals []int, e int) { with context { Scheduler: time.NewManual(0) } { doneFinal(signals, e) } }`,
	"actor RejectKeepsState":   `RejectKeepsState(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { rejectKeeps(signals, n) } }`,
	"actor SendsStepInOrder":   `SendsStepInOrder(signals []int) { with context { Scheduler: time.NewManual(0) } { inOrder(signals) } }`,
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
