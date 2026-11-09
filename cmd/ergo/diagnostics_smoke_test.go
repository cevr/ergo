package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"ergo.local/prototype/internal/report"
)

// The diagnostics smokes hold the CLI's diagnostic envelopes to their
// positions, bounds and document identity on sketch sources.

// diagnosticsSmokeCLI runs `ergo diagnostics --json`, whose exit status must
// state the report's policy verdict.
func diagnosticsSmokeCLI(t *testing.T, binary, path string, strict bool) map[string]any {
	t.Helper()
	arguments := []string{"diagnostics", path, "--json"}
	if strict {
		arguments = append(arguments, "--strict")
	}
	stdout, stderr, code := runSmokeCLI(t, binary, filepath.Dir(path), "", arguments...)
	if code != 0 && code != 1 {
		t.Fatalf("diagnostics exit %d: %s", code, stderr)
	}
	findings := smokeJSON(t, stdout)
	if want := map[bool]int{true: 0, false: 1}[findings["policyPassed"] == true]; code != want {
		t.Fatalf("exit %d does not state the policy verdict: %v", code, findings)
	}
	return findings
}

// diagnosticsSmokeEditorPosition is an independent protocol oracle: decode
// the known byte prefix, normalize editor line endings, then count UTF-16
// code units on its final line.
func diagnosticsSmokeEditorPosition(t *testing.T, source string, byteOffset int) map[string]any {
	t.Helper()
	prefix := source[:byteOffset]
	if !utf8.ValidString(prefix) {
		t.Fatalf("offset %d splits a character of %q", byteOffset, source)
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(prefix, "\r\n", "\n"), "\r", "\n"), "\n")
	last := lines[len(lines)-1]
	return map[string]any{"line": float64(len(lines) - 1), "character": float64(len(utf16.Encode([]rune(last))))}
}

type diagnosticsSmokeFinding struct {
	code   string
	anchor string // "" anchors at the end of input
	length int
}

// TestDiagnosticsSmokePositions: every finding's UTF-8 span starts at its
// anchor, and its LSP range is what an editor computes for that span, across
// astral characters, CRLF, a lone CR and the end of input; the check
// envelope carries the same spans.
func TestDiagnosticsSmokePositions(t *testing.T) {
	binary := buildTestCLI(t)
	for _, fixture := range []struct {
		name     string
		source   string
		expected []diagnosticsSmokeFinding
	}{
		{"unicode.ergo", "func main() { \"𐐀é\" @ }\n", []diagnosticsSmokeFinding{{"S001", "@", 1}}},
		{"crlf.ergo", "// comment\r\nfunc main() {}\r\n@", []diagnosticsSmokeFinding{{"S001", "@", 1}}},
		{"eof.ergo", "// comment\r\nfunc main() {\r\n", []diagnosticsSmokeFinding{{"S001", "", 0}}},
		{"cr-code.ergo", "func main() {}\rfunc g() {}\n", []diagnosticsSmokeFinding{{"S001", "func g", 4}}},
		{"checker.ergo", "func main() needs Console {\n\tConsole.Println(\"𐐀é\" + nope)\n}\n", []diagnosticsSmokeFinding{{"S104", "nope", 4}}},
		{"clean-crlf.ergo", strings.ReplaceAll(sketchCleanSource, "\n", "\r\n"), nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			directory := smokeDir(t)
			source := fixture.source
			path := smokeWrite(t, directory, fixture.name, source)
			findingsReport := diagnosticsSmokeCLI(t, binary, path, false)
			producerSnapshot(t, findingsReport)
			digest := sha256.Sum256([]byte(source))
			if findingsReport["revision"] != hex.EncodeToString(digest[:]) || findingsReport["schemaVersion"] != float64(report.DiagnosticReportSchemaVersion) {
				t.Fatalf("revision %v schema %v", findingsReport["revision"], findingsReport["schemaVersion"])
			}
			uri, _ := report.FileURI(path)
			if want := map[string]any{"uri": uri, "origin": "disk"}; !reflect.DeepEqual(findingsReport["source"], want) {
				t.Fatalf("source = %v, want %v", findingsReport["source"], want)
			}
			findings, _ := findingsReport["diagnostics"].([]any)
			if len(findings) != len(fixture.expected) || findingsReport["checked"] != (len(fixture.expected) == 0) {
				t.Fatalf("findings = %v", findings)
			}
			stdout, _, _ := runSmokeCLI(t, binary, directory, "", "check", path)
			envelope := smokeJSON(t, stdout)
			listed, _ := envelope["diagnostics"].([]any)
			if len(listed) != len(findings) || envelope["revision"] != findingsReport["revision"] {
				t.Fatalf("check envelope %v disagrees with the report", envelope)
			}
			for index, expected := range fixture.expected {
				finding := findings[index].(map[string]any)
				offset := len(source)
				if expected.anchor != "" {
					offset = strings.Index(source, expected.anchor)
				}
				if finding["code"] != expected.code || finding["severity"] != "error" || finding["origin"] != "compiler" || finding["locationAvailable"] != true {
					t.Fatalf("finding %v, want %+v", finding, expected)
				}
				span, _ := finding["span"].(map[string]any)
				if span["offset"] != float64(offset) || span["length"] != float64(expected.length) {
					t.Fatalf("span %v, want offset %d length %d", span, offset, expected.length)
				}
				if !reflect.DeepEqual(listed[index].(map[string]any)["span"], span) {
					t.Fatalf("check span %v, report span %v", listed[index], span)
				}
				lsp, _ := finding["lsp"].(map[string]any)
				wantRange := map[string]any{
					"start": diagnosticsSmokeEditorPosition(t, source, offset),
					"end":   diagnosticsSmokeEditorPosition(t, source, offset+expected.length),
				}
				if lsp["severity"] != float64(1) || !reflect.DeepEqual(lsp["range"], wantRange) {
					t.Fatalf("LSP %v, want severity 1 and range %v", lsp, wantRange)
				}
			}
			strict := diagnosticsSmokeCLI(t, binary, path, true)
			if strict["strict"] != true || strict["policyPassed"] != findingsReport["policyPassed"] || !reflect.DeepEqual(strict["diagnostics"], findingsReport["diagnostics"]) {
				t.Fatalf("strict changed the findings or the verdict: %v", strict)
			}
		})
	}
}

// TestDiagnosticsSmokeBounds: the check envelope lists at most 100
// diagnostics and says when it dropped the rest, while the report keeps the
// complete set; a directory and a FIFO are refused at once.
func TestDiagnosticsSmokeBounds(t *testing.T) {
	binary := buildTestCLI(t)
	directory := smokeDir(t)
	for _, count := range []int{100, 101} {
		// Each declaration of f after the first is one S108.
		name := "findings-" + strconv.Itoa(count) + ".ergo"
		path := smokeWrite(t, directory, name, "func main() {}\n"+strings.Repeat("func f() {}\n", count+1))
		stdout, _, code := runSmokeCLI(t, binary, directory, "", "check", path)
		envelope := smokeJSON(t, stdout)
		listed, _ := envelope["diagnostics"].([]any)
		if code != 1 || len(listed) != 100 || envelope["diagnosticsTruncated"] != (count > 100) {
			t.Fatalf("%d findings: check listed %d, truncated %v", count, len(listed), envelope["diagnosticsTruncated"])
		}
		findings := diagnosticsSmokeCLI(t, binary, path, false)
		totals, _ := findings["totalCounts"].(map[string]any)
		if totals["errors"] != float64(count) || findings["returnedCount"] != float64(count) || findings["truncated"] != false {
			t.Fatalf("%d findings: report %v", count, totals)
		}
	}
	refused := []string{filepath.Join(directory, "directory.ergo")}
	if err := os.Mkdir(refused[0], 0o755); err != nil {
		t.Fatal(err)
	}
	if diagnosticsSmokeMkfifo(filepath.Join(directory, "fifo.ergo")) == nil {
		refused = append(refused, filepath.Join(directory, "fifo.ergo"))
	}
	for _, path := range refused {
		for _, command := range []string{"check", "diagnostics", "build", "run"} {
			// An open of the FIFO blocks for good, so the test's deadline
			// tells a refusal from an open; a 5 s bound failed only a
			// loaded host (fu-gate-flakes).
			stdout, stderr, code := runSmokeCLI(t, binary, directory, "", command, path)
			if code != 1 || len(stdout) != 0 || !strings.Contains(string(stderr), "source must be a regular file") {
				t.Fatalf("ergo %s %s: exit %d stdout %q stderr %q", command, filepath.Base(path), code, stdout, stderr)
			}
		}
	}
}

// TestDiagnosticsSmokeIdentity: a source path is made absolute lexically
// once, so `link/..` names the directory holding the link, for the
// document's identity and its bytes alike; a link to a file keeps its own
// URI and reports the bytes of the file it reaches.
func TestDiagnosticsSmokeIdentity(t *testing.T) {
	binary := buildTestCLI(t)
	t.Run("lexical-normalization", func(t *testing.T) {
		t.Parallel()
		dots := filepath.Join(smokeDir(t), "dots")
		if err := os.MkdirAll(filepath.Join(dots, "real", "inner"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("real", "inner"), filepath.Join(dots, "link")); err != nil {
			t.Fatal(err)
		}
		lexical := smokeWrite(t, dots, "b.ergo", sketchCleanSource)
		smokeWrite(t, filepath.Join(dots, "real"), "b.ergo", sketchMissingNeeds)
		expected := diagnosticsSmokeCLI(t, binary, lexical, false)
		// Join would clean the path; the request must keep link/.. literally.
		requested := dots + string(filepath.Separator) + "link" + string(filepath.Separator) + ".." + string(filepath.Separator) + "b.ergo"
		assertReportParity(t, diagnosticsSmokeCLI(t, binary, requested, false), expected, report.DiagnosticReportSchemaVersion)
	})
	t.Run("symlink-uri", func(t *testing.T) {
		t.Parallel()
		directory := smokeDir(t)
		original := smokeWrite(t, directory, "one.ergo", sketchMissingNeeds)
		smokeWrite(t, directory, "two.ergo", sketchCleanSource)
		link := filepath.Join(directory, "selected.ergo")
		if err := os.Symlink(filepath.Base(original), link); err != nil {
			t.Fatal(err)
		}
		baseline := diagnosticsSmokeCLI(t, binary, original, false)
		selected := diagnosticsSmokeCLI(t, binary, link, false)
		uri, _ := report.FileURI(link)
		if source, _ := selected["source"].(map[string]any); source["uri"] != uri {
			t.Fatalf("source = %v, want %s", selected["source"], uri)
		}
		if selected["revision"] != baseline["revision"] || !reflect.DeepEqual(selected["diagnostics"], baseline["diagnostics"]) {
			t.Fatalf("report %v does not match baseline %v", selected, baseline)
		}
	})
}
