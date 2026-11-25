package lower

import (
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestWireOwnWriteTakesTheIR is the in-process half of the IR codec
// writer for types with a WireWrite hook on the reference Writer
// (ir_wire.go wireGen.ownWrite). The fixture runs on both paths in
// TestIRReprosMatchSource/wireownwrite (irspike).
//
// Mutant (testdata/mutants.json): ir-wire-own-write-refused.
func TestWireOwnWriteTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/wireownwrite.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "wireownwrite", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for k := range rep.CodecText {
		if strings.Contains(k, "an own write") {
			t.Errorf("own-write codec writer kept text body: %s", k)
		}
	}
	var gotRaw, gotAttr bool
	for _, name := range rep.CodecWrite {
		if strings.Contains(name, "EncodeRaw") {
			gotRaw = true
		}
		if strings.Contains(name, "EncodeAttribute") {
			gotAttr = true
		}
	}
	if !gotRaw || !gotAttr {
		t.Errorf("expected EncodeRaw and EncodeAttribute in CodecWrite, got %v", rep.CodecWrite)
	}
}
