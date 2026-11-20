package check_test

// prelude-scope (checker-fixes unit 3): one owner of the predeclared set.
//
//   S1 A prelude operation (a function on a slice, a Map or a prelude type,
//      196m) is in no scope: a program may declare its name, and a bare use
//      is S104 naming the dot form.
//   S2 An extern's parameter list gets a function's binder rules (D-D3).
//   S3 check.IsPredeclared is the checker's D-D3 set.

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// An extern's parameter list gets a function's binder rules: D-D3's S108
// at a prelude name, "already a parameter" at a name used twice, and
// nothing for `_` or a fresh name.
func TestExternParametersKeepTheBinderRules(t *testing.T) {
	runD1(t, []d1Case{
		{"prelude-type", "extern go strings.Repeat func Rep(Option string, n int) string\n", []string{"S108:1"}},
		{"built-in", "extern go strings.Repeat func Rep(len string, n int) string\n", []string{"S108:1"}},
		{"twice", "extern go strings.Index func Idx(s string, s string) int\n", []string{"S108:1"}},
		{"prelude-and-twice", "extern go strings.Index func Idx(true string, true string) int\n", []string{"S108:1", "S108:1"}},
		{"blank-twice", "extern go strings.Index func Idx(_ string, _ string) int\n", nil},
		{"fresh", "extern go strings.Index func Idx(s string, sub string) int\n", nil},
		{"prelude-operation", "extern go strings.Repeat func Rep(Sleep string, n int) string\n", nil},
	})
	_, d := check.CheckSource("extern go strings.Index func Idx(s string, s string) int\n")
	if want := "s is already a parameter of Idx"; len(d) != 1 || d[0].Message != want {
		t.Fatalf("want %q, got %s", want, describe(d))
	}
}

// IsPredeclared answers what D-D3 refuses: a prelude type, a variant, a
// built-in, a universe type and Map are predeclared; a prelude operation,
// a universe name outside D-D3's list and `_` are not.
func TestIsPredeclaredControls(t *testing.T) {
	for n, want := range map[string]bool{
		"Option": true, "None": true, "Console": true, "LiveScheduler": true, "len": true, "true": true, "int": true, "duration": true, "ordered": true, "Map": true,
		"Sleep": false, "Append": false, "Get": false, "Delete": false, "error": false, "uintptr": false, "go": false, "with": false, "_": false, "fresh": false,
	} {
		if got := check.IsPredeclared(n); got != want {
			t.Errorf("IsPredeclared(%q) = %v, want %v", n, got, want)
		}
	}
}
