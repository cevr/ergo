package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// Queue.Poll is the first std operation whose Ergo result is the prelude's
// Option[T] (std-align S3d). Its Go body returns Go's comma-ok (T, bool),
// and the lowering adapts the call to Some or None with the helper the
// prelude's Get uses (optionReturn; stdlib.md, Types 5).

// queuePollAllocSource has a function that polls (an operation call, so it
// takes an environment, pure.go); queuePollAllocDriver calls it from Go
// beside the emitted code on an empty queue and counts its allocations:
// the None path makes no value on the heap, as Go's select with a default
// case makes none.
const queuePollAllocSource = `import queue

func poll(q queue.Queue[int]) Option[int] {
	q.Poll()
}

func main() needs Console {
	let q = queue.New[int](1)
	match poll(q) {
	case Some[int]:
		Console.Println("some")
	case None:
		Console.Println("none")
	}
}
`

const queuePollAllocDriver = `package main

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

var (
	env  = &Env{task: rt.Root("driver")}
	sink = poll(env, v1.New[int](0))
)

func TestPollNoneAllocatesNothing(t *testing.T) {
	q := v1.New[int](1)
	if n := testing.AllocsPerRun(200, func() { sink = poll(env, q) }); n != 0 {
		t.Errorf("a Poll of an empty queue allocates %v times", n)
	}
	if _, none := sink.(None); !none {
		t.Errorf("a Poll of an empty queue gave %#v; want None", sink)
	}
	if q.Push(env.task, 3) != nil {
		t.Fatal("Push failed")
	}
	if a, b := poll(env, q), poll(env, q); a == b || b != sink {
		t.Errorf("poll gave %#v then %#v; want a value, then the None of an empty queue (%#v)", a, b, sink)
	}
}
`

// TestQueuePollNoneAllocatesNothing: a Poll of an empty queue, called as
// a pure function from Go, allocates nothing.
func TestQueuePollNoneAllocatesNothing(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "queuepollalloc", queuePollAllocSource), queuePollAllocDriver)
}

// queuePollLawsProbe restates each law whose ledger runner is
// TestQueuePollLaws, as a test of the same name (TestStdLawsRun's naming).
// PollOnEmptyIsNone runs on queues of capacity 0, 1 and 4.
const queuePollLawsProbe = `import queue

test QueuePollOnEmptyIsNone(c int) {
	let q = queue.New[int](c % 3 * (c % 3))
	assert q.Poll() == None
	q.End()
	assert q.Poll() == None
}

test QueuePollTakesTheFront(a int, b int) {
	let q = queue.New[int](2)
	try q.Push(a)
	try q.Push(b)
	q.End()
	assert q.Poll() == Some[int]{Value: a}
	assert q.Poll() == Some[int]{Value: b}
	assert q.Poll() == None
}

test QueueLenCountsUntaken(xs []int) {
	let q = queue.New[int](len(xs))
	for x in xs {
		try q.Push(x)
	}
	assert q.Len() == len(xs)
	for x in xs {
		assert q.Poll() == Some[int]{Value: x}
	}
	assert q.Len() == 0
}
`

// queuePollLawStatements is the statement of each law the probe restates,
// the law's source text as the ledger writes it, by ledger key;
// lawledger.Run holds it equal to the ledger (fu-ledger-runner).
var queuePollLawStatements = map[string]string{
	"queue LenCountsUntaken":  `LenCountsUntaken(xs []int) { let q = New[int](len(xs)) for x in xs { try q.Push(x) } let full = q.Len() == len(xs) let mut drained = true for x in xs { drained = drained && q.Poll() == Some[int]{Value: x} } full && drained && q.Len() == 0 }`,
	"queue PollOnEmptyIsNone": `PollOnEmptyIsNone() { let q = New[int](1) let open = q.Poll() == None q.End() open && q.Poll() == None }`,
	"queue PollTakesTheFront": `PollTakesTheFront(a int, b int) { let q = New[int](2) try q.Push(a) try q.Push(b) q.End() q.Poll() == Some[int]{Value: a} && q.Poll() == Some[int]{Value: b} && q.Poll() == None }`,
}

// TestQueuePollLaws runs the probe with --seed 1; lawledger.Run holds that
// it restates exactly the ledger's laws whose runner is this test, each
// with the row's statement, and that each passed.
func TestQueuePollLaws(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "queuepolllaws", queuePollLawsProbe, queuePollLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// TestQueuePollRuns: Poll lowers to the Go method's comma-ok call, adapted
// in place to Some or None, and the program prints each case. The run is
// the subtest of the Effect case it ports, Queue.test.ts "poll returns
// Option for non-blocking take": the program's first lines poll an empty
// queue (None), offer, and poll the front (Some).
func TestQueuePollRuns(t *testing.T) {
	needsBuild(t)
	var progs map[string]*lowered
	if !t.Run("effect.queue.poll-option", func(t *testing.T) {
		progs = runBehaviors(t, []behavior{{
			name:   "queuepoll",
			src:    queuePollProgram,
			stdout: "none\nlen 2\nsome 1\nlen 1\nsome 2\nnone\nlen 0\nnone\nsome 7\nnone\n",
		}})
	}) || progs == nil {
		t.FailNow()
	}
	got := progs["queuepoll"].mainGo()
	if !regexp.MustCompile(`if (v\w*), (ok\w*) := \(?\w+\)?\.Poll\(\); ok\w* \{\n.*return Some\[int\]\{Value: v\w*\}\n.*\}\n.*return None\{\}\n`).MatchString(got) {
		t.Errorf("no comma-ok call of the Go Poll in the emitted Go:\n%s", got)
	}
	if !strings.Contains(got, ".Len()") {
		t.Errorf("no call of the Go Len in the emitted Go:\n%s", got)
	}
}

// queuePollProgram polls each kind of queue: open and empty, with values,
// closed with values, closed and empty, and capacity 0 with a waiting Push,
// whose value Poll takes. Len counts the values in the queue.
const queuePollProgram = `import queue
import task

func show(o Option[int]) needs Console {
	match o {
	case Some[int] as s:
		Console.Println("some \(s.Value)")
	case None:
		Console.Println("none")
	}
}

func main() fails queue.Closed needs Console | Ambient {
	let clock = NewLiveScheduler()
	let q = queue.New[int](2)
	show(q.Poll())
	try q.Push(1)
	try q.Push(2)
	Console.Println("len \(q.Len())")
	show(q.Poll())
	Console.Println("len \(q.Len())")
	q.End()
	show(q.Poll())
	show(q.Poll())
	Console.Println("len \(q.Len())")
	let r = queue.New[int](0)
	show(r.Poll())
	let t = task.Go() { try r.Push(7) }
	let mut got = r.Poll()
	for _ in 0..5000 {
		if got == None {
			clock.Sleep(1ms)
			got = r.Poll()
		}
	}
	show(got)
	try t.Wait()
	show(r.Poll())
}
`

// TestQueuePollRuns: Poll lowers to the Go method's comma-ok call, adapted
// in place to Some or None, and the program prints each case. The run is
// the subtest of the Effect case it ports, Queue.test.ts "poll returns
