package stdlib_test

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// processLaws are the ledger keys TestStdProcessLaws runs: the Runner
// laws of std process (stdlib.md, Process), for its provider Local.
var processLaws = map[string][]string{
	"process": {"Runner.ValueOrFailurePasses", "Runner.DefectIsCrashed", "Runner.TimeIsExpired",
		"Runner.StepsIsExceeded", "Runner.CleanupRunsBeforeReturn", "Runner.ModelAgrees"},
}

// TestStdProcessLaws runs the laws of processLaws in process, at check
// time: it checks each package's own files as the root package, so a law
// reads its unexported helpers, and evaluates them, as ergo check does.
// A member law of Runner runs for each provider in its package (Local).
// lawledger.Run holds that the ledger's rows are these laws, each with
// the source's statement, and that each passed every case it ran.
func TestStdProcessLaws(t *testing.T) {
	statements := sourceLaws(t, stdLawPackages(t))
	laws := map[string]lawledger.Law{}
	for pkg, names := range processLaws {
		results := evaluatedLaws(t, pkg)
		for _, name := range names {
			key := pkg + " " + name
			statement, ok := statements[key]
			if !ok {
				t.Errorf("no std law %s", key)
				continue
			}
			laws[key] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
				ran := 0
				for _, r := range results {
					if r.Name != "law "+name && !strings.HasPrefix(r.Name, "law "+name+" for ") {
						continue
					}
					ran++
					if r.Status != eval.Passed || r.Cases == 0 {
						t.Errorf("%s: %s after %d cases: %s", r.Name, r.Status, r.Cases, r.Reason)
					}
				}
				if ran == 0 {
					t.Fatalf("the evaluator ran no law %s", name)
				}
			}}
		}
	}
	lawledger.Run(t, laws)
}

type processTrust struct{}

func (processTrust) Import(path string) (*check.Package, error) { return check.Std.Import(path) }
func (processTrust) TrustsGo() (string, bool)                   { return "process", true }

// evaluatedLaws checks the std package pkg's files as a trusted package
// and evaluates its tests and laws.
func evaluatedLaws(t *testing.T, pkg string) []eval.Result {
	t.Helper()
	sources, ok := stdlib.Package(pkg)
	if !ok {
		t.Fatalf("no std package %s", pkg)
	}
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	var files []*syntax.File
	for _, name := range names {
		f, err := syntax.Parse(sources[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files = append(files, f)
	}
	p, diags := check.Check(pkg, files, processTrust{})
	if len(diags) > 0 {
		t.Fatalf("check %s: %v", pkg, diags)
	}
	rep, err := eval.Run(p, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	return rep.Results
}
