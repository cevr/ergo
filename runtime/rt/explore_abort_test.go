//go:build ergoexplore

package rt

import (
	"errors"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// TestExploreAbortReleasesTheRun: a run whose root waits where the
// scheduler cannot see (a raw channel receive) never reaches another
// point, so only AbortExplore, called from outside the run, ends it.
// Explore then returns with the abort as the run's Stopped, a second abort
// finds no run, and the next run starts at once (no ErrExploreBusy): a
// watchdog never leaks the explorer to the next test. The deadline only
// names the hang an ignored abort leaves.
//
// Mutant: AbortExplore records the abort but does not end the run.
func TestExploreAbortReleasesTheRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	ran := make(chan Result, 1)
	go func() {
		ran <- Explore(time.Unix(0, 0), Cooperative{}, func(*Task) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	why := errors.New("watchdog: the run did not end")
	if !AbortExplore(why) {
		t.Fatal("AbortExplore found no active run")
	}
	select {
	case res := <-ran:
		if res.Stopped != why {
			t.Fatalf("Stopped %v; want the abort", res.Stopped)
		}
	case <-testwait.After(t):
		t.Fatal("Explore did not return after AbortExplore: the abort was ignored")
	}
	if AbortExplore(why) {
		t.Error("a second abort found an active run")
	}
	if res := Explore(time.Unix(0, 0), Cooperative{}, func(*Task) error { return nil }); res.Exit != nil || res.Stopped != nil || res.Deadlock != nil {
		t.Errorf("the next run: %+v; want a clean end", res)
	}
}
