package lower

import (
	"fmt"
	"strings"
	"testing"
)

// tzResolveAllocSource resolves wall times around every change of
// America/Toronto from 1990 to 2060 (a gap and an overlap each year, in its
// table and, after 2037, in its rule) and around a fixed zone's midnight.
const tzResolveAllocSource = `import civil
import time
import tz
import tz/iana

func Toronto() tz.Zone {
	tz.FromTZif("America/Toronto", iana.TZif("America/Toronto")) catch {
	case tz.BadData:
		tz.UTC()
	}
}

func Fixed() tz.Zone {
	tz.FixedOffset(-12600) catch {
	case tz.BadOffset:
		tz.UTC()
	}
}

func Walls(z tz.Zone) []civil.DateTime {
	let mut out = []civil.DateTime{}
	for c in tz.Transitions(z, time.Unix(631152000, 0), time.Unix(2840140800, 0)) {
		let at = time.UnixSeconds(c.At) + int64(c.Before.Seconds)
		for k in -8..9 {
			out = out.Append(civil.DateTimeOf(time.Unix(at + int64(k) * 900, 0)))
		}
	}
	for k in -8..9 {
		out = out.Append(civil.DateTimeOf(time.Unix(int64(k) * 900, 0)))
	}
	out
}

// Resolves is the sum of Compatible(Resolve) over ws, and Kinds counts
// each Resolved variant, so the test sees that ws reach all three.
func Resolves(z tz.Zone, ws []civil.DateTime) int64 {
	let mut s = int64(0)
	for w in ws {
		s = s + time.UnixSeconds(tz.Compatible(tz.Resolve(z, w)))
	}
	s
}

func Kinds(z tz.Zone, ws []civil.DateTime) string {
	let mut u = 0
	let mut g = 0
	let mut o = 0
	for w in ws {
		match tz.Resolve(z, w) {
		case tz.Unique:
			u += 1
		case tz.Gap:
			g += 1
		case tz.Overlap:
			o += 1
		}
	}
	"\(u) \(g) \(o)"
}

func main() needs Console {
	Console.Println(Kinds(Toronto(), Walls(Toronto())))
}
`

// tzResolveAllocDriver measures the allocations of Resolve on each wall
// time alone and logs the walls that allocate more than its result.
const tzResolveAllocDriver = `package main

import (
	"fmt"
	"testing"

	rt "ergo.local/prototype/runtime/rt"
)

var sink int64

var benchEnv = &Env{task: rt.Root("allocs")}

func TestResolveAllocsDriver(t *testing.T) {
	for _, z := range []Zone{{{toronto}}, {{fixed}}} {
		ws := {{walls}}
		fmt.Printf("KINDS %s\n", {{kinds}})
		over := 0
		for i := range ws {
			one := ws[i : i+1]
			if n := testing.AllocsPerRun(20, func() { sink = {{resolves}} }); n > 1 {
				over++
				if over <= 5 {
					fmt.Printf("OVER %d %v\n", i, n)
				}
			}
		}
		fmt.Printf("CALLS %d OVER %d\n", len(ws), over)
	}
}
`

// TestStdTzResolveAllocs is the counted guard of Resolve's allocations
// (std-spike T3, from T2's review): Resolve walks the changes of its
// window one at a time and keeps the first and last instant it finds, so
// a call allocates its result alone, the Resolved box of the lowering's
// interface union (stdlib.md tz 5; a follow-up of the union lane). It
// counts allocations with testing.AllocsPerRun on each wall time of a
// gap, an overlap and a plain hour of Toronto's table and rule, and of a
// fixed zone; each is at most one.
//
// It builds Go because the allocations are the built program's.
func TestStdTzResolveAllocs(t *testing.T) {
	needsBuild(t)
	prog := lowerSource("tzallocs", tzResolveAllocSource, true)
	if prog.err != nil || len(prog.diags) > 0 || prog.panicked != nil || len(prog.refused) > 0 {
		t.Fatalf("does not lower: %v %v %v %v", prog.err, prog.diags, prog.panicked, prog.refused)
	}
	main := prog.mainGo()
	driver := strings.NewReplacer(
		"{{toronto}}", emittedCall(main, "Toronto", ""),
		"{{fixed}}", emittedCall(main, "Fixed", ""),
		"{{walls}}", emittedCall(main, "Walls", "z"),
		"{{kinds}}", emittedCall(main, "Kinds", "z, ws"),
		"{{resolves}}", emittedCall(main, "Resolves", "z, one"),
	).Replace(tzResolveAllocDriver)
	out := goTestEmittedVerbose(t, prog, driver)
	var kinds []string
	zones := 0
	for line := range strings.Lines(out) {
		var calls, over int
		var u, g, o int
		if _, err := fmt.Sscanf(line, "KINDS %d %d %d", &u, &g, &o); err == nil {
			kinds = append(kinds, fmt.Sprint(u, g, o))
			if zones == 0 && (u == 0 || g == 0 || o == 0) {
				t.Errorf("Toronto's walls reach Unique %d, Gap %d, Overlap %d times; want each", u, g, o)
			}
		}
		if _, err := fmt.Sscanf(line, "CALLS %d OVER %d", &calls, &over); err == nil {
			zones++
			if over > 0 {
				t.Errorf("%d of %d calls of Resolve allocate more than their result:\n%s", over, calls, firstLines(out, 40))
			}
		}
	}
	if zones != 2 {
		t.Fatalf("want 2 zones measured, got %d:\n%s", zones, firstLines(out, 40))
	}
	t.Logf("kinds %v", kinds)
}
