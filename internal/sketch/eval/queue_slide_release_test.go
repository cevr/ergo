package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// slideReleaseProbe slides resources through a full queue: each Slide gives
// back the value it evicted (stdlib.md, PubSub and send strategies 1), the
// caller owns it (resources R42), and its scope releases it, so every
// resource made is closed exactly once: the evicted one by the binding's
// scope, the one left by the queue's release.
const slideReleaseProbe = `import queue
import sync

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

type Res { id int } implements Closer

func Close(r Res) { say("close \(r.id)") }

test SlideReleasesTheEvicted {
	journal.Set([]string{})
	if true {
		let q = queue.New[Res](1)
		let first = try q.Slide(Res{id: 1})
		assert first == None
		let evicted = try q.Slide(Res{id: 2})
		say("slid")
	}
	let j = journal.Get()
	assert j == []string{"slid", "close 1", "close 2"} || j == []string{"slid", "close 2", "close 1"}
}
`

// TestEvalSlideReleasesTheEvicted runs slideReleaseProbe in the evaluator
// (SLIDE-RELEASE). Mutant only it kills (testdata/mutants.json):
// slide-eval-evicted-dropped (the evaluator's Slide gives back None).
func TestEvalSlideReleasesTheEvicted(t *testing.T) {
	f, err := syntax.Parse(slideReleaseProbe)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	rep, err := Run(pkg, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 {
		t.Fatalf("the evaluator ran %d obligations, not the probe's 1: %+v", len(rep.Results), rep.Results)
	}
	for _, r := range rep.Results {
		if r.Status != Passed {
			t.Errorf("%s: %s (%s at %s:%d)", r.Name, r.Status, r.Reason, r.Site.File, r.Site.Line)
		}
	}
}
