package stdlib_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

func collectPureStdLaws(t *testing.T, pkgName string) map[string]lawledger.Law {
	t.Helper()
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import(pkgName)
	if err != nil {
		t.Fatalf("import %s: %v", pkgName, err)
	}
	b := eval.DefaultBudget()
	b.Steps = 50_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate %s: %v", pkgName, err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results[pkgName+" "+strings.TrimPrefix(r.Name, "law ")] = r
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
			case r.Status != eval.Passed || (r.Evidence != "tested" && r.Evidence != "proved"):
				t.Errorf("law %s: %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	return laws
}

// TestStdContentLawsRun runs std content's laws at check time and holds that
// every content row in runners.txt passes.
func TestStdContentLawsRun(t *testing.T) {
	lawledger.Run(t, collectPureStdLaws(t, "content"))
}

// TestStdRegexLawsRun runs std regex's laws at check time and holds that
// every regex row in runners.txt passes.
func TestStdRegexLawsRun(t *testing.T) {
	lawledger.Run(t, collectPureStdLaws(t, "regex"))
}
