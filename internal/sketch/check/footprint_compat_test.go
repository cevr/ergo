package check

import (
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// CallPrelude is retained for compatibility, but no source produces it
// since D3a. Its classification can only be tested at this boundary.
// The source-driven CallKind meta-test separately checks its retirement.
func TestRuntime199RetiredPreludeFootprint(t *testing.T) {
	n := &fpNode{}
	fp := &footprinter{}
	fp.call(n, &syntax.CallExpr{}, &Call{Kind: CallPrelude})
	if n.opaque || n.tasks || len(n.caps) != 0 || len(n.edges) != 0 {
		t.Fatal("retired CallPrelude must add no footprint; source dot calls are CallFunc")
	}
}
