package rt

import (
	"math"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync/atomic"
)

// Heap pacing (IR-G PACE1). Go's collector starts a cycle when the heap has
// grown by GOGC percent over the live heap of the last cycle, with a
// minimum heap goal of 4 MiB. A program whose live heap stays small (a
// parser, a command, a router replaying a log) therefore collects many
// times for little garbage: measured on jsonv, router-model, mdhtml and
// router, the collector took 13% to 76% of the instructions at a peak of
// 15 to 138 MB. An Ergo program instead keeps a heap floor: while the heap
// goal at Go's own pace is under the floor, the collector waits until the
// heap reaches the floor; above it, Go's pace (GOGC=100) is unchanged.
//
// The bound: the heap goal, the size at which the next cycle ends, is at
// most max(floor, live + (live + roots)) plus a rounding of one percent of
// the larger of the live heap with its roots and Go's minimum heap. live is
// the heap the last cycle marked, roots the stacks and globals it scanned:
// with no floor Go's own goal is live + (live + roots). The memory the
// runtime keeps beside the heap is Go's as without the floor.
//
// The user wins: with GOGC in the environment the program is not paced at
// all, and a percent the program sets itself (runtime/debug.SetGCPercent)
// stops the pacing at the next cycle. A memory limit lowers the floor to a
// quarter of the limit: GOMEMLIMIT (from the environment or set by the
// program before main) and the cgroup v2 memory.max of the process and of
// every cgroup above it. Go's own memory limit stays in force.
//
// Prior art: Go has no public minimum-heap setting (issue 23044 proposed
// one; GOMEMLIMIT shipped instead), so the floor is set through the public
// GC percent, recomputed after each cycle. BEAM gives each process a
// min_heap_size, so a short task often ends with no collection; OCaml's
// space_overhead trades memory for collector work as GOGC does.

// PaceFloor is the heap below which an Ergo program does not collect: the
// one value of the pacing default (owner question Q2 of IR-G).
const PaceFloor = 64 << 20

// heapMinimum is Go's minimum heap goal at GOGC=100 (runtime
// defaultHeapMinimum). Go scales it with the percent, so a high percent
// raises the minimum too; pacePercent accounts for both terms. Under the
// heapminimum512kib experiment Go's minimum is smaller, and the goal falls
// short of the floor; the bound still holds.
const heapMinimum = 4 << 20

// basePercent is Go's default pace, which the pacing keeps above the floor.
const basePercent = 100

// paceFloorFor returns the floor of a program, given whether the
// environment sets GOGC and the smallest memory limit in force (0 for
// none), and whether the program is paced at all. GOGC in the environment
// turns the pacing off; a limit lowers the floor to a quarter of it.
func paceFloorFor(gogcSet bool, limit uint64) (floor uint64, on bool) {
	if gogcSet {
		return 0, false
	}
	floor = PaceFloor
	if limit > 0 && limit/4 < floor {
		floor = limit / 4
	}
	return floor, true
}

// pacePercent returns the GC percent of the next cycle for a live heap of
// live bytes, roots bytes of stacks and globals scanned, and a floor. At
// Go's pace the goal is max(live + (live+roots)*p/100, heapMinimum*p/100)
// (runtime gcControllerState.commit); when that is under the floor at
// p = 100, pacePercent returns the smallest p whose goal reaches the
// floor, so the goal is the floor. Otherwise it returns 100.
func pacePercent(live, roots, floor uint64) int {
	if live+(live+roots)*basePercent/100 >= floor || heapMinimum*basePercent/100 >= floor {
		return basePercent
	}
	// The minimum heap term reaches the floor at p = floor*100/heapMinimum,
	// the live term at p = (floor-live)*100/(live+roots); the goal is the
	// larger term, so it first reaches the floor at the smaller p.
	p := ceilDiv(floor*100, heapMinimum)
	if live+roots > 0 {
		p = min(p, ceilDiv((floor-live)*100, live+roots))
	}
	return int(max(p, basePercent))
}

func ceilDiv(a, b uint64) uint64 { return (a + b - 1) / b }

// Pace starts the heap pacing of the program: emitted main calls it once,
// through runtime/v1's Main, before the root task starts. It reads the
// environment and the memory limits, sets the percent for the first cycle
// and recomputes it after each cycle.
func Pace() {
	_, gogcSet := os.LookupEnv("GOGC")
	limit := uint64(math.MaxUint64)
	if l := debug.SetMemoryLimit(-1); l > 0 && l < math.MaxInt64 {
		limit = uint64(l)
	}
	if l := cgroupMemoryLimit("/proc/self/cgroup", "/sys/fs/cgroup"); l > 0 {
		limit = min(limit, l)
	}
	if limit == math.MaxUint64 {
		limit = 0
	}
	floor, on := paceFloorFor(gogcSet, limit)
	if !on {
		return
	}
	newPacer(floor, nil).cycle()
}

// pacer recomputes the GC percent after each cycle: a cleanup on a
// sentinel object runs once the cycle that finds the sentinel unreachable
// has swept it, and arms the next sentinel. A sentinel armed while a cycle
// marks is allocated live and outlasts that cycle, so the pacer then
// acts one cycle later. Only the cleanup goroutine runs cycle after the
// first call.
type pacer struct {
	floor   uint64
	set     int64 // the percent the pacer last set
	samples [4]metrics.Sample
	stopped atomic.Bool
	// observe, when not nil, is called with each decision: the cycles
	// completed, the percent in force and whether the pacing goes on.
	observe func(cycles uint64, percent int, on bool)
}

// paceSentinel has a pointer, so the tiny allocator, whose objects may
// never be cleaned up, does not hold it.
type paceSentinel struct{ _ *byte }

func newPacer(floor uint64, observe func(uint64, int, bool)) *pacer {
	p := &pacer{floor: floor, set: -2, observe: observe}
	p.samples[0].Name = "/gc/heap/live:bytes"
	p.samples[1].Name = "/gc/scan/stack:bytes"
	p.samples[2].Name = "/gc/scan/globals:bytes"
	p.samples[3].Name = "/gc/cycles/total:gc-cycles"
	return p
}

func (p *pacer) cycle() {
	if p.stopped.Load() {
		return
	}
	metrics.Read(p.samples[:])
	live := p.samples[0].Value.Uint64()
	roots := p.samples[1].Value.Uint64() + p.samples[2].Value.Uint64()
	cycles := p.samples[3].Value.Uint64()
	want := pacePercent(live, roots, p.floor)
	// The swap tells whether the program set its own percent since the
	// last cycle; then it wins, and the pacer puts it back and stops.
	if old := debug.SetGCPercent(want); p.set != -2 && int64(old) != p.set {
		debug.SetGCPercent(old)
		p.stopped.Store(true)
		if p.observe != nil {
			p.observe(cycles, old, false)
		}
		return
	}
	p.set = int64(want)
	if p.observe != nil {
		p.observe(cycles, want, true)
	}
	runtime.AddCleanup(&paceSentinel{}, (*pacer).cycle, p)
}

// stop ends the pacing at the next cycle; the percent stays as it is.
func (p *pacer) stop() { p.stopped.Store(true) }

// cgroupMemoryLimit returns the smallest cgroup v2 memory.max of the
// process's cgroup and every cgroup above it, or 0 when none is set or
// the files cannot be read (cgroup v1, or not Linux). self is the
// process's cgroup file (/proc/self/cgroup), root the cgroup v2 mount.
func cgroupMemoryLimit(self, root string) uint64 {
	data, err := os.ReadFile(self)
	if err != nil {
		return 0
	}
	dir := ""
	for line := range strings.Lines(string(data)) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			dir = path.Clean("/" + rest)
			break
		}
	}
	if dir == "" {
		return 0
	}
	var limit uint64
	for {
		if b, err := os.ReadFile(root + dir + "/memory.max"); err == nil {
			if n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64); err == nil && n > 0 && (limit == 0 || n < limit) {
				limit = n
			}
		}
		if dir == "/" {
			return limit
		}
		dir = path.Dir(dir)
	}
}
