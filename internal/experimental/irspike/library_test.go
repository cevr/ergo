package irspike

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// irLibrary checks that `ergo lib` on the IR (lower.LowerIRLibrary;
// LIB-ON-IR) gives Go the library the text lowering gave (lower.
// LowerLibrary), on every example package that has tests, split by
// package (TestIRLibraryAgreesOnExamplesPart0 …): the same exported Go
// functions with the same signatures, the same functions not exported with
// the same reasons, and the same package clause. The bodies behind them are
// the IR's, which TestIRAgreesOnExamples and TestIRTextChecks check in the
// examples' programs; TestGoCallsErgo (package lower) runs a Go host
// against both libraries.
//
// Mutant it alone catches: lib-ir-unit-lib-dropped (the build lowerer
// ignores Unit.lib, so the IR library is package main with no Go entries).
// It checks in process: the question is the Go API the lowering declares.
var irLibrary = newSplit(split[exampleTarget]{
	name:  "TestIRLibraryAgreesOnExamples",
	parts: libraryParts,
	cases: libraryDomain,
	key:   exampleTarget.key,
	run:   libraryAgreeOn,
})

// libraryParts is the split's part count: a part lowers about six packages
// twice, under the gate's 12 s shard target (inferred from the examples'
// lowering times, no build).
const libraryParts = 32

func TestIRLibraryAgreesOnExamplesPart0(t *testing.T)  { irLibrary.Part(t, 0) }
func TestIRLibraryAgreesOnExamplesPart1(t *testing.T)  { irLibrary.Part(t, 1) }
func TestIRLibraryAgreesOnExamplesPart2(t *testing.T)  { irLibrary.Part(t, 2) }
func TestIRLibraryAgreesOnExamplesPart3(t *testing.T)  { irLibrary.Part(t, 3) }
func TestIRLibraryAgreesOnExamplesPart4(t *testing.T)  { irLibrary.Part(t, 4) }
func TestIRLibraryAgreesOnExamplesPart5(t *testing.T)  { irLibrary.Part(t, 5) }
func TestIRLibraryAgreesOnExamplesPart6(t *testing.T)  { irLibrary.Part(t, 6) }
func TestIRLibraryAgreesOnExamplesPart7(t *testing.T)  { irLibrary.Part(t, 7) }
func TestIRLibraryAgreesOnExamplesPart8(t *testing.T)  { irLibrary.Part(t, 8) }
func TestIRLibraryAgreesOnExamplesPart9(t *testing.T)  { irLibrary.Part(t, 9) }
func TestIRLibraryAgreesOnExamplesPart10(t *testing.T) { irLibrary.Part(t, 10) }
func TestIRLibraryAgreesOnExamplesPart11(t *testing.T) { irLibrary.Part(t, 11) }
func TestIRLibraryAgreesOnExamplesPart12(t *testing.T) { irLibrary.Part(t, 12) }
func TestIRLibraryAgreesOnExamplesPart13(t *testing.T) { irLibrary.Part(t, 13) }
func TestIRLibraryAgreesOnExamplesPart14(t *testing.T) { irLibrary.Part(t, 14) }
func TestIRLibraryAgreesOnExamplesPart15(t *testing.T) { irLibrary.Part(t, 15) }
func TestIRLibraryAgreesOnExamplesPart16(t *testing.T) { irLibrary.Part(t, 16) }
func TestIRLibraryAgreesOnExamplesPart17(t *testing.T) { irLibrary.Part(t, 17) }
func TestIRLibraryAgreesOnExamplesPart18(t *testing.T) { irLibrary.Part(t, 18) }
func TestIRLibraryAgreesOnExamplesPart19(t *testing.T) { irLibrary.Part(t, 19) }
func TestIRLibraryAgreesOnExamplesPart20(t *testing.T) { irLibrary.Part(t, 20) }
func TestIRLibraryAgreesOnExamplesPart21(t *testing.T) { irLibrary.Part(t, 21) }
func TestIRLibraryAgreesOnExamplesPart22(t *testing.T) { irLibrary.Part(t, 22) }
func TestIRLibraryAgreesOnExamplesPart23(t *testing.T) { irLibrary.Part(t, 23) }
func TestIRLibraryAgreesOnExamplesPart24(t *testing.T) { irLibrary.Part(t, 24) }
func TestIRLibraryAgreesOnExamplesPart25(t *testing.T) { irLibrary.Part(t, 25) }
func TestIRLibraryAgreesOnExamplesPart26(t *testing.T) { irLibrary.Part(t, 26) }
func TestIRLibraryAgreesOnExamplesPart27(t *testing.T) { irLibrary.Part(t, 27) }
func TestIRLibraryAgreesOnExamplesPart28(t *testing.T) { irLibrary.Part(t, 28) }
func TestIRLibraryAgreesOnExamplesPart29(t *testing.T) { irLibrary.Part(t, 29) }
func TestIRLibraryAgreesOnExamplesPart30(t *testing.T) { irLibrary.Part(t, 30) }
func TestIRLibraryAgreesOnExamplesPart31(t *testing.T) { irLibrary.Part(t, 31) }

// libraryDomain is the example packages with tests: the test targets'
// folders, each an Ergo package `ergo lib` takes.
func libraryDomain(t *testing.T) []exampleTarget {
	var out []exampleTarget
	for _, e := range exampleDomain(t) {
		if e.test {
			out = append(out, e)
		}
	}
	return out
}

// libraryAgreeOn compares the Go API of target's package as the two
// lowerings write it.
func libraryAgreeOn(t *testing.T, target exampleTarget) {
	u, err := Load(target.dir)
	if err != nil {
		t.Fatalf("%s: %v", target.dir, err)
	}
	text, err := lower.LowerLibrary(u, "example.com/lib", "lib")
	if err != nil {
		t.Fatalf("%s: text library: %v", target.key(), err)
	}
	ir, _, err := lower.LowerIRLibrary(u, "lib")
	if err != nil {
		t.Fatalf("%s: IR library: %v", target.key(), err)
	}
	want, got := goAPI(t, text, "lib.go"), goAPI(t, ir, "lib.go")
	if !slices.Equal(got, want) {
		t.Errorf("%s: the IR library's Go API differs from the text library's\nIR:\n%s\ntext:\n%s", target.key(), strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// goAPI is the Go API of the file name of files: its package clause, each
// exported top-level function's signature, and each "Not exported to Go"
// line, sorted.
func goAPI(t *testing.T, files []lower.File, name string) []string {
	t.Helper()
	i := slices.IndexFunc(files, func(f lower.File) bool { return f.Path == name })
	if i < 0 {
		t.Fatalf("the library has no %s", name)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, files[i].Data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := []string{"package " + f.Name.Name}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || !fd.Name.IsExported() {
			continue
		}
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, fd.Type); err != nil {
			t.Fatal(err)
		}
		out = append(out, "func "+fd.Name.Name+strings.TrimPrefix(b.String(), "func"))
	}
	for _, line := range strings.Split(string(files[i].Data), "\n") {
		if strings.HasPrefix(line, "// Not exported to Go:") {
			out = append(out, line)
		}
	}
	slices.Sort(out[1:])
	return out
}
