package lower

import (
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestWireTupleWriterTakesTheIR is the in-process half of the IR codec
// writer for tuple types (ir_wire.go wireGen.plan *check.Tuple). The
// fixture runs on both paths in TestIRReprosMatchSource/wiretuple
// (irspike).
//
// Mutant (testdata/mutants.json): ir-wire-tuple-writer-refused.
func TestWireTupleWriterTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/wiretuple.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "wiretuple", string(src))
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
	found := false
	for _, name := range rep.CodecWrite {
		if strings.Contains(name, "EncodeTuple") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no EncodeTuple codec writer built on IR (CodecWrite: %v)", rep.CodecWrite)
	}
}
