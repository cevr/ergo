package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// formsPrelude is the callees of the forms rows: sq, a pure function;
// noisy, one that prints; ones, a pure one whose result is a slice;
// Box.Get, a method.
const formsPrelude = `func sq(a int) int {
	a * a + 1
}

func noisy(a int) int needs Console {
	Console.Println("noisy \(a)")
	a + 1
}

func ones(a int) []int {
	[]int{a, a}
}

type Box { V int }

func Get(b Box) int {
	b.V
}

`

// formsRows are the concrete rows of ergo inspect forms (IR-FORMS IRF-6):
// one program per reason a pass gives, the loop body that gives it, and
// the line the command prints for its site.
var formsRows = []struct {
	reason string
	body   string
	line   string
}{
	{"computed once per entry of its loop", "let s = sq(k)\n\t\tacc = acc + s", ":23:11  call  memo  fired  f  (computed once per entry of its loop)"},
	{"the callee is not pure", "let s = noisy(k)\n\t\tacc = acc + s", ":23:11  call  memo  not fired  f  (the callee is not pure)"},
	{"a method, a task, a call with a block or a call site", "let s = Box{V: k}.Get()\n\t\tacc = acc + s", "  call  memo  not fired  f  (a method, a task, a call with a block or a call site)"},
	{"an operand or the result is not a number, a bool or a string", "let s = ones(k)\n\t\tacc = acc + len(s)", ":23:11  call  memo  not fired  f  (an operand or the result is not a number, a bool or a string)"},
	{"an operand may change in the loop", "let s = sq(i)\n\t\tacc = acc + s", ":23:11  call  memo  not fired  f  (an operand may change in the loop)"},
	{"statements run before the call", "let s = sq(noisy(k))\n\t\tacc = acc + s", ":23:11  call  memo  not fired  f  (statements run before the call)"},
	{"reads the value of an equal call before it", "let a = sq(i)\n\t\tlet b = sq(i)\n\t\tacc = acc + a + b", ":24:11  call  merge  fired  f  (reads the value of an equal call before it)"},
	{"an operand or the earlier value is written in the function, or not of a basic type", "let mut q = i\n\t\tlet a = sq(q)\n\t\tq = q + 1\n\t\tlet b = sq(q)\n\t\tacc = acc + a + b", ":26:11  call  merge  not fired  f  (an operand or the earlier value is written in the function, or not of a basic type)"},
	{"the equal call before it is not a whole value", "acc = acc + sq(i)\n\t\tlet b = sq(i)\n\t\tacc = acc + b", ":24:11  call  merge  not fired  f  (the equal call before it is not a whole value)"},
	{"the equal call before it does not run first on every path", "if i > 1 {\n\t\t\tlet a = sq(i)\n\t\t\tacc = acc + a\n\t\t}\n\t\tlet b = sq(i)\n\t\tacc = acc + b", ":27:11  call  merge  not fired  f  (the equal call before it does not run first on every path)"},
}

// TestInspectFormsRows holds ergo inspect forms: for each reason a pass
// may give (lower.FormReasons), a program whose one site gives it prints
// its line, in text and in JSON, and docs/spec/inspect.md names it. It
// checks in process; no Go tool runs.
//
// Mutants it alone catches: irf-inspect-reports-fired-when-not (the text
// says fired for a site the pass refused), irf-inspect-unrouted (the
// command is not routed), irf-memo-reason-swapped (a refusal names
// another reason).
func TestInspectFormsRows(t *testing.T) {
	docs, err := os.ReadFile(filepath.Join("..", "..", "docs", "spec", "inspect.md"))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, row := range formsRows {
		have[row.reason] = true
	}
	for _, r := range lower.FormReasons() {
		if !have[r.Reason] {
			t.Errorf("no row for the %s pass's reason %q", r.Pass, r.Reason)
		}
		if !strings.Contains(string(docs), r.Reason) {
			t.Errorf("docs/spec/inspect.md does not name the %s pass's reason %q", r.Pass, r.Reason)
		}
	}
	dir := t.TempDir()
	for i, row := range formsRows {
		src := formsPrelude + "func f(n int, k int) int needs Console {\n\tlet mut acc = 0\n\tfor i in 0..n {\n\t\t" + row.body + "\n\t\tacc = acc + i\n\t}\n\tacc\n}\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(f(3, 2))\")\n}\n"
		path := filepath.Join(dir, "row"+string(rune('a'+i))+".ergo")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		text := captureInspect(t, dir, "forms", path)
		if !strings.Contains(text, row.line+"\n") {
			t.Errorf("row %q: ergo inspect forms prints\n%s\nwant a line ending %q; the program:\n%s", row.reason, text, row.line, src)
			continue
		}
		var sites []lower.FormSite
		if err := json.Unmarshal([]byte(captureInspect(t, dir, "forms", path, "--json")), &sites); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range sites {
			found = found || s.Reason == row.reason && s.Func == "f" && s.Fired == strings.Contains(row.line, "  fired  ")
		}
		if !found {
			t.Errorf("row %q: ergo inspect forms --json has no such site: %+v", row.reason, sites)
		}
	}
}

// captureInspect is what ergo inspect args prints on stdout.
func captureInspect(t *testing.T, dir string, args ...string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stdout
	os.Stdout = f
	err = inspectCommand(args)
	os.Stdout = saved
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
