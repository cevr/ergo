//go:build ergoexplore

package v1

import (
	"fmt"
	"strings"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// exploreMain runs main's body under the controlled scheduler (runtime.md,
// Explored schedules): `ergo test --explore` builds the test binary with
// the ergoexplore tag, and its tests run on one baton under the
// cooperative schedule. A run that cannot end (a deadlock) or that did a
// step outside the scheduler is a defect, and its text names the tasks.
func exploreMain(body func(*rt.Task) error) error {
	res := rt.Explore(time.Unix(0, 0).UTC(), rt.Cooperative{}, func(t *rt.Task) error {
		_, err := guarded(t, func(t *rt.Task) (struct{}, error) { return struct{}{}, body(t) })
		return err
	})
	if why := exploreDefect(res); why != "" {
		return &rt.Cause{Reasons: []rt.Reason{{Kind: rt.KindDefect, Value: why}}}
	}
	return res.Exit
}

// exploreDefect is the text of a run that did not end as a program does,
// or "".
func exploreDefect(res rt.Result) string {
	switch {
	case res.Escape != "":
		return "explore: " + res.Escape + " (a step outside the scheduler: the run does not replay)"
	case res.Stopped != nil:
		return "explore: the schedule stopped the run: " + res.Stopped.Error()
	case res.Deadlock != nil:
		var waits []string
		for _, p := range res.Deadlock {
			waits = append(waits, fmt.Sprintf("task %s waits to %v", p.Task, p.Kind))
		}
		return fmt.Sprintf("explore: no task can run after %d steps (a deadlock): %s", res.Picks, strings.Join(waits, "; "))
	}
	return ""
}
