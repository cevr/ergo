package main

import (
	"strings"
	"testing"
)

// The explore leg builds a test binary and searches its schedules, so its
// time follows the host's load: only -test-timeout, the safety net for a
// hung one, stops it, never a case's -timeout (batch 18: a healthy jobq
// search failed a 30 s bound at load 50). The fake search runs a second;
// the case bound here is a millisecond.
//
// Mutant: the explore leg runs under -timeout (explored.go), so the
// search fails "timed out after 1ms".
func TestExploreLegOutlastsTheCaseTimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.program("searcher", "#!/bin/sh\necho hi\n# EXPLORESLOW\n", "default\n", map[string]map[string]string{
		"basic": {"expected.out": "hi\n"},
	}, map[string]string{"explored": ". Fixture --explore dfs --preempt 1\n"})
	_, out := f.run("-timeout", "1ms", "-test-timeout", caseTimeout(t), "searcher")
	if !strings.Contains(out, "ok searcher: explore . Fixture (") {
		t.Fatalf("want the explore leg to pass past the case bound:\n%s", out)
	}
}
