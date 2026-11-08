package lower

import (
	"regexp"
	"slices"
	"testing"
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
const stdTimeLawsProbe = `import time

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
`

// TestStdTimeLawsRun runs the time probe's tests with --seed 1, and holds
// that the probe restates exactly the ledger's laws whose runner is this
// test.
func TestStdTimeLawsRun(t *testing.T) {
	want := ledgerRunnerLaws(t, "TestStdTimeLawsRun")
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^test (\w+)\(`).FindAllStringSubmatch(stdTimeLawsProbe, -1) {
		got = append(got, m[1])
	}
	slices.Sort(want)
	slices.Sort(got)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("the probe restates %v; the ledger runs %v in TestStdTimeLawsRun", got, want)
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "stdtime", stdTimeLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["stdtime"], "--seed", "1")
	if code != 0 {
		t.Fatalf("the time law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	for _, name := range want {
		if !regexp.MustCompile(`(?m)^ok ` + name + `( \(100 cases\))?$`).MatchString(stdout) {
			t.Errorf("no ok line for %s:\n%s", name, stdout)
		}
	}
}
