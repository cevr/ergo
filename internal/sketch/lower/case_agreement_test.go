package lower

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/casegen"
)

// TestCasePlansAgreeWithGo compares actual native arguments, first refutations,
// sample order, constructors and evidence with the check-time report. The model
// property in eval checks the sequential case rule; this independent execution
// catches drift in either typed value-construction adapter.
// It builds Go because only executing the generated cases observes their actual
// values and verdicts; go/types and a Go-text golden cannot establish parity.
// The one-binary helper and runProgram enforce child memory and group bounds.
// casegen's imported program (casegen.GenerateImported) is built once per
// lowering and runs at every seed: its cases show a constructor and a
// builder of another package, imported under an alias, so the qualifier
// both runners print is the one the source spells, and its laws must be
// refuted at their first case, so the arguments are compared at all.
// Mutants: native-case-stream-wrong, lgf-lower-noop-not-drawn,
// lgf-lower-noop-literal; eval's reseed-per-case, lgf-noop-prints-as-name,
// provider-case-reused, eval-ctor-arg-unqualified and
// eval-built-arg-unqualified.
func TestCasePlansAgreeWithGo(t *testing.T) {
	needsBuild(t)
	var builds []*lowered
	seeds := map[string][]uint64{}
	refuted := map[string]map[string]int{}
	shows := map[string]map[string][]string{}
	first, last := uint64(1), uint64(8)
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
		name := fmt.Sprintf("caseplans_%d", seed)
		seeds[name], seeds[name+"_ref"] = []uint64{seed}, []uint64{seed}
		builds = append(builds, lowerTestBuild(t, name, p.Source))
		restore := referenceLowerings()
		ref, err := lowerTestBuildErr(name+"_ref", p.Source)
		restore()
		if err != nil {
			t.Fatal(err)
		}
		builds = append(builds, ref)
	}
	imported := casegen.GenerateImported()
	lib := checkPackage(t, imported.LibPath, []pkgSource{{"/ergo-test/" + imported.LibPath + "/lib.ergo", imported.Lib}}, nil)
	root := checkPackage(t, "main", []pkgSource{{"/ergo-test/caseimport/main.ergo", imported.Source}}, pkgImporter{imported.LibPath: lib.Package})
	root.Imports = []Unit{lib}
	for _, name := range []string{"caseimport", "caseimport_ref"} {
		restore := func() {}
		if name == "caseimport_ref" {
			restore = referenceLowerings()
		}
		prog := mustLowerUnit(t, name, root, true)
		restore()
		for seed := first; seed <= last; seed++ {
			seeds[name] = append(seeds[name], seed)
		}
		refuted[name], shows[name] = imported.First, imported.Shows
		builds = append(builds, prog)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, prog := range builds {
		for _, seed := range seeds[prog.name] {
			agreeAtSeed(t, prog, exes[prog.name], seed, refuted[prog.name], shows[prog.name])
		}
	}
}

// agreeAtSeed runs prog's executable exe at seed and compares its verdicts
// and arguments with the evaluator's report; each law of first must be
// refuted at its case, its arguments holding each text shows gives it.
func agreeAtSeed(t *testing.T, prog *lowered, exe string, seed uint64, first map[string]int, shows map[string][]string) {
	t.Helper()
	b := eval.DefaultBudget()
	b.Seed = seed
	report, err := eval.Run(prog.pkg, b)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runExeArgs(t, exe, "--seed", strconv.FormatUint(seed, 10))
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d (%s): %s\n%s\n%s\n%s\nreplay: ERGO_CASE_SEED=%d go test -run '^TestCasePlansAgreeWithGo$' ./internal/sketch/lower", seed, prog.name, fmt.Sprintf(format, args...), stdout, stderr, prog.src, seed)
	}
	if code != 1 {
		fail("native exit %d", code)
	}
	for _, r := range report.Results {
		if k := first[r.Name]; k > 0 && (r.Status != eval.Refuted || r.Case != k) {
			fail("model first case %d; evaluator %+v", k, r)
		}
		switch r.Status {
		case eval.Passed:
			text := "ok " + r.Name
			if r.Evidence == "proved" {
				text += " (" + provedText(r.Cases) + ")"
			} else if r.Evidence == "tested" {
				text += fmt.Sprintf(" (%d cases)", r.Cases)
			}
			if !strings.Contains(stdout, text+"\n") {
				fail("missing %s: %+v", text, r)
			}
		case eval.Skipped:
			if !strings.Contains(stdout, "skip "+r.Name+":") {
				fail("native did not skip %+v", r)
			}
		case eval.Refuted:
			var line string
			for _, s := range lines(stdout) {
				if strings.HasPrefix(s, "FAIL "+r.Name+" (") {
					line = s
					break
				}
			}
			if !strings.Contains(line, fmt.Sprintf("(case %d of %d, ", r.Case, r.TotalCases)) {
				fail("first case differs: %+v", r)
			}
			var args, drawn []string
			for _, a := range r.Arguments {
				args = append(args, a.Name+" = "+a.Value)
			}
			for _, a := range r.Drawn {
				drawn = append(drawn, a.Name+" = "+a.Value)
			}
			// A shrunk case (Drawn set) also names the case it was drawn as, as the
			// native runner does (eval 4b2ffb035).
			want := strings.Join(args, ", ")
			if len(drawn) > 0 {
				want += "; shrunk from: " + strings.Join(drawn, ", ")
			}
			if !strings.Contains(line, ": "+want+"): ") {
				fail("arguments differ: %+v", r)
			}
			for _, text := range shows[r.Name] {
				if !strings.Contains(want, text) {
					fail("the arguments %q do not show %q as the source spells it", want, text)
				}
			}
		default:
			fail("unevaluated result %+v", r)
		}
	}
	for name := range first {
		if !slices.ContainsFunc(report.Results, func(r eval.Result) bool { return r.Name == name }) {
			fail("the evaluator ran no %s", name)
		}
	}
}
