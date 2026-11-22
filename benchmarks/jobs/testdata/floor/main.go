// Command floor is the Go floor of jobsbench
// (benchmarks/jobs/cmd/jobsbench): the cheapest Go program for the same
// jobs. Jobs are enqueued in time order, so the ready jobs are a ring in
// (ready time, ID) order and a reserve takes its head; a lease is a slot
// of a ring indexed by ID with its end and attempt, and an ack checks and
// clears it. No lock (one goroutine), times as int64 nanoseconds, no
// allocation after the rings are made.
package main

import (
	"fmt"
	"os"
	"strconv"
)

type slot struct {
	at, until int64
	value     int
	attempts  int
	leased    bool
	live      bool
}

func bench(n int) int {
	const size = 128 // a power of two above the 64 waiting
	var ring [size]int
	var slots [size]slot
	head, tail := 0, 0
	lease := int64(1e9)
	sum := 0
	for i := range n + 64 {
		now := int64(i) * 1000
		if i < n {
			id := i + 1
			slots[id%size] = slot{at: now, value: i, live: true}
			ring[tail%size] = id
			tail++
		}
		if i >= 64 && head < tail {
			id := ring[head%size]
			s := &slots[id%size]
			if s.at <= now {
				head++
				s.attempts++
				s.leased, s.until = true, now+lease
				attempt := s.attempts
				if s.live && s.leased && s.attempts == attempt && now < s.until {
					s.live = false
					sum += s.value
				}
			}
		}
	}
	return sum
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench memory N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "floor: N is not a count")
		os.Exit(2)
	}
	fmt.Println(bench(n))
}
