package v1

import (
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestMailboxRendezvousAllocatesNothing: an Ask of an actor whose inbox
// has capacity 0, so each Push waits for the runner (a rendezvous),
// allocates nothing once the actor runs: the Push waits on its task's
// park word, with the mailbox's spare sender, in a senders' slice that
// keeps its room (PARK3). Mutants spare-sender-dropped (each wait makes a
// sender) and senders-lose-room (popSender always reslices from 1, so
// append regrows) are killed only here: the equality property with the
// Queue checks what the mailbox does, not what it costs.
func TestMailboxRendezvousAllocatesNothing(t *testing.T) {
	root := rt.Root("rendezvous")
	a := MachineStart(Site("rendezvous"), root, releaser{make(chan sleep)}, nil, 0,
		func(_ *rt.Task, s int, e int) (int, error) { return decideStay, nil },
		func(*rt.Task, int, int) (int, error) { return decideIgnore, nil },
		func(int) time.Duration { return ActorNoTimer() }, noWork[int], hasNoWork, runNoWork[int],
		func(d int) int { return d },
		func(s int, d int) int { return s + 1 }, func(*rt.Task, int) {}, 0)
	defer func() { _ = a.Stop(rt.Root("stop")) }()
	if _, err := a.Ask(root, 1); err != nil { // the actor runs
		t.Fatal(err)
	}
	got := testing.AllocsPerRun(1000, func() {
		if _, err := a.Ask(root, 1); err != nil {
			t.Fatal(err)
		}
	})
	if got != 0 {
		t.Fatalf("an Ask at capacity 0 allocates %v times; want 0", got)
	}
}
