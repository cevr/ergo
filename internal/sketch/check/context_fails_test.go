package check_test

import (
	"fmt"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestContextFailsIsRefused: a context header's fails row, which the parser
// reads (CTX-ROWS-1b), is S109 at the row until CTX-ROWS-2 checks it, on a
// named context and on a literal. Mutants only it kills
// (testdata/mutants.json): ctxf-named-unrefused, ctxf-lit-unrefused.
func TestContextFailsIsRefused(t *testing.T) {
	const why = "S109 a context's fails clause is CTX-ROWS-2's (the context header, rows.md Context 8); it is not checked yet"
	cases := []struct {
		name string
		src  string
		want []string // "line:column code message", in order
	}{
		{"a named context", `type Missing {}

let Live = context fails Missing {}
`, []string{"3:20 " + why}},
		{"a literal", `type Missing {}

func f() {
	with context fails Missing {} {}
}
`, []string{"4:15 " + why}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ds := check.CheckSource(tc.src)
			var got []string
			for _, d := range ds {
				got = append(got, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("diagnostics:\n%v\nwant:\n%v", got, tc.want)
			}
		})
	}
}
