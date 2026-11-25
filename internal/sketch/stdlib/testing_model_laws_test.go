package stdlib_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdTestingModelLawsRun runs the stateful model-testing laws of std
// testing at check time, in process: the evaluator runs std testing's Ergo
// directly, and lawledger.Run holds that every testing-model row of
// runners.txt is checked and passes.
func TestStdTestingModelLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("testing")
	if err != nil {
		t.Fatalf("import testing: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate testing: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["testing "+strings.TrimPrefix(r.Name, "law ")] = r
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
