package lower

import (
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
)

// TestTimeRuns (std-align S2b): std time lowers and runs. time.Since on a
// Manual clock prints 1.5s; Format and Parse in UTC with named and Custom
// layouts; a ParseError in a catch; a task asleep on the Manual wakes when
// Advance reaches its deadline and not before; the monotonic reading
// decides Before, After and Sub when wall and monotonic disagree; Unix and
// Add carry nanoseconds into seconds; a host clock's two reads never fall.
func TestTimeRuns(t *testing.T) {
	runBehaviors(t, []behavior{{name: "time", src: stdTimeProgram, stdout: stdTimeOutput}})
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
// sleeps: the test bounds it.
func TestTimeEffectCases(t *testing.T) {
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
	start := time.Now()
	stdout, stderr, code := runExe(t, exes["timecases"])
	took := time.Since(start)
	head, sections, _ := strings.Cut(stdout, "== ")
	if code != 0 || head != "" || took > 30*time.Second {
		t.Errorf("exit %d after %v\nstdout %q\nstderr %s", code, took, stdout, firstLines(stderr, 10))
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
	"true true\n"

// stdTimeLawsProbe restates the time laws whose runner is
// TestStdTimeLawsRun as Ergo tests over the std's providers. The member
// law Timeline.AdvanceMovesRead reads the Timeline through the Manual that
// holds it: Now's monotonic part is Read's Mono, so Sub of two Nows is the
// difference of the two Reads.
const stdTimeLawsProbe = `import queue
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

test TimeFormatParseRoundTrip(sec int64, nsec int64) {
	let t = time.Unix(anyYear(sec), within(nsec, 0, 999999999))
	let back = time.Parse(time.RFC3339Nano, t.Format(time.RFC3339Nano)) catch {
	case time.ParseError:
		assert false
		t
	}
	assert back.Equal(t) && back == t
}

test TimeSubAddInverse(sec int64, nsec int64, n int64) {
	let t = time.Unix(anyYear(sec), nsec)
	let d = duration(within(n, -4000000000000000000, 4000000000000000000))
	assert t.Add(d).Sub(t) == d
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
	"task SleepAdvancesNow":          `SleepAdvancesNow(n int64) { let m = time.NewManual(0) let d = duration(1 + (n%1099511627775+1099511627775)%1099511627775) let woke = queue.New[duration](1) let t = with context { Scheduler: m, time.Clock: m } { Go() { let before = time.Clock.Now() Scheduler.Sleep(d) try woke.Push(time.Clock.Now().Sub(before)) } } for m.Sleepers() == 0 && woke.Len() == 0 { } m.Advance(d) try t.Wait() try woke.Pop() >= d }`,
	"time FormatParseRoundTrip":      `FormatParseRoundTrip(sec int64, nsec int64) { let t = Unix(anyYear(sec), within(nsec, 0, 999999999)) let back = Parse(RFC3339Nano, t.Format(RFC3339Nano)) catch { case ParseError: return false } back.Equal(t) && back == t }`,
	"time SubAddInverse":             `SubAddInverse(sec int64, nsec int64, n int64) { let t = Unix(anyYear(sec), nsec) let d = duration(within(n, -4000000000000000000, 4000000000000000000)) t.Add(d).Sub(t) == d }`,
	"time Timeline.AdvanceMovesRead": `AdvanceMovesRead(n int64) { let d = duration(within(n, 0, 1099511627775)) let before = Read() Advance(d) Read().Mono-before.Mono == int64(d) }`,
}

// TestStdTimeLawsRun runs the time probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdTimeLawsRun(t *testing.T) {
	if laws := lawProbe(t, "stdtime", stdTimeLawsProbe, stdTimeLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
