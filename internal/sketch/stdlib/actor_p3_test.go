package stdlib_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/sketch/check"
)

func TestActorP3BorrowChecksMutCall(t *testing.T) {
	goodSrc := `import actor
import time

type Box { Count int, Items []int }

type BadErr {}

func Use() int fails actor.Stopped | actor.NoReply | BadErr needs time.Scheduler {
	let ag = actor.StartAgent(Box{Count: 1, Items: []int{}})
	try actor.Call(ag, func(s mut Box) int fails BadErr {
		s.Count = s.Count + 2
		s.Items = s.Items.Append(9)
		s.Count
	})
}
`
	if _, diags := check.CheckSource(goodSrc); len(diags) > 0 {
		t.Fatalf("expected valid borrow Call to check cleanly, got: %+v", diags)
	}

	escapeAliasSrc := `import actor
import time

type Box { Count int, Items []int }

func Bump(a mut int, b mut int) int {
	a + b
}

func Use() int fails actor.Stopped | actor.NoReply needs time.Scheduler {
	let ag = actor.StartAgent(Box{Count: 1, Items: []int{}})
	try actor.Call(ag, func(s mut Box) int {
		Bump(&s.Count, &s.Count)
	})
}
`
	if _, diags := check.CheckSource(escapeAliasSrc); len(diags) == 0 {
		t.Fatal("expected aliasing mutable borrows inside actor.Call callback to be rejected by checker, got zero diagnostics")
	}

	escapeMutCaptureSrc := `import actor
import sync
import time

func Use() int fails actor.Stopped | actor.NoReply needs time.Scheduler {
	let mut leaked = 0
	let ag = actor.StartAgent(1)
	try actor.Call(ag, func(s mut int) int {
		leaked = s
		s = s + 1
		s
	})
}
`
	if _, diags := check.CheckSource(escapeMutCaptureSrc); len(diags) == 0 {
		t.Fatal("expected escaping write to caller let mut inside actor.Call callback to fail check, got zero diagnostics")
	}
}

const actorP3NativeMainSrc = `import actor
import io
import machines
import option
import schedule
import sync
import task
import tasks
import time

type testErr { Code int }

type reqMsg =
	| ReqAdd{N int, Reply actor.Reply[int, testErr]}
	| ReqFail{Reply actor.Reply[int, testErr]}
	| ReqForget{Reply actor.Reply[int, testErr]}
	| ReqDefer{Reply actor.Reply[int, testErr]}
	| ReqFlushDeferred{}
	| ReqHalt{}
	| ReqCrash{}

type reqState {
	Val     int,
	Pending option.Option[actor.Reply[int, testErr]],
}

func triggerDefect() int {
	Unreachable("boom")
	0
}

func testAddressAndReply() bool needs time.Scheduler {
	let proc = actor.Start[reqState, reqMsg, testErr](reqState{Val: 0, Pending: option.None}, 16, func(s reqState, msg reqMsg) actor.Next[reqState] fails testErr {
		match msg {
		case ReqAdd as r:
			let nextVal = s.Val + r.N
			actor.Respond(r.Reply, nextVal)
			actor.Continue[reqState]{State: reqState{..s, Val: nextVal}}
		case ReqFail as r:
			actor.Fail(r.Reply, testErr{Code: 42})
			actor.Continue[reqState]{State: s}
		case ReqForget:
			actor.Continue[reqState]{State: s}
		case ReqDefer as r:
			actor.Defer(r.Reply)
			actor.Continue[reqState]{State: reqState{..s, Pending: option.Some[actor.Reply[int, testErr]]{Value: r.Reply}}}
		case ReqFlushDeferred:
			match s.Pending {
			case option.Some[actor.Reply[int, testErr]] as p:
				actor.Respond(p.Value, s.Val + 100)
			case option.None:
			}
			actor.Continue[reqState]{State: reqState{..s, Pending: option.None}}
		case ReqHalt:
			actor.Halt
		case ReqCrash:
			return testErr{Code: 99}
		}
	})
	let addr = proc.Addr()
	let copyAddr = addr

	let r1 = actor.Ask(addr, func(r actor.Reply[int, testErr]) reqMsg { ReqAdd{N: 7, Reply: r} }) catch {
	case _:
		return false
	}
	if r1 != 7 {
		return false
	}

	let mut gotTypedErr = false
	let _ = actor.Ask(copyAddr, func(r actor.Reply[int, testErr]) reqMsg { ReqFail{Reply: r} }) catch {
	case testErr as te:
		gotTypedErr = te.Code == 42
		0
	case _:
		return false
	}
	if !gotTypedErr {
		return false
	}

	let mut gotNoReply = false
	let _ = actor.Ask(addr, func(r actor.Reply[int, testErr]) reqMsg { ReqForget{Reply: r} }) catch {
	case actor.NoReply:
		gotNoReply = true
		0
	case _:
		return false
	}
	if !gotNoReply {
		return false
	}

	let armedLatch = sync.NewLatch()
	let deferredProc = actor.Start[reqState, reqMsg, testErr](reqState{Val: 7, Pending: option.None}, 16, func(s reqState, msg reqMsg) actor.Next[reqState] fails testErr {
		match msg {
		case ReqAdd as r:
			let nextVal = s.Val + r.N
			actor.Respond(r.Reply, nextVal)
			actor.Continue[reqState]{State: reqState{..s, Val: nextVal}}
		case ReqFail as r:
			actor.Fail(r.Reply, testErr{Code: 42})
			actor.Continue[reqState]{State: s}
		case ReqForget:
			actor.Continue[reqState]{State: s}
		case ReqDefer as r:
			actor.Defer(r.Reply)
			let _ = sync.Open(armedLatch)
			actor.Continue[reqState]{State: reqState{..s, Pending: option.Some[actor.Reply[int, testErr]]{Value: r.Reply}}}
		case ReqFlushDeferred:
			match s.Pending {
			case option.Some[actor.Reply[int, testErr]] as p:
				actor.Respond(p.Value, s.Val + 100)
			case option.None:
			}
			actor.Continue[reqState]{State: reqState{..s, Pending: option.None}}
		case ReqHalt:
			actor.Halt
		case ReqCrash:
			return testErr{Code: 99}
		}
	})
	let defAddr1 = deferredProc.Addr()
	let wDef1 = task.Go() {
		actor.Ask(defAddr1, func(r actor.Reply[int, testErr]) reqMsg { ReqDefer{Reply: r} }) catch {
		case _:
			-1
		}
	}
	sync.Await(armedLatch)
	actor.Send(defAddr1, ReqFlushDeferred) catch {
	case actor.Stopped:
		return false
	}
	if wDef1.Wait() != 107 {
		return false
	}
	actor.Stop(deferredProc) catch {
	case _:
		return false
	}

	let abortLatch = sync.NewLatch()
	let procDef = actor.Start[reqState, reqMsg, testErr](reqState{Val: 5, Pending: option.None}, 16, func(s reqState, msg reqMsg) actor.Next[reqState] fails testErr {
		match msg {
		case ReqAdd as r:
			actor.Respond(r.Reply, s.Val + r.N)
			actor.Continue[reqState]{State: reqState{..s, Val: s.Val + r.N}}
		case ReqFail as r:
			actor.Fail(r.Reply, testErr{Code: 1})
			actor.Continue[reqState]{State: s}
		case ReqForget:
			actor.Continue[reqState]{State: s}
		case ReqDefer as r:
			actor.Defer(r.Reply)
			let _ = sync.Open(abortLatch)
			actor.Continue[reqState]{State: reqState{..s, Pending: option.Some[actor.Reply[int, testErr]]{Value: r.Reply}}}
		case ReqFlushDeferred:
			match s.Pending {
			case option.Some[actor.Reply[int, testErr]] as p:
				actor.Respond(p.Value, s.Val + 100)
			case option.None:
			}
			actor.Continue[reqState]{State: reqState{..s, Pending: option.None}}
		case ReqHalt:
			actor.Halt
		case ReqCrash:
			return testErr{Code: 99}
		}
	})
	let defAddr = procDef.Addr()
	let wDef2 = task.Go() {
		let mut sawNoReply = false
		let _ = actor.Ask(defAddr, func(r actor.Reply[int, testErr]) reqMsg { ReqDefer{Reply: r} }) catch {
		case actor.NoReply:
			sawNoReply = true
			0
		case _:
			0
		}
		sawNoReply
	}
	sync.Await(abortLatch)
	actor.Stop(procDef) catch {
	case _:
		return false
	}
	let mut stoppedAfterExit = false
	let _ = actor.Ask(defAddr, func(r actor.Reply[int, testErr]) reqMsg { ReqAdd{N: 1, Reply: r} }) catch {
	case actor.Stopped:
		stoppedAfterExit = true
		0
	case _:
		return false
	}
	let mut cleanHalt = false
	actor.Send(addr, ReqHalt) catch {
	case actor.Stopped:
		return false
	}
	match actor.PeerExit(proc) {
	case actor.NormalExit:
		cleanHalt = true
	case actor.AbnormalExit:
	}
	wDef2.Wait() && stoppedAfterExit && cleanHalt
}

type agentState { Count int, Items []int }

func testAgentAndBorrows() bool needs time.Scheduler | io.Console {
	let ag = actor.StartAgent(agentState{Count: 10, Items: []int{}})
	let v1 = actor.Call(ag, func(s mut agentState) int fails testErr {
		s.Count = s.Count + 5
		s.Items = s.Items.Append(1)
		s.Count
	}) catch {
	case _:
		io.Console.Println("agent v1 catch")
		return false
	}
	if v1 != 15 {
		io.Console.Println("agent v1=\(v1)")
		return false
	}

	let mut errObserved = false
	let _ = actor.Call(ag, func(s mut agentState) int fails testErr {
		s.Count = 999
		s.Items = s.Items.Append(2)
		return testErr{Code: 9}
	}) catch {
	case testErr as te:
		errObserved = te.Code == 9
		0
	case _:
		io.Console.Println("agent err catch other")
		return false
	}
	let snap = actor.Snapshot(ag)
	if !errObserved || snap.Count != 15 || snap.Items.Len() != 1 {
		io.Console.Println("agent errObserved=\(errObserved) count=\(snap.Count) len=\(snap.Items.Len())")
		return false
	}

	actor.Cast(ag, func(s mut agentState) {
		s.Count = s.Count + 2
	}) catch {
	case actor.Stopped:
		io.Console.Println("agent cast stopped")
		return false
	}
	let v2 = actor.Call(ag, func(s mut agentState) int fails testErr { s.Count }) catch {
	case _:
		io.Console.Println("agent v2 catch")
		return false
	}
	if v2 != 17 {
		io.Console.Println("agent v2=\(v2)")
		return false
	}

	actor.HaltAgent(ag)
	let mut stoppedAfterHalt = false
	let _ = actor.Call(ag, func(s mut agentState) int fails testErr { s.Count }) catch {
	case actor.Stopped:
		stoppedAfterHalt = true
		0
	case _:
		io.Console.Println("agent after-halt unexpected")
		return false
	}

	let crashAg = actor.StartAgent(0)
	let mut gotCrashNoReply = false
	let _ = actor.Call(crashAg, func(s mut int) int fails testErr {
		let _ = s
		triggerDefect()
	}) catch {
	case actor.NoReply:
		gotCrashNoReply = true
		0
	case _:
		io.Console.Println("agent crash unexpected")
		return false
	}
	let mut stoppedAfterCrash = false
	let _ = actor.Call(crashAg, func(s mut int) int fails testErr {
		let _ = s
		1
	}) catch {
	case actor.Stopped:
		stoppedAfterCrash = true
		0
	case _:
		io.Console.Println("agent post-crash unexpected")
		return false
	}
	if !(stoppedAfterHalt && gotCrashNoReply && stoppedAfterCrash) {
		io.Console.Println("agent flags halt=\(stoppedAfterHalt) crash=\(gotCrashNoReply) post=\(stoppedAfterCrash)")
	}
	stoppedAfterHalt && gotCrashNoReply && stoppedAfterCrash
}

type supMsg =
	| MsgCrash{}
	| MsgHalt{}
	| MsgPing{Reply actor.Reply[string, testErr]}

func spawnWorker(name string, log sync.Ref[[]string], latestAddr sync.Ref[option.Option[actor.Address[supMsg]]]) actor.Target needs time.Scheduler {
	log.Update(func(xs []string) []string { xs.Append("start:" + name) })
	let p = actor.Start[bool, supMsg, testErr](true, 16, func(s bool, msg supMsg) actor.Next[bool] fails testErr {
		match msg {
		case MsgCrash:
			let _ = triggerDefect()
			actor.Continue[bool]{State: s}
		case MsgHalt:
			actor.Halt
		case MsgPing as r:
			actor.Respond(r.Reply, name)
			actor.Continue[bool]{State: s}
		}
	})
	latestAddr.Set(option.Some[actor.Address[supMsg]]{Value: p.Addr()})
	actor.OnPeerEnd(p, func() {
		log.Update(func(xs []string) []string { xs.Append("stop:" + name) })
	}) catch {
	case actor.Stopped:
		let _ = 0
	}
	p
}

func testDynamicSupervisorStrategies() bool {
	let clk = time.NewManual(0)
	with context { time.Scheduler: clk, time.Clock: clk } {
		let log = sync.NewRef[[]string]([]string{})
		let addrA = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let addrB = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let addrC = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let polRest = tasks.Supervision{
			Strategy:  tasks.RestForOne,
			Intensity: tasks.Intensity{Max: 5, Period: 60s},
			Backoff:   schedule.Spaced(0s),
		}
		let supRest = actor.SuperviseSet(
			[]actor.ChildSpec{
				actor.PermanentChild("A", func() actor.Target needs time.Scheduler { spawnWorker("A", log, addrA) }),
				actor.PermanentChild("B", func() actor.Target needs time.Scheduler { spawnWorker("B", log, addrB) }),
				actor.PermanentChild("C", func() actor.Target needs time.Scheduler { spawnWorker("C", log, addrC) }),
			},
			polRest,
		) catch {
		case _:
			return false
		}
		let ids = actor.Children(supRest)
		if ids.Len() != 3 {
			return false
		}
		log.Set([]string{})
		match addrB.Get() {
		case option.Some[actor.Address[supMsg]] as ab:
			actor.Send(ab.Value, MsgCrash) catch {
			case actor.Stopped:
				return false
			}
		case option.None:
			return false
		}
		clk.Advance(0s)
		let ev = log.Get()
		let rA = actor.RestartsOf(supRest, ids[0])
		let rB = actor.RestartsOf(supRest, ids[1])
		let rC = actor.RestartsOf(supRest, ids[2])
		if rA != 0 || rB != 1 || rC != 1 {
			return false
		}
		if ev.Len() < 4 || ev[0] != "stop:B" || ev[1] != "stop:C" || ev[2] != "start:B" || ev[3] != "start:C" {
			return false
		}
		log.Set([]string{})
		actor.Shutdown(supRest) catch {
		case _:
			return false
		}
		let shutEv = log.Get()
		if shutEv.Len() != 3 || shutEv[0] != "stop:C" || shutEv[1] != "stop:B" || shutEv[2] != "stop:A" {
			return false
		}

		let allLog = sync.NewRef[[]string]([]string{})
		let aA = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let aB = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let aC = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let polAll = tasks.Supervision{
			Strategy:  tasks.AllForOne,
			Intensity: tasks.Intensity{Max: 5, Period: 60s},
			Backoff:   schedule.Spaced(0s),
		}
		let supAll = actor.SuperviseSet(
			[]actor.ChildSpec{
				actor.PermanentChild("A", func() actor.Target needs time.Scheduler { spawnWorker("A", allLog, aA) }),
				actor.PermanentChild("B", func() actor.Target needs time.Scheduler { spawnWorker("B", allLog, aB) }),
				actor.TemporaryChild("C", func() actor.Target needs time.Scheduler { spawnWorker("C", allLog, aC) }),
			},
			polAll,
		) catch {
		case _:
			return false
		}
		let allIDs = actor.Children(supAll)
		allLog.Set([]string{})
		match aB.Get() {
		case option.Some[actor.Address[supMsg]] as ab:
			actor.Send(ab.Value, MsgCrash) catch {
			case actor.Stopped:
				return false
			}
		case option.None:
			return false
		}
		clk.Advance(0s)
		let allEv = allLog.Get()
		let remaining = actor.Children(supAll)
		let okAll = remaining.Len() == 2 &&
			actor.RestartsOf(supAll, allIDs[0]) == 1 &&
			actor.RestartsOf(supAll, allIDs[1]) == 1 &&
			allEv.Len() == 5 &&
			allEv[0] == "stop:B" &&
			allEv[1] == "stop:C" &&
			allEv[2] == "stop:A" &&
			allEv[3] == "start:A" &&
			allEv[4] == "start:B"
		actor.Shutdown(supAll) catch {
		case _:
			let _ = 0
		}
		if !okAll {
			return false
		}

		let oneLog = sync.NewRef[[]string]([]string{})
		let oA = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let oB = sync.NewRef[option.Option[actor.Address[supMsg]]](option.None)
		let supOne = actor.StartDynamicSupervisor(tasks.Supervision{
			Strategy:  tasks.OneForOne,
			Intensity: tasks.Intensity{Max: 1, Period: 10s},
			Backoff:   schedule.Spaced(2s),
		})
		let idA = actor.AddChild(supOne, actor.TransientChild("A", func() actor.Target needs time.Scheduler { spawnWorker("A", oneLog, oA) })) catch {
		case _:
			return false
		}
		let idB = actor.AddChild(supOne, actor.PermanentChild("B", func() actor.Target needs time.Scheduler { spawnWorker("B", oneLog, oB) })) catch {
		case _:
			return false
		}
		match oA.Get() {
		case option.Some[actor.Address[supMsg]] as aa:
			actor.Send(aa.Value, MsgHalt) catch {
			case actor.Stopped:
				let _ = 0
			}
		case option.None:
			return false
		}
		clk.Advance(0s)
		if actor.Count(supOne) != 1 || actor.RestartsOf(supOne, idA) != 0 {
			return false
		}
		match oB.Get() {
		case option.Some[actor.Address[supMsg]] as ab:
			actor.Send(ab.Value, MsgCrash) catch {
			case actor.Stopped:
			}
		case option.None:
			return false
		}
		clk.Advance(1s)
		if actor.RestartsOf(supOne, idB) != 0 {
			return false
		}
		clk.Advance(1s)
		if actor.RestartsOf(supOne, idB) != 1 {
			return false
		}
		match oB.Get() {
		case option.Some[actor.Address[supMsg]] as ab2:
			actor.Send(ab2.Value, MsgCrash) catch {
			case actor.Stopped:
			}
		case option.None:
			return false
		}
		clk.Advance(0s)
		match actor.PeerExit(supOne) {
		case actor.AbnormalExit:
			true
		case actor.NormalExit:
			false
		}
	}
}

func testBidirectionalLinkAndUnlink() bool needs time.Scheduler {
	let p1 = actor.Start[bool, supMsg, testErr](true, 16, func(s bool, msg supMsg) actor.Next[bool] fails testErr {
		match msg {
		case MsgCrash:
			return testErr{Code: 1}
		case MsgHalt:
			actor.Halt
		case MsgPing as r:
			actor.Respond(r.Reply, "p1")
			actor.Continue[bool]{State: s}
		}
	})
	let p2 = actor.Start[bool, supMsg, testErr](true, 16, func(s bool, msg supMsg) actor.Next[bool] fails testErr {
		match msg {
		case MsgCrash:
			let _ = triggerDefect()
			actor.Continue[bool]{State: s}
		case MsgHalt:
			actor.Halt
		case MsgPing as r:
			actor.Respond(r.Reply, "p2")
			actor.Continue[bool]{State: s}
		}
	})
	let p3 = actor.Start[bool, supMsg, testErr](true, 16, func(s bool, msg supMsg) actor.Next[bool] fails testErr {
		match msg {
		case MsgCrash:
			return testErr{Code: 3}
		case MsgHalt:
			actor.Halt
		case MsgPing as r:
			actor.Respond(r.Reply, "p3")
			actor.Continue[bool]{State: s}
		}
	})
	let h13 = actor.Link(p1, p3)
	actor.Unlink(h13)
	let _ = actor.Link(p1, p2)
	let norm = actor.Start[bool, supMsg, testErr](true, 16, func(s bool, msg supMsg) actor.Next[bool] fails testErr {
		match msg {
		case MsgCrash:
			return testErr{Code: 4}
		case MsgHalt:
			actor.Halt
		case MsgPing as r:
			actor.Respond(r.Reply, "n")
			actor.Continue[bool]{State: s}
		}
	})
	let _ = actor.Link(p3, norm)
	actor.Send(norm.Addr(), MsgHalt) catch {
	case actor.Stopped:
		return false
	}
	let _ = actor.PeerExit(norm)
	let p3AliveAfterNormal = actor.Ask(p3.Addr(), func(r actor.Reply[string, testErr]) supMsg { MsgPing{Reply: r} }) catch {
	case _:
		return false
	}
	if p3AliveAfterNormal != "p3" {
		return false
	}

	actor.Send(p1.Addr(), MsgCrash) catch {
	case actor.Stopped:
		return false
	}
	let ex1 = actor.PeerExit(p1)
	let ex2 = actor.PeerExit(p2)
	let p3StillAlive = actor.Ask(p3.Addr(), func(r actor.Reply[string, testErr]) supMsg { MsgPing{Reply: r} }) catch {
	case _:
		return false
	}
	actor.StopPeer(p3)
	match (ex1, ex2) {
	case (actor.AbnormalExit, actor.AbnormalExit):
		p3StillAlive == "p3"
	case (_, _):
		false
	}
}

func testDurableOutboxAndTimerAnchor() bool {
	let clk = time.NewManual(5000000000000)
	with context { time.Scheduler: clk, time.Clock: clk } {
		let savedLog = sync.NewRef[[]int]([]int{})
		let committedState = sync.NewRef[option.Option[int]](option.None)
		let sinkLog = sync.NewRef[[]int]([]int{})
		let spec = actor.LiveMachine[int, int, int]{
			Initial: 0,
			Step: func(s int, sig int) machines.Decision[int, int] {
				if sig == 99 {
					return machines.Emit[int, int]{Next: s + 100, Output: s + 100}
				}
				machines.Emit[int, int]{Next: s + sig, Output: sig}
			},
			After: func(s int) option.Option[Timeout[int]] {
				let _ = s
				option.None
			},
			Work: func(s int) option.Option[func(send entry func(e int)) defects] {
				let _ = s
				option.None
			},
			Exit: func(s int) option.Option[func()] {
				let _ = s
				option.None
			},
		}
		let a = actor.SuperviseDurableOutboxLiveWith(
			spec,
			16,
			func(o int) { sinkLog.Update(func(xs []int) []int { xs.Append(o) }) },
			tasks.Supervision{Strategy: tasks.OneForOne, Intensity: tasks.Intensity{Max: 2, Period: 10s}, Backoff: schedule.Spaced(0s)},
			func() option.Option[int] fails testErr { committedState.Get() },
			func(prev int, next int, sig int, outbox []int) fails testErr {
				let _ = prev
				if sig == 5 {
					return testErr{Code: 500}
				}
				committedState.Set(option.Some[int]{Value: next})
				savedLog.Update(func(xs []int) []int { xs.Append(outbox[0]) })
			},
		) catch {
		case _:
			return false
		}
		let _ = a.Ask(3) catch {
		case actor.Stopped:
			return false
		}
		let _ = a.Ask(5) catch {
		case actor.Stopped:
			machines.Ignore
		}
		clk.Advance(0s)
		let _ = a.Ask(4) catch {
		case actor.Stopped:
			return false
		}
		a.Stop()
		let anchor = actor.AnchorTimer(10s)
		clk.Advance(4s)
		let rem1 = actor.RemainingDelay(anchor)
		clk.Advance(7s)
		let rem2 = actor.RemainingDelay(anchor)
		let sv = savedLog.Get()
		let sk = sinkLog.Get()
		sv.Len() == 2 && sv[0] == 3 && sv[1] == 4 &&
			sk.Len() == 2 && sk[0] == 3 && sk[1] == 4 &&
			a.State() == 7 &&
			rem1 == 6s &&
			rem2 == actor.DueNow()
	}
}

func main() needs io.Console | Ambient {
	let sched = time.NewLiveScheduler()
	with context { time.Scheduler: sched } {
		if !testAddressAndReply() {
			io.Console.Println("FAIL testAddressAndReply")
			return
		}
		if !testAgentAndBorrows() {
			io.Console.Println("FAIL testAgentAndBorrows")
			return
		}
		if !testBidirectionalLinkAndUnlink() {
			io.Console.Println("FAIL testBidirectionalLinkAndUnlink")
			return
		}
	}
	if !testDynamicSupervisorStrategies() {
		io.Console.Println("FAIL testDynamicSupervisorStrategies")
		return
	}
	if !testDurableOutboxAndTimerAnchor() {
		io.Console.Println("FAIL testDurableOutboxAndTimerAnchor")
		return
	}
	io.Console.Println("ALL_OK")
}
`

func TestActorP3NativeBeamAndSupervision(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: compiles native Ergo")
	}
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	modDir := filepath.Join(tmp, "mod")
	srcPath := filepath.Join(modDir, "cmd", "driver", "main.ergo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module p3accept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcPath, []byte(actorP3NativeMainSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(tmp, "bin", "driver")
	compile := exec.Command(ergoBin, "build", "./cmd/driver", "-o", exe)
	compile.Dir = modDir
	compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	if out, err := runBound(compile); err != nil {
		t.Fatalf("ergo build driver (ERGO_FALLBACK=off): %v\n%s", err, out)
	}
	run := exec.Command(exe)
	out, err := runBound(run)
	if err != nil {
		t.Fatalf("run driver: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "ALL_OK" {
		t.Fatalf("expected ALL_OK, got:\n%s", out)
	}
}
