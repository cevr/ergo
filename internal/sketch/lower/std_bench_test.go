package lower

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdBenchProgram is std bench (the statistics of ergo bench,
// docs/specs/bench.md) as one test-build program: its files in name order,
// with every file's imports moved to the top once. Its laws are then the
// program's own, and the test build runs each as written in std, so this
// runner restates no law by hand and cannot drift from the source.
var stdBenchProgram = stdPackageProgram("bench")

// stdPackageProgram is the source of the std package at path as one
// program: the import lines of its files first, each once, then the rest
// of each file in name order. It panics when std has no such package, as
// a program table declared at init cannot report to a test.
func stdPackageProgram(path string) string {
	files, ok := std.Package(path)
	if !ok {
		panic("std has no package " + path)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	var imports, body strings.Builder
	for _, name := range names {
		for _, line := range strings.Split(files[name], "\n") {
			if strings.HasPrefix(line, "import ") {
				if !seen[line] {
					seen[line] = true
					imports.WriteString(line + "\n")
				}
				continue
			}
			body.WriteString(line + "\n")
		}
	}
	return imports.String() + "\n" + body.String()
}

// TestStdBenchLawsRun runs the laws of std bench as the program's own laws
// with --seed 1, as ergo test --seed 1 does, and gives lawledger.Run each
// law with its source text, which the ledger rows must equal, and the
// check that its ok line was printed.
//
// It builds Go because the laws run in the built test binary (ergo test);
// the checker's evaluator skips them, since they call Go bodies (math,
// slices).
func TestStdBenchLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdbench"), "--seed", "1")
	if code != 0 {
		t.Errorf("the std bench laws failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	f, err := syntax.Parse(stdBenchProgram)
	if err != nil {
		t.Fatal(err)
	}
	laws := map[string]lawledger.Law{}
	for _, d := range f.Decls {
		law, ok := d.(*syntax.LawDecl)
		if !ok {
			continue
		}
		name := law.Name.Name
		ran := regexp.MustCompile(`(?m)^ok law ` + name + ` \((\d+ cases|proved: (all \d+ cases|1 case))\)$`)
		laws["bench "+name] = lawledger.Law{
			Statement: stdBenchProgram[law.Name.Pos():law.Body.End()],
			Check: func(t *testing.T) {
				if !ran.MatchString(stdout) {
					t.Errorf("no ok line for law %s:\n%s", name, stdout)
				}
			},
		}
	}
	if len(laws) == 0 {
		t.Fatal("std bench declares no law")
	}
	lawledger.Run(t, laws)
}
