package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// Stage D3a rewrite (check-v1): one assignability relation in Go's clause
// order (types.md, Assignability), whose admitting clause yields the
// lowering fact; a defined type only over a basic, slice, Map, function,
// tuple or record type; and repair texts that check when fed back. The
// lowering runs the admitted forms (internal/sketch/lower
// stage_d3a_assign_test.go).

// Each clause admits its witnesses, and two named types cross by none of
// them unless clause 3 or 4 relates them.
func TestAssignabilityClauses(t *testing.T) {
	runD1(t, []d1Case{
		// Clause 2: identical underlying types, one side unnamed.
		{"slice-out", "type Ints []int\nfunc f(xs Ints) []int { let raw []int = xs; raw }\n", nil},
		{"slice-in", "type Ints []int\nfunc f(xs []int) Ints { let named Ints = xs; named }\n", nil},
		{"map-out", "type Counts Map[string, int]\nfunc f(xs Counts) Map[string, int] { let raw Map[string, int] = xs; raw }\n", nil},
		{"map-in", "type Counts Map[string, int]\nfunc f(xs Map[string, int]) Counts { let named Counts = xs; named }\n", nil},
		{"tuple-out", "type Pair (int, string)\nfunc f(xs Pair) (int, string) { let raw (int, string) = xs; raw }\n", nil},
		{"tuple-in", "type Pair (int, string)\nfunc f(xs (int, string)) Pair { let named Pair = xs; named }\n", nil},
		{"function-out", "type F func(v int) int\nfunc f(xs F) func(v int) int { let raw func(v int) int = xs; raw }\n", nil},
		{"function-in", "type F func(v int) int\nfunc f(xs escaping func(v int) int) F { let named F = xs; named }\n", nil},
		{"argument", "type Ints []int\nfunc g(xs []int) int { len(xs) }\nfunc f(xs Ints) int { g(xs) }\n", nil},
		{"field", "type Ints []int\ntype Box { xs []int }\nfunc f(xs Ints) Box { Box{xs: xs} }\n", nil},
		{"element", "type Ints []int\nfunc f(xs Ints) [][]int { [][]int{xs} }\n", nil},
		{"compare", "type Pair (int, string)\nfunc f(p Pair, q (int, string)) bool { p == q }\n", nil},
		// Two named types: no clause.
		{"two-named-slices", "type A []int\ntype B []int\nfunc f(x A) B { x }\n", []string{"S108:3"}},
		{"two-defined-records", "type Mem { N int }\ntype A Mem\ntype B Mem\nfunc f(x A) B { x }\n", []string{"S108:4"}},
		{"defined-record-to-base", "type Mem { N int }\ntype Mem2 Mem\nfunc f(x Mem2) Mem { x }\n", []string{"S108:3"}},
		{"basic-out", "type Count int\nfunc f(x Count) int { x }\n", []string{"S108:2"}},
		{"basic-in", "type Count int\nfunc f(x int) Count { x }\n", []string{"S108:2"}},
		{"type-parameter", "func f[T](x T) int { x }\n", []string{"S108:1"}},
		{"different-elements", "type Ints []int\nfunc f(xs Ints) []int64 { xs }\n", []string{"S108:2"}},
		{"two-named-in-multi-let", "type A []int\ntype B []int\nfunc two() (A, A) { (A{1}, A{2}) }\nfunc f() int { let x, y B = two(); len(x) + len(y) }\n", []string{"S108:4"}},
		{"conversion-through-base", "type Mem { N int }\ntype A Mem\ntype B Mem\nfunc f(x A) B { B(Mem(x)) }\n", nil},
		// Clause 3: whatever the source's namedness.
		{"defined-basic-provider", "type Store { Get() int }\ntype Count int\nfunc Get(c Count) int { int(c) }\nfunc f() Store { Count(3) }\n", nil},
		{"defined-record-provider", "type Store { Get() int }\ntype Mem { N int }\ntype Mem2 Mem\nfunc Get(m Mem2) int { m.N }\nfunc f(m Mem2) Store { m }\n", nil},
		{"defined-slice-provider", "type Sized { Size() int }\ntype Ints []int\nfunc Size(xs Ints) int { len(xs) }\nfunc f(xs Ints) Sized { xs }\n", nil},
		{"provider-multi-let", "type Store { Get() int }\ntype Count int\nfunc Get(c Count) int { int(c) }\nfunc two() (Count, Count) { (Count(1), Count(2)) }\nfunc f() int { let a, b Store = two(); a.Get() + b.Get() }\n", nil},
		{"provider-compare", "type Store { Get() int }\ntype Count int\nfunc Get(c Count) int { int(c) }\nfunc f(s Store) bool { s == Count(1) }\n", []string{"S108:4"}},
		// Clause 4.
		{"variant-to-union", "func f() Option[int] { Some{1} }\n", nil},
		{"defined-variant-not-member", "type Other Some[int]\nfunc f(x Other) Option[int] { x }\n", []string{"S108:2"}},
		{"variant-conversion", "type Other Some[int]\nfunc f(x Other) Option[int] { Some[int](x) }\n", nil},
		{"variant-compare", "func f(o Option[int]) bool { o == None }\n", nil},
		// Clause 5: one side unnamed.
		{"signature-widening", "type Fault {}\nfunc seven() int { 7 }\nfunc f() int {\n\tlet g func() int fails Fault = seven\n\tg() catch { case Fault: 0 }\n}\n", nil},
		{"named-signature-widening", "type Fault {}\ntype G func() int\nfunc f(h G) int {\n\tlet g func() int fails Fault = h\n\tg() catch { case Fault: 0 }\n}\n", nil},
		{"two-named-signatures", "type F func(n int) int\ntype G func(n int) int\nfunc f(x F) G { x }\n", []string{"S108:3"}},
	})
}

// A defined type is over a basic, slice, Map, function, tuple or record
// type (types.md, Defined types 1). Any other base is one S108 at the
// definition naming the base, and the type's uses (a parameter, a let, a
// method call, a conversion, a type defined over it) add nothing.
func TestDefinedTypeBase(t *testing.T) {
	runD1(t, []d1Case{
		{"operation-type", "type Store { Get() int }\ntype MyStore Store\nfunc f(s MyStore) int {\n\tlet a MyStore = s\n\t_ = MyStore(s)\n\ta.Get()\n}\n", []string{"S108:2"}},
		{"generic-operation-instance", "type Getter[T] { Get() T }\ntype IntGetter Getter[int]\nfunc f(g IntGetter) int { g.Get() }\n", []string{"S108:2"}},
		{"union", "type A {}\ntype B {}\ntype U = A | B\ntype MyU U\nfunc f(u MyU) MyU { let v = u; v }\n", []string{"S108:4"}},
		{"union-instance", "type MyOpt Option[int]\nfunc f(o MyOpt) MyOpt { let b = o; b }\nfunc g() MyOpt { Some{1} }\n", []string{"S108:1"}},
		{"task", "type Fault {}\ntype MyTask Task[int, Fault]\nfunc f(t MyTask) int fails Fault { t.Wait() }\n", []string{"S108:2"}},
		{"std-operation", "import queue\ntype MyQ queue.Queue[int]\nfunc f(q MyQ) { q.End() }\n", []string{"S108:2"}},
		{"over-a-refused-type", "type Store { Get() int }\ntype A Store\ntype B A\nfunc f(b B) int { b.Get() }\n", []string{"S108:2"}},
		// Allowed bases.
		{"basic", "type Count int\nfunc f(c Count) int { int(c) }\n", nil},
		{"record", "type Mem { N int }\ntype Mem2 Mem\nfunc f(m Mem2) int { m.N }\n", nil},
		{"record-instance", "type Pair[A, B] { First A, Second B }\ntype P2 Pair[int, int]\nfunc f(p P2) int { p.First + p.Second }\n", nil},
		{"variant-record", "type S2 Some[int]\nfunc f(s S2) int { s.Value }\n", nil},
		{"slice-map-function-tuple", "type Ints []int\ntype Counts Map[string, int]\ntype F func(n int) int\ntype Pair (int, string)\nfunc f(a Ints, b Counts, c F, d Pair) int { let (n, _) = d; len(a) + len(b) + c(1) + n }\n", nil},
	})
	_, d := check.CheckSource("type Store { Get() int }\ntype MyStore Store\n")
	want := "a defined type is over a basic, slice, Map, function, tuple or record type, and Store is an operation type; use Store directly"
	if len(d) != 1 || d[0].Line != 2 || d[0].Column != 14 || !strings.Contains(d[0].Message, want) {
		t.Fatalf("want one S108 at 2:14 naming Store, got %s", describe(d))
	}
}

// Every repair a diagnostic writes checks when fed back: the D-D8
// assignment and dot call, the conversion through an unnamed base, the
// conversion through a named base, and the assignment's conversion. A name
// already bound at the site moves the repair to raw2.
func TestRepairTextsCheck(t *testing.T) {
	for _, tc := range []struct {
		name, head, line, tail string
		after, before          string
	}{
		{"append", "type Ints []int\nfunc f(xs Ints) {\n", "\t_ = xs.Append(1)\n", "}\n", "after assignment: ", ""},
		{"get", "type Counts Map[string, int]\nfunc f(m Counts) {\n", "\t_ = m.Get(\"a\")\n", "}\n", "after assignment: ", ""},
		{"delete", "type Counts Map[string, int]\nfunc f(m Counts) {\n", "\t_ = m.Delete(\"a\")\n", "}\n", "after assignment: ", ""},
		{"append-raw-bound", "type Ints []int\nfunc f(xs Ints, raw int) {\n", "\t_ = xs.Append(raw)\n", "}\n", "after assignment: ", ""},
		{"unnamed-base", "type Ints []int\ntype Other []int\nfunc f(x Ints) Other {\n", "\tOther(x)\n", "}\n", "defined over: ", " (types.md"},
		{"unnamed-base-raw-bound", "type Ints []int\ntype Other []int\nlet raw = 1\nfunc f(x Ints) Other {\n", "\tOther(x)\n", "}\n", "defined over: ", " (types.md"},
		{"tuple-base", "type P (int, int)\ntype Q (int, int)\nfunc f(x P) Q {\n", "\tQ(x)\n", "}\n", "defined over: ", " (types.md"},
		{"named-base", "type Celsius float64\ntype Temp Celsius\nfunc f(x float64) Temp {\n", "\tTemp(x)\n", "}\n", "defined over: ", " (types.md"},
		{"assignment", "type Mem { N int }\ntype Mem2 Mem\nfunc f(x Mem2) Mem {\n", "\tx\n", "}\n", "write ", " or declare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.head + tc.line + tc.tail)
			if len(d) != 1 {
				t.Fatalf("want one diagnostic, got %s", describe(d))
			}
			_, repair, ok := strings.Cut(d[0].Message, tc.after)
			if tc.before != "" {
				repair, _, _ = strings.Cut(repair, tc.before)
			}
			if !ok || repair == "" {
				t.Fatalf("message has no repair: %s", d[0].Message)
			}
			if strings.HasSuffix(tc.name, "raw-bound") && !strings.Contains(repair, "raw2") {
				t.Fatalf("want the repair under raw2: %s", repair)
			}
			var fixed strings.Builder
			fixed.WriteString(tc.head)
			for _, stmt := range strings.Split(repair, "; then ") {
				if strings.HasSuffix(stmt, "(…)") {
					// The D-D8 call keeps the original call's arguments.
					_, args, _ := strings.Cut(tc.line, "(")
					stmt = "_ = " + strings.TrimSuffix(stmt, "(…)") + "(" + strings.TrimSuffix(args, "\n")
				}
				fixed.WriteString("\t" + stmt + "\n")
			}
			fixed.WriteString(tc.tail)
			if _, diags := check.CheckSource(fixed.String()); len(diags) != 0 {
				t.Fatalf("the repair %q does not check: %s\n%s", repair, describe(diags), fixed.String())
			}
		})
	}
}
