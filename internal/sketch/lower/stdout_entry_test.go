package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestEntryKeepsStdoutDirect: standard output is buffered when it is not a
// terminal (stdlib.md io 10), except where Go code may write the host's
// stdout beside the buffer: the entry of a main whose row holds
// go.Foreign and the test build, whose harness reports through Go, call
// v1.StdoutDirect before v1.Main; a main without go.Foreign does not. It reads the lowered
// Go's main function, the one promised fact. Mutants sb8-foreign-buffered,
// sb8-every-main-direct and sb8-test-buffered.
func TestEntryKeepsStdoutDirect(t *testing.T) {
	const plain = "func main() needs Console {\n\tConsole.Println(\"hi\")\n}\n"
	const foreign = "import go strings\n\nfunc main() needs Console | go.Foreign {\n\tConsole.Println(strings.ToUpper(\"hi\"))\n}\n"
	entry := func(code string) string {
		i := strings.Index(code, "\nfunc main() {\n")
		if i < 0 {
			t.Fatalf("no Go main in:\n%s", code)
		}
		return code[i : i+strings.Index(code[i+1:], "\n}\n")+1]
	}
	if e := entry(mustLower(t, "stdout_plain", plain).mainGo()); strings.Contains(e, ".StdoutDirect()") {
		t.Errorf("a main without go.Foreign keeps standard output direct:\n%s", e)
	}
	if e := entry(mustLower(t, "stdout_foreign", foreign).mainGo()); !strings.Contains(e, ".StdoutDirect()") {
		t.Errorf("a main that needs go.Foreign does not keep standard output direct:\n%s", e)
	}
	f, err := syntax.Parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	files, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: plain}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Path == "main.go" {
			if e := entry(string(file.Data)); !strings.Contains(e, ".StdoutDirect()") {
				t.Errorf("the test build does not keep standard output direct:\n%s", e)
			}
			return
		}
	}
	t.Fatal("the test build has no main.go")
}
