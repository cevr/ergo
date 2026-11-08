package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
)

// The sketch process tests drive the CLI's one front end on sketch sources
// in directories with no ergo.json anywhere above them.

// TestSketchExampleBuildsAndRuns: the repository's sketch example builds to
// an executable, to -o or to dist/main, and runs, printing expected.out.
func TestSketchExampleBuildsAndRuns(t *testing.T) {
	binary := buildTestCLI(t)
	workspace := smokeWorkspace(t, "examples/editions/sketch", "go.mod")
	dir := filepath.Join("examples", "editions", "sketch")
	source := filepath.Join(dir, "main.ergo")
	expected := smokeRead(t, filepath.Join(workspace, dir, "expected.out"))
	for _, c := range []struct {
		args   []string
		output string
	}{
		{[]string{"build", source, "-o", filepath.Join("out", "hello")}, filepath.Join("out", "hello")},
		{[]string{"build", source}, filepath.Join("dist", "main")},
	} {
		stdout, stderr, code := runSmokeCLI(t, binary, workspace, "", c.args...)
		if code != 0 || string(stdout) != c.output+"\n" {
			t.Fatalf("ergo %v: exit %d stdout %q\n%s", c.args, code, stdout, stderr)
		}
		got, err := exec.Command(filepath.Join(workspace, c.output)).Output()
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("%s printed %q (%v), want %q", c.output, got, err, expected)
		}
	}
	stdout, stderr, code := runSmokeCLI(t, binary, workspace, "", "run", source)
	if code != 0 || !bytes.Equal(stdout, expected) {
		t.Fatalf("ergo run: exit %d stdout %q, want %q\n%s", code, stdout, expected, stderr)
	}
}

// diagnosticKey is one finding as both envelopes state it: the code and
// the UTF-8 byte span.
type diagnosticKey struct {
	Code string
	Span report.Span
}

func envelopeKeys(t *testing.T, data []byte) []diagnosticKey {
	t.Helper()
	var decoded struct {
		Diagnostics []diagnosticKey `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("envelope %s: %v", data, err)
	}
	return decoded.Diagnostics
}

// TestCheckAndDiagnosticsReportLocatedSketchCodes: check and diagnostics
// agree on every finding of a source, every finding has a sketch code and a
// location, and source in the retired language is refused with a sketch syntax code.
func TestCheckAndDiagnosticsReportLocatedSketchCodes(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	sketchCode := regexp.MustCompile(`^S[0-9]{3}$`)
	for _, file := range []struct {
		name, text string
		clean      bool
	}{
		{"legacy-syntax.ergo", legacyCleanSource, false},
		{"unknown-name.ergo", "func main() needs Console {\n\tConsole.Println(\"🌍 ü\" + mïssing)\n}\n", false},
		{"lexer.ergo", "// ünïcode\nfunc main() needs Console { Console.Println(\"日本\") $ }\n", false},
		{"missing-needs.ergo", sketchMissingNeeds, false},
		{"clean.ergo", sketchCleanSource, true},
	} {
		path := smokeWrite(t, root, file.name, file.text)
		checkOut, checkErr, checkCode := runSmokeCLI(t, binary, root, "", "check", path)
		reportOut, reportErr, reportCode := runSmokeCLI(t, binary, root, "", "diagnostics", path, "--json")
		if (checkCode == 0) != file.clean || (reportCode == 0) != file.clean || checkCode > 1 || reportCode > 1 {
			t.Fatalf("%s: check exit %d, diagnostics exit %d\n%s%s%s%s", file.name, checkCode, reportCode, checkOut, checkErr, reportOut, reportErr)
		}
		envelope, findings := smokeJSON(t, checkOut), smokeJSON(t, reportOut)
		if envelope["checked"] != file.clean || findings["checked"] != file.clean || findings["policyPassed"] != file.clean {
			t.Fatalf("%s: checked %v / %v, policy %v", file.name, envelope["checked"], findings["checked"], findings["policyPassed"])
		}
		if envelope["revision"] != findings["revision"] {
			t.Fatalf("%s: revisions differ", file.name)
		}
		fromCheck, fromReport := envelopeKeys(t, checkOut), envelopeKeys(t, reportOut)
		if !slices.Equal(fromCheck, fromReport) {
			t.Fatalf("%s: check %v, diagnostics %v", file.name, fromCheck, fromReport)
		}
		if file.clean != (len(fromCheck) == 0) {
			t.Fatalf("%s: findings %v, want clean=%v", file.name, fromCheck, file.clean)
		}
		for index, key := range fromCheck {
			if !sketchCode.MatchString(key.Code) || key.Span == (report.Span{}) {
				t.Fatalf("%s: finding %v has a code outside the sketch path or no location", file.name, key)
			}
			lsp := findings["diagnostics"].([]any)[index].(map[string]any)["lsp"].(map[string]any)
			if lsp["range"] == nil {
				t.Fatalf("%s: finding %v has no LSP range", file.name, key)
			}
		}
	}
}

// TestAManifestSelectsNothing: an ergo.json beside a source is never read.
// Whatever it names, check and run behave as without it.
func TestAManifestSelectsNothing(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	for _, manifest := range []string{`{"edition":"legacy"}`, `{"edition":"2026"}`, `{"edition":"bogus"}`, `{"edition":`, "not json"} {
		dir := filepath.Join(root, strings.NewReplacer(`"`, "", "{", "", "}", "", ":", "-", " ", "-").Replace(manifest))
		smokeWrite(t, dir, "ergo.json", manifest)
		smokeWrite(t, dir, "main.ergo", sketchCleanSource)
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", "run", "main.ergo")
		if code != 0 || string(stdout) != "ok\n" {
			t.Fatalf("%s: run exit %d stdout %q\n%s", manifest, code, stdout, stderr)
		}
		stdout, stderr, code = runSmokeCLI(t, binary, dir, "", "check", "main.ergo")
		if code != 0 || bytes.Contains(stdout, []byte("edition")) || regexp.MustCompile(`EF14[4-6]`).Match(append(stdout, stderr...)) {
			t.Fatalf("%s: check exit %d stdout %s stderr %s", manifest, code, stdout, stderr)
		}
	}
}

// TestCommandsReadTheFileALinkNames: check, diagnostics and build read the
// file a link reaches, and the diagnostics URI names the link as spelled.
func TestCommandsReadTheFileALinkNames(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	target := smokeWrite(t, root, "new/main.ergo", sketchCleanSource)
	link := filepath.Join(root, "link.ergo")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSmokeCLI(t, binary, root, "", "check", link)
	if code != 0 {
		t.Fatalf("ergo check through the link: exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
	}
	direct, _, _ := runSmokeCLI(t, binary, root, "", "check", target)
	if smokeJSON(t, stdout)["revision"] != smokeJSON(t, direct)["revision"] {
		t.Fatal("the link and its target check to different revisions")
	}
	stdout, _, code = runSmokeCLI(t, binary, root, "", "diagnostics", link, "--json")
	uri, _ := report.FileURI(link)
	if source := smokeJSON(t, stdout)["source"].(map[string]any); code != 0 || source["uri"] != uri {
		t.Fatalf("diagnostics through the link: exit %d source %v, want %s", code, source, uri)
	}
	stdout, stderr, code = runSmokeCLI(t, binary, root, "", "run", link)
	if code != 0 || string(stdout) != "ok\n" {
		t.Fatalf("run through the link: exit %d stdout %q\n%s", code, stdout, stderr)
	}
}

// TestALargeSourceChecksAndFmtRefusesIt: check reads a source of any size;
// fmt refuses one over its 2 MiB per-file bound and writes nothing.
func TestALargeSourceChecksAndFmtRefusesIt(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	large := "//" + strings.Repeat("x", maxFormatInputBytes) + "\n" + sketchCleanSource
	path := smokeWrite(t, root, "main.ergo", large)
	if stdout, stderr, code := runSmokeCLI(t, binary, root, "", "check", path); code != 0 {
		t.Fatalf("check of a large source: exit %d\n%s\n%s", code, firstLines(stdout), stderr)
	}
	for _, args := range [][]string{{"fmt", "--check", path}, {"fmt", path}} {
		_, stderr, code := runSmokeCLI(t, binary, root, "", args...)
		if code != 2 || !strings.Contains(string(stderr), "EFMT_INPUT_LIMIT") {
			t.Fatalf("ergo %v: exit %d stderr %q", args, code, stderr)
		}
	}
	if !bytes.Equal(smokeRead(t, path), []byte(large)) {
		t.Fatal("the source changed")
	}
}

// TestBuildAndRunRefuseASourceThatDoesNotCheck: the check envelope, exit 1,
// and nothing built.
func TestBuildAndRunRefuseASourceThatDoesNotCheck(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	smokeWrite(t, root, "bad.ergo", sketchMissingNeeds)
	for _, args := range [][]string{{"run", "bad.ergo"}, {"build", "bad.ergo"}, {"build", "bad.ergo", "-o", "out/bad"}} {
		stdout, stderr, code := runSmokeCLI(t, binary, root, "", args...)
		envelope := smokeJSON(t, stdout)
		if code != 1 || envelope["checked"] != false || string(stderr) != "check failed\n" {
			t.Fatalf("ergo %v: exit %d stdout %s stderr %q", args, code, stdout, stderr)
		}
		producerSnapshot(t, envelope)
	}
	for _, written := range []string{"dist", "out"} {
		if _, err := os.Stat(filepath.Join(root, written)); err == nil {
			t.Fatalf("a refused build wrote %s", written)
		}
	}
}
