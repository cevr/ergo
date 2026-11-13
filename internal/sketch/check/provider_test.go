package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestProviderDerive: a user package's derived provider gets what
// testing.Script gets, and the plan's refusals (patterns.md, Provider
// plans 1 to 3). Mutants: the provider rules keyed on testing.Script's name
// again (provider-by-name: the user's rec[probe] is an empty record, so the
// Answer call is refused), the field type's Go contract never checked
// (provider-field-unchecked: MyOp is not refused), and type parameters
// admitted on any plan (provider-generic-any-plan). What a
// provider records and answers is the testing laws' (TestStdLawsRun) and
// the particles example's, which uses its own recorder.
func TestProviderDerive(t *testing.T) {
	const head = "import testing\n\ntype probe { Len() int }\ntype Box { n int }\n"
	const rec = "type rec[S] derive Provider(Of[S](), Of[testing.Op](), Of[testing.NoArgs]())\n"
	rows := []struct{ name, src, want string }{
		{"user provider", head + rec + "func f() probe {\n\tlet s = rec[probe]{}\n\ts.Len.Answer(func() int { 3 })\n\ts\n}\n", ""},
		{"instance rule", head + rec + "func f(s rec[Box]) {}\n", "S108:6 rec[Box]: Box is not a capability"},
		{"generic without Provider", head + "type T[S] derive Pattern(\"a\")\n", "S001:5 a derived type has no type parameters"},
		{"capability not the parameter", head + "type rec[S] derive Provider(Of[probe](), Of[testing.Op](), Of[testing.NoArgs]())\n", "S108:5 a Provider derives rec from the operations of its one type parameter"},
		{"field type with no Go type", head + "type MyOp[A, F] {}\ntype rec[S] derive Provider(Of[S](), Of[MyOp](), Of[testing.NoArgs]())\n", "S108:6 its field type MyOp names no runtime Go type"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, d := check.CheckSource(r.src)
			if r.want == "" {
				if len(d) > 0 {
					t.Fatalf("want no diagnostics: %s", describe(d))
				}
				return
			}
			head, msg, _ := strings.Cut(r.want, " ")
			for _, x := range d {
				if x.Code+":"+strconv.Itoa(x.Line) == head && strings.Contains(x.Message, msg) {
					return
				}
			}
			t.Fatalf("want %s: %s", r.want, describe(d))
		})
	}
}
