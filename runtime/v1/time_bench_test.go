package v1

import (
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Benchmarks of std time against hand-written Go (std-align S2b; plan
// section 4, Benchmark). The ergo side is the Go the lowering emits for
// time.Clock.Now() bound to time.NewHost(), for t.Format(time.RFC3339) and
// for time.Since(t), copied from `ergo build -work` at this unit's head:
// benchTime is the program's time.Time, benchClock its Clock interface,
// benchHostClock the operations conversion of time.Host, and
// benchLayout the Layout union.

type benchTime struct {
	sec, nsec, mono int64
	hasMono         bool
}

type benchClock interface{ Now(*rt.Task) benchTime }

type benchHost struct{}

func (benchHost) Now(env *benchEnv) benchTime {
	env.task.Check()
	r := TimeHostNow()
	return benchRead(r.Sec, r.Nsec, r.Mono)
}

func benchRead(sec, nsec, mono int64) benchTime {
	return benchTime{sec: sec, nsec: nsec, mono: mono, hasMono: true}
}

type benchHostClock struct{ v benchHost }

func (a *benchHostClock) Now(t *rt.Task) benchTime { return a.v.Now(&benchEnv{task: t}) }

type benchLayout interface{ isBenchLayout() }

type benchRFC3339 struct{}

func (benchRFC3339) isBenchLayout() {}

type benchCustom struct{ Text string }

func (benchCustom) isBenchLayout() {}

func benchLayoutText(l benchLayout) string {
	switch v := l.(type) {
	case benchRFC3339:
		return "2006-01-02T15:04:05Z07:00"
	case benchCustom:
		return v.Text
	}
	panic("no case")
}

func (t benchTime) Format(layout benchLayout) string {
	return TimeFormat(t.sec, t.nsec, benchLayoutText(layout))
}

func (t benchTime) Sub(u benchTime) time.Duration {
	if t.hasMono && u.hasMono {
		return TimeSubMono(t.mono, u.mono)
	}
	return TimeSub(t.sec, t.nsec, u.sec, u.nsec)
}

var benchSink any

// BenchmarkTimeNow: Clock.Now() through the env against time.Now().
func BenchmarkTimeNow(b *testing.B) {
	task := rt.Root("bench")
	var clock benchClock = &benchHostClock{}
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = clock.Now(task).mono
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = time.Now().UnixNano()
		}
	})
}

// BenchmarkTimeFormat: t.Format(time.RFC3339) against Go's in UTC.
func BenchmarkTimeFormat(b *testing.B) {
	t := benchTime{sec: 1_700_000_000, nsec: 5}
	g := time.Unix(1_700_000_000, 5).UTC()
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = t.Format(benchRFC3339{})
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = g.Format(time.RFC3339)
		}
	})
}

// BenchmarkTimeSince: time.Since(t) on the host clock against Go's.
func BenchmarkTimeSince(b *testing.B) {
	task := rt.Root("bench")
	var clock benchClock = &benchHostClock{}
	start := clock.Now(task)
	gstart := time.Now()
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = clock.Now(task).Sub(start)
		}
	})
	b.Run("go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			benchSink = time.Since(gstart)
		}
	})
}
