package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/gobind"
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
	src := "func Twice(n int) int { n + n }\n\ntest ExampleRefuted {\n\tassert Twice(2) == 5\n}\n\nlaw Refuted(n int) { Twice(n) - n != n }\n"
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

// TestCheckReplaysAMachineLawAsTheTestBuildDoes is the smallest repro of
// MODELCHECK T3 trial 2's two replay commands for one law: check's JSON
// said `--seed 1 --law L` where the test build prints `--law L --states
// N`. A refuted invariant names the states its search held; a goal no run
// reaches names the budget. Mutant it kills that nothing else does:
// mc-check-machine-replay-seeded.
func TestCheckReplaysAMachineLawAsTheTestBuildDoes(t *testing.T) {
	src := `type Counter { N int }

type Op = Inc{} | Reset{}

func step(s Counter, e Op) Decision[Counter, string] {
	let mut w = s
	match e {
	case Inc:
		if w.N >= 3 {
			return Ignore
		}
		w.N = w.N + 1
	case Reset:
		w.N = 0
	}
	return Enter{w}
}

func small(s Counter) bool { s.N < 2 }

func five(s Counter) bool { s.N == 5 }

type Count machine[Counter, Op] {
	initial: Counter{N: 0},
	step:    step,
	law Small always small,
	law Five sometimes five,
}
`
	a := Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatalf("the program does not check: %v", a.Diagnostics)
	}
	e, err := a.Evaluation(eval.DefaultBudget(), "m.ergo")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"law Count.Small": "", "law Count.Five": "ergo test m.ergo --law Count.Five --states 1000000"}
	for _, o := range e.Obligations {
		w, ok := want[o.Name]
		if !ok {
			continue
		}
		delete(want, o.Name)
		if o.Status != "refuted" || o.Machine == nil || o.Machine.Runs != 0 {
			t.Fatalf("%s: want a graph search's refutation, got %+v", o.Name, o)
		}
		if w == "" {
			w = "ergo test m.ergo --law Count.Small --states " + strconv.Itoa(o.Machine.States)
		}
		if o.Replay != w {
			t.Errorf("%s: replay %q, want %q", o.Name, o.Replay, w)
		}
	}
	if len(want) != 0 {
		t.Errorf("no obligation for %v in %+v", want, e.Obligations)
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
	// A Go function check time has no native for (strings.ToUpper is a
	// listed native, which runs): its test is skipped.
	b.WriteString("extern go strings.ToTitle func upper(s string) string\n\n")
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
			// The step budget is each case's (TP-BUILT-FAST): an
			// obligation of n cases needs its steps over n, rounded up.
			for _, o := range full.Obligations {
				n := int64(max(1, o.TotalCases))
				need = append(need, (o.Steps+n-1)/n)
			}
			// The initializers run under a step bound of their own
			// (runtime.md, Tests 9; LITERAL-LET), far past any cut here,
			// so a steps cut never stops them.
			init = 0
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
		// The cut shows: a record is incomplete, or a refuted one whose
		// shrink the cut stopped stays refuted (kept).
		kept := false
		ok := len(cs) == len(want)
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
			case need[i] > limit && want[i] == "refuted" && cs[i] == "refuted":
				// The cut fell in its shrink, after the drawn case
				// refuted it: a counterexample is a refutation whatever
				// the shrink's budget (EVAL-SHRINK-REFUTED, 765), so it
				// stays refuted, with its replay, and never passes.
				kept = true
				ok = cut.Obligations[i].Replay != ""
			case need[i] > limit:
				ok = cs[i] == "incomplete"
			case want[i] == "skipped" && cs[i] == "incomplete":
				// A skipped test runs its pad before it reaches the Go
				// call that skips it, and its record carries no steps:
				// the cut may stop it in the pad.
			case cs[i] == "incomplete" && axis == "bytes":
				// pad's bytes are a least count: the cut may stop it.
			default:
				ok = cs[i] == want[i]
			}
			if ok && cs[i] != want[i] && want[i] == "defect" {
				break // a stopped defect lets the records after it run
			}
		}
		ok = ok && (slices.Contains(cs, "incomplete") || kept)
		if !ok || cut.Status != modelSummary(cs) || cut.Status == "passed" {
			fail("a %s cut (%+v): runner %v (%s), want the full run's %v with an incomplete record for each obligation that needs more than %d (initializer needs %d, obligations %v)", axis, b, cs, cut.Status, want, limit, init, need)
		}
		for _, o := range cut.Obligations {
			if o.Steps > cut.StepsLimit*int64(max(1, o.TotalCases)) {
				fail("a %s cut: %s used %d of %d steps for each of %d cases", axis, o.Name, o.Steps, cut.StepsLimit, o.TotalCases)
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

func TestEvaluateConcurrentSharedPackagesNoRace(t *testing.T) {
	dir := t.TempDir()
	depDir := filepath.Join(dir, "dep")
	useDir := filepath.Join(dir, "use")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(useDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module example.com/sharedrace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	depSrc := "type Box {\n\tValue() int,\n\tlaw Positive() { Value() >= 0 },\n}\n\ntype Unit {} implements Box\n\nfunc Value(u Unit) int { 1 }\n"
	useSrc := "import example.com/sharedrace/dep\n\ntype Zero {} implements dep.Box\n\nfunc Value(z Zero) int { 0 }\n"
	if err := os.WriteFile(filepath.Join(depDir, "dep.ergo"), []byte(depSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(useDir, "use.ergo"), []byte(useSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	analyses, err := CheckPackages([]string{depDir, useDir}, gobind.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for idx, a := range analyses {
		if len(a.Diagnostics) != 0 {
			t.Fatalf("pkg %d diagnostics: %v", idx, a.Diagnostics)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for range 8 {
		for idx, a := range analyses {
			wg.Add(1)
			go func(idx int, a Analysis) {
				defer wg.Done()
				rep, err := a.Evaluate(eval.DefaultBudget())
				if err != nil {
					errs <- fmt.Sprintf("pkg %d: %v", idx, err)
					return
				}
				if idx == 1 {
					for _, r := range rep.Results {
						if strings.Contains(r.Name, "main.Box.") {
							errs <- fmt.Sprintf("use reported main.Box law: %+v", rep.Results)
							return
						}
					}
					if len(rep.Results) == 0 || rep.Results[0].Name != "law dep.Box.Positive for Zero" {
						errs <- fmt.Sprintf("use results mismatch: %+v", rep.Results)
					}
				}
			}(idx, a)
		}
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}
