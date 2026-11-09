package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std lowering gaps (batch 11, lower-gaps; repros in the run dir's
// batch-10/fuc/lowering-parse-gap.md). A std package's body and its
// named contexts are lowered with no source of the program: a body into
// the generated part of main.go, with no //line directive. Two shapes of
// it had no test, because no std package has them yet:
//
//   - G1: a std function that returns a record as an operation type. The
//     operations conversion writes its adapter type into the generated
//     part while the function's head was already there, so the Go type
//     landed inside the Go function, and the Go did not parse.
//   - G2: a with of a named context that a std package exports. The with
//     refused every named context that no source file of the program
//     declares, an internal compiler error.
//
// A std fixture package (check.StdFixture) is checked as std is, so these
// tests take the path ergo run takes: check, lower, go vet, go build, run.

// gapStd is the std fixture of the repros.
const gapStd = `type GapReader { GapGet() int }

type gapBox { n int }

func GapGet(b gapBox) int { b.n }

func GapMake() GapReader { gapBox{n: 7} }

let GapCtx = context { GapReader: gapBox{n: 8} }
`

// runGapProgram checks src against the std fixtures, lowers, vets, builds
// and runs it, and returns its stdout.
func runGapProgram(t *testing.T, name, src string, imp pkgImporter) string {
	t.Helper()
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	out := &lowered{name: name, src: src, file: file}
	out.pkg, out.diags = check.Check("main", []*syntax.File{file}, imp)
	if len(out.diags) != 0 {
		t.Fatalf("%s: diagnostics: %v", name, out.diags)
	}
	code, err := lowerPackage(Unit{Package: out.pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, false)
	if err != nil {
		t.Fatalf("%s: lowering: %v\nsource:\n%s\nGo:\n%s", name, err, src, code)
	}
	out.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: code}}
	exes := buildAll(t, []*lowered{out}, true)
	if exes == nil {
		t.FailNow()
	}
	stdout, stderr, exit := runExe(t, exes[name])
	if exit != 0 {
		t.Fatalf("%s: exit %d, stderr %s", name, exit, firstLines(stderr, 12))
	}
	return stdout
}

// TestStdGapParseGuard: buildAll's parse guard (unparsedGo) names a
// program whose Go does not parse, here G1's emit at the base (a method
// declaration inside a function), with its Ergo source and its Go, and
// passes a program whose Go parses.
func TestStdGapParseGuard(t *testing.T) {
	const bad = "package main\n\nfunc GapMake() GapReader {\ntype ops struct{ v gapBox }\n\nfunc (a *ops) GapGet() int {\n\treturn 7\n}\n\n\treturn &ops{}\n}\n"
	const good = "package main\n\nfunc main() {}\n"
	program := func(name, code string) *lowered {
		return &lowered{name: name, src: "ergo source of " + name, files: []File{{Path: "main.go", Data: []byte(code)}}}
	}
	if err := unparsedGo([]*lowered{program("fine", good)}); err != nil {
		t.Errorf("a program whose Go parses: %v", err)
	}
	err := unparsedGo([]*lowered{program("fine", good), program("broken", bad)})
	if err == nil {
		t.Fatal("a program whose Go does not parse passes the guard")
	}
	for _, want := range []string{"broken:", "does not parse", "ergo source of broken", "func (a *ops) GapGet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the guard's error does not name %q:\n%v", want, err)
		}
	}
}

// TestStdGapOpsConversion (G1): a std function converts a record into an
// operation type; the program calls its operation.
func TestStdGapOpsConversion(t *testing.T) {
	needsBuild(t)
	imp := pkgImporter{"gapstd": stdFixture(t, "gapstd", gapStd)}
	const src = `import gapstd

func main() needs Console {
	let r = gapstd.GapMake()
	Console.Println("value: \(r.GapGet())")
}
`
	if got, want := runGapProgram(t, "gapops", src, imp), "value: 7\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}

// TestStdGapNamedContext (G2): a with of a std package's named context.
func TestStdGapNamedContext(t *testing.T) {
	needsBuild(t)
	imp := pkgImporter{"gapstd": stdFixture(t, "gapstd", gapStd)}
	const src = `import gapstd

func viaContext() int needs gapstd.GapReader { gapstd.GapReader.GapGet() }

func main() needs Console {
	with gapstd.GapCtx {
		Console.Println("context: \(viaContext())")
	}
}
`
	if got, want := runGapProgram(t, "gapctx", src, imp), "context: 8\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
}
