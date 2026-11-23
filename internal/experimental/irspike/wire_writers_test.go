package irspike

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWireWritersMatchSource is W1's property: the encoders on the
// prelude's Writer interface (ergoEncode*) of GenerateWireWriters's
// records, lists, Maps, the tagged union U, omittable and nullable
// fields take IR bodies, none keeps its text, and each program prints
// the events codec's Events recorded as the source path's program
// prints them, with the build-time fold on and off.
//
// Mutants: w1-writer-text (no encoder on the Writer interface takes the
// IR), w1-writer-const (a Key's or a Tag's name not passed as it is).
//
// It builds Go because the other side of the agreement is the built
// program, by both paths.
func TestIRWireWritersMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRWireWritersMatchSource", GenerateWireWriters, func(rep lower.IRReport) string {
		var kept []string
		for k := range rep.CodecText {
			if strings.HasPrefix(k, lower.HelperSpelling("", "ergoEncode")) {
				kept = append(kept, strings.ReplaceAll(k, "\t", ": "))
			}
		}
		if len(kept) > 0 {
			sort.Strings(kept)
			return "encoders on the Writer interface keep their text: " + strings.Join(kept, "; ")
		}
		for _, name := range rep.CodecWrite {
			if strings.HasPrefix(name, lower.HelperSpelling("", "ergoEncodeR")) {
				return ""
			}
		}
		return "no record's encoder on the Writer interface has an IR body"
	}, wireWriterSites...)
}
