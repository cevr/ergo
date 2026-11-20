package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// offerProbe holds queue's send strategies (stdlib.md, PubSub and send
// strategies) as tests the evaluator runs: Offer keeps the oldest values
// and fails Full past the capacity, and Slide keeps the newest. The same
// laws run in a test build in lower's TestStdPubsubLawsRun; this is the
// check-time evaluator's Offer (tasks.go) against them.
const offerProbe = `import queue

test Dropping(xs []int, c int) {
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
	assert q.Len() == 0 && q.Cap() == capacity
}

test Sliding(xs []int, c int) {
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

test OfferAfterEndFails(v int) {
	let q = queue.New[int](1)
	q.End()
	q.Offer(v) catch {
	case queue.Closed:
		return
	case queue.Full:
		assert false
	}
	assert false
}
`

// TestEvalQueueOffer runs offerProbe in the evaluator: every test passes.
// Mutants only it kills (testdata/mutants.json of internal/sketch/std):
// sb2-eval-offer-ignores-capacity, sb2-eval-offer-after-end-enters.
func TestEvalQueueOffer(t *testing.T) {
	f, err := syntax.Parse(offerProbe)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 3 {
		t.Fatalf("the evaluator ran %d obligations, not the probe's 3: %+v", len(rep.Results), rep.Results)
	}
	for _, r := range rep.Results {
		if r.Status != Passed {
			t.Errorf("%s: %s (%s)", r.Name, r.Status, r.Reason)
		}
	}
}
