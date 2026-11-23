package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFindingExcusesOnlyItsOwnAndGoesStale: an expected finding turns the
// one problem it names into a red-by-record note, and a finding no problem
// matches is a layout error, so the file empties itself as the owed units
// land.
//
// Mutants: findings-stale-never (stale reports nothing),
// findings-match-any-provider (matches drops the provider),
// findings-match-any-package (matches drops the package).
func TestFindingExcusesOnlyItsOwnAndGoesStale(t *testing.T) {
	t.Parallel()
	fs := &findings{list: []*finding{
		{example: "shop", pkg: "orders", code: "S108", law: "Store.Keeps", provider: "Disk", unit: "OWED-A", line: 1},
		{example: "shop", pkg: "orders", code: "S108", law: "Store.Gone", provider: "Disk", unit: "OWED-B", line: 2},
	}}
	at := func(pkg, law, provider string) string {
		return "examples/shop: " + pkg + lawCheck + "S108 at orders/disk.ergo:3:9: law " + law + ", declared at store.ergo:4:6, cannot run at " + provider + ": its values have no generator"
	}
	rest, notes := fs.apply([]string{
		at("orders", "Store.Keeps", "Disk"),
		at("orders", "Store.Keeps", "Cloud"),
		at("billing", "Store.Keeps", "Disk"),
	})
	if len(notes) != 1 || !strings.Contains(notes[0], "owed OWED-A") {
		t.Errorf("notes = %q; want one red-by-record note owed OWED-A", notes)
	}
	if len(rest) != 2 {
		t.Errorf("rest = %q; want the problems at Cloud and in billing, which no line names", rest)
	}
	stale := fs.stale()
	if len(stale) != 1 || !strings.Contains(stale[0], "Store.Gone") {
		t.Errorf("stale = %q; want the line of Store.Gone, which no problem matched", stale)
	}
}

// TestExpectedFindingsNameOwedUnits: each line of the expected findings
// reads as six fields and names an owed unit examples/README.md describes
// under "Red by record"; it pins no count.
func TestExpectedFindingsNameOwedUnits(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fs, problems := readFindings(root)
	for _, p := range problems {
		t.Error(p)
	}
	readme, err := os.ReadFile(filepath.Join(root, "examples", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, ledger, ok := strings.Cut(string(readme), "## Red by record")
	if !ok {
		t.Fatal(`examples/README.md has no "## Red by record" section, the ledger of owed units`)
	}
	ledger, _, _ = strings.Cut(ledger, "\n## ")
	for _, f := range fs.list {
		if !strings.Contains(ledger, "**"+f.unit+"**") {
			t.Errorf("%s: unit %s is not in examples/README.md's Red by record", f, f.unit)
		}
	}
}
