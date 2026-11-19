package lower

import (
	"os"
	"testing"
)

// TestNarrowedFunctionTypeChecks is the smallest repro of CK-FAILPARAM
// (MQ2's func-of-failure-param): a function of Boom goes into a field whose
// parameter is a row value of Boom, a Go error, so the lowering wraps it in
// a function that asserts the error to Boom (ConvNarrow, narrowFunc). The
// emitted Go is type-checked in process; the evaluator and `ergo run` give
// testdata/narrow_param.out. Mutant: lower-narrow-no-assert.
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
