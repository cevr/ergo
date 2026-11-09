package lower

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
)

// schedModel is the reference of one std schedule (docs/spec/stdlib.md,
// Schedule): the arguments its constructor was given, clamped as the
// constructor clamps them, and its Ergo source.
type schedModel struct {
	kind      string // spaced, forever, exponential, recurs, upto, jitter, and, or
	d, cap    time.Duration
	f, lo, hi float64
	n         int
	seed      uint64
	a, b      *schedModel
	src       string
}

func schedAtLeastZero(d time.Duration) time.Duration { return max(d, 0) }

func schedFactorIn(f float64) float64 {
	if f >= 1000 {
		return 1000
	}
	if f >= 0 {
		return f
	}
	return 0
}

func schedMix(x uint64) uint64 {
	z := x + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func schedScale(d time.Duration, f float64) time.Duration {
	x := float64(d) * f
	if x >= 4.6e18 {
		return 4600000000000000000
	}
	return time.Duration(int64(x))
}

// decide is the model's Decide: the delay and true for Continue, false for
// Stop. reach records each case of the model a decision takes.
func (m *schedModel) decide(runs int, e time.Duration, reach map[string]bool) (time.Duration, bool) {
	switch m.kind {
	case "spaced", "forever":
		return m.d, true
	case "exponential":
		if m.d == 0 {
			reach["exponential from zero"] = true
			return 0, true
		}
		k := 0
		if runs > 1 {
			k = runs - 1
		}
		x := float64(m.d) * math.Pow(m.f, float64(k))
		if x >= float64(m.cap) {
			reach["exponential at its cap"] = true
			return m.cap, true
		}
		reach["exponential under its cap"] = true
		return time.Duration(int64(x)), true
	case "recurs":
		if runs <= m.n {
			reach["recurs continues"] = true
			return 0, true
		}
		reach["recurs stops"] = true
		return 0, false
	case "upto":
		c, ok := m.a.decide(runs, e, reach)
		switch {
		case !ok:
			return 0, false
		case c > m.d-e:
			reach["upto stops"] = true
			return 0, false
		case c == m.d-e:
			reach["upto continues at its limit exactly"] = true
		}
		return c, true
	case "jitter":
		c, ok := m.a.decide(runs, e, reach)
		if !ok {
			reach["jitter of a stop"] = true
			return 0, false
		}
		span := m.hi - m.lo
		u := float64(schedMix(m.seed+uint64(runs))>>11) / 9007199254740992.0
		if float64(c)*m.lo >= 4.6e18 || float64(c)*(span*u) >= 4.6e18 {
			reach["jitter saturates"] = true
		}
		reach["jitter continues"] = true
		return schedScale(c, m.lo) + schedScale(c, span*u), true
	}
	x, xok := m.a.decide(runs, e, reach)
	y, yok := m.b.decide(runs, e, reach)
	reach[fmt.Sprintf("%s of %v %v", m.kind, xok, yok)] = true
	if m.kind == "and" {
		return max(x, y), xok && yok
	}
	switch {
	case xok && yok:
		return min(x, y), true
	case xok:
		return x, true
	}
	return y, yok
}

// genSched draws a schedule depth combinators deep at most, with arguments
// on a 100ms grid (so UpTo meets its limit exactly), some out of range so
// each constructor clamps.
func genSched(r *rand.Rand, depth int, reach map[string]bool) *schedModel {
	grid := func() time.Duration { return time.Duration(r.IntN(13)-1) * 100 * time.Millisecond }
	lit := func(d time.Duration) string { return fmt.Sprintf("duration(int64(%d))", int64(d)) }
	kinds := 4
	if depth > 0 {
		kinds = 8
	}
	switch r.IntN(kinds) {
	case 0:
		d := grid()
		if r.IntN(4) == 0 {
			d = 100_000_000_000_000_000 // saturates a jitter's scale
		}
		reach["spaced"], reach["spaced clamps"] = true, reach["spaced clamps"] || d < 0
		return &schedModel{kind: "spaced", d: schedAtLeastZero(d), src: "schedule.Spaced(" + lit(d) + ")"}
	case 1:
		d, c := grid(), grid()*time.Duration(1+r.IntN(20))
		f := float64(r.IntN(41)-5) / 10
		if f < 1 {
			reach["exponential factor clamps"] = true
		}
		return &schedModel{kind: "exponential", d: schedAtLeastZero(d), f: max(f, 1), cap: schedAtLeastZero(c),
			src: fmt.Sprintf("schedule.Exponential(%s, %.1f, %s)", lit(d), f, lit(c))}
	case 2:
		n := r.IntN(11) - 2
		reach["recurs clamps"] = reach["recurs clamps"] || n < 0
		return &schedModel{kind: "recurs", n: max(n, 0), src: fmt.Sprintf("schedule.Recurs(%d)", n)}
	case 3:
		return &schedModel{kind: "forever", src: "schedule.Forever()"}
	case 4:
		a, l := genSched(r, depth-1, reach), grid()*time.Duration(1+r.IntN(3))
		return &schedModel{kind: "upto", a: a, d: schedAtLeastZero(l), src: fmt.Sprintf("schedule.UpTo(%s, %s)", a.src, lit(l))}
	case 5:
		a := genSched(r, depth-1, reach)
		lo, hi := float64(r.IntN(31)-5)/10, float64(r.IntN(31)-5)/10
		if r.IntN(3) == 0 {
			hi = 2000
		}
		seed := r.Uint64()
		l, h := schedFactorIn(lo), schedFactorIn(hi)
		if h < l {
			reach["jitter high below low"] = true
			h = l
		}
		return &schedModel{kind: "jitter", a: a, lo: l, hi: h, seed: seed,
			src: fmt.Sprintf("schedule.Jitter(%s, %.1f, %.1f, %d)", a.src, lo, hi, seed)}
	}
	a, b := genSched(r, depth-1, reach), genSched(r, depth-1, reach)
	if r.IntN(2) == 0 {
		return &schedModel{kind: "and", a: a, b: b, src: fmt.Sprintf("schedule.And(%s, %s)", a.src, b.src)}
	}
	return &schedModel{kind: "or", a: a, b: b, src: fmt.Sprintf("%s.Or(%s)", a.src, b.src)}
}

// TestScheduleAgreesWithModel: std schedule's Decide, lowered and run,
// gives the reference model's decision for every generated schedule, run
// count 0 to 13 and elapsed time on the model's grid. A failing case
// prints the schedule's Ergo source and both decisions;
// ERGO_SCHEDULE_SEED replays another seed. Generation goes on until each
// case of the model is reached.
//
// It builds Go because Decide is an Ergo body, which runs only lowered:
// the repository has no reference interpreter of Ergo. It builds one
// program for every case.
// The schedule laws state bounds (a cap, a limit, a jitter range, And and
// Or as max and min); this property pins each value, so it alone catches
// an exponent off by one, Recurs counting one run more, UpTo stopping at
// its limit exactly, a jitter draw that ignores the run count, and a
// constructor that clamps another way.
func TestScheduleAgreesWithModel(t *testing.T) {
	needsBuild(t)
	seed := uint64(13)
	if s := os.Getenv("ERGO_SCHEDULE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_SCHEDULE_SEED: %v", err)
		}
		seed = n
	}
	r := rand.New(rand.NewPCG(seed, 179))
	elapsed := []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 2900 * time.Millisecond, time.Hour}
	want := []string{"spaced", "spaced clamps", "exponential from zero", "exponential at its cap", "exponential under its cap", "exponential factor clamps",
		"recurs continues", "recurs stops", "recurs clamps", "upto stops", "upto continues at its limit exactly",
		"jitter of a stop", "jitter continues", "jitter saturates", "jitter high below low"}
	for _, k := range []string{"and", "or"} {
		for _, x := range []bool{true, false} {
			for _, y := range []bool{true, false} {
				want = append(want, fmt.Sprintf("%s of %v %v", k, x, y))
			}
		}
	}
	reach := map[string]bool{}
	var cases []*schedModel
	var lines []string
	missing := func() string {
		for _, w := range want {
			if !reach[w] {
				return w
			}
		}
		return ""
	}
	for len(cases) < 64 || missing() != "" {
		if len(cases) == 400 {
			t.Fatalf("seed %d: 400 schedules reach no %q", seed, missing())
		}
		m := genSched(r, 3, reach)
		i := len(cases)
		cases = append(cases, m)
		for runs := 0; runs < 14; runs++ {
			for _, e := range elapsed {
				line := fmt.Sprintf("%d %d %d s", i, runs, int64(e))
				if d, ok := m.decide(runs, e, reach); ok {
					line = fmt.Sprintf("%d %d %d c %d", i, runs, int64(e), int64(d))
				}
				lines = append(lines, line)
			}
		}
	}
	var src strings.Builder
	src.WriteString("import schedule\n\nfunc show(n schedule.Next) string {\n\tmatch n {\n\tcase schedule.Continue as c:\n\t\t\"c \\(int64(c.After))\"\n\tcase schedule.Stop:\n\t\t\"s\"\n\t}\n}\n\n")
	src.WriteString("func main() needs Console {\n\tlet cases = []schedule.Schedule{\n")
	for _, m := range cases {
		src.WriteString("\t\t" + m.src + ",\n")
	}
	src.WriteString("\t}\n\tlet elapsed = []duration{")
	for i, e := range elapsed {
		if i > 0 {
			src.WriteString(", ")
		}
		fmt.Fprintf(&src, "duration(int64(%d))", int64(e))
	}
	src.WriteString("}\n\tfor i, s in cases {\n\t\tfor runs in 0..14 {\n\t\t\tfor e in elapsed {\n")
	src.WriteString("\t\t\t\tConsole.Println(\"\\(i) \\(runs) \\(int64(e)) \\(show(schedule.Decide(s, runs, e)))\")\n\t\t\t}\n\t\t}\n\t}\n}\n")
	exes := buildAll(t, []*lowered{mustLower(t, "schedmodel", src.String())}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["schedmodel"])
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s", seed, code, firstLines(stderr, 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(lines) {
		t.Fatalf("seed %d: %d lines, want %d", seed, len(got), len(lines))
	}
	for i := range lines {
		if got[i] != lines[i] {
			m := cases[i/(14*len(elapsed))]
			t.Fatalf("seed %d: case, runs, elapsed: Ergo %q, model %q\nschedule: %s", seed, got[i], lines[i], m.src)
		}
	}
}

// stdScheduleLawsProbe restates the schedule laws, and the tasks laws of
// Retry and Repeat, whose runner is TestStdScheduleLawsRun, as Ergo tests
// over the exported API: within, draw, gen and lawSchedule are the
// packages' own helpers, restated.
const stdScheduleLawsProbe = `import schedule
import sync
import tasks
import testing
import time

func within(n int64, lo int64, hi int64) int64 {
	let span = hi - lo + 1
	let r = n % span
	if r < 0 {
		lo + (r + span)
	} else {
		lo + r
	}
}

func mix(x uint64) uint64 {
	let mut z = x + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^ (z >> 31)
}

func draw(seed uint64, k int, n int) int { int(mix(seed+uint64(k)*0x9e3779b97f4a7c15) % uint64(n)) }

func gen(seed uint64, depth int) schedule.Schedule {
	let kinds = if depth > 0 { 8 } else { 4 }
	let d = duration(int64(draw(seed, 1, 1000)) * 1000000)
	let left = mix(seed + 2)
	let right = mix(seed + 3)
	match draw(seed, 0, kinds) {
	case 0:
		schedule.Spaced(d)
	case 1:
		schedule.Exponential(d, 1.0+float64(draw(seed, 4, 30))/10.0, duration(int64(draw(seed, 5, 10000))*1000000))
	case 2:
		schedule.Recurs(draw(seed, 4, 8))
	case 3:
		schedule.Forever()
	case 4:
		schedule.UpTo(gen(left, depth-1), duration(int64(d)*10))
	case 5:
		schedule.Jitter(gen(left, depth-1), float64(draw(seed, 4, 20))/10.0, float64(draw(seed, 5, 20))/10.0, right)
	case 6:
		schedule.And(gen(left, depth-1), gen(right, depth-1))
	case _:
		schedule.Or(gen(left, depth-1), gen(right, depth-1))
	}
}

type lawFailed { Run int }

func lawSchedule(k int, limit int) schedule.Schedule {
	let retries = schedule.Recurs(1 + (k%6+6)%6)
	let l = duration(int64(1+(limit%5000+5000)%5000) * 1000000)
	schedule.Exponential(10ms, 2.0, 1s).And(retries).UpTo(l)
}

test ScheduleRecursStopsAfter(n int64, at int64) {
	let k = int(within(n, 0, 64))
	let e = duration(within(at, 0, 1000000000000))
	let s = schedule.Recurs(k)
	assert schedule.Decide(s, k+1, e) == schedule.Stop
	for runs in 1..k+1 {
		assert schedule.Decide(s, runs, e) == schedule.Continue{After: 0s}
	}
}

test ScheduleUpToNeverPassesLimit(seed uint64, limit int64, runs int64, at int64) {
	let l = duration(within(limit, 0, 10000000000))
	let e = duration(within(at, 0, 20000000000))
	match schedule.Decide(schedule.UpTo(gen(seed, 3), l), int(within(runs, 1, 20)), e) {
	case schedule.Continue as c:
		assert e+c.After <= l
	case schedule.Stop:
	}
}

test ScheduleExponentialUnderCap(base int64, cap int64, factor int64, runs int64) {
	let c = duration(within(cap, 0, 1000000000000))
	let s = schedule.Exponential(duration(within(base, 0, 1000000000000)), 1.0+float64(within(factor, 0, 40))/10.0, c)
	let r = int(within(runs, 1, 200))
	match (schedule.Decide(s, r, 0s), schedule.Decide(s, r+1, 0s)) {
	case (schedule.Continue as x, schedule.Continue as y):
		assert x.After <= c && x.After <= y.After
	case _:
		assert false
	}
}

test ScheduleJitterInBounds(seed uint64, every int64, runs int64) {
	let d = duration(within(every, 0, 1099511627776))
	let low = float64(draw(seed, 0, 30)) / 10.0
	let high = low + float64(draw(seed, 1, 30))/10.0
	match schedule.Decide(schedule.Jitter(schedule.Spaced(d), low, high, seed), int(within(runs, 1, 1000)), 0s) {
	case schedule.Continue as c:
		let got = float64(int64(c.After))
		assert got >= float64(int64(d))*low-1.0 && got <= float64(int64(d))*high+1.0
	case schedule.Stop:
		assert false
	}
}

test ScheduleAndIsMaxOrIsMin(a uint64, b uint64, runs int64, at int64) {
	let x = gen(a, 2)
	let y = gen(b, 2)
	let r = int(within(runs, 1, 20))
	let e = duration(within(at, 0, 20000000000))
	let both = schedule.Decide(schedule.And(x, y), r, e)
	let either = schedule.Decide(schedule.Or(x, y), r, e)
	match (schedule.Decide(x, r, e), schedule.Decide(y, r, e)) {
	case (schedule.Continue as p, schedule.Continue as q):
		let long = if p.After >= q.After { p.After } else { q.After }
		let short = if p.After <= q.After { p.After } else { q.After }
		assert both == schedule.Continue{After: long} && either == schedule.Continue{After: short}
	case (schedule.Continue as p, schedule.Stop):
		assert both == schedule.Stop && either == p
	case (schedule.Stop, schedule.Continue as q):
		assert both == schedule.Stop && either == q
	case (schedule.Stop, schedule.Stop):
		assert both == schedule.Stop && either == schedule.Stop
	}
}

test TasksRetryFollowsDecide(failing int, work int, k int, limit int) {
	let s = lawSchedule(k, limit)
	let w = duration(int64((work%300+300)%300) * 1000000)
	let n = (failing%10 + 10) % 10
	let mut runs = 0
	let mut at = 0s
	let mut want = 0
	for want == 0 {
		runs = runs + 1
		at = at + w
		if runs > n {
			want = runs
		} else {
			match schedule.Decide(s, runs, at) {
			case schedule.Continue as c:
				at = at + c.After
			case schedule.Stop:
				want = 0 - runs
			}
		}
	}
	let c = testing.NewVirtualClock(0)
	let calls = sync.NewRef[int](0)
	with context { Scheduler: c, time.Clock: c } {
		let start = time.Clock.Now()
		let got = tasks.Retry(s, func() int fails lawFailed needs Scheduler {
			Scheduler.Sleep(w)
			let i = sync.Modify(calls, func(v int) (int, int) { (v + 1, v + 1) })
			if i <= n {
				return lawFailed{Run: i}
			}
			i
		}) catch {
		case lawFailed as f:
			0 - f.Run
		}
		assert got == want && calls.Get() == runs && time.Clock.Now().Sub(start) == at
	}
}

test TasksRepeatFollowsDecide(work int, k int, limit int) {
	let s = lawSchedule(k, limit)
	let w = duration(int64((work%300+300)%300) * 1000000)
	let mut runs = 0
	let mut at = 0s
	let mut more = true
	for more {
		runs = runs + 1
		at = at + w
		match schedule.Decide(s, runs, at) {
		case schedule.Continue as c:
			at = at + c.After
		case schedule.Stop:
			more = false
		}
	}
	let c = testing.NewVirtualClock(0)
	let calls = sync.NewRef[int](0)
	with context { Scheduler: c, time.Clock: c } {
		let start = time.Clock.Now()
		let got = tasks.Repeat(s, func() int needs Scheduler {
			Scheduler.Sleep(w)
			sync.Modify(calls, func(v int) (int, int) { (v + 1, v + 1) })
		})
		assert got == runs && calls.Get() == runs && time.Clock.Now().Sub(start) == at
	}
}
`

// stdScheduleLawStatements is the statement of each law the probe
// restates, as the ledger writes it, by ledger key.
var stdScheduleLawStatements = map[string]string{
	"schedule RecursStopsAfter":     `RecursStopsAfter(n int64, at int64) { let k = int(within(n, 0, 64)) let e = duration(within(at, 0, 1000000000000)) let s = Recurs(k) let mut ok = Decide(s, k+1, e) == Stop for runs in 1..k+1 { ok = ok && Decide(s, runs, e) == Continue{After: 0s} } ok }`,
	"schedule UpToNeverPassesLimit": `UpToNeverPassesLimit(seed uint64, limit int64, runs int64, at int64) { let l = duration(within(limit, 0, 10000000000)) let e = duration(within(at, 0, 20000000000)) match Decide(UpTo(gen(seed, 3), l), int(within(runs, 1, 20)), e) { case Continue as c: e+c.After <= l case Stop: true } }`,
	"schedule ExponentialUnderCap":  `ExponentialUnderCap(base int64, cap int64, factor int64, runs int64) { let c = duration(within(cap, 0, 1000000000000)) let s = Exponential(duration(within(base, 0, 1000000000000)), 1.0+float64(within(factor, 0, 40))/10.0, c) let r = int(within(runs, 1, 200)) match (Decide(s, r, 0s), Decide(s, r+1, 0s)) { case (Continue as x, Continue as y): x.After <= c && x.After <= y.After case _: false } }`,
	"schedule JitterInBounds":       `JitterInBounds(seed uint64, every int64, runs int64) { let d = duration(within(every, 0, 1099511627776)) let low = float64(draw(seed, 0, 30)) / 10.0 let high = low + float64(draw(seed, 1, 30))/10.0 match Decide(Jitter(Spaced(d), low, high, seed), int(within(runs, 1, 1000)), 0s) { case Continue as c: let got = float64(int64(c.After)) got >= float64(int64(d))*low-1.0 && got <= float64(int64(d))*high+1.0 case Stop: false } }`,
	"schedule AndIsMaxOrIsMin":      `AndIsMaxOrIsMin(a uint64, b uint64, runs int64, at int64) { let x = gen(a, 2) let y = gen(b, 2) let r = int(within(runs, 1, 20)) let e = duration(within(at, 0, 20000000000)) let both = Decide(And(x, y), r, e) let either = Decide(Or(x, y), r, e) match (Decide(x, r, e), Decide(y, r, e)) { case (Continue as p, Continue as q): let long = if p.After >= q.After { p.After } else { q.After } let short = if p.After <= q.After { p.After } else { q.After } both == Continue{After: long} && either == Continue{After: short} case (Continue as p, Stop): both == Stop && either == p case (Stop, Continue as q): both == Stop && either == q case (Stop, Stop): both == Stop && either == Stop } }`,
	"tasks RetryFollowsDecide":      `RetryFollowsDecide(failing int, work int, k int, limit int) { let s = lawSchedule(k, limit) let w = duration(int64((work%300+300)%300) * 1000000) let n = (failing%10 + 10) % 10 let mut runs = 0 let mut at = 0s let mut want = 0 for want == 0 { runs = runs + 1 at = at + w if runs > n { want = runs } else { match schedule.Decide(s, runs, at) { case schedule.Continue as c: at = at + c.After case schedule.Stop: want = 0 - runs } } } let c = testing.NewVirtualClock(0) let calls = sync.NewRef[int](0) with context { Scheduler: c, time.Clock: c } { let start = time.Clock.Now() let got = Retry(s, func() int fails lawFailed needs Scheduler { Scheduler.Sleep(w) let i = sync.Modify(calls, func(v int) (int, int) { (v + 1, v + 1) }) if i <= n { return lawFailed{Run: i} } i }) catch { case lawFailed as f: 0 - f.Run } got == want && calls.Get() == runs && time.Clock.Now().Sub(start) == at } }`,
	"tasks RepeatFollowsDecide":     `RepeatFollowsDecide(work int, k int, limit int) { let s = lawSchedule(k, limit) let w = duration(int64((work%300+300)%300) * 1000000) let mut runs = 0 let mut at = 0s let mut more = true for more { runs = runs + 1 at = at + w match schedule.Decide(s, runs, at) { case schedule.Continue as c: at = at + c.After case schedule.Stop: more = false } } let c = testing.NewVirtualClock(0) let calls = sync.NewRef[int](0) with context { Scheduler: c, time.Clock: c } { let start = time.Clock.Now() let got = Repeat(s, func() int needs Scheduler { Scheduler.Sleep(w) sync.Modify(calls, func(v int) (int, int) { (v + 1, v + 1) }) }) got == runs && calls.Get() == runs && time.Clock.Now().Sub(start) == at } }`,
}

// TestStdScheduleLawsRun runs the probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdScheduleLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdschedule", stdScheduleLawsProbe, stdScheduleLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
