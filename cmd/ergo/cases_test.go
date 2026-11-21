package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCasesFlagRunsTheBuiltProgram is `ergo test PKG --cases DIR` end to
// end: the lru example's cases pass against its built cmd/lru; a copy with
// one expected file edited fails with exit 1 and that case's diff; a
// folder of no case is exit 1 (no tests is red). The verdicts of each
// case folder are internal/cases' property (TestCasesAgreeWithTheRunner);
// this test holds the flag's wiring. It builds Go because the flag's
// contract is the built program's bytes on a real run.
func TestCasesFlagRunsTheBuiltProgram(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	pkg, err := filepath.Abs(filepath.Join("..", "..", "examples", "lru", "cmd", "lru"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(pkg, "..", "..", "cases")

	stdout, stderr, code := runTestCLI(t, binary, "test", pkg, "--cases", source)
	if code != 0 || !strings.Contains(string(stdout), "ok lookup\n") || !strings.HasSuffix(string(stdout), " cases)\n") || !strings.Contains(string(stdout), "PASS (") {
		t.Fatalf("the lru cases: exit %d, want 0 and PASS\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	edited := filepath.Join(t.TempDir(), "cases")
	if out, err := exec.Command("cp", "-R", source, edited).CombinedOutput(); err != nil {
		t.Fatalf("copy the cases: %v\n%s", err, out)
	}
	golden := filepath.Join(edited, "lookup", "expected.out")
	content, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(golden, append(content, "edited\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runTestCLI(t, binary, "test", pkg, "--cases", edited)
	if code != 1 || !strings.Contains(string(stdout), "FAIL lookup: stdout differs\n") || !strings.Contains(string(stdout), "\n-edited\n") || !strings.Contains(string(stdout), "ok lookup-three\n") {
		t.Fatalf("an edited golden: exit %d, want 1 with the diff of lookup\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	empty := t.TempDir()
	stdout, stderr, code = runTestCLI(t, binary, "test", pkg, "--cases", empty)
	if code != 1 || !strings.Contains(string(stdout), "holds no case") {
		t.Fatalf("a folder of no case: exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
