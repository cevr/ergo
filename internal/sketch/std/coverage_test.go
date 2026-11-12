package std_test

import (
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/std/gostd"
)

// TestStdCoverageIsTotal keeps the coverage table (coverage.go) total, so
// a new std package cannot leave foreign-where-std silent by omission:
// every std package is in exactly one of the table and its covers-none
// list, each Go path has a binding (import go can name it), and no Go path
// is covered twice.
func TestStdCoverageIsTotal(t *testing.T) {
	table, none := std.Coverage()
	seen := map[string]string{}
	for _, p := range std.Packages() {
		_, listed := table[p]
		if listed == slices.Contains(none, p) {
			t.Errorf("std package %s: list it once, in coverage or in coversNone (coverage.go)", p)
		}
	}
	for p, gos := range table {
		if _, ok := std.Package(p); !ok {
			t.Errorf("coverage names %s, which is not a std package", p)
		}
		for _, g := range gos {
			if _, ok := gostd.Binding(g); !ok {
				t.Errorf("std %s covers Go %s, which has no binding", p, g)
			}
			if other, dup := seen[g]; dup {
				t.Errorf("Go %s is covered by %s and %s", g, other, p)
			}
			seen[g] = p
			if got, ok := std.CoveredBy(g); !ok || got != p {
				t.Errorf("CoveredBy(%s) = %s, %v; want %s", g, got, ok, p)
			}
		}
	}
	for _, p := range none {
		if _, ok := std.Package(p); !ok {
			t.Errorf("coversNone names %s, which is not a std package", p)
		}
	}
}
