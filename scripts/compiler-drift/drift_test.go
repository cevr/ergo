package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runGit(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", argv, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIsGoCompilerRuleFileExclusions(t *testing.T) {
	for _, tc := range []struct {
		file   string
		subsys string
		want   bool
	}{
		{"internal/sketch/check/body.go", "check", true},
		{"internal/sketch/lower/calls.go", "lower", true},
		{"internal/sketch/syntax/ast.go", "syntax", true},
		{"internal/sketch/format/fmt.go", "format", true},
		{"internal/sketch/check/body_test.go", "", false},
		{"internal/sketch/syntax/codec_gen.go", "", false},
		{"internal/sketch/check/testdata/fixture.go", "", false},
		{"internal/sketch/lower/snapshot/snap.go", "", false},
		{"internal/sketch/eval/eval.go", "", false},
		{"compiler/check/check.ergo", "", false},
	} {
		sub, ok := isGoCompilerRuleFile(tc.file)
		if ok != tc.want || sub != tc.subsys {
			t.Errorf("isGoCompilerRuleFile(%q) = (%q, %v), want (%q, %v)", tc.file, sub, ok, tc.subsys, tc.want)
		}
	}
}

func TestValidateExemptionsAndExpiry(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"internal/sketch/check/valid.go":   "package check\n",
		"internal/sketch/check/expired.go": "package check\n",
		"internal/sketch/lower/empty.go":   "package lower\n",
	})
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	reg := &ExemptionRegistry{
		Exemptions: []DriftExemption{
			{File: "internal/sketch/check/valid.go", Reason: "Go-only fast path", Owner: "compiler", Expires: "2027-01-01"},
			{File: "internal/sketch/check/expired.go", Reason: "temporary", Owner: "compiler", Expires: "2026-11-01"},
			{File: "internal/sketch/lower/empty.go", Reason: "   ", Owner: "compiler"},
			{File: "internal/sketch/syntax/missing.go", Reason: "gone", Owner: "compiler"},
		},
	}

	errs := validateExemptions(root, reg, now)
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"expired.go: exemption expired", "empty.go: reason must not be empty", "missing.go: exempted file does not exist"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validateExemptions missing %q; got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "valid.go") {
		t.Errorf("valid exemption unexpectedly reported error:\n%s", joined)
	}

	// Active check: valid passes, expired and empty-reason are refused.
	if ok, _ := activeExemption(reg, "internal/sketch/check/valid.go", now); !ok {
		t.Errorf("expected valid.go to be actively exempted")
	}
	if ok, note := activeExemption(reg, "internal/sketch/check/expired.go", now); ok || !strings.Contains(note, "expired") {
		t.Errorf("expected expired.go exemption to be rejected as expired, got ok=%v note=%q", ok, note)
	}
	if ok, _ := activeExemption(reg, "internal/sketch/lower/empty.go", now); ok {
		t.Errorf("expected empty-reason exemption to be rejected")
	}
}

func TestCheckDriftEndToEndGitRepo(t *testing.T) {
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{
		"internal/sketch/check/body.go":                         "package check\nfunc f() int { return 1 }\n",
		"internal/sketch/check/body_test.go":                    "package check\n",
		"internal/sketch/lower/calls.go":                        "package lower\nfunc g() int { return 1 }\n",
		"internal/sketch/syntax/ast.go":                         "package syntax\nfunc h() int { return 1 }\n",
		"internal/sketch/syntax/codec_gen.go":                   "package syntax\n",
		"compiler/check/body.ergo":                              "func f() int { 1 }\n",
		"compiler/lower/calls.ergo":                             "func g() int { 1 }\n",
		"compiler/syntax/ast.ergo":                              "func h() int { 1 }\n",
		"internal/selfhostcheck/testdata/drift-exemptions.json": `{"about":"test","exemptions":[]}`,
	})
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "base")
	base := runGit(t, repo, "rev-parse", "HEAD")

	// 1. Editing only *_test.go and *_gen.go -> PASS (exit 0).
	writeFiles(t, repo, map[string]string{
		"internal/sketch/check/body_test.go":  "package check\n// edited\n",
		"internal/sketch/syntax/codec_gen.go": "package syntax\n// regenerated\n",
	})
	var out, errBuf bytes.Buffer
	if code := run([]string{"-root", repo, "-check-drift", "-base", base}, &out, &errBuf); code != 0 {
		t.Fatalf("test/gen-only edit exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}

	// 2. Editing Go rule file internal/sketch/check/body.go without compiler/check/ edit -> FAIL (exit 1).
	writeFiles(t, repo, map[string]string{
		"internal/sketch/check/body.go": "package check\nfunc f() int { return 2 }\n",
	})
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-root", repo, "-check-drift", "-base", base}, &out, &errBuf); code != 1 {
		t.Fatalf("unmirrored Go edit exit %d, want 1\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "internal/sketch/check/body.go") || !strings.Contains(out.String(), "compiler/check/") {
		t.Errorf("expected drift failure naming body.go and compiler/check/, got:\n%s", out.String())
	}

	// 3. Mirroring in compiler/check/body.ergo -> PASS (exit 0).
	writeFiles(t, repo, map[string]string{
		"compiler/check/body.ergo": "func f() int { 2 }\n",
	})
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-root", repo, "-check-drift", "-base", base, "-json"}, &out, &errBuf); code != 0 {
		t.Fatalf("mirrored edit exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	var rep DriftReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("decode JSON drift report: %v\n%s", err, out.String())
	}
	if rep.MirroredFiles != 1 || len(rep.Violations) != 0 {
		t.Errorf("expected 1 mirrored file and 0 violations, got %+v", rep)
	}

	// 4. Editing internal/sketch/lower/calls.go covered by a valid exemption -> PASS (exit 0).
	writeFiles(t, repo, map[string]string{
		"internal/sketch/lower/calls.go": "package lower\nfunc g() int { return 2 }\n",
		"internal/selfhostcheck/testdata/drift-exemptions.json": `{
  "about": "test",
  "exemptions": [
    {"file": "internal/sketch/lower/calls.go", "reason": "Go-only emitter bridge", "owner": "lower-team"}
  ]
}`,
	})
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-root", repo, "-check-drift", "-check-exemptions", "-base", base}, &out, &errBuf); code != 0 {
		t.Fatalf("exempted edit exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "1 exempted") {
		t.Errorf("expected 1 exempted in output, got:\n%s", out.String())
	}

	// 5. Empty reason in exemption -> -check-exemptions fails (exit 1) AND -check-drift fails (exit 1).
	writeFiles(t, repo, map[string]string{
		"internal/selfhostcheck/testdata/drift-exemptions.json": `{
  "about": "test",
  "exemptions": [
    {"file": "internal/sketch/lower/calls.go", "reason": "", "owner": "lower-team"}
  ]
}`,
	})
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-root", repo, "-check-exemptions"}, &out, &errBuf); code != 1 {
		t.Fatalf("empty-reason -check-exemptions exit %d, want 1\nstdout:\n%s", code, out.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-root", repo, "-check-drift", "-base", base}, &out, &errBuf); code != 1 {
		t.Fatalf("empty-reason -check-drift exit %d, want 1\nstdout:\n%s", code, out.String())
	}
}
