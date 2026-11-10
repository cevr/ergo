package eval

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval/casegen"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/runtime/testgen"
)

// TestCasePlansRun guards the generated-obligation seam that the scalar and
// collection models do not exercise: a property and law must run their first
// refuting case, not be silently left as generated-case support skips.
// Mutants: reseed-per-case, provider-case-reused, finite-cap-overflow,
// imported-provider-wrong (the native agreement property also guards arguments).
func TestCasePlansRun(t *testing.T) {
	first, last := uint64(1), uint64(12)
	if s := os.Getenv("ERGO_CASE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, last = n, n
	}
	for offset := uint64(0); offset <= last-first; offset++ {
		seed := first + offset
		p := casegen.Generate(seed)
		src := p.Source
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		pkg, ds := check.Check("main", []*syntax.File{f}, nil)
		if len(ds) != 0 {
			t.Fatal(ds)
		}
		b := DefaultBudget()
		b.Seed = seed
		r, err := Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, result := range r.Results {
			seen[result.Name] = true
			if result.Name == "law Skips" {
				if result.Status != Skipped || result.Reason == "" {
					t.Fatalf("skip: %+v", result)
				}
				continue
			}
			if result.Name == "Nested" {
				if result.Status != Refuted || result.Case < 4 {
					t.Fatalf("nested generator: %+v", result)
				}
				continue
			}
			if k := p.First[result.Name]; k > 0 {
				if result.Status != Refuted || result.Case != k || result.Cases != k || len(result.Arguments) == 0 {
					t.Fatalf("seed %d: model first case %d; evaluator %+v\n%s\nreplay: ERGO_CASE_SEED=%d go test -run '^TestCasePlansRun$' ./internal/sketch/eval", seed, k, result, src, seed)
				}
			} else if result.Status != Passed {
				t.Fatalf("seed %d: %+v\n%s", seed, result, src)
			}
			if result.Name == "law Domain" {
				evidence, cases := "proved", 256
				if seed%2 != 0 {
					evidence, cases = "tested", 100
				}
				if result.Evidence != evidence || result.Cases != cases {
					t.Fatalf("seed %d: finite cap: %+v", seed, result)
				}
			}
		}
		for name := range p.First {
			if !seen[name] {
				t.Fatalf("missing %s in %+v", name, r.Results)
			}
		}
	}
}

type caseImports map[string]*check.Package

func (m caseImports) Import(path string) (*check.Package, error) {
	if p := m[path]; p != nil {
		return p, nil
	}
	return check.Std.Import(path)
}

// TestCaseProviderIdentity guards the least-owner cross-package binding. Neither
// provider package sees the law; the root is the first package that sees both.
// Mutant imported-provider-wrong dispatches the binding to the other provider's
// method. Local-only laws cannot observe that package identity mistake.
func TestCaseProviderIdentity(t *testing.T) {
	for seed := 0; seed < 8; seed++ {
		imp := caseImports{}
		makePkg := func(path, src string) *check.Package {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			p, ds := check.Check(path, []*syntax.File{f}, imp)
			if len(ds) > 0 {
				t.Fatal(ds)
			}
			imp[path] = p
			return p
		}
		makePkg("contract", `type Reader { Read() int, law Holds() { Read() == 0 } }`)
		makePkg("a", `type P {}
func Read(p P) int { 0 }`)
		makePkg("b", `type P {}
func Read(p P) int { 1 }`)
		src := fmt.Sprintf(`import contract
import a
import b
func useA() contract.Reader { a.P }
func useB() contract.Reader { b.P }
law Own() { %d >= 0 }
`, seed)
		root := makePkg("main", src)
		r, err := Run(root, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]Status{}
		for _, x := range r.Results {
			seen[x.Name] = x.Status
		}
		if seen["law contract.Reader.Holds for a.P"] != Passed || seen["law contract.Reader.Holds for b.P"] != Refuted {
			t.Fatalf("seed %d: bindings %+v", seed, r.Results)
		}
	}
}

// TestCaseBudgetStopsGeneration guards generator-fuel-ignored. A public run's
// total work must at least pay the independent stream's draw/allocation charge;
// the scalar/copy budget properties do not draw strings. No exact interpreter
// instruction count or wall-clock bound is pinned.
func TestCaseBudgetStopsGeneration(t *testing.T) {
	pkg := checked(t, `test Words(s string) { assert true }`)
	for seed := uint64(1); seed <= 12; seed++ {
		r := testgen.New(seed, "Words")
		var streamWork int64
		r.Charge = func(n int64) { streamWork += n }
		for k := 1; k <= 100; k++ {
			r.Start(k)
			_ = r.Str()
		}
		b := DefaultBudget()
		b.Seed = seed
		report, err := Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		if report.Results[0].Status != Passed || report.Steps < streamWork {
			t.Fatalf("seed %d: stream work %d, report %+v", seed, streamWork, report)
		}
		b.Steps = streamWork / 2
		report, err = Run(pkg, b)
		if err != nil || report.Results[0].Status != Incomplete || report.Steps > b.Steps {
			t.Fatalf("seed %d: bounded generation %+v %v", seed, report, err)
		}
	}
}
