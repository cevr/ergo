package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// TestLintGateAndDiffCommand is LHP's command acceptance for --gate and
// --diff (red at base: neither flag exists). In process, over the LINT0
// fixture with the justifiers on: silent-catch publishes p0.16 and
// foreign-error-public no number, so --gate 0.16 fails (exit 1, gate not
// passed) and --gate 0.17 passes; an untracked file is all added, so
// --diff reports every finding; after a commit nothing is added, so none;
// a function appended with a silent catch is the one finding --diff
// reports, and --diff=HEAD reads the same; ergo lint rules prints
// silent-catch's precision. It runs git, which the diff
// scope reads by design (no in-process stand-in exists for a work tree).
// Mutants: lhp-cmd-gate-ignored, lhp-untracked-not-added,
// lhp-single-file-unkeyed, lhp-rules-precision-dropped.
func TestLintGateAndDiffCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is needed: %v", err)
	}
	dir := physicalTempDir(t)
	smokeWrite(t, dir, "ergo.mod", "module app\n")
	smokeWrite(t, dir, "ergo.lint.json", `{"version": 1, "extends": ["justifiers"]}`)
	main := smokeWrite(t, dir, "cmd/app/main.ergo", lintFixture)
	type report struct {
		PolicyPassed bool `json:"policyPassed"`
		Diagnostics  []struct {
			Code      string
			Precision *float64
			Span      struct{ Line int }
		} `json:"diagnostics"`
		Gate *struct{ Passed bool } `json:"gate"`
	}
	run := func(args ...string) (report, int) {
		t.Helper()
		out, code := commandInProcess(t, args...)
		var r report
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("ergo %s: exit %d, stdout %q: %v", strings.Join(args, " "), code, out, err)
		}
		return r, code
	}
	codes := func(r report) string {
		var out []string
		for _, d := range r.Diagnostics {
			p := "-"
			if d.Precision != nil {
				p = fmt.Sprintf("%.2f", *d.Precision)
			}
			out = append(out, fmt.Sprintf("%s@%d:%s", d.Code, d.Span.Line, p))
		}
		return strings.Join(out, " ")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"--no-optional-locks", "-c", "user.email=lint@example.org", "-c", "user.name=lint", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if r, code := run("lint", "--json", "--gate", "0.16", main); code != 1 || r.Gate == nil || r.Gate.Passed || r.PolicyPassed || codes(r) != "L102@10:- L101@16:0.16" {
		t.Fatalf("--gate 0.16: exit %d %+v", code, r)
	}
	if r, code := run("lint", "--json", "--gate=0.17", main); code != 0 || r.Gate == nil || !r.Gate.Passed || !r.PolicyPassed {
		t.Fatalf("--gate 0.17: exit %d %+v", code, r)
	}
	if _, code := commandInProcess(t, "lint", "--gate", "1.5", main); code != 2 {
		t.Fatalf("--gate 1.5: exit %d, want 2", code)
	}
	git("init", "-q")
	if r, code := run("lint", "--json", "--diff", main); code != 0 || codes(r) != "L102@10:- L101@16:0.16" {
		t.Fatalf("--diff, untracked: exit %d %+v", code, r)
	}
	git("add", ".")
	git("commit", "-q", "-m", "fixture")
	if r, code := run("lint", "--json", "--diff", main); code != 0 || codes(r) != "" {
		t.Fatalf("--diff, committed: exit %d %+v", code, r)
	}
	smokeWrite(t, dir, "cmd/app/main.ergo", lintFixture+"\nfunc more() int {\n\tparse(\"m\") catch {\n\tcase Bad:\n\t\t0\n\t}\n}\n")
	for _, flag := range []string{"--diff", "--diff=HEAD"} {
		if r, code := run("lint", "--json", flag, main); code != 0 || codes(r) != "L101@29:0.16" {
			t.Fatalf("%s, appended: exit %d %+v", flag, code, r)
		}
	}
	t.Chdir(dir)
	out, code := commandInProcess(t, "lint", "rules")
	var rules []struct {
		Rule      string
		Precision *float64
	}
	if err := json.Unmarshal(out, &rules); err != nil || code != 0 {
		t.Fatalf("ergo lint rules: exit %d %v: %s", code, err, out)
	}
	i := slices.IndexFunc(rules, func(r struct {
		Rule      string
		Precision *float64
	}) bool {
		return r.Rule == "silent-catch"
	})
	if i < 0 || rules[i].Precision == nil || *rules[i].Precision != 0.16 {
		t.Fatalf("ergo lint rules: silent-catch's precision is not 0.16: %s", out)
	}
}
