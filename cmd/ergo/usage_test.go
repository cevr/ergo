package main

import (
	"strings"
	"testing"
)

// TestUnknownOptionIsUsageForEveryCommand runs every available command of
// the command table (and inspect's api and machine forms) with an option no
// command takes: each refuses it as an invalid invocation, exit 2, before it
// reads a source. Mutant lint3-cli-unknown-option-not-usage (unknownOption
// returns a plain error, so build, run, check and inspect exit 1) is killed
// here; no other test runs build or run with an unknown option.
func TestUnknownOptionIsUsageForEveryCommand(t *testing.T) {
	var invocations [][]string
	for _, c := range commands {
		if c.fate == available {
			invocations = append(invocations, strings.Fields(c.name))
		}
	}
	invocations = append(invocations, []string{"inspect", "api"}, []string{"inspect", "machine"})
	for _, words := range invocations {
		args := append(append([]string{}, words...), "--no-such-option", "/nonexistent/usage-probe.ergo")
		err := command(args)
		if code := exitCode(err); code != 2 {
			t.Errorf("ergo %s: exit %d, want 2 (%v)", strings.Join(args, " "), code, err)
		}
	}
}

// TestFileCommandUsageIsExitTwo: check, build, run and inspect refuse the
// other invalid invocations as diagnostics, lint and test do, exit 2 before
// they read a source: no source, an option of another command, and a
// target other than Go. bind and gen name an unknown option as every other
// command does. Mutants lint4-cli-usage-unclassified (parseFileOptions
// returns its refusal unclassified), lint4-cli-flag-unknown-unnamed
// (flagError reads no option), lint4-cli-bind-flag-usage and
// lint4-cli-gen-flag-usage (bind or gen answers its usage line instead).
func TestFileCommandUsageIsExitTwo(t *testing.T) {
	const probe = "/nonexistent/usage-probe.ergo"
	for _, word := range []string{"check", "build", "run", "inspect"} {
		other := []string{"--receipt", "r.json"}
		for _, c := range []struct {
			args []string
			want string
		}{
			{nil, "source file required"},
			{append(other, probe), "--receipt is only supported by test"},
			{[]string{"--target", "js", probe}, "EF110: unsupported target js"},
		} {
			err := command(append([]string{word}, c.args...))
			if code := exitCode(err); code != 2 || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ergo %s %v: exit %d (%v), want 2 and %q", word, c.args, code, err, c.want)
			}
		}
	}
	for _, args := range [][]string{{"bind", "go", "-x", "fmt"}, {"gen", "rpc", "-x", "limits.Limiter"}} {
		err := command(args)
		if code := exitCode(err); code != 2 || err == nil || err.Error() != "unknown option -x" {
			t.Errorf("ergo %s: exit %d (%v), want 2 and \"unknown option -x\"", strings.Join(args, " "), code, err)
		}
	}
}
