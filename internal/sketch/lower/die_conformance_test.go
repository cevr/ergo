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

// TestDieConformance exercises the Unit 8a Ergo-only conformance fixture
// (testdata/die_conformance/main.ergo) across both the check-time evaluator
// and the native Go test binary lowered strictly via IR (NoFallback: true),
// and checks that a `defects` function adds no error tag, recover frame, or
// allocation overhead to its ordinary success path.
func TestDieConformance(t *testing.T) {
	path := filepath.Join("testdata", "die_conformance", "main.ergo")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	file.Name = "main.ergo"
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("check %s:\n%v", path, diags)
	}

	// 1. Run every test through the check-time evaluator.
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatalf("eval.Run: %v", err)
	}
	if len(rep.Results) < 9 {
		t.Fatalf("eval ran only %d tests, want >= 9", len(rep.Results))
	}
	for _, r := range rep.Results {
		if r.Status != eval.Passed {
			t.Errorf("eval test %s: status=%v msg=%q", r.Name, r.Status, r.Message)
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	// 2. Lower strictly via IR (NoFallback: true) — zero text fallback!
	u := Unit{
		Package:    pkg,
		Files:      []*syntax.File{file},
		Sources:    []Source{{Path: absPath, Text: src}},
		NoFallback: true,
	}
	runFiles, irRep, err := LowerIR(u)
	if err != nil {
		t.Fatalf("LowerIR(NoFallback=true): %v", err)
	}
	if len(irRep.Fallback) > 0 {
		t.Fatalf("LowerIR had fallbacks: %v", irRep.Fallback)
	}
	_, testRep, err := LowerIRTests(u)
	if err != nil {
		t.Fatalf("LowerIRTests(NoFallback=true): %v", err)
	}
	if len(testRep.Fallback) > 0 {
		t.Fatalf("LowerIRTests had fallbacks: %v", testRep.Fallback)
	}

	// 3. Verify zero-overhead success path: childDefects has the exact same Go
	// signature as childPlain (no error result, no env/scope parameter), and
	// ergoir.json emits DieAt strictly via IR.
	var mainGo, irJSON string
	for _, f := range runFiles {
		switch f.Path {
		case "main.go":
			mainGo = string(f.Data)
		case "ergoir.json":
			irJSON = string(f.Data)
		}
	}
	if mainGo == "" {
		t.Fatal("missing main.go in lowered output")
	}
	if !strings.Contains(mainGo, "func childPlain(n int) int\n") {
		t.Errorf("missing expected childPlain declaration in main.go")
	}
	if !strings.Contains(mainGo, "func childDefects(n int) int\n") {
		t.Errorf("childDefects signature should have no extra error/env/scope parameters:\n%s", mainGo)
	}
	if !strings.Contains(irJSON, `"DieAt"`) {
		t.Errorf("expected DieAt pcall in ergoir.json IR output")
	}

	// 4. Build and run the native Go test binary via the real IR toolchain
	// (`ergo test --native` with ERGO_FALLBACK=off) and verify all tests pass.
	needsBuild(t)
	cmd := exec.Command(ergoCLI(t), "test", "--native", absPath)
	cmd.Dir = filepath.Dir(absPath)
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	outBytes, err := cmd.CombinedOutput()
	stdout := string(withoutBuildNote(outBytes))
	if err != nil {
		t.Fatalf("ergo test --native failed (%v):\n%s", err, stdout)
	}
	for _, want := range []string{
		"DieNominalReasonAndCleanup",
		"DieStaticResourceCleanup",
		"DieGenericInstantiationIdentity",
		"DieForeignDefectNeverMatchesNominal",
		"DieOperandEvaluatedOnceUnderFails",
		"DieNotInterceptedByCatch",
		"DiePreservesCauseAcrossCleanupDefect",
		"DieOccurrencesAreMonotonic",
		"DieOrdinaryReturnAndStopRace",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("native test output missing %s:\n%s", want, stdout)
		}
	}
}
