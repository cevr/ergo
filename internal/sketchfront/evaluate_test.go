package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestEvaluateRefutesFalseExample is the check-time seam's acceptance (L7
// EV1; the planner's evidence/refuted.ergo): a statically valid program
// whose example is false must be refuted by Analysis.Evaluate, at the
// assert's site, with no Go build. The law beside it is refuted by its
// generated cases (EV3 draws them; this row said skipped before). Mutants eval-bypassed (Evaluate reports without running
// the obligations) and skip-as-pass are caught here and nowhere else.
func TestEvaluateRefutesFalseExample(t *testing.T) {
	src := "func twice(n int) int { n + n }\n\ntest ExampleRefuted {\n\tassert twice(2) == 5\n}\n\nlaw Refuted(n int) { twice(n) - n != n }\n"
	a := Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatalf("the program does not check: %v", a.Diagnostics)
	}
	rep, err := a.Evaluate(eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 obligations, got %+v", rep.Results)
	}
	ex := rep.Results[0]
	if ex.Name != "ExampleRefuted" || ex.Status != eval.Refuted || ex.Site.Line != 4 || ex.Site.Column != 2 {
		t.Errorf("example: want refuted at 4:2, got %+v", ex)
	}
	law := rep.Results[1]
	if law.Name != "law Refuted" || law.Status != eval.Refuted {
		t.Errorf("law: want it refuted by a generated case, got %+v", law)
	}
	if !rep.Refuted() {
		t.Errorf("the report does not count the refutation: %+v", rep)
	}
}

// runnerKind is what one generated obligation does after its work, a
// string of n bytes built one byte at a time (pad).
type runnerKind int

const (
	kindPass runnerKind = iota
	kindAssert
	kindFail
	kindDefect
	kindSkip
	kindLawHolds
	kindLawFalse
)

type runnerObligation struct {
	name string
	kind runnerKind
	work int // pad's n
}

// runnerProgram is a generated program: an initializer's work, tests in
// order, then laws, and maybe a static error.
type runnerProgram struct {
	init      int
	tests     []runnerObligation
	laws      []runnerObligation
	staticBad bool
}

func genRunnerProgram(r *rand.Rand) runnerProgram {
	p := runnerProgram{init: r.IntN(12), staticBad: r.IntN(10) == 0}
	for i := range 1 + r.IntN(6) {
		p.tests = append(p.tests, runnerObligation{name: fmt.Sprintf("T%d", i), kind: runnerKind(r.IntN(5)), work: r.IntN(20)})
	}
	for i := range r.IntN(3) {
		p.laws = append(p.laws, runnerObligation{name: fmt.Sprintf("law L%d", i), kind: kindLawHolds + runnerKind(r.IntN(2)), work: r.IntN(8)})
	}
	return p
}

func (p runnerProgram) source() string {
	var b strings.Builder
	b.WriteString("extern go strings.ToUpper func upper(s string) string\n\n")
	b.WriteString("type Bad { Code int }\n\n")
	b.WriteString("func boom(n int) int fails Bad {\n\tif n > 0 {\n\t\treturn Bad{Code: n}\n\t}\n\tn\n}\n\n")
	b.WriteString("func pad(n int) string {\n\tlet mut s = \"\"\n\tlet mut i = 0\n\tfor i < n {\n\t\ts = s + \"x\"\n\t\ti = i + 1\n\t}\n\ts\n}\n\n")
	b.WriteString("func half(x int) int { x / x }\n\n")
	// An unused declaration that calls Go: it never makes a pure
	// obligation a skip.
	b.WriteString("func shout() string { upper(\"q\") }\n\n")
	fmt.Fprintf(&b, "let base = pad(%d)\n\n", p.init)
	if p.staticBad {
		b.WriteString("func wrong() int { \"s\" }\n\n")
	}
	for _, o := range p.tests {
		fmt.Fprintf(&b, "test %s {\n\tlet w = pad(%d)\n", o.name, o.work)
		switch o.kind {
		case kindPass:
			b.WriteString("\tassert w == w\n")
		case kindAssert:
			b.WriteString("\tassert w != w\n")
		case kindFail:
			b.WriteString("\tlet x = try boom(1)\n\tassert x == 0 && w == w\n")
		case kindDefect:
			b.WriteString("\tassert half(0) == 1 && w == w\n")
		case kindSkip:
			b.WriteString("\tassert upper(w) == w\n")
		}
		b.WriteString("}\n\n")
	}
	for _, o := range p.laws {
		op := "=="
		if o.kind == kindLawFalse {
			op = "!="
		}
		fmt.Fprintf(&b, "law %s(n int) { pad(%d) %s pad(%d) }\n\n", strings.TrimPrefix(o.name, "law "), o.work, op, o.work)
	}
	return b.String()
}

// model is the runner's reference (plan section 1, TestCheckEvalRunnerModel):
// each obligation's status from what it does, a defect stopping the run;
// and a lower bound of the value bytes the run must allocate (pad(n)
// builds strings of 1, 2, ... n bytes).
func (p runnerProgram) model() (names, statuses []string, minBytes int64) {
	padBytes := func(n int) int64 { return int64(n) * int64(n+1) / 2 }
	minBytes = padBytes(p.init)
	stopped := false
	add := func(o runnerObligation, status string) {
		names = append(names, o.name)
		if stopped {
			statuses = append(statuses, "not run")
			return
		}
		statuses = append(statuses, status)
		if status != "skipped" {
			minBytes += padBytes(o.work)
		}
		if status == "defect" {
			stopped = true
		}
	}
	for _, o := range p.tests {
		add(o, map[runnerKind]string{kindPass: "passed", kindAssert: "refuted", kindFail: "refuted", kindDefect: "defect", kindSkip: "skipped"}[o.kind])
	}
	for _, o := range p.laws {
		add(o, map[runnerKind]string{kindLawHolds: "passed", kindLawFalse: "refuted"}[o.kind])
	}
	return names, statuses, minBytes
}

// modelSummary is the evaluation's status from its obligations', worst
// first, written apart from report.EvaluationStatus.
func modelSummary(statuses []string) string {
	switch {
	case slices.Contains(statuses, "defect"):
		return "defect"
	case slices.Contains(statuses, "refuted"):
		return "refuted"
	case slices.Contains(statuses, "incomplete"), slices.Contains(statuses, "not run"):
		return "incomplete"
	case slices.Contains(statuses, "skipped"):
		return "skipped"
	case len(statuses) > 0:
		return "passed"
	}
	return "none"
}

// TestCheckEvalRunnerModel drives the check-time runner (Analysis.Evaluation,
// what ergo check reports) over generated programs against the reference
// model above (plan section 1): a static error runs nothing; a false
// assert or an escaping typed failure refutes its obligation and the run
// goes on; a defect stops the run; a skip is never passed; every
// obligation has one record, in ergo test's order. It then cuts each
// program's run short at a random point of either budget, the steps or the
// value bytes, which bound the initializer and each obligation apart: an
// initializer that needs more stops every obligation (incomplete, then not
// run); otherwise each obligation that needs more is incomplete, each
// other one keeps the full run's record, and the summary is never passed.
// An obligation below the model's least byte count must stop. Mutants: eval-bytes-unmetered (allocations never charge the
// byte budget), report-incomplete-as-passed (an incomplete record sums
// up as passed). A failing seed prints its source, both outputs and
// ERGO_EVAL_RUNNER_SEED=<seed> to replay it.
func TestCheckEvalRunnerModel(t *testing.T) {
	seeds := make([]uint64, 0, 60)
	if s := os.Getenv("ERGO_EVAL_RUNNER_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(60) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x5eed))
		p := genRunnerProgram(r)
		src := p.source()
		names, want, minBytes := p.model()
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed %d: %s\nsource:\n%s\nreplay: ERGO_EVAL_RUNNER_SEED=%d go test -run TestCheckEvalRunnerModel ./internal/sketchfront", seed, fmt.Sprintf(format, args...), src, seed)
		}
		a := Check(src)
		if p.staticBad {
			if len(a.Diagnostics) == 0 {
				fail("the static error has no diagnostic")
			}
			if _, err := a.Evaluation(eval.DefaultBudget(), "p.ergo"); err == nil {
				fail("a source with a static error was evaluated")
			}
			continue
		}
		if len(a.Diagnostics) != 0 {
			fail("the program does not check: %v", a.Diagnostics)
		}
		full, err := a.Evaluation(eval.DefaultBudget(), "p.ergo")
		if err != nil {
			fail("%v", err)
		}
		got, gotNames := statusesOf(full)
		if !slices.Equal(gotNames, names) || !slices.Equal(got, want) || full.Status != modelSummary(want) {
			fail("full run: model %v %v (%s), runner %v %v (%s)", names, want, modelSummary(want), gotNames, got, full.Status)
		}
		if full.Bytes < minBytes {
			fail("the run allocated %d bytes, the model at least %d", full.Bytes, minBytes)
		}
		for _, o := range full.Obligations {
			if (o.Status == "passed") != (o.Replay == "") {
				fail("%s (%s) has replay %q", o.Name, o.Status, o.Replay)
			}
		}

		// A cut of either budget, which bounds the initializer and each
		// obligation apart: an obligation that needs more is incomplete,
		// the others keep the full run's records. need is each record's
		// least need of the cut axis, -1 when it does not run; init is
		// the initializer's.
		padBytes := func(n int) int64 { return int64(n) * int64(n+1) / 2 }
		b := eval.DefaultBudget()
		axis := "steps"
		var need []int64
		init := full.Steps
		for _, o := range full.Obligations {
			init -= o.Steps
		}
		if r.IntN(2) == 0 {
			axis, init = "bytes", padBytes(p.init)
			for _, o := range append(append([]runnerObligation(nil), p.tests...), p.laws...) {
				need = append(need, padBytes(o.work))
			}
		} else {
			for _, o := range full.Obligations {
				need = append(need, o.Steps)
			}
		}
		most := init
		for i := range need {
			if want[i] == "not run" || want[i] == "skipped" {
				need[i] = -1
			}
			most = max(most, need[i])
		}
		if most <= 1 {
			continue
		}
		limit := 1 + r.Int64N(most-1)
		if axis == "bytes" {
			b.Bytes = limit
		} else {
			b.Steps = limit
		}
		cut, err := a.Evaluation(b, "p.ergo")
		if err != nil {
			fail("%v", err)
		}
		cs, _ := statusesOf(cut)
		ok := len(cs) == len(want) && slices.Contains(cs, "incomplete")
		// The initializer did not finish: the first record is incomplete
		// and the rest not run. It must not when it needs more; pad's
		// bytes are a least count, so a bytes cut may stop it anyway.
		initStopped := ok
		for i, s := range cs {
			initStopped = initStopped && s == map[bool]string{true: "incomplete", false: "not run"}[i == 0]
		}
		if init > limit {
			ok = ok && initStopped
		}
		for i := 0; ok && init <= limit && !(axis == "bytes" && initStopped) && i < len(cs); i++ {
			switch {
			case need[i] > limit:
				ok = cs[i] == "incomplete"
			case cs[i] == "incomplete" && axis == "bytes":
				// pad's bytes are a least count: the cut may stop it.
			default:
				ok = cs[i] == want[i]
			}
			if ok && cs[i] != want[i] && want[i] == "defect" {
				break // a stopped defect lets the records after it run
			}
		}
		if !ok || cut.Status != modelSummary(cs) || cut.Status == "passed" {
			fail("a %s cut (%+v): runner %v (%s), want the full run's %v with an incomplete record for each obligation that needs more than %d", axis, b, cs, cut.Status, want, limit)
		}
		for _, o := range cut.Obligations {
			if o.Steps > cut.StepsLimit {
				fail("a %s cut: %s used %d of %d steps", axis, o.Name, o.Steps, cut.StepsLimit)
			}
		}
	}
}

func statusesOf(e report.Evaluation) (statuses, names []string) {
	for _, o := range e.Obligations {
		statuses = append(statuses, o.Status)
		names = append(names, o.Name)
	}
	return statuses, names
}
