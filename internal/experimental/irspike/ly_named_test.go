package irspike

import "testing"

// TestIRAgreesOnRouterMemory is TestIRAgreesOnExamples on one target named
// by its folder, examples/router/memory's test build (EXAMPLES BY NAME: a
// part number moves when an example is added). It kills
// ly-fix-generic-rt-leaf-unlinked: router's memory laws recover an actor,
// whose hosts need the hostwalk leaf that a generic runtime call links.
//
// It builds Go because a leaf that is not linked shows only when the
// compiled test runs and recovers the actor.
func TestIRAgreesOnRouterMemory(t *testing.T) { agreeOnNamed(t, "router/memory test") }

// TestIRAgreesOnShards is TestIRAgreesOnExamples on examples/shards/cmd/shards
// alone, named by its folder: std actor's ExitOf, whose F is a Go row
// (rowslot.go), takes an IR body there (ROWSLOT-IR), as TestIRReprosMatchSource's
// rowslot row does in small.
//
// It builds Go because a row's spelling fails only the Go build of the
// IR file.
func TestIRAgreesOnShards(t *testing.T) { agreeOnNamed(t, "shards/cmd/shards") }

// agreeOnNamed runs agreeOn on the example target whose key is key, and
// fails when no target has that key, so a renamed example cannot pass by
// running nothing.
func agreeOnNamed(t *testing.T, key string) {
	t.Helper()
	for _, e := range exampleDomain(t) {
		if e.key() == key {
			t.Run(key, func(t *testing.T) { agreeOn(t, e) })
			return
		}
	}
	t.Fatalf("no example target %q", key)
}
