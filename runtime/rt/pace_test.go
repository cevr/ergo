package rt

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"testing"
)

// goGoal is Go 1.27's heap goal for a GC percent p after a cycle that
// marked live bytes and scanned roots bytes of stacks and globals
// (runtime gcControllerState.commit: the percent goal, raised to the
// minimum heap, which Go scales with the percent). The sweep-distance and
// runway adjustments of heapGoalInternal are left out: they apply at a
// cycle's trigger, not to the pace.
func goGoal(live, roots uint64, p int) uint64 {
	return max(live+(live+roots)*uint64(p)/100, heapMinimum*uint64(p)/100)
}

// TestPaceKeepsItsBound is the generated property of the pacing: for
// generated live heaps, roots, floors, memory limits and GOGC settings,
// the goal that Go's pacer computes from the returned percent (goGoal)
// keeps the stated bound, max(floor, Go's own goal) plus one percent of
// rounding, where the floor is at most a quarter of the limit; it reaches
// the floor; it never paces slower than Go; and GOGC in the environment
// turns the pacing off. ERGO_PACE_SEED=n replays one case.
//
// Mutants (runtime/rt/testdata/mutants.json): pace-ignores-user-gogc,
// pace-floor-above-limit, pace-never-returns-to-100,
// pace-ignores-roots, pace-ignores-heap-minimum.
func TestPaceKeepsItsBound(t *testing.T) {
	first, n := uint64(1), uint64(20000)
	if s := os.Getenv("ERGO_PACE_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_PACE_SEED %q: %v", s, err)
		}
		first, n = v, 1
	}
	// The branches the generator must reach, so a mutant of either
	// formula meets a case that tells.
	reached := map[string]int{}
	for seed := first; seed < first+n; seed++ {
		c := genPaceCase(seed)
		why, branch := checkPaceCase(c)
		reached[branch]++
		if why != "" {
			t.Fatalf("seed %d: %s\ncase: %+v\nreplay: ERGO_PACE_SEED=%d go test -run '^TestPaceKeepsItsBound$' ./runtime/rt", seed, why, c, seed)
		}
	}
	if n > 1 {
		for _, b := range []string{"off", "go-pace", "minimum-term", "live-term", "limit-lowers"} {
			if reached[b] == 0 {
				t.Errorf("the generator never reached branch %s (reached: %v)", b, reached)
			}
		}
	}
}

type paceCase struct {
	Live, Roots, Floor, Limit uint64
	GOGCSet                   bool
}

// genPaceCase draws sizes on a log scale from 0 to 2 GiB, so live heaps
// far below, near and far above the floor all occur, and limits from
// none to below the floor.
func genPaceCase(seed uint64) paceCase {
	r := rand.New(rand.NewPCG(seed, 0x9ace1))
	size := func(maxShift int) uint64 {
		if r.IntN(8) == 0 {
			return 0
		}
		return r.Uint64N(1 << uint(1+r.IntN(maxShift)))
	}
	c := paceCase{Live: size(31), Roots: size(26), GOGCSet: r.IntN(10) == 0}
	if r.IntN(3) == 0 {
		c.Limit = size(32)
	}
	return c
}

func checkPaceCase(c paceCase) (why, branch string) {
	floor, on := paceFloorFor(c.GOGCSet, c.Limit)
	if c.GOGCSet {
		if on {
			return "GOGC is set and the program is paced", "off"
		}
		return "", "off"
	}
	if !on {
		return "GOGC is unset and the program is not paced", ""
	}
	want := uint64(PaceFloor)
	branch = "go-pace"
	if c.Limit > 0 && c.Limit/4 < want {
		want = c.Limit / 4
		branch = "limit-lowers"
	}
	if floor > want {
		return fmt.Sprintf("floor %d is above %d (PaceFloor %d, a quarter of the limit %d)", floor, want, PaceFloor, c.Limit), branch
	}
	p := pacePercent(c.Live, c.Roots, floor)
	if p < basePercent {
		return fmt.Sprintf("percent %d paces slower than Go's %d", p, basePercent), branch
	}
	goal := goGoal(c.Live, c.Roots, p)
	base := goGoal(c.Live, c.Roots, basePercent)
	slack := max(heapMinimum, c.Live+c.Roots)/100 + 1
	if bound := max(want, base) + slack; goal > bound {
		return fmt.Sprintf("percent %d gives goal %d, above the bound %d (floor %d, Go's goal %d)", p, goal, bound, want, base), branch
	}
	if goal < floor {
		return fmt.Sprintf("percent %d gives goal %d, under the floor %d", p, goal, floor), branch
	}
	if branch == "go-pace" && base < floor {
		if c.Live+(c.Live+c.Roots)*uint64(p)/100 >= floor {
			branch = "live-term"
		} else {
			branch = "minimum-term"
		}
	}
	return "", branch
}

// TestPaceFollowsTheCycles runs the pacer on this process's own collector:
// after a cycle with a small live heap the goal Go reports reaches the
// floor; after a cycle with a live heap far above the floor the percent is
// Go's 100 again; and a percent the program sets stops the pacing. The
// property above checks the arithmetic; this checks the loop: the
// cleanup that runs after each cycle, the metrics it reads and the
// percent it sets.
//
// Mutants: pace-not-rearmed, pace-overrides-program-percent.
func TestPaceFollowsTheCycles(t *testing.T) {
	old := debug.SetGCPercent(basePercent)
	defer debug.SetGCPercent(old)
	const floor = 16 << 20
	type decision struct {
		cycles  uint64
		percent int
		on      bool
	}
	decisions := make(chan decision, 64)
	p := newPacer(floor, func(cycles uint64, percent int, on bool) {
		decisions <- decision{cycles, percent, on}
	})
	defer p.stop()
	p.cycle()
	<-decisions
	// next forces a cycle and waits for the first pacer decision made
	// after it. A decision made while a cycle marked arms a sentinel that
	// outlasts that cycle, so each earlier decision read forces one more
	// cycle; each such decision re-armed the pacer, so a decision always
	// follows. A pacer that is not re-armed sends nothing more, and the
	// test's -timeout names this wait.
	next := func() decision {
		runtime.GC()
		s := []metrics.Sample{{Name: "/gc/cycles/total:gc-cycles"}}
		metrics.Read(s)
		done := s[0].Value.Uint64()
		for d := range decisions {
			if d.cycles >= done || !d.on {
				return d
			}
			runtime.GC()
		}
		panic("unreachable")
	}
	goal := func() uint64 {
		s := []metrics.Sample{{Name: "/gc/heap/goal:bytes"}}
		metrics.Read(s)
		return s[0].Value.Uint64()
	}
	if d := next(); !d.on || d.percent <= basePercent {
		t.Fatalf("small live heap: decision %+v, want a percent above %d", d, basePercent)
	}
	if g := goal(); g < floor {
		t.Fatalf("small live heap: heap goal %d, want at least the floor %d", g, floor)
	}
	live := make([]byte, 4*floor)
	for i := range live {
		live[i] = byte(i)
	}
	if d := next(); !d.on || d.percent != basePercent {
		t.Fatalf("live heap of %d: decision %+v, want Go's %d", len(live), d, basePercent)
	}
	if g := goal(); g > 2*uint64(len(live))+floor {
		t.Fatalf("live heap of %d: heap goal %d, want about twice the live heap", len(live), g)
	}
	runtime.KeepAlive(live)
	live = nil
	if d := next(); !d.on || d.percent <= basePercent {
		t.Fatalf("live heap freed: decision %+v, want a percent above %d again", d, basePercent)
	}
	debug.SetGCPercent(300)
	if d := next(); d.on || d.percent != 300 {
		t.Fatalf("after the program set 300: decision %+v, want the pacing stopped", d)
	}
	if got := debug.SetGCPercent(basePercent); got != 300 {
		t.Fatalf("the pacer changed the program's percent 300 to %d", got)
	}
}

// TestPaceReadsTheCgroupTree checks the limit read: the smallest
// memory.max of the process's cgroup and its ancestors, "max" read as no
// limit, and no limit when the files are missing.
//
// Mutant: pace-cgroup-leaf-only.
func TestPaceReadsTheCgroupTree(t *testing.T) {
	root := t.TempDir()
	write := func(rel, text string) {
		t.Helper()
		name := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("self", "0::/user.slice/run-1.scope\n")
	write("fs/user.slice/run-1.scope/memory.max", "max\n")
	write("fs/user.slice/memory.max", "268435456\n")
	if got := cgroupMemoryLimit(filepath.Join(root, "self"), filepath.Join(root, "fs")); got != 256<<20 {
		t.Errorf("limit %d, want the parent's 268435456", got)
	}
	if got := cgroupMemoryLimit(filepath.Join(root, "missing"), filepath.Join(root, "fs")); got != 0 {
		t.Errorf("limit %d with no cgroup file, want 0", got)
	}
}
