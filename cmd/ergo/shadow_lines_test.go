package main

import (
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/runtime/v1/shadow"
)

// TestShadowLinesReadAcrossWrites is ergo test's reader of the test
// program's standard error (LF6d): a refutation line that reaches it in
// pieces, split inside the line and beside other output, is read once,
// whole, and no other line is.
//
// Mutant: lf6d-shadow-lines-per-write (each Write is read as lines on its
// own, so a line split across two writes is lost).
func TestShadowLinesReadAcrossWrites(t *testing.T) {
	s := &shadowLines{}
	line := shadow.RefutedPrefix + "main.DecEnc refuted (\"zq-17\")\n"
	for _, chunk := range []string{"panic: something else\n" + line[:9], line[9:20], line[20:] + "\tF at main.ergo:12\n"} {
		if n, err := s.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write: %d, %v", n, err)
		}
	}
	want := sketchfront.ShadowRefutation{Law: "main.DecEnc", Case: `("zq-17")`}
	if len(s.refs) != 1 || s.refs[0] != want {
		t.Errorf("refutations %+v; want only %+v", s.refs, want)
	}
}
