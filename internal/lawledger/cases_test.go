package lawledger

import (
	"math"
	"path/filepath"
	"slices"
	"testing"
)

func TestCaseStoreRoundTripMixedShadowAndProperty(t *testing.T) {
	input := `# comment line
law app.DecEnc ("zq-17")
property app.SortLaw tape h1:abc123 0,1,a,ffffffffffffffff
property app.Empty tape h1:def456 -
`
	store, err := ParseCases(input)
	if err != nil {
		t.Fatalf("ParseCases: %v", err)
	}
	if got := store.Shadow["app.DecEnc"]; !slices.Equal(got, []string{`("zq-17")`}) {
		t.Fatalf("shadow cases: got %v", got)
	}
	props := store.ForProperty("app.SortLaw")
	if len(props) != 1 || props[0].Digest != "h1:abc123" || !slices.Equal(props[0].Tape, []uint64{0, 1, 10, math.MaxUint64}) {
		t.Fatalf("SortLaw property cases: got %+v", props)
	}
	empty := store.ForProperty("app.Empty")
	if len(empty) != 1 || len(empty[0].Tape) != 0 {
		t.Fatalf("Empty property cases: got %+v", empty)
	}

	// Deduplication: adding identical entry returns false.
	if store.AddProperty(PropertyCase{Key: "app.SortLaw", Digest: "h1:abc123", Tape: []uint64{0, 1, 10, math.MaxUint64}}) {
		t.Fatalf("expected duplicate AddProperty to return false")
	}
	// Adding different tape or digest returns true.
	if !store.AddProperty(PropertyCase{Key: "app.SortLaw", Digest: "h1:newdig", Tape: []uint64{2, 3}}) {
		t.Fatalf("expected new AddProperty to return true")
	}

	// Format -> ParseCases round-trip is lossless.
	formatted := store.Format()
	again, err := ParseCases(formatted)
	if err != nil {
		t.Fatalf("re-parse formatted: %v\n%s", err, formatted)
	}
	if again.Format() != formatted {
		t.Fatalf("non-canonical format:\nfirst:\n%s\nsecond:\n%s", formatted, again.Format())
	}

	// File read/write round trip.
	p := filepath.Join(t.TempDir(), CasesFile)
	if err := WriteCasesFile(p, store); err != nil {
		t.Fatalf("WriteCasesFile: %v", err)
	}
	loaded, err := ReadCasesFile(p)
	if err != nil {
		t.Fatalf("ReadCasesFile: %v", err)
	}
	if loaded.Format() != formatted {
		t.Fatalf("file round-trip mismatch:\ngot:\n%s\nwant:\n%s", loaded.Format(), formatted)
	}

	// RemoveProperty removes only matching entries and preserves shadow lines.
	removed := loaded.RemoveProperty("app.SortLaw", func(c PropertyCase) bool { return c.Digest == "h1:abc123" })
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	if len(loaded.ForProperty("app.SortLaw")) != 1 || len(loaded.Shadow["app.DecEnc"]) != 1 {
		t.Fatalf("unexpected state after RemoveProperty: %+v", loaded)
	}
}

func TestCaseStoreRejectsMalformedLines(t *testing.T) {
	for _, bad := range []string{
		"garbage line",
		"law onlykey",
		"property app.Foo nottape h1:1 0",
		"property app.Foo tape h1:1 nothex",
	} {
		if _, err := ParseCases(bad); err == nil {
			t.Errorf("ParseCases(%q): expected error, got nil", bad)
		}
	}
}
