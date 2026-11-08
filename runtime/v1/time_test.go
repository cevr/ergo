package v1

import (
	"math/rand/v2"
	"runtime"
	"sort"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// The Go runners of std time (std-align S2b; docs/spec/stdlib.md, time).

// manualSleeper is one task asleep on a Timeline in TestManualClockModel.
type manualSleeper struct {
	task     *rt.Task
	deadline int64 // model time, ns since the start
	woke     chan bool
}

// TestManualClockModel is the model check of time.Manual (the red
// acceptance check of S2b). A reference model holds the time and the
// sleepers with their deadlines. Generated sequences of Now, Sleep(d) in a
// task of its own, Advance(d) and a stop of one sleeper run against the
// model and a Timeline, 500 seeds. After each step: Now equals the model
// time and never falls; each sleeper whose deadline the time reached has
// woken, and only those (a sleeper wakes exactly when the time reaches its
// deadline); a stopped sleeper never wakes, and its timer is gone; and at
// the end no goroutine is left.
func TestManualClockModel(t *testing.T) {
	before := runtime.NumGoroutine()
	for seed := uint64(0); seed < 500; seed++ {
		manualModelRun(t, seed)
		if t.Failed() {
			t.Fatalf("seed %d", seed)
		}
	}
	waitFor(t, "goroutines to end", func() bool { return runtime.NumGoroutine() <= before })
}

func manualModelRun(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 2))
	startSec := r.Int64N(4_000_000_000) - 2_000_000_000
	line := NewTimeline(startSec, 0)
	var now int64 // the model time
	var lastMono int64
	var asleep []*manualSleeper
	check := func(step string) {
		t.Helper()
		got := line.Read()
		want := time.Unix(startSec, now)
		if got.Sec != want.Unix() || got.Nsec != int64(want.Nanosecond()) || got.Mono != now {
			t.Fatalf("seed %d %s: Read %+v, model %d ns past %d", seed, step, got, now, startSec)
		}
		if got.Mono < lastMono {
			t.Fatalf("seed %d %s: Now fell from %d to %d", seed, step, lastMono, got.Mono)
		}
		lastMono = got.Mono
		// Each woken sleeper's deadline is reached; each other one waits.
		kept := asleep[:0]
		for _, s := range asleep {
			if s.deadline <= now {
				select {
				case ok := <-s.woke:
					if !ok {
						t.Fatalf("seed %d %s: a sleeper due at %d was interrupted", seed, step, s.deadline)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("seed %d %s: a sleeper due at %d did not wake at %d", seed, step, s.deadline, now)
				}
				continue
			}
			select {
			case <-s.woke:
				t.Fatalf("seed %d %s: a sleeper due at %d woke at %d", seed, step, s.deadline, now)
			default:
			}
			kept = append(kept, s)
		}
		asleep = kept
		if n := line.Sleepers(); n != len(asleep) {
			t.Fatalf("seed %d %s: Sleepers %d, model %d", seed, step, n, len(asleep))
		}
	}
	for step := 0; step < 24; step++ {
		switch op := r.IntN(10); {
		case op < 4: // Sleep(d) in a task of its own
			d := r.Int64N(5_000) - 500 // some at or below zero
			s := &manualSleeper{task: rt.Root("sleeper"), deadline: now + max(d, 0), woke: make(chan bool, 1)}
			go func() {
				defer func() {
					if p := recover(); p != nil {
						if p != rt.Interrupted {
							panic(p)
						}
						s.woke <- false
					}
				}()
				line.Wait(s.task, time.Duration(d))
				s.woke <- true
			}()
			if d <= 0 {
				// A sleep of zero or less returns at once.
				if ok := <-s.woke; !ok {
					t.Fatalf("seed %d: Sleep(%d) was interrupted", seed, d)
				}
			} else {
				asleep = append(asleep, s)
				waitFor(t, "the sleeper to park", func() bool { return line.Sleepers() == len(asleep) })
			}
			check("sleep")
		case op < 8: // Advance(d)
			d := r.Int64N(3_000)
			line.Advance(time.Duration(d))
			now += d
			check("advance")
		case op < 9 && len(asleep) > 0: // stop one sleeper
			i := r.IntN(len(asleep))
			s := asleep[i]
			s.task.Interrupt()
			if ok := <-s.woke; ok {
				t.Fatalf("seed %d: a stopped sleeper woke", seed)
			}
			asleep = append(asleep[:i], asleep[i+1:]...)
			waitFor(t, "the stopped sleeper's timer to go", func() bool { return line.Sleepers() == len(asleep) })
			check("stop")
		default: // Now
			check("now")
		}
	}
	// End the run: wake every sleeper.
	if len(asleep) > 0 {
		var last int64
		for _, s := range asleep {
			last = max(last, s.deadline)
		}
		line.Advance(time.Duration(last - now))
		now = last
		check("drain")
	}
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestManualAdvanceWakesAllDue: one Advance past several deadlines, ties
// included, wakes every sleeper due and leaves none (the firing order is
// rt.Virtual's, tested there).
func TestManualAdvanceWakesAllDue(t *testing.T) {
	line := NewTimeline(0, 0)
	ds := []time.Duration{30, 10, 20, 10}
	order := make(chan int, len(ds))
	for i, d := range ds {
		task := rt.Root("sleeper")
		go func() {
			line.Wait(task, d)
			order <- i
		}()
		line.v.AwaitTimers(i + 1)
	}
	line.Advance(25)
	var got []int
	for range 3 {
		got = append(got, <-order)
	}
	sort.Ints(got)
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 || line.Sleepers() != 1 {
		t.Fatalf("Advance(25) woke %v, Sleepers %d; want [1 2 3] and 1", got, line.Sleepers())
	}
	line.Advance(5)
	if last := <-order; last != 0 {
		t.Fatalf("woke %d last", last)
	}
	if line.Sleepers() != 0 {
		t.Fatalf("Sleepers %d after the Advance", line.Sleepers())
	}
}

// TestManualAdvanceNegativeIsADefect: time never falls.
func TestManualAdvanceNegativeIsADefect(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Advance(-1) did not panic")
		}
	}()
	NewTimeline(0, 0).Advance(-1)
}

// timeFromBodies is the Go time a Reading names.
func timeFromBodies(r Reading) time.Time { return time.Unix(r.Sec, r.Nsec) }

// TestTimeLaws runs the time laws over the Go bodies: NowNeverFalls on the
// host clock and on a Timeline; FormatParseRoundTrip over every second of
// years 1 to 9999 and every nanosecond (10,000 seeded cases plus the
// edges); SubAddInverse; and Sub's saturation, against Go's time.Time.
func TestTimeLaws(t *testing.T) {
	// NowNeverFalls: host readings, compared by Mono as Before does.
	prev := TimeHostNow()
	for range 10_000 {
		next := TimeHostNow()
		if next.Mono < prev.Mono {
			t.Fatalf("NowNeverFalls: host Mono fell from %d to %d", prev.Mono, next.Mono)
		}
		prev = next
	}
	line := NewTimeline(5, 0)
	a := line.Read()
	line.Advance(7)
	if b := line.Read(); b.Mono < a.Mono || b.Mono-a.Mono != 7 {
		t.Fatalf("NowNeverFalls/AdvanceMovesRead: %+v then %+v", a, b)
	}

	r := rand.New(rand.NewPCG(1, 2))
	const first, last = -62135596800, 253402300799
	secs := []int64{first, last, 0, -1, 1}
	nsecs := []int64{0, 999_999_999, 1, 500_000_000}
	for range 10_000 {
		secs = append(secs, first+r.Int64N(last-first+1))
		nsecs = append(nsecs, r.Int64N(1_000_000_000))
	}
	nano := "2006-01-02T15:04:05.999999999Z07:00"
	for i, sec := range secs {
		nsec := nsecs[i%len(nsecs)]
		text := TimeFormat(sec, nsec, nano)
		if want := time.Unix(sec, nsec).UTC().Format(time.RFC3339Nano); text != want {
			t.Fatalf("Format(%d, %d) = %q, Go %q", sec, nsec, text, want)
		}
		back, err := TimeParse(nano, text)
		if err != nil || back.Sec != sec || back.Nsec != nsec || back.Mono != 0 {
			t.Fatalf("FormatParseRoundTrip(%d, %d): %q parsed to %+v, %v", sec, nsec, text, back, err)
		}
		d := time.Duration(r.Int64N(8_000_000_000_000_000_000) - 4_000_000_000_000_000_000)
		u := time.Unix(sec, nsec).Add(d)
		if got := TimeSub(u.Unix(), int64(u.Nanosecond()), sec, nsec); got != d {
			t.Fatalf("SubAddInverse(%d, %d, %d) = %d", sec, nsec, d, got)
		}
	}
	// Sub saturates as Go's does.
	if got := TimeSub(last, 0, first, 0); got != time.Duration(1<<63-1) {
		t.Errorf("TimeSub saturates up: %d", got)
	}
	if got := TimeSub(first, 0, last, 0); got != time.Duration(-1<<63) {
		t.Errorf("TimeSub saturates down: %d", got)
	}
	if got := TimeSubMono(1<<62, -(1 << 62)); got != time.Duration(1<<63-1) {
		t.Errorf("TimeSubMono saturates up: %d", got)
	}
	if got := TimeSubMono(-(1 << 62), 1<<62); got != time.Duration(-1<<63) {
		t.Errorf("TimeSubMono saturates down: %d", got)
	}
	if got := TimeSubMono(10, 3); got != 7 {
		t.Errorf("TimeSubMono(10, 3) = %d", got)
	}
}

// TestTimeParseReadsNoZone: Parse reads a zone abbreviation against UTC,
// never the host's zone, and fails ParseError with Go's message.
func TestTimeParseReadsNoZone(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	time.Local = time.FixedZone("EST", -5*3600)
	r, err := TimeParse("2006-01-02 15:04 MST", "2026-01-02 10:00 EST")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC).Unix(); r.Sec != want {
		t.Fatalf("Parse read the host zone: sec %d, want %d", r.Sec, want)
	}
	if got := TimeFormat(0, 0, "15:04 MST"); got != "00:00 UTC" {
		t.Fatalf("Format(0) = %q, want UTC", got)
	}
	_, err = TimeParse(time.RFC3339, "nope")
	pe, ok := err.(ParseError)
	if !ok || pe.Layout != time.RFC3339 || pe.Value != "nope" || pe.Message == "" {
		t.Fatalf("Parse(nope) = %#v", err)
	}
	if _, goErr := time.Parse(time.RFC3339, "nope"); pe.Message != goErr.Error() {
		t.Fatalf("message %q, Go %q", pe.Message, goErr.Error())
	}
}

// TestTimeHostNowReadsOnce: a host Reading's wall and monotonic parts come
// from one read: Mono is the time since hostStart by the monotonic clock.
func TestTimeHostNowReadsOnce(t *testing.T) {
	before := time.Now()
	r := TimeHostNow()
	after := time.Now()
	if r.Mono < int64(before.Sub(hostStart)) || r.Mono > int64(after.Sub(hostStart)) {
		t.Fatalf("Mono %d outside [%d, %d]", r.Mono, before.Sub(hostStart), after.Sub(hostStart))
	}
	wall := timeFromBodies(r)
	if wall.Before(before.Round(0).Add(-time.Second)) || wall.After(after.Round(0).Add(time.Second)) {
		t.Fatalf("wall %v outside %v..%v", wall, before, after)
	}
}
