// Package report prints the runtime's counters of a DOD0 program to
// stderr at its end, so both sides of a pair print the same stdout.
package report

import (
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
)

var names = []string{
	"/gc/scan/heap:bytes",
	"/gc/cycles/total:gc-cycles",
	"/gc/heap/allocs:bytes",
	"/gc/heap/allocs:objects",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/user:cpu-seconds",
}

// Done collects once (so the live heap is measured) and prints each
// counter, and the extra lines of the program, as "dod0 name value".
func Done(extra map[string]float64) {
	runtime.GC()
	s := make([]metrics.Sample, len(names))
	for i, n := range names {
		s[i].Name = n
	}
	metrics.Read(s)
	// The runtime's estimate of the GC's share of the CPU it used: GC
	// over GC plus user (the rest of /cpu/classes is idle and scavenge).
	if gc, user := s[4].Value.Float64(), s[5].Value.Float64(); gc+user > 0 {
		fmt.Fprintf(os.Stderr, "dod0 gc-share %.4f\n", gc/(gc+user))
	}
	for _, x := range s {
		switch x.Value.Kind() {
		case metrics.KindUint64:
			fmt.Fprintf(os.Stderr, "dod0 %s %d\n", x.Name, x.Value.Uint64())
		case metrics.KindFloat64:
			fmt.Fprintf(os.Stderr, "dod0 %s %.4f\n", x.Name, x.Value.Float64())
		}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	fmt.Fprintf(os.Stderr, "dod0 live-heap-bytes %d\n", ms.HeapAlloc)
	for k, v := range extra {
		fmt.Fprintf(os.Stderr, "dod0 %s %.0f\n", k, v)
	}
}

// Rand is splitmix64: the same stream on every side and host.
type Rand struct{ s uint64 }

// New is a stream from seed.
func New(seed uint64) *Rand { return &Rand{s: seed} }

// Next is the next 64 bits.
func (r *Rand) Next() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// IntN is a number in [0, n).
func (r *Rand) IntN(n int) int { return int(r.Next() % uint64(n)) }
