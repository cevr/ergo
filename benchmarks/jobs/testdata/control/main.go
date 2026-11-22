// Command control is jobsbench's hand Go twin
// (benchmarks/jobs/cmd/jobsbench): the same jobs, as a person writing Go
// would keep a queue with leases: a heap of ready jobs by (ready time,
// ID), a map of jobs by ID with each lease's end and attempt, all under a
// mutex. Run as `control bench memory N`.
package main

import (
	"container/heap"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type ready struct {
	at time.Time
	id int
}

type readyHeap []ready

func (h readyHeap) Len() int { return len(h) }
func (h readyHeap) Less(i, j int) bool {
	if c := h[i].at.Compare(h[j].at); c != 0 {
		return c < 0
	}
	return h[i].id < h[j].id
}
func (h readyHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *readyHeap) Push(x any)   { *h = append(*h, x.(ready)) }
func (h *readyHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

type job struct {
	value    int
	attempts int
	leased   bool
	until    time.Time
}

type queue struct {
	mu    sync.Mutex
	lease time.Duration
	next  int
	jobs  map[int]*job
	ready readyHeap
}

func (q *queue) enqueue(v int, at time.Time) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.next++
	q.jobs[q.next] = &job{value: v}
	heap.Push(&q.ready, ready{at: at, id: q.next})
	return q.next
}

func (q *queue) reserve(now time.Time) (id, attempt, value int, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ready) == 0 || q.ready[0].at.After(now) {
		return 0, 0, 0, false
	}
	r := heap.Pop(&q.ready).(ready)
	j := q.jobs[r.id]
	j.attempts++
	j.leased, j.until = true, now.Add(q.lease)
	return r.id, j.attempts, j.value, true
}

func (q *queue) ack(id, attempt int, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok || !j.leased || j.attempts != attempt || !now.Before(j.until) {
		return false
	}
	delete(q.jobs, id)
	return true
}

func bench(n int) int {
	q := &queue{lease: time.Second, jobs: map[int]*job{}}
	start := time.Unix(0, 0)
	sum := 0
	for i := range n + 64 {
		now := start.Add(time.Duration(i) * time.Microsecond)
		if i < n {
			q.enqueue(i, now)
		}
		if i >= 64 {
			if id, attempt, v, ok := q.reserve(now); ok && q.ack(id, attempt, now) {
				sum += v
			}
		}
	}
	return sum
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench memory N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	fmt.Println(bench(n))
}
