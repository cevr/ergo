package check_test

// prelude-scope (checker-fixes unit 3): one owner of the predeclared set.
//
//   S1 A prelude operation (a function on a slice, a Map or a prelude type,
//      196m) is in no scope: a program may declare its name, and a bare use
//      is S104 naming the dot form.
//   S2 An extern's parameter list gets a function's binder rules (D-D3).
//   S3 check.IsPredeclared is the checker's D-D3 set.

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// A bare Sleep is S104 with the dot form, as a bare Append or Get is.
func TestPreludeOperationBareNameNamesTheDotForm(t *testing.T) {
	for _, tc := range []struct{ name, src, msg string }{
		{"Sleep", "func f() {\n\tSleep(LiveScheduler, 1ms)\n}\n", "unknown name Sleep; the prelude function Sleep is reached only by the dot form, s.Sleep(d)"},
		{"Append", "func f(xs []int) []int {\n\tAppend(xs, 1)\n}\n", "unknown name Append; the prelude function Append is reached only by the dot form, xs.Append(x)"},
		{"Get", "func f(m Map[string, int]) Option[int] {\n\tGet(m, \"k\")\n}\n", "unknown name Get; the prelude function Get is reached only by the dot form, m.Get(k)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			if len(d) != 1 || d[0].Code != check.CodeUnknown || d[0].Line != 2 || d[0].Message != tc.msg {
				t.Fatalf("want one S104 %q at line 2, got %s", tc.msg, describe(d))
			}
		})
	}
}

// A program's own Scheduler provider and the prelude's live one, each
// bound in its own context: the user's Sleep is Instant's operation only,
// and LiveScheduler.Sleep still reaches the prelude's (196m: x.f(y)
// resolves in the type's package).
func TestOwnSchedulerBesideTheLiveOne(t *testing.T) {
	src := `type Instant {}

func Sleep(s Instant, d duration) {}

let Quick = context { Scheduler: Instant }

func nap() needs Scheduler {
	Scheduler.Sleep(1ms)
}

func main() needs Ambient {
	with Quick {
		Scheduler.Sleep(1h)
		nap()
	}
	let Live = context { Scheduler: NewLiveScheduler() }
	with Live {
		nap()
	}
	NewLiveScheduler().Sleep(1ms)
	Instant.Sleep(1h)
	Sleep(Instant, 1h)
}
`
	if _, d := check.CheckSource(src); len(d) != 0 {
		t.Fatalf("want no diagnostic, got %s", describe(d))
	}
	// Control: without the user's function, Instant provides nothing.
	ctl := strings.Replace(src, "func Sleep(s Instant, d duration) {}\n", "", 1)
	ctl = strings.Replace(ctl, "\tInstant.Sleep(1h)\n\tSleep(Instant, 1h)\n", "", 1)
	if _, d := check.CheckSource(ctl); len(d) == 0 {
		t.Fatal("control: an Instant with no Sleep checks clean as a Scheduler provider")
	}
}

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

// A prelude name used twice in one parameter list: D-D3's S108 at the
// first, which still binds, so the second is "already a parameter", not a
// second prelude-name refusal (diagnostics.md, the D-D3 row: "with a
// function's 'already a parameter' rule"). The function and the extern
// forms share the binder (paramBinder), so each is pinned by its text.
func TestPreludeNameTwiceIsAlreadyAParameter(t *testing.T) {
	const prelude = "len is a prelude name, which cannot be redeclared; choose another name (#196, eighth round, item 3)"
	for _, tc := range []struct{ name, src, fn string }{
		{"func", "func f(len string, len string) {}\n", "f"},
		{"extern", "extern go strings.Index func Idx(len string, len string) int\n", "Idx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, d := check.CheckSource(tc.src)
			first := strings.Index(tc.src, "len")
			second := strings.LastIndex(tc.src, "len")
			want := []check.Diagnostic{
				{Code: check.CodeType, Message: prelude, Offset: first},
				{Code: check.CodeType, Message: "len is already a parameter of " + tc.fn, Offset: second},
			}
			if len(d) != len(want) {
				t.Fatalf("want %d diagnostics, got %s", len(want), describe(d))
			}
			for i, w := range want {
				if d[i].Code != w.Code || d[i].Message != w.Message || d[i].Offset != w.Offset {
					t.Fatalf("diagnostic %d: want %s %q at %d, got %s", i, w.Code, w.Message, w.Offset, describe(d))
				}
			}
		})
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
