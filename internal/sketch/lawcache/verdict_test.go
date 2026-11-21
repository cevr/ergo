package lawcache

import (
	"slices"
	"testing"
)

// TestVerdictKeepsItsReads: a verdict is valid for what it read
// (GB-WS0C, q14), so the store keeps a run's reads beside it, an empty
// set as empty, and a verdict whose reads are not known (a test binary's)
// as unknown, never as a verdict that read nothing; a verdict of the
// format before reads were kept is not read at all.
//
// Mutant it alone catches: ws0c-unknown-reads-as-none
// (internal/sketch/lawcache/testdata/mutants.json).
func TestVerdictKeepsItsReads(t *testing.T) {
	for _, v := range []Verdict{
		{Evidence: "tested", Cases: 100, Text: "100 cases", Reads: []string{"m/a", "m/b\tc"}, ReadsKnown: true},
		{Evidence: "proved", Cases: 3, Text: "proved: all 3 cases", ReadsKnown: true},
		{Evidence: "tested", Cases: 7, Text: "7 cases\n\twitness"},
	} {
		got, ok := Decode(v.Encode())
		if !ok || got.Evidence != v.Evidence || got.Cases != v.Cases || got.Text != v.Text || got.ReadsKnown != v.ReadsKnown || !slices.Equal(got.Reads, v.Reads) {
			t.Errorf("Decode(Encode(%+v)) = %+v, %v", v, got, ok)
		}
	}
	// The format before reads: tab-separated evidence, cases and text.
	if v, ok := Decode("tested\t100\t\"100 cases\""); ok {
		t.Errorf("a verdict of the older format was read: %+v", v)
	}
}
