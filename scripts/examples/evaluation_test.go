package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestCheckVerdictsMatchNative guards the runner's two seams: equal exits
// do not establish agreement when an obligation, evidence or case count
// differs. The fake compiler is the process boundary; real compiler
// agreement runs in the examples step. Mutant: examples-ignore-evaluation.
func TestCheckVerdictsMatchNative(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, status, evidence, native string
		cases                          int
		bad                            bool
	}{
		{"example", "passed", "evaluated", "ok Fixture", 1, false},
		{"listed", "passed", "proved", "ok Fixture (proved: all 3 cases)", 3, false},
		{"drawn", "passed", "tested", "ok Fixture (100 cases)", 100, false},
		{"skipped", "skipped", "", "ok Fixture", 0, false},
		{"incomplete", "incomplete", "", "ok Fixture", 0, false},
		{"refuted", "refuted", "", "ok Fixture", 1, true},
		{"defect", "defect", "", "ok Fixture", 1, true},
		{"missing", "passed", "evaluated", "ok Different", 1, true},
		{"evidence", "passed", "proved", "ok Fixture (100 cases)", 100, true},
		{"count", "passed", "tested", "ok Fixture (99 cases)", 100, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			json := fmt.Sprintf(`{"checked":true,"evaluation":{"schemaVersion":1,"status":%q,"obligations":[{"name":"Fixture","status":%q,"evidence":%q,"cases":%d,"reason":"fixture limit"}]}}`, c.status, c.status, c.evidence, c.cases)
			src := "#!/bin/sh\necho hi\nexit 0\n# CHECKJSON " + json + "\n# NATIVE " + c.native + "\n"
			f.program("probe", src, "default\n", map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
			code, out := f.run()
			if (code != 0) != c.bad || !strings.Contains(out, "probe: check") {
				t.Fatalf("exit %d (failure wanted: %t)\n%s", code, c.bad, out)
			}
			calls := readCalls(t, f)
			if !strings.Contains(strings.Join(calls, "\n"), "check ") {
				t.Fatalf("no check call: %q", calls)
			}
		})
	}
}
