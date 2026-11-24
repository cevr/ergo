package main

import (
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A replacement in a different declaration cannot spend the slot a deletion
// frees, even if it has the same reason. A local callable may shadow the
// prelude name alongside a bound call in the same package.
func TestRatchetRejectsReplacementNotShadow(t *testing.T) {
	const old = `
func removed() int { Unreachable("same reason") }
func survivor() int { Unreachable("keep") }
`
	const next = `
// The surviving call has moved by a line and changed its spacing.
func survivor() int { Unreachable ( "keep" ) }
func added() int { Unreachable("same reason") }
func shadow() int {
	let Unreachable = func(s string) int { len(s) }
	Unreachable("same reason")
}
`
	before := checkFixture(t, old)
	after := checkFixture(t, next)
	if len(before) != 2 || len(after) != len(before) {
		t.Fatalf("expected equal bound counts, without the shadow: %v → %v", before, after)
	}
	baseline := map[siteID]int{}
	for _, s := range before {
		baseline[s.id]++
	}
	got := newSites(after, baseline)
	if len(got) != 1 || !strings.HasPrefix(got[0].id.anchor, "func added/") {
		t.Fatalf("expected only the added declaration to exceed the baseline, got %v", got)
	}
	if got := newSites(slices.DeleteFunc(after, func(s site) bool {
		return strings.HasPrefix(s.id.anchor, "func added/")
	}), baseline); len(got) != 0 {
		t.Fatalf("deletions and position-only edits are allowed: %v", got)
	}
}

func checkFixture(t *testing.T, src string) []site {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("fixture", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	return boundSites("fixture.ergo", src, f, pkg)
}
