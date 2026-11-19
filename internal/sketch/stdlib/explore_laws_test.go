package stdlib_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// exploreLawPackages are the std packages whose ledger rows name
// TestStdExploreLawsRun: std explore and its visited set, dense.Keys.
var exploreLawPackages = []string{"explore", "dense"}

// TestStdExploreLawsRun runs the laws of std explore (AlwaysAgreesWithReference,
// the search against a reference that keeps whole states; KeyIsEquality, a
// state's key against ==; Conform's ProjectionsConform,
// ConformAgreesWithReference, TraceErrorsNameTheLine and
// LimitNeverConforms) and dense.KeysAgreeWithAList at check time, in
// process: the evaluator runs std explore's Ergo, which is the code a test
// build lowers, so no Go is built. Each law runs its 100 generated cases
// with a step budget large enough to finish them; lawledger.Run holds that
// the ledger's rows that name this runner are exactly these laws, and each
// passed. The mc-* mutants of std explore's ledger name it.
func TestStdExploreLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]eval.Result{}
	for _, path := range exploreLawPackages {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		b := eval.DefaultBudget()
		b.Steps = 50_000_000
		b.Bytes = 1 << 30
		rep, err := eval.Run(pkg, b)
		if err != nil {
			t.Fatalf("evaluate %s: %v", path, err)
		}
		for _, r := range rep.Results {
			results[path+" "+strings.TrimPrefix(r.Name, "law ")] = r
		}
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		laws[row.Key()] = lawledger.Law{Statement: row.Statement, Check: func(t *testing.T) {
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
