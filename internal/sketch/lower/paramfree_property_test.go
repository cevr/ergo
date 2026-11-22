package lower

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower/paramfreegen"
	"ergo.local/prototype/internal/sketch/syntax"
)

// pfSeeds is the property's seeds: 1 to 60, or ERGO_PF_SEED alone.
func pfSeeds(t *testing.T) []*paramfreegen.Seed {
	if s := os.Getenv("ERGO_PF_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ERGO_PF_SEED %q: %v", s, err)
		}
		return []*paramfreegen.Seed{paramfreegen.Generate(n)}
	}
	var seeds []*paramfreegen.Seed
	for n := 1; n <= 60; n++ {
		seeds = append(seeds, paramfreegen.Generate(n))
	}
	return seeds
}

// TestParamFreeArmsVerdict is the param-free arm property's checker half
// (paramfreegen; GEN9-GAP42 D): for each seed, alone, the checker reports
// S108 Gap 42 iff the model refuses, and nothing else. A failing seed
// prints the source and the replay command (ERGO_PF_SEED=n).
//
// Mutants only it kills (check mutants.json): gap42-tpart-variant-free (a
// variant holding T, through a field or an Option, counted T-free) and
// gap42-mixed-union-drop-admitted (a union that also holds a concrete
// resource dropped whole in a generic body, its T part leaked).
func TestParamFreeArmsVerdict(t *testing.T) {
	reached := map[bool]int{}
	seeds := pfSeeds(t)
	for _, s := range seeds {
		src := paramfreegen.Source([]*paramfreegen.Seed{s}, false)
		replay := fmt.Sprintf("replay: ERGO_PF_SEED=%d go test -run '^TestParamFreeArms' ./internal/sketch/lower", s.N)
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", s.N, err, numbered(src))
		}
		_, diags := check.Check("main", []*syntax.File{f}, nil)
		var gap42, other []string
		for _, d := range diags {
			if d.Code == check.CodeType && strings.Contains(d.Message, "Gap 42") {
				gap42 = append(gap42, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Message))
				continue
			}
			other = append(other, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
		}
		reached[s.Admitted]++
		if len(other) > 0 {
			t.Fatalf("seed %d: diagnostics outside the property: %v\n%s\n%s", s.N, other, numbered(src), replay)
		}
		if got := len(gap42) == 0; got != s.Admitted {
			t.Errorf("seed %d: the checker admits=%v, the model admits=%v (drops %v): %v\n%s\n%s", s.N, got, s.Admitted, s.Drop, gap42, numbered(src), replay)
		}
	}
	if len(seeds) > 1 && (reached[true] == 0 || reached[false] == 0) {
		t.Errorf("the seeds reach admitted=%d refused=%d: both verdicts must be reached", reached[true], reached[false])
	}
}

// TestParamFreeArmsRun is the property's run half: the admitted seeds, in
// one program, whose tests assert the model's journal for pick and for
// pickRes, pass in the text lowering's test build, and pick's pass at
// check time (the evaluator). The IR build's leg is cmd/ergo's
// TestParamFreeArmsNative.
//
// It builds Go because its subject is the release the lowering emits on
// the dropped arm, generic and concrete: go/types says only that the
// lowered Go compiles, not which Close it runs.
//
// Mutant only it kills (check mutants.json): gap42-concrete-release-skipped
// (a T-free arm's concrete resource not released).
func TestParamFreeArmsRun(t *testing.T) {
	needsBuild(t)
	var admitted []*paramfreegen.Seed
	var names []string
	for _, s := range pfSeeds(t) {
		if s.Admitted {
			admitted = append(admitted, s)
			names = append(names, s.TestNames...)
		}
	}
	if len(admitted) == 0 {
		t.Fatal("no admitted seed")
	}
	src := paramfreegen.Source(admitted, true)
	rep, err := evaluate(src)
	if err != nil {
		t.Fatalf("%v\n%s", err, numbered(src))
	}
	for _, res := range rep.Results {
		// The evaluator releases a pattern binding that a function's
		// tail match arm hands into its returned value (the concrete
		// twin's arm), a bug main has without generics: unit
		// EVAL-RETURNED-TEMPS, repro
		// /home/exedev/.cache/baton/wf10/lang/EVAL-RETURNED-TEMPS/repro-tail-arm.
		// Until it lands the evaluator leg reads the generic pick's
		// tests only; the text and IR legs read every test.
		if strings.HasSuffix(res.Name, "_pickRes") {
			continue
		}
		if string(res.Status) != "passed" {
			t.Errorf("the evaluator: %s %s %s", res.Name, res.Status, res.Reason)
		}
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "pf_text", src)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["pf_text"])
	for _, name := range names {
		if !strings.Contains(stdout, "ok "+name+"\n") {
			t.Errorf("the text lowering: test %s did not pass (exit %d)\n%s\n%s", name, code, stdout, firstLines(stderr, 8))
		}
	}
	if t.Failed() {
		t.Logf("source:\n%s", numbered(src))
	}
}
