package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testinputs"
)

// testCLI is this package's CLI, built once per test process. Every process
// test drives the same executable bytes, so rebuilding it per test only
// repeated an identical link.
var testCLI struct {
	once        sync.Once
	environment []string
	dir         string
	path        string
	err         error
	output      []byte
}

func TestMain(m *testing.M) {
	// The CLI builds in the process's own environment, before any test
	// overrides Go variables with t.Setenv.
	testCLI.environment = os.Environ()
	// Process tests run the CLI and the programs it builds, which Go's test
	// cache sees only through observation.
	code := testinputs.Main(m)
	if testCLI.dir != "" {
		_ = os.RemoveAll(testCLI.dir)
	}
	os.Exit(code)
}

func buildTestCLI(t *testing.T) string {
	t.Helper()
	testCLI.once.Do(func() {
		testCLI.dir, testCLI.err = os.MkdirTemp("", "ef-test-cli-")
		if testCLI.err != nil {
			return
		}
		testCLI.path = filepath.Join(testCLI.dir, "ergo")
		command := exec.Command("go", "build", "-buildvcs=false", "-o", testCLI.path, ".")
		command.Env = testCLI.environment
		testCLI.output, testCLI.err = command.CombinedOutput()
	})
	if testCLI.err != nil {
		t.Fatalf("build CLI: %v\n%s", testCLI.err, testCLI.output)
	}
	return testCLI.path
}

func runTestCLI(t *testing.T, binary string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
	}
	t.Fatal(err)
	return nil, nil, -1
}

func runTestCLIInput(t *testing.T, binary, input string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
	}
	t.Fatal(err)
	return nil, nil, -1
}

func runTestCLIDir(t *testing.T, binary, directory string, input string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
	}
	t.Fatal(err)
	return nil, nil, -1
}

// sketchMissingNeeds is a source whose exported Greet uses Console
// without declaring it (an exported function spells its rows): one
// located S102 on line 2.
const sketchMissingNeeds = "func Greet() {\n\tConsole.Println(\"é\")\n}\n\nfunc main() needs Console {\n\tGreet()\n}\n"

func TestDiagnosticsCLIProcessFormatsTextJSONAndUsage(t *testing.T) {
	binary := buildTestCLI(t)
	root := t.TempDir()
	path := filepath.Join(root, "space é.ergo")
	if err := os.WriteFile(path, []byte(sketchMissingNeeds), 0600); err != nil {
		t.Fatal(err)
	}

	textOutput, textError, code := runTestCLI(t, binary, "diagnostics", path)
	if code != 1 || string(textOutput) != path+":2:2: error S102: this call requires Console, which is not in the capability row of function Greet and no with provides it\n" || string(textError) != "diagnostics failed policy\n" {
		t.Fatalf("unexpected diagnostics text: code=%d stdout=%q stderr=%q", code, textOutput, textError)
	}

	jsonOutput, jsonError, code := runTestCLI(t, binary, "diagnostics", path, "--strict", "--json")
	if code != 1 || len(jsonError) == 0 {
		t.Fatalf("diagnostics of an unchecked source did not fail policy: code=%d stderr=%q", code, jsonError)
	}
	var report struct {
		Checked      bool `json:"checked"`
		PolicyPassed bool `json:"policyPassed"`
		Strict       bool `json:"strict"`
		Source       struct {
			URI string `json:"uri"`
		} `json:"source"`
		Diagnostics []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			LSP      struct {
				Severity int `json:"severity"`
			} `json:"lsp"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(jsonOutput, &report); err != nil {
		t.Fatalf("diagnostics JSON was not parseable: %v\n%s", err, jsonOutput)
	}
	if report.Checked || report.PolicyPassed || !report.Strict || !strings.HasPrefix(report.Source.URI, "file://") || strings.Contains(report.Source.URI, " ") || !strings.Contains(report.Source.URI, "%20") || !strings.Contains(report.Source.URI, "%C3%A9") || len(report.Diagnostics) != 1 || report.Diagnostics[0].Code != "S102" || report.Diagnostics[0].Severity != "error" || report.Diagnostics[0].LSP.Severity != 1 {
		t.Fatalf("wrong diagnostics JSON: %+v", report)
	}

	clean := filepath.Join(root, "clean.ergo")
	if err := os.WriteFile(clean, []byte(sketchCleanSource), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"diagnostics", clean}, {"diagnostics", clean, "--strict", "--json"}} {
		stdout, stderr, code := runTestCLI(t, binary, args...)
		if code != 0 || len(stderr) != 0 {
			t.Fatalf("ergo %v on a clean source: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"diagnostics", path, "--unknown"}, "unknown option --unknown"},
		{[]string{"diagnostics", path, "--lint-config", "lint.json"}, "unknown option --lint-config"},
		{[]string{"diagnostics", path, "--rules", "rules.json"}, "unknown option --rules"},
		{[]string{"diagnostics", path, "--target", "llvm"}, "EF110: unsupported target llvm; the only target is go"},
		{[]string{"diagnostics", path, "--target"}, "--target requires a value"},
		{[]string{"diagnostics", path, clean}, "incorrect arguments for diagnostics"},
		{[]string{"diagnostics", "--json"}, "source file required"},
		{[]string{"diagnostics", filepath.Join(root, "main.go")}, "source file must have .ergo extension"},
		{[]string{"diagnostics", path, "-o", "out"}, "-o is only supported by build"},
	} {
		stdout, stderr, code := runTestCLI(t, binary, c.args...)
		if code != 2 || len(stdout) != 0 || !strings.Contains(string(stderr), c.want) {
			t.Fatalf("ergo %v: code=%d stdout=%q stderr=%q; want exit 2 and %q", c.args, code, stdout, stderr, c.want)
		}
	}
	_, missingError, code := runTestCLI(t, binary, "diagnostics", filepath.Join(root, "missing.ergo"), "--json")
	if code != 1 || len(missingError) == 0 {
		t.Fatalf("missing source was not reported as an operational failure: code=%d stderr=%q", code, missingError)
	}
}

// The other file commands report an invalid invocation with exit 1, and
// their options are the ones the command table names.
func TestFileCommandsRefuseOptionsTheyDoNotTake(t *testing.T) {
	binary := buildTestCLI(t)
	root := t.TempDir()
	path := filepath.Join(root, "main.ergo")
	if err := os.WriteFile(path, []byte(sketchCleanSource), 0600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"check", path, "--json"}, "--json is only supported by diagnostics"},
		{[]string{"check", path, "--strict"}, "--strict is only supported by diagnostics"},
		{[]string{"check", path, "--target", "js"}, "EF110: unsupported target js; the only target is go"},
		{[]string{"run", "-o", "out", path}, "-o is only supported by build"},
		{[]string{"build", path, "--receipt", "receipt.json"}, "--receipt is only supported by test"},
		{[]string{"build", path, "--target", "wasm"}, "EF110: unsupported target wasm; the only target is go"},
		{[]string{"build", path, "--platform", "windows/amd64"}, "EF110: unsupported target go on windows/amd64; the platforms are darwin/amd64, darwin/arm64, linux/amd64, linux/arm64"},
		{[]string{"build", path, "--platform", "linux"}, `--platform takes OS/ARCH, such as linux/arm64, not "linux"`},
		{[]string{"run", "--platform", "linux/arm64", path}, "--platform is only supported by build"},
		{[]string{"check"}, "source file required"},
		{[]string{"build", path, path}, "incorrect arguments for build"},
		{[]string{"check", filepath.Join(root, "main.txt")}, "source file must have .ergo extension"},
		{[]string{"build", path, "-o", root + string(filepath.Separator)}, "names a directory"},
		{[]string{"build", path, "-o", root}, "is a directory"},
	} {
		stdout, stderr, code := runTestCLIDir(t, binary, root, "", c.args...)
		if code != 1 || len(stdout) != 0 || !strings.Contains(string(stderr), c.want) {
			t.Fatalf("ergo %v: code=%d stdout=%q stderr=%q; want exit 1 and %q", c.args, code, stdout, stderr, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "dist")); err == nil {
		t.Fatal("a refused build wrote dist/")
	}
}

func TestFormatCLIProcessModesAndAtomicWrites(t *testing.T) {
	binary := buildTestCLI(t)
	root := t.TempDir()
	path := filepath.Join(root, "main.ergo")
	source := sketchUnformatted
	want, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The formatter preserves the original mode; set it explicitly because
	// WriteFile's permission is filtered by the process umask.
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0711); err != nil {
		t.Fatal(err)
	}

	stdinOutput, stdinError, code := runTestCLIInput(t, binary, source, "fmt", "--stdin")
	if code != 0 || string(stdinOutput) != want.Text || len(stdinError) != 0 {
		t.Fatalf("stdin formatting mismatch: code=%d stdout=%q stderr=%q want=%q", code, stdinOutput, stdinError, want.Text)
	}
	jsonOutput, jsonError, code := runTestCLI(t, binary, "fmt", "--check", "--json", path)
	if code != 1 || len(jsonError) != 0 {
		t.Fatalf("format check did not report difference: code=%d stderr=%q", code, jsonError)
	}
	var checkReport struct {
		Success bool   `json:"success"`
		Mode    string `json:"mode"`
		Files   []struct {
			Changed bool `json:"changed"`
			Written bool `json:"written"`
		} `json:"files"`
	}
	if err := json.Unmarshal(jsonOutput, &checkReport); err != nil || !checkReport.Success || checkReport.Mode != "check" || len(checkReport.Files) != 1 || !checkReport.Files[0].Changed || checkReport.Files[0].Written {
		t.Fatalf("wrong format check report: err=%v report=%+v", err, checkReport)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeOutput, writeError, code := runTestCLI(t, binary, "fmt", "--json", path)
	if code != 0 || len(writeError) != 0 {
		t.Fatalf("format write failed: code=%d stderr=%q stdout=%q", code, writeError, writeOutput)
	}
	var writeReport struct {
		FormatterVersion string `json:"formatterVersion"`
		Files            []struct {
			FormatterVersion string `json:"formatterVersion"`
			Changed          bool   `json:"changed"`
			Written          bool   `json:"written"`
		} `json:"files"`
	}
	if err := json.Unmarshal(writeOutput, &writeReport); err != nil || writeReport.FormatterVersion != sketchfront.FormatterIdentity || len(writeReport.Files) != 1 || writeReport.Files[0].FormatterVersion != sketchfront.FormatterIdentity || !writeReport.Files[0].Changed || !writeReport.Files[0].Written {
		t.Fatalf("wrong format write report: err=%v report=%+v", err, writeReport)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != want.Text {
		t.Fatalf("wrong formatted file: got=%q want=%q", actual, want.Text)
	}
	afterWrite, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterWrite.Mode().Perm() != 0711 || before.Mode().Perm() != afterWrite.Mode().Perm() {
		t.Fatalf("file mode changed: before=%#o after=%#o", before.Mode().Perm(), afterWrite.Mode().Perm())
	}
	unchangedOutput, unchangedError, code := runTestCLI(t, binary, "fmt", path)
	if code != 0 || len(unchangedOutput) != 0 || !strings.Contains(string(unchangedError), "already formatted") {
		t.Fatalf("unchanged formatting was not a no-op: code=%d stdout=%q stderr=%q", code, unchangedOutput, unchangedError)
	}
	afterNoop, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !afterNoop.ModTime().Equal(afterWrite.ModTime()) {
		t.Fatal("unchanged formatting rewrote the file")
	}

	valid := filepath.Join(root, "valid.ergo")
	invalid := filepath.Join(root, "invalid.ergo")
	validSource := sketchUnformatted
	if err := os.WriteFile(valid, []byte(validSource), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalid, []byte("func main() { @ }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mixedOutput, mixedError, code := runTestCLI(t, binary, "fmt", "--json", valid, invalid)
	if code != 2 || len(mixedError) != 0 {
		t.Fatalf("mixed syntax failure had wrong process result: code=%d stderr=%q stdout=%q", code, mixedError, mixedOutput)
	}
	var mixedReport struct {
		Success  bool `json:"success"`
		Partial  bool `json:"partial"`
		Failures []struct {
			Code string `json:"code"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(mixedOutput, &mixedReport); err != nil || mixedReport.Success || mixedReport.Partial || len(mixedReport.Failures) == 0 || mixedReport.Failures[0].Code != "EFMT_SYNTAX" {
		t.Fatalf("mixed syntax failure was not structured: err=%v report=%+v", err, mixedReport)
	}
	unchangedValid, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchangedValid) != validSource {
		t.Fatal("syntax failure partially wrote an earlier file")
	}
	humanOutput, humanError, code := runTestCLI(t, binary, "fmt", invalid)
	if code != 2 || len(humanOutput) != 0 || strings.Contains(string(humanError), "already formatted") || !strings.Contains(string(humanError), "EFMT_SYNTAX") {
		t.Fatalf("syntax failure was reported as a completed human result: code=%d stdout=%q stderr=%q", code, humanOutput, humanError)
	}
	checkHumanOutput, checkHumanError, code := runTestCLI(t, binary, "fmt", "--check", invalid)
	if code != 2 || len(checkHumanOutput) != 0 || strings.Contains(string(checkHumanError), "already formatted") || !strings.Contains(string(checkHumanError), "EFMT_SYNTAX") {
		t.Fatalf("check syntax failure was reported as formatted: code=%d stdout=%q stderr=%q", code, checkHumanOutput, checkHumanError)
	}

	_, stdinJSONError, code := runTestCLIInput(t, binary, source, "fmt", "--stdin", "--json")
	if code != 2 || len(stdinJSONError) != 0 {
		t.Fatalf("incompatible stdin invocation was not a structured JSON failure: code=%d stderr=%q", code, stdinJSONError)
	}
	symlink := filepath.Join(root, "link.ergo")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	_, symlinkError, code := runTestCLI(t, binary, "fmt", symlink)
	if code != 2 || !strings.Contains(string(symlinkError), "EFMT_SYMLINK") {
		t.Fatalf("symlink write was not rejected: code=%d stderr=%q", code, symlinkError)
	}
	limitedStdout, limitedStderr, code := runTestCLIInput(t, binary, string(formatExpansion(80000)), "fmt", "--stdin")
	if code != 2 || len(limitedStdout) != 0 || !strings.Contains(string(limitedStderr), "EFMT_OUTPUT_LIMIT") {
		t.Fatalf("stdin output bound was not enforced before replacement: code=%d stdout=%d stderr=%q", code, len(limitedStdout), limitedStderr)
	}
}

// formatExpansion is a source whose formatted text is about ten times
// longer: each one-line statement inside 64 nested ifs gets its own line
// and 65 tabs.
func formatExpansion(statements int) []byte {
	var source strings.Builder
	source.WriteString("func f() {")
	source.WriteString(strings.Repeat("if c { ", 64))
	source.WriteString(strings.Repeat("x = 1; ", statements))
	source.WriteString(strings.Repeat(" }", 64))
	source.WriteString("}\n")
	return []byte(source.String())
}

func TestFormatCLIResolvesOSPathsAndPreservesFilesystemPolicy(t *testing.T) {
	binary := buildTestCLI(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deep", "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "deep", "inner"), filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	source := sketchUnformatted
	deepPath := filepath.Join(root, "deep", "e.ergo")
	rootPath := filepath.Join(root, "e.ergo")
	if err := os.WriteFile(deepPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	output, stderr, code := runTestCLIDir(t, binary, root, "", "fmt", "--json", "jump/../e.ergo", "./e.ergo")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("OS path resolution failed: code=%d stderr=%q stdout=%q", code, stderr, output)
	}
	var report formatReport
	if err := json.Unmarshal(output, &report); err != nil || len(report.Files) != 2 || !report.Files[0].Written || !report.Files[1].Written {
		t.Fatalf("distinct OS paths were falsely deduplicated: err=%v report=%+v", err, report)
	}
	want, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{deepPath, rootPath} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want.Text {
			t.Fatalf("requested OS path was not formatted: path=%s err=%v got=%q", path, err, got)
		}
	}

	resolvedParentTarget := filepath.Join(root, "deep", "parent-target.ergo")
	lexicalParentCounterpart := filepath.Join(root, "parent-target.ergo")
	lexicalBytes := []byte("// lexical\n")
	if err := os.WriteFile(resolvedParentTarget, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lexicalParentCounterpart, lexicalBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0555); err != nil {
		t.Fatal(err)
	}
	output, stderr, code = runTestCLIDir(t, binary, root, "", "fmt", "--json", "jump/../parent-target.ergo")
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("resolved parent replacement failed with a read-only lexical parent: code=%d stderr=%q stdout=%q", code, stderr, output)
	}
	var resolvedParentReport formatReport
	if err := json.Unmarshal(output, &resolvedParentReport); err != nil || len(resolvedParentReport.Files) != 1 || !resolvedParentReport.Files[0].Written || resolvedParentReport.Files[0].Path != "jump/../parent-target.ergo" {
		t.Fatalf("resolved parent replacement lost requested identity: err=%v report=%+v", err, resolvedParentReport)
	}
	resolvedBytes, err := os.ReadFile(resolvedParentTarget)
	if err != nil || string(resolvedBytes) != want.Text {
		t.Fatalf("resolved parent target was not formatted: err=%v bytes=%q", err, resolvedBytes)
	}
	if got, err := os.ReadFile(lexicalParentCounterpart); err != nil || !bytes.Equal(got, lexicalBytes) {
		t.Fatalf("lexical counterpart was changed: err=%v bytes=%q", err, got)
	}

	hardlinkOne := filepath.Join(root, "hard-one.ergo")
	hardlinkTwo := filepath.Join(root, "hard-two.ergo")
	if err := os.WriteFile(hardlinkOne, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardlinkOne, hardlinkTwo); err != nil {
		t.Fatal(err)
	}
	output, stderr, code = runTestCLIDir(t, binary, root, "", "fmt", "--json", "hard-one.ergo")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("single hardlink formatting failed: code=%d stderr=%q stdout=%q", code, stderr, output)
	}
	firstBytes, err := os.ReadFile(hardlinkOne)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(hardlinkTwo)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != want.Text || string(secondBytes) != source {
		t.Fatalf("single hardlink policy changed unexpectedly: first=%q second=%q", firstBytes, secondBytes)
	}
	firstInfo, err := os.Stat(hardlinkOne)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(hardlinkTwo)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(firstInfo, secondInfo) {
		t.Fatal("atomic replacement did not split the selected hardlink entry")
	}

	readOnly := filepath.Join(root, "readonly.ergo")
	if err := os.WriteFile(readOnly, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readOnly, 0444); err != nil {
		t.Fatal(err)
	}
	output, stderr, code = runTestCLIDir(t, binary, root, "", "fmt", "--json", "readonly.ergo")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("read-only file in writable directory was not replaceable: code=%d stderr=%q stdout=%q", code, stderr, output)
	}
	readOnlyBytes, err := os.ReadFile(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	readOnlyInfo, err := os.Stat(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if string(readOnlyBytes) != want.Text || readOnlyInfo.Mode().Perm() != 0444 {
		t.Fatalf("read-only replacement did not preserve bytes/mode: mode=%#o bytes=%q", readOnlyInfo.Mode().Perm(), readOnlyBytes)
	}

	specialMode := filepath.Join(root, "special-mode.ergo")
	if err := os.WriteFile(specialMode, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(specialMode, 0755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if _, _, code = runTestCLIDir(t, binary, root, "", "fmt", "special-mode.ergo"); code != 0 {
		t.Fatalf("special mode file formatting failed: code=%d", code)
	}
	specialInfo, err := os.Stat(specialMode)
	if err != nil {
		t.Fatal(err)
	}
	if specialInfo.Mode().Perm() != 0755 || specialInfo.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("special mode bits were not preserved: mode=%#o", specialInfo.Mode())
	}
}

func TestFormatApplySeamReportsStaleAndEarlierWrites(t *testing.T) {
	root := t.TempDir()
	source := sketchUnformatted
	firstPath := filepath.Join(root, "first.ergo")
	secondPath := filepath.Join(root, "second.ergo")
	if err := os.WriteFile(firstPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	report := formatReport{Success: true, Files: []formatFileReport{{Path: "first.ergo", Changed: true, Completed: true}, {Path: "second.ergo", Changed: true, Completed: true}}}
	plans := []formatPlan{
		{path: firstPath, displayPath: "first.ergo", info: firstInfo, source: []byte(source), result: result, reportAt: 0},
		{path: secondPath, displayPath: "second.ergo", info: secondInfo, source: []byte(source), result: result, reportAt: 1},
	}
	calls := 0
	err = applyFormatPlans(plans, &report, func(plan formatPlan) error {
		calls++
		if calls == 2 {
			before, err := os.Stat(plan.path)
			if err != nil {
				return err
			}
			edited := "// edited\n"
			if err := os.WriteFile(plan.path, []byte(edited), 0600); err != nil {
				return err
			}
			if err := os.Chtimes(plan.path, before.ModTime(), before.ModTime()); err != nil {
				return err
			}
		}
		return replaceFormattedFile(plan)
	})
	if err == nil || !report.Partial || report.Success || !report.Files[0].Written || report.Files[1].Written || len(report.Failures) != 1 || report.Failures[0].Code != "EFMT_STALE" || report.Failures[0].Path != "second.ergo" {
		t.Fatalf("partial apply report lost causal state: err=%v report=%+v", err, report)
	}
	firstBytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != result.Text || string(secondBytes) != "// edited\n" {
		t.Fatalf("real partial write did not preserve disk state: first=%q second=%q", firstBytes, secondBytes)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "ergo-format-") {
			t.Fatalf("temporary file leaked: %s", entry.Name())
		}
	}
}

func TestReplaceFormattedFileCleansTempAfterActualFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.ergo")
	source := sketchUnformatted
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan := formatPlan{path: path, displayPath: "main.ergo", info: info, source: []byte(source), result: result}
	err = replaceFormattedFileWithHook(plan, func(string) error { return os.ErrPermission })
	var failure *formatAdapterError
	if err == nil || !errors.As(err, &failure) || failure.code != "EFMT_WRITE" || failure.path != plan.displayPath {
		t.Fatalf("actual replacement failure was not reported: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != source {
		t.Fatalf("failed replacement changed source: err=%v bytes=%q", err, got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "ergo-format-") {
			t.Fatalf("temporary file leaked after failure: %s", entry.Name())
		}
	}
}

func TestReplaceFormattedFileRejectsSameBytesDifferentInode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.ergo")
	source := sketchUnformatted
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan := formatPlan{path: path, parentPath: root, displayPath: "main.ergo", info: info, source: []byte(source), result: result}
	replacement := filepath.Join(root, "replacement.ergo")
	if err := os.WriteFile(replacement, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	err = replaceFormattedFile(plan)
	var failure *formatAdapterError
	if err == nil || !errors.As(err, &failure) || failure.code != "EFMT_STALE" || failure.path != plan.displayPath {
		t.Fatalf("same-bytes inode replacement was accepted without the requested identity: %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != source {
		t.Fatalf("same-bytes inode replacement changed disk state: err=%v bytes=%q", readErr, got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "ergo-format-") {
			t.Fatalf("temporary file leaked after same-bytes inode rejection: %s", entry.Name())
		}
	}
}

func TestReplaceFormattedFileUsesResolvedParentForRawPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "deep", "inner"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "deep", "inner"), filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	source := sketchUnformatted
	actualPath := filepath.Join(root, "deep", "raw-parent.ergo")
	if err := os.WriteFile(actualPath, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	rawPath := root + string(filepath.Separator) + "jump" + string(filepath.Separator) + ".." + string(filepath.Separator) + "raw-parent.ergo"
	info, err := os.Stat(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sketchfront.Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	plan := formatPlan{path: rawPath, parentPath: filepath.Dir(resolved), displayPath: "jump/../raw-parent.ergo", info: info, source: []byte(source), result: result}
	var temporaryParent string
	err = replaceFormattedFileWithHook(plan, func(tempPath string) error {
		temporaryParent = filepath.Dir(tempPath)
		return os.ErrPermission
	})
	var failure *formatAdapterError
	if err == nil || !errors.As(err, &failure) || failure.code != "EFMT_WRITE" || failure.path != plan.displayPath {
		t.Fatalf("resolved-parent hook failure lost write identity: %v", err)
	}
	parentInfo, err := os.Stat(filepath.Dir(resolved))
	if err != nil {
		t.Fatal(err)
	}
	temporaryInfo, err := os.Stat(temporaryParent)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(parentInfo, temporaryInfo) {
		t.Fatalf("temporary was created in lexical rather than resolved parent: temp=%s resolved=%s", temporaryParent, filepath.Dir(resolved))
	}
}
