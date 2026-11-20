package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestTimeRuns (std-align S2b): std time lowers and runs. time.Since on a
// Manual clock prints 1.5s; Format and Parse in UTC with named and Custom
// layouts; a ParseError in a catch; a task asleep on the Manual wakes when
// Advance reaches its deadline and not before; the monotonic reading
// decides Before, After and Sub when wall and monotonic disagree; Unix and
// Add carry nanoseconds into seconds; a host clock's two reads never fall;
// the host's Since by its monotonic reading and, for a time with none, by
// the wall, between two reads' Sub (std-speed-1, NP4f-17: kills a reversed
// monotonic difference or Sub in Host.Elapsed).
func TestTimeRuns(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{{name: "time", src: stdTimeProgram, stdout: stdTimeOutput}})
}

const stdTimeProgram = `import time
import task

func nap(d duration) string needs Scheduler | time.Clock {
	let start = time.Clock.Now()
	Scheduler.Sleep(d)
	"\(time.Since(start))"
}

func layout(l time.Layout) string {
	match l {
	case time.RFC3339:
		"rfc3339"
	case time.RFC3339Nano:
		"nano"
	case time.DateTime:
		"datetime"
	case time.DateOnly:
		"date"
	case time.TimeOnly:
		"time"
	case time.Custom as c:
		"custom " + c.Text
	}
}

func main() fails time.ParseError needs Console | Ambient {
	let m = time.NewManual(0)
	let t0 = m.Now()
	m.Advance(1500ms)
	Console.Println("\(with context { time.Clock: m } { time.Since(t0) })")

	let t = time.Unix(1700000000, 5)
	Console.Println(t.Format(time.RFC3339Nano))
	Console.Println(t.Add(1500ms).Format(time.RFC3339Nano) + " " + t.Add(-1ns).Format(time.DateTime))
	Console.Println(time.Unix(0, -1).Format(time.RFC3339Nano) + " " + time.Unix(0, 3000000001).Format(time.TimeOnly))
	Console.Println(t.Format(time.Custom{Text: "Jan 2 2006"}) + " " + t.Format(time.DateOnly) + " " + layout(time.Custom{Text: "x"}))
	let p = try time.Parse(time.RFC3339, "2026-11-08T10:00:00+02:00")
	Console.Println(p.Format(time.RFC3339) + " \(p.Sub(t)) \(p.After(t)) \(p.Before(t)) \(p.Equal(p))")
	let bad = time.Parse(time.RFC3339, "nope") catch {
	case time.ParseError as e:
		Console.Println(e.Value + ": " + e.Message)
		t
	}
	Console.Println("\(bad.Equal(t)) \(t.UnixSeconds()) \(t.Nanosecond())")

	let clock = time.NewManual(100)
	let sleeper = with context { time.Clock: clock, Scheduler: clock } {
		task.Go() { nap(2s) }
	}
	let live = NewLiveScheduler()
	for clock.Sleepers() == 0 {
		live.Sleep(1ms)
	}
	clock.Advance(1999ms)
	Console.Println("\(clock.Sleepers())")
	clock.Advance(1ms)
	Console.Println(sleeper.Wait())
	Console.Println(clock.Now().Format(time.RFC3339Nano) + " \(clock.Sleepers())")

	// Two clocks whose wall and monotonic readings disagree: Before, After
	// and Sub use the monotonic readings, as Go's do; times with no
	// monotonic reading compare by the wall.
	let late = time.NewManual(100)
	let early = time.NewManual(0)
	early.Advance(5s)
	let x = late.Now()
	let y = early.Now()
	Console.Println("\(x.Before(y)) \(x.After(y)) \(y.Sub(x)) \(x.Equal(y)) \(time.Unix(100, 0).Before(time.Unix(5, 0)))")
	Console.Println("\(t.Add(999999999ns).Nanosecond()) \(t.Add(-6ns).Nanosecond()) \(time.Unix(0, 3000000001).Nanosecond()) \(time.Unix(0, -1).UnixSeconds())")

	let h = time.NewHost()
	let a = h.Now()
	let b = h.Now()
	Console.Println("\(!b.Before(a)) \(b.Sub(a) >= 0s)")
	// The host's Elapsed (std-speed-1): read after b, by the monotonic
	// clock alone, it is no less than b.Sub(a) and no more than a later
	// read's Sub (NP4f-17: a reversed difference saturates to the largest
	// duration); of a time with no monotonic reading it is the wall's,
	// here over 45 years.
	let since = with context { time.Clock: h } { time.Since(a) }
	let later = h.Now().Sub(a)
	let wall = with context { time.Clock: h } { time.Since(time.Unix(0, 0)) }
	Console.Println("\(since >= b.Sub(a) && since <= later) \(wall > 400000h)")
}
`

const stdTimeOutput = "1.5s\n" +
	"2023-11-14T22:13:20.000000005Z\n" +
	"2023-11-14T22:13:21.500000005Z 2023-11-14 22:13:20\n" +
	"1969-12-31T23:59:59.999999999Z 00:00:03\n" +
	"Nov 14 2023 2023-11-14 custom x\n" +
	"2026-11-08T08:00:00Z 26145h46m39.999999995s true false true\n" +
	"nope: parsing time \"nope\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"nope\" as \"2006\"\n" +
	"true 1700000000 5\n" +
	"1\n" +
	"2s\n" +
	"1970-01-01T00:01:42Z 0\n" +
	"true false 5s false false\n" +
	"4 999999999 1 -1\n" +
	"true true\n" +
	"true true\n"

// stdTimeLawsProbe restates the time laws whose runner is
// TestStdTimeLawsRun as Ergo tests over the std's providers. The member
// law Timeline.AdvanceMovesRead reads the Timeline through the Manual that
// holds it: Now's monotonic part is Read's Mono, so Sub of two Nows is the
// difference of the two Reads.
const stdTimeLawsProbe = `import queue
import utf8
import task
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

func anyYear(n int64) int64 { within(n, -62135596800, 253402300799) }

test TimeWireIsFormat(sec int64, nsec int64, near bool, pre []byte) {
	let s = if near { anyYear(sec) } else { sec }
	let t = time.Unix(s, within(nsec, 0, 999999999))
	assert utf8.String(time.Wire(t, pre)) == utf8.String(pre) + t.Format(time.RFC3339Nano)
}

test TimeTimeRoundTrips(x time.Time) {
	let back = time.ParseTime(time.Wire(x, []byte{})) catch {
	case _:
		assert false
		return
	}
	assert back == x
}

test TimeFormatParseRoundTrip(sec int64, nsec int64) {
	let t = time.Unix(sec, within(nsec, 0, 999999999))
	let back = time.Parse(time.RFC3339Nano, t.Format(time.RFC3339Nano)) catch {
	case time.ParseError:
		assert false
		t
	}
	assert back.Equal(t) && back == t
}

test TimeUnixMilliInverts(ms int64, sec int64, nsec int64) {
	let t = time.Unix(within(sec, -9000000000000000, 9000000000000000), within(nsec, 0, 999999999))
	let floor = time.UnixMilli(time.Milliseconds(t))
	assert time.Milliseconds(time.UnixMilli(ms)) == ms && !floor.After(t) && t.Sub(floor) < 1ms
}

test TimeTruncateIsFloor(sec int64, nsec int64, n int64) {
	let t = time.Unix(anyYear(sec), within(nsec, 0, 999999999))
	let d = duration(within(n, 1, 864000000000000000))
	let f = time.Truncate(t, d)
	assert !f.After(t) && t.Sub(f) < d && time.Truncate(f, d) == f && time.Truncate(t.Add(d), d).Equal(f.Add(d))
}

test TimeRoundIsNearest(sec int64, nsec int64, n int64) {
	let t = time.Unix(anyYear(sec), within(nsec, 0, 999999999))
	let d = duration(within(n, 1, 864000000000000000))
	let f = time.Truncate(t, d)
	let near = if t.Sub(f)+t.Sub(f) < d { f } else { f.Add(d) }
	assert time.Round(t, d).Equal(near)
}

test TimeIntervalContainsStart(sec int64, n int64) {
	let start = time.Unix(anyYear(sec), 0)
	let end = start.Add(duration(within(n, -1000000000000000, 1000000000000000)))
	let i = time.NewInterval(start, end) catch {
	case time.Reversed:
		assert end.Before(start)
		return
	}
	assert !end.Before(start) && i.Contains(start) == start.Before(end) && !i.Contains(end) && i.Length() == end.Sub(start)
}

test TimeCompareOrders(a int64, b int64) {
	let t = time.Unix(within(a, -2, 2), 0)
	let u = time.Unix(within(b, -2, 2), 0)
	assert time.Compare(t, u) == -time.Compare(u, t) && (time.Compare(t, u) < 0) == t.Before(u) && (time.Compare(t, u) == 0) == t.Equal(u)
}

test TimeOverlapsIffIntersect(a int64, b int64, c int64, d int64) {
	let x = time.Unix(within(a, 0, 100), 0)
	let y = x.Add(duration(within(b, 0, 100) * 1000000000))
	let u = time.Unix(within(c, 0, 100), 0)
	let v = u.Add(duration(within(d, 0, 100) * 1000000000))
	let p = time.NewInterval(x, y) catch {
	case time.Reversed:
		assert false
		return
	}
	let q = time.NewInterval(u, v) catch {
	case time.Reversed:
		assert false
		return
	}
	match time.Intersect(p, q) {
	case Some[time.Interval] as r:
		assert time.Overlaps(p, q) && r.Value.Length() > 0ns && p.Contains(r.Value.Start()) && q.Contains(r.Value.Start())
	case None:
		assert !time.Overlaps(p, q)
	}
}

test TimeSubAddInverse(sec int64, nsec int64, n int64) {
	let t = time.Unix(anyYear(sec), nsec)
	let d = duration(within(n, -4000000000000000000, 4000000000000000000))
	assert t.Add(d).Sub(t) == d
}

func anyMonth(n int64) time.Month {
	match time.MonthOf(int(within(n, 1, 12))) {
	case Some[time.Month] as m:
		m.Value
	case None:
		time.January
	}
}

test TimeDateRoundTrip(sec int64, nsec int64) {
	let t = time.Unix(anyYear(sec), within(nsec, 0, 999999999))
	let c = t.Calendar()
	assert time.Date(c.Year, c.Month, c.Day, c.Hour, c.Minute, c.Second, c.Nanosecond) == t
}

test TimeWeekdayStep(sec int64) {
	let t = time.Unix(anyYear(sec), 0)
	assert time.WeekdayNumber(t.Add(24h).Calendar().Weekday) == (time.WeekdayNumber(t.Calendar().Weekday)+1)%7
}

test TimeDateNormalises(year int64, month int64, day int64, hour int64) {
	let y = int(within(year, 1, 9999))
	let m = anyMonth(month)
	let d = within(day, -400, 400)
	let h = within(hour, -100, 100)
	assert time.Date(y, m, int(d), int(h), 0, 0, 0).Equal(time.Date(y, m, 1, 0, 0, 0, 0).Add(duration(((d-1)*24 + h) * 3600000000000)))
}

test TimeYearDayCounts(sec int64) {
	let t = time.Unix(anyYear(sec), 0)
	let c = t.Calendar()
	let first = time.Date(c.Year, time.January, 1, 0, 0, 0, 0)
	assert c.YearDay == time.Count(time.Date(c.Year, c.Month, c.Day, 0, 0, 0, 0).Sub(first), 24h)+1
}

test TimeMonthOfInvertsNumber(n int) {
	match time.MonthOf(n) {
	case Some[time.Month] as m:
		assert time.MonthNumber(m.Value) == n
	case None:
		assert n < 1 || n > 12
	}
}

test TimeTimelineAdvanceMovesRead(n int64) {
	let m = time.NewManual(0)
	let d = duration(within(n, 0, 1099511627775))
	let before = m.Now()
	m.Advance(d)
	assert m.Now().Sub(before) == d
}

test TaskSleepAdvancesNow(n int64) {
	let m = time.NewManual(0)
	let d = duration(1 + (n%1099511627775+1099511627775)%1099511627775)
	let woke = queue.New[duration](1)
	let t = with context { Scheduler: m, time.Clock: m } {
		task.Go() {
			let before = time.Clock.Now()
			Scheduler.Sleep(d)
			try woke.Push(time.Clock.Now().Sub(before))
		}
	}
	for m.Sleepers() == 0 && woke.Len() == 0 {
	}
	m.Advance(d)
	try t.Wait()
	assert try woke.Pop() >= d
}
`

// stdTimeLawStatements is the statement of each law the probe restates,
// the law's source text as the ledger writes it, by ledger key;
// lawledger.Run holds it equal to the ledger (fu-ledger-runner).
var stdTimeLawStatements = map[string]string{
	"time WireIsFormat":              `WireIsFormat(sec int64, nsec int64, near bool, pre []byte) { let s = if near { anyYear(sec) } else { sec } let t = Time{sec: s, nsec: within(nsec, 0, 999999999), mono: 0, hasMono: false} utf8.String(t.Wire(pre)) == utf8.String(pre)+t.Format(RFC3339Nano) }`,
	"time TimeRoundTrips":            `TimeRoundTrips(x Time) { try ParseTime(x.Wire([]byte{})) == x }`,
	"task SleepAdvancesNow":          `SleepAdvancesNow(n int64) { let m = time.NewManual(0) let d = duration(1 + (n%1099511627775+1099511627775)%1099511627775) let woke = queue.New[duration](1) let t = with context { Scheduler: m, time.Clock: m } { Go() { let before = time.Clock.Now() Scheduler.Sleep(d) try woke.Push(time.Clock.Now().Sub(before)) } } for m.Sleepers() == 0 && woke.Len() == 0 { } m.Advance(d) try t.Wait() try woke.Pop() >= d }`,
	"time FormatParseRoundTrip":      `FormatParseRoundTrip(sec int64, nsec int64) { let t = Unix(sec, within(nsec, 0, 999999999)) let back = Parse(RFC3339Nano, t.Format(RFC3339Nano)) catch { case ParseError: return false } back.Equal(t) && back == t }`,
	"time DateRoundTrip":             `DateRoundTrip(sec int64, nsec int64) { let t = Unix(anyYear(sec), within(nsec, 0, 999999999)) let c = t.Calendar() Date(c.Year, c.Month, c.Day, c.Hour, c.Minute, c.Second, c.Nanosecond) == t }`,
	"time WeekdayStep":               `WeekdayStep(sec int64) { let t = Unix(anyYear(sec), 0) WeekdayNumber(t.Add(24h).Calendar().Weekday) == (WeekdayNumber(t.Calendar().Weekday)+1)%7 }`,
	"time DateNormalises":            `DateNormalises(year int64, month int64, day int64, hour int64) { let y = int(within(year, 1, 9999)) let m = anyMonth(month) let d = within(day, -400, 400) let h = within(hour, -100, 100) Date(y, m, int(d), int(h), 0, 0, 0).Equal(Date(y, m, 1, 0, 0, 0, 0).Add(duration(((d-1)*24 + h) * 3600000000000))) }`,
	"time YearDayCounts":             `YearDayCounts(sec int64) { let t = Unix(anyYear(sec), 0) let c = t.Calendar() let first = Date(c.Year, January, 1, 0, 0, 0, 0) c.YearDay == Count(Date(c.Year, c.Month, c.Day, 0, 0, 0, 0).Sub(first), 24h)+1 }`,
	"time MonthOfInvertsNumber":      `MonthOfInvertsNumber(n int) { match MonthOf(n) { case Some[Month] as m: MonthNumber(m.Value) == n case None: n < 1 || n > 12 } }`,
	"time UnixMilliInverts":          `UnixMilliInverts(ms int64, sec int64, nsec int64) { let t = Unix(within(sec, -9000000000000000, 9000000000000000), within(nsec, 0, 999999999)) let floor = UnixMilli(Milliseconds(t)) Milliseconds(UnixMilli(ms)) == ms && !floor.After(t) && t.Sub(floor) < 1ms }`,
	"time TruncateIsFloor":           `TruncateIsFloor(sec int64, nsec int64, n int64) { let t = Unix(anyYear(sec), within(nsec, 0, 999999999)) let d = duration(within(n, 1, 864000000000000000)) let f = Truncate(t, d) !f.After(t) && t.Sub(f) < d && Truncate(f, d) == f && Truncate(t.Add(d), d).Equal(f.Add(d)) }`,
	"time RoundIsNearest":            `RoundIsNearest(sec int64, nsec int64, n int64) { let t = Unix(anyYear(sec), within(nsec, 0, 999999999)) let d = duration(within(n, 1, 864000000000000000)) let f = Truncate(t, d) let near = if t.Sub(f)+t.Sub(f) < d { f } else { f.Add(d) } Round(t, d).Equal(near) }`,
	"time CompareOrders":             `CompareOrders(a int64, b int64) { let t = Unix(within(a, -2, 2), 0) let u = Unix(within(b, -2, 2), 0) Compare(t, u) == -Compare(u, t) && (Compare(t, u) < 0) == t.Before(u) && (Compare(t, u) == 0) == t.Equal(u) }`,
	"time IntervalContainsStart":     `IntervalContainsStart(sec int64, n int64) { let start = Unix(anyYear(sec), 0) let end = start.Add(duration(within(n, -1000000000000000, 1000000000000000))) let i = NewInterval(start, end) catch { case Reversed: return end.Before(start) } !end.Before(start) && i.Contains(start) == start.Before(end) && !i.Contains(end) && i.Length() == end.Sub(start) }`,
	"time OverlapsIffIntersect":      `OverlapsIffIntersect(a int64, b int64, c int64, d int64) { let x = Unix(within(a, 0, 100), 0) let y = x.Add(duration(within(b, 0, 100) * 1000000000)) let u = Unix(within(c, 0, 100), 0) let v = u.Add(duration(within(d, 0, 100) * 1000000000)) let p = NewInterval(x, y) catch { case Reversed: return false } let q = NewInterval(u, v) catch { case Reversed: return false } match Intersect(p, q) { case Some[Interval] as r: Overlaps(p, q) && r.Value.Length() > 0ns && p.Contains(r.Value.Start()) && q.Contains(r.Value.Start()) case None: !Overlaps(p, q) } }`,
	"time SubAddInverse":             `SubAddInverse(sec int64, nsec int64, n int64) { let t = Unix(anyYear(sec), nsec) let d = duration(within(n, -4000000000000000000, 4000000000000000000)) t.Add(d).Sub(t) == d }`,
	"time Timeline.AdvanceMovesRead": `AdvanceMovesRead(n int64) { let d = duration(within(n, 0, 1099511627775)) let before = Read() Advance(d) Read().Mono-before.Mono == int64(d) }`,
}

// TestStdTimeLawsRun runs the time probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdTimeLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdtime", stdTimeLawsProbe, stdTimeLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// TestTimeEffectCases ports the sleep cases of Effect's TestClock tests
// (packages/effect/test/TestClock.test.ts at the conformance pin 460272d3)
// to time.Manual, which is Effect's TestClock: each prints its output
// after a line "== <case id>", and each is a subtest named after its case
// id in conformance/effect-cases.json. Effect's adjust also waits until the
// woken fibers have run; a Manual's Advance wakes the sleepers, and the
// program reads their results with Wait. A program waits until the tasks
// are asleep (Sleepers) before it advances, as Effect's fork has run to
// its sleep before adjust. The run takes no wall time near the hours it
// sleeps: Manual time is virtual, and the program's exit is the event the
// test waits for (no wall-clock bound; AGENTS.md, Tests).
//
// It builds Go because it is the evidence of Effect conformance cases
// (conformance/effect-cases.json): what the built program prints is each
// case's outcome. Restored at the batch-13 integration: T5 cut it, which
// left its cases with no evidence test.
func TestTimeEffectCases(t *testing.T) {
	needsBuild(t)
	const src = `import task
import time

func main() needs Console {
	// TestClock.test.ts:8, sleep - does not require passage of wall time
	Console.Println("== effect.test-clock.admitted-fork")
	let a = time.NewManual(0)
	let elapsed = with context { Scheduler: a } {
		task.Go() {
			Scheduler.Sleep(10h)
			true
		}
	}
	for a.Sleepers() == 0 {
	}
	a.Advance(11h)
	Console.Println("\(elapsed.Wait())")

	// TestClock.test.ts:18, sleep - delays effects until time is adjusted
	Console.Println("== effect.test-clock.delays-until-adjusted")
	let b = time.NewManual(0)
	let delayed = with context { Scheduler: b } {
		task.Go() {
			Scheduler.Sleep(10h)
			true
		}
	}
	for b.Sleepers() == 0 {
	}
	b.Advance(9h)
	Console.Println("asleep \(b.Sleepers())")
	b.Advance(11h)
	Console.Println("\(delayed.Wait()) asleep \(b.Sleepers())")

	// TestClock.test.ts:31, sleep - handles multiple sleeps
	Console.Println("== effect.test-clock.multiple-sleeps")
	let c = time.NewManual(0)
	let world = with context { Scheduler: c } {
		task.Go() {
			Scheduler.Sleep(3h)
			"World!"
		}
	}
	let hello = with context { Scheduler: c } {
		task.Go() {
			Scheduler.Sleep(1h)
			"Hello, "
		}
	}
	for c.Sleepers() < 2 {
	}
	c.Advance(1h)
	let first = hello.Wait()
	Console.Println(first + "| asleep \(c.Sleepers())")
	c.Advance(4h)
	Console.Println(first + world.Wait())
}
`
	run := mustLower(t, "timecases", src)
	exes := buildAll(t, []*lowered{run}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["timecases"])
	head, sections, _ := strings.Cut(stdout, "== ")
	if code != 0 || head != "" {
		t.Errorf("exit %d\nstdout %q\nstderr %s", code, stdout, firstLines(stderr, 10))
	}
	t.Run("effect.test-clock.admitted-fork", func(t *testing.T) {
		effectCaseSection(t, sections, "true\n")
	})
	t.Run("effect.test-clock.delays-until-adjusted", func(t *testing.T) {
		effectCaseSection(t, sections, "asleep 1\ntrue asleep 0\n")
	})
	t.Run("effect.test-clock.multiple-sleeps", func(t *testing.T) {
		effectCaseSection(t, sections, "Hello, | asleep 1\nHello, World!\n")
	})
}
