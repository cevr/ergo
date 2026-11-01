package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage D3a (check-v1): the prelude's functions on slices and Maps, Append,
// Get and Delete (D-D6), reached only by the dot form on a value of exactly
// an unnamed slice or Map type (D-D8, decision 9), through the one
// mechanism of a function on a declared type (196m), with the generic
// receiver's type arguments in Info (D-D8). Lowering runs the admitted
// forms (internal/sketch/lower stage_d3a_test.go).

// D-D6, D-D8: each admitted form checks with no diagnostic.
func TestPreludeDotCalls(t *testing.T) {
	runD1(t, []d1Case{
		{"append", "func f(xs []int) []int {\n\txs.Append(1)\n}\n", nil},
		{"append-empty-literal", "func f() []string {\n\t[]string{}.Append(\"a\")\n}\n", nil},
		{"append-generic-element", "func f[T](xs []T, x T) []T {\n\txs.Append(x)\n}\n", nil},
		{"append-chain", "func f(xs []int) int {\n\tlen(xs.Append(1).Append(2))\n}\n", nil},
		{"get", "func f(m Map[string, int]) int {\n\tmatch m.Get(\"a\") {\n\tcase Some{v}:\n\t\tv\n\tcase None:\n\t\t0\n\t}\n}\n", nil},
		{"get-generic", "func f[K comparable, V](m Map[K, V], k K) Option[V] {\n\tm.Get(k)\n}\n", nil},
		{"delete", "func f(m Map[string, int]) Map[string, int] {\n\tm.Delete(\"a\")\n}\n", nil},
		{"delete-assign", "func f() int {\n\tlet mut m = Map[string, int]{\"a\": 1}\n\tm = m.Delete(\"a\")\n\tlen(m)\n}\n", nil},
		{"delete-discarded", "func f(m Map[string, int]) {\n\t_ = m.Delete(\"a\")\n}\n", nil},
		{"element-of-slice-of-slices", "func f(xs [][]int) []int {\n\txs[0].Append(1)\n}\n", nil},
		// Decision 9: Append, Get and Delete are names in no scope, so a
		// program declares its own (the decided preview's func Get).
		{"own-get", "type Store { N int }\nfunc Get(s Store, k string) int {\n\ts.N + len(k)\n}\nfunc f(s Store, m Map[string, int]) int {\n\tlet n = s.Get(\"a\")\n\tmatch m.Get(\"a\") {\n\tcase Some{v}:\n\t\tv + n\n\tcase None:\n\t\tn\n\t}\n}\n", nil},
		{"own-append-plain", "func Append(a int, b int) int {\n\ta + b\n}\nfunc f() int {\n\tAppend(1, 2)\n}\n", nil},
		// D-D8: a generic first parameter instantiates from x.
		{"generic-receiver", "type Pair[A, B] { First A, Second B }\nfunc Swap[A, B](p Pair[A, B]) Pair[B, A] {\n\tPair[B, A]{First: p.Second, Second: p.First}\n}\nfunc f(p Pair[int, string]) string {\n\tp.Swap().First\n}\n", nil},
	})
}

// D-D6, D-D8: every refused form is one located diagnostic, and the
// message names the form to write.
func TestPreludeDotCallRefusals(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		text      string
	}{
		{"bare-get", "func f(m Map[string, int]) {\n\t_ = Get(m, \"a\")\n}\n", []string{"S104:2"}, "m.Get(k)"},
		{"bare-append", "func f(xs []int) {\n\t_ = Append(xs, 1)\n}\n", []string{"S104:2"}, "xs.Append(x)"},
		{"bare-delete", "func f(m Map[string, int]) {\n\t_ = Delete(m, \"a\")\n}\n", []string{"S104:2"}, "m.Delete(k)"},
		{"defined-slice", "type Ints []int\nfunc f(xs Ints) {\n\t_ = xs.Append(1)\n}\n", []string{"S104:3"}, "[]int(xs).Append(…)"},
		{"defined-map", "type M Map[string, int]\nfunc f(m M) {\n\t_ = m.Get(\"a\")\n}\n", []string{"S104:3"}, "Map[string, int](m).Get(…)"},
		{"no-call", "func f(xs []int) {\n\tlet g = xs.Append\n\t_ = g\n}\n", []string{"S104:2"}, "reached only by a call"},
		{"append-two", "func f(xs []int) {\n\t_ = xs.Append(1, 2)\n}\n", []string{"S108:2"}, ""},
		{"append-wrong-element", "func f(xs []int) {\n\t_ = xs.Append(\"a\")\n}\n", []string{"S108:2"}, ""},
		{"get-wrong-key", "func f(m Map[string, int]) {\n\t_ = m.Get(1)\n}\n", []string{"S108:2"}, ""},
		{"get-on-slice", "func f(xs []int) {\n\t_ = xs.Get(0)\n}\n", []string{"S104:2"}, "has no field or method Get"},
		{"append-on-map", "func f(m Map[string, int]) {\n\t_ = m.Append(1)\n}\n", []string{"S104:2"}, "has no field or method Append"},
		{"pointer-to-slice", "func f(xs *[]int) {\n\t_ = xs.Append(1)\n}\n", []string{"S104:2"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("want %v, got %s", tc.want, describe(d))
			}
			if tc.text != "" && !strings.Contains(d[0].Message, tc.text) {
				t.Errorf("message %q does not name %q", d[0].Message, tc.text)
			}
		})
	}
}

// D-D8: Info records a prelude dot call as a CallFunc with its receiver
// and every type argument of the called function, the receiver-bound ones
// included, in the function's order; CallPrelude is no longer produced.
func TestPreludeDotCallInfo(t *testing.T) {
	src := `type Pair[A, B] { First A, Second B }

func Swap[A, B](p Pair[A, B]) Pair[B, A] {
	Pair[B, A]{First: p.Second, Second: p.First}
}

func Map2[A, B, C](p Pair[A, B], f func(a A) C) Pair[C, B] {
	Pair[C, B]{First: f(p.First), Second: p.Second}
}

func f[T](xs []T, x T) []T {
	xs.Append(x)
}

func g(xs []int, m Map[string, bool], p Pair[int, string]) int {
	let ys = xs.Append(1)
	let o = m.Get("a")
	let m2 = m.Delete("a")
	let q = p.Swap()
	let r = p.Map2(func(a int) int64 { int64(a) })
	_ = o
	_ = q
	_ = r
	len(ys) + len(m2)
}
`
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	calls := map[string]*check.Call{}
	for x, call := range pkg.Info.Calls {
		calls[src[x.Pos():x.End()]] = call
		if call.Kind == check.CallPrelude {
			t.Errorf("%s: CallPrelude is no longer produced", src[x.Pos():x.End()])
		}
	}
	for _, w := range []struct{ call, fn, recv, targs string }{
		{"xs.Append(x)", "Append", "xs", "[T]"},
		{"xs.Append(1)", "Append", "xs", "[int]"},
		{`m.Get("a")`, "Get", "m", "[string bool]"},
		{`m.Delete("a")`, "Delete", "m", "[string bool]"},
		{"p.Swap()", "Swap", "p", "[int string]"},
		{"p.Map2(func(a int) int64 { int64(a) })", "Map2", "p", "[int string int64]"},
	} {
		c := calls[w.call]
		if c == nil || c.Kind != check.CallFunc || c.Func == nil || c.Func.Name != w.fn || c.Recv == nil {
			t.Errorf("%s: want a CallFunc of %s with a receiver, got %+v", w.call, w.fn, c)
			continue
		}
		if got := src[c.Recv.Pos():c.Recv.End()]; got != w.recv {
			t.Errorf("%s: receiver %q, want %q", w.call, got, w.recv)
		}
		if got := fmt.Sprint(c.TypeArgs); got != w.targs {
			t.Errorf("%s: TypeArgs %s, want %s", w.call, got, w.targs)
		}
	}
}

// D-D7: an import alias is allowed only when the path's last element
// clashes with a package-level name or with the name another import of the
// file binds; any other alias, `_` and an alias equal to the default name
// included, is S108 at the import. A refused alias (a prelude name, an
// unknown package) keeps its one diagnostic.
func TestImportAliasOnlyForClash(t *testing.T) {
	const use = "func f() t.Expired {\n\tt.Expired\n}\n"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"no-clash", "import t timeout\n" + use, []string{"S108:1"}},
		{"default-name", "import timeout timeout\nfunc f() timeout.Expired {\n\ttimeout.Expired\n}\n", []string{"S108:1"}},
		{"blank", "import _ timeout\n", []string{"S108:1"}},
		{"plain", "import timeout\nfunc f() timeout.Expired {\n\ttimeout.Expired\n}\n", nil},
		{"clash-func", "import t timeout\nfunc timeout() int {\n\t1\n}\n" + use, nil},
		{"clash-type", "import t timeout\ntype timeout { N int }\n" + use, nil},
		{"clash-let", "import t timeout\nlet timeout = 1\n" + use, nil},
		// The alias timeout for queue frees nothing (S108), and makes the
		// alias t for timeout a clash resolved (admitted).
		{"clash-import", "import timeout queue\nimport t timeout\n" + use + "func g() timeout.Queue[int] {\n\ttimeout.New[int](1)\n}\n", []string{"S108:1"}},
		{"blank-clash", "import _ timeout\nfunc timeout() {}\n", nil},
		{"prelude-name", "import string timeout\nfunc f() string.Expired {\n\tstring.Expired\n}\n", []string{"S108:1"}},
		{"unknown-package", "import m missing/deep\nfunc f() m.T {\n\tm.T{}\n}\n", []string{"S104:1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			var got []string
			for _, x := range d {
				got = append(got, fmt.Sprintf("%s:%d", x.Code, x.Line))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("want %v, got %s", tc.want, describe(d))
			}
			if tc.name == "no-clash" && !strings.Contains(d[0].Message, "an import alias is only for a name clash; write import timeout") {
				t.Errorf("message %q does not name the plain import", d[0].Message)
			}
		})
	}
}

func TestImportClashRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, src, text string
		line            int
	}{
		{"plain-func", "import timeout\nfunc timeout() {}\n", "timeout clashes with func timeout; alias the import (import t timeout)", 1},
		{"plain-type", "import timeout\ntype timeout {}\n", "timeout clashes with type timeout", 1},
		{"plain-let", "import timeout\nlet timeout = 1\n", "timeout clashes with let timeout", 1},
		{"plain-used", "import timeout\nfunc timeout() {}\nfunc f() timeout.Expired { timeout.Expired }\n", "alias the import", 1},
		{"duplicate-i3", "import task\nimport task timeout\nfunc f() {\n\tlet t = task.Go() { 1 }\n\tt.Stop()\n\t_ = timeout.Expired\n}\nfunc g() timeout.Expired { timeout.Expired }\n", "task is imported twice", 2},
		{"duplicate-alias", "import t timeout\nimport t task\nfunc timeout() {}\nfunc f() t.Expired { t.Expired }\nfunc g() { _ = task.Go }\n", "t is imported twice", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != tc.line || !strings.Contains(d[0].Message, tc.text) {
				t.Fatalf("want one S108:%d naming %q, got %s", tc.line, tc.text, describe(d))
			}
		})
	}
	file, err := syntax.Parse("import timeout\n")
	if err != nil {
		t.Fatal(err)
	}
	other, err := syntax.Parse("func timeout() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	_, d := check.Check("main", []*syntax.File{file, other}, nil)
	if len(d) != 1 || d[0].Code != check.CodeType || d[0].File != 0 || d[0].Line != 1 {
		t.Fatalf("cross-file clash: want one S108 in import file, got %s", describe(d))
	}
}

// Broken imports and package declarations must resolve alike in typing
// and the body dependency graph (FuzzCheck d3e93add8122c13e).
func TestImportReferenceRecovery(t *testing.T) {
	for _, src := range []string{
		"import k\nfunc k(){k}",
		"import k\nlet k = 1\nfunc f(){ _ = k }\n",
		"import task\nfunc task(){ task() }\n",
		"import timeout\nfunc timeout(){}\nfunc f(){ timeout() }\n",
		"func k(){}\nfunc f(){ k() }\n",
	} {
		if p := check.RefWalkProblems(src); len(p) != 0 {
			t.Errorf("%s: %v", src, p)
		}
	}
}

// An import alias that clashes with a package-level name is one S108 at
// the import, naming another alias, with no cascade at its uses (D3a round
// 2 review m3).
func TestImportAliasClashHasOneDiagnostic(t *testing.T) {
	for _, tc := range []struct{ name, source, kind string }{
		{"used-func", "import k timeout\nfunc timeout() {}\nfunc k() {}\nfunc f() k.Expired { k.Expired }\n", "func"},
		{"unused-func", "import k timeout\nfunc timeout() {}\nfunc k() {}\n", "func"},
		{"no-default-clash", "import k timeout\nfunc k() {}\nfunc f() k.Expired { k.Expired }\n", "func"},
		{"type", "import k timeout\nfunc timeout() {}\ntype k {}\nfunc f() k.Expired { k.Expired }\n", "type"},
		{"let", "import k timeout\nfunc timeout() {}\nlet k = 0\nfunc f() k.Expired { k.Expired }\n", "let"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.source)
			want := "k clashes with " + tc.kind + " k; choose another alias"
			if len(d) != 1 || d[0].Code != check.CodeType || d[0].Line != 1 || !strings.Contains(d[0].Message, want) {
				t.Fatalf("want one import S108 naming %q, got %s", want, describe(d))
			}
		})
	}
	file, err := syntax.Parse("import k timeout\nfunc f() k.Expired { k.Expired }\n")
	if err != nil {
		t.Fatal(err)
	}
	other, err := syntax.Parse("func timeout() {}\nfunc k() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	_, d := check.Check("main", []*syntax.File{file, other}, nil)
	if len(d) != 1 || d[0].Code != check.CodeType || d[0].File != 0 || d[0].Line != 1 || !strings.Contains(d[0].Message, "choose another alias") {
		t.Fatalf("want one S108 for cross-file alias clash, got %s", describe(d))
	}
}

// A conversion whose operand's type holds an invalid part adds no
// diagnostic: the mistake is reported where the part came from (D3a round
// 2 review m1).
func TestInvalidConversionsDoNotCascade(t *testing.T) {
	runD1(t, []d1Case{
		{"invalid-element", "type Ints []int\nfunc g(ys []Unknown) Ints {\n\tInts(ys)\n}\n", []string{"S104:2"}},
		{"invalid-map-value", "type Counts Map[string, int]\nfunc g(m Map[string, Unknown]) Counts {\n\tCounts(m)\n}\n", []string{"S104:2"}},
	})
}
