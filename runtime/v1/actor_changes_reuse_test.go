package v1

import (
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestActorChangesReuse: a loop that subscribes to Changes once per signal
// and leaves at the state it waits for (std actor.WaitFor after a Send)
// reuses the subscription, its buffer and its wake channel (ACT-CH): per
// round it allocates at least two times fewer than the reference, which
// makes them anew for each subscription (changesReuse "off"). It counts
// allocations, not time.
//
// Mutants it catches that nothing else does (runtime/v1 mutants.json):
// changes-spare-dropped, changes-buf-not-rewound.
func TestActorChangesReuse(t *testing.T) {
	perRound := func(mode string) float64 {
		saved := changesReuse
		changesReuse = mode
		defer func() { changesReuse = saved }()
		root := rt.Root("changes")
		step := func(_ *rt.Task, s int, e int) (rdec, error) { return rdec{decideStay, e}, nil }
		a := MachineStart(Site("counter"), root, releaser{make(chan sleep)}, nil, 0, step,
			func(*rt.Task, int, int) (rdec, error) { return rdec{kind: decideIgnore}, nil },
			func(int) time.Duration { return ActorNoTimer() }, noWork[int], hasNoWork, runNoWork[int],
			func(d rdec) int { return d.kind },
			func(s int, d rdec) int {
				if d.kind == decideStay {
					return s + 1
				}
				return s
			}, func(*rt.Task, rdec) {}, 1)
		defer func() { _ = a.Stop(root) }()
		want := 0
		round := func() {
			want++
			if err := a.Send(root, 1); err != nil {
				t.Fatal(err)
			}
			for s := range a.Changes(root, everyState) {
				if s >= want {
					break
				}
			}
		}
		round() // the first subscription makes what the later ones reuse
		perRound := testing.AllocsPerRun(200, round)
		if mode != "" {
			return perRound
		}
		// One subscription that reads n states, sending each next signal
		// as it reads one (a Select or an Every): after the first, a state
		// takes the buffer's array again.
		long := func(n int) func() {
			return func() {
				last := want + n
				if err := a.Send(root, 1); err != nil {
					t.Fatal(err)
				}
				for s := range a.Changes(root, everyState) {
					if s >= last {
						break
					}
					if s > want {
						want = s
						if err := a.Send(root, 1); err != nil {
							t.Fatal(err)
						}
					}
				}
				want = last
			}
		}
		short, longer := testing.AllocsPerRun(20, long(10)), testing.AllocsPerRun(20, long(1010))
		if per := (longer - short) / 1000; per > 0.1 {
			t.Errorf("a long subscription allocates %.2f times per state it reads (%.0f for 10, %.0f for 1010): its buffer is not rewound", per, short, longer)
		}
		a.mu.Lock()
		size := cap(a.spareSub.buf)
		a.mu.Unlock()
		if size > 4 {
			t.Errorf("a subscription that read 1010 states one at a time holds a buffer of %d: it grows instead of rewinding", size)
		}
		return perRound
	}
	got, ref := perRound(""), perRound("off")
	t.Logf("allocations per round: %.2f (reused), %.2f (new each time)", got, ref)
	if got > ref-2 {
		t.Errorf("a round allocates %.2f times, the reference %.2f: a Changes subscription is made anew", got, ref)
	}
}
