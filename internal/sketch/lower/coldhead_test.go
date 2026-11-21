package lower

import (
	"fmt"
	"strings"
	"testing"
)

// TestColdHeadsWriteGrowthOnce is the counting guard of the cold heads
// (coldhead.go, BUN5-cold): over TestInlineAgrees's programs, an inlined
// pushOn writes its growth guard as a call of pushOn, so the program's Go
// holds the growth (its Concat) once, in pushOn's own body; under the
// reference (coldLowering "copy") every inlined site copies it. The
// rule's programs type-check (each guard jumps to its site's label).
// TestInlineAgrees runs both and compares their output.
//
// It catches the inliner copying the guard again (cold-head-off in the
// mutant ledger), which no output check can see.
func TestColdHeadsWriteGrowthOnce(t *testing.T) {
	var rule []*lowered
	calls, copies := 0, 0
	for seed := uint64(1); seed <= 8; seed++ {
		src, _ := inlineProgram(seed, map[string]bool{})
		p, s := lowerInlineMode(t, fmt.Sprintf("cold%d", seed), src, false)
		rule = append(rule, p)
		calls += s.ColdCalls
		if n := strings.Count(p.mainGo(), "SlicesConcat"); n != 1 {
			t.Errorf("seed %d: the rule writes the growth %d times, want once (pushOn's body)", seed, n)
		}
		saved := coldLowering
		coldLowering = "copy"
		q, sq := lowerInlineMode(t, fmt.Sprintf("copy%d", seed), src, false)
		coldLowering = saved
		if sq.ColdCalls != 0 {
			t.Errorf("seed %d: the reference wrote %d cold calls", seed, sq.ColdCalls)
		}
		copies += strings.Count(q.mainGo(), "SlicesConcat") - 1
	}
	t.Logf("%d cold calls; %d copies of the growth under the reference", calls, copies)
	if calls == 0 || copies == 0 {
		t.Errorf("no inlined pushOn: %d cold calls, %d copies under the reference", calls, copies)
	}
	typecheckGo(t, rule)
}
