package lower

import (
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestWireGenericWriterTakesTheIR is the in-process half of the IR codec
// writer for generic types holding type parameters (ir_wire.go wireWrite
// and wireGen.value). The fixture runs on both paths in
// TestIRReprosMatchSource/wiregeneric (irspike).
//
// Mutant (testdata/mutants.json): ir-wire-generic-writer-refused.
func TestWireGenericWriterTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/wiregeneric.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "wiregeneric", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	if len(rep.CodecText) != 0 {
		t.Errorf("codec functions keep their text body: %v", rep.CodecText)
	}
	var gotSlice, gotOption bool
	for _, name := range rep.CodecWrite {
		if strings.Contains(name, "EncodeSlice") {
			gotSlice = true
		}
		if strings.Contains(name, "EncodeOption") {
			gotOption = true
		}
	}
	if !gotSlice || !gotOption {
		t.Errorf("expected EncodeSlice and EncodeOption in CodecWrite, got %v", rep.CodecWrite)
	}
}
