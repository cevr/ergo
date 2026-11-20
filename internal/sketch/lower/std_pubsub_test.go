package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdPubsubLawsProbe restates each law whose ledger runner is
// TestStdPubsubLawsRun (STORES SB2): queue's send strategies, Offer caught
// on Full and Slide, and std pubsub's fan-out, each as a test of its own
// name (stdLawsTestName), in the dot form a user writes.
const stdPubsubLawsProbe = `import pubsub
import queue
import task

test QueueDroppingKeepsTheOldest(xs []int, c int) {
	let capacity = 1 + (c % 4 + 4) % 4
	let q = queue.New[int](capacity)
	let mut kept = []int{}
	for x in xs {
		q.Offer(x) catch {
		case queue.Full:
			assert len(kept) == capacity
			continue
		case queue.Closed:
			assert false
		}
		kept = kept.Append(x)
	}
	q.End()
	for k in kept {
		assert try q.Pop() == k
	}
	assert q.Len() == 0
	assert len(kept) <= capacity
}

test QueueSlidingKeepsTheNewest(xs []int, c int) {
	let capacity = 1 + (c % 4 + 4) % 4
	let q = queue.New[int](capacity)
	for x in xs {
		try q.Slide(x)
	}
	q.End()
	let from = if len(xs) > capacity { len(xs) - capacity } else { 0 }
	assert q.Len() == len(xs) - from
	for i in from..len(xs) {
		assert try q.Pop() == xs[i]
	}
}

test PubsubEverySubscriberSeesEveryValueAfterItJoined(before []int, after []int) {
	let p = pubsub.New[int](len(before) + len(after) + 1)
	let early = p.Subscribe()
	for x in before {
		try p.Publish(x)
	}
	let late = p.Subscribe()
	for x in after {
		try p.Publish(x)
	}
	p.End()
	for x in before {
		assert try early.Take() == x
	}
	for x in after {
		assert try early.Take() == x
		assert try late.Take() == x
	}
	assert early.Len() == 0
	assert late.Len() == 0
}

test PubsubSlowSubscriberBoundsThePublisher(c int, k int) {
	let capacity = 1 + (c % 3 + 3) % 3
	let n = capacity + 1 + (k % 4 + 4) % 4
	let p = pubsub.New[int](capacity)
	let s = p.Subscribe()
	let publisher = task.Go() {
		for i in 0..n {
			try p.Publish(i)
		}
	}
	for i in 0..n {
		assert s.Len() <= capacity
		assert try s.Take() == i
	}
	try publisher.Wait()
}

test PubsubClosedSubscriberIsSkipped(xs []int) {
	let p = pubsub.New[int](1)
	let watching = p.Subscribe()
	if true {
		let gone = p.Subscribe()
		try p.Publish(0)
	}
	assert p.Subscribers() == 1
	assert try watching.Take() == 0
	for x in xs {
		try p.Publish(x)
		assert try watching.Take() == x
	}
}
`

// stdPubsubLawStatements is the statement of each law the probe restates,
// as the ledger writes it, by ledger key.
var stdPubsubLawStatements = map[string]string{
	"queue DroppingKeepsTheOldest":                      `DroppingKeepsTheOldest(xs []int, c int) { let capacity = 1 + (c%4+4)%4 let q = New[int](capacity) let mut kept = []int{} let mut ok = true for x in xs { q.Offer(x) catch { case Full: ok = ok && len(kept) == capacity continue case Closed: return false } kept = kept.Append(x) } q.End() for k in kept { ok = ok && try q.Pop() == k } ok && q.Len() == 0 && len(kept) <= capacity }`,
	"queue SlidingKeepsTheNewest":                       `SlidingKeepsTheNewest(xs []int, c int) { let capacity = 1 + (c%4+4)%4 let q = New[int](capacity) for x in xs { try q.Slide(x) } q.End() let from = if len(xs) > capacity { len(xs) - capacity } else { 0 } let mut ok = q.Len() == len(xs) - from for i in from..len(xs) { ok = ok && try q.Pop() == xs[i] } ok }`,
	"pubsub EverySubscriberSeesEveryValueAfterItJoined": `EverySubscriberSeesEveryValueAfterItJoined(before []int, after []int) { let p = New[int](len(before) + len(after) + 1) let early = Subscribe(p) for x in before { try Publish(p, x) } let late = Subscribe(p) for x in after { try Publish(p, x) } End(p) let mut ok = true for x in before { ok = ok && try Take(early) == x } for x in after { ok = ok && try Take(early) == x && try Take(late) == x } ok && Len(early) == 0 && Len(late) == 0 }`,
	"pubsub SlowSubscriberBoundsThePublisher":           `SlowSubscriberBoundsThePublisher(c int, k int) { let capacity = 1 + (c%3+3)%3 let n = capacity + 1 + (k%4+4)%4 let p = New[int](capacity) let s = Subscribe(p) let publisher = task.Go() { for i in 0..n { try Publish(p, i) } } let mut ok = true for i in 0..n { ok = ok && Len(s) <= capacity ok = ok && try Take(s) == i } try publisher.Wait() ok }`,
	"pubsub ClosedSubscriberIsSkipped":                  `ClosedSubscriberIsSkipped(xs []int) { let p = New[int](1) let watching = Subscribe(p) if true { let gone = Subscribe(p) try Publish(p, 0) } let mut ok = Subscribers(p) == 1 && try Take(watching) == 0 for x in xs { try Publish(p, x) ok = ok && try Take(watching) == x } ok }`,
}

// TestStdPubsubLawsRun runs the probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's laws whose runner is
// this test, each with the row's statement, and that each passed. The
// sb2-* mutants of the std ledger are its kills.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdPubsubLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdpubsub", stdPubsubLawsProbe, stdPubsubLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
