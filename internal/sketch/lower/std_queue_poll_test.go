package lower

import (
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
	q.Close()
	assert q.Poll() == None
}

test QueuePollTakesTheFront(a int, b int) {
	let q = queue.New[int](2)
	try q.Push(a)
	try q.Push(b)
	q.Close()
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
	"queue PollOnEmptyIsNone": `PollOnEmptyIsNone() { let q = New[int](1) let open = q.Poll() == None q.Close() open && q.Poll() == None }`,
	"queue PollTakesTheFront": `PollTakesTheFront(a int, b int) { let q = New[int](2) try q.Push(a) try q.Push(b) q.Close() q.Poll() == Some[int]{Value: a} && q.Poll() == Some[int]{Value: b} && q.Poll() == None }`,
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
