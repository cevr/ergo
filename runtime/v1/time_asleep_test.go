package v1

import (
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// TestTimeAsleepSeesBelow holds time.Asleep's rule (FK4-g2-g1): a task is
// asleep on a manual clock when it, or a task it started, sleeps there,
// and not when it waits on anything else or has ended. Three tasks of the
// clock's domain wait after an Advance of nothing: one sleeps, one starts
// a child that sleeps (a fetch that sleeps inside timeout.After or a task
// of its own), and one waits on a queue (a fetch waiting for a server).
// Mutant (rt-asleep-self-only): Asleep looks only at the sleeper itself,
// not its ancestors, so the child's sleep is missed and the ui test host's
// Settle waits for ever on that fetch.
func TestTimeAsleepSeesBelow(t *testing.T) {
	root := rt.Root("asleep")
	line := NewTimeline(root, 0, 0)
	sc := NewScope(root)
	q := New[int](1)
	direct := ownedGo(&sc, Site("direct"), func(ct *rt.Task) (bool, error) {
		line.Wait(ct, time.Hour)
		return true, nil
	})
	nested := ownedGo(&sc, Site("nested"), func(ct *rt.Task) (bool, error) {
		inner := NewScope(ct)
		child := ownedGo(&inner, Site("child"), func(gt *rt.Task) (bool, error) {
			line.Wait(gt, time.Hour)
			return true, nil
		})
		v, err := child.Wait()
		inner.End(nil)
		return v, err
	})
	waiting := ownedGo(&sc, Site("waiting"), func(ct *rt.Task) (bool, error) {
		_, err := q.Pop(ct)
		return err == nil, nil
	})
	line.Advance(root, 0)
	if !TimeAsleep(line, direct) || !TimeAsleep(line, nested) || TimeAsleep(line, waiting) {
		t.Fatalf("asleep: direct %v, nested %v, waiting %v; want true, true, false",
			TimeAsleep(line, direct), TimeAsleep(line, nested), TimeAsleep(line, waiting))
	}
	line.Advance(root, time.Hour)
	if TimeAsleep(line, direct) || TimeAsleep(line, nested) {
		t.Fatal("a task whose sleep ended is still asleep")
	}
	if err := q.Push(root, 1); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []*Task[bool]{direct, nested, waiting} {
		if _, err := tk.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	sc.End(nil)
}
