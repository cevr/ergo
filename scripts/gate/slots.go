package main

import "sync"

// slotPool hands out the gate's step slots: at most its size at once. A
// step that waits for a slot waits in one of two queues, each served in
// the order the steps came: a freed slot goes to the first step of the
// writers' queue, and only when that queue is empty to the first of the
// others. The writers' queue holds every step that writes the checkout and
// every step such a step needs (writerPath). The closure sweeps start only
// after every writer has ended (sweepsAfterWriters), so a writer that
// waited behind unrelated steps made every sweep wait too: in the batch-9
// full tier, build bin/ergo ran at 204.6 s and the first sweep at 270.5 s.
type slotPool struct {
	mu     sync.Mutex
	free   int
	queues [2][]chan struct{}
}

func newSlotPool(size int) *slotPool {
	return &slotPool{free: size}
}

// acquire returns when the caller holds a slot; first puts it in the
// writers' queue.
func (p *slotPool) acquire(first bool) {
	p.mu.Lock()
	if p.free > 0 {
		p.free--
		p.mu.Unlock()
		return
	}
	queue := 1
	if first {
		queue = 0
	}
	ready := make(chan struct{})
	p.queues[queue] = append(p.queues[queue], ready)
	p.mu.Unlock()
	<-ready
}

// release gives the caller's slot to the next waiting step, or frees it.
func (p *slotPool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, queue := range p.queues {
		if len(queue) > 0 {
			p.queues[i] = queue[1:]
			close(queue[0])
			return
		}
	}
	p.free++
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
