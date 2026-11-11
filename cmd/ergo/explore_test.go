package main

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/runtime/explore/sched"
)

// TestReplayTestChecks: the failing test that a search prints is an Ergo
// test the checker accepts once the file imports explore, and its
// schedule, read back relative to the root, names the tasks of the failing
// test's window, whatever children the root started in the tests before
// it. It checks in process (the checker); `ergo test --explore` running
// the printed test is the batch's end-to-end receipt.
func TestReplayTestChecks(t *testing.T) {
	src := `import sync
import task

test First {
	let r = sync.NewRef(0)
	let a = task.Go() { r.Set(1) }
	a.Wait()
}

test Second {
	let r = sync.NewRef(0)
	let a = task.Go() { r.Set(r.Get() + 1) }
	a.Wait()
	assert r.Get() == 2
}
`
	paths := regexp.MustCompile(`"([0-9.]+)"`)
	for off := 0; off <= 3; off++ {
		first := "ok First\n"
		out := first + "FAIL Second: assert failed\nFAIL (1 of 2 tests)\n"
		var run sched.Run
		pick := func(c sched.Choice) {
			run.Choices = append(run.Choices, c)
			run.Picks = append(run.Picks, 0)
		}
		before := []sched.Step{{Task: "0", Kind: "spawn"}}
		for k := range off {
			before = append(before, sched.Step{Task: "0." + string(rune('0'+k)), Kind: "start"})
		}
		pick(sched.Choice{Enabled: before})
		child := "0." + string(rune('0'+off))
		window := []string{"0", child, child + ".0", "0"}
		for _, p := range window {
			pick(sched.Choice{Enabled: []sched.Step{{Task: p, Kind: "write"}}, Out: int64(len(first))})
		}
		pick(sched.Choice{Enabled: []sched.Step{{Task: "0", Kind: "end"}}, Out: int64(len(out))})
		run.Outcome = sched.Outcome{Failure: "1 of 2 tests failed", Value: out}

		l := loaded{snapshot: report.SourceSnapshot{Text: src}}
		text, ok := replayTest(l, run)
		if !ok {
			t.Fatalf("offset %d: no test printed", off)
		}
		a := sketchfront.Check("import explore\n" + src + text)
		for _, d := range a.Diagnostics {
			t.Errorf("offset %d: the printed test does not check: %s %s\n%s", off, d.Code, d.Message, text)
		}
		replay, _, _ := strings.Cut(text[strings.Index(text, "explore.Replay"):], "\n")
		var got []string
		for _, m := range paths.FindAllStringSubmatch(replay, -1) {
			got = append(got, sched.Absolute(m[1], "0", off))
		}
		if !slices.Equal(got, window) {
			t.Fatalf("offset %d: the printed schedule %s names %v, want the window %v", off, replay, got, window)
		}
	}
}
