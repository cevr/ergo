package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// gap-fail (G-L5-2): whether a value of an error type is a failure, and
// whether the type is a Go error, must not depend on which package declares
// it and which raises it (types.md, Error types 1). Package a declares
// Missing; the raising package is a, b (which imports a) or the root
// (which imports both); a's own program names Missing in a row or not. In
// each program the raising package writes the K1 rule table's functions
// (error_value_run_test.go: form × position × target), and the root calls
// each one and catches its failure or reads its value. Each program must
// check, and its lowered Go must type-check (typecheckGo): a return lowered
// as the result's value, or a catch of a Go type with no Error method, is
// a Go type error. The table is enumerated whole; the seed draws the keys
// and the order of the functions. A failure prints the program's Ergo
// source and its Go.
var errorImporterSeed = flag.Uint64("errimporter.seed", 0, "run TestFailureRaisedByImporter on this seed alone")

func TestFailureRaisedByImporter(t *testing.T) {
	seeds := []uint64{1, 2}
	if *errorImporterSeed != 0 {
		seeds = []uint64{*errorImporterSeed}
	}
	var programs []*lowered
	for _, seed := range seeds {
		for _, raiser := range []string{"a", "b", "main"} {
			for _, declared := range []bool{false, true} {
				programs = append(programs, errorImporterProgram(t, seed, raiser, declared))
			}
		}
	}
	typecheckGo(t, programs)
}

// errorImporterProgram checks and lowers one program: package a declares
// Missing (and raises it in its own row when declared), raiser writes the
// rule table's functions, and the root calls them.
func errorImporterProgram(t *testing.T, seed uint64, raiser string, declared bool) *lowered {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0x9a5))
	q := "a."
	if raiser == "a" {
		q = ""
	}
	var funcs, calls strings.Builder
	funcs.WriteString(fmt.Sprintf("func missing(k string) %[1]sMissing { %[1]sMissing{Key: k} }\n", q))
	funcs.WriteString(fmt.Sprintf("func probe(k string) int fails %[1]sMissing {\n\tif k != \"\" {\n\t\treturn %[1]sMissing{Key: k}\n\t}\n\t1\n}\n", q))
	type row struct{ form, position, target string }
	var rows []row
	for _, form := range evForms {
		for _, position := range evPositions {
			for _, target := range []string{"value", "failure"} {
				if form == "catch" && position == "tail" {
					continue
				}
				rows = append(rows, row{form, position, target})
			}
		}
	}
	r.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	call := map[string]string{"a": "a.", "b": "b.", "main": ""}[raiser]
	for i, w := range rows {
		name := fmt.Sprintf("G%d", i)
		f, _ := evFunc(name, w.form, w.position, w.target)
		if q != "" {
			// The type is a's; missing and probe are the raiser's own.
			f = strings.ReplaceAll(f, "Missing", q+"Missing")
		}
		funcs.WriteString(f)
		key := fmt.Sprintf("k%d", r.IntN(1000))
		switch w.target {
		case "value":
			fmt.Fprintf(&calls, "\tConsole.Println(%s%s(%q).Key)\n", call, name, key)
		case "failure":
			fmt.Fprintf(&calls, "\tlet v%[2]s = %[1]s%[2]s(%[3]q) catch {\n\tcase a.Missing as m:\n\t\tConsole.Println(m.Key)\n\t\t0\n\t}\n\tConsole.Println(\"\\(v%[2]s)\")\n", call, name, key)
		}
	}
	aText := "type Missing { Key string }\n"
	if declared {
		aText += "func Raise(k string) int fails Missing { Missing{Key: k} }\n"
	}
	var raised string
	switch raiser {
	case "a":
		aText += funcs.String()
	case "b":
		raised = "import a\n\n" + funcs.String()
	case "main":
		raised = funcs.String()
	}
	pa := checkPackage(t, "a", []pkgSource{{"/ergo-test/a/a.ergo", aText}}, nil)
	imp := pkgImporter{"a": pa.Package}
	imports := []Unit{pa}
	mainImports := "import a\n"
	if raiser == "b" {
		pb := checkPackage(t, "b", []pkgSource{{"/ergo-test/b/b.ergo", raised}}, imp)
		imp["b"] = pb.Package
		imports = append(imports, pb)
		mainImports += "import b\n"
		raised = ""
	}
	mainText := mainImports + "\n" + raised + "func main() needs Console {\n" + calls.String() + "}\n"
	pm := checkPackage(t, "main", []pkgSource{{"/ergo-test/main/main.ergo", mainText}}, imp)
	pm.Imports = imports
	name := fmt.Sprintf("errimporter_%d_%s_%t", seed, raiser, declared)
	o := mustLowerUnit(t, name, pm, false)
	o.src = fmt.Sprintf("// replay: go test ./internal/sketch/lower -run TestFailureRaisedByImporter -errimporter.seed=%d\n// a/a.ergo\n%s", seed, aText)
	for _, u := range imports[1:] {
		o.src += "// b/b.ergo\n" + u.Sources[0].Text
	}
	o.src += "// main/main.ergo\n" + mainText
	return o
}
