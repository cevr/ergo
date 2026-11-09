package check

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// An unknown kind is a compiler bug, never silent purity. Probe both sides
// of the valid range and the sentinel itself, which is not a call kind.
func TestRuntime199UnknownCallKind(t *testing.T) {
	for _, kind := range []CallKind{-1, CallKindCount, CallKindCount + 1} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			defer func() {
				want := fmt.Sprintf("check: unknown call kind %d in footprint", kind)
				if got := recover(); got != want {
					t.Errorf("unknown call kind: got %v, want ICE %q", got, want)
				}
			}()
			fp := &footprinter{}
			fp.call(&fpNode{}, &syntax.CallExpr{}, &Call{Kind: kind})
		})
	}
}
