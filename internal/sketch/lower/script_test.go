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
