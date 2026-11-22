package eval

import (
	"strings"
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
}

type Run { Name string, Seed uint64, Site string }

type Failed { Message string }

type always {} implements Coin

func Flip(a always) bool { true }

func Heads() bool needs Coin { Coin.Flip() }

func Toss(run Run, body func() fails Failed needs Coin) fails Failed {
	with context { Coin: always } { body() } catch {
	case Failed as f:
		return Failed{Message: run.Name + " seed \(run.Seed) at " + run.Site + ": " + f.Message}
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
// with the harness's failure, which holds the run's name, its --seed and
// the test's site, and the assert's site and text.
func TestHarnessRunsTheBody(t *testing.T) {
	defer check.SetTestHarnesses(map[string]string{"lib.Coin": "Toss"})()
	lf, err := syntax.Parse(coinLib)
	if err != nil {
		t.Fatal(err)
	}
	lib, ds := check.Check("lib", []*syntax.File{lf}, nil)
	if len(ds) != 0 {
		t.Fatalf("lib: %v", ds)
	}
	f, err := syntax.Parse("import lib\n\ntest Holds {\n\tassert lib.Heads()\n}\n\ntest Fails {\n\tlet h = lib.Heads()\n\tassert !h\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "coin_test.ergo"
	pkg, ds := check.Check("main", []*syntax.File{f}, coinImporter{lib})
	if len(ds) != 0 {
		t.Fatalf("main: %v", ds)
	}
	b := DefaultBudget()
	b.Seed = 7
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
	want := "Fails seed 7 at coin_test.ergo:7: assert failed at coin_test.ergo:9:2: !h"
	if r.Status != Refuted || !strings.Contains(r.Reason, want) {
		t.Errorf("Fails: %s %q, want refuted with %q", r.Status, r.Reason, want)
	}
}
