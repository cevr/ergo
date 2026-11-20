package lower

import (
	"os"
	"testing"
)

// TestNarrowedFunctionTypeChecks is the smallest repro of CK-FAILPARAM
// (MQ2's func-of-failure-param): a function of Boom goes into Classed's
// field, declared func(e E) with E an error row, a Go error, so the
// lowering wraps it in a function that asserts the error to Boom (the row
// gap of a write, rowgap.go; ConvNarrow before SM WEB1 made Classed[Boom]'s
// parameter Boom itself). The emitted Go is type-checked in process; the
// evaluator and `ergo run` give testdata/narrow_param.out. Mutants:
// lower-narrow-no-assert, rowgap-no-wrap.
func TestNarrowedFunctionTypeChecks(t *testing.T) {
	src, err := os.ReadFile("testdata/narrow_param.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := lowerSource("narrow_param", string(src), true)
	if o.err != nil || len(o.diags) != 0 || len(o.refused) != 0 || o.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v", o.err, o.diags, o.refused, o.panicked)
	}
	typecheckGo(t, []*lowered{o})
}
