package lower

import (
	"fmt"
	"os"
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
// Mutants: native-case-stream-wrong; eval's reseed-per-case and provider-case-reused.
func TestCasePlansAgreeWithGo(t *testing.T) {
	needsBuild(t)
	var builds []*lowered
	seeds := map[string]uint64{}
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
		seeds[name], seeds[name+"_ref"] = seed, seed
		builds = append(builds, lowerTestBuild(t, name, p.Source))
		restore := referenceLowerings()
		ref, err := lowerTestBuildErr(name+"_ref", p.Source)
		restore()
		if err != nil {
			t.Fatal(err)
		}
		builds = append(builds, ref)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, prog := range builds {
		seed := seeds[prog.name]
		b := eval.DefaultBudget()
		b.Seed = seed
		report, err := eval.Run(prog.pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runExeArgs(t, exes[prog.name], "--seed", strconv.FormatUint(seed, 10))
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d (%s): %s\n%s\n%s\n%s\nreplay: ERGO_CASE_SEED=%d go test -run '^TestCasePlansAgreeWithGo$' ./internal/sketch/lower", seed, prog.name, fmt.Sprintf(format, args...), stdout, stderr, prog.src, seed)
		}
		if code != 1 {
			fail("native exit %d", code)
		}
		for _, r := range report.Results {
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
				var args []string
				for _, a := range r.Arguments {
					args = append(args, a.Name+" = "+a.Value)
				}
				if !strings.Contains(line, ": "+strings.Join(args, ", ")+"): ") {
					fail("arguments differ: %+v", r)
				}
			default:
				fail("unevaluated result %+v", r)
			}
		}
	}
}
