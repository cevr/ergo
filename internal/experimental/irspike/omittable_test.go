package irspike

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIROmittableMatchSource is the seeded property over records with
// omittable fields written on cell writers (PORT-WIRE, GenerateOmittable):
// each program prints the same under both lowerings, the callers have IR
// bodies, and no codec function on a cell writer keeps its text body.
// A failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because the field count and the keys reach a provider's
// state only when the compiled program runs the codec functions.
func TestIROmittableMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIROmittableMatchSource", GenerateOmittable, cellCodecText, omittableSites...)
}

// cellCodecText names the codec functions on a cell writer that keep
// their text body: every CodecText entry but the encoders on a writer
// that is no cell and the decoders, which no IR builder takes yet.
func cellCodecText(rep lower.IRReport) string {
	var kept []string
	for k := range rep.CodecText {
		if strings.HasSuffix(k, "\tcodec writer on the reference Writer") || strings.HasSuffix(k, "\tcodec writer on a direct writer that is no cell") || strings.HasSuffix(k, "\tcodec reader") {
			continue
		}
		kept = append(kept, strings.ReplaceAll(k, "\t", ": "))
	}
	if len(kept) == 0 {
		return ""
	}
	sort.Strings(kept)
	return "codec functions on a cell writer keep their text: " + strings.Join(kept, "; ")
}
