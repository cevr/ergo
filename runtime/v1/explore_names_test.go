//go:build ergoexplore

package v1

import (
	"slices"
	"testing"

	"ergo.local/prototype/runtime/explore/sched"
	"ergo.local/prototype/runtime/rt"
)

// TestHandlesNamedBySource: an explored run names a queue and a Ref by
// the Ergo call that made each, at its source position, relative to the
// folder of the program's source map, and a second handle of one call
// (a loop) as "(2)". The probe below carries line directives, as lowered
// Go does, and a source map that puts two calls on the queue's line. It
// runs in process with the ergoexplore tag (the gate's explored worker
// step); no Go is built.
func TestHandlesNamedBySource(t *testing.T) {
	savedRegistered, savedDecls, savedCalls := registered, decls, calls
	defer func() { registered, decls, calls = savedRegistered, savedDecls, savedCalls }()
	type decl = struct {
		Name      string
		File      string
		Line, End int
	}
	type call = struct {
		Func, Callee string
		File         string
		Line, Column int
	}
	const probe = "/src/app/pool/probe.ergo"
	RegisterCalls([]decl{{Name: "main", File: "/src/app/main.ergo", Line: 1, End: 3}, {Name: "fill", File: probe, Line: 9, End: 13}},
		[]call{{Func: "fill", Callee: "limit", File: probe, Line: 10, Column: 3}, {Func: "fill", Callee: "queue.New", File: probe, Line: 10, Column: 14}, {Func: "fill", Callee: "sync.NewRef", File: probe, Line: 11, Column: 9}})
	res := exploreRun(func(t *rt.Task) error { return probeFill(t) }, sched.Cooperative{})
	if res.Exit != nil || exploreDefect(res) != "" {
		t.Fatalf("the probe failed: %v %s", res.Exit, exploreDefect(res))
	}
	// The names reach the search as the worker reports them, and its
	// words print a name for a number.
	o := outcomeOf(res)
	var names []string
	for k, n := range o.Handles {
		names = append(names, n)
		words := sched.Words(sched.Run{Choices: []sched.Choice{{Enabled: []sched.Step{{Task: "9", Kind: "push", Handle: k}}}}, Picks: []int{0}, Outcome: o})
		if want := "step 0: task 9: push on " + n; words[0] != want {
			t.Errorf("handle %d: the words are %q, want %q", k, words[0], want)
		}
	}
	for _, want := range []string{"queue.New at pool/probe.ergo:10:14", "queue.New at pool/probe.ergo:10:14 (2)", "sync.NewRef at pool/probe.ergo:11:9", "sync.NewRef at pool/probe.ergo:11:9 (2)"} {
		if !slices.Contains(names, want) {
			t.Errorf("no handle is named %q; the names: %q", want, names)
		}
	}
}

// probeFill makes a queue and a Ref twice and touches each, at the
// positions its line directives give.
func probeFill(t *rt.Task) error {
	for range 2 {
//line /src/app/pool/probe.ergo:10:3
		q := New[int](1)
//line /src/app/pool/probe.ergo:11:9
		r := NewRef(0)
		if err := q.Push(t, 1); err != nil {
			return err
		}
		r.Set(1)
	}
	return nil
}
