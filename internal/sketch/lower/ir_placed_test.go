package lower

import (
	"strings"
	"testing"
)

// TestIRPlacedNamesTheNode: a type switch with no default position, the
// codec writer's before JOBS-ICE, stops the lowering with an internal
// compiler error naming the switch and its function, not Go's compile at
// a DWARF scope. A row for the diagnostic's text.
//
// Mutant it alone catches: jobsice-placed-tswitch-default (the default's
// position is checked only when the IR has an else).
func TestIRPlacedNamesTheNode(t *testing.T) {
	at := irPos{0, 56, 25}
	sw := &irStmt{K: "tswitch", Pos: at, End: at, X: &irExpr{K: "local", Pos: at, I: 2},
		Cases: []*irCase{{Pos: at, Types: []string{"A"}}, {Pos: at, Types: []string{"B"}}}}
	f := &irFile{Funcs: []irFunc{{Name: "ergoWrite", Pos: at, Body: []*irStmt{sw}}}}
	err := irPlaced(f)
	if err == nil || !strings.Contains(err.Error(), "internal compiler error: the default or else of tswitch at [0,56,25] of ergoWrite has no position") {
		t.Fatalf("irPlaced: %v; want the tswitch's default named, in ergoWrite", err)
	}
	sw.ElsePos = at
	if err := irPlaced(f); err != nil {
		t.Fatalf("irPlaced of a placed switch: %v", err)
	}
}
