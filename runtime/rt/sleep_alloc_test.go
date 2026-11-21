package rt

import (
	"testing"
	"time"
)

// TestVirtualSleepAllocatesNothing: a steady sleep on an auto clock, the
// timer of every testing.VirtualClock Sleep, allocates nothing: it takes
// the clock's spare timer, and a fire keeps the timer list's room
// (FU-sleep-timer-reuse). Mutants sleep-timer-not-reused (every sleep
// makes a timer), timers-pop-reslices (a fire reslices the list from
// the front) and sleep-timer-stays-fired are killed only here.
func TestVirtualSleepAllocatesNothing(t *testing.T) {
	if Exploring {
		t.Skip("under the explored scheduler each sleep has a timer of its own")
	}
	root := Root("sleeper")
	v := NewAuto(root, epoch)
	sleep := func() {
		if err := Sleep(root, v, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	sleep() // the spare timer and the list's room
	if n := testing.AllocsPerRun(1000, sleep); n != 0 {
		t.Fatalf("a sleep allocates %v times; want 0", n)
	}
	// Each of the 1,002 sleeps (AllocsPerRun warms up once) waited its
	// second: a reused timer starts unfired (mutant sleep-timer-stays-fired).
	if got, want := v.Now(), Instant(epoch.Add(1002*time.Second).UnixNano()); got != want {
		t.Fatalf("clock at %v after 1,002 sleeps of a second; want %v", got, want)
	}
}
