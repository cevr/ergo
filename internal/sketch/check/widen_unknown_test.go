package check

import (
	"strings"
	"testing"
)

// TestWidenUnknownNestedResult is the smallest repro of widenUnknown's
// copy of a result list (typeops.go): the branches of a value if join two
// function values whose results are functions, the first with a row made
// unknown by an undefined name. The join widens the nested result, so the
// result list is copied before it is written. Mutant it kills that no
// in-process test does (its known killer, a corpus part, builds Go in the
// full tier only, so the mutant runner found it uncovered at EX2):
// t6-8-negate-if-typeops.go:457 (the copy skipped, an index panic).
func TestWidenUnknownNestedResult(t *testing.T) {
	const src = "type E {}\nfunc f(b bool) {\n\tlet h = if b {\n\t\tfunc() (func() int fails Undefined) { func() int fails Undefined { 1 } }\n\t} else {\n\t\tfunc() (func() int fails E) { func() int fails E { 1 } }\n\t}\n\th\n}\n"
	_, ds := checkFor(src)
	var got []string
	for _, d := range ds {
		got = append(got, d.String())
	}
	want := []string{
		"4:28: S104 unknown error type Undefined",
		"4:58: S104 unknown error type Undefined",
		"8:2: S110 this value is not used; an expression statement is a call, try, catch or with",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
