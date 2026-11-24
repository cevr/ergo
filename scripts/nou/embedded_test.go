package main

import (
	"strings"
	"testing"
)

// The same inventory must see a bound call assembled from Go constants and
// a generated fragment, but not a shadow, an explanatory string, or die.
func TestInventoryExtractsEmbeddedErgo(t *testing.T) {
	const fixture = `package fixture
func examples() {
	program := "func f() int { Unre" + "achable(\"old binding\") }\n" +
		"func user() int { let Unreachable = func(s string) int { len(s) }; Unreachable(\"shadow\") }"
	_ = program
	_ = "func worker() int defects { die Failed{} }"
	_ = "func text() string { \"Unreachable(\\\"not code\\\")\" }"
}
`
	const generator = `package generator
func generated() {
	fmt.Fprintf(&b, "case %s:\nUnreachable(%q)\n}\n", tag, reason)
}
`
	for _, tc := range []struct {
		name, src, scope, anchor string
	}{
		{"fixture_test.go", fixture, "embedded Go tests/property source", "func f/"},
		{"generator.go", generator, "generator template", "fragment/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := embeddedSites(tc.name, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || sourceScope(got[0].id.file) != tc.scope ||
				!strings.Contains(got[0].id.anchor, tc.anchor) {
				t.Fatalf("wanted one %s site anchored at %q, got %v", tc.scope, tc.anchor, got)
			}
		})
	}
	// A lexical error before a candidate cannot turn a corrupt fixture
	// into an apparently empty inventory.
	damaged := strings.Replace(generator, "case %s:", "§case %s:", 1)
	if got, err := embeddedSites("generator.go", damaged); err == nil ||
		!strings.Contains(err.Error(), "cannot lex through a legacy-looking source fragment") {
		t.Fatalf("expected an extraction error for the corrupt template, got %v, %v", got, err)
	}
}
