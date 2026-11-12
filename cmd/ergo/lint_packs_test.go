package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// examplePack is a copy of examples/lintpack in a temporary directory: its
// manifest, configurations and fixtures, and, when built, the policy-lint
// program at the manifest's bin/policy-lint.
func examplePack(t *testing.T) string {
	t.Helper()
	dir := physicalTempDir(t)
	src := filepath.Join("..", "..", "examples", "lintpack")
	for _, name := range []string{"policy.json", "provider.lint.json", "failure.lint.json",
		"testdata/provider_boundary.ergo", "testdata/provider_boundary.lint.json",
		"testdata/forbidden_failure.ergo", "testdata/forbidden_failure.lint.json"} {
		data, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		smokeWrite(t, dir, name, string(data))
	}
	return dir
}

// buildPolicyLint builds the example pack program at dir/bin/policy-lint,
// where its manifest names it. It builds Go because a rule pack is a
// separate program: `ergo lint test` over the real process protocol is the
// acceptance, and the in-process evaluation (examples/lintpack/policy)
// does not cross that boundary. The pack runs under the runner's kernel
// memory bound (lint.Limits, RLIMIT_DATA).
func buildPolicyLint(t *testing.T, dir string) {
	t.Helper()
	needsBuild(t)
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, "bin", "policy-lint"), "../../examples/lintpack/cmd/policy-lint")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(errors.New(string(output)))
	}
}

// TestLintTestRunsThePolicyPack is custom-lint.md's acceptance over the
// built pack process: `ergo lint test` runs both example policies over
// their real checked fixtures through the process protocol and matches
// the committed expectations (findings, related places, every rule's
// status, applied, unused and not-evaluated suppressions); a changed
// expectation fails (exit 1) and names the fixture. `ergo lint` merges
// the same findings and fails policy on them; a configured function the
// program does not declare fails the rule, an L000 lint-runner error and
// exit 1. Mutants: lint3-cli-test-ignores-mismatch, lint3-pack-failure-silent.
func TestLintTestRunsThePolicyPack(t *testing.T) {
	dir := examplePack(t)
	buildPolicyLint(t, dir)
	fixture := func(name string) string { return filepath.Join(dir, "testdata", name) }
	for _, run := range [][]string{
		{"lint", "test", fixture("provider_boundary.ergo"), "--lint-config", filepath.Join(dir, "provider.lint.json")},
		{"lint", "test", fixture("forbidden_failure.ergo"), "--lint-config", filepath.Join(dir, "failure.lint.json")},
	} {
		if out, code := commandInProcess(t, run...); code != 0 {
			t.Fatalf("ergo %s: exit %d\n%s", strings.Join(run, " "), code, out)
		}
	}
	smokeWrite(t, dir, "testdata/provider_boundary.lint.json", `{"complete": true, "rules": [], "findings": []}`)
	out, code := commandInProcess(t, "lint", "test", fixture("provider_boundary.ergo"), "--lint-config", filepath.Join(dir, "provider.lint.json"))
	if code != 1 || !strings.Contains(string(out), "FAIL "+fixture("provider_boundary.ergo")) || !strings.Contains(string(out), "main.named binds main.Disk") {
		t.Fatalf("a changed expectation: exit %d\n%s", code, out)
	}

	type report struct {
		LintComplete bool                                   `json:"lintComplete"`
		Diagnostics  []struct{ Code, Rule, Message string } `json:"diagnostics"`
	}
	lint := func(config string) (report, int) {
		t.Helper()
		out, code := commandInProcess(t, "lint", "--json", "--lint-config", config, fixture("forbidden_failure.ergo"))
		var r report
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("exit %d, stdout %q: %v", code, out, err)
		}
		return r, code
	}
	r, code := lint(filepath.Join(dir, "failure.lint.json"))
	var codes []string
	for _, d := range r.Diagnostics {
		codes = append(codes, d.Code)
	}
	if code != 1 || !r.LintComplete || strings.Count(strings.Join(codes, " "), "policy/forbidden-failure") != 3 {
		t.Fatalf("ergo lint with the failure policy: exit %d %+v", code, r)
	}
	missing := smokeWrite(t, dir, "missing.lint.json", `{"version":1,"packs":[{"manifest":"policy.json"}],"rules":{"policy/provider-boundary":"off","policy/forbidden-failure":{"options":{"failure":"main.Denied","functions":["main.nonesuch"]}}}}`)
	r, code = lint(missing)
	runner := false
	for _, d := range r.Diagnostics {
		runner = runner || d.Code == "L000" && strings.Contains(d.Message, `lint-runner: rule policy/forbidden-failure of pack policy`) && strings.Contains(d.Message, `configured function "main.nonesuch" is not declared`)
	}
	if code != 1 || r.LintComplete || !runner {
		t.Fatalf("a configured function the program does not declare: exit %d %+v", code, r)
	}
}

// TestLintRulesStartsNoPack: `ergo lint rules` lists the selected pack's
// rules from its manifest alone (the pack program here does not exist, so
// starting it would fail), and a module's own ergo.lint.json cannot select
// a pack (exit 2): packs run only from an explicit selection, and build
// and run take no selection at all. Mutants: lint3-cli-module-doc-runs-packs,
// lint3-cli-rules-dropped.
func TestLintRulesStartsNoPack(t *testing.T) {
	dir := examplePack(t)
	out, code := commandInProcess(t, "lint", "rules", "--lint-config", filepath.Join(dir, "failure.lint.json"))
	var rules []struct {
		Rule, Pack, Severity string
		Builtin              bool
	}
	if err := json.Unmarshal(out, &rules); code != 0 || err != nil {
		t.Fatalf("ergo lint rules: exit %d %q %v", code, out, err)
	}
	var packRules []string
	for _, r := range rules {
		if !r.Builtin {
			packRules = append(packRules, r.Rule+"="+r.Severity)
		}
	}
	if strings.Join(packRules, " ") != "policy/forbidden-failure=error policy/provider-boundary=off" {
		t.Fatalf("pack rules: %v", packRules)
	}
	smokeWrite(t, dir, "ergo.mod", "module app\n")
	config, err := os.ReadFile(filepath.Join(dir, "failure.lint.json"))
	if err != nil {
		t.Fatal(err)
	}
	smokeWrite(t, dir, "ergo.lint.json", string(config))
	if out, code := commandInProcess(t, "lint", filepath.Join(dir, "testdata", "forbidden_failure.ergo")); code != 2 {
		t.Fatalf("a module document selecting a pack: exit %d, want 2\n%s", code, out)
	}
	// build and run refuse a selection as an unknown option.
	for _, run := range [][]string{
		{"build", "--rules", filepath.Join(dir, "policy.json"), filepath.Join(dir, "testdata", "forbidden_failure.ergo")},
		{"run", "--lint-config", filepath.Join(dir, "failure.lint.json"), filepath.Join(dir, "testdata", "forbidden_failure.ergo")},
	} {
		if out, code := commandInProcess(t, run...); code == 0 {
			t.Fatalf("ergo %s: exit 0\n%s", strings.Join(run, " "), out)
		}
	}
}
