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
		{"defined-slice", "type Ints []int\nfunc f(xs Ints) {\n\t_ = xs.Append(1)\n}\n", []string{"S104:3"}, "let raw []int = xs; then raw.Append(…)"},
		{"defined-map", "type M Map[string, int]\nfunc f(m M) {\n\t_ = m.Get(\"a\")\n}\n", []string{"S104:3"}, "let raw Map[string, int] = m; then raw.Get(…)"},
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
