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
import testing
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

func unexited(s int) Option[func()] { None }

func tally() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: tallyStep, After: untimed, Work: unworked, Exit: unexited} }

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

func dropped(room int, n int) bool needs Scheduler {
	let r = (room%4+4)%4 + 1
	let k = (n%10+10)%10 + 1
	let a = actor.StartMachine(tally(), 1, func(o int) {})
	let mut got = []actor.Change[int]{}
	for c in actor.Changes(a, r) {
		if len(got) == 0 {
			for _ in 0..k {
				a.Send(1) catch {
				case actor.Stopped:
				}
			}
			let _ = a.State()
			a.Stop()
		}
		got = got.Append(c)
	}
	let mut kept = []int{}
	let mut missed = 0
	for i in 1..k+1 {
		if len(kept) >= r {
			missed = missed + len(kept)
			kept = []int{}
		}
		kept = kept.Append(i)
	}
	let mut want = []actor.Change[int]{actor.Changed[int]{State: 0}}
	if missed > 0 {
		want = want.Append(actor.Overflowed{Missed: missed})
	}
	for s in kept {
		want = want.Append(actor.Changed[int]{State: s})
	}
	if len(got) != len(want) {
		return false
	}
	for i in 0..len(got) {
		if got[i] != want[i] {
			return false
		}
	}
	true
}

func ChangesCountsWhatItDrops(room int, n int) bool {
	with context { Scheduler: time.NewManual(0) } {
		dropped(room, n)
	}
}

func changesStop(k int) bool needs Scheduler {
	if k < 1 {
		return true
	}
	let a = actor.StartMachine(tally(), 1, func(o int) {})
	a.Stop()
	let mut calls = 0
	let s = actor.Changes(a, 2)
	s(func(c actor.Change[int]) bool {
		calls = calls + 1
		calls < k
	})
	calls == 1
}

func ActorChangesStops(k int) bool {
	with context { Scheduler: time.NewManual(0) } {
		changesStop(k)
	}
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

test ActorActorLatestChangesStops(signals []int) {
	with context { Scheduler: time.NewManual(0) } {
		for k in 1..4 {
			let a = sent(signals)
			let want = a.State()
			a.Stop()
			let mut calls = 0
			let mut got = 0 - 1
			let s = a.LatestChanges()
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

test ActorActorChangesStops(k int) { assert ActorChangesStops(k) }

test ActorChangesCountsWhatItDrops(room int, n int) { assert ChangesCountsWhatItDrops(room, n) }

test ActorRejectKeepsState(signals []int, n int) { assert RejectKeepsState(signals, n) }

test ActorDoneIsFinal(signals []int, e int) { assert DoneIsFinal(signals, e) }

type Paces { Opened time.Time, Count int, Wrong int }

let Pace = 5ms

func paceAt(at time.Time) Paces { Paces{Opened: at, Count: 0, Wrong: 0} }

func paceStep(p Paces, at time.Time) Decision[Paces, Never] {
	let wrong = if at.Equal(p.Opened.Add(Pace)) { p.Wrong } else { p.Wrong + 1 }
	Enter[Paces]{Next: Paces{Opened: at, Count: p.Count + 1, Wrong: wrong}}
}

func paceEvery(p Paces) Option[machines.Every[time.Time]] { Some{machines.Every[time.Time]{In: Pace, Signal: paceSignal}} }

func paceSignal(at time.Time) time.Time { at }

type Pacer machine[Paces, time.Time] { initial: paceAt, step: paceStep, after: paceEvery }

func pacedOn(m time.Manual, n int) bool needs time.Clock | Scheduler {
	let beats = 1 + ((n % 4) + 4) % 4
	let a = actor.StartMachine(machines.Start(Pacer{}, time.Clock.Now()), 1, func(o Never) {})
	for _ in 0..beats {
		time.Advance(m, Pace)
	}
	let s = actor.WaitFor(a, func(p Paces) bool { p.Count >= beats }) catch {
	case actor.Stopped:
		return false
	}
	a.Stop()
	s.Wrong == 0
}

func EveryFiresWithTheReading(n int) bool {
	let m = time.NewManual(0)
	with context { Scheduler: m, time.Clock: m } { pacedOn(m, n) }
}

test ActorEveryFiresWithTheReading(n int) { assert EveryFiresWithTheReading(n) }

func exitParts[F](x actor.Exit[int, int, F]) (int, int, int) {
	match x {
	case actor.Finished[int, int] as f:
		(0, f.State, f.Output)
	case actor.Halted[int] as h:
		(1, h.State, 0)
	case actor.Defected:
		(2, 0, 0)
	case actor.Failed[F]:
		(3, 0, 0)
	}
}

type Boom { At int }

func boomStep(s int, e int) Decision[int, int] fails Boom {
	if e < 0 {
		return Boom{At: s}
	}
	Enter[int]{Next: s + 1}
}

func boom() Machine[int, int, int, Boom] { Machine[int, int, int, Boom]{Initial: 0, Step: boomStep, After: untimed, Work: unworked, Exit: unexited} }

func failedExit(n int) bool needs Scheduler {
	let k = ((n % 4) + 4) % 4
	let a = actor.StartMachine(boom(), 1, func(o int) {})
	for _ in 0..k {
		a.Send(1) catch {
		case actor.Stopped:
		}
	}
	a.Send(-1) catch {
	case actor.Stopped:
	}
	failedAt(k, actor.ExitOf(a)) && failedAt(k, actor.ExitOf(a))
}

// failedAt is whether x is Failed with the state k.
func failedAt(k int, x actor.Exit[int, int, Boom | Boom]) bool {
	match x {
	case actor.Failed as f:
		match f.Error {
		case Boom as b:
			b.At == k
		}
	case _:
		false
	}
}

func FailedIsExit(n int) bool {
	with context { Scheduler: time.NewManual(0) } {
		failedExit(n)
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

test ActorFailedIsExit(n int) { assert FailedIsExit(n) }

test ActorSelectOncePerChange(xs []int) { assert SelectOncePerChange(xs) }

func logStep(s []int, e int) Decision[[]int, int] { Stay[[]int]{Next: s.Append(e)} }

func logUntimed(s []int) Option[Timeout[int]] { None }

func logUnworked(s []int) Option[func(send func(e int))] { None }

func logUnexited(s []int) Option[func()] { None }

func fed(xs []int) bool needs Scheduler {
	let a = actor.StartMachine(Machine[[]int, int, int]{Initial: []int{}, Step: logStep, After: logUntimed, Work: logUnworked, Exit: logUnexited}, 1, func(o int) {})
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

func crashy() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: crashStep, After: untimed, Work: unworked, Exit: unexited} }

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
	let a = actor.SuperviseDurable(Machine[int, int, int]{Initial: 0, Step: crashStep, After: untimed, Work: unworked, Exit: unexited}, 1, func(o int) {}, tasks.Restart{Limit: limit}, func() Option[int] { cell.Get() }, func(prev int, next int, sig int) {
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

func tellStep(s int, e int) Decision[int, int] {
	match ((e % 3) + 3) % 3 {
	case 0:
		Tell[int, int]{Next: s + 1, Output: s}
	case 1:
		Emit[int, int]{Next: s + 1, Output: -s}
	case _:
		Stay[int]{Next: s + e}
	}
}

func heard(signals []int) bool needs Scheduler {
	let got = sync.NewRef[[]int]([]int{})
	let a = actor.StartMachine(Machine[int, int, int]{Initial: 0, Step: tellStep, After: untimed, Work: unworked, Exit: unexited}, 1, func(o int) {
		got.Set(got.Get().Append(o))
	})
	let mut s = 0
	let mut want = []int{}
	for e in signals {
		let d = tellStep(s, e)
		match d {
		case Emit[int, int] as em:
			want = want.Append(em.Output)
		case Tell[int, int] as tl:
			want = want.Append(tl.Output)
		case _:
		}
		s = machines.Next(s, d)
		let _ = a.Ask(e) catch {
		case actor.Stopped:
			a.Stop()
			return false
		}
	}
	a.Stop()
	got.Get() == want
}

func SinkGetsEachOutput(signals []int) bool {
	with context { Scheduler: time.NewManual(0) } {
		heard(signals)
	}
}

test ActorSinkGetsEachOutput(signals []int) { assert SinkGetsEachOutput(signals) }

func versioned(signals []int) bool needs Scheduler {
	let a = actor.StartMachine(tally(), 1, func(o int) {})
	let _ = a.State()
	let mut want = 1
	for e in signals {
		let d = a.Ask(e) catch {
		case actor.Stopped:
			a.Stop()
			return false
		}
		match d {
		case Done[int]:
			a.Stop()
			return actor.Version(a) == want
		case Ignore, Reject:
		case _:
			want = want + 1
		}
		let (s, v) = actor.Shown(a)
		if v != want || s != a.State() {
			a.Stop()
			return false
		}
	}
	a.Stop()
	true
}

func VersionCountsCommits(signals []int) bool {
	with context { Scheduler: time.NewManual(0) } {
		versioned(signals)
	}
}

test ActorVersionCountsCommits(signals []int) { assert VersionCountsCommits(signals) }

// staged and its helpers restate the machine of the actor stage's law
// (std actor stage_laws.ergo), which this package cannot name.
type staged = stagedOpen{N int, Ticks int} | stagedSaving{N int, Ticks int} | stagedSaved{N int, Ticks int}

type stagedSignal = stagedAdd{N int} | stagedSave{} | stagedStored{} | stagedTick{}

func stagedStep(s staged, e stagedSignal) Decision[staged, int] {
	match (s, e) {
	case (stagedOpen as o, stagedAdd as a):
		Stay[staged]{Next: stagedOpen{N: o.N + a.N, Ticks: o.Ticks}}
	case (stagedOpen as o, stagedTick):
		Enter[staged]{Next: stagedOpen{N: o.N, Ticks: o.Ticks + 1}}
	case (stagedOpen as o, stagedSave):
		Enter[staged]{Next: stagedSaving{N: o.N, Ticks: o.Ticks}}
	case (stagedSaving as v, stagedStored):
		Enter[staged]{Next: stagedSaved{N: v.N, Ticks: v.Ticks}}
	case (_, _):
		Ignore
	}
}

func stagedTimer(s staged) Option[Timeout[stagedSignal]] {
	match s {
	case stagedOpen:
		Some[Timeout[stagedSignal]]{Value: Timeout[stagedSignal]{In: 1s, Signal: stagedTick}}
	case stagedSaving, stagedSaved:
		None
	}
}

func stagedWork(s staged) Option[machines.Work[stagedSignal, Scheduler]] {
	match s {
	case stagedSaving:
		Some[machines.Work[stagedSignal, Scheduler]]{Value: machines.Do[stagedSignal, Scheduler](func() stagedSignal { stagedStored })}
	case stagedOpen, stagedSaved:
		None
	}
}

type stagedLife machine[staged, stagedSignal] {
	initial: stagedOpen{N: 0, Ticks: 0},
	step:    stagedStep,
	after:   stagedTimer,
	work:    stagedWork,
}

func stagedStepOf(k int) testing.Step[stagedSignal] {
	match ((k % 4) + 4) % 4 {
	case 0:
		testing.Do[stagedSignal]{Act: stagedAdd{N: k}}
	case 1:
		testing.Do[stagedSignal]{Act: stagedSave}
	case 2:
		testing.Advance{By: stagedMove(k)}
	case _:
		testing.Settle
	}
}

func stagedMove(k int) duration { duration(int64(((k % 1500) + 1500) % 1500) * int64(1ms)) }

func stagedFolded(acts []int) []staged {
	let mut s staged = stagedOpen{N: 0, Ticks: 0}
	let mut since = 0
	let mut out = []staged{}
	for k in acts {
		match (s, stagedStepOf(k)) {
		case (stagedOpen as o, testing.Do as d):
			match d.Act {
			case stagedAdd as a:
				s = stagedOpen{N: o.N + a.N, Ticks: o.Ticks}
			case stagedSave:
				s = stagedSaved{N: o.N, Ticks: o.Ticks}
			case stagedStored, stagedTick:
			}
		case (stagedOpen as o, testing.Advance as m):
			let waited = since + int(int64(m.By) / int64(1ms))
			s = stagedOpen{N: o.N, Ticks: o.Ticks + waited / 1000}
			since = waited % 1000
		case (_, _):
		}
		out = out.Append(s)
	}
	out
}

func stagedAgrees(acts []int, clock time.Manual) bool needs Scheduler {
	let mut steps = []testing.Step[stagedSignal]{}
	for k in acts {
		steps = steps.Append(stagedStepOf(k)).Append(testing.Settle)
	}
	let a = actor.StartMachine(stagedLife{}, 8, func(o int) {})
	let shows = testing.Play(actor.NewStage(a, clock), steps) catch {
	case testing.Refused:
		return false
	}
	a.Stop()
	let want = stagedFolded(acts)
	let mut same = len(shows) == len(steps)
	for i, s in want {
		same = same && shows[2*i+1] == s
	}
	same
}

test ActorActorStageAgreesWithFold(acts []int) {
	let clock = time.NewManual(0)
	assert with context { Scheduler: clock } { stagedAgrees(acts, clock) }
}

// stagedSettled restates the Stage laws on the stage of a running
// stagedLife after the acts: two settles show one state, and a settled
// stage shows it twice.
func stagedSettled(acts []int, clock time.Manual) bool needs Scheduler {
	let a = actor.StartMachine(stagedLife{}, 8, func(o int) {})
	let st = actor.NewStage(a, clock)
	for k in acts {
		match stagedStepOf(k) {
		case testing.Do as d:
			st.Do(d.Act) catch {
			case testing.Refused:
				return false
			}
		case testing.Advance as m:
			st.Advance(m.By)
		case testing.Settle:
			st.Settle()
		}
	}
	st.Settle()
	let v = st.Show()
	st.Settle()
	let idempotent = st.Show() == v
	let stable = st.Show() == st.Show()
	a.Stop()
	idempotent && stable
}

test TestingStageSettleIsIdempotent(acts []int) {
	let clock = time.NewManual(0)
	assert with context { Scheduler: clock } { stagedSettled(acts, clock) }
}

test TestingStageShowIsStable(acts []int) {
	let clock = time.NewManual(0)
	assert with context { Scheduler: clock } { stagedSettled(acts, clock) }
}

type exitSeen {
	Left(s int),
}

type exitLog { seen sync.Ref[[]int] } implements exitSeen

func Left(l exitLog, s int) {
	l.seen.Update(func(v []int) []int { v.Append(s) })
}

// exitCount is the counter's state and exitTick its signal.
type exitCount { N int }
type exitTick { K int }

func exitStep(c exitCount, t exitTick) Decision[exitCount, int] fails Boom {
	let s = c.N
	let e = t.K
	match ((e % 7) + 7) % 7 {
	case 0:
		Enter[exitCount]{Next: exitCount{N: s + 100}}
	case 1:
		Stay[exitCount]{Next: exitCount{N: s + 1}}
	case 2:
		Ignore
	case 3:
		Reject
	case 4:
		Emit[exitCount, int]{Next: exitCount{N: s + 1000}, Output: s}
	case 5:
		Done[int]{Output: s}
	case _:
		Boom{At: s}
	}
}

func hasExit(s int) bool { s%5 != 4 }

func leave(c exitCount) Option[func() needs exitSeen] {
	let s = c.N
	if !hasExit(s) {
		return None
	}
	Some{func() needs exitSeen { exitSeen.Left(s) }}
}

func exitFirst(i int) exitCount { exitCount{N: i} }

// exitCounter is the counter as a declared machine with an exit entry,
// started from an input, so the conversion (Exit from exit:) and
// machines.Start are under the laws.
type exitCounter machine[exitCount, exitTick] {
	initial: exitFirst,
	step:    exitStep,
	exit:    leave,
}

func noteExit(out []int, s int) []int {
	if hasExit(s) {
		return out.Append(s)
	}
	out
}

func exitsLeft(signals []int) []int {
	let mut s = 0
	let mut out = []int{}
	for e in signals {
		let d = exitStep(exitCount{N: s}, exitTick{K: e}) catch {
		case Boom:
			return noteExit(out, s)
		}
		match d {
		case Enter[exitCount] as en:
			out = noteExit(out, s)
			s = en.Next.N
		case Emit[exitCount, int] as em:
			out = noteExit(out, s)
			s = em.Next.N
		case Stay[exitCount] as st:
			s = st.Next.N
		case Done[int]:
			return noteExit(out, s)
		case _:
		}
	}
	noteExit(out, s)
}

func exitsRan(signals []int, drain bool, durable bool) []int {
	let log = exitLog{seen: sync.NewRef[[]int]([]int{})}
	with context { Scheduler: time.NewManual(0), exitSeen: log } {
		let a = if durable {
			actor.StartDurable(machines.Start(exitCounter{}, 0), 1, func(o int) {}, func() Option[exitCount] { None }, func(prev exitCount, next exitCount, sig exitTick) {})
		} else {
			actor.StartMachine(machines.Start(exitCounter{}, 0), 1, func(o int) {})
		}
		for e in signals {
			a.Send(exitTick{K: e}) catch {
			case actor.Stopped:
			}
		}
		let _ = a.State()
		if drain {
			a.Drain() catch {
			case Boom:
			}
		} else {
			a.Stop() catch {
			case Boom:
			}
		}
	}
	log.seen.Get()
}

test ActorExitSeesLastState(signals []int) { assert exitsRan(signals, false, false) == exitsLeft(signals) }

test ActorExitRunsOncePerEntry(signals []int, drain bool) { assert exitsRan(signals, drain, drain) == exitsLeft(signals) }
`

// actorLawStatements is the statement of each actor law, as the ledger
// writes it.
var actorLawStatements = map[string]string{
	"actor Actor.StopIsFinal":          `StopIsFinal(e E) { Stop() catch { case _: } Send(e) catch { case Stopped: return true } false }`,
	"actor CanIsTheStep":               `CanIsTheStep(signals []int, e int) { with context { Scheduler: time.NewManual(0) } { can(signals, e) } }`,
	"actor EveryFiresWithTheReading":   `EveryFiresWithTheReading(n int) { let m = time.NewManual(0) with context { Scheduler: m, time.Clock: m } { pacedOn(m, n) } }`,
	"actor FeedStepsInOrder":           `FeedStepsInOrder(xs []int) { with context { Scheduler: time.NewManual(0) } { fed(xs) } }`,
	"actor DoneIsFinal":                `DoneIsFinal(signals []int, e int) { with context { Scheduler: time.NewManual(0) } { doneFinal(signals, e) } }`,
	"actor ExitIsShared":               `ExitIsShared(signals []int) { with context { Scheduler: time.NewManual(0) } { drainedExit(signals) } }`,
	"actor FailedIsExit":               `FailedIsExit(n int) { with context { Scheduler: time.NewManual(0) } { failedExit(n) } }`,
	"actor SelectStops":                `SelectStops(xs []int) { selectStops(Select(source(xs), func(x int) int { x % 3 })) }`,
	"actor SelectByStops":              `SelectByStops(xs []int) { selectStops(SelectBy(source(xs), func(x int) int { x % 3 }, func(a int, b int) bool { a == b })) }`,
	"actor SelectOncePerChange":        `SelectOncePerChange(xs []int) { let mut want = []int{} for x in xs { if len(want) == 0 || want[len(want)-1] != x % 3 { want = want.Append(x % 3) } } let mut got = []int{} for v in Select(Seq[int](func(yield func(v int) bool) { for x in xs { if !yield(x) { return } } }), func(x int) int { x % 3 }) { got = got.Append(v) } let mut same = len(got) == len(want) for i in 0..len(got) { same = same && got[i] == want[i] } same }`,
	"actor RejectKeepsState":           `RejectKeepsState(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { rejectKeeps(signals, n) } }`,
	"actor RestartIsFresh":             `RestartIsFresh(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { restarted(signals, n) } }`,
	"actor RestartRecovers":            `RestartRecovers(signals []int, n int) { with context { Scheduler: time.NewManual(0) } { recoveredRun(signals, n) } }`,
	"actor ChangesCountsWhatItDrops":   `ChangesCountsWhatItDrops(room int, n int) { with context { Scheduler: time.NewManual(0) } { dropped(room, n) } }`,
	"actor ActorChangesStops":          `ActorChangesStops(k int) { with context { Scheduler: time.NewManual(0) } { changesStop(k) } }`,
	"actor SinkGetsEachOutput":         `SinkGetsEachOutput(signals []int) { with context { Scheduler: time.NewManual(0) } { heard(signals) } }`,
	"actor VersionCountsCommits":       `VersionCountsCommits(signals []int) { with context { Scheduler: time.NewManual(0) } { versioned(signals) } }`,
	"actor SendsStepInOrder":           `SendsStepInOrder(signals []int) { with context { Scheduler: time.NewManual(0) } { inOrder(signals) } }`,
	"actor ExitSeesLastState":          `ExitSeesLastState(signals []int) { exitsRan(signals, false, false) == exitsLeft(signals) }`,
	"actor ExitRunsOncePerEntry":       `ExitRunsOncePerEntry(signals []int, drain bool) { exitsRan(signals, drain, drain) == exitsLeft(signals) }`,
	"testing Stage.SettleIsIdempotent": `SettleIsIdempotent() { Settle() let v = Show() Settle() Show() == v }`,
	"testing Stage.ShowIsStable":       `ShowIsStable() { Settle() Show() == Show() }`,
	"actor ActorStageAgreesWithFold":   `ActorStageAgreesWithFold(acts []int) { let clock = time.NewManual(0) with context { Scheduler: clock } { stagedAgrees(acts, clock) } }`,
	"actor Actor.LatestChangesStops":   `LatestChangesStops(k int) { Stop() catch { case _: } if k < 1 { return true } let mut calls = 0 let s = LatestChanges() s(func(v S) bool { calls = calls + 1 calls < k }) calls == 1 }`,
}

// TestStdActorLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It is
// the ledger runner the Std laws rule requires for each std law (stdlib.md,
// Std laws 4); it builds and runs because the laws run over the lowered
// std body and the runtime's actor runner, as ergo test runs them.
func TestStdActorLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdactor", actorLawsProbe, actorLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
