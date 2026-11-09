package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guardProbeVariable makes TestFastTierGuardProbe reach the helpers that
// build or run Go without needsBuild; TestFastTierGuard runs this test
// binary with it under -short.
const guardProbeVariable = "ERGO_TEST_GUARD_PROBE"

// TestFastTierGuardProbe calls one helper that builds or runs Go, named by
// guardProbeVariable, with no needsBuild first; otherwise it does nothing.
// It builds Go because the probe must reach the real build helpers, so the
// guard is seen to stop them.
func TestFastTierGuardProbe(t *testing.T) {
	switch os.Getenv(guardProbeVariable) {
	case "":
		t.Skip("a probe for TestFastTierGuard")
	case "ergoCLI":
		ergoCLI(t)
	case "buildAll":
		buildAll(t, []*lowered{mustLower(t, "probe", "func main() {}\n")}, true)
	case "runProgram":
		_ = runProgram(t, exec.Command("/nonexistent/loop-probe-x"))
	}
}

// TestFastTierGuard: under -short, a test that reaches ergoCLI, a linking
// buildAll or runProgram without needsBuild fails before any go command or
// program starts (test-speed plan T3: no test builds in the fast tier by
// accident). It runs this test binary, which is already built, once per
// helper; the probe's ERGO_TEST_CLI names no file, so a guard that let the
// call through would fail differently.
func TestFastTierGuard(t *testing.T) {
	for _, helper := range []string{"ergoCLI", "buildAll", "runProgram"} {
		cmd := exec.Command(os.Args[0], "-test.short", "-test.run=^TestFastTierGuardProbe$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), guardProbeVariable+"="+helper, cliVariable+"=/nonexistent/loop-probe-x", "PATH=/nonexistent/loop-probe-x")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "under -short: call needsBuild(t) first") {
			t.Errorf("%s under -short without needsBuild: err %v, want the guard's failure:\n%s", helper, err, firstLines(string(out), 20))
		}
	}
}

// TestNeedsBuildComesFirst: a test that calls needsBuild calls it as its
// first statement, so under -short it skips before it lowers, builds or
// runs anything.
func TestNeedsBuildComesFirst(t *testing.T) {
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, path := range paths {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name == "needsBuild" {
				continue
			}
			for i, s := range fd.Body.List {
				call, ok := s.(*ast.ExprStmt)
				if !ok {
					continue
				}
				if c, ok := call.X.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "needsBuild" {
						marked++
						if i != 0 || !strings.HasPrefix(fd.Name.Name, "Test") {
							t.Errorf("%s: %s calls needsBuild as statement %d; call it first, in the test itself", path, fd.Name.Name, i+1)
						}
					}
				}
			}
		}
	}
	if marked == 0 {
		t.Fatal("no test calls needsBuild: the scan reads the wrong files?")
	}
}
