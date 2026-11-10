package lower

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/evalgen"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEvalAgreesWithGo is L7's native agreement (plan section 1,
// TestEvalAgreesWithGo): each seed's evalgen program, whose test asserts
// that r() equals the reference model's value, is lowered twice, by the
// default lowering and by the reference lowerings (the copying value
// rule, flat matches, reference captures, switch catches and Option
// matches, interface unions, the plain Update call), built and run; and
// it is evaluated at check time. The three verdicts must be the model's:
// the test passes, or r() hits a defect. The two native runs must print
// the same lines. A failing seed prints the source, every verdict (a
// native failure shows r()'s Go value beside the model's) and the replay
// command.
//
// It builds Go because the oracle is the Go target itself: go/types says
// only that the lowered Go compiles, not what its arithmetic, copies,
// matches and defects compute, and the evaluator and the model are the
// two things under test. Each program runs under the kit's memory limit
// (runProgram).
//
// Mutant it kills (testdata/mutants.json): eval-model-shared-drift, a
// paired edit of the model and the evaluator that keeps them agreeing
// (TestEvalAgreesWithModel passes) while both depart from the Go target:
// both read an int8 conversion without its wrap.
func TestEvalAgreesWithGo(t *testing.T) {
	needsBuild(t)
	seeds := make([]uint64, 0, 40)
	for s := uint64(1); s <= 400; s++ {
		seeds = append(seeds, s)
	}
	if s := os.Getenv("ERGO_EVAL_GO_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{v}
	}
	programs := map[uint64]evalgen.Program{}
	var builds []*lowered
	for _, seed := range seeds {
		p := evalgen.Generate(seed)
		programs[seed] = p
		builds = append(builds, lowerTestBuild(t, fmt.Sprintf("evalgo_%d", seed), p.Source))
		restore := referenceLowerings()
		ref, err := lowerTestBuildErr(fmt.Sprintf("evalgo_%d_ref", seed), p.Source)
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
	defects := 0
	for _, seed := range seeds {
		p := programs[seed]
		want := "passed"
		if p.Defect {
			want, defects = "defect", defects+1
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nmodel: %s %s\n%s\nreplay: ERGO_EVAL_GO_SEED=%d go test -run TestEvalAgreesWithGo ./internal/sketch/lower", seed, fmt.Sprintf(format, args...), want, p.Want, p.Source, seed)
		}
		stdout, stderr, _ := runExeArgs(t, exes[fmt.Sprintf("evalgo_%d", seed)])
		refOut, _, _ := runExeArgs(t, exes[fmt.Sprintf("evalgo_%d_ref", seed)])
		if stdout != refOut {
			fail("the default lowering prints\n%s\nthe reference lowerings\n%s", stdout, refOut)
		}
		if got := nativeVerdict(stdout); got != want {
			fail("Go says %s:\n%s\n%s", got, stdout, firstLines(stderr, 6))
		}
		rep, err := evaluate(p.Source)
		if err != nil {
			fail("%v", err)
		}
		if len(rep.Results) != 1 || string(rep.Results[0].Status) != want {
			fail("the evaluator says %+v", rep.Results)
		}
	}
	if len(seeds) > 1 && (defects == 0 || defects == len(seeds)) {
		t.Errorf("%d of %d seeds defect: the verdicts are not both reached", defects, len(seeds))
	}
}

// nativeVerdict is the test binary's verdict of its test V.
func nativeVerdict(stdout string) string {
	for _, l := range lines(stdout) {
		switch {
		case l == "ok V" || strings.HasPrefix(l, "ok V "):
			return "passed"
		case strings.HasPrefix(l, "FAIL V: a defect"):
			return "defect"
		case strings.HasPrefix(l, "FAIL V:"):
			return "refuted"
		}
	}
	return "no verdict"
}

// evaluate checks src and evaluates it at check time.
func evaluate(src string) (eval.Report, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return eval.Report{}, err
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		return eval.Report{}, fmt.Errorf("check: %v", diags)
	}
	return eval.Run(pkg, eval.DefaultBudget())
}

// referenceLowerings selects every reference lowering the evalgen forms
// reach, until restore runs: the value rule without owned parameters, the
// flat match, Go's capture by reference, the switch catch, the Option's
// type switch, interface unions and the plain Update call.
func referenceLowerings() (restore func()) {
	saved := []string{valueLowering, matchLowering, captureLowering, getLowering, unionLowering, updateLowering}
	valueLowering, matchLowering, captureLowering = "copy", "flat", "reference"
	getLowering, unionLowering, updateLowering = "switch", "interface", "call"
	catch := SetCatchSwitchForTest(true)
	return func() {
		catch()
		valueLowering, matchLowering, captureLowering = saved[0], saved[1], saved[2]
		getLowering, unionLowering, updateLowering = saved[3], saved[4], saved[5]
	}
}
