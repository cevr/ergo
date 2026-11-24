package irspike

import (
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRWireReadersMatchSource checks decoder bodies on both the prelude
// Reader interface (ergoDecode*) and json's direct reader (ergoRead*).
// No decoder keeps its text, and the program agrees with the source path
// with the build-time fold on and off. GenerateWireReaders reaches
// concrete and generic codecs, introduced wire forms, and failures
// including texts cut short mid-decode on codec's Replay.
//
// Mutants: pwr-reader-text (no decoder takes the IR), pwr-reader-range
// (the range check of a narrow integer dropped), pwr-reader-skip (an
// unknown key's value is not skipped), pwr-reader-missing (a missing
// field is not a failure), r2a-direct-text (no direct decoder takes the
// IR) and r2a-direct-via-iface (a direct decoder's nested value read by
// the interface's decoder function), pwr-r3-absent-not-initialized (a
// missing optional key does not produce the explicit empty alternative),
// pwr-r3-wrapper-loses-payload (the present alternative loses its value),
// pwr-r3-unknown-tag-lost (the rejected tag is missing from its failure),
// pwr-r3-parser-bypassed (a decoded base bypasses its parser),
// pwr-r3-null-not-read (a null is sent to the payload reader), and
// pwr-r3-tuple-extra-ignored (a tuple ignores trailing elements).
// The small forms add pwr-r3-own-read-ignored, pwr-r3-bare-null-not-taken,
// pwr-r3-members-key-lost, pwr-r3-number-text-lost and pwr-r3-append-prefix.
// pwr-r3-pointer-assertion-unavailable removes pointer assertions; the
// generic record uses pwr-r3-generic-field-reader-lost.
// pwr-r3-row-variant-refused sends a row value through the named-union
// conversion, leaving the error-row reader on text.
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
