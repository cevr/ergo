package stdlib_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdTemporalLawsRun runs the laws of std temporal at check time, in
// process: the evaluator runs std temporal's Ergo directly, and lawledger.Run
// holds that every temporal row of runners.txt is checked and passes.
func TestStdTemporalLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("temporal")
	if err != nil {
		t.Fatalf("import temporal: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate temporal: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["temporal "+strings.TrimPrefix(r.Name, "law ")] = r
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			switch {
			case !ok:
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
			case r.Status != eval.Passed || r.Evidence != "tested":
				t.Errorf("law %s: %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	lawledger.Run(t, laws)
}
