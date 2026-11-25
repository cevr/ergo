package lower

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

func runAlwaysSometimesProgram(t *testing.T, src string, seed uint64) (*eval.Report, string, int) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	f.Name = "main.ergo"
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("check: %v\n%s", diags, src)
	}

	budget := eval.DefaultBudget()
	budget.Seed = seed
	rep, err := eval.Run(pkg, budget)
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u := Unit{
		Package:    pkg,
		Files:      []*syntax.File{f},
		Sources:    []Source{{Path: srcPath, Text: src}},
		NoFallback: true,
	}
	files, testRep, err := LowerIRTests(u)
	if err != nil {
		t.Fatalf("LowerIRTests(NoFallback=true): %v", err)
	}
	if len(testRep.Fallback) > 0 {
		t.Fatalf("LowerIRTests had IR fallbacks: %v", testRep.Fallback)
	}
	_ = files
	// Production build must erase all law constructs.
	runFiles, runRep, err := LowerIR(u)
	if err != nil {
		t.Fatalf("LowerIR(NoFallback=true): %v", err)
	}
	if len(runRep.Fallback) > 0 {
		t.Fatalf("LowerIR had IR fallbacks: %v", runRep.Fallback)
	}
	for _, rf := range runFiles {
		if rf.Path == "main.go" && strings.Contains(string(rf.Data), "LawSometimes") {
			t.Fatalf("production run build leaked LawSometimes:\n%s", string(rf.Data))
		}
	}

	cmd := exec.Command(ergoCLI(t), "test", "--native", "--seed", formatUint(seed), srcPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, runErr := cmd.CombinedOutput()
	code := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("ergo test --native failed: %v\n%s", runErr, string(out))
		}
	}
	return &rep, string(withoutBuildNote(out)), code
}

func formatUint(u uint64) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}

func findEvalResult(t *testing.T, rep *eval.Report, name string) eval.Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("eval result %q not found in %+v", name, rep.Results)
	return eval.Result{}
}

func TestLawAlwaysSometimesStatements(t *testing.T) {
	needsBuild(t)
	src := `law AlwaysHolds(n int) {
	always n - n == 0
	true
}

law AlwaysFails(n int) {
	always n < 5
	true
}

law SometimesHits(n int) {
	sometimes n > 0
	sometimes n <= 0
	true
}

law SometimesNeverHits(n int) {
	sometimes n != n
	true
}

law SometimesUnreachedBranch(n int) {
	if false {
		sometimes true
	}
	n == n
}

func main() {}
`
	rep, stdout, code := runAlwaysSometimesProgram(t, src, 7)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s", code, stdout)
	}

	// 1. AlwaysHolds passes in both eval and native.
	if r := findEvalResult(t, rep, "law AlwaysHolds"); r.Status != eval.Passed {
		t.Errorf("eval AlwaysHolds: status=%v reason=%q", r.Status, r.Reason)
	}
	if !strings.Contains(stdout, "ok law AlwaysHolds (100 cases)") {
		t.Errorf("native missing ok AlwaysHolds:\n%s", stdout)
	}

	// 2. AlwaysFails fails with site, expr, and shown operands in both eval and native.
	rFail := findEvalResult(t, rep, "law AlwaysFails")
	if rFail.Status != eval.Refuted || !strings.Contains(rFail.Message, "always failed at main.ergo:7:2: n < 5: left ") {
		t.Errorf("eval AlwaysFails: status=%v msg=%q", rFail.Status, rFail.Message)
	}
	if !strings.Contains(stdout, "FAIL law AlwaysFails") || !strings.Contains(stdout, "always failed at main.ergo:7:2: n < 5: left ") {
		t.Errorf("native AlwaysFails missing expected failure:\n%s", stdout)
	}

	// 3. SometimesHits passes in both.
	if r := findEvalResult(t, rep, "law SometimesHits"); r.Status != eval.Passed {
		t.Errorf("eval SometimesHits: status=%v reason=%q", r.Status, r.Reason)
	}
	if !strings.Contains(stdout, "ok law SometimesHits (100 cases)") {
		t.Errorf("native missing ok SometimesHits:\n%s", stdout)
	}

	// 4. SometimesNeverHits fails naming exact site and expr in both.
	rNever := findEvalResult(t, rep, "law SometimesNeverHits")
	if rNever.Status != eval.Refuted || !strings.Contains(rNever.Message, "main.ergo:18:2: sometimes n != n was never true across 100 cases") {
		t.Errorf("eval SometimesNeverHits: status=%v msg=%q", rNever.Status, rNever.Message)
	}
	if !strings.Contains(stdout, "FAIL law SometimesNeverHits: main.ergo:18:2: sometimes n != n was never true across 100 cases") {
		t.Errorf("native SometimesNeverHits missing expected failure:\n%s", stdout)
	}

	// 5. SometimesUnreachedBranch fails naming exact unreached site in both!
	rUnreach := findEvalResult(t, rep, "law SometimesUnreachedBranch")
	if rUnreach.Status != eval.Refuted || !strings.Contains(rUnreach.Message, "main.ergo:24:3: sometimes true was never true across 100 cases") {
		t.Errorf("eval SometimesUnreachedBranch: status=%v msg=%q", rUnreach.Status, rUnreach.Message)
	}
	if !strings.Contains(stdout, "FAIL law SometimesUnreachedBranch: main.ergo:24:3: sometimes true was never true across 100 cases") {
		t.Errorf("native SometimesUnreachedBranch missing expected failure:\n%s", stdout)
	}
}

func TestLawAlwaysSometimesGenCheckClosure(t *testing.T) {
	needsBuild(t)
	src := `import gen

law GenCheckPass() {
	let cfg = gen.Config{..gen.Defaults(), Cases: 20}
	let res = gen.Check(cfg, func() bool fails gen.Rejected needs gen.Draws {
		let x = gen.IntIn(-10, 10)
		always x >= -10 && x <= 10
		sometimes x > 0
		sometimes x <= 0
		true
	})
	match res {
	case gen.Held:
		true
	case _:
		false
	}
}

law GenCheckMissesSometimes() {
	let cfg = gen.Config{..gen.Defaults(), Cases: 10}
	let res = gen.Check(cfg, func() bool fails gen.Rejected needs gen.Draws {
		let x = gen.IntIn(0, 5)
		sometimes x > 100
		true
	})
	match res {
	case gen.Held:
		true
	case _:
		false
	}
}

law GenCheckAlwaysFails() {
	let cfg = gen.Config{..gen.Defaults(), Cases: 10}
	let res = gen.Check(cfg, func() bool fails gen.Rejected needs gen.Draws {
		let x = gen.IntIn(0, 5)
		always x < 0
		true
	})
	match res {
	case gen.Held:
		true
	case _:
		false
	}
}

func main() {}
`
	rep, stdout, code := runAlwaysSometimesProgram(t, src, 7)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s", code, stdout)
	}

	if r := findEvalResult(t, rep, "law GenCheckPass"); r.Status != eval.Passed {
		t.Errorf("eval GenCheckPass: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "ok law GenCheckPass") {
		t.Errorf("native missing ok GenCheckPass:\n%s", stdout)
	}

	rMiss := findEvalResult(t, rep, "law GenCheckMissesSometimes")
	if rMiss.Status != eval.Refuted || !strings.Contains(rMiss.Message, "sometimes x > 100 was never true") {
		t.Errorf("eval GenCheckMissesSometimes: status=%v msg=%q", rMiss.Status, rMiss.Message)
	}
	if !strings.Contains(stdout, "FAIL law GenCheckMissesSometimes:") || !strings.Contains(stdout, "sometimes x > 100 was never true") {
		t.Errorf("native GenCheckMissesSometimes missing expected failure:\n%s", stdout)
	}

	rAlw := findEvalResult(t, rep, "law GenCheckAlwaysFails")
	if rAlw.Status != eval.Refuted || !strings.Contains(rAlw.Message, "always failed at") || !strings.Contains(rAlw.Message, "x < 0") {
		t.Errorf("eval GenCheckAlwaysFails: status=%v msg=%q", rAlw.Status, rAlw.Message)
	}
	if !strings.Contains(stdout, "FAIL law GenCheckAlwaysFails") || !strings.Contains(stdout, "always failed at") {
		t.Errorf("native GenCheckAlwaysFails missing expected failure:\n%s", stdout)
	}
}

func TestLawExistentialAndOperationType(t *testing.T) {
	needsBuild(t)
	src := `law DrawnWitness sometimes(n int) {
	n > 0
}

law DrawnNoWitness sometimes(n int) {
	n != n
}

law DrawnExistAlwaysFails sometimes(n int) {
	always n < 5
	n > 100
}

law ListedWitness sometimes(b bool) {
	b
}

law ListedNoWitness sometimes(b bool) {
	b && !b
}

law ExplicitAlways always(n int) {
	n - n == 0
}

type Probe {
	Value() int,
	law CanBePositive sometimes() { Value() > 0 },
}

type Pos { v int } implements Probe
func Value(p Pos) int { p.v }

type Zero {} implements Probe
func Value(z Zero) int { 0 }

func main() {}
`
	rep, stdout, code := runAlwaysSometimesProgram(t, src, 7)
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d\nstdout:\n%s", code, stdout)
	}

	// DrawnWitness passes in both.
	if r := findEvalResult(t, rep, "law DrawnWitness"); r.Status != eval.Passed {
		t.Errorf("eval DrawnWitness: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "ok law DrawnWitness (witness found at") {
		t.Errorf("native DrawnWitness:\n%s", stdout)
	}

	// DrawnNoWitness fails in both.
	if r := findEvalResult(t, rep, "law DrawnNoWitness"); r.Status != eval.Refuted || !strings.Contains(r.Message, "no witness found across 100 cases") {
		t.Errorf("eval DrawnNoWitness: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "FAIL law DrawnNoWitness: no witness found across 100 cases") {
		t.Errorf("native DrawnNoWitness:\n%s", stdout)
	}

	// DrawnExistAlwaysFails refutes immediately via always in both!
	if r := findEvalResult(t, rep, "law DrawnExistAlwaysFails"); r.Status != eval.Refuted || !strings.Contains(r.Message, "always failed at") {
		t.Errorf("eval DrawnExistAlwaysFails: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "FAIL law DrawnExistAlwaysFails") || !strings.Contains(stdout, "always failed at") {
		t.Errorf("native DrawnExistAlwaysFails:\n%s", stdout)
	}

	// ListedWitness passes, ListedNoWitness fails in both.
	if r := findEvalResult(t, rep, "law ListedWitness"); r.Status != eval.Passed {
		t.Errorf("eval ListedWitness: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "ok law ListedWitness (proved: witness found at") {
		t.Errorf("native ListedWitness:\n%s", stdout)
	}
	if r := findEvalResult(t, rep, "law ListedNoWitness"); r.Status != eval.Refuted || !strings.Contains(r.Message, "no witness found across 2 cases") {
		t.Errorf("eval ListedNoWitness: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "FAIL law ListedNoWitness: no witness found across 2 cases") {
		t.Errorf("native ListedNoWitness:\n%s", stdout)
	}

	// ExplicitAlways passes in both.
	if r := findEvalResult(t, rep, "law ExplicitAlways"); r.Status != eval.Passed {
		t.Errorf("eval ExplicitAlways: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "ok law ExplicitAlways (100 cases)") {
		t.Errorf("native ExplicitAlways:\n%s", stdout)
	}

	// Operation-type member existential law: Pos passes, Zero fails!
	if r := findEvalResult(t, rep, "law Probe.CanBePositive for Pos"); r.Status != eval.Passed {
		t.Errorf("eval Probe.CanBePositive for Pos: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "ok law Probe.CanBePositive for Pos (witness found at") {
		t.Errorf("native Probe.CanBePositive for Pos:\n%s", stdout)
	}
	if r := findEvalResult(t, rep, "law Probe.CanBePositive for Zero"); r.Status != eval.Refuted || !strings.Contains(r.Message, "no witness found across 1 cases") {
		t.Errorf("eval Probe.CanBePositive for Zero: status=%v msg=%q", r.Status, r.Message)
	}
	if !strings.Contains(stdout, "FAIL law Probe.CanBePositive for Zero: no witness found across 1 cases") {
		t.Errorf("native Probe.CanBePositive for Zero:\n%s", stdout)
	}
}
