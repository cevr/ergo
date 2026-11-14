package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commandInProcess runs one ergo invocation in this process and returns
// what it printed on stdout and the exit status main gives its error.
func commandInProcess(t *testing.T, args ...string) ([]byte, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.Bytes()
	}()
	runErr := command(args)
	os.Stdout = saved
	w.Close()
	out := <-done
	r.Close()
	switch e := runErr.(type) {
	case nil:
		return out, 0
	case exitStatusError:
		return out, e.code
	case usageError:
		return out, 2
	}
	return out, 1
}

const lintFixture = `type Bad { Reason string }

func parse(s string) int fails Bad {
	if s == "" {
		return Bad{Reason: "empty"}
	}
	1
}

func Load(n int) int fails go.Error {
	n
}

func main() {
	let a = parse("a") catch {
	case Bad:
		0
	}
	let b = parse("") catch {
	// ergo-lint-disable-next-line silent-catch -- the default is the answer
	case Bad:
		0
	}
	let _ = a + b
}
`

// TestLintCommandReadsModuleConfiguration is LINT0's command acceptance
// (red at base: lint was a removed command). In process, over a module
// whose ergo.lint.json the command reads beside ergo.mod: the recommended
// rules report, a reasoned directive suppresses, --strict fails policy on
// a warning, the configuration turns a rule off, ergo diagnostics merges
// the same advice, and a bad preset or configuration is exit 2.
func TestLintCommandReadsModuleConfiguration(t *testing.T) {
	dir := physicalTempDir(t)
	smokeWrite(t, dir, "ergo.mod", "module app\n")
	main := smokeWrite(t, dir, "cmd/app/main.ergo", lintFixture)
	type report struct {
		PolicyPassed bool `json:"policyPassed"`
		LintComplete bool `json:"lintComplete"`
		Diagnostics  []struct {
			Code, Origin, Rule, Severity string
			Span                         struct{ Line int }
		} `json:"diagnostics"`
		Suppressions []struct {
			Rule, Status string
			Line         int
		} `json:"suppressions"`
	}
	run := func(args ...string) (report, int) {
		t.Helper()
		out, code := commandInProcess(t, args...)
		var r report
		if code != 2 {
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatalf("ergo %s: exit %d, stdout %q: %v", strings.Join(args, " "), code, out, err)
			}
		}
		return r, code
	}
	codes := func(r report) string {
		var out []string
		for _, d := range r.Diagnostics {
			out = append(out, fmt.Sprintf("%s@%d", d.Code, d.Span.Line))
		}
		return strings.Join(out, " ")
	}
	r, code := run("lint", "--json", main)
	if code != 0 || !r.PolicyPassed || !r.LintComplete || codes(r) != "L102@10 L101@16" || len(r.Suppressions) != 1 || r.Suppressions[0].Status != "applied" || r.Suppressions[0].Line != 21 {
		t.Fatalf("ergo lint: exit %d %+v", code, r)
	}
	if r, code := run("lint", "--json", "--strict", main); code != 1 || r.PolicyPassed {
		t.Fatalf("ergo lint --strict: exit %d %+v", code, r)
	}
	if r, code := run("diagnostics", "--json", main); code != 0 || codes(r) != "L102@10 L101@16" || r.Diagnostics[0].Origin != "lint" || r.Diagnostics[0].Rule != "foreign-error-public" {
		t.Fatalf("ergo diagnostics: exit %d %+v", code, r)
	}
	smokeWrite(t, dir, "ergo.lint.json", `{"version": 1, "rules": {"silent-catch": "off", "foreign-error-public": "error"}}`)
	r, code = run("lint", "--json", main)
	if code != 1 || codes(r) != "L102@10" || r.Diagnostics[0].Severity != "error" || r.Suppressions[0].Status != "not-evaluated" {
		t.Fatalf("ergo lint with a configuration: exit %d %+v", code, r)
	}
	if _, code := run("lint", "--preset", "nonesuch", main); code != 2 {
		t.Fatalf("an unknown preset: exit %d, want 2", code)
	}
	smokeWrite(t, dir, "ergo.lint.json", `{"version": 1, "rules": {"silent-catch": "loud"}}`)
	if _, code := run("lint", main); code != 2 {
		t.Fatalf("an invalid configuration: exit %d, want 2", code)
	}
	if _, code := run("diagnostics", filepath.Join(dir, "cmd", "app")); code != 2 {
		t.Fatalf("diagnostics under an invalid configuration: exit %d, want 2", code)
	}
}
