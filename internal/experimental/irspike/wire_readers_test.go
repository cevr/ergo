package irspike

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWireReadersMatchSource is PORT-WIRE-READERS R1's property: Ev's
// decoder functions on the prelude's Reader interface (ergoDecode*: a
// record of basics with narrow integers, a nested record, a list of
// records, a map, a list of ints) take IR bodies, none keeps its text,
// and each program prints what the source path's prints, with the
// build-time fold on and off. take's decode is on the interface, so these
// are the bodies that run. The decoders on a direct reader (ergoRead*,
// R2) keep their text. GenerateWireReaders's texts reach every failure
// the decoder gives (a missing field, the wrong kind, an integer out of
// its range), an unknown key, a key given twice and a text cut short
// mid-decode (on a second reader, codec's Replay), and print each
// failure's value and place.
//
// Mutants: pwr-reader-text (no decoder takes the IR), pwr-reader-range
// (the range check of a narrow integer dropped), pwr-reader-skip (an
// unknown key's value is not skipped) and pwr-reader-missing (a missing
// field is not a failure).
//
// It builds Go because the other side of the agreement is the built
// program, by both paths.
func TestIRWireReadersMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRWireReadersMatchSource", GenerateWireReaders, func(rep lower.IRReport) string {
		var kept []string
		for k := range rep.CodecText {
			if strings.HasPrefix(k, "ergoDecode") {
				kept = append(kept, strings.ReplaceAll(k, "\t", ": "))
			}
		}
		if len(kept) > 0 {
			sort.Strings(kept)
			return "decoders on the Reader interface keep their text: " + strings.Join(kept, "; ")
		}
		for _, name := range rep.CodecRead {
			if strings.HasPrefix(name, "ergoDecodeEv") {
				return ""
			}
		}
		return "Ev's decoder on the Reader interface has no IR body"
	}, wireReaderSites...)
}
