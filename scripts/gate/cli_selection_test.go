package main

import (
	"slices"
	"strings"
	"testing"
)

// cliBase is a fixture whose package c is a command whose test runs it, as
// cmd/ergo's tests run the CLI (gate-speed GS11): TestCLI runs the
// executable the record hands it (SEL_CLI), or builds its own; TestD calls
// the library directly.
var cliBase = map[string]string{
	"go.mod": "module example.test/cli\n\ngo 1.24\n",
	"lib/lib.go": `package lib

// B is what the command prints.
func B() int {
	x := 2
	return x
}

// D is what TestD calls.
func D() int {
	y := 4
	return y
}
`,
	"c/c.go": `package main

import (
	"fmt"

	"example.test/cli/lib"
)

func main() {
	fmt.Println(lib.B())
}
`,
	"c/c_test.go": `package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"example.test/cli/lib"
)

func TestCLI(t *testing.T) {
	path := os.Getenv("SEL_CLI")
	if path == "" {
		path = filepath.Join(t.TempDir(), "c")
		if out, err := exec.Command("go", "build", "-o", path, ".").CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	// The command runs in an environment of the test's own, as cmd/ergo's
	// tests give the CLI a private cache: GOCOVERDIR is not passed on.
	command := exec.Command(path)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := command.Output()
	if err != nil || string(out) != "2\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestD(t *testing.T) {
	if lib.D() != 4 {
		t.Fatal("D")
	}
}
`,
}

var cliNames = []string{"TestCLI", "TestD"}

// TestSelectionSeesTheCLIATestRuns is the selection law for a package whose
// tests run its own command (recordedCLIs; gate-speed GS11): after an edit
// of a function body, the selection runs exactly the tests that ran it,
// in the test process or in the command. An edit of the body the command
// runs reaches TestCLI, whose verdict it changes; an edit of the body only
// TestD calls reaches TestD alone, not TestCLI, which a CLI built from
// source in the test would tie to every source file.
//
// Mutants it catches, which TestSelectionRunsEveryTestAnEditReaches does
// not (its fixture has no command): gs11-cli-unrecorded,
// gs11-cli-counters-lost (scripts/gate's ledger).
//
// It builds Go because the law is about what go test runs, as
// TestSelectionRunsEveryTestAnEditReaches does.
func TestSelectionSeesTheCLIATestRuns(t *testing.T) {
	if realTools["go"] == "" {
		t.Skip("no go")
	}
	recorded := recordedCLIs
	recordedCLIs = map[string]string{"c": "SEL_CLI"}
	defer func() { recordedCLIs = recorded }()
	root := t.TempDir()
	for name, content := range cliBase {
		write(t, root, name, content)
	}
	env := append(stepEnvironment(), "GOWORK=off", "GOTMPDIR="+t.TempDir())
	listed, _, err := testPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(listed, func(p testPackage) bool { return p.dir == "c" })
	if at < 0 {
		t.Fatalf("no test package c in %v", listed)
	}
	p := listed[at]
	cache := t.TempDir()
	record, err := recordCoverage(root, cache, passThroughLimiter{}, p, env, env, cliNames, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range record.Tests {
		if test.Always != "" {
			t.Fatalf("%s runs on every edit at the record: %s", test.Name, test.Always)
		}
	}
	if err := saveCoverageRecord(coveragePath(cache, p.dir), record); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		name, file, from, to string
		reaches              []string
	}{
		{"the body the command runs", "lib/lib.go", "\tx := 2\n", "\tx := 3\n", []string{"TestCLI"}},
		{"the command's main", "c/c.go", "lib.B())", "lib.B()+1)", []string{"TestCLI"}},
		{"the body only TestD calls", "lib/lib.go", "\ty := 4\n", "\ty := 5\n", []string{"TestD"}},
	} {
		write(t, root, e.file, strings.Replace(cliBase[e.file], e.from, e.to, 1))
		chosen := selectTests(root, cache, p, env, cliNames)
		var run []string
		for _, name := range cliNames {
			if !slices.Contains(chosen.skip, name) {
				run = append(run, name)
			}
		}
		verdicts := selectionVerdicts(t, root, env, "c", cliNames)
		for _, name := range cliNames {
			if verdicts[name] != "pass" && !slices.Contains(run, name) {
				t.Errorf("after %s, %s %s, and the selection skips it (%s)", e.name, name, verdicts[name], chosen.note)
			}
		}
		if !slices.Equal(run, e.reaches) {
			t.Errorf("after %s the selection runs %v (%s); want %v", e.name, run, chosen.note, e.reaches)
		}
		write(t, root, e.file, cliBase[e.file])
	}
}
