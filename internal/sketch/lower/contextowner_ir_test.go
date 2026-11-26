package lower

import (
	"os"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestContextOwnerTakesTheIR is the in-process half of the IR leg of a
// with of a context owner (contextWith / contextCall, context.go): the
// operand is evaluated once, protocol contextKey and contextText are
// called on it with any type arguments or witnesses (including open
// caller witnesses), Scope.OpenContext opens on the enclosing scope, and
// Context.End closes at the block's normal end while early exits restore
// through Scope.End. Every function of the fixture has an IR body with
// NoFallback=true. The fixture runs on both paths in
// TestIRReprosMatchSource/contextowner (irspike).
//
// Mutant (testdata/mutants.json): ir-context-owner-refused.
func TestContextOwnerTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/contextowner.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "contextowner", string(src))
	u := Unit{
		Package:    o.pkg,
		Files:      []*syntax.File{o.file},
		Sources:    []Source{{Path: testSourcePath, Text: string(src)}},
		NoFallback: true,
	}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{
		"contextWithUser", "contextWithText", "contextWithGen",
		"contextWithOpen", "ergo__contextWithOpenDog", "shadowAndRestore",
		"earlyReturn", "mixedWithClose", "taskSnapshot", "runSpan", "main_1",
	} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (fallbacks %v; IR %v)", name, rep.Fallback, rep.IR)
		}
	}
}
