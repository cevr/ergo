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

func wantMessages(t *testing.T, name, src string, msgs ...string) {
	t.Helper()
	_, ds := check.CheckSource(src)
	all := describe(ds)
	for _, m := range msgs {
		if !strings.Contains(all, m) {
			t.Errorf("%s: want %q, got %s", name, m, all)
		}
	}
	for _, d := range ds {
		if strings.Contains(d.Message, "invalid") {
			t.Errorf("%s: invalid printed: %s", name, all)
		}
	}
}

// capDecls declares walk, a class G member refused for its capability row
// only (line 5): its error row {Bang} is written, so it is evidence, and a
// caller's catch and try are checked against it.
const capDecls = "type Cap { M() }\ntype Bang {}\ntype Other {}\nfunc mark() fails Bang { return Bang }\n" +
	"func walk() int fails Bang { let m = Map[int, func() int fails Bang]{1: walk}; _ = m; Cap.M(); try mark(); 1 }\n"

// M1: only an unknown row holds invalid. A member refused for its
// capability row keeps its written error row, and an inferred error row
// whose pinned empty row held stays empty, so the errors a caller makes
// against them are independent and reported. The needs Cap control
// reports the same errors with no refusal; the error-row refusal keeps a
// written capability row.
func TestOnlyTheRefusedRowIsUnknown(t *testing.T) {
	const callers = "func F() int { walk() catch { case Bang: 1; case Other: 2 } }\n" +
		"func G() int { walk() }\n" +
		"func H() int fails Bang { try walk() catch { case Bang: 1 } }\n"
	written := strings.Replace(capDecls, "func walk() int fails Bang {", "func walk() int fails Bang needs Cap {", 1)
	written = strings.Replace(written, "func() int fails Bang]", "func() int fails Bang needs Cap]", 1)
	writtenCallers := strings.NewReplacer("func F() int {", "func F() int needs Cap {", "func G() int {", "func G() int needs Cap {", "func H() int fails Bang {", "func H() int fails Bang needs Cap {").Replace(callers)
	cases := []d1Case{
		// p01: the catch and the try read the written {Bang}.
		{"caponly-written-error-row", capDecls + callers, []string{"S108:5", "S108:6", "S101:7", "S108:8"}},
		// p02: walk's inferred error row did not grow, so {} held.
		{"caponly-held-empty-error-row", "type Cap { M() }\nfunc walk() { let m = Map[int, func()]{1: walk}; _ = m; Cap.M() }\nfunc F() { try walk() }\n", []string{"S108:2", "S108:3"}},
		// p17: the same callers with needs Cap written: no refusal.
		{"needs-written-control", written + writtenCallers, []string{"S108:6", "S101:7", "S108:8"}},
		// An error-row refusal keeps the written capability row: G's
		// pinned empty row does not hold Cap.
		{"errors-only-written-capability-row", "type Cap { M() }\ntype Bang {}\nfunc mark() fails Bang { return Bang }\n" +
			"func w() needs Cap { let m = Map[int, func() needs Cap]{1: w}; _ = m; Cap.M(); try mark() }\n" +
			"func G() { w() }\n", []string{"S108:4", "S102:5"}},
	}
	runD1(t, cases)
	wantMessages(t, cases[0].name, cases[0].src,
		"Other is not in the caught row {Bang}",
		"{Bang} escapes function G",
		"try over a catch that handles every failure")
	wantMessages(t, cases[1].name, cases[1].src, "try on an expression that cannot fail")
	wantMessages(t, cases[3].name, cases[3].src, "requires Cap, which is not in the capability row of function G")
}

// targetDecls refuses a for its error row (line 3) and w for its
// capability row (line 6; its error row {Q} is written). q and qc are
// definite: q fails Q, qc fails Q and needs Cap2. pick is a generic
// identity of its first argument. Line 11 is the first program line.
const targetDecls = "type Bang {}\nfunc mark() fails Bang { return Bang }\n" +
	"func a(n int) int { let m = Map[int, func(int) int]{1: a}; _ = m; try mark(); n }\n" +
	"type Q {}\ntype Cap2 { N() }\n" +
	"func w(n int) int fails Q { let m = Map[int, func(int) int fails Q]{1: w}; _ = m; Cap2.N(); n }\n" +
	"func mq() fails Q { return Q }\n" +
	"func q(n int) int fails Q { try mq(); n }\n" +
	"func qc(n int) int fails Q needs Cap2 { Cap2.N(); try mq(); n }\n" +
	"func pick[T](x T, y T) T { x }\n"

// M2 and T2: an unknown row on the receiving side accepts any row, with no
// diagnostic, and an inferred receiver (an if join, a type parameter's
// binding) keeps the sender's known part, so a failure or a capability the
// sender is known to have still reaches the checks downstream. The
// generator puts each refused member (a: unknown error row; w: unknown
// capability row) first and second, against its definite partner (q, qc),
// in a join, an assignment, a generic argument and a contextual literal.
// The result is called unhandled in an exported function, whose rows are
// empty: the known Q escapes (S101) and the known Cap2 is missing (S102)
// whenever they flow. A let mut local keeps the type it was declared with,
// so when the refused member comes first its unknown row absorbs the
// partner's (decided by: an unknown row accepts; a local's type does not
// change after its declaration).
func TestUnknownRowOnTheReceivingSide(t *testing.T) {
	shapes := []struct {
		name, tmpl string
		fixed      bool // the receiver is a local declared from the first operand
	}{
		{"join", "func F(c bool) int { (if c { %X } else { %Y })(1) }", false},
		{"assign", "func F() int { let mut g = %X; g = %Y; g(1) }", true},
		{"generic", "func F() int { pick(%X, %Y)(1) }", false},
		{"contextual", "func F() int { let mut g = %X; g = func(n int) int { %TRY%Y(n) }; g(1) }", true},
	}
	members := []struct{ name, member, partner string }{
		{"errors", "a", "q"},
		{"capability", "w", "qc"},
	}
	var cases []d1Case
	for _, s := range shapes {
		for _, m := range members {
			for _, first := range []bool{true, false} {
				x, y := m.member, m.partner
				order := "member-first"
				if !first {
					x, y, order = y, x, "member-second"
				}
				// try is needed where the literal's body calls a callee
				// whose error row is known to be nonempty.
				try := ""
				if y != "a" {
					try = "try "
				}
				src := targetDecls + strings.NewReplacer("%X", x, "%Y", y, "%TRY", try).Replace(s.tmpl) + "\n"
				flows := !(s.fixed && first)
				// Both members' declarations are refused in every program.
				want := []string{"S108:3", "S108:6"}
				switch m.name {
				case "errors":
					if flows {
						want = append(want, "S101:11")
					}
				case "capability":
					// Q is written in both, so it always escapes.
					want = append(want, "S101:11")
					if flows {
						want = append(want, "S102:11")
					}
				}
				cases = append(cases, d1Case{s.name + "-" + m.name + "-" + order, src, want})
			}
		}
	}
	runD1(t, cases)
	for _, tc := range cases {
		_, ds := check.CheckSource(tc.src)
		for _, d := range ds {
			if strings.Contains(d.Message, "invalid") || strings.Contains(d.Message, "is wider than {}") {
				t.Errorf("%s: a follow-on of the unknown row: %s", tc.name, describe(ds))
			}
		}
	}
	// The reviewer's probes (p03, p07, p16): the known Q is named.
	wantMessages(t, "p07", targetDecls+"func F(c bool) int { (if c { a } else { q })(1) }\n", "{Q} escapes function F, whose error row {} does not list it")
	wantMessages(t, "p03", targetDecls+"func F(c bool) int fails Bang { try (if c { a } else { q })(1) }\n", "{Q} escapes function F, whose error row {Bang} does not list it")
	runD1(t, []d1Case{
		{"p16-contextual", targetDecls + "func F() int { let mut g = a; g = func(n int) int { try q(n) }; g(1) }\n", []string{"S108:3", "S108:6"}},
		{"p16-generic-try", targetDecls + "func H() int fails Q { try pick(a, q)(1) }\n", []string{"S108:3", "S108:6"}},
		{"p16-generic-try-second", targetDecls + "func K() int fails Q { try pick(q, a)(1) }\n", []string{"S108:3", "S108:6"}},
		// A literal whose expected row is unknown infers its own row, so
		// its known Q reaches the type parameter's binding.
		{"generic-literal", targetDecls + "func F() int { pick(a, func(n int) int { try q(n) })(1) }\n", []string{"S108:3", "S108:6", "S101:11"}},
		{"generic-literal-capability", targetDecls + "func F() int fails Q { try pick(w, func(n int) int { try qc(n) })(1) }\n", []string{"S108:3", "S108:6", "S102:11"}},
	})
}

// blockDecls refuses z for its error row (line 3) and zw for its capability
// row (line 6; its error row {Q} is written). Both take no parameters, so
// they fit a trailing block. run's block has the type of its first
// argument; mk returns a function that returns its argument. Line 10 is the
// first program line.
const blockDecls = "type Bang {}\nfunc mark() fails Bang { return Bang }\n" +
	"func z() int { let m = Map[int, func() int]{1: z}; _ = m; try mark(); 1 }\n" +
	"type Q {}\ntype Cap2 { N() }\n" +
	"func zw() int fails Q { let m = Map[int, func() int fails Q]{1: zw}; _ = m; Cap2.N(); 1 }\n" +
	"func mq() fails Q { return Q }\n" +
	"func run[T](x T, f T) T { x }\n" +
	"func mk[T](f T) func() T { func() T { f } }\n"

// M2 and T2 in two more target positions: a trailing block whose expected
// row is unknown infers that row, so its known Q or Cap2 reaches the type
// parameter's binding; and a join widens an unknown row nested in a result
// type, not only the outer one. Each Q or Cap2 named in a want is known, so
// its S101 or S102 stays.
func TestUnknownRowInBlockAndResultTargets(t *testing.T) {
	cases := []d1Case{
		{"block-errors", blockDecls + "func F() int { let g = run(z) { try mq(); 1 }; g() }\n", []string{"S108:3", "S108:6", "S101:10"}},
		{"block-capability", blockDecls + "func F() int fails Q { let g = run(zw) { Cap2.N(); try mq(); 1 }; try g() }\n", []string{"S108:3", "S108:6", "S102:10"}},
		{"nested-result", blockDecls + "func q0() int fails Q { try mq(); 1 }\nfunc F(c bool) int { (if c { mk(z) } else { mk(q0) })()() }\n", []string{"S108:3", "S108:6", "S101:11"}},
		{"nested-result-second", blockDecls + "func q0() int fails Q { try mq(); 1 }\nfunc F(c bool) int { (if c { mk(q0) } else { mk(z) })()() }\n", []string{"S108:3", "S108:6", "S101:11"}},
		// The nested rows differ whatever z's error row holds: z's
		// capability row is known and empty, and w0 needs Cap2, so the
		// results differ (an independent error) and stay reported.
		{"nested-result-independent", blockDecls + "func w0() int needs Cap2 { Cap2.N(); 1 }\nfunc F(c bool) int { let g = if c { mk(z) } else { mk(w0) }; 1 }\n", []string{"S108:3", "S108:6", "S108:11"}},
		// h's error row is unknown and holds the known Q (run widens z's
		// row by q0's); b0's row is {Bang}, which no content of h's row
		// equals, so the results differ here too.
		{"nested-known-part-independent", blockDecls + "func q0() int fails Q { try mq(); 1 }\nfunc b0() int fails Bang { try mark(); 1 }\nfunc F(c bool) int { let h = run(z, q0); let g = if c { mk(h) } else { mk(b0) }; 1 }\n", []string{"S108:3", "S108:6", "S108:12"}},
		{"nested-known-part-second-independent", blockDecls + "func q0() int fails Q { try mq(); 1 }\nfunc b0() int fails Bang { try mark(); 1 }\nfunc F(c bool) int { let h = run(z, q0); let g = if c { mk(b0) } else { mk(h) }; 1 }\n", []string{"S108:3", "S108:6", "S108:12"}},
		// ap(z) returns a function whose parameter type is z's, with its
		// unknown error row, and the call of it is not generic: a block or
		// a literal passed there infers its row, which the unknown row
		// accepts, so neither reports a follow-on S101 or S102.
		{"concrete-block-errors", blockDecls + "func ap[T](x T) func(f T) T { func(f T) T { f } }\nfunc F() int { _ = ap(z)() { try mq(); 1 }; 1 }\n", []string{"S108:3", "S108:6"}},
		{"concrete-block-capability", blockDecls + "func ap[T](x T) func(f T) T { func(f T) T { f } }\nfunc F() int { _ = ap(zw)() { Cap2.N(); try mq(); 1 }; 1 }\n", []string{"S108:3", "S108:6"}},
		{"concrete-literal-errors", blockDecls + "func ap[T](x T) func(f T) T { func(f T) T { f } }\nfunc F() int { _ = ap(z)(func() int { try mq(); 1 }); 1 }\n", []string{"S108:3", "S108:6"}},
		{"concrete-literal-capability", blockDecls + "func ap[T](x T) func(f T) T { func(f T) T { f } }\nfunc F() int { _ = ap(zw)(func() int fails Q { Cap2.N(); try mq(); 1 }); 1 }\n", []string{"S108:3", "S108:6"}},
	}
	runD1(t, cases)
	for _, tc := range cases {
		if strings.HasSuffix(tc.name, "-independent") {
			wantMessages(t, tc.name, tc.src, "the parameters or results differ")
		}
	}
	for _, tc := range cases {
		if strings.HasSuffix(tc.name, "-independent") {
			continue
		}
		_, ds := check.CheckSource(tc.src)
		for _, d := range ds {
			if strings.Contains(d.Message, "invalid") || strings.Contains(d.Message, "is wider than {}") || strings.Contains(d.Message, "results differ") {
				t.Errorf("%s: a follow-on of the unknown row: %s", tc.name, describe(ds))
			}
		}
	}
}

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

// T1: a value position handles nothing of an unknown row, so the row
// escapes to the enclosing function's inferred row (expr.go, handled): b's
// row is unknown, and the caller's try on it is a follow-on, not "try on an
// expression that cannot fail". It fails when that escape is deleted.
func TestHandledUnknownRowEscapes(t *testing.T) {
	runD1(t, []d1Case{
		{"let-binding", pinnedDecls + "func b() int { let x = a(1); x }\nfunc F() int fails Bang { try b() }\n", []string{"S108:3"}},
		{"argument", pinnedDecls + "func id(n int) int { n }\nfunc b() int { id(a(1)) }\nfunc F() int fails Bang { try b() }\n", []string{"S108:3"}},
	})
}
