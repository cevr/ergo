package eval

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

func checkTestSnippet(t *testing.T, src string) *check.Package {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pkg, diags := check.Check("sample", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v", diags)
	}
	return pkg
}

func TestStoredCaseReplayOrderAndStaleDigest(t *testing.T) {
	src := `func checkVal(x int) int {
	x
}

law Bound(x int) {
	checkVal(x) <= 5
}
`
	pkg := checkTestSnippet(t, src)

	// 1. Refute under seed 17 and capture the shrunk tape.
	b1 := DefaultBudget()
	b1.Seed = 17
	rep1, err := Run(pkg, b1)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if len(rep1.Results) != 1 || rep1.Results[0].Status != Refuted {
		t.Fatalf("expected refutation on seed 17, got %+v", rep1.Results)
	}
	r1 := rep1.Results[0]
	if len(r1.ShrunkTape) == 0 {
		t.Fatalf("expected non-empty ShrunkTape on refutation")
	}

	// 2. Replay stored tape under seed 99 (where random draws also refute):
	// stored tape MUST replay at case 0 before random draws.
	b2 := DefaultBudget()
	b2.Seed = 99
	b2.StoredCases = map[string][]StoredCase{
		r1.Name: {{
			Key:    "sample." + r1.Name,
			Digest: "h1:current",
			Tape:   r1.ShrunkTape,
			Stale:  false,
		}},
	}
	rep2, err := Run(pkg, b2)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	r2 := rep2.Results[0]
	if r2.Status != Refuted || !r2.StoredCaseHit || r2.StoredStale || r2.Case != 0 {
		t.Fatalf("expected matching stored-case hit at case 0, got %+v", r2)
	}
	line2, ok := VerdictLine(r2, "sample.ergo")
	if !ok || !strings.Contains(line2, "case 0, stored case") {
		t.Fatalf("expected 'case 0, stored case' in verdict line, got ok=%v line=%q", ok, line2)
	}

	// 3. Stale digest failing tape MUST be reported as extra case, digest changed,
	// never named as 'stored case'.
	b3 := DefaultBudget()
	b3.Seed = 101
	b3.StoredCases = map[string][]StoredCase{
		r1.Name: {{
			Key:    "sample." + r1.Name,
			Digest: "h1:old-digest",
			Tape:   r1.ShrunkTape,
			Stale:  true,
		}},
	}
	rep3, err := Run(pkg, b3)
	if err != nil {
		t.Fatalf("Run 3: %v", err)
	}
	r3 := rep3.Results[0]
	if r3.Status != Refuted || !r3.StoredCaseHit || !r3.StoredStale {
		t.Fatalf("expected stale stored-case hit, got %+v", r3)
	}
	line3, ok := VerdictLine(r3, "sample.ergo")
	if !ok || !strings.Contains(line3, "case 0, extra case, digest changed") {
		t.Fatalf("expected 'case 0, extra case, digest changed' in verdict line, got ok=%v line=%q", ok, line3)
	}
	if strings.Contains(line3, "stored case") {
		t.Fatalf("stale-digest verdict line must not say 'stored case': %q", line3)
	}

	// 4. Fixed body with stale stored tape: passes, records PassedStored, and
	// reports extra case(s): digest changed.
	fixed := checkTestSnippet(t, `func checkVal(x int) int {
	0
}

law Bound(x int) {
	checkVal(x) <= 5
}
`)
	b4 := DefaultBudget()
	b4.Seed = 103
	b4.StoredCases = b3.StoredCases
	rep4, err := Run(fixed, b4)
	if err != nil {
		t.Fatalf("Run 4: %v", err)
	}
	r4 := rep4.Results[0]
	if r4.Status != Passed || r4.ExtraStaleCases != 1 || len(r4.PassedStored) != 1 || r4.PassedStored[0] != 0 {
		t.Fatalf("expected pass with 1 stale extra case & PassedStored=[0], got %+v", r4)
	}
	line4, ok := VerdictLine(r4, "sample.ergo")
	if !ok || !strings.Contains(line4, "extra case(s): digest changed") {
		t.Fatalf("expected 'extra case(s): digest changed' in passing verdict line, got ok=%v line=%q", ok, line4)
	}
}
