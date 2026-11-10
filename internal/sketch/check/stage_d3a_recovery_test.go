package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

func TestTupleReturnRepairChecks(t *testing.T) {
	const prefix = "type Mem { N int }\ntype Store { Get() int }\nfunc Get(m Mem) int { m.N }\nfunc pair() (Mem, int) { (Mem{N: 1}, 2) }\n" +
		"func pairOf(a int, b int) (Mem, int) { (Mem{N: a}, b) }\nfunc pairBy(g func() int, s string) (Mem, int) { (Mem{N: g()}, 1) }\n" +
		"func id[T](x T) T { x }\nfunc gen[T](x T) (Mem, int) { (Mem{N: 1}, 2) }\nfunc Pair(m Mem, a int) (Mem, int) { (m, a) }\n"
	// A call on one line is repaired with its source text copied byte for
	// byte; the suggested text checks when fed back unchanged.
	for _, tc := range []struct{ body, params, repair string }{
		{"return pair()", "", "let (m, n) = pair(); return (m, n)"},
		{"pair()", "", "let (m, n) = pair(); return (m, n)"},
		{"return pair()", "m int, n int", "let (m2, n2) = pair(); return (m2, n2)"},
		{"pairBy(func() int { let k = 1; k }, \"a;b\\nc\")", "", "let (m, n) = pairBy(func() int { let k = 1; k }, \"a;b\\nc\"); return (m, n)"},
		{"return pairOf(id[int](1),  id[int](2))", "", "let (m, n) = pairOf(id[int](1),  id[int](2)); return (m, n)"},
		{"gen[int](id[int](1))", "", "let (m, n) = gen[int](id[int](1)); return (m, n)"},
		{"return k.Pair(2)", "k Mem", "let (m, n) = k.Pair(2); return (m, n)"},
		{"(Mem{N: 1}).Pair(id[int](2))", "", "let (m, n) = (Mem{N: 1}).Pair(id[int](2)); return (m, n)"},
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
	// A call written over more than one line gets the plain message, with
	// no repair (diagnostics.md S108; stage D3a rewrite r3 review M1). The
	// last four are the review's inputs that the old re-spacing broke.
	for _, body := range []string{
		"return pairOf(\n\t\t1,\n\t\t2,\n\t)",
		"pairOf(1,\n\t2)",
		"pairOf(1,\r\n\t2)",
		"pairBy(func() int {\n\t\tlet k = 1\n\t\tk\n\t}, \"a\\nb\")",
		"pairOf(\n\t\t1, // one\n\t\t2,\n\t)",
		"pairBy(func() int { 1 }, `a\nb`)",
		"pairBy(func\n() int { 1 }, \"s\")",
		"pairBy(func() int { if\ntrue { 1 } else { 2 } }, \"s\")",
		"pairBy(func() int { let\nx = 1\nx }, \"s\")",
		"pairBy(func() int {\nlet café = 1\nlet mut n = café\nn = café\nn\n}, \"s\")",
	} {
		src := prefix + "func f() (Store, int) { " + body + " }\n"
		_, d := check.CheckSource(src)
		if len(d) != 1 || d[0].Code != check.CodeType || !strings.HasPrefix(d[0].Message, "cannot use (Mem, int) as (Store, int) in ") || strings.Contains(d[0].Message, "write ") {
			t.Errorf("%q: want one S108 with no repair, got %s", body, describe(d))
		}
	}
}
