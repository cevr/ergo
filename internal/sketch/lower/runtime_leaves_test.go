package lower

import (
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// TestMainImportsOnlyTheRuntimeLeavesItCalls: a program imports a leaf of
// runtime/v1 only when it names one of its declarations (binsize BS1). The
// Go linker keeps the init task of every imported package, so hello must
// import runtime/v1 and runtime/rt and no leaf, and a program that calls
// hex.Encode and reads a csv.ParseError imports exactly the digest and csv
// leaves. The gofmt leaf, which links fmt for its init alone (binsize
// BS2), is imported by a program that imports go and by one with a
// machine timer, whose defect writes the state with fmt, and by no
// other. The hostwalk leaf (binsize BS8), which links reflect for its
// init alone, is imported by a program that makes an actor's host, whose
// recovered state the runtime walks for hosts, and by no other; the http
// leaf, which links net/http, by a program that calls an http body. The
// import set is the size rule itself, not a shape of the lowered Go.
// Mutants: bs1-import-all-leaves (main.go imports every leaf),
// bs1-leaf-in-core (rtRef names every declaration through runtime/v1),
// bs2-hook-always (every program imports gofmt), bs2-hook-never (a
// program that imports go does not), bs8-walk-always (every program
// imports hostwalk), bs8-walk-never (a program that makes a host does
// not) and bs8-probe-imports (a test whether a std type is the runtime's
// names it, importing its leaf).
func TestMainImportsOnlyTheRuntimeLeavesItCalls(t *testing.T) {
	const leaf = runtimeImport + "/"
	cases := []struct {
		name, src string
		want      []string
	}{
		{"hello", "func main() needs Console {\n\tConsole.Println(\"hello\")\n}\n", nil},
		{"digest and csv", "import csv\nimport hex\n\nfunc main() needs Console {\n\tlet rows = csv.ReadAll(hex.Encode(\"a\")) catch {\n\t\tcase csv.ParseError: [][]string{}\n\t}\n\tConsole.Println(\"\\(len(rows))\")\n}\n", []string{leaf + "csv", leaf + "digest"}},
		{"import go", "import go strconv\n\nfunc main() needs Console {\n\tlet n = strconv.ParseInt(\"12\", 10, 64) catch { case go.Error: -1 }\n\tConsole.Println(\"\\(n)\")\n}\n", []string{leaf + "gofmt"}},
		{"machine timer", machineTimerProgram(1, false), []string{leaf + "gofmt"}},
		{"actor host", "import actor\n\nfunc main() needs Console {\n\tlet _ = actor.NewHost[int]()\n\tConsole.Println(\"x\")\n}\n", []string{leaf + "hostwalk"}},
		{"http", "import http\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(len(http.ParseQuery(\"a=1\")))\")\n}\n", []string{leaf + "http"}},
	}
	check := func(name string, out *lowered, want []string) {
		t.Helper()
		if out.err != nil || len(out.diags) > 0 || len(out.refused) > 0 || out.panicked != nil {
			t.Fatalf("%s: %v %v %v %v", name, out.err, out.diags, out.refused, out.panicked)
		}
		file, err := parser.ParseFile(token.NewFileSet(), "main.go", out.mainGo(), parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		var leaves []string
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if len(path) > len(leaf) && path[:len(leaf)] == leaf {
				leaves = append(leaves, path)
			}
		}
		slices.Sort(leaves)
		if !slices.Equal(leaves, want) {
			t.Errorf("%s imports the runtime leaves %v; want %v", name, leaves, want)
		}
		typecheckGo(t, []*lowered{out})
	}
	for _, c := range cases {
		check(c.name, lowerSource(c.name, c.src, true), c.want)
	}
	// An imported package's function that main never calls is not lowered
	// (reach), so a leaf only it would name is not imported: a std type's
	// operation call there is classified without naming the type (the bug
	// of examples/calc, whose ast.Samples alone drew from rand.Seeded).
	gen := checkPackage(t, "gen", []pkgSource{{"/ergo-test/gen/gen.ergo", "import rand\n\nfunc Name() string { \"gen\" }\n\nfunc Samples(seed int) int {\n\twith context { Random: rand.Seeded(uint64(seed)) } {\n\t\tRandom.IntN(6)\n\t}\n}\n"}}, nil)
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/main/main.ergo", "import gen\n\nfunc main() needs Console {\n\tConsole.Println(gen.Name())\n}\n"}}, pkgImporter{"gen": gen.Package})
	main.Imports = []Unit{gen}
	check("unreached rand", mustLowerUnit(t, "unreached rand", main, false), nil)
}
