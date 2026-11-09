package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D1 round 5: the round 4 review's findings (stage-D1-round-4-
// findings.md), each as the program that showed it. runD1 compares the
// exact diagnostics, as CODE:line; wantMessages checks the independent
// ones by text, and that no message prints invalid.

// FuzzCheck input ae4371d31e8dd0ae (round 5 fuzz, failing at ba058dac too):
// a struct literal's value never elides its type, so the reference walk
// only visits an elided literal there (rows.md, Inference 6, round 18).
// Every refusal route of a struct literal (an extra positional value, an
// unknown field, a key that is not a name, a field set twice) refuses such
// a literal untyped, as fieldValue
// does, so no name in it resolves where the walk found no reference.
func TestStrayStructValuesAreNotReferences(t *testing.T) {
	cases := []d1Case{
		{"extra-positional", "type y{}\nfunc t(){y{{t} }}", []string{"S108:2", "S108:2", "S110:2"}},
		{"unknown-field", "type y{a int}\nfunc t() { _ = y{a: 1, b: {t}} }\n", []string{"S104:2", "S108:2"}},
		{"key-not-a-name", "type y{a int}\nfunc t() { _ = y{a: 1, 1: {t}} }\n", []string{"S108:2", "S108:2"}},
		{"set-twice", "type y{a int}\nfunc t() { _ = y{a: 1, a: {t}} }\n", []string{"S108:2"}},
		// A written type is typed on every route, so its names resolve.
		{"set-twice-written", "type y{a int}\ntype p{f func()}\nfunc t() { _ = y{a: 1, a: p{t}} }\n", []string{"S108:3"}},
	}
	runD1(t, cases)
	// The same routes with a function literal that reassigns a let mut
	// local inside the elided literal, and the field route (fieldValue) as
	// the control: the walk and typing agree, with no false advice.
	for _, src := range []string{
		"type y{a func()}\nfunc e() int { let mut n = 0; _ = y{a: {func() { n = 1 }}}; n }",
		"type y{}\nfunc e() int { let mut n = 0; _ = y{{func() { n = 1 }}}; n }",
		"type y{a int}\nfunc e() int { let mut n = 0; _ = y{a: 1, b: {func() { n = 1 }}}; n }",
		"type y{a int}\nfunc e() int { let mut n = 0; _ = y{a: 1, a: {func() { n = 1 }}}; n }",
	} {
		cases = append(cases, d1Case{name: src, src: src})
	}
	for _, tc := range cases {
		_, ds := check.CheckSource(tc.src)
		if p := check.RefWalkProblems(tc.src); len(p) > 0 {
			t.Errorf("%s: reference walk: %s", tc.name, strings.Join(p, "; "))
		}
		for _, d := range ds {
			if strings.Contains(d.Message, "never reassigned") {
				t.Errorf("%s: false advice from a skipped reassignment: %s", tc.name, describe(ds))
			}
		}
	}
}
