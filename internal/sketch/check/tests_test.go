package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// testDecls are the declarations the tests below share.
const testDecls = `type Store {
	Get() string,
}

type Mem {}

func Get(_ Mem) string {
	"ok"
}

type Bang {}

func risky(n int) int fails Bang {
	if n < 0 {
		return Bang
	}
	n
}

type Point { X int, Y float64 }
type Shape = Dot | Line{From Point, To Point}
type Dot {}
type Celsius int
type Pair (int, string)
type List = Nil | Cons{Head int, Tail List}
type Nil {}
type Tree { Kids []Tree, Tags Map[string, int] }
type Loop = Again{Next Loop}
type Box[T] { V T }
type Fn func(int) int

`

// codesAt is each diagnostic of src as "CODE:line".
func codesAt(src string) ([]string, []check.Diagnostic) {
	_, diags := check.CheckSource(src)
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%s:%d", d.Code, d.Line))
	}
	return out, diags
}

func line(src, marker string) int {
	return strings.Count(src[:strings.Index(src, marker)], "\n") + 1
}

// TestPropertyParamDomain: rule 3 over each type kind a parameter can name,
// generatable or not (types.Type's kinds; the closure of the generatable
// constructors over one another, and every kind outside them).
func TestPropertyParamDomain(t *testing.T) {
	for _, tc := range []struct {
		typ string
		ok  bool
	}{
		{"bool", true}, {"string", true}, {"int", true}, {"int8", true}, {"uint64", true},
		{"uintptr", true}, {"float32", true}, {"float64", true}, {"complex128", true},
		{"[]int", true}, {"Map[string, bool]", true}, {"(int, string)", true},
		{"Point", true}, {"Shape", true}, {"Celsius", true}, {"Pair", true},
		{"List", true}, {"Tree", true}, {"Box[List]", true}, {"[]Map[int, (bool, Shape)]", true},
		{"Store", false}, {"Fn", false}, {"func(int) int", false}, {"*Point", false},
		{"Loop", false}, {"[]Loop", false}, {"Box[Store]", false},
		{"Map[int, Fn]", false}, {"(int, Store)", false},
	} {
		src := testDecls + "test P(x " + tc.typ + ") {\n\tassert true\n}\n"
		got, diags := codesAt(src)
		at := line(src, "test P")
		switch {
		case tc.ok && len(got) != 0:
			t.Errorf("%s: want a generatable parameter, got %v", tc.typ, diags)
		case !tc.ok && (len(got) != 1 || got[0] != fmt.Sprintf("S108:%d", at)):
			t.Errorf("%s: want one S108 at line %d, got %v", tc.typ, at, diags)
		case !tc.ok && !strings.Contains(diags[0].Message, "property parameter"):
			t.Errorf("%s: the S108 does not name the property parameter rule: %v", tc.typ, diags)
		}
	}
}

// TestPropertyParamStd: a standard library record is not generatable (the
// orchestrator's E3a decision: S108, the simpler, reversible default).
func TestPropertyParamStd(t *testing.T) {
	// queue.Closed is an empty record: only its package refuses it.
	src := "import queue\n\ntest P(x queue.Closed) {\n\tassert true\n}\n"
	got, diags := codesAt(src)
	if len(got) != 1 || got[0] != "S108:3" || !strings.Contains(diags[0].Message, "standard library") {
		t.Errorf("want one S108 naming the standard library at line 3, got %v", diags)
	}
}

// TestTestBodies: rules 1, 2 and 5 at their edges.
func TestTestBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string // codes, each on the marked line
	}{
		{"a failure escapes a test", "test T {\n\t$assert try risky(1) == 1\n}\n", nil},
		{"a fallible assert is tried", "test T {\n\t$assert risky(1) == 1\n}\n", []string{"S105"}},
		{"a bool from a named bool type", "type Flag bool\n\ntest T {\n\tlet f Flag = true\n\t$assert f\n}\n", nil},
		{"assert takes a bool", "test T {\n\t$assert \"yes\"\n}\n", []string{"S108"}},
		{"a test's own with discharges", "test T {\n\t$assert with context { Store: Mem } { Store.Get() } == \"ok\"\n}\n", nil},
		{"a service needs a with", "test T {\n\t$assert Store.Get() == \"ok\"\n}\n", []string{"S102"}},
		{"a test name is no value", "test T {\n\tassert true\n}\n\nfunc f() {\n\t$let g = T\n}\n", []string{"S104"}},
		{"a test name beside a function", "func T() {}\n\ntest T {\n\t$assert true\n}\n", nil},
		{"two parameters of one name", "$test T(a int, a int) {\n\tassert true\n}\n", []string{"S108"}},
		{"an unused local", "test T {\n\t$let x = 1\n\tassert true\n}\n", []string{"S110"}},
		// A literal's body is not the test's: it runs when called (rule 4).
		{"assert in a literal in a test", "test T {\n\tlet f = func() {\n\t\t$assert true\n\t}\n\tf()\n}\n", []string{"S108"}},
		{"assert in a literal in a literal in a test", "test T {\n\tlet f = func() {\n\t\tlet g = func() {\n\t\t\t$assert true\n\t\t}\n\t\tg()\n\t}\n\tf()\n}\n", []string{"S108"}},
		{"assert in a trailing block in a test", "func run(f func() int) int {\n\tf()\n}\n\ntest T {\n\tlet n = run() {\n\t\t$assert true\n\t\t1\n\t}\n\tassert n == 1\n}\n", nil},
		{"return in a test", "test T {\n\tassert true\n\t$return\n}\n", nil},
		{"an empty test", "test T {\n$}\n", nil},
	} {
		src := testDecls + strings.Replace(tc.body, "$", "", 1)
		at := line(testDecls+tc.body, "$")
		var want []string
		for _, c := range tc.want {
			want = append(want, fmt.Sprintf("%s:%d", c, at))
		}
		got, diags := codesAt(src)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: want %v, got %v", tc.name, want, diags)
		}
	}
}
