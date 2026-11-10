package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestMachineLawDiagnostics holds the text and place of the diagnostics of
// machine laws (MU6; types.md, Laws 13): a target that is not a variant of
// the state, a law declared twice in one machine, and an entry after a law.
// Mutant caught: a reachability law that admits any type as its target.
func TestMachineLawDiagnostics(t *testing.T) {
	const head = "type S = Idle{} | Busy{}\ntype E = Ping{}\ntype Other {}\nfunc step(s S, e E) Decision[S, int] { Ignore }\n"
	rows := []struct {
		name, src, code, msg string
		line                 int
	}{
		{"not a variant", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L reaches Other,\n}\nfunc main() {}\n",
			"S108", "Other is not a variant of S, the state of machine M", 8},
		{"declared twice", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tstep: step,\n\tlaw L reaches Busy,\n\tlaw L never reaches Busy,\n}\nfunc main() {}\n",
			"S108", "law L is declared twice", 9},
		{"entry after a law", head + "type M machine[S, E] {\n\tinitial: Idle,\n\tlaw L reaches Busy,\n\tstep: step,\n}\nfunc main() {}\n",
			"S001", "a machine's entries come before its laws", 8},
	}
	for _, r := range rows {
		_, diags := check.CheckSource(r.src)
		found := false
		for _, d := range diags {
			if d.Code == r.code && d.Message == r.msg && d.Line == r.line {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s %q at line %d, got %v", r.name, r.code, r.msg, r.line, diags)
		}
	}
}
