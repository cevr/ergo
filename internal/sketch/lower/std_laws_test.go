package lower

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/lawledger/parsed"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std laws (docs/spec/stdlib.md, Std laws; std-align S1a) are checked,
// not run, until laws and proofs. TestStdLawsRun is the ledger's runner for the laws
// that need the lowering and the runtime: one Ergo program restates each
// as a test of its own name, its parameters drawn by the property runner,
// and the test build runs them with --seed 1, as ergo test --seed 1 does.

// stdLawsProbe restates each law whose ledger runner is TestStdLawsRun.
// A test's name is the ledger key without spaces and dots, its package
// capitalized: queue's Queue.EndIsIdempotent is QueueQueueEndIsIdempotent.
// A member law is restated for each kind of provider the std makes: a
// queue of capacity 0 and one with room, a waiting task.
const stdLawsProbe = `import queue
import fstest
import filepath
import sync
import signal
import os
import api
import atomic
import tasks
import embed
import fs
import task
import time
import timeout
import scope
import trace
import log
import strings
import testing
import pace
import math

// mathEdges are f and the floats at and next to every bound of the
// checked conversions: the drawn float alone almost never reaches one.
func mathEdges(f float64) []float64 {
	[]float64{f, 0.0, -0.0, 0.5, -0.5, -0.99, -1.0, -1.5, 127.5, 128.0, -128.5, -129.0, 255.5, 256.0,
		32767.5, 32768.0, -32768.5, -32769.0, 65535.5, 65536.0, 2147483647.5, 2147483648.0,
		-2147483648.5, -2147483649.0, 4294967295.5, 4294967296.0, 9223372036854774784.0,
		9223372036854775808.0, -9223372036854775808.0, -9223372036854777856.0,
		18446744073709549568.0, 18446744073709551616.0, math.Inf(1), math.Inf(-1), math.NaN()}
}

type apiEcho {} implements api.Transport[string, int]

func Carry(t apiEcho, c string, answer own func(c string) int) int { answer(c) }

type apiDropping {} implements api.Transport[string, int]

func Carry(t apiDropping, c string, answer own func(c string) int) int {
	if len(c) == 0 {
		answer(c)
	} else {
		answer(c[0:len(c)-1])
	}
}

test ApiExitKeepsTheCode(o api.Output) {
	let want = if o.Code >= 1 && o.Code <= 125 || o.Code >= 129 && o.Code <= 192 { o.Code } else if o.Code == 0 { 0 } else { 1 }
	api.ExitOf(o) catch {
	case os.Exit as e:
		assert int(e.Code) == want && want != 0
		return
	}
	assert want == 0
}

test ApiKebabRoundTrips(words []string) {
	let mut name = ""
	let mut parts = []string{}
	for w in words {
		let mut low = ""
		for i in 0..len(w) {
			let c = w[i]
			if c >= 'a' && c <= 'z' {
				low = low + w[i:i+1]
			} else if c >= 'A' && c <= 'Z' {
				low = low + strings.ToLower(w[i:i+1])
			}
		}
		for len(low) < 2 {
			low = low + "x"
		}
		parts = parts.Append(low)
		name = name + strings.ToUpper(low[0:1]) + low[1:]
	}
	let k = api.Kebab(name)
	assert k == strings.Join(parts, "-") && api.Kebab(k) == k
}

test ApiAgreesSeesTheLoss(c string, o int) {
	assert api.Agrees(apiEcho, c, o) && !api.Agrees(apiDropping, c + "!", o)
}

test ApiWordsReadLine(args []string) {
	let back = api.Words(api.Line(args)) catch {
	case api.Unclosed:
		assert false
		return
	}
	assert back == args
}

func apiEchoed(args []string) api.Output {
	api.Output{Out: strings.Join(args, "|"), Err: "", Code: if len(args) == 3 { 1 } else { 0 }}
}

test ApiSessionIsEachLine(script [][]string) {
	let mut lines = []string{}
	for a in script {
		lines = lines.Append(" " + api.Line(a))
		lines = lines.Append("# " + api.Line(a))
		lines = lines.Append("")
	}
	let mut want = []api.Step{}
	for k, a in script {
		if len(a) == 0 {
			continue
		}
		let o = apiEchoed(a)
		want = want.Append(api.Step{Line: 3*k + 1, Text: api.Line(a), Output: o})
		if o.Code != 0 {
			break
		}
	}
	assert api.Session(lines, apiEchoed) == want
}

test ApiCompletionNamesEachCommand(words []string, shell api.Shell) {
	let mut commands = []api.Entry{}
	for w in words {
		let mut low = ""
		for i in 0..len(w) {
			let c = w[i]
			if c >= 'a' && c <= 'z' {
				low = low + w[i:i+1]
			} else if c >= 'A' && c <= 'Z' {
				low = low + strings.ToLower(w[i:i+1])
			}
		}
		for len(low) < 2 {
			low = low + "x"
		}
		commands = commands.Append(api.Entry{Name: low, Doc: "does " + w + "\nmore"})
	}
	let text = api.Completion("prog", shell, commands)
	let mut at = 0
	for c in commands {
		let i = strings.Index(text[at:], c.Name)
		assert i >= 0
		at = at + i + len(c.Name)
	}
	assert !strings.Contains(text, "\nmore")
}

test OsCodeWhere(v os.Code) {
	let it = int(v)
	assert (1 <= it && it <= 125) || (129 <= it && it <= 192)
	assert try os.ParseCode(it) == v
}

type releaseCount { Seen queue.Queue[int] } implements Closer

func Close(r releaseCount) {
	r.Seen.Push(1) catch {
	case queue.Closed:
		return
	}
}

test QueuePushAfterEndReleases {
	let seen = queue.New[int](8)
	let q = queue.New[releaseCount](1)
	q.End()
	let r = releaseCount{Seen: seen}
	q.Push(r) catch {
	case queue.Closed:
		assert seen.Len() == 1
		return
	}
	assert false
}

test QueueDrainReleasesLeft(n int) {
	let k = (n%8 + 8) % 8
	let seen = queue.New[int](8)
	if true {
		let q = queue.New[releaseCount](k)
		for _ in 0..k {
			let r = releaseCount{Seen: seen}
			try q.Push(r)
		}
	}
	assert seen.Len() == k
}

test AtomicInitialValues(value int64, flag bool) {
	assert atomic.NewInt64(value).Load() == value && atomic.NewBool(flag).Load() == flag
}

test AtomicInt64StoresAndAdds(a int64, b int64) {
	let c = atomic.NewInt64(0)
	let alias = c
	alias.Store(a)
	let sum = c.Add(b)
	let seen = c.Load()
	let previous = c.Swap(a)
	let changed = c.CompareAndSwap(a, b)
	let unchanged = !c.CompareAndSwap(b + 1, a)
	assert sum == a + b && seen == sum && previous == sum && changed && unchanged && alias.Load() == b
}

test AtomicBoolExchanges(a bool, b bool) {
	let f = atomic.NewBool(false)
	f.Store(a)
	let previous = f.Swap(b)
	let seen = f.Load()
	let changed = f.CompareAndSwap(b, a)
	let unchanged = !f.CompareAndSwap(!a, b)
	assert previous == a && seen == b && changed && unchanged && f.Load() == a
}

test AtomicIncrementsCount(n int) {
	let k = 1 + (n % 8 + 8) % 8
	let c = atomic.NewInt64(0)
	let s = tasks.NewSet(tasks.FailFast)
	for _ in 0..k {
		try tasks.Go(s, func() { _ = c.Add(1) })
	}
	s.Wait()
	assert c.Load() == int64(k)
}

test EmbedEmptyHasNoFiles(path string) {
	let _ = embed.NewEmpty().ReadFile(path) catch {
	case fs.NotFound:
		return
	case fs.Denied, fs.Failed:
		assert false
		return
	}
	assert false
}

test EmbedFSReadIsRepeatable(path string) {
	let f = embed.NewEmpty()
	let first = f.ReadFile(path) catch {
	case fs.NotFound:
		return
	case fs.Denied, fs.Failed:
		assert false
		return
	}
	assert try f.ReadFile(path) == first
}

// small is a capacity in 0..2 drawn from n.
func small(n int) int {
	let c = n % 3
	if c < 0 {
		return -c
	}
	c
}

test PreludeTaskStopIsIdempotent(n int) {
	let waiting = with context { Scheduler: time.NewManual(0) } {
		task.Go() {
			Scheduler.Sleep(1h)
			n
		}
	}
	waiting.Stop()
	waiting.Stop()
	let done = task.Go() { n }
	done.Stop()
	done.Stop()
	assert true
}

test QueueQueueEndIsIdempotent(n int) {
	let empty = queue.New[int](0)
	empty.End()
	empty.End()
	let full = queue.New[int](1)
	try full.Push(n)
	full.End()
	full.End()
	assert try full.Pop() == n
}

test QueueQueuePushAfterEndFails(v int, cap int) {
	let c = small(cap)
	let q = queue.New[int](c)
	if c != 0 {
		try q.Push(v)
	}
	q.End()
	let mut failed = false
	q.Push(v) catch {
	case queue.Closed:
		failed = true
	}
	assert failed
}

test QueueQueueValuesStops(xs []int, extra int) {
	let n = len(xs)
	for k in 1..n+2 {
		let q = queue.New[int](n + small(extra))
		for x in xs {
			try q.Push(x)
		}
		q.End()
		let mut calls = 0
		let s = q.Values()
		s(func(v int) bool {
			calls = calls + 1
			calls < k
		})
		let taken = if k < n { k } else { n }
		assert calls == taken && q.Len() == n - taken
		for i in taken..n {
			assert q.Poll() == Some[int]{Value: xs[i]}
		}
		assert q.Poll() == None
	}
	let empty = queue.New[int](0)
	empty.End()
	let mut none = 0
	for _ in empty.Values() {
		none = none + 1
	}
	assert none == 0 && empty.Len() == 0
}

test QueueFirstInFirstOut(a int, b int) {
	let q = queue.New[int](2)
	try q.Push(a)
	try q.Push(b)
	let first = try q.Pop()
	let second = try q.Pop()
	assert first == a && second == b
}

test QueueEndKeepsValues(v int) {
	let q = queue.New[int](1)
	try q.Push(v)
	q.End()
	assert try q.Pop() == v
}

test ScopeActionsRunLastFirst() {
	let mut order = ""
	let f = func() {
		scope.OnExit() { order = order + "a" }
		scope.OnExit() { order = order + "b" }
	}
	f()
	assert order == "ba"
}

test TaskWaitReturnsTheBlock(n int) {
	let t = task.Go() { n }
	assert t.Wait() == n
}

test TaskStopIsIdempotent() {
	let t = with context { Scheduler: time.NewManual(0) } {
		task.Go() {
			Scheduler.Sleep(1h)
			0
		}
	}
	t.Stop()
	t.Stop()
	assert true
}

type taskOdd { N int }

func taskEnded(k int, n int) int fails taskOdd {
	if k == 0 {
		Unreachable("crash \(n)")
	}
	if k == 1 {
		return taskOdd{N: n}
	}
	n
}

func taskCrashed(k int, marks queue.Queue[int]) int {
	for i in 0..k {
		scope.OnExit() {
			marks.Push(i) catch {
			case queue.Closed:
			}
		}
	}
	Unreachable("after \(k) actions")
}

test TaskDefectIsObservedOnce(n int) {
	let k = ((n % 3) + 3) % 3
	let t = task.Go() { try taskEnded(k, n) }
	let ok = match task.ExitOf(t) {
	case task.Defected as d:
		k == 0 && strings.HasPrefix(d.Reason, "unreachable at ") && strings.HasSuffix(d.Reason, ": crash \(n)")
	case task.Failed[taskOdd] as f:
		k == 1 && f.Error.N == n
	case task.Done[int] as v:
		k == 2 && v.Value == n
	}
	assert ok
}

test TaskExitOfWaitsForCleanup(n int) {
	let k = ((n % 4) + 4) % 4
	let marks = queue.New[int](4)
	let t = task.Go() { taskCrashed(k, marks) }
	let ok = match task.ExitOf(t) {
	case task.Defected:
		marks.Len() == k
	case _:
		false
	}
	assert ok
}

test TaskExitOfAnywhere(n int) {
	let outer = task.Go() { task.Go() { n } }
	let t = outer.Wait()
	let ok = match task.ExitOf(t) {
	case task.Done[int] as d:
		d.Value == n
	case _:
		false
	}
	assert ok
}

test TaskStopOfWaiterStillInterrupts() {
	let m = time.NewManual(0)
	with context { Scheduler: m } {
		let waiter = task.Go() {
			let sleeper = task.Go() {
				Scheduler.Sleep(1h)
				0
			}
			let _ = task.ExitOf(sleeper)
			Unreachable("ExitOf returned to a stopped waiter")
		}
		for m.Sleepers() == 0 {
		}
		waiter.Stop()
		assert m.Sleepers() == 0
	}
}

test TimeoutAfterKeepsWhatTheBodyTook(c int, w int) {
	let m = time.NewManual(0)
	let ok = with context { Scheduler: m } {
		let n = 40
		let q = queue.New[int](((c % 3) + 3) % 3)
		let kept = queue.New[int](n)
		let producer = task.Go() {
			for i in 0..n {
				Scheduler.Sleep(1ms)
				q.Push(i) catch {
				case queue.Closed:
				}
			}
			q.End()
		}
		let consumer = task.Go() {
			let mut done = false
			for !done {
				let v = timeout.After(time.Times(1ms, 1+((w%3)+3)%3)) { try q.Pop() } catch {
				case timeout.Expired:
					-1
				case queue.Closed:
					-2
				}
				if v == -2 {
					done = true
				} else if v >= 0 {
					kept.Push(v) catch {
					case queue.Closed:
					}
				}
			}
			kept.End()
		}
		for _ in 0..n+2 {
			m.Advance(1ms)
		}
		producer.Wait()
		consumer.Wait()
		let mut next = 0
		for v in kept.Values() {
			if v == next {
				next = next + 1
			}
		}
		next == n
	}
	assert ok
}

test TimeoutPopWithinWaitsUpToD(g int, w int) {
	let m = time.NewManual(0)
	let gap = ((g % 60) + 60) % 60
	let d = ((w % 59) + 59) % 59 + 1
	let start = m.Now()
	let ok = with context { Scheduler: m } {
		let q = queue.New[int](1)
		let got = queue.New[int](3)
		let producer = task.Go() {
			Scheduler.Sleep(time.Times(1ms, gap))
			q.Push(7) catch {
			case queue.Closed:
			}
		}
		let consumer = task.Go() {
			let v = timeout.PopWithin(q, time.Times(1ms, d)) catch {
			case timeout.Expired:
				-1
			case queue.Closed:
				-2
			}
			got.Push(v) catch {
			case queue.Closed:
			}
			got.Push(time.Count(m.Elapsed(start), 1ms)) catch {
			case queue.Closed:
			}
			got.Push(m.Sleepers()) catch {
			case queue.Closed:
			}
		}
		m.Advance(120ms)
		producer.Wait()
		consumer.Wait()
		let v = got.Poll()
		let at = got.Poll()
		let asleep = got.Poll()
		let took = v == Some[int]{Value: 7} && at == Some[int]{Value: gap} && q.Len() == 0 && asleep == Some[int]{Value: 0}
		let expired = v == Some[int]{Value: -1} && at == Some[int]{Value: d} && q.Len() == 1
		if gap < d {
			took
		} else if gap > d {
			expired
		} else {
			took || expired
		}
	}
	assert ok
}

test TimeoutPopWithinTakesWhatIsThere(v int) {
	let ok = with context { Scheduler: time.NewManual(0) } {
		let q = queue.New[int](1)
		try q.Push(v)
		q.End()
		let first = try timeout.PopWithin(q, 0ns)
		let then = timeout.PopWithin(q, 0ns) catch {
		case queue.Closed:
			-1
		case timeout.Expired:
			-2
		}
		first == v && then == -1 && q.Len() == 0
	}
	assert ok
}

test TimeoutPopBeforeKeepsTheDeadline(gaps []int, w int) {
	let m = time.NewManual(0)
	let big = 2*(((w%30)+30)%30) + 1
	let n = len(gaps)
	let start = m.Now()
	let ok = with context { Scheduler: m } {
		let q = queue.New[int](n + 1)
		let got = queue.New[int](n + 4)
		let producer = task.Go() {
			for i, g in gaps {
				Scheduler.Sleep(time.Times(1ms, 2*(((g%10)+10)%10)))
				q.Push(i) catch {
				case queue.Closed:
				}
			}
			Scheduler.Sleep(time.Times(1ms, 2*big))
			q.End()
		}
		let consumer = task.Go() {
			let dl = timeout.NewDeadline()
			dl.Arm(time.Times(1ms, big))
			got.Push(if dl.Armed() { 1 } else { 0 }) catch {
			case queue.Closed:
			}
			let mut open = true
			for open {
				let v = timeout.PopBefore(q, dl) catch {
				case timeout.Expired:
					-1
				case queue.Closed:
					-2
				}
				if v >= 0 {
					got.Push(v) catch {
					case queue.Closed:
					}
				} else {
					open = false
					got.Push(v) catch {
					case queue.Closed:
					}
					got.Push(time.Count(m.Elapsed(start), 1ms)) catch {
					case queue.Closed:
					}
					got.Push(if dl.Armed() { 1 } else { 0 }) catch {
					case queue.Closed:
					}
				}
			}
			got.End()
		}
		m.Advance(time.Times(1ms, 20*n+4*big+2))
		producer.Wait()
		consumer.Wait()
		let mut want = []int{1}
		let mut at = 0
		for i, g in gaps {
			at = at + 2*(((g%10)+10)%10)
			if at < big {
				want = want.Append(i)
			}
		}
		let left = n - (len(want) - 1)
		want = want.Append(-1).Append(big).Append(0)
		let mut same = true
		let mut k = 0
		for v in got.Values() {
			same = same && k < len(want) && v == want[k]
			k = k + 1
		}
		same && k == len(want) && q.Len() == left
	}
	assert ok
}

test TimeoutQuickBodyWins(n int) {
	let v = with context { Scheduler: time.NewManual(0) } {
		try timeout.After(1h) { n }
	}
	assert v == n
}

test TimeoutDeadlineFirstExpires() {
	let m = time.NewManual(0)
	let expired = with context { Scheduler: m } {
		timeout.After(1ms) {
			for m.Sleepers() == 0 {
			}
			m.Advance(1ms)
			Scheduler.Sleep(1h)
			false
		} catch {
		case timeout.Expired:
			true
		}
	}
	assert expired
}

func paceWithin(n int, lo int, hi int) int {
	let span = hi - lo + 1
	let r = n % span
	if r < 0 { lo + (r + span) } else { lo + r }
}

func paceKeeps(xs []int, s string) bool needs pace.Pacing {
	let kept = xs.Append(len(s))
	let text = s + "."
	pace.Pacing.Edge()
	let mut same = len(kept) == len(xs)+1 && kept[len(xs)] == len(s) && text == s+"."
	for i, x in xs {
		same = same && kept[i] == x
	}
	same
}

test PacePacingEdgeKeepsValues(xs []int, s string) {
	assert with context { pace.Pacing: pace.Default } { paceKeeps(xs, s) }
	assert with context { pace.Pacing: pace.NewCounting() } { paceKeeps(xs, s) }
	assert with context { pace.Pacing: pace.NewEvery(pace.NewQuiet(pace.Default), 2) } { paceKeeps(xs, s) }
	assert with context { pace.Pacing: pace.NewQuiet(pace.NewEvery(pace.NewCounting(), 3)) } { paceKeeps(xs, s) }
}

test PaceDefaultReachesNothing(xs []int, n int) {
	let kept = xs.Append(n)
	with context { pace.Pacing: pace.Default } {
		for _ in 0..paceWithin(n, 0, 64) {
			pace.Pacing.Edge()
		}
	}
	assert len(kept) == len(xs) + 1 && kept[len(xs)] == n
}

test PaceNextReadLeavesRoom(grown int64, edges int64, room int64) {
	let g = int64(paceWithin(int(grown % 1000000000), 0, 999999999))
	let e = int64(paceWithin(int(edges % 1000), 1, 1000))
	let perEdge = if g / e < 1 { int64(1) } else { g / e }
	let r = perEdge*int64(paceWithin(int(room%1000), -2, 600)) + int64(paceWithin(int(room%7), 0, 6))
	let next = pace.NextRead(g, e, r)
	assert 1 <= next && next <= pace.MaxSpan && (next == 1 || g == 0 || next * perEdge <= r / 2)
}

test PaceCountingCountsEachEdge(n int) {
	let k = paceWithin(n, 0, 64)
	let c = pace.NewCounting()
	let before = c.Edges()
	with context { pace.Pacing: c } {
		for _ in 0..k {
			pace.Pacing.Edge()
		}
	}
	assert before == 0 && c.Edges() == int64(k)
}

func paceHoldKeeps(xs []int, n int) bool needs pace.Pacing {
	let kept = xs.Append(n)
	pace.Pacing.Hold()
	pace.Pacing.Edge()
	pace.Pacing.Release()
	len(kept) == len(xs)+1 && kept[len(xs)] == n
}

test PacePacingHoldKeepsValues(xs []int, n int) {
	assert with context { pace.Pacing: pace.Default } { paceHoldKeeps(xs, n) }
	assert with context { pace.Pacing: pace.NewCounting() } { paceHoldKeeps(xs, n) }
	assert with context { pace.Pacing: pace.NewEvery(pace.NewQuiet(pace.Default), 2) } { paceHoldKeeps(xs, n) }
	assert with context { pace.Pacing: pace.NewQuiet(pace.NewEvery(pace.NewCounting(), 3)) } { paceHoldKeeps(xs, n) }
}

test PaceHoldsOweTheLastClose(ops []int) {
	let h = pace.NewHolds()
	let mut depth = 0
	let mut owed = false
	let mut same = true
	for op in ops {
		let kind = paceWithin(op, 0, 2)
		if kind == 0 {
			h.Open()
			depth = depth + 1
		} else if kind == 1 && depth > 0 {
			let collects = h.Close()
			depth = depth - 1
			same = same && collects == (depth == 0 && owed)
			if depth == 0 {
				owed = false
			}
		} else if kind == 2 {
			same = same && h.Owe() == (depth > 0)
			owed = owed || depth > 0
		}
		same = same && h.Depth() == int64(depth)
	}
	assert same
}

type paceStopped {}

func paceNest(depth int, edges int, stop bool) fails paceStopped needs pace.Pacing {
	pace.Held() { try paceInner(depth, edges, stop) }
}

func paceInner(depth int, edges int, stop bool) fails paceStopped needs pace.Pacing {
	if depth > 1 {
		try paceNest(depth-1, edges, stop)
		return
	}
	for _ in 0..edges {
		pace.Pacing.Edge()
	}
	if stop {
		return paceStopped
	}
}

test PaceHeldDefersToTheLastRelease(n int, k int, stop bool) {
	let edges = paceWithin(n, 0, 16)
	let depth = paceWithin(k, 1, 4)
	let c = pace.NewCounting()
	with context { pace.Pacing: c } {
		paceNest(depth, edges, stop) catch {
		case paceStopped:
		}
		pace.Pacing.Edge()
	}
	let owed = if edges > 0 { int64(1) } else { int64(0) }
	assert c.Edges() == int64(edges+1) && c.HeldEdges() == int64(edges) && c.Deferred() == owed
}

func paceLargest(xs []int) int {
	let mut m = xs[0]
	for x in xs {
		if x > m {
			m = x
		}
	}
	m
}

test PaceEveryOneIsParent(n int, k int) {
	let edges = paceWithin(n, 0, 64)
	let holds = paceWithin(k, 0, 4)
	let c = pace.NewCounting()
	with context { pace.Pacing: pace.NewEvery(c, 1) } {
		for _ in 0..holds {
			pace.Pacing.Hold()
		}
		for _ in 0..edges {
			pace.Pacing.Edge()
		}
		for _ in 0..holds {
			pace.Pacing.Release()
		}
	}
	let held = if holds > 0 { edges } else { 0 }
	let owed = if holds > 0 && edges > 0 { int64(1) } else { int64(0) }
	assert c.Edges() == int64(edges) && c.HeldEdges() == int64(held) && c.Deferred() == owed
}

test PaceEveryPassesOneInN(n int, k int) {
	let every = paceWithin(n, 1, 9)
	let edges = paceWithin(k, 0, 200)
	let c = pace.NewCounting()
	with context { pace.Pacing: pace.NewEvery(c, every) } {
		for _ in 0..edges {
			pace.Pacing.Edge()
		}
	}
	assert c.Edges() == int64(edges/every)
}

test PaceEveryNests(a int, b int, k int) {
	let x = paceWithin(a, 1, 6)
	let y = paceWithin(b, 1, 6)
	let edges = paceWithin(k, 0, 200)
	let nested = pace.NewCounting()
	let flat = pace.NewCounting()
	let outer = pace.NewEvery(pace.NewEvery(nested, y), x)
	let one = pace.NewEvery(flat, x*y)
	let mut same = true
	for _ in 0..edges {
		with context { pace.Pacing: outer } { pace.Pacing.Edge() }
		with context { pace.Pacing: one } { pace.Pacing.Edge() }
		same = same && nested.Edges() == flat.Edges()
	}
	assert same
}

test PaceQuietForwardsHolds(n int, k int) {
	let edges = paceWithin(n, 0, 64)
	let holds = paceWithin(k, 0, 4)
	let c = pace.NewCounting()
	with context { pace.Pacing: pace.NewQuiet(c) } {
		for _ in 0..holds {
			pace.Pacing.Hold()
		}
		for _ in 0..edges {
			pace.Pacing.Edge()
		}
		c.Edge()
		for _ in 0..holds {
			pace.Pacing.Release()
		}
	}
	c.Edge()
	let held = if holds > 0 { int64(1) } else { int64(0) }
	assert c.Edges() == 2 && c.HeldEdges() == held && c.Deferred() == held
}

test PaceRaisesFollowModel(ops []int) {
	let r = pace.NewRaises()
	let mut open = []int{}
	let mut same = true
	for op in ops {
		let percent = 100 * paceWithin(op/2, 1, 8)
		if paceWithin(op, 0, 1) == 0 {
			let got = r.Raise(percent)
			open = open.Append(percent)
			same = same && got == paceLargest(open)
		} else if len(open) > 0 {
			let i = paceWithin(op/16, 0, len(open)-1)
			let gone = open[i]
			let (_, rest) = open.TakeAt(i)
			open = rest
			let got = r.Lower(gone)
			same = same && got == (if len(open) == 0 { None } else { Some[int]{Value: paceLargest(open)} })
		}
		same = same && r.Open() == len(open)
	}
	assert same
}

test TraceInnerSpanShadows(outer string, inner string) {
	let got = with trace.Span(outer) {
		with trace.Span(inner) { trace.Current() }
	}
	assert got == Some[string]{Value: inner}
}

test TraceSpanEndRestores(outer string, name string) {
	let before = trace.Current()
	with trace.Span(name) {
		trace.Current()
	}
	assert trace.Current() == before
	let nested = with trace.Span(outer) {
		let mine = trace.Current()
		with trace.Span(name) {
			trace.Current()
		}
		trace.Current() == mine
	}
	assert nested
}

test LogInnerAnnotationShadows(key string, outer string, inner string) {
	let got = with log.Annotate(key, outer) {
		with log.Annotate(key, inner) { log.Annotation(key) }
	}
	assert got == Some[string]{Value: inner}
}

test LogAnnotationEndRestores(key string, outer string, text string) {
	let before = log.Annotation(key)
	with log.Annotate(key, text) {
		log.Annotation(key)
	}
	assert log.Annotation(key) == before
	let nested = with log.Annotate(key, outer) {
		let mine = log.Annotation(key)
		with log.Annotate(key, text) {
			log.Annotation(key)
		}
		log.Annotation(key) == mine
	}
	assert nested
}

// probe and Absent restate the capability of the testing laws, which this
// package cannot name.
type probe {
	Size() int,
	Has(k string) bool,
	Put(k string, v int),
	Get(k string) int fails Absent,
}

type Absent { Key string }

test TestingScriptRecordsEachCall(ks []string, n int) {
	let s = testing.Script[probe]{}
	s.Has.Answer(func(k string) bool { len(k) > 0 })
	with context { probe: s } {
		for i, k in ks {
			probe.Put(k, n + i)
			let _ = probe.Has(k)
		}
	}
	let puts = s.Put.Calls()
	assert len(puts) == len(ks) && len(s.Has.Calls()) == len(ks) && len(s.Size.Calls()) == 0
	for i, k in ks {
		assert puts[i] == (k, n + i) && s.Has.Calls()[i] == k
	}
}

test TestingScriptAnswersByTheLastAnswer(k string, a int, b int) {
	let s = testing.Script[probe]{}
	s.Size.Answer(func() int { a })
	s.Get.Answer(func(key string) int fails Absent { Absent{Key: key} })
	with context { probe: s } {
		let first = probe.Size()
		s.Size.Answer(func() int { b })
		let second = probe.Size()
		let got = probe.Get(k) catch {
		case Absent as e:
			if e.Key == k { -1 } else { -2 }
		}
		assert first == a && second == b && got == -1 && len(s.Size.Calls()) == 2 && s.Size.Calls()[0] == testing.NoArgs && s.Get.Calls()[0] == k
	}
}

test TestingHeldCallWaitsForRelease(k string, a int) {
	let s = testing.Script[probe]{}
	s.Get.Answer(func(key string) int fails Absent { a })
	s.Get.Hold()
	let c = testing.NewVirtualClock(0)
	with context { probe: s, Scheduler: c } {
		let t = task.Go() { probe.Get(k) }
		Scheduler.Sleep(1s)
		let held = s.Get.Held()
		assert len(held) == 1 && held[0] == k && len(s.Get.Calls()) == 1
		s.Get.Release(func(key string) int fails Absent { a + 1 })
		let got = t.Wait() catch {
		case Absent:
			a
		}
		s.Get.Answer(func(key string) int fails Absent { a + 2 })
		let later = probe.Get(k) catch {
		case Absent:
			a
		}
		assert got == a + 1 && later == a + 2 && len(s.Get.Held()) == 0
	}
}

test TestingReleaseAnswersOldestFirst(k string, j string, a int) {
	let s = testing.Script[probe]{}
	s.Get.Hold()
	let c = testing.NewVirtualClock(0)
	with context { probe: s, Scheduler: c } {
		let first = task.Go() { probe.Get(k) }
		Scheduler.Sleep(1s)
		let second = task.Go() { probe.Get(j) }
		Scheduler.Sleep(1s)
		let held = s.Get.Held()
		assert len(held) == 2 && held[0] == k && held[1] == j
		s.Get.Release(func(key string) int fails Absent { a })
		s.Get.Release(func(key string) int fails Absent { a + 1 })
		let one = first.Wait() catch {
		case Absent:
			a - 1
		}
		let two = second.Wait() catch {
		case Absent:
			a - 1
		}
		s.Get.Release(func(key string) int fails Absent { a + 2 })
		let three = probe.Get(k) catch {
		case Absent:
			a - 1
		}
		assert one == a && two == a + 1 && three == a + 2 && len(s.Get.Held()) == 0
	}
}

test TestingScriptNamesTheHeldCall(k string) {
	let s = testing.Script[probe]{}
	s.Get.Hold()
	let c = testing.NewVirtualClock(0)
	with context { probe: s, Scheduler: c } {
		let t = task.Go() { probe.Get(k) }
		Scheduler.Sleep(1s)
		let report = testing.Unanswered()
		assert strings.HasPrefix(report, "testing.Script[") && strings.HasSuffix(report, "probe]: Get has a held call that no Release answered")
		assert len(s.Get.Unread()) == 1 && len(s.Get.Calls()) == 1 && len(s.Get.Unread()) == 0
		s.Get.Release(func(key string) int fails Absent { 0 })
		let _ = t.Wait() catch {
		case Absent:
			0
		}
		assert testing.Unanswered() == ""
	}
}

test TestingWorldSharesEachPair(text string, n int64) {
	let d = duration(1 + (n%1099511627775+1099511627775)%1099511627775)
	with testing.World() {
		try fs.Writes.WriteFile("f", text)
		let back = try fs.Files.ReadFile("f")
		let before = time.Clock.Now()
		Scheduler.Sleep(d)
		assert back == text && time.Clock.Now().Sub(before) == d
	}
}

test TestingWorldsAreFresh(text string) {
	with testing.World() {
		try fs.Writes.WriteFile("f", text)
	}
	with testing.World() {
		fs.Files.Stat("f") catch {
		case fs.NotFound:
			return
		case fs.Denied, fs.Failed:
			assert false
		}
		assert false
	}
}

func lawInt64(f float64) bool {
	let n = math.ToInt64(f) catch {
	case math.OutOfRange:
		return math.IsNaN(f) || f >= 9223372036854775808.0 || math.Trunc(f) < -9223372036854775808.0
	}
	float64(n) == math.Trunc(f) && n == int64(f)
}

test MathToInt64FailsOnlyOutside(f float64) {
	for g in mathEdges(f) {
		assert lawInt64(g)
	}
}

func lawInt(f float64) bool {
	let n = math.ToInt(f) catch {
	case math.OutOfRange:
		return math.IsNaN(f) || f >= 9223372036854775808.0 || math.Trunc(f) < -9223372036854775808.0
	}
	float64(n) == math.Trunc(f) && n == int(f)
}

test MathToIntFailsOnlyOutside(f float64) {
	for g in mathEdges(f) {
		assert lawInt(g)
	}
}

func lawUint64(f float64) bool {
	let n = math.ToUint64(f) catch {
	case math.OutOfRange:
		return math.IsNaN(f) || f >= 18446744073709551616.0 || math.Trunc(f) < 0.0
	}
	float64(n) == math.Trunc(f) && n == uint64(f)
}

test MathToUint64FailsOnlyOutside(f float64) {
	for g in mathEdges(f) {
		assert lawUint64(g)
	}
}

func lawUint(f float64) bool {
	let n = math.ToUint(f) catch {
	case math.OutOfRange:
		return math.IsNaN(f) || f >= 18446744073709551616.0 || math.Trunc(f) < 0.0
	}
	float64(n) == math.Trunc(f) && n == uint(f)
}

test MathToUintFailsOnlyOutside(f float64) {
	for g in mathEdges(f) {
		assert lawUint(g)
	}
}

func lawUintptr(f float64) bool {
	let n = math.ToUintptr(f) catch {
	case math.OutOfRange:
		return math.IsNaN(f) || f >= 18446744073709551616.0 || math.Trunc(f) < 0.0
	}
	float64(n) == math.Trunc(f) && n == uintptr(f)
}

test MathToUintptrFailsOnlyOutside(f float64) {
	for g in mathEdges(f) {
		assert lawUintptr(g)
	}
}

func signalOf(p int) signal.Stop {
	let c = (p%3+3)%3
	if c == 0 {
		signal.Interrupt
	} else if c == 1 {
		signal.Terminate
	} else {
		signal.Hangup
	}
}

test SignalRaisedInOrder(picks []int) {
	let mut raised = []signal.Stop{}
	for p in picks {
		raised = raised.Append(signalOf(p))
	}
	let s = signal.NewScript(raised)
	for k in raised {
		assert s.Next() == k
	}
	assert s.Poll() == None
}

test SignalPollTakesOnce(pick int) {
	let k = signalOf(pick)
	let s = signal.NewScript([]signal.Stop{k})
	assert s.Poll() == Some[signal.Stop]{Value: k}
	assert s.Poll() == None
}

test SignalQuietRunsBody(n int) {
	let got = signal.UntilStop(signal.NewScript([]signal.Stop{}), func() int { n }) catch {
	case signal.Stopped:
		assert false
		0
	}
	assert got == n
}

// signalAfterStart is StopEndsBody's Stops: Next gives k once the body
// has pushed to started, so the stop comes while the body waits.
type signalAfterStart { started queue.Queue[int], k signal.Stop } implements signal.Stops

func Next(a signalAfterStart) signal.Stop {
	let _ = a.started.Pop() catch {
	case queue.Closed:
		return a.k
	}
	a.k
}

func Poll(_ signalAfterStart) Option[signal.Stop] { None }

test SignalStopEndsBody(pick int) {
	let k = signalOf(pick)
	let cleaned = sync.NewRef[int](0)
	let started = queue.New[int](1)
	let never = queue.New[int](0)
	let _ = signal.UntilStop(signalAfterStart{started: started, k: k}, func() int {
		scope.OnExit() { cleaned.Update(func(v int) int { v + 1 }) }
		started.Push(1) catch {
		case queue.Closed:
		}
		never.Pop() catch {
		case queue.Closed:
			0
		}
	}) catch {
	case signal.Stopped as st:
		assert st.Kind == k
		assert cleaned.Get() == 1
		return
	}
	assert false
}

test SignalStatusIsTheShells(pick int) {
	let k = signalOf(pick)
	let s = int(signal.StatusOf(k))
	let want = match k {
	case signal.Interrupt:
		130
	case signal.Terminate:
		143
	case signal.Hangup:
		129
	}
	assert s == want
	assert !(1 <= s && s <= 125)
}

test FilepathAbsFromIsAbsolute(dir string, path string) {
	let d = "/" + dir
	assert filepath.IsAbs(filepath.AbsFrom(d, path))
	assert !filepath.IsAbs(path) || filepath.AbsFrom(d, path) == filepath.Clean(path)
}

test FilepathMatchItself(name string) {
	if strings.Contains(name, "*") || strings.Contains(name, "?") || strings.Contains(name, "[") || strings.Contains(name, "\\") {
		return
	}
	let ok = filepath.Match(name, name) catch {
	case filepath.BadPattern:
		assert false
		false
	}
	assert ok
}

// fsGrown is fstest's grown: a Memory with a file at each path that
// can hold one.
func fsGrown(paths []string) fstest.Memory {
	let m = fstest.NewMemory()
	for p in paths {
		m.MkdirAll(filepath.Dir(p)) catch {
		case fs.Denied, fs.Failed:
		}
		m.WriteFile(p, "x") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
		}
	}
	m
}

func fsUnder(q string, p string) bool { q == p || strings.HasPrefix(q, p + "/") }

test FstestWalkVisitsEachOnce(paths []string) {
	let m = fsGrown(paths)
	with context { fs.Files: m, fs.Writes: m } {
		let walked = try fs.Walk(".")
		assert len(walked) > 0 && walked[0] == "."
		let mut seen = Map[string, int]{}
		let mut i = 0
		for w in walked {
			assert seen.Get(w) == None
			assert i == 0 || seen.Get(filepath.Dir(w)) != None
			seen[w] = i
			i = i + 1
		}
		for p in paths {
			let info = m.Stat(p) catch {
			case fs.NotFound, fs.Denied, fs.Failed:
				continue
			}
			assert info.Dir || seen.Get(filepath.Clean(p)) != None
		}
	}
}

test FstestRemoveAllTouchesOnlyPath(paths []string, pick int) {
	let m = fsGrown(paths)
	with context { fs.Files: m, fs.Writes: m } {
		let before = try fs.Walk(".")
		let target = before[(pick%len(before)+len(before))%len(before)]
		if target == "." {
			return
		}
		try fs.RemoveAll(target)
		for q in before {
			let gone = m.Lstat(q) catch {
			case fs.NotFound:
				assert fsUnder(q, target)
				continue
			case fs.Denied, fs.Failed:
				assert false
				return
			}
			assert !fsUnder(q, target) && gone.Name != ""
		}
	}
}

test FstestMkdirTempIsNew(paths []string, prefix string) {
	if strings.Contains(prefix, "/") {
		return
	}
	let m = fsGrown(paths)
	with context { fs.Files: m, fs.Writes: m } {
		let a = try fs.MkdirTemp(".", prefix)
		let b = try fs.MkdirTemp(".", prefix)
		assert a != b && filepath.Dir(a) == "." && filepath.Dir(b) == "."
		assert len(try fs.Walk(a)) == 1 && len(try fs.Walk(b)) == 1
	}
}

test FstestScratchIsRemoved(name string) {
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return
	}
	let m = fstest.NewMemory()
	let made = func() string fails fs.NotFound | fs.Denied | fs.Failed {
		let s = try fs.NewScratch(m, m, ".", "t")
		try m.WriteFile(filepath.Join([]string{s.Path, name}), "x")
		s.Path
	}
	let path = try made()
	let _ = m.Lstat(path) catch {
	case fs.NotFound:
		return
	case fs.Denied, fs.Failed:
		assert false
		return
	}
	assert false
}
`

// stdLawsTestName is the probe test that restates the law of a ledger row.
func stdLawsTestName(pkg, law string) string {
	return strings.ToUpper(pkg[:1]) + pkg[1:] + strings.ReplaceAll(law, ".", "")
}

// stdLawsStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key. lawledger.Run
// holds it equal to the ledger, so a law changed in source fails here
// until its restatement in the probe is changed with it.
var stdLawsStatements = map[string]string{
	"fstest WalkVisitsEachOnce":            `WalkVisitsEachOnce(paths []string) { let m = grown(paths) with context { fs.Files: m, fs.Writes: m } { let walked = try fs.Walk(".") if len(walked) == 0 || walked[0] != "." { return false } let mut seen = Map[string, int]{} let mut i = 0 for w in walked { if seen.Get(w) != None { return false } if i > 0 && seen.Get(filepath.Dir(w)) == None { return false } seen[w] = i i = i + 1 } for p in paths { let info = m.Stat(p) catch { case fs.NotFound, fs.Denied, fs.Failed: continue } if !info.Dir && seen.Get(filepath.Clean(p)) == None { return false } } true } }`,
	"fstest RemoveAllTouchesOnlyPath":      `RemoveAllTouchesOnlyPath(paths []string, pick int) { let m = grown(paths) with context { fs.Files: m, fs.Writes: m } { let before = try fs.Walk(".") let target = before[(pick%len(before)+len(before))%len(before)] if target == "." { return true } try fs.RemoveAll(target) for q in before { let _ = m.Lstat(q) catch { case fs.NotFound: if !under(q, target) { return false } continue case fs.Denied, fs.Failed: return false } if under(q, target) { return false } } true } }`,
	"fstest MkdirTempIsNew":                `MkdirTempIsNew(paths []string, prefix string) { if strings.Contains(prefix, "/") { return true } let m = grown(paths) with context { fs.Files: m, fs.Writes: m } { let a = try fs.MkdirTemp(".", prefix) let b = try fs.MkdirTemp(".", prefix) let inA = try fs.Walk(a) let inB = try fs.Walk(b) a != b && filepath.Dir(a) == "." && filepath.Dir(b) == "." && len(inA) == 1 && len(inB) == 1 } }`,
	"fstest ScratchIsRemoved":              `ScratchIsRemoved(name string) { if name == "" || strings.Contains(name, "/") || name == "." || name == ".." { return true } let m = NewMemory() let made = func() string fails fs.NotFound | fs.Denied | fs.Failed { let s = try fs.NewScratch(m, m, ".", "t") try m.WriteFile(filepath.Join([]string{s.Path, name}), "x") s.Path } let path = try made() let _ = m.Lstat(path) catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } false }`,
	"filepath AbsFromIsAbsolute":           `AbsFromIsAbsolute(dir string, path string) { let d = "/" + dir IsAbs(AbsFrom(d, path)) && (!IsAbs(path) || AbsFrom(d, path) == Clean(path)) }`,
	"filepath MatchItself":                 `MatchItself(name string) { if strings.Contains(name, "*") || strings.Contains(name, "?") || strings.Contains(name, "[") || strings.Contains(name, "\\") { return true } try Match(name, name) }`,
	"signal StatusIsTheShells":             `StatusIsTheShells(pick int) { let k = stopOf((pick%3+3)%3 + 1) let s = int(StatusOf(k)) s == 128 + hostNumber(codeOf(k)) && !(1 <= s && s <= 125) }`,
	"signal RaisedInOrder":                 `RaisedInOrder(picks []int) { let mut raised = []Stop{} for p in picks { raised = raised.Append(stopOf((p%3+3)%3 + 1)) } let s = NewScript(raised) for k in raised { if s.Next() != k { return false } } s.Poll() == None }`,
	"signal PollTakesOnce":                 `PollTakesOnce(pick int) { let k = stopOf((pick%3+3)%3 + 1) let s = NewScript([]Stop{k}) s.Poll() == Some[Stop]{Value: k} && s.Poll() == None }`,
	"signal QuietRunsBody":                 `QuietRunsBody(n int) { let got = UntilStop(NewScript([]Stop{}), func() int { n }) catch { case Stopped: return false } got == n }`,
	"signal StopEndsBody":                  `StopEndsBody(pick int) { let k = stopOf((pick%3+3)%3 + 1) let cleaned = sync.NewRef[int](0) let started = queue.New[int](1) let never = queue.New[int](0) let _ = UntilStop(afterStart{started: started, k: k}, func() int { scope.OnExit() { cleaned.Update(func(v int) int { v + 1 }) } started.Push(1) catch { case queue.Closed: } never.Pop() catch { case queue.Closed: 0 } }) catch { case Stopped as st: return st.Kind == k && cleaned.Get() == 1 } false }`,
	"api ExitKeepsTheCode":                 `ExitKeepsTheCode(o Output) { let want = if o.Code >= 1 && o.Code <= 125 || o.Code >= 129 && o.Code <= 192 { o.Code } else if o.Code == 0 { 0 } else { 1 } ExitOf(o) catch { case os.Exit as e: return int(e.Code) == want && want != 0 } want == 0 }`,
	"api KebabRoundTrips":                  `KebabRoundTrips(words []string) { let mut name = "" let mut parts = []string{} for w in words { let low = word(w) parts = parts.Append(low) name = name + strings.ToUpper(low[0:1]) + low[1:] } let k = Kebab(name) k == strings.Join(parts, "-") && Kebab(k) == k }`,
	"api AgreesSeesTheLoss":                `AgreesSeesTheLoss(c string, o int) { Agrees(echo, c, o) && !Agrees(dropping, c+"!", o) }`,
	"api CompletionNamesEachCommand":       `CompletionNamesEachCommand(words []string, shell Shell) { let mut commands = []Entry{} for w in words { commands = commands.Append(Entry{Name: word(w), Doc: "does " + w + "\nmore"}) } let text = Completion("prog", shell, commands) let mut at = 0 for c in commands { let i = strings.Index(text[at:], c.Name) if i < 0 { return false } at = at + i + len(c.Name) } !strings.Contains(text, "\nmore") }`,
	"math ToInt64FailsOnlyOutside":         `ToInt64FailsOnlyOutside(f float64) { let n = ToInt64(f) catch { case OutOfRange: return IsNaN(f) || f >= 9223372036854775808.0 || Trunc(f) < -9223372036854775808.0 } float64(n) == Trunc(f) && n == int64(f) }`,
	"math ToIntFailsOnlyOutside":           `ToIntFailsOnlyOutside(f float64) { let n = ToInt(f) catch { case OutOfRange: return IsNaN(f) || f >= 9223372036854775808.0 || Trunc(f) < -9223372036854775808.0 } float64(n) == Trunc(f) && n == int(f) }`,
	"math ToUint64FailsOnlyOutside":        `ToUint64FailsOnlyOutside(f float64) { let n = ToUint64(f) catch { case OutOfRange: return IsNaN(f) || f >= 18446744073709551616.0 || Trunc(f) < 0.0 } float64(n) == Trunc(f) && n == uint64(f) }`,
	"math ToUintFailsOnlyOutside":          `ToUintFailsOnlyOutside(f float64) { let n = ToUint(f) catch { case OutOfRange: return IsNaN(f) || f >= 18446744073709551616.0 || Trunc(f) < 0.0 } float64(n) == Trunc(f) && n == uint(f) }`,
	"math ToUintptrFailsOnlyOutside":       `ToUintptrFailsOnlyOutside(f float64) { let n = ToUintptr(f) catch { case OutOfRange: return IsNaN(f) || f >= 18446744073709551616.0 || Trunc(f) < 0.0 } float64(n) == Trunc(f) && n == uintptr(f) }`,
	"api WordsReadLine":                    `WordsReadLine(args []string) { let back = Words(Line(args)) catch { case Unclosed: return false } back == args }`,
	"api SessionIsEachLine":                `SessionIsEachLine(script [][]string) { let mut lines = []string{} for a in script { lines = lines.Append(" " + Line(a)) lines = lines.Append("# " + Line(a)) lines = lines.Append("") } let mut want = []Step{} for k, a in script { if len(a) == 0 { continue } let o = echoed(a) want = want.Append(Step{Line: 3*k + 1, Text: Line(a), Output: o}) if o.Code != 0 { break } } Session(lines, echoed) == want }`,
	"os CodeWhere":                         `CodeWhere(v Code) { let it = int(v) ((1 <= it && it <= 125) || (129 <= it && it <= 192)) && try ParseCode(it) == v }`,
	"atomic InitialValues":                 `InitialValues(value int64, flag bool) { NewInt64(value).Load() == value && NewBool(flag).Load() == flag }`,
	"atomic Int64.StoresAndAdds":           `StoresAndAdds(a int64, b int64) { Store(a) let sum = Add(b) let seen = Load() let previous = Swap(a) let changed = CompareAndSwap(a, b) let unchanged = !CompareAndSwap(b+1, a) sum == a+b && seen == sum && previous == sum && changed && unchanged && Load() == b }`,
	"atomic Bool.Exchanges":                `Exchanges(a bool, b bool) { Store(a) let previous = Swap(b) let seen = Load() let changed = CompareAndSwap(b, a) let unchanged = !CompareAndSwap(!a, b) previous == a && seen == b && changed && unchanged && Load() == a }`,
	"atomic IncrementsCount":               `IncrementsCount(n int) { let k = 1 + (n%8+8)%8 let c = NewInt64(0) let s = tasks.NewSet(tasks.FailFast) for _ in 0..k { try tasks.Go(s, func() { _ = c.Add(1) }) } s.Wait() c.Load() == int64(k) }`,
	"embed EmptyHasNoFiles":                `EmptyHasNoFiles(path string) { let _ = NewEmpty().ReadFile(path) catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } false }`,
	"embed FS.ReadIsRepeatable":            `ReadIsRepeatable(path string) { let first = ReadFile(path) catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } try ReadFile(path) == first }`,
	"log AnnotationEndRestores":            `AnnotationEndRestores(key string, text string) { let before = Annotation(key) with Annotate(key, text) { Annotation(key) } Annotation(key) == before }`,
	"log InnerAnnotationShadows":           `InnerAnnotationShadows(key string, outer string, inner string) { with Annotate(key, outer) { with Annotate(key, inner) { Annotation(key) == Some[string]{Value: inner} } } }`,
	"prelude Task.StopIsIdempotent":        `StopIsIdempotent() { Stop() Stop() true }`,
	"queue EndKeepsValues":                 `EndKeepsValues(v int) { let q = New[int](1) try q.Push(v) q.End() try q.Pop() == v }`,
	"queue PushAfterEndReleases":           `PushAfterEndReleases() { let seen = New[int](8) let q = New[releaseCount](1) q.End() let r = releaseCount{Seen: seen} q.Push(r) catch { case Closed: return seen.Len() == 1 } false }`,
	"queue DrainReleasesLeft":              `DrainReleasesLeft(n int) { let k = (n%8 + 8) % 8 let seen = New[int](8) if true { let q = New[releaseCount](k) for _ in 0..k { let r = releaseCount{Seen: seen} try q.Push(r) } } seen.Len() == k }`,
	"queue FirstInFirstOut":                `FirstInFirstOut(a int, b int) { let q = New[int](2) try q.Push(a) try q.Push(b) let first = try q.Pop() let second = try q.Pop() first == a && second == b }`,
	"queue Queue.EndIsIdempotent":          `EndIsIdempotent() { End() End() true }`,
	"queue Queue.PushAfterEndFails":        `PushAfterEndFails(v T) { End() Push(v) catch { case Closed: return true } false }`,
	"queue Queue.ValuesStops":              `ValuesStops(k int) { if k < 1 { return true } End() let n = Len() let mut calls = 0 let s = Values() s(func(v T) bool { calls = calls + 1 calls < k }) let taken = if k < n { k } else { n } calls == taken && Len() == n-taken }`,
	"scope ActionsRunLastFirst":            `ActionsRunLastFirst() { let mut order = "" let f = func() { OnExit() { order = order + "a" } OnExit() { order = order + "b" } } f() order == "ba" }`,
	"task StopIsIdempotent":                `StopIsIdempotent() { let t = with context { Scheduler: time.NewManual(0) } { Go() { Scheduler.Sleep(1h) 0 } } t.Stop() t.Stop() true }`,
	"task WaitReturnsTheBlock":             `WaitReturnsTheBlock(n int) { let t = Go() { n } t.Wait() == n }`,
	"task DefectIsObservedOnce":            `DefectIsObservedOnce(n int) { let k = ((n % 3) + 3) % 3 let t = Go() { try ended(k, n) } match ExitOf(t) { case Defected as d: k == 0 && strings.HasPrefix(d.Reason, "unreachable at ") && strings.HasSuffix(d.Reason, ": crash \(n)") case Failed[odd] as f: k == 1 && f.Error.N == n case Done[int] as v: k == 2 && v.Value == n } }`,
	"task ExitOfWaitsForCleanup":           `ExitOfWaitsForCleanup(n int) { let k = ((n % 4) + 4) % 4 let marks = queue.New[int](4) let t = Go() { crashed(k, marks) } match ExitOf(t) { case Defected: marks.Len() == k case _: false } }`,
	"task ExitOfAnywhere":                  `ExitOfAnywhere(n int) { let outer = Go() { Go() { n } } let t = outer.Wait() match ExitOf(t) { case Done[int] as d: d.Value == n case _: false } }`,
	"task StopOfWaiterStillInterrupts":     `StopOfWaiterStillInterrupts() { let m = time.NewManual(0) with context { Scheduler: m } { let waiter = Go() { let sleeper = Go() { Scheduler.Sleep(1h) 0 } let _ = ExitOf(sleeper) Unreachable("ExitOf returned to a stopped waiter") } for m.Sleepers() == 0 { } waiter.Stop() m.Sleepers() == 0 } }`,
	"testing HeldCallWaitsForRelease":      `HeldCallWaitsForRelease(k string, a int) { let s = Script[probe]{} s.Get.Answer(func(key string) int fails Absent { a }) s.Get.Hold() let c = NewVirtualClock(0) with context { probe: s, Scheduler: c } { let t = task.Go() { probe.Get(k) } Scheduler.Sleep(1s) let held = s.Get.Held() let waited = len(held) == 1 && held[0] == k && len(s.Get.Calls()) == 1 s.Get.Release(func(key string) int fails Absent { a + 1 }) let got = t.Wait() catch { case Absent: a } s.Get.Answer(func(key string) int fails Absent { a + 2 }) let later = probe.Get(k) catch { case Absent: a } waited && got == a + 1 && later == a + 2 && len(s.Get.Held()) == 0 } }`,
	"testing ReleaseAnswersOldestFirst":    `ReleaseAnswersOldestFirst(k string, j string, a int) { let s = Script[probe]{} s.Get.Hold() let c = NewVirtualClock(0) with context { probe: s, Scheduler: c } { let first = task.Go() { probe.Get(k) } Scheduler.Sleep(1s) let second = task.Go() { probe.Get(j) } Scheduler.Sleep(1s) let held = s.Get.Held() let order = len(held) == 2 && held[0] == k && held[1] == j s.Get.Release(func(key string) int fails Absent { a }) s.Get.Release(func(key string) int fails Absent { a + 1 }) let one = first.Wait() catch { case Absent: a - 1 } let two = second.Wait() catch { case Absent: a - 1 } s.Get.Release(func(key string) int fails Absent { a + 2 }) let three = probe.Get(k) catch { case Absent: a - 1 } order && one == a && two == a + 1 && three == a + 2 && len(s.Get.Held()) == 0 } }`,
	"testing ScriptNamesTheHeldCall":       `ScriptNamesTheHeldCall(k string) { let s = Script[probe]{} s.Get.Hold() let c = NewVirtualClock(0) with context { probe: s, Scheduler: c } { let t = task.Go() { probe.Get(k) } Scheduler.Sleep(1s) let report = Unanswered() let named = strings.HasPrefix(report, "testing.Script[") && strings.HasSuffix(report, "probe]: Get has a held call that no Release answered") let unread = len(s.Get.Unread()) == 1 && len(s.Get.Calls()) == 1 && len(s.Get.Unread()) == 0 s.Get.Release(func(key string) int fails Absent { 0 }) let _ = t.Wait() catch { case Absent: 0 } named && unread && Unanswered() == "" } }`,
	"timeout AfterKeepsWhatTheBodyTook":    `AfterKeepsWhatTheBodyTook(c int, w int) { let m = time.NewManual(0) with context { Scheduler: m } { let n = 40 let q = queue.New[int](((c % 3) + 3) % 3) let kept = queue.New[int](n) let producer = task.Go() { for i in 0..n { Scheduler.Sleep(1ms) q.Push(i) catch { case queue.Closed: } } q.End() } let consumer = task.Go() { let mut done = false for !done { let v = After(time.Times(1ms, 1+((w%3)+3)%3)) { try q.Pop() } catch { case Expired: -1 case queue.Closed: -2 } if v == -2 { done = true } else if v >= 0 { kept.Push(v) catch { case queue.Closed: } } } kept.End() } for _ in 0..n+2 { m.Advance(1ms) } producer.Wait() consumer.Wait() let mut next = 0 for v in kept.Values() { if v == next { next = next + 1 } } next == n } }`,
	"timeout PopWithinWaitsUpToD":          `PopWithinWaitsUpToD(g int, w int) { let m = time.NewManual(0) let gap = ((g % 60) + 60) % 60 let d = ((w%59)+59)%59 + 1 let start = m.Now() with context { Scheduler: m } { let q = queue.New[int](1) let got = queue.New[int](3) let producer = task.Go() { Scheduler.Sleep(time.Times(1ms, gap)) q.Push(7) catch { case queue.Closed: } } let consumer = task.Go() { let v = PopWithin(q, time.Times(1ms, d)) catch { case Expired: -1 case queue.Closed: -2 } got.Push(v) catch { case queue.Closed: } got.Push(time.Count(m.Elapsed(start), 1ms)) catch { case queue.Closed: } got.Push(m.Sleepers()) catch { case queue.Closed: } } m.Advance(120ms) producer.Wait() consumer.Wait() let v = got.Poll() let at = got.Poll() let asleep = got.Poll() let took = v == Some[int]{Value: 7} && at == Some[int]{Value: gap} && q.Len() == 0 && asleep == Some[int]{Value: 0} let expired = v == Some[int]{Value: -1} && at == Some[int]{Value: d} && q.Len() == 1 if gap < d { took } else if gap > d { expired } else { took || expired } } }`,
	"timeout PopWithinTakesWhatIsThere":    `PopWithinTakesWhatIsThere(v int) { with context { Scheduler: time.NewManual(0) } { let q = queue.New[int](1) try q.Push(v) q.End() let first = try PopWithin(q, 0ns) let then = PopWithin(q, 0ns) catch { case queue.Closed: -1 case Expired: -2 } first == v && then == -1 && q.Len() == 0 } }`,
	"timeout PopBeforeKeepsTheDeadline":    `PopBeforeKeepsTheDeadline(gaps []int, w int) { let m = time.NewManual(0) let big = 2*(((w%30)+30)%30) + 1 let n = len(gaps) let start = m.Now() with context { Scheduler: m } { let q = queue.New[int](n + 1) let got = queue.New[int](n + 4) let producer = task.Go() { for i, g in gaps { Scheduler.Sleep(time.Times(1ms, 2*(((g%10)+10)%10))) q.Push(i) catch { case queue.Closed: } } Scheduler.Sleep(time.Times(1ms, 2*big)) q.End() } let consumer = task.Go() { let dl = NewDeadline() dl.Arm(time.Times(1ms, big)) got.Push(if dl.Armed() { 1 } else { 0 }) catch { case queue.Closed: } let mut open = true for open { let v = PopBefore(q, dl) catch { case Expired: -1 case queue.Closed: -2 } if v >= 0 { got.Push(v) catch { case queue.Closed: } } else { open = false got.Push(v) catch { case queue.Closed: } got.Push(time.Count(m.Elapsed(start), 1ms)) catch { case queue.Closed: } got.Push(if dl.Armed() { 1 } else { 0 }) catch { case queue.Closed: } } } got.End() } m.Advance(time.Times(1ms, 20*n+4*big+2)) producer.Wait() consumer.Wait() let mut want = []int{1} let mut at = 0 for i, g in gaps { at = at + 2*(((g%10)+10)%10) if at < big { want = want.Append(i) } } let left = n - (len(want) - 1) want = want.Append(-1).Append(big).Append(0) let mut same = true let mut k = 0 for v in got.Values() { same = same && k < len(want) && v == want[k] k = k + 1 } same && k == len(want) && q.Len() == left } }`,
	"timeout DeadlineFirstExpires":         `DeadlineFirstExpires() { let m = time.NewManual(0) with context { Scheduler: m } { After(1ms) { for m.Sleepers() == 0 { } m.Advance(1ms) Scheduler.Sleep(1h) false } catch { case Expired: true } } }`,
	"testing ScriptAnswersByTheLastAnswer": `ScriptAnswersByTheLastAnswer(k string, a int, b int) { let s = Script[probe]{} s.Size.Answer(func() int { a }) s.Get.Answer(func(key string) int fails Absent { Absent{Key: key} }) with context { probe: s } { let first = probe.Size() s.Size.Answer(func() int { b }) let second = probe.Size() let got = probe.Get(k) catch { case Absent as e: if e.Key == k { -1 } else { -2 } } first == a && second == b && got == -1 && len(s.Size.Calls()) == 2 && s.Size.Calls()[0] == NoArgs && s.Get.Calls()[0] == k } }`,
	"testing WorldSharesEachPair":          `WorldSharesEachPair(text string, n int64) { let d = span(n) with World() { try fs.Writes.WriteFile("f", text) let back = try fs.Files.ReadFile("f") let before = time.Clock.Now() Scheduler.Sleep(d) back == text && time.Clock.Now().Sub(before) == d } }`,
	"testing WorldsAreFresh":               `WorldsAreFresh(text string) { with World() { try fs.Writes.WriteFile("f", text) } with World() { fs.Files.Stat("f") catch { case fs.NotFound: return true case fs.Denied, fs.Failed: return false } false } }`,
	"testing ScriptRecordsEachCall":        `ScriptRecordsEachCall(ks []string, n int) { let s = Script[probe]{} s.Has.Answer(func(k string) bool { len(k) > 0 }) with context { probe: s } { for i, k in ks { probe.Put(k, n + i) let _ = probe.Has(k) } } let puts = s.Put.Calls() let mut same = len(puts) == len(ks) && len(s.Has.Calls()) == len(ks) && len(s.Size.Calls()) == 0 for i, k in ks { same = same && puts[i] == (k, n + i) && s.Has.Calls()[i] == k } same }`,
	"timeout QuickBodyWins":                `QuickBodyWins(n int) { with context { Scheduler: time.NewManual(0) } { try After(1h) { n } == n } }`,
	"pace Pacing.EdgeKeepsValues":          `EdgeKeepsValues(xs []int, s string) { let kept = xs.Append(len(s)) let text = s + "." Edge() let mut same = len(kept) == len(xs)+1 && kept[len(xs)] == len(s) && text == s+"." for i, x in xs { same = same && kept[i] == x } same }`,
	"pace DefaultReachesNothing":           `DefaultReachesNothing(xs []int, n int) { let kept = xs.Append(n) with context { Pacing: Default } { for _ in 0..within(n, 0, 64) { Pacing.Edge() } } len(kept) == len(xs)+1 && kept[len(xs)] == n }`,
	"pace NextReadLeavesRoom":              `NextReadLeavesRoom(grown int64, edges int64, room int64) { let g = int64(within(int(grown%1000000000), 0, 999999999)) let e = int64(within(int(edges%1000), 1, 1000)) let perEdge = if g/e < 1 { int64(1) } else { g / e } let r = perEdge*int64(within(int(room%1000), -2, 600)) + int64(within(int(room%7), 0, 6)) let next = NextRead(g, e, r) 1 <= next && next <= MaxSpan && (next == 1 || g == 0 || next*perEdge <= r/2) }`,
	"pace CountingCountsEachEdge":          `CountingCountsEachEdge(n int) { let k = within(n, 0, 64) let c = NewCounting() let before = c.Edges() with context { Pacing: c } { for _ in 0..k { Pacing.Edge() } } before == 0 && c.Edges() == int64(k) }`,
	"pace Pacing.HoldKeepsValues":          `HoldKeepsValues(xs []int, n int) { let kept = xs.Append(n) Hold() Edge() Release() len(kept) == len(xs)+1 && kept[len(xs)] == n }`,
	"pace HoldsOweTheLastClose":            `HoldsOweTheLastClose(ops []int) { let h = NewHolds() let mut depth = 0 let mut owed = false let mut same = true for op in ops { let kind = within(op, 0, 2) if kind == 0 { h.Open() depth = depth + 1 } else if kind == 1 && depth > 0 { let collects = h.Close() depth = depth - 1 same = same && collects == (depth == 0 && owed) if depth == 0 { owed = false } } else if kind == 2 { same = same && h.Owe() == (depth > 0) owed = owed || depth > 0 } same = same && h.Depth() == int64(depth) } same }`,
	"pace HeldDefersToTheLastRelease":      `HeldDefersToTheLastRelease(n int, k int, stop bool) { let edges = within(n, 0, 16) let depth = within(k, 1, 4) let c = NewCounting() with context { Pacing: c } { nest(depth, edges, stop) catch { case stopped: } Pacing.Edge() } let owed = if edges > 0 { int64(1) } else { int64(0) } c.Edges() == int64(edges+1) && c.HeldEdges() == int64(edges) && c.Deferred() == owed }`,
	"pace EveryOneIsParent":                `EveryOneIsParent(n int, k int) { let edges = within(n, 0, 64) let holds = within(k, 0, 4) let c = NewCounting() with context { Pacing: NewEvery(c, 1) } { for _ in 0..holds { Pacing.Hold() } for _ in 0..edges { Pacing.Edge() } for _ in 0..holds { Pacing.Release() } } let held = if holds > 0 { edges } else { 0 } let owed = if holds > 0 && edges > 0 { int64(1) } else { int64(0) } c.Edges() == int64(edges) && c.HeldEdges() == int64(held) && c.Deferred() == owed }`,
	"pace EveryPassesOneInN":               `EveryPassesOneInN(n int, k int) { let every = within(n, 1, 9) let edges = within(k, 0, 200) let c = NewCounting() with context { Pacing: NewEvery(c, every) } { for _ in 0..edges { Pacing.Edge() } } c.Edges() == int64(edges/every) }`,
	"pace EveryNests":                      `EveryNests(a int, b int, k int) { let x = within(a, 1, 6) let y = within(b, 1, 6) let edges = within(k, 0, 200) let nested = NewCounting() let flat = NewCounting() let outer = NewEvery(NewEvery(nested, y), x) let one = NewEvery(flat, x*y) let mut same = true for _ in 0..edges { with context { Pacing: outer } { Pacing.Edge() } with context { Pacing: one } { Pacing.Edge() } same = same && nested.Edges() == flat.Edges() } same }`,
	"pace QuietForwardsHolds":              `QuietForwardsHolds(n int, k int) { let edges = within(n, 0, 64) let holds = within(k, 0, 4) let c = NewCounting() with context { Pacing: NewQuiet(c) } { for _ in 0..holds { Pacing.Hold() } for _ in 0..edges { Pacing.Edge() } c.Edge() for _ in 0..holds { Pacing.Release() } } c.Edge() let held = if holds > 0 { int64(1) } else { int64(0) } c.Edges() == 2 && c.HeldEdges() == held && c.Deferred() == held }`,
	"pace RaisesFollowModel":               `RaisesFollowModel(ops []int) { let r = NewRaises() let mut open = []int{} let mut same = true for op in ops { let percent = 100 * within(op/2, 1, 8) if within(op, 0, 1) == 0 { let got = r.Raise(percent) open = open.Append(percent) same = same && got == largest(open) } else if len(open) > 0 { let i = within(op/16, 0, len(open)-1) let gone = open[i] let (_, rest) = open.TakeAt(i) open = rest let got = r.Lower(gone) same = same && got == (if len(open) == 0 { None } else { Some[int]{Value: largest(open)} }) } same = same && r.Open() == len(open) } same }`,
	"trace InnerSpanShadows":               `InnerSpanShadows(outer string, inner string) { with Span(outer) { with Span(inner) { Current() == Some[string]{Value: inner} } } }`,
	"trace SpanEndRestores":                `SpanEndRestores(name string) { let before = Current() with Span(name) { Current() } Current() == before }`,
}

// lawsPassed is the lawledger checks of a test build's run: the law of
// each key passes when stdout has the ok line of its probe test, named by
// testName. Every test of the probe is a law's, or one of extra: any other
// probe test fails t.
func lawsPassed(t *testing.T, statements map[string]string, testName func(key string) string, probe, stdout string, extra ...string) map[string]lawledger.Law {
	t.Helper()
	laws := map[string]lawledger.Law{}
	named := map[string]bool{}
	for _, name := range extra {
		named[name] = true
	}
	for key, statement := range statements {
		name := testName(key)
		named[name] = true
		laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			if !regexp.MustCompile(`(?m)^ok ` + name + `( \((\d+ cases|proved: all \d+ cases)\))?$`).MatchString(stdout) {
				t.Errorf("no ok line for %s:\n%s", name, stdout)
			}
		}}
	}
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)`).FindAllStringSubmatch(probe, -1) {
		if !named[m[1]] {
			t.Errorf("the probe test %s restates no law of this runner and is not one of its extra tests %v", m[1], extra)
		}
	}
	return laws
}

// lawProbe runs the tests of the law probe name, a test build its caller
// declares in a program table (testBuildProgram(name, probe)), with --seed
// 1, and returns its laws for the runner's lawledger.Run: the probe's test
// of each ledger key is named by stdLawsTestName, and statements is each
// law's restatement by key (fu-ledger-runner; the runners of S2 and S3
// moved onto it at the batch-10 merge).
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func lawProbe(t *testing.T, name, probe string, statements map[string]string) map[string]lawledger.Law {
	t.Helper()
	stdout, stderr, code := runExeArgs(t, tableExe(t, name), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of %s failed: exit %d\n%s\n%s", name, code, stdout, firstLines(stderr, 20))
	}
	return lawsPassed(t, statements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, probe, stdout)
}

// TestStdLawsRun runs the probe's tests; lawledger.Run holds that the
// probe restates exactly the ledger's laws whose runner is this test, each
// with the row's statement, and that each passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdlaws"), "--seed", "1")
	if code != 0 {
		t.Errorf("the std law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	parsed.Run(t, lawsPassed(t, stdLawsStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, stdLawsProbe, stdout))
}

// stdLawsZeroCost is a program that calls every std package with a law in
// this unit: a law adds no Go to a build that imports its package
// (types.md, Laws 11).
const stdLawsZeroCost = `import queue
import task
import timeout
import scope
import trace
import log

func run() fails queue.Closed | timeout.Expired needs Console | Scheduler {
	let q = queue.New[int](1)
	try q.Push(1)
	q.End()
	let t = task.Go() { try q.Pop() }
	let v = try t.Wait()
	scope.OnExit() { Console.Println("exit") }
	let w = try timeout.After(1h) { v + 1 }
	with trace.Span("s") {
		with log.Annotate("k", "x") {
			Console.Println("\(w) \(trace.Current() == None) \(log.Annotation("k") == None)")
		}
	}
}

func main() fails queue.Closed | timeout.Expired needs Console | Ambient {
	with context { Scheduler: NewLiveScheduler() } { try run() }
}
`

// stdLawsTail is declared at the end of both programs below; stdLawsOnly
// is a law appended to the second, with an import only it uses (hex), a
// generic instance only it makes (queue.New[string]) and a failure of
// LawOnly only it raises, so LawOnly is an error type only in a law.
const stdLawsTail = `
type LawOnly { n int }
`

const stdLawsOnly = `
law OnlyALawUsesThem(s string, n int) {
	let q = queue.New[string](1)
	try q.Push(hex.Encode(s))
	let f = func(k int) int fails LawOnly {
		if k < 0 {
			return LawOnly{n: k}
		}
		k
	}
	let m = f(n) catch {
	case LawOnly:
		0
	}
	try q.Pop() == hex.Encode(s) && m >= 0
}
`

// TestStdLawsAddNoGo: under both call lowerings (inline.go), the program
// above lowers to Go that type-checks and names no law of the std packages
// it imports, and a law appended to it, with an import, a generic instance
// and an error type only the law uses, changes no byte of its Go (types.md,
// Laws 11; a blank line stands for the import, so the lines agree). It
// replaces a golden of the Go text: the text is no promise, the absence of
// every law is.
func TestStdLawsAddNoGo(t *testing.T) {
	names := map[string]bool{"OnlyALawUsesThem": true}
	for _, p := range []string{"queue", "task", "timeout", "scope", "trace", "log"} {
		files, ok := stdlib.Package(p)
		if !ok {
			t.Fatalf("no std package %s", p)
		}
		for _, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if d, ok := n.(*syntax.LawDecl); ok {
					names[d.Name.Name] = true
				}
				return true
			})
		}
	}
	var programs []*lowered
	for _, ref := range []bool{false, true} {
		var texts []string
		for _, src := range []string{"\n" + stdLawsZeroCost + stdLawsTail, "import hex\n" + stdLawsZeroCost + stdLawsTail + stdLawsOnly} {
			name := fmt.Sprintf("stdlawszero%d", len(programs))
			o, _ := lowerInlineMode(t, name, src, ref)
			programs = append(programs, o)
			text := o.mainGo()
			for law := range names {
				if regexp.MustCompile(`\b` + law + `\b`).MatchString(text) {
					t.Errorf("%s (reference %v): the Go names the law %s", name, ref, law)
				}
			}
			texts = append(texts, text)
		}
		if texts[0] != texts[1] {
			t.Errorf("reference %v: a law changes the Go:\n%s", ref, firstDiff(texts[0], texts[1]))
		}
	}
	typecheckGo(t, programs)
}

// firstDiff is the first differing line of a and b, with its number.
func firstDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(x), len(y)) {
		var p, q string
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			return "line " + strconv.Itoa(i+1) + ":\n-" + p + "\n+" + q
		}
	}
	return ""
}
