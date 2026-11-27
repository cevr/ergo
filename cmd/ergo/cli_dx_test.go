package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIDXGroupedHelpNoLeakage(t *testing.T) {
	t.Parallel()
	help := generalHelp()
	for _, want := range []string{
		"ergo — failures handled, services supplied, laws tested",
		"Examples:",
		"Core:",
		"Verify & Inspect:",
		"Packages & Workspace:",
		"verify mutants|explore|conform|cases",
		"inspect [api|machine|deps|facts|copies|forms|rewrites|graph]",
		"--help-all",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("generalHelp missing %q:\n%s", want, help)
		}
	}
	for _, forbid := range []string{
		"Ergo prototype",
		"Go is the only target",
		"--target go",
		"--native",
		"--compiled",
		"ergo budget",
		"ergo tsmap",
		"ergo diagnostics",
		"ergo lib",
		"ergo conform",
		"-work",
		"--emitted",
		"-go-text",
		"-fallback",
		"-views=off",
		"-rewrites",
		"--dry-run",
	} {
		for _, text := range []string{help, checkHelp, testHelp, buildHelp, runHelp, fmtHelp, lintHelp, fixHelp, verifyHelp, inspectHelp} {
			if strings.Contains(text, forbid) {
				t.Errorf("primary help contains forbidden %q:\n%s", forbid, text)
			}
		}
	}
	for _, wantAll := range []string{"budget", "tsmap", "diagnostics", "lib", "conform", "-work", "--emitted"} {
		if !strings.Contains(generalHelpAll()+testAllHelp+buildAllHelp+fixAllHelp, wantAll) {
			t.Errorf("--help-all texts missing %q", wantAll)
		}
	}
}

func TestCLIDXDefaultTargetsOmittedPath(t *testing.T) {
	binary := buildTestCLI(t)

	// Single-package folder without ergo.mod: default target resolves to ".".
	single := t.TempDir()
	smokeWrite(t, single, "main.ergo", `type Bad {}

func Double(n int) int {
	n * 2
}

func main() needs Console {
	Console.Println("\(Double(3))")
}

test Doubles {
	assert Double(2) == 4
}
`)
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "fmt", "--check"); code != 0 {
		t.Fatalf("ergo fmt --check (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "check"); code != 0 || !strings.Contains(string(stdout), `"checked":true`) {
		t.Fatalf("ergo check (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "check", "--json"); code != 0 || !strings.Contains(string(stdout), `"policyPassed": true`) {
		t.Fatalf("ergo check --json (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "test"); code != 0 || !strings.Contains(string(stdout), "PASS (1 tests)") {
		t.Fatalf("ergo test (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "lint"); code != 0 {
		t.Fatalf("ergo lint (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "fix", "--check"); code != 0 {
		t.Fatalf("ergo fix --check (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "inspect"); code != 0 || !strings.Contains(string(stdout), `"imports"`) {
		t.Fatalf("ergo inspect (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "run"); code != 0 || string(stdout) != "6\n" {
		t.Fatalf("ergo run (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, single, "", "build", "-o", "appbin"); code != 0 || !strings.Contains(string(stdout), "appbin") {
		t.Fatalf("ergo build -o appbin (omitted path): exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// Module root with subpackages and no direct .ergo: check/test default to "./...".
	modRoot := t.TempDir()
	smokeWrite(t, modRoot, "ergo.mod", "module sample\n")
	smokeWrite(t, modRoot, "a/a.ergo", "func Inc(n int) int { n + 1 }\ntest ATest { assert Inc(1) == 2 }\n")
	smokeWrite(t, modRoot, "b/b.ergo", "func Dec(n int) int { n - 1 }\ntest BTest { assert Dec(2) == 1 }\n")

	if stdout, stderr, code := runSmokeCLI(t, binary, modRoot, "", "check"); code != 0 || strings.Count(string(stdout), `"checked":true`) != 2 {
		t.Fatalf("ergo check at module root: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, modRoot, "", "test"); code != 0 || !strings.Contains(string(stdout), "package sample/a") || !strings.Contains(string(stdout), "package sample/b") {
		t.Fatalf("ergo test at module root: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCLIDXParallelDefaultAndJobsOne(t *testing.T) {
	if runtime.GOMAXPROCS(0) >= 2 {
		if j := effectiveJobs(testShardOptions{}); j < 2 {
			t.Fatalf("effectiveJobs default = %d, want >= 2 when GOMAXPROCS >= 2", j)
		}
	}
	if j := effectiveJobs(testShardOptions{jobs: 1, jobsSet: true}); j != 1 {
		t.Fatalf("effectiveJobs(-j 1) = %d, want 1", j)
	}
	if j := effectiveJobs(testShardOptions{}); j > 8 {
		t.Fatalf("effectiveJobs default = %d, want <= 8", j)
	}

	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module par\n")
	smokeWrite(t, dir, "p1/p1.ergo", "func A(x int) int { x + 1 }\ntest TA { assert A(1) == 2 }\n")
	smokeWrite(t, dir, "p2/p2.ergo", "func B(x int) int { x + 2 }\ntest TB { assert B(1) == 3 }\n")

	binary := buildTestCLI(t)
	outDefBytes, stderrDef, codeDef := runSmokeCLI(t, binary, dir, "", "test", "--fresh", "./...")
	outDef := string(outDefBytes)
	if codeDef != 0 || !strings.Contains(outDef, "package par/p1") || !strings.Contains(outDef, "package par/p2") {
		t.Fatalf("parallel default test ./...: code=%d out=%q stderr=%q", codeDef, outDef, stderrDef)
	}
	outSerBytes, stderrSer, codeSer := runSmokeCLI(t, binary, dir, "", "test", "--fresh", "-j", "1", "./...")
	outSer := string(outSerBytes)
	if codeSer != 0 || outSer != outDef {
		t.Fatalf("test -j 1 mismatch: code=%d ser=%q stderr=%q def=%q", codeSer, outSer, stderrSer, outDef)
	}
	outChk, err := commandOutput(t, checkCommand, []string{filepath.Join(dir, "..."), "-j", "1"})
	if err != nil || strings.Count(outChk, `"checked":true`) != 2 {
		t.Fatalf("check -j 1: err=%v out=%q", err, outChk)
	}
}

func TestCLIDXFilterSkipAndFresh(t *testing.T) {
	t.Setenv("ERGO_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module filtdemo\n")
	pkg := filepath.Join(dir, "p")
	smokeWrite(t, pkg, "p.ergo", `func Add(a int, b int) int { a + b }

test AddOne { assert Add(1, 2) == 3 }
test AddTwo { assert Add(2, 3) == 5 }
test SubOne { assert 5 - 2 == 3 }

law Comm(a int, b int) { Add(a, b) == Add(b, a) }
`)
	// -k matching multiple tests runs both Add tests and excludes SubOne / Comm.
	out, err := testCommandOutput(t, []string{pkg, "-k", "^Add"})
	if err != nil || !strings.Contains(out, "ok AddOne") || !strings.Contains(out, "ok AddTwo") || strings.Contains(out, "SubOne") || strings.Contains(out, "Comm") || !strings.Contains(out, "PASS (2 tests)") {
		t.Fatalf("test -k ^Add: err=%v out=%q", err, out)
	}

	// --filter matching one law runs only that law.
	out, err = testCommandOutput(t, []string{pkg, "--filter", "Comm"})
	if err != nil || !strings.Contains(out, "ok law Comm") || strings.Contains(out, "AddOne") {
		t.Fatalf("test --filter Comm: err=%v out=%q", err, out)
	}

	// --skip excludes SubOne and runs the rest.
	out, err = testCommandOutput(t, []string{pkg, "--skip", "SubOne"})
	if err != nil || !strings.Contains(out, "ok AddOne") || !strings.Contains(out, "ok AddTwo") || !strings.Contains(out, "ok law Comm") || strings.Contains(out, "SubOne") {
		t.Fatalf("test --skip SubOne: err=%v out=%q", err, out)
	}

	// Unmatched --filter fails.
	if _, err := testCommandOutput(t, []string{pkg, "--filter", "DoesNotExist"}); err == nil {
		t.Fatal("expected unmatched --filter to fail")
	}

	// Invalid regex returns usage error exit 2.
	if code := exitCode(command([]string{"test", pkg, "--filter", "[unclosed"})); code != 2 {
		t.Fatalf("invalid --filter regex exit = %d, want 2", code)
	}

	// --fresh bypasses cached verdict note.
	binary := buildTestCLI(t)
	_, _, code := runSmokeCLI(t, binary, pkg, "", "test")
	if code != 0 {
		t.Fatalf("initial test exit %d", code)
	}
	_, stderrWarm, code := runSmokeCLI(t, binary, pkg, "", "test")
	if code != 0 || !strings.Contains(string(stderrWarm), cachedNote) {
		t.Fatalf("warm test stderr = %q (exit %d), want %q", stderrWarm, code, cachedNote)
	}
	_, stderrFresh, code := runSmokeCLI(t, binary, pkg, "", "test", "--fresh")
	if code != 0 || strings.Contains(string(stderrFresh), cachedNote) {
		t.Fatalf("fresh test stderr = %q (exit %d), want no cached note", stderrFresh, code)
	}
}

func TestCLIDXConsolidatedVerifyInspectGraphBuildLibAndFix(t *testing.T) {
	binary := buildTestCLI(t)

	// 1. ergo build --lib -o DIR builds Go-callable library module cleanly.
	libSrc := filepath.Join(t.TempDir(), "mathlib")
	smokeWrite(t, libSrc, "calc.ergo", "func Sum(a int, b int) int { a + b }\n")
	libOut := filepath.Join(t.TempDir(), "calc")
	if stdout, stderr, code := runSmokeCLI(t, binary, libSrc, "", "build", "--lib", "-o", libOut); code != 0 || strings.TrimSpace(string(stdout)) != libOut {
		t.Fatalf("ergo build --lib -o: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(libOut, "go.mod")); err != nil {
		t.Fatalf("ergo build --lib missing go.mod: %v", err)
	}
	if _, _, code := runSmokeCLI(t, binary, libSrc, "", "build", "--lib"); code != 2 {
		t.Fatalf("ergo build --lib without -o exit %d, want 2", code)
	}

	// 2. ergo inspect graph routes cleanly.
	mod := t.TempDir()
	smokeWrite(t, mod, "ergo.mod", "module gdemo\n")
	smokeWrite(t, mod, "a/a.ergo", "func Val() int { 1 }\n")
	smokeWrite(t, mod, "b/b.ergo", "import gdemo/a\nfunc Use() int { a.Val() }\n")
	if stdout, stderr, code := runSmokeCLI(t, binary, mod, "", "inspect", "graph", "--in", "./...", "export", "--json"); code != 0 || !strings.Contains(string(stdout), `"gdemo/b"`) {
		t.Fatalf("ergo inspect graph export: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// 3. ergo verify {mutants, explore, conform, cases}.
	mutDir := t.TempDir()
	writeMutantFixtureModule(t, mutDir)
	stdout, stderr, code := runSmokeCLI(t, binary, mutDir, "", "verify", "mutants", "-k", "^m-add-killed$", "--fresh", "--json")
	if code != 0 {
		t.Fatalf("ergo verify mutants: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var mrep mutantReport
	if err := json.Unmarshal(stdout, &mrep); err != nil || mrep.Killed != 1 {
		t.Fatalf("verify mutants report = %+v (err=%v)", mrep, err)
	}

	expDir := t.TempDir()
	smokeWrite(t, expDir, "exp.ergo", "test Ok { assert 1 + 1 == 2 }\n")
	if stdout, stderr, code := runSmokeCLI(t, binary, expDir, "", "verify", "explore", "dfs", "--schedules", "5"); code != 0 || !strings.Contains(string(stdout), "PASS") {
		t.Fatalf("ergo verify explore: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	confDir := t.TempDir()
	smokeWrite(t, confDir, "shop.ergo", conformStore)
	tracePath := filepath.Join(t.TempDir(), "trace.jsonl")
	smokeWrite(t, filepath.Dir(tracePath), filepath.Base(tracePath), `{"signal": "Put", "state": {"_tag": "Open", "count": 1}}`+"\n")
	if stdout, stderr, code := runSmokeCLI(t, binary, confDir, "", "verify", "conform", "--machine", "Shop", "--trace", tracePath); code != 0 || !strings.Contains(string(stdout), "conforms: 1 lines") {
		t.Fatalf("ergo verify conform: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	pkg, err := filepath.Abs(filepath.Join("..", "..", "examples", "lru", "cmd", "lru"))
	if err != nil {
		t.Fatal(err)
	}
	casesDir := filepath.Join(pkg, "..", "..", "cases")
	if stdout, stderr, code := runSmokeCLI(t, binary, pkg, "", "verify", "cases", "--cases", casesDir); code != 0 || !strings.Contains(string(stdout), "PASS (") {
		t.Fatalf("ergo verify cases: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}

	// 4. ergo check --json matches DiagnosticReport and respects --strict.
	chkDir := t.TempDir()
	smokeWrite(t, chkDir, "bad.ergo", sketchMissingNeeds)
	outJSON, errJSON, code := runSmokeCLI(t, binary, chkDir, "", "check", "--json", "--strict")
	if code != 1 || !strings.Contains(string(errJSON), "check failed") {
		t.Fatalf("ergo check --json: exit %d stderr=%q stdout=%q", code, errJSON, outJSON)
	}
	var diagRep map[string]any
	if err := json.Unmarshal(outJSON, &diagRep); err != nil || diagRep["policyPassed"] != false || diagRep["strict"] != true {
		t.Fatalf("check --json output = %s (err=%v)", outJSON, err)
	}

	// 5. ergo fix defaults to . and integrates clause + safe lint fixes together.
	fixDir := t.TempDir()
	smokeWrite(t, fixDir, "ergo.mod", "module fixdemo\n")
	smokeWrite(t, fixDir, "main.ergo", `type Bad {}

func leaf() int fails Bad { 1 }

func helper() int { try leaf() }

func main() {}
`)
	if stdout, stderr, code := runSmokeCLI(t, binary, fixDir, "", "fix", "--check"); code != 1 {
		t.Fatalf("ergo fix --check: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, fixDir, "", "fix"); code != 0 {
		t.Fatalf("ergo fix: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	fixedBytes, _ := os.ReadFile(filepath.Join(fixDir, "main.ergo"))
	if !strings.Contains(string(fixedBytes), "func helper() int fails Bad {") {
		t.Fatalf("ergo fix did not write clause:\n%s", fixedBytes)
	}
	if _, _, code := runSmokeCLI(t, binary, fixDir, "", "fix", "--check"); code != 0 {
		t.Fatalf("ergo fix --check after fix: exit %d", code)
	}

	// Verify command errors cleanly on invalid invocations.
	for _, args := range [][]string{{"verify"}, {"verify", "unknown"}, {"verify", "cases"}} {
		err := command(args)
		if code := exitCode(err); code != 2 {
			t.Errorf("ergo %v: exit %d (%v), want 2", args, code, err)
		}
	}
	var status exitStatusError
	_ = errors.As(errors.New(""), &status)
	_ = bytes.NewReader
}
