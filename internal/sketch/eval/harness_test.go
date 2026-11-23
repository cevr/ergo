package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// coinLib is a user library whose capability Coin declares the harness
// Toss (check harness.go): Toss runs the body and names the run in the
// failure, so a report shows the name, the seed and the site the runner
// gave it.
const coinLib = `type Coin {
	Flip() bool,
	test Toss,
}

type Run { Name string, Seed uint64, Site string, Source string }

type Failed { Message string }

type always {} implements Coin

func Flip(a always) bool { true }

func Heads() bool needs Coin { Coin.Flip() }

func Toss(run Run, body func() fails Failed needs Coin) fails Failed {
	with context { Coin: always } { body() } catch {
	case Failed as f:
		return Failed{Message: run.Name + " seed \(run.Seed) at " + run.Site + " of " + run.Source + ": " + f.Message}
	}
}
`

type coinImporter struct{ lib *check.Package }

func (i coinImporter) Import(path string) (*check.Package, error) {
	if path == "lib" {
		return i.lib, nil
	}
	return check.Std.Import(path)
}

// TestHarnessRunsTheBody: the evaluator runs a harnessed test through its
// harness: a body that holds passes, and a failed assert fails the test
// with the harness's failure as its whole report, which holds the run's
// name, its --seed, the test's site and the source ergo test was given
// (Budget.Source), and the assert's site and text; a failed comparison
// shows its operands as the runner's assert does (P1c part 1).
func TestHarnessRunsTheBody(t *testing.T) {
	lf, err := syntax.Parse(coinLib)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{lf}, nil)
	if len(ds) != 0 {
		t.Fatalf("lib: %v", ds)
	}
	f, err := syntax.Parse("import lib\n\ntest Holds {\n\tassert lib.Heads()\n}\n\ntest Fails {\n\tlet h = lib.Heads()\n\tassert !h\n}\n\ntest Shows {\n\tlet n = if lib.Heads() { 2 } else { 0 }\n\tassert n == 3\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "coin_test.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, coinImporter{lib})
	if len(ds) != 0 {
		t.Fatalf("main: %v", ds)
	}
	b := DefaultBudget()
	b.Seed, b.Source = 7, "pkg/coin"
	rep, err := Run(pkg, b)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Result{}
	for _, r := range rep.Results {
		got[r.Name] = r
	}
	if r := got["Holds"]; r.Status != Passed {
		t.Errorf("Holds: %s %s", r.Status, r.Reason)
	}
	r := got["Fails"]
	want := "Fails seed 7 at coin_test.ergo:7 of pkg/coin: assert failed at coin_test.ergo:9:2: !h"
	if r.Status != Refuted || r.Message != want {
		t.Errorf("Fails: %s %q, want refuted with %q", r.Status, r.Message, want)
	}
	r = got["Shows"]
	want = "Shows seed 7 at coin_test.ergo:12 of pkg/coin: assert failed at coin_test.ergo:14:2: n == 3: left 2, right 3"
	if r.Status != Refuted || r.Message != want {
		t.Errorf("Shows: %s %q, want refuted with %q", r.Status, r.Message, want)
	}
}
