package lower

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestSourceAnchorsReachTheGoBinary: a //line directive from Directive puts
// the .ergo path and line into the executable's position tables, which
// stack traces and runtime.Caller read, for a path with spaces, a colon
// and a non-ASCII letter, also under the build flags of every native build.
func TestSourceAnchorsReachTheGoBinary(t *testing.T) {
	module := t.TempDir()
	source := filepath.Join(t.TempDir(), "dír with space", "a:b.ergo")
	if err := ValidatePath(source); err != nil {
		t.Fatal(err)
	}
	main := "package main\n\nimport (\n\t\"os\"\n\t\"runtime\"\n\t\"strconv\"\n)\n\nfunc main() {\n" +
		Directive(source, 7, 0) +
		"\t_, file, line, _ := runtime.Caller(0)\n\tos.Stdout.WriteString(file + \"|\" + strconv.Itoa(line) + \"\\n\")\n}\n"
	for name, data := range map[string]string{"go.mod": "module anchor\n\ngo 1.23\n", "main.go": main} {
		if err := os.WriteFile(filepath.Join(module, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	program := filepath.Join(t.TempDir(), "program")
	child := receipt.GoBuildCommand(module, program, ".")
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	run := exec.Command(program)
	var out bytes.Buffer
	run.Stdout = &out
	err := runProgram(t, run)
	if err != nil || out.String() != source+"|7\n" {
		t.Fatalf("program reports %q (%v), want %q", out.String(), err, source+"|7\n")
	}
	if got := Directive(source, 7, 3); got != "//line "+source+":7:3\n" {
		t.Fatalf("directive with a column %q", got)
	}
}

// TestValidatePathRefusesWhatADirectiveCannotName: a line feed would end the
// directive and make the rest of the name Go code; the other refusals keep
// the generated file valid UTF-8 Go and the name unambiguous.
func TestValidatePathRefusesWhatADirectiveCannotName(t *testing.T) {
	for _, path := range []string{
		"", "main.ergo", "./main.ergo", "/x/../y.ergo", "/x//y.ergo",
		"/x/a\nfunc init() { panic(1) }\n//.ergo", "/x/a\r.ergo", "/x/a\x00.ergo", "/x/a\x7f.ergo",
		"/x/\xff.ergo", "/x/\uFEFF.ergo", "/x/a\u0085.ergo",
	} {
		if err := ValidatePath(path); err == nil {
			t.Errorf("ValidatePath(%q) accepted", path)
		}
	}
	for _, path := range []string{"/x/main.ergo", "/x/dír/a b:c.ergo", "/x/日本.ergo"} {
		if err := ValidatePath(path); err != nil {
			t.Errorf("ValidatePath(%q): %v", path, err)
		}
	}
}

// TestUnitValidate: files and sources are index-aligned, present, anchorable
// and admitted once.
func TestUnitValidate(t *testing.T) {
	file, err := syntax.Parse("func main() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	pkg, diagnostics := check.Check("main", []*syntax.File{file}, nil)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	good := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: "/x/main.ergo", Text: "func main() {}\n"}}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, u := range map[string]Unit{
		"no files":       {Package: pkg},
		"misaligned":     {Package: pkg, Files: []*syntax.File{file}},
		"nil file":       {Package: pkg, Files: []*syntax.File{nil}, Sources: good.Sources},
		"relative":       {Package: pkg, Files: good.Files, Sources: []Source{{Path: "main.ergo"}}},
		"admitted twice": {Package: pkg, Files: []*syntax.File{file, file}, Sources: []Source{good.Sources[0], good.Sources[0]}},
	} {
		if err := u.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
