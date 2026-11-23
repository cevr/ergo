package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// machineExprWant is a fixture's `// want CODE` or `// want CODE "text"`
// marker: the line has exactly that diagnostic, whose message holds text.
var machineExprWant = regexp.MustCompile(`// want (S\d{3})(?: "([^"]*)")?\s*$`)

// machineExprTexts are what the refusals of the MACHINE-EXPR programs
// in fixtures/negative say: those files carry the plain marker the
// negative fixtures' harness reads (the S3-AGREE corpus holds them).
var machineExprTexts = map[string]string{
	"machine-expr-tuple-state.ergo":         "write a record, such as type PairState { A S, B S }",
	"machine-expr-computed-wrong-type.ergo": "the step of machine M must have the type",
}

// TestMachineExprFixtures checks the MACHINE-EXPR programs
// (testdata/fixtures/machine-expr and fixtures/negative/machine-expr-*;
// runtime.md, Machines and actors 1): each has exactly the diagnostics
// its markers name, at their lines, and each marked text is in its
// line's message; an unmarked program checks clean. The README names
// what each program holds and the mutants it kills
// (mexpr-step-ident-only, mexpr-tparams-dropped, mexpr-step-impure).
func TestMachineExprFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "fixtures", "machine-expr", "*.ergo"))
	neg, err2 := filepath.Glob(filepath.Join("testdata", "fixtures", "negative", "machine-expr-*.ergo"))
	files = append(files, neg...)
	if err != nil || err2 != nil || len(files) == 0 || len(neg) != len(machineExprTexts)+1 {
		t.Fatalf("machine-expr fixtures: %v %v, %d in negative", err, err2, len(neg))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		var want []string
		texts := map[int]string{}
		for i, line := range strings.Split(src, "\n") {
			if m := machineExprWant.FindStringSubmatch(line); m != nil {
				want = append(want, fmt.Sprintf("%s@%d", m[1], i+1))
				texts[i+1] = m[2]
				if text, ok := machineExprTexts[filepath.Base(f)]; ok {
					texts[i+1] = text
				}
			}
		}
		sort.Strings(want)
		_, diags := check.CheckSource(src)
		if g := got(diags); !slices.Equal(g, want) {
			t.Errorf("%s: diagnostics %v, want %v\n\t%s", f, g, want, describe(diags))
			continue
		}
		for _, d := range diags {
			if text := texts[d.Line]; !strings.Contains(d.Message, text) {
				t.Errorf("%s:%d: the message %q does not say %q", f, d.Line, d.Message, text)
			}
		}
	}
}
