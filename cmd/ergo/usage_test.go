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
