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
// Answer call is refused), type parameters admitted on any plan
// (provider-generic-any-plan), and no Provider plan read
// (provider-plan-unread). An Ergo field
// type (Provider plans 3): refused like one that names no runtime type
// (provider-ergo-field-refused), admitted unexported
// (provider-ergo-unexported-admitted), its Call's shape never checked
// (provider-ergo-shape-unchecked), and its pair's row never checked
// (provider-ergo-row-unchecked). What a
// provider records and answers is the testing laws' (TestStdLawsRun) and
// the particles example's, which uses its own recorder.
func TestProviderDerive(t *testing.T) {
	const head = "import testing\n\ntype probe { Len() int }\ntype Box { n int }\n"
	// An Ergo field type, Rec, its NewRec and Answer, then line 7 (its Call)
	// and the provider M at line 9.
	ergo := func(call, use string) string {
		return "import sync\n\ntype probe { Len() int }\n" +
			"type Rec[A, G] { calls sync.Ref[[]A], answer sync.Ref[Option[G]] }\n" +
			"func NewRec[A, G]() Rec[A, G] { Rec[A, G]{calls: sync.NewRef[[]A]([]A{}), answer: sync.NewRef[Option[G]](None)} }\n" +
			"func Answer[A, G](r Rec[A, G], f G) { r.answer.Set(Some{f}) }\n" +
			call + "\n" +
			"type None0 {}\n" +
			"type M[S] derive Provider(Of[S](), Of[Rec](), Of[None0]())\n" + use
	}
	const call = "func Call[A, G](r Rec[A, G], a A) Option[G] { r.answer.Get() }"
	const use = "func f() probe {\n\tlet m = M[probe]{}\n\tm.Len.Answer(func() int { 3 })\n\tm\n}\n"
	const rec = "type rec[S] derive Provider(Of[S](), Of[testing.Op](), Of[testing.NoArgs]())\n"
	rows := []struct{ name, src, want string }{
		{"user provider", head + rec + "func f() probe {\n\tlet s = rec[probe]{}\n\ts.Len.Answer(func() int { 3 })\n\ts\n}\n", ""},
		{"instance rule", head + rec + "func f(s rec[Box]) {}\n", "S108:6 rec[Box]: Box is not a capability"},
		{"generic without Provider", head + "type T[S] derive Pattern(\"a\")\n", "S001:5 a derived type has no type parameters"},
		{"capability not the parameter", head + "type rec[S] derive Provider(Of[probe](), Of[testing.Op](), Of[testing.NoArgs]())\n", "S001:5 a Provider derives rec from the operations of its one type parameter"},
		{"field type with no Go type", head + "type MyOp[A, F] {}\ntype rec[S] derive Provider(Of[S](), Of[MyOp](), Of[testing.NoArgs]())\n", "S108:6 its field type MyOp names no runtime Go type"},
		{"Ergo field type", ergo(call, use), ""},
		{"Ergo field type with no Call", ergo("func Other() {}", ""), "S108:9 its package declares no func Call[A, G]"},
		{"Ergo Call of another shape", ergo("func Call[A, G](r Rec[A, G]) Option[G] { r.answer.Get() }", ""), "S108:9 its field type's Call is not func Call[A, G](r Rec[A, G], a A) Option[G]"},
		{"Ergo Call with a row", ergo("func Call[A, G](r Rec[A, G], a A) Option[G] fails Gone { r.answer.Get() }", "type Gone {}\n"), "S108:9 its field type's Call has a row"},
		{"Ergo field type unexported", "type probe { Len() int }\ntype rec[A, G] { n int }\ntype None0 {}\ntype M[S] derive Provider(Of[S](), Of[rec](), Of[None0]())\n", "S108:4 is declared in Ergo and not exported"},
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
