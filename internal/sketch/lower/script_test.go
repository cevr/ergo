package lower

import (
	"strings"
	"testing"
)

// TestScriptNoAnswer: a call of a Script operation that has no answer is
// the defect that names the Script and the operation, for an operation
// with a result and for one that only fails; an operation with neither is
// only recorded (stdlib.md, Testing 10). Mutants: a fallible operation with
// no result recorded only (script-fallible-noresult-unanswered), and the
// defect's text without the operation (script-defect-text). The answered
// and recorded calls are the testing laws' (TestStdLawsRun).
//
// It builds Go because the defect is a panic of the running program: the
// lowered text shows a call of NoAnswer, and only the run shows which call
// reaches it and the text the entry reports.
func TestScriptNoAnswer(t *testing.T) {
	needsBuild(t)
	const head = "import testing\n\ntype Gone { Key string }\n\ntype Photos {\n\tTag(k string),\n\tDrop(k string) fails Gone,\n\tGet(k string) int,\n}\n\n"
	cases := []struct{ name, body, want string }{
		{"scriptresult", "\tlet _ = with context { Photos: s } { Photos.Get(\"a\") }\n", "panic: testing.Script[main.Photos]: Get has no answer"},
		{"scriptfails", "\twith context { Photos: s } { Photos.Drop(\"a\") } catch {\n\tcase Gone:\n\t}\n", "panic: testing.Script[main.Photos]: Drop has no answer"},
		{"scriptplain", "\twith context { Photos: s } { Photos.Tag(\"a\") }\n\tConsole.Println(s.Tag.Calls()[0])\n", ""},
	}
	var programs []*lowered
	for _, c := range cases {
		src := head + "func main() needs Console {\n\tlet s = testing.Script[Photos]{}\n" + c.body + "}\n"
		programs = append(programs, mustLower(t, c.name, src))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatal("the Script programs do not build")
	}
	for _, c := range cases {
		out, stderr, code := runExe(t, exes[c.name])
		if c.want == "" {
			if code != 0 || out != "a\n" {
				t.Errorf("%s: exit %d, stdout %q, stderr %s; want the call recorded with no answer", c.name, code, out, stderr)
			}
			continue
		}
		if code == 0 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %q; want %q", c.name, code, stderr, c.want)
		}
	}
}

// scriptHeldProgram holds two calls of Get and never releases them: the
// tasks that made them are stopped at the end of the scope, which ends
// their waits, not the holds. Released releases its one held call.
// ReleasedUnused releases a call the code never makes.
const scriptHeldProgram = `import task
import testing

type Gone { Key string }

type Photos {
	Get(k string) int fails Gone,
}

test Released() {
	let s = testing.Script[Photos]{}
	s.Get.Hold()
	s.Get.Release(func(k string) int fails Gone { 1 })
	let got = with context { Photos: s } { Photos.Get("a") } catch {
	case Gone:
		0
	}
	assert got == 1
}

test HeldTwice() {
	let s = testing.Script[Photos]{}
	s.Get.Hold()
	let c = testing.NewVirtualClock(0)
	with context { Photos: s, Scheduler: c } {
		let a = task.Go() { Photos.Get("a") }
		let b = task.Go() { Photos.Get("b") }
		Scheduler.Sleep(1s)
	}
}

test ReleasedUnused() {
	let s = testing.Script[Photos]{}
	s.Get.Hold()
	s.Get.Release(func(k string) int fails Gone { 1 })
}
`

// TestScriptHeldCallFailsTheTest is the end-of-test check's diagnostic row
// (stdlib.md, Testing 10, its text the spec): a test whose body returns
// with held calls no Release answered fails, naming the Script, the
// operation and the count, a test that ends with a release no call took
// fails too, and a test that released its call passes. Mutants: the check
// in $Try skipped (sc1-end-check-skipped) and an untaken release left out
// of the report (sc1-unused-release-silent), which no law catches, since a
// law's case releases what it holds and takes what it releases.
//
// It builds Go because the check runs in the built test binary's case
// runner ($Try), after the body's scope stopped its tasks.
func TestScriptHeldCallFailsTheTest(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "scriptheld"), "--seed", "1")
	got := verdicts(stdout)
	want := []string{
		"ok Released",
		"FAIL HeldTwice: testing.Script[main.Photos]: Get has 2 held calls that no Release answered",
		"FAIL ReleasedUnused: testing.Script[main.Photos]: Get was released with no held call",
		"FAIL (2 of 3 tests)",
	}
	if code != 1 || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("exit %d, verdicts:\n%s\nwant:\n%s\nstderr:\n%s", code, strings.Join(got, "\n"), strings.Join(want, "\n"), firstLines(stderr, 8))
	}
}
