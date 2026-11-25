package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/report"
)

func runErgoTestInProc(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wo, we
	var outBuf, errBuf bytes.Buffer
	outDone := make(chan struct{})
	errDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&outBuf, ro)
		_ = ro.Close()
		close(outDone)
	}()
	go func() {
		_, _ = io.Copy(&errBuf, re)
		_ = re.Close()
		close(errDone)
	}()
	runErr := testCommand(args)
	_ = wo.Close()
	_ = we.Close()
	os.Stdout, os.Stderr = origOut, origErr
	<-outDone
	<-errDone
	return outBuf.String(), errBuf.String(), exitCode(runErr)
}

func lineForObligation(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func writeCaseStoreFixture(t *testing.T, dir, checkValBody string) {
	t.Helper()
	smokeWrite(t, dir, "ergo.mod", "module sample\n")
	smokeWrite(t, dir, "lib.ergo", `func checkVal(x int) int {
	`+checkValBody+`
}

func checkPos(x int) int {
	x
}

law Bound(x int) {
	checkVal(x) <= 5
}

test Positive(x int) {
	assert checkPos(x) >= 0
}
`)
}

func TestCasesStoreEvalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeCaseStoreFixture(t, dir, "x")
	casesPath := filepath.Join(dir, lawledger.CasesFile)
	// Seed a shadow refutation line to prove backward compatibility across writes and --prune-cases.
	smokeWrite(t, dir, lawledger.CasesFile, "law sample.LegacyShadow (x = 0)\n")

	// 1. Seeded refutation records shrunk tapes into ergo.cases while preserving shadow lines.
	out1, _, code1 := runErgoTestInProc(t, "--seed", "17", dir)
	if code1 != 1 {
		t.Fatalf("step 1: expected exit 1, got %d\nstdout:\n%s", code1, out1)
	}
	store1, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("step 1: ReadCasesFile: %v", err)
	}
	if len(store1.Shadow["sample.LegacyShadow"]) != 1 {
		t.Fatalf("step 1: shadow line lost: %+v", store1.Shadow)
	}
	boundKey := lawledger.PropertyKey("main", "law Bound")
	posKey := lawledger.PropertyKey("main", "Positive")
	boundCases := store1.ForProperty(boundKey)
	posCases := store1.ForProperty(posKey)
	if len(boundCases) != 1 || len(posCases) != 1 {
		t.Fatalf("step 1: expected 1 stored case each for Bound and Positive, got Bound=%d Positive=%d\nstore=%s", len(boundCases), len(posCases), store1.Format())
	}
	if !strings.HasPrefix(boundCases[0].Digest, "h1:") || strings.HasPrefix(boundCases[0].Digest, "h1:h1:") {
		t.Fatalf("step 1: malformed digest %q", boundCases[0].Digest)
	}

	// 2. Different seed replays stored tapes at case 0 BEFORE random draws.
	out2, _, code2 := runErgoTestInProc(t, "--seed", "99", dir)
	if code2 != 1 {
		t.Fatalf("step 2: expected exit 1, got %d\nstdout:\n%s", code2, out2)
	}
	boundLine2 := lineForObligation(out2, "FAIL law Bound ")
	posLine2 := lineForObligation(out2, "FAIL Positive ")
	if !strings.Contains(boundLine2, "case 0, stored case") {
		t.Fatalf("step 2: expected Bound to replay before draws at 'case 0, stored case', got %q\nfull:\n%s", boundLine2, out2)
	}
	if !strings.Contains(posLine2, "case 0, stored case") {
		t.Fatalf("step 2: expected Positive to replay before draws at 'case 0, stored case', got %q\nfull:\n%s", posLine2, out2)
	}

	// 3. Edit checkVal body so reach digest changes while still failing:
	// stale tape MUST be reported as extra case, digest changed — never "stored case".
	writeCaseStoreFixture(t, dir, "x + 0")
	out3, _, code3 := runErgoTestInProc(t, "--seed", "101", dir)
	if code3 != 1 {
		t.Fatalf("step 3: expected exit 1, got %d\nstdout:\n%s", code3, out3)
	}
	boundLine3 := lineForObligation(out3, "FAIL law Bound ")
	if !strings.Contains(boundLine3, "case 0, extra case, digest changed") {
		t.Fatalf("step 3: expected stale Bound line to say 'case 0, extra case, digest changed', got %q\nfull:\n%s", boundLine3, out3)
	}
	if strings.Contains(boundLine3, "stored case") {
		t.Fatalf("step 3: stale Bound line must not say 'stored case': %q", boundLine3)
	}

	// 4. Fix checkVal so Bound passes while Positive still fails:
	// normal ergo test keeps passing stored cases in ergo.cases!
	writeCaseStoreFixture(t, dir, "0")
	out4, _, code4 := runErgoTestInProc(t, "--seed", "103", dir)
	if code4 != 1 {
		t.Fatalf("step 4: expected exit 1 (Positive still failing), got %d\nstdout:\n%s", code4, out4)
	}
	boundLine4 := lineForObligation(out4, "ok law Bound ")
	if !strings.Contains(boundLine4, "extra case(s): digest changed") {
		t.Fatalf("step 4: expected passing Bound line to mention stale extra cases, got %q\nfull:\n%s", boundLine4, out4)
	}
	store4, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("step 4: ReadCasesFile: %v", err)
	}
	if len(store4.ForProperty(boundKey)) == 0 {
		t.Fatalf("step 4: normal ergo test dropped passing stored cases for Bound:\n%s", store4.Format())
	}

	// 5. --prune-cases removes ONLY passing stored cases (keeps failing Positive & shadow lines).
	out5, err5, code5 := runErgoTestInProc(t, "--prune-cases", dir)
	if code5 != 0 {
		t.Fatalf("step 5: expected exit 0 from --prune-cases, got %d\nstdout:\n%s\nstderr:\n%s", code5, out5, err5)
	}
	if !strings.Contains(out5, "pruned ") || !strings.Contains(out5, "passing case(s)") {
		t.Fatalf("step 5: expected prune summary, got %q", out5)
	}
	store5, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("step 5: ReadCasesFile: %v", err)
	}
	if len(store5.ForProperty(boundKey)) != 0 {
		t.Fatalf("step 5: expected passing Bound cases pruned, got:\n%s", store5.Format())
	}
	if len(store5.ForProperty(posKey)) != 1 {
		t.Fatalf("step 5: expected failing Positive case kept, got:\n%s", store5.Format())
	}
	if len(store5.Shadow["sample.LegacyShadow"]) != 1 {
		t.Fatalf("step 5: expected shadow refutation line kept, got:\n%s", store5.Format())
	}
}

func TestCasesStoreNativeRoundTrip(t *testing.T) {
	t.Setenv("ERGO_FALLBACK", "off")
	dir := t.TempDir()
	writeCaseStoreFixture(t, dir, "x")
	casesPath := filepath.Join(dir, lawledger.CasesFile)
	boundKey := lawledger.PropertyKey("main", "law Bound")
	posKey := lawledger.PropertyKey("main", "Positive")

	// 1. Native refutation records shrunk tapes into ergo.cases without leaking machine lines.
	out1, err1, code1 := runErgoTestInProc(t, "--native", "--seed", "17", dir)
	if code1 != 1 {
		t.Fatalf("native step 1: expected exit 1, got %d\nstdout:\n%s\nstderr:\n%s", code1, out1, err1)
	}
	if strings.Contains(out1, "ergo:tape") || strings.Contains(err1, "ergo:tape") {
		t.Fatalf("native step 1: machine tape line leaked to user output\nstdout:\n%s\nstderr:\n%s", out1, err1)
	}
	store1, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("native step 1: ReadCasesFile: %v", err)
	}
	if len(store1.ForProperty(boundKey)) != 1 || len(store1.ForProperty(posKey)) != 1 {
		t.Fatalf("native step 1: expected 1 stored case each, got:\n%s", store1.Format())
	}
	if !strings.HasPrefix(store1.ForProperty(boundKey)[0].Digest, "h1:") || strings.HasPrefix(store1.ForProperty(boundKey)[0].Digest, "h1:h1:") {
		t.Fatalf("native step 1: malformed digest %q", store1.ForProperty(boundKey)[0].Digest)
	}

	// 2. Native replay under a different seed hits stored cases at case 0 before random draws.
	out2, _, code2 := runErgoTestInProc(t, "--native", "--seed", "99", dir)
	if code2 != 1 {
		t.Fatalf("native step 2: expected exit 1, got %d\nstdout:\n%s", code2, out2)
	}
	if line := lineForObligation(out2, "FAIL law Bound "); !strings.Contains(line, "case 0, stored case") {
		t.Fatalf("native step 2: expected Bound 'case 0, stored case', got %q\nfull:\n%s", line, out2)
	}
	if line := lineForObligation(out2, "FAIL Positive "); !strings.Contains(line, "case 0, stored case") {
		t.Fatalf("native step 2: expected Positive 'case 0, stored case', got %q\nfull:\n%s", line, out2)
	}

	// 3. Stale digest while still failing says extra case, digest changed (never stored case).
	writeCaseStoreFixture(t, dir, "x + 0")
	out3, _, code3 := runErgoTestInProc(t, "--native", "--seed", "101", dir)
	if code3 != 1 {
		t.Fatalf("native step 3: expected exit 1, got %d\nstdout:\n%s", code3, out3)
	}
	boundLine3 := lineForObligation(out3, "FAIL law Bound ")
	if !strings.Contains(boundLine3, "case 0, extra case, digest changed") || strings.Contains(boundLine3, "stored case") {
		t.Fatalf("native step 3: expected stale Bound line 'case 0, extra case, digest changed', got %q\nfull:\n%s", boundLine3, out3)
	}

	// 4. Fix checkVal -> native pass keeps passing entries in ergo.cases.
	writeCaseStoreFixture(t, dir, "0")
	out4, _, code4 := runErgoTestInProc(t, "--native", "--seed", "103", dir)
	if code4 != 1 {
		t.Fatalf("native step 4: expected exit 1, got %d\nstdout:\n%s", code4, out4)
	}
	if line := lineForObligation(out4, "ok law Bound "); !strings.Contains(line, "extra case(s): digest changed") {
		t.Fatalf("native step 4: expected passing Bound extra case note, got %q\nfull:\n%s", line, out4)
	}
	store4, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("native step 4: ReadCasesFile: %v", err)
	}
	if len(store4.ForProperty(boundKey)) == 0 {
		t.Fatalf("native step 4: normal native test dropped passing Bound cases:\n%s", store4.Format())
	}

	// 5. Native --prune-cases removes passing Bound cases and keeps failing Positive case.
	out5, err5, code5 := runErgoTestInProc(t, "--native", "--prune-cases", dir)
	if code5 != 0 {
		t.Fatalf("native step 5: expected exit 0, got %d\nstdout:\n%s\nstderr:\n%s", code5, out5, err5)
	}
	store5, err := lawledger.ReadCasesFile(casesPath)
	if err != nil {
		t.Fatalf("native step 5: ReadCasesFile: %v", err)
	}
	if len(store5.ForProperty(boundKey)) != 0 || len(store5.ForProperty(posKey)) != 1 {
		t.Fatalf("native step 5: unexpected store after prune:\n%s", store5.Format())
	}
}

func TestCasesFileEditAffectsModuleObligations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	base := map[string]string{
		"ergo.mod":   "module m\n",
		"lib.ergo":   "func Id(x int) int {\n\tx\n}\n\nlaw Identity(x int) {\n\tId(x) == x\n}\n",
		"ergo.cases": "",
	}
	head := map[string]string{
		"ergo.mod":   base["ergo.mod"],
		"lib.ergo":   base["lib.ergo"],
		"ergo.cases": "property m.law Identity tape h1:abc 0,1\n",
	}
	dir := affectedRepo(t, base, head)
	opts := fileOptions{command: "test", target: report.Target, file: dir}
	opts.affected = affectedOptions{on: true, base: "HEAD", list: true}
	var out bytes.Buffer
	if err := affectedTestCommand(opts, &out, func(fileOptions, loaded) error { return nil }); err != nil {
		t.Fatalf("--affected: %v", err)
	}
	if !strings.Contains(out.String(), "law Identity") {
		t.Fatalf("expected ergo.cases edit to affect module obligations, got %q", out.String())
	}
}
