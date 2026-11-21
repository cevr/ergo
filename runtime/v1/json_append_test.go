package v1

import (
	"bytes"
	"flag"
	"math/rand"
	"testing"
)

var quotedSeed = flag.Int64("quoted.seed", 0, "run TestJSONAppendQuotedBytesIsQuoted on this seed alone")

// TestJSONAppendQuotedBytesIsQuoted is a seeded property against the
// reference model JSONAppendQuoted: the bytes form quotes the string its
// bytes spell to the same text, after any prefix, with and without HTML
// escaping. The pieces reach every branch of the loop (a plain byte, '"'
// and '\\', the named and the \u00XX controls, <, > and &, an invalid
// byte, U+2028 and U+2029, and other multi-byte runes); a failing case
// prints its seed and both texts, and -quoted.seed replays it (mutant
// gx10-quoted-bytes-runes, runtime/v1/testdata/mutants.json).
func TestJSONAppendQuotedBytesIsQuoted(t *testing.T) {
	pieces := []string{"a", "Z", " ", "\"", "\\", "\n", "\r", "\t", "\x01", "\x1f", "<", ">", "&", "\xff", "\xc3", "\u2028", "\u2029", "é", "世", "😀"}
	seeds := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	if *quotedSeed != 0 {
		seeds = []int64{*quotedSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewSource(seed))
		for n := 0; n < 200; n++ {
			var s []byte
			for k := r.Intn(12); k > 0; k-- {
				s = append(s, pieces[r.Intn(len(pieces))]...)
			}
			prefix := []byte("[1,")[:r.Intn(4)]
			html := r.Intn(2) == 0
			want := JSONAppendQuoted(append([]byte{}, prefix...), string(s), html)
			got := JSONAppendQuotedBytes(append([]byte{}, prefix...), s, html)
			if !bytes.Equal(got, want) {
				t.Fatalf("seed %d case %d: JSONAppendQuotedBytes(%q, %q, %v) = %q, JSONAppendQuoted gives %q; replay: go test ./runtime/v1 -run TestJSONAppendQuotedBytesIsQuoted -quoted.seed %d", seed, n, prefix, s, html, got, want, seed)
			}
		}
	}
}
