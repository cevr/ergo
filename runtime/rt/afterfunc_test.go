package rt

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

var afterFuncSeed = flag.Uint64("afterfunc.seed", 0, "replay one seed of TestVirtualAfterFuncIsTimer")

// TestVirtualAfterFuncIsTimer is a seeded property against NewTimer, the
// reference: on one virtual clock, each step arms a timer and an alarm
// (AfterFunc) of the same span, stops a pair, or advances; after each
// Advance, the alarms whose functions ran are the pairs whose timers
// fired, and each pair's two Stops agree. A failing seed prints its replay.
// Mutants it kills, which no other test sees: virtual-alarm-never-runs (fire
// sends on the alarm's nil channel instead of starting f; the wait for the
// fired alarm hangs and the watchdog names the seed), virtual-alarm-now-
// never-runs (a span of zero or less is armed instead of started), and
// virtual-alarm-stop-keeps (Stop leaves the alarm armed).
func TestVirtualAfterFuncIsTimer(t *testing.T) {
	seeds := []uint64{}
	if *afterFuncSeed != 0 {
		seeds = append(seeds, *afterFuncSeed)
	} else {
		for s := uint64(1); s <= 64; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		afterFuncMatchesTimer(t, seed)
	}
}

func afterFuncMatchesTimer(t *testing.T, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 11))
	v := NewVirtual(time.Unix(0, 0))
	type pair struct {
		timer, alarm Timer
		fired        bool // the timer's channel delivered
	}
	var pairs []*pair
	ran := make(chan int, 256)
	replay := fmt.Sprintf("seed %d; replay: go test ./runtime/rt -run TestVirtualAfterFuncIsTimer -afterfunc.seed %d", seed, seed)
	// settle takes from ran each pair whose timer has fired since the last
	// look, and fails on a function that ran with no fire of its timer.
	settle := func(step int) {
		t.Helper()
		want := 0
		for _, p := range pairs {
			if !p.fired {
				select {
				case <-p.timer.C():
					p.fired = true
					want++
				default:
				}
			}
		}
		got := map[int]bool{}
		for range want {
			select {
			case i := <-ran:
				got[i] = true
			case <-testwait.After(t):
				t.Fatalf("step %d: %d of %d fired alarms ran; %s", step, len(got), want, replay)
			}
		}
		for i := range got {
			if !pairs[i].fired {
				t.Fatalf("step %d: alarm %d ran, its timer has not fired; %s", step, i, replay)
			}
		}
	}
	for step := range 40 {
		switch rng.IntN(4) {
		case 0, 1:
			d := Span(rng.IntN(20) - 2)
			i := len(pairs)
			pairs = append(pairs, &pair{timer: v.NewTimer(d), alarm: v.AfterFunc(d, func() { ran <- i })})
		case 2:
			if len(pairs) > 0 {
				p := pairs[rng.IntN(len(pairs))]
				a, b := p.timer.Stop(), p.alarm.Stop()
				if a != b {
					t.Fatalf("step %d: the timer's Stop is %v, the alarm's %v; %s", step, a, b, replay)
				}
			}
		case 3:
			v.Advance(Span(rng.IntN(10)))
		}
		settle(step)
	}
	v.Advance(100)
	settle(40)
}
