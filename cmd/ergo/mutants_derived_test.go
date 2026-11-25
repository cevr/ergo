package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func runMutantsTextInProc(t *testing.T, args ...string) (string, int) {
	t.Helper()
	opts, err := parseFileOptions("test", append([]string{"--mutants"}, args...))
	if err != nil {
		t.Fatalf("parseFileOptions(%v): %v", args, err)
	}
	var stdout, stderr bytes.Buffer
	runErr := mutantsTestCommand(opts, &stdout, &stderr)
	return stdout.String(), exitCode(runErr)
}

func TestMutantsDerivedFlagsValidation(t *testing.T) {
	if _, err := parseFileOptions("test", []string{"--derived", "."}); err == nil {
		t.Fatalf("expected error for --derived without --mutants")
	}
	if _, err := parseFileOptions("test", []string{"--mutants", "--strict", "."}); err == nil {
		t.Fatalf("expected error for --strict with --mutants without --derived")
	}
	opts, err := parseFileOptions("test", []string{"--mutants", "--derived", "--strict", "."})
	if err != nil {
		t.Fatalf("expected --mutants --derived --strict to parse cleanly, got %v", err)
	}
	if !opts.mutants || !opts.derived || !opts.strict {
		t.Fatalf("expected flags set, got %+v", opts)
	}
}

func TestMutantsDerivedOffByDefault(t *testing.T) {
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module offbydefault\n")
	smokeWrite(t, dir, "lib.ergo", `func Less(a int, b int) bool {
	a < b
}

law LessAntiSym(a int, b int) {
	!(Less(a, b) && Less(b, a))
}
`)
	smokeWrite(t, dir, filepath.Join("testdata", "mutants.json"), `{
  "about": "off by default fixture",
  "mutants": [
    {
      "name": "hand-less-true",
      "file": "lib.ergo",
      "decl": "Less",
      "from": "a < b",
      "to": "true",
      "killers": []
    }
  ]
}
`)
	rep, code := runMutantsInProc(t, "--fresh", dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", code, rep)
	}
	if rep.DerivedMode || rep.DerivedGenerated != 0 {
		t.Fatalf("expected derived off by default, got DerivedMode=%v Generated=%d", rep.DerivedMode, rep.DerivedGenerated)
	}
	for _, v := range rep.Results {
		if v.Derived {
			t.Fatalf("unexpected derived verdict when --derived not passed: %+v", v)
		}
	}
}

func TestMutantsDerivedNoLedgerRequired(t *testing.T) {
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module noledger\n")
	smokeWrite(t, dir, "lib.ergo", `func IsPos(x int) bool {
	x > 0
}

law PosSign(x int) {
	IsPos(x) == (x > 0)
}

test TestIsPosBoundary {
	assert !IsPos(0)
	assert IsPos(1)
}
`)
	// No testdata/mutants.json at all!
	rep, code := runMutantsInProc(t, "--fresh", "--derived", "--strict", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 when all derived mutants in law-covered decl are killed, got %d (%+v)", code, rep)
	}
	if !rep.DerivedMode || rep.DerivedGenerated == 0 {
		t.Fatalf("expected derived mutants generated without ledger, got %+v", rep)
	}
	if rep.DerivedInvalid != 0 {
		t.Fatalf("expected 0 invalid derived mutants, got %d", rep.DerivedInvalid)
	}
	if rep.DerivedKilled == 0 {
		t.Fatalf("expected derived mutants killed, got %+v", rep)
	}
	if rep.DerivedLawSurvived != 0 {
		t.Fatalf("expected 0 law-covered survivors, got %+v", rep)
	}
}

func TestMutantsDerivedStrictAndLawFindings(t *testing.T) {
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module weaklaw\n")
	// WeakLaw calls Clamp with x > 10 only, so changing `x < 0` to `x <= 0` survives!
	// Uncovered has NO law covering it, so its survivors must NOT fail --strict.
	smokeWrite(t, dir, "lib.ergo", `func Clamp(x int) int {
	if x < 0 {
		return 0
	}
	x
}

func Uncovered(a int, b int) bool {
	a < b
}

law WeakClampLaw(x int where x > 10 && x < 100) {
	Clamp(x) == x
}
`)
	// Without --strict: exit 0, reports surviving derived mutant in law-covered decl as FINDING.
	rep, code := runMutantsInProc(t, "--fresh", "--derived", dir)
	if code != 0 {
		t.Fatalf("expected exit 0 without --strict, got %d (%+v)", code, rep)
	}
	if rep.DerivedInvalid != 0 {
		t.Fatalf("expected 0 invalid derived mutants, got %d", rep.DerivedInvalid)
	}
	if rep.DerivedLawSurvived == 0 {
		t.Fatalf("expected surviving derived mutant in law-covered Clamp, got %+v", rep)
	}
	foundClampFinding := false
	foundUncoveredNonLaw := false
	for _, v := range rep.Results {
		if v.Derived && v.Decl == "Clamp" && v.Status == mutantSurvived && v.LawCovered {
			foundClampFinding = true
		}
		if v.Derived && v.Decl == "Uncovered" && v.Status == mutantSurvived && !v.LawCovered {
			foundUncoveredNonLaw = true
		}
	}
	if !foundClampFinding {
		t.Fatalf("expected surviving law-covered derived mutant in Clamp, got %+v", rep.Results)
	}
	if !foundUncoveredNonLaw {
		t.Fatalf("expected surviving non-law derived mutant in Uncovered, got %+v", rep.Results)
	}

	// Text output must print a FINDING naming decl, operator, and line.
	text, textCode := runMutantsTextInProc(t, "--fresh", "--derived", dir)
	if textCode != 0 {
		t.Fatalf("expected text exit 0 without --strict, got %d\n%s", textCode, text)
	}
	if !strings.Contains(text, "FINDING") || !strings.Contains(text, "Clamp") || !strings.Contains(text, "cmp-swap") {
		t.Fatalf("expected FINDING naming Clamp and cmp-swap in text output:\n%s", text)
	}

	// With --strict: exit 1 because a derived mutant survived in law-covered Clamp.
	repStrict, strictCode := runMutantsInProc(t, "--fresh", "--derived", "--strict", dir)
	if strictCode != 1 {
		t.Fatalf("expected exit 1 with --derived --strict on law-covered survivor, got %d (%+v)", strictCode, repStrict)
	}

	// Now strengthen WeakClampLaw so Clamp's derived mutants are all killed, while Uncovered still has NO law.
	smokeWrite(t, dir, "lib.ergo", `func Clamp(x int) int {
	if x < 0 {
		return 0
	}
	x
}

func Uncovered(a int, b int) bool {
	a < b
}

law StrongClampLaw(x int) {
	let c = Clamp(x)
	if x < 0 {
		assert c == 0
	} else {
		assert c == x
	}
}

test TestClampZero {
	assert Clamp(0) == 0
	assert Clamp(-1) == 0
	assert Clamp(1) == 1
}
`)
	// Note: for Clamp, `x < 0` vs `x <= 0` returns 0 for x == 0 either way (equivalent mutant at x==0!),
	// so use a non-equivalent boundary function to test strict exit 0 when all law-covered mutants die.
	smokeWrite(t, dir, "lib.ergo", `func IsStrictPos(x int) bool {
	x > 0
}

func Uncovered(a int, b int) bool {
	a < b
}

law StrictPosLaw(x int) {
	IsStrictPos(x) == (x > 0)
}

test TestStrictPosBoundary {
	assert !IsStrictPos(0)
	assert IsStrictPos(1)
}
`)
	repClean, cleanCode := runMutantsInProc(t, "--fresh", "--derived", "--strict", dir)
	if cleanCode != 0 {
		t.Fatalf("expected exit 0 with --derived --strict when only non-law Uncovered has survivors, got %d (%+v)", cleanCode, repClean)
	}
	if repClean.DerivedLawSurvived != 0 {
		t.Fatalf("expected 0 law-covered survivors, got %+v", repClean)
	}
	if repClean.DerivedSurvived == 0 {
		t.Fatalf("expected non-law survivors in Uncovered, got %+v", repClean)
	}
}

func TestMutantsDerivedDuplicateDetection(t *testing.T) {
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module dupcheck\n")
	smokeWrite(t, dir, "lib.ergo", `func InRange(x int, hi int) bool {
	x < hi
}

law InRangeLaw(x int, hi int) {
	InRange(x, hi) == (x < hi)
}
`)
	// Row 1 ("dup-cmp-swap") duplicates the derived cmp-swap `x < hi` -> `x <= hi`.
	// Row 2 ("semantic-always-true") is a semantic hand row (`true`) NOT produced by derived operators
	// (return-zero produces `false`, cmp-swap produces `x <= hi`).
	smokeWrite(t, dir, filepath.Join("testdata", "mutants.json"), `{
  "about": "duplicate detection fixture",
  "mutants": [
    {
      "name": "dup-cmp-swap",
      "file": "lib.ergo",
      "decl": "InRange",
      "from": "x < hi",
      "to": "x <= hi",
      "killers": []
    },
    {
      "name": "semantic-always-true",
      "file": "lib.ergo",
      "decl": "InRange",
      "from": "x < hi",
      "to": "true",
      "killers": []
    }
  ]
}
`)
	rep, code := runMutantsInProc(t, "--fresh", "--derived", dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (%+v)", code, rep)
	}
	if len(rep.Duplicates) != 1 {
		t.Fatalf("expected exactly 1 duplicate (dup-cmp-swap), got %d: %+v", len(rep.Duplicates), rep.Duplicates)
	}
	dup := rep.Duplicates[0]
	if dup.HandRow != "dup-cmp-swap" || dup.Operator != "cmp-swap" || dup.Decl != "InRange" {
		t.Fatalf("unexpected duplicate record: %+v", dup)
	}
	// Verify text output also reports the duplicate clearly.
	text, _ := runMutantsTextInProc(t, "--fresh", "--derived", dir)
	if !strings.Contains(text, "duplicate dup-cmp-swap") || strings.Contains(text, "duplicate semantic-always-true") {
		t.Fatalf("unexpected duplicate lines in text report:\n%s", text)
	}
}

func TestMutantsDerivedWarmCache(t *testing.T) {
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module derivedcache\n")
	smokeWrite(t, dir, "lib.ergo", `func IsPos(x int) bool {
	x > 0
}

law PosLaw(x int) {
	IsPos(x) == (x > 0)
}
`)
	smokeWrite(t, dir, "unrelated.ergo", `func Helper(n int) int {
	n + 1
}
`)
	// Cold run populates both viability cache and killer verdict cache.
	cold, code := runMutantsInProc(t, "--derived", filepath.Join(dir, "lib.ergo"))
	if code != 0 {
		t.Fatalf("cold run exit %d: %+v", code, cold)
	}
	if cold.DerivedKilled == 0 || cold.DerivedCached != 0 {
		t.Fatalf("expected cold derived kills with 0 cached, got killed=%d cached=%d", cold.DerivedKilled, cold.DerivedCached)
	}

	// Warm run must hit cache for every killed derived mutant.
	warm, code := runMutantsInProc(t, "--derived", filepath.Join(dir, "lib.ergo"))
	if code != 0 {
		t.Fatalf("warm run exit %d: %+v", code, warm)
	}
	if warm.DerivedKilled != cold.DerivedKilled || warm.DerivedCached != cold.DerivedKilled {
		t.Fatalf("expected full warm cache reuse (%d/%d), got killed=%d cached=%d", cold.DerivedKilled, cold.DerivedKilled, warm.DerivedKilled, warm.DerivedCached)
	}

	// Edit unrelated.ergo: IsPos and PosLaw digests are unchanged, so killer verdicts stay cached!
	if err := os.WriteFile(filepath.Join(dir, "unrelated.ergo"), []byte("func Helper(n int) int {\n\tn + 2\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	afterEdit, code := runMutantsInProc(t, "--derived", filepath.Join(dir, "lib.ergo"))
	if code != 0 {
		t.Fatalf("after-edit run exit %d: %+v", code, afterEdit)
	}
	if afterEdit.DerivedCached != cold.DerivedKilled {
		t.Fatalf("expected derived killer verdicts to stay cached after unrelated edit, got cached=%d want=%d", afterEdit.DerivedCached, cold.DerivedKilled)
	}
}

func TestMutantsTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix process group test")
	}
	// 1. Direct process-group test: child spawns a long-running grandchild and
	// prints its PID. On timeout, runBoundedChild must kill BOTH child and
	// grandchild before returning.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $! > "+pidFile+"; wait")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runBoundedChild(ctx, cmd, 0)
	elapsed := time.Since(start)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("runBoundedChild took too long to kill timed-out process group: %v", elapsed)
	}
	raw, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("grandchild pid file missing: %v", rerr)
	}
	gpid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if perr != nil || gpid <= 1 {
		t.Fatalf("bad grandchild pid %q: %v", string(raw), perr)
	}
	// Give the kernel a tiny window to terminate the SIGKILLed grandchild, then verify it is dead.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(gpid) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(gpid) {
		_ = syscall.Kill(gpid, syscall.SIGKILL)
		t.Fatalf("grandchild pid %d survived timeout (process group was not killed)", gpid)
	}

	// 2. End-to-end infinite-loop derived mutant test: dropping `n = n - 1`
	// causes an infinite loop; runner must time out cleanly and classify
	// unjudged without hanging or leaking processes.
	t.Setenv("ERGO_LAW_CACHE", t.TempDir())
	t.Setenv("ERGO_MUTANT_TIMEOUT", "1s")
	dir := t.TempDir()
	smokeWrite(t, dir, "ergo.mod", "module infloop\n")
	smokeWrite(t, dir, "loop.ergo", `func CountDown(x int) int {
	let mut n = x
	if n < 0 {
		n = 0
	}
	let mut steps = 0
	for n > 0 {
		steps = steps + 1
		n = n - 1
	}
	steps
}

test TestCountDown {
	assert CountDown(3) == 3
}
`)
	loopStart := time.Now()
	rep, _ := runMutantsInProc(t, "--fresh", "--derived", "--run", "drop-stmt", dir)
	if time.Since(loopStart) > 20*time.Second {
		t.Fatalf("infinite-loop derived mutant took too long: %v", time.Since(loopStart))
	}
	foundTimeout := false
	for _, v := range rep.Results {
		if v.Derived && v.Status == mutantUnjudged && strings.Contains(v.Detail, "timed out") {
			foundTimeout = true
		}
	}
	if !foundTimeout {
		t.Fatalf("expected timed-out infinite-loop mutant classified as unjudged, got %+v", rep.Results)
	}
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		if idx := strings.LastIndexByte(string(data), ')'); idx >= 0 && idx+2 < len(data) {
			state := data[idx+2]
			if state == 'Z' || state == 'X' {
				return false
			}
		}
	}
	return true
}
