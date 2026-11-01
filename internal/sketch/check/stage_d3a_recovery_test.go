package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A refused definition has one diagnostic. Its name resolves as invalid
// in every type/value context, including contexts outside assignment.
func TestRefusedDefinedTypeUsesStayQuiet(t *testing.T) {
	const cap = "type Store { Get() int }\ntype DStore Store\n"
	const union = "type Mem {}\ntype Other {}\ntype U = Mem | Other\ntype DU U\n"
	for _, tc := range []struct {
		name, src string
		line      int
	}{
		{"needs-row", cap + "func f() needs DStore {}\n", 2},
		{"capability-selector", cap + "func f() { DStore.Get() }\n", 2},
		{"wire-key", cap + "type Mem {}\nfunc Get(m Mem) int { 1 }\nfunc f() { with wire { DStore: Mem } {} }\n", 2},
		{"match-subject", union + "func f(x DU) int { match x {\ncase Mem: 1\ncase Other: 2\n} }\n", 4},
		{"match-pattern", cap + "func f(x Store) { match x {\ncase DStore: _ = x\ncase _: _ = x\n} }\n", 2},
		{"union-pattern", cap + "type Mem {}\ntype Other {}\ntype U = Mem | Other\nfunc f(x U) { match x {\ncase DStore: _ = x\ncase _: _ = x\n} }\n", 2},
		{"fails-row", cap + "func f() fails DStore {}\n", 2},
		{"constraint", cap + "func f[T DStore](x T) { _ = x }\n", 2},
		{"generic-base", "type Store[T] { Get() T }\ntype DStore[T] Store[T]\nfunc f(x DStore[int]) { _ = x }\n", 2},
		{"nested-types", cap + "type Box[T] { V T }\nfunc f(x Box[[]DStore]) Box[[]DStore] { x }\n", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != tc.line || !strings.Contains(d[0].Message, "a defined type is over") {
				t.Fatalf("want one definition S108:%d, got %s", tc.line, describe(d))
			}
		})
	}
}

func TestRefusedImportAliasIsNotUnused(t *testing.T) {
	for _, src := range []string{
		"import t task\n",
		"import t timeout\n",
	} {
		_, d := check.CheckSource(src)
		if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 1 || !strings.Contains(d[0].Message, "an import alias is only for a name clash") {
			t.Fatalf("%s: want one alias S108:1, got %s", src, describe(d))
		}
	}
	_, d := check.CheckSource("import task\n")
	if len(d) != 1 || d[0].Code != check.CodeUnused {
		t.Fatalf("valid unused import: want S110, got %s", describe(d))
	}
}

func TestTaskAssignmentNamesMissingOperation(t *testing.T) {
	const src = "type Fault {}\ntype Store { Get() int }\nfunc f(t Task[int, Fault]) Store {\n\tt\n}\n"
	_, d := check.CheckSource(src)
	if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 4 || !strings.Contains(d[0].Message, "has no method Get") {
		t.Fatalf("want one S108:4 naming the missing Get operation, got %s", describe(d))
	}
}

func TestTupleReturnRepairChecks(t *testing.T) {
	const prefix = "type Mem { N int }\ntype Store { Get() int }\nfunc Get(m Mem) int { m.N }\nfunc pair() (Mem, int) { (Mem{N: 1}, 2) }\n" +
		"func pairOf(a int, b int) (Mem, int) { (Mem{N: a}, b) }\nfunc pairBy(g func() int, s string) (Mem, int) { (Mem{N: g()}, 1) }\n"
	for _, tc := range []struct{ body, params, repair string }{
		{"return pair()", "", "let (m, n) = pair(); return (m, n)"},
		{"pair()", "", "let (m, n) = pair(); return (m, n)"},
		{"return pair()", "m int, n int", "let (m2, n2) = pair(); return (m2, n2)"},
		// A call over several lines is repaired on one line (m2): a break
		// after an opening bracket, before a closing one or after a comma
		// goes; a break that ends a statement in a block becomes "; ".
		{"return pairOf(\n\t\t1,\n\t\t2,\n\t)", "", "let (m, n) = pairOf(1, 2); return (m, n)"},
		{"pairOf(1,\n\t2)", "", "let (m, n) = pairOf(1, 2); return (m, n)"},
		{"pairBy(func() int {\n\t\tlet k = 1\n\t\tk\n\t}, \"a\\nb\")", "", "let (m, n) = pairBy(func() int {let k = 1; k}, \"a\\nb\"); return (m, n)"},
	} {
		src := prefix + "func f(" + tc.params + ") (Store, int) { " + tc.body + " }\n"
		_, d := check.CheckSource(src)
		if len(d) != 1 || d[0].Code != check.CodeType || !strings.HasSuffix(d[0].Message, "; write "+tc.repair) {
			t.Fatalf("%s: want one S108 ending in %q, got %s", src, "; write "+tc.repair, describe(d))
		}
		// The exact suggested text replaces the body and checks.
		at := strings.LastIndex(src, tc.body)
		repaired := src[:at] + tc.repair + src[at+len(tc.body):]
		if _, rd := check.CheckSource(repaired); len(rd) != 0 {
			t.Fatalf("suggested repair does not check: %s\n%s", repaired, describe(rd))
		}
	}
	// A call that one line cannot keep (a comment, a raw string over
	// lines) gets the plain message, with no repair (diagnostics.md).
	for _, body := range []string{
		"pairOf(\n\t\t1, // one\n\t\t2,\n\t)",
		"pairBy(func() int { 1 }, `a\nb`)",
	} {
		src := prefix + "func f() (Store, int) { " + body + " }\n"
		_, d := check.CheckSource(src)
		if len(d) != 1 || d[0].Code != check.CodeType || strings.Contains(d[0].Message, "write ") {
			t.Fatalf("%s: want one S108 with no repair, got %s", src, describe(d))
		}
	}
}
