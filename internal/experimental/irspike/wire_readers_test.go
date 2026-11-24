package irspike

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWireReadersMatchSource is PORT-WIRE-READERS R1's and R2a's
// property: Ev's decoder functions on the prelude's Reader interface
// (ergoDecode*) and on json's Reader, a direct reader (ergoRead*: a
// record of basics with narrow integers, a nested record, a list of
// records, a map, a list of ints, and an omittable field) take IR bodies.
// None keeps its text, and each program prints what the source path prints, with the
// build-time fold on and off. take decodes on the interface and direct on
// json's Reader itself, so both decoders run on every text.
// GenerateWireReaders's texts reach every failure the decoder gives (a
// missing field, the wrong kind, an integer out of its range), an unknown
// key, a key given twice and a text cut short mid-decode (on a second
// reader, codec's Replay), and print each failure's value and place.
//
// Mutants: pwr-reader-text (no decoder takes the IR), pwr-reader-range
// (the range check of a narrow integer dropped), pwr-reader-skip (an
// unknown key's value is not skipped), pwr-reader-missing (a missing
// field is not a failure), r2a-direct-text (no direct decoder takes the
// IR) and r2a-direct-via-iface (a direct decoder's nested value read by
// the interface's decoder function), pwr-r3-absent-not-initialized (a
// missing optional key does not produce the explicit empty alternative),
// and pwr-r3-wrapper-loses-payload (the present alternative loses its value).
//
// It builds Go because the other side of the agreement is the built
// program, by both paths.
func TestIRWireReadersMatchSource(t *testing.T) {
	f7PropertyWith(t, "TestIRWireReadersMatchSource", GenerateWireReaders, func(rep lower.IRReport) string {
		var kept []string
		for k := range rep.CodecText {
			if strings.HasPrefix(k, lower.HelperSpelling("", "ergoDecode")) || strings.HasPrefix(k, lower.HelperSpelling("", "ergoRead")) {
				kept = append(kept, strings.ReplaceAll(k, "\t", ": "))
			}
		}
		if len(kept) > 0 {
			sort.Strings(kept)
			return "decoders keep their text: " + strings.Join(kept, "; ")
		}
		iface, direct := false, false
		for _, name := range rep.CodecRead {
			iface = iface || strings.HasPrefix(name, lower.HelperSpelling("", "ergoDecodeEv"))
			direct = direct || strings.HasPrefix(name, lower.HelperSpelling("", "ergoReadEv"))
		}
		switch {
		case !iface:
			return "Ev's decoder on the Reader interface has no IR body"
		case !direct:
			return "Ev's decoder on json's Reader (a direct reader) has no IR body"
		}
		return ""
	}, wireReaderSites...)
}
