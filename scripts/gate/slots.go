package main

import "sync"

// slotPool hands out the gate's step slots: at most its size at once. A
// step that waits for a slot waits in one of two queues: a freed slot goes
// to the writers' queue, and only when that queue is empty to the others.
// The writers' queue holds every step that writes the checkout and every
// step such a step needs (writerPath). The closure sweeps start only after
// every writer has ended (sweepsAfterWriters), so a writer that waited
// behind unrelated steps made every sweep wait too: in the batch-9 full
// tier, build bin/ergo ran at 204.6 s and the first sweep at 270.5 s.
//
// In each queue the heaviest waiting step goes first (Step.Weight, its
// expected seconds), and steps of equal weight in the order they came:
// longest processing time first, so the longest step does not start last
// and become the gate's tail. In the batch-10 merge tier the closure sweep
// of internal/sketch/lower, the longest step (841 s), started at 381 s,
// behind steps of seconds (fu-sweep-deadline). A step with no weight (0)
// weighs unrecordedWeight: the gate has no record of how long it takes.
type slotPool struct {
	mu     sync.Mutex
	free   int
	queues [2][]waiter
}

// waiter is a step that waits for a slot: ready closes when it gets one.
type waiter struct {
	ready  chan struct{}
	weight float64
	slots  int
}

// unrecordedWeight is the expected seconds of a step with no weight: a
// step that runs no recorded tests (gofmt, go vet, the examples), or the
// tests of a package that durations.json has no row for yet. It is not the
// heaviest: before gl-durations an unknown step counted as the heaviest, and
// the steps of every package without a row (scripts/gate/*) took the first
// slots ahead of the closure sweep of internal/sketch/lower. 30 s is about
// a test package's step with its build; a package's rows come from
// `./scripts/gate.sh -record-durations`, and TestStepsWeighTheirRecordedTime
// fails while a test package has none.
const unrecordedWeight = 30.0

func newSlotPool(size int) *slotPool {
	return &slotPool{free: size}
}

// acquire is acquireWeighted for a step with no weight.
func (p *slotPool) acquire(first bool) { p.acquireWeighted(first, 0) }

// acquireWeighted returns when the caller holds a slot; first puts it in
// the writers' queue, and weight (expected seconds, 0 unknown) orders it in
// its queue.
func (p *slotPool) acquireWeighted(first bool, weight float64) {
	p.acquireN(first, weight, 1)
}

// acquireN reserves slots together: a memory-heavy step takes the whole
// pool. Later small steps cannot pass a waiting large reservation, or a
// steady stream of small steps could starve it forever.
func (p *slotPool) acquireN(first bool, weight float64, slots int) {
	p.mu.Lock()
	if p.free >= slots && len(p.queues[0])+len(p.queues[1]) == 0 {
		p.free -= slots
		p.mu.Unlock()
		return
	}
	queue := 1
	if first {
		queue = 0
	}
	if weight <= 0 {
		weight = unrecordedWeight
	}
	ready := make(chan struct{})
	p.queues[queue] = append(p.queues[queue], waiter{ready, weight, slots})
	p.dispatch()
	p.mu.Unlock()
	<-ready
}

// release gives the caller's slot to the next waiting step, or frees it.
func (p *slotPool) release() {
	p.releaseN(1)
}

func (p *slotPool) releaseN(slots int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.free += slots
	p.dispatch()
}

// dispatch admits the next highest-priority reservation only when all of
// its slots fit. Called with p.mu held.
func (p *slotPool) dispatch() {
	for i, queue := range p.queues {
		for len(queue) > 0 {
			next := heaviest(queue)
			if queue[next].slots > p.free {
				return
			}
			p.free -= queue[next].slots
			close(queue[next].ready)
			queue = append(queue[:next:next], queue[next+1:]...)
			p.queues[i] = queue
		}
	}
}

// heaviest is the index of the waiter with the most weight, the first of
// them on a tie.
func heaviest(queue []waiter) int {
	best := 0
	for i, w := range queue {
		if w.weight > queue[best].weight {
			best = i
		}
	}
	return best
}

// waiting is the number of steps in each queue, for tests.
func (p *slotPool) waiting() (first, rest int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queues[0]), len(p.queues[1])
}

// writerPath marks each step that writes the checkout and each step that
// such a step needs, directly or through other needs: the path the sweeps
// wait on.
func writerPath(steps []*Step, index map[string]int) []bool {
	marked := make([]bool, len(steps))
	var mark func(i int)
	mark = func(i int) {
		if marked[i] {
			return
		}
		marked[i] = true
		for _, need := range steps[i].Needs {
			if j, ok := index[need]; ok {
				mark(j)
			}
		}
	}
	for i, step := range steps {
		if len(step.Writes) > 0 {
			mark(i)
		}
	}
	return marked
}
