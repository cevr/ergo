package syntax

import (
	"errors"
	"testing"
)

// TestDecodeRefusesAnotherLayout: rows of a layout the decoder does not
// read are refused with the bootstrap instruction, never decoded at the
// wrong widths (the 2026-11-19 SELFHOST-MERGE panic: a law row read at 9
// words from a parser that wrote 8). Mutant syntax-layout-unchecked.
func TestDecodeRefusesAnotherLayout(t *testing.T) {
	for _, v := range []uint32{0, PreviousColumnsVersion - 1, ColumnsVersion + 1} {
		_, err := decodeColumns("", []uint32{v, 0, 0, 0, 0, 0, 0})
		var le *LayoutError
		if !errors.As(err, &le) || le.Linked != v {
			t.Errorf("layout %d: got %v, want a *LayoutError naming %d", v, err, v)
		}
	}
	if v, err := LinkedLayout(); err != nil || v != ColumnsVersion && v != PreviousColumnsVersion {
		t.Errorf("the linked parser writes layout %d (%v); this decoder reads %d and %d", v, err, PreviousColumnsVersion, ColumnsVersion)
	}
}
