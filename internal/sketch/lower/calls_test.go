package lower

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/receipt"
)

const callsSource = `type Missing { name string }

type Store {
	Find(name string) string fails Missing,
}

type Mem {}

func Find(_ Mem, name string) string fails Missing {
	if name == "a" {
		return "A"
	}
	return Missing{name: name}
}

func Label(m Mem, s string) string fails Missing {
	return try m.Find(s)
}

let Live = context {
	Store: Mem,
}

func double(n int) int {
	return n * 2
}

func lookup(name string) string fails Missing needs Store {
	return try Store.Find(name)
}

func apply(f func(string) string fails Missing, s string) string fails Missing {
	return try f(s)
}

func main() needs Console {
	let n = double(2)
	with Live {
		let v = lookup("a") catch {
		case Missing:
			"none"
		}
		Console.Println(v)
	}
	let m = Mem
	let w = apply(func(s string) string fails Missing { return try m.Label(s) }, "a") catch {
	case Missing:
		"none"
	}
	Console.Println(w)
	if n == 4 {
		Console.Println("four")
	}
}
`

// TestCallIdentity: each lowered call of the source, a pure one included,
// keeps its Ergo identity (#230): the declaration that makes it, what it
// calls and its position. The emitted package lists the identities in
// source order in ergoCalls; the Go line of each call follows the //line
// directive of its own position; and the ordinary executable, with no
// reader added, links the table (the defect report reads it).
func TestCallIdentity(t *testing.T) {
	o := mustLower(t, "calls", callsSource)
	code := o.mainGo()
	at := func(snippet string, nth int) (int, int) {
		i := -1
		for range nth {
			j := strings.Index(callsSource[i+1:], snippet)
			if j < 0 {
				t.Fatalf("snippet %q not found", snippet)
			}
			i += 1 + j
		}
		line := strings.Count(callsSource[:i], "\n") + 1
		return line, i - strings.LastIndex(callsSource[:i], "\n")
	}
	type want struct {
		fn, callee, snippet string
		nth                 int
		goCall              string // text of the Go line that makes the call
	}
	wants := []want{
		{"Mem.Label", "Mem.Find", "m.Find(s)", 1, ".Find(env, s)"},
		{"lookup", "Store.Find", "Store.Find(name)", 1, ".Store.Find(env.task, name)"},
		{"apply", "f", "f(s)", 1, " f("},
		{"main", "double", "double(2)", 1, "double("},
		{"main", "lookup", `lookup("a")`, 1, `lookup(`},
		{"main", "Console.Println", "Console.Println(v)", 1, ".Println((&env_1).task, v"},
		{"main", "apply", "apply(func", 1, "apply("},
		{"main", "Mem.Label", "m.Label(s)", 1, ".Label("},
		{"main", "Console.Println", "Console.Println(w)", 1, ".Println(env.task, w)"},
		{"main", "Console.Println", `Console.Println("four")`, 1, `.Println(env.task, "four")`},
	}
	var wantTable []string
	for _, w := range wants {
		line, col := at(w.snippet, w.nth)
		wantTable = append(wantTable, fmt.Sprintf("%s %s %s:%d:%d", w.fn, w.callee, testSourcePath, line, col))
	}

	entry := regexp.MustCompile(`\{Func: "([^"]*)", Callee: "([^"]*)", File: "([^"]*)", Line: (\d+), Column: (\d+)\}`)
	var gotTable []string
	for _, m := range entry.FindAllStringSubmatch(code, -1) {
		gotTable = append(gotTable, fmt.Sprintf("%s %s %s:%s:%s", m[1], m[2], m[3], m[4], m[5]))
	}
	if strings.Join(gotTable, "\n") != strings.Join(wantTable, "\n") {
		t.Errorf("call table:\n%s\nwant:\n%s\nGo:\n%s", strings.Join(gotTable, "\n"), strings.Join(wantTable, "\n"), code)
	}

	// Each identified call's Go line follows its own directive.
	lines := strings.Split(code, "\n")
	for i, w := range wants {
		line, col := at(w.snippet, w.nth)
		directive := fmt.Sprintf("//line %s:%d:%d", testSourcePath, line, col)
		found := false
		for j, l := range lines {
			if l == directive && j+1 < len(lines) && strings.Contains(lines[j+1], w.goCall) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("entry %d (%s): no Go line holding %q directly after %q\n%s", i, w.callee, w.goCall, directive, code)
		}
	}

	// The ordinary executable links the table: no reader is added.
	module := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(module, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range append(Runtime(), o.files...) {
		write(f.Path, f.Data)
	}
	exe := filepath.Join(t.TempDir(), "calls")
	if out, err := receipt.GoBuildCommand(module, exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	var stdout bytes.Buffer
	cmd := exec.Command(exe)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := stdout.String(); got != "A\nA\nfour\n" {
		t.Errorf("program output %q; want A, A, four", got)
	}
	nm, err := exec.Command("go", "tool", "nm", exe).CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm: %v\n%s", err, nm)
	}
	if !regexp.MustCompile(`(?m) main\.ergoCalls$`).Match(nm) {
		t.Errorf("the executable does not link main.ergoCalls:\n%s", nm)
	}
}

// TestDefectReportNamesErgoCallSites: a defect that escapes main is
// reported with its Ergo call chain from the call table, innermost first:
// the frame that panicked, then each call, by Ergo function, callee and
// .ergo position; no Go frame (#230; runtime.md, Exit causes). The pure
// call into pick is named as well as the capability call into middle.
func TestDefectReportNamesErgoCallSites(t *testing.T) {
	src := `func pick(xs []int, i int) int {
	xs[i]
}

func middle(n int) int needs Console {
	Console.Println("middle")
	pick([]int{1, 2}, n)
}

func main() needs Console {
	let v = middle(5)
	if v == 0 {
		Console.Println("zero")
	}
}
`
	path := filepath.Join(t.TempDir(), "main.ergo")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/ergo", "run", path)
	cmd.Dir = filepath.Clean("../../..")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the program did not fail:\n%s", out)
	}
	want := "middle\npanic: runtime error: index out of range [5] with length 2\n" +
		"\tpick at " + path + ":2\n" +
		"\tmiddle calls pick at " + path + ":7:2\n" +
		"\tmain calls middle at " + path + ":11:10\n"
	if !strings.HasPrefix(string(out), want) {
		t.Fatalf("defect report:\n%s\nwant it to start with:\n%s", out, want)
	}
	if strings.Contains(string(out), "goroutine") || strings.Contains(string(out), ".go:") {
		t.Fatalf("the report shows Go frames:\n%s", out)
	}
}
