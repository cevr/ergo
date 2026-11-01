package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A refused element never invalidates its list: it stands as invalid and
// the valid elements stay, so the definition's one diagnostic is the only
// one in the file (rewrite round 2 review M1). Line 2 is the one refused
// definition: DStore, or DU in a case that names DU.
const listsPrelude = "type Store { Get() int }\n" +
	"type A {}\n" +
	"type B {}\n" +
	"type U = A | B\n" +
	"type Log { Put() int }\n" +
	"type L {}\n" +
	"func Put(l L) int { 1 }\n" +
	"type Mem {}\n" +
	"func Get(m Mem) int { 1 }\n" +
	"type C {}\n" +
	"type Fault {}\n" +
	"type Box[T] { V T }\n"

// listsSource puts the refused definition the body names on line 2.
func listsSource(body string) string {
	def := "type DStore Store\n"
	if strings.Contains(body, "DU") {
		def = "type DU U\n"
	}
	return "// lists\n" + def + listsPrelude + body
}

func TestRefusedListElementKeepsSiblings(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		// Rows (the M1 witnesses).
		{"needs-first", "func f() int needs DStore, Log { Log.Put() }\nfunc main() { with wire { Log: L } { _ = f() } }\n"},
		{"needs-last", "func f() int needs Log, DStore { Log.Put() }\n"},
		{"fails-try", "func g() int fails Fault { 1 }\nfunc f() int fails DStore | Fault { try g() }\n"},
		{"fails-catch", "func g() int fails DStore | Fault { 1 }\nfunc f() int { g() catch {\ncase Fault: 2\n} }\n"},
		{"fails-try-and-catch", "func g() int fails C { 1 }\nfunc f() int fails DStore | Fault | C { try g() }\nfunc h() int { f() catch {\ncase Fault: 1\ncase C: 2\n} }\n"},
		// An unknown written row may hold what escapes or is charged, as an
		// unknown destination row accepts any row (clause 5).
		{"fails-unknown-row-escape", "func g() int fails C { 1 }\nfunc F() int fails DStore | Fault { try g() }\n"},
		{"needs-unknown-row-charge", "func k() int needs Store { Store.Get() }\nfunc F() int needs DStore, Log { k() }\n"},
		{"operation-method-row", "type Ops2 { Run() int fails DStore | C }\nfunc Run(m Mem) int fails Fault { 1 }\nfunc f() Ops2 { Mem }\n"},
		{"literal-written-row", "func g() int fails C { 1 }\nfunc f() int { let h = func() int fails DStore { try g() }\nh() catch {\ncase _: 1\n} }\n"},
		{"fails-generic-try", "func g() int fails C { 1 }\nfunc f() int fails Box[DStore] | C { try g() }\n"},
		{"fails-generic-catch", "func g() int fails Box[DStore] | C { 1 }\nfunc f() int { g() catch {\ncase C: 2\n} }\n"},
		{"fails-generic-alone-catch", "func g() int fails Box[DStore] { 1 }\nfunc f() int { g() catch {\ncase _: 2\n} }\n"},
		{"functype-result", "func k() int needs Log { Log.Put() }\nfunc mk() func() int needs DStore, Log { k }\n"},
		{"functype-into-wider", "func k() int needs Log { Log.Put() }\nfunc f() int needs Log, Store {\nlet h func() int needs DStore, Log = k\nlet w func() int needs Log, Store = h\nw()\n}\n"},
		{"functype-from-wider", "func k() int needs Log, Store { Log.Put() }\nfunc f() int needs Log, Store {\nlet h func() int needs DStore, Log = k\nh()\n}\n"},
		{"functype-errors", "func g() int fails C { 1 }\nfunc f(h func() int fails DStore | C) int fails C { try h() }\nfunc k() int { f(g) catch {\ncase C: 1\n} }\n"},
		// Catch type-test patterns over a refused type.
		{"catch-total-refused-arg", "func g() int fails C | Box[Store] { 1 }\nfunc f() int { g() catch {\ncase C: 1\ncase Box[DStore]: 2\n} }\n"},
		{"catch-total-refused-name", "func g() int fails C | Fault { 1 }\nfunc f() int { g() catch {\ncase C: 1\ncase DU: 2\n} }\n"},
		{"catch-arm-two-types", "func g() int fails C | Fault { 1 }\nfunc f() int { g() catch {\ncase Box[DStore], C: 1\ncase Fault: 2\n} }\n"},
		// Every other list a type expression builds.
		{"type-args", "type P[K, V] { K K, V V }\nfunc f(p P[DStore, int]) int { p.V }\n"},
		{"map-type-args", "func f(m Map[string, DStore]) int { len(m) }\n"},
		{"tuple-type", "func g() (DStore, int) { g() }\nfunc f() int { let (_, n) = g()\nn }\n"},
		{"union-variants", "type U2 = A | Box[DU]\nfunc f(x U2) int { match x {\ncase A: 1\n} }\n"},
		{"union-variants-total", "type U2 = A | Box[DU] | B\nfunc f(x U2) int { match x {\ncase A: 1\ncase B: 2\n} }\n"},
		{"wire-header", "func f() int needs Log { Log.Put() }\nfunc main() { with wire { DStore: Mem, Log: L } { _ = f() } }\n"},
		{"row-arg-func", "func k[E](a int) int fails E { a }\nfunc f() int { k[Box[DStore]](1) catch {\ncase _: 2\n} }\n"},
		{"row-arg-type", "type R[E] { Run() int fails E }\nfunc f(r R[Box[DStore]]) int { 1 }\n"},
		{"params", "func f(s DStore, n int) int { n }\nfunc g() int { f(Mem, 2) }\n"},
		{"record-fields", "type Rec { S DStore, N int }\nfunc f(r Rec) int { r.N }\n"},
		{"operation-methods", "type Ops { Take(s DStore) int, N() int }\nfunc f(o Ops) int { o.N() }\n"},
		{"match-arm-two-types", "func f(x U) int { match x {\ncase DU, A: 1\ncase B: 2\n} }\n"},
		{"match-arm-two-types-rev", "func f(x U) int { match x {\ncase A, DU: 1\ncase B: 2\n} }\n"},
		{"catch-arm-two-types-rev", "func g() int fails C | Fault { 1 }\nfunc f() int { g() catch {\ncase C, DU: 1\ncase Fault: 2\n} }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(listsSource(tc.src))
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 2 || !strings.Contains(d[0].Message, "a defined type is over") {
				t.Fatalf("want one definition S108:2, got %s", describe(d))
			}
		})
	}
}

// The valid elements stay evidence: a row keeps its known part, so a
// missing capability or an unhandled error it names is still reported.
func TestRefusedListElementKeepsEvidence(t *testing.T) {
	for _, tc := range []struct{ name, src, want, code string }{
		{"needs-caller-misses-log", "func g() int needs DStore, Log { Log.Put() }\nfunc main() { _ = g() }\n", "main.Log", check.CodeMissingCap},
		{"fails-caller-misses-c", "func g() int fails DStore | C { 1 }\nfunc main() { _ = g() }\n", "main.C", check.CodeUnhandled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(listsSource(tc.src))
			if len(d) != 2 || d[0].Line != 2 || d[1].Code != tc.code || !strings.Contains(d[1].Message, tc.want) {
				t.Fatalf("want the definition S108:2 and %s naming %s, got %s", tc.code, tc.want, describe(d))
			}
		})
	}
}
