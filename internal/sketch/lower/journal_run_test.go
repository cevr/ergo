package lower

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// journalLawsProbe restates the laws of std journal and journal/memory
// (MU9) as tests: the Journal laws over memory.New after a generated
// prefix, Codec.RoundTrips over a codec of ints, and the durable actor's
// laws with a crashing journal of the probe's own (a program cannot build
// a memory.Journal with a failing append, so it wraps one).
const journalLawsProbe = `import actor
import journal
import journal/memory
import machines
import queue
import strconv
import time

func same(a Option[int], b Option[int]) bool {
	match (a, b) {
	case (Some[int], Some[int]):
		true
	case (None, None):
		true
	case (_, _):
		false
	}
}

func filled(pre []int, snapAt int) journal.Journal[int, int] {
	let j journal.Journal[int, int] = memory.New[int, int]()
	for i, x in pre {
		j.Append(x) catch {
		case journal.Failed:
		}
		if i == snapAt {
			j.Snapshot(x) catch {
			case journal.Failed:
			}
		}
	}
	j
}

func appendAddsOne(j journal.Journal[int, int], e int) bool {
	let before = j.Load() catch {
	case journal.Failed:
		return true
	}
	j.Append(e) catch {
	case journal.Failed:
		return true
	}
	let after = j.Load() catch {
	case journal.Failed:
		return false
	}
	len(after.Signals) == len(before.Signals)+1 && same(after.Snapshot, before.Snapshot)
}

func snapshotDropsSignals(j journal.Journal[int, int], s int) bool {
	j.Snapshot(s) catch {
	case journal.Failed:
		return true
	}
	let after = j.Load() catch {
	case journal.Failed:
		return false
	}
	len(after.Signals) == 0 && same(after.Snapshot, Some[int]{Value: s})
}

test JournalJournalAppendAddsOne(pre []int, snapAt int, e int) { assert appendAddsOne(filled(pre, snapAt), e) }

test JournalJournalSnapshotDropsSignals(pre []int, snapAt int, s int) {
	assert snapshotDropsSignals(filled(pre, snapAt), s)
}

type Ints {}

func Encode(_ Ints, v int) string { strconv.Itoa(v) }

func Decode(_ Ints, text string) int fails journal.Failed {
	strconv.Atoi(text) catch {
	case strconv.Malformed, strconv.OutOfRange:
		return journal.Failed{Reason: text}
	}
}

func roundTrips(c journal.Codec[int], v int) bool {
	let text = c.Encode(v)
	let back = c.Decode(text) catch {
	case journal.Failed:
		return false
	}
	c.Encode(back) == text
}

test JournalCodecRoundTrips(v int) { assert roundTrips(Ints, v) }

func tallyStep(s int, e int) Decision[int, int] {
	match ((e % 5) + 5) % 5 {
	case 0:
		Enter[int]{Next: ((s*3 + e%1009) % 1009 + 1009) % 1009}
	case 1:
		Stay[int]{Next: (s + 1) % 1009}
	case 2:
		Ignore
	case 3:
		Reject
	case _:
		Emit[int, int]{Next: (s * 2) % 1009, Output: s}
	}
}

func untimed(s int) Option[Timeout[int]] { None }

func unworked(s int) Option[func(send func(e int))] { None }

func tally() Machine[int, int, int] { Machine[int, int, int]{Initial: 0, Step: tallyStep, After: untimed, Work: unworked} }

func fold(signals []int) int {
	let o = machines.From(tally(), 0, signals) catch {
	case machines.Rejected:
		return -1
	}
	match o {
	case machines.Running[int, int] as r:
		r.State
	case machines.Finished[int, int]:
		-1
	}
}

func within(want []int, all []int) bool {
	let mut i = 0
	for x in all {
		if i < len(want) && want[i] == x {
			i = i + 1
		}
	}
	i == len(want)
}

// Crashing is a memory journal whose next Append fails when armed: mode 1
// before it writes, mode 2 after; all is every signal it wrote.
type Crashing {
	inner  memory.Journal[int, int],
	append escaping func(e int) fails journal.Failed,
	arm    escaping func(mode int),
	all    escaping func() []int,
}

func Append(c Crashing, e int) fails journal.Failed { try c.append(e) }

func Snapshot(c Crashing, s int) fails journal.Failed { try c.inner.Snapshot(s) }

func Load(c Crashing) journal.Log[int, int] fails journal.Failed { try c.inner.Load() }

func crashing() Crashing {
	let inner = memory.New[int, int]()
	let mut armed = 0
	let mut all = []int{}
	Crashing{
		inner: inner,
		append: func(e int) fails journal.Failed {
			let mode = armed
			armed = 0
			if mode == 1 {
				return journal.Failed{Reason: "crash before the write"}
			}
			try inner.Append(e)
			all = all.Append(e)
			if mode == 2 {
				return journal.Failed{Reason: "crash after the write"}
			}
		},
		arm: func(mode int) { armed = mode },
		all: func() []int { all },
	}
}

func recovers(j journal.Journal[int, int], arm func(mode int), all func() []int, ops []int, every int) bool needs Scheduler {
	let n = ((every % 4) + 4) % 4
	let mut a = journal.Start(tally(), j, n, 4, func(o int) {}) catch {
	case _:
		return false
	}
	let mut want = []int{}
	for x in ops {
		let k = ((x % 9) + 9) % 9
		let mut crashed = false
		if k == 8 {
			a.Stop()
			crashed = true
		} else if k == 7 {
			arm(1 + ((x / 9) % 2 + 2) % 2)
		} else {
			let d = a.Ask(x) catch {
			case actor.Stopped:
				crashed = true
				Ignore
			}
			match d {
			case Enter[int]:
				want = want.Append(x)
			case Stay[int]:
				want = want.Append(x)
			case Emit[int, int]:
				want = want.Append(x)
			case _:
			}
			if !within(want, all()) {
				return false
			}
		}
		if crashed {
			a = journal.Start(tally(), j, n, 4, func(o int) {}) catch {
			case _:
				return false
			}
			if a.State() != fold(all()) {
				return false
			}
		}
	}
	let last = a.State()
	a.Stop()
	last == fold(all()) && within(want, all())
}

func RecoveryIsTheFold(ops []int, every int) bool {
	with context { Scheduler: time.NewManual(0) } {
		let c = crashing()
		recovers(c, c.arm, c.all, ops, every)
	}
}

test MemoryRecoveryIsTheFold(ops []int, every int) { assert RecoveryIsTheFold(ops, every) }

func relayStep(s int, e int) Decision[int, int] {
	match (s, e) {
	case (0, 10):
		Emit[int, int]{Next: 1, Output: 0}
	case (1, 11):
		Enter[int]{Next: 2}
	case (_, 12):
		Stay[int]{Next: s + 100}
	case (_, _):
		Ignore
	}
}

func dropped(j journal.Journal[int, int]) bool needs Scheduler {
	let start = queue.New[int](1)
	let gate = queue.New[int](1)
	let sent = queue.New[int](1)
	let relay = Machine[int, int, int]{Initial: 0, Step: relayStep, After: func(s int) Option[Timeout[int]] { None }, Work: func(s int) Option[func(send func(e int))] {
		if s != 1 {
			return None
		}
		Some[func(send func(e int))]{Value: func(send func(e int)) {
			gate.Pop() catch {
			case queue.Closed:
				return
			}
			send(12)
			sent.Push(1) catch {
			case queue.Closed:
			}
		}}
	}}
	let a = journal.Start(relay, j, 0, 4, func(o int) {
		start.Pop() catch {
		case queue.Closed:
			return
		}
		gate.Push(1) catch {
		case queue.Closed:
		}
		sent.Pop() catch {
		case queue.Closed:
		}
	}) catch {
	case _:
		return false
	}
	a.Send(10) catch {
	case actor.Stopped:
		return false
	}
	a.Send(11) catch {
	case actor.Stopped:
		return false
	}
	start.Push(1) catch {
	case queue.Closed:
		return false
	}
	a.Ask(13) catch {
	case actor.Stopped:
		return false
	}
	let state = a.State()
	let drops = a.Dropped()
	a.Stop()
	let log = j.Load() catch {
	case journal.Failed:
		return false
	}
	let b = journal.Start(relay, j, 0, 4, func(o int) {}) catch {
	case _:
		return false
	}
	let back = b.State()
	b.Stop()
	drops == 1 && state == 2 && back == 2 && len(log.Signals) == 2 && log.Signals[0] == 10 && log.Signals[1] == 11
}

func DroppedIsNotJournaled() bool {
	with context { Scheduler: time.NewManual(0) } {
		let j journal.Journal[int, int] = memory.New[int, int]()
		dropped(j)
	}
}

test MemoryDroppedIsNotJournaled() { assert DroppedIsNotJournaled() }
`

// journalLawStatements is the statement of each journal law, as the
// ledger writes it.
var journalLawStatements = map[string]string{
	"journal Codec.RoundTrips":             `RoundTrips(v T) { let text = Encode(v) let back = Decode(text) catch { case Failed: return false } Encode(back) == text }`,
	"journal Journal.AppendAddsOne":        `AppendAddsOne(e E) { let before = Load() catch { case Failed: return true } Append(e) catch { case Failed: return true } let after = Load() catch { case Failed: return false } len(after.Signals) == len(before.Signals)+1 && same(after.Snapshot, before.Snapshot) }`,
	"journal Journal.SnapshotDropsSignals": `SnapshotDropsSignals(s S) { Snapshot(s) catch { case Failed: return true } let after = Load() catch { case Failed: return false } len(after.Signals) == 0 && same(after.Snapshot, Some[S]{Value: s}) }`,
	"journal/memory DroppedIsNotJournaled": `DroppedIsNotJournaled() { with context { Scheduler: time.NewManual(0) } { dropped(New[int, int]()) } }`,
	"journal/memory RecoveryIsTheFold":     `RecoveryIsTheFold(ops []int, every int) { with context { Scheduler: time.NewManual(0) } { let (j, arm, all) = crashing() recovers(j, arm, all, ops, every) } }`,
}

// journalLawTestName is the probe's test of a ledger key: the last element
// of the package path, capitalized, then the law ("journal/memory
// RecoveryIsTheFold" is MemoryRecoveryIsTheFold).
func journalLawTestName(key string) string {
	pkg, law, _ := strings.Cut(key, " ")
	return stdLawsTestName(pkg[strings.LastIndex(pkg, "/")+1:], law)
}

// TestStdJournalLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. It
// builds and runs because the laws run the durable actor of std actor and
// journal on the lowered runtime, as ergo test runs them; no interpreter
// has the actor runner. MemoryRecoveryIsTheFold is the model-based test of
// durable.qnt (MU9's acceptance).
func TestStdJournalLawsRun(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "stdjournal", journalLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdjournal"], "--seed", "1")
	if code != 0 {
		t.Errorf("the journal law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, journalLawStatements, journalLawTestName, journalLawsProbe, stdout))
}

// journalFileProgram counts in a file journal: each run recovers the
// counter from the file its argument names, sends Go and two Bumps, and
// prints the count. A snapshot every 2 signals rewrites the file.
const journalFileProgram = `import fs
import journal
import journal/file
import os
import strconv
import time

type Idle {}
type Busy { N int }
type State = Idle | Busy

type Go {}
type Bump {}
type Signal = Go | Bump

func step(s State, e Signal) Decision[State, int] {
	match (s, e) {
	case (Idle, Go):
		Enter[State]{Next: Busy{N: 0}}
	case (Busy as b, Bump):
		Stay[State]{Next: Busy{N: b.N + 1}}
	case (_, _):
		Ignore
	}
}

type Counter machine[State, Signal] {
	initial: Idle,
	step: step,
}

type States {}

func Encode(_ States, s State) string {
	match s {
	case Idle:
		"idle"
	case Busy as b:
		strconv.Itoa(b.N)
	}
}

func Decode(_ States, text string) State fails journal.Failed {
	if text == "idle" {
		return Idle
	}
	let n = strconv.Atoi(text) catch {
	case strconv.Malformed, strconv.OutOfRange:
		return journal.Failed{Reason: "bad state \(text)"}
	}
	Busy{N: n}
}

type Signals {}

func Encode(_ Signals, e Signal) string {
	match e {
	case Go:
		"go"
	case Bump:
		"bump"
	}
}

func Decode(_ Signals, text string) Signal fails journal.Failed {
	match text {
	case "go":
		Go
	case "bump":
		Bump
	case _:
		journal.Failed{Reason: "bad signal \(text)"}
	}
}

func run(host fs.Host, path string) fails journal.Failed | journal.Diverged needs Console | Scheduler {
	let j = try file.Open(host, path, States, Signals)
	let a = try journal.Start(Counter{}, j, 2, 4, func(o int) {})
	for e in []Signal{Go, Bump, Bump} {
		a.Send(e) catch {
		case _:
		}
	}
	a.Drain()
	match a.State() {
	case Busy as x:
		Console.Println("count \(x.N)")
	case Idle:
		Console.Println("idle")
	}
}

func main() needs Console | Ambient {
	let path = os.NewHost().Args()[0]
	with context { Scheduler: time.NewManual(0) } {
		run(fs.NewHost(), path) catch {
		case journal.Failed as f:
			Console.Println("failed \(f.Reason)")
		case journal.Diverged as d:
			Console.Println("diverged \(d.At)")
		}
	}
}
`

// TestJournalFileRecovers runs journalFileProgram three times on one file:
// each run recovers the count of the runs before it, from a snapshot and
// the signals after it. It builds and runs a binary because the recovery
// crosses a process's end on the host's file system, which no in-process
// check has, and no law can make an fs.Host (the mutant it kills: Open's
// parse skips the snapshot line, so the next process replays the signals
// after the snapshot from the initial state).
func TestJournalFileRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter.journal")
	exes := buildAll(t, []*lowered{mustLower(t, "journalfile", journalFileProgram)}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	for i, want := range []string{"count 2\n", "count 4\n", "count 6\n"} {
		stdout, stderr, code := runExeArgs(t, exes["journalfile"], path)
		if stdout != want || code != 0 {
			t.Fatalf("run %d: exit %d, stdout %q, want %q\nstderr %s", i+1, code, stdout, want, firstLines(stderr, 20))
		}
	}
}
