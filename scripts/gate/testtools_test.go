package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hostPathVariable carries the host's PATH into a test binary a test runs
// again as a helper. Its presence says that PATH already is the tests' PATH,
// with whatever the test put first.
const hostPathVariable = "ERGO_GATE_TESTS_HOST_PATH"

// setTestsPath makes bin, which holds the go stand-in, the tests' whole PATH,
// with a link to each test tool (linkTestTools). In a helper process a test
// started, PATH already is the tests' PATH, with what the test put first, so
// bin only goes first.
func setTestsPath(bin string) error {
	if inherited, ok := os.LookupEnv(hostPathVariable); ok {
		hostPath = inherited
		return os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	if err := linkTestTools(bin); err != nil {
		return err
	}
	hostPath = os.Getenv("PATH")
	if err := os.Setenv(hostPathVariable, hostPath); err != nil {
		return err
	}
	return os.Setenv("PATH", bin)
}

// linkTestTools links into bin, by name, each tool of testTools that PATH
// finds, except go, whose stand-in TestMain writes there. With bin the whole
// PATH, a test that runs a tool by name runs a tool the tool digest keys, or
// fails to find it: a failed run is never cached. A tool testTools names by
// an absolute path runs by that path and needs no link.
func linkTestTools(bin string) error {
	for _, tool := range testTools {
		if tool == "go" || filepath.IsAbs(tool) {
			continue
		}
		found, err := exec.LookPath(tool)
		if err != nil {
			continue // absent here: a test that needs it fails or skips
		}
		if found, err = filepath.Abs(found); err != nil {
			return err
		}
		if err := os.Symlink(found, filepath.Join(bin, tool)); err != nil {
			return err
		}
	}
	return nil
}

// hostPath is the PATH the test binary started with, before TestMain made
// the tests' PATH hold only the test tools.
var hostPath string

// needTool returns the file PATH finds for a tool a test runs, and skips the
// test where the host has no such tool, saying why. A tool the host has but
// testTools does not key fails the test instead: the tests' PATH hides it,
// and a skip would silently drop what the test checks.
func needTool(t *testing.T, tool, why string) string {
	t.Helper()
	if found, err := exec.LookPath(tool); err == nil {
		return found
	}
	for _, dir := range filepath.SplitList(hostPath) {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, tool)); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			t.Fatalf("%s is in %s on the host's PATH, but testTools does not key it, so the tests do not find it", tool, dir)
		}
	}
	t.Skip(why)
	return ""
}

// TestTestsFindOnlyTheKeyedTools checks the tests' PATH (TestMain): it is
// one directory, holding the go stand-in and a link for each tool of
// testTools found on the test binary's PATH, and nothing else. So every tool
// the tests run by name is keyed.
func TestTestsFindOnlyTheKeyedTools(t *testing.T) {
	path, _ := environmentVariable(startEnvironment, "PATH")
	bin := filepath.Join(testScratch, "bin")
	if path != bin {
		t.Fatalf("the tests' PATH is %q, not only %q", path, bin)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if name := entry.Name(); !slices.Contains(testTools, name) {
			t.Errorf("the tests' PATH holds %s, which testTools does not key", name)
		}
	}
}

// TestGoTestStepsKeyTheTestTools checks that every gate step that runs Go
// tests keys testTools: the tools a test runs reach Go's test cache only
// through the step's tool digest.
func TestGoTestStepsKeyTheTestTools(t *testing.T) {
	_, steps := repositorySteps(t)
	checked := 0
	for _, step := range steps {
		if !runsGoTest(step) || slices.Contains(step.Argv, "^$") {
			continue // go test -run ^$ builds the test binaries and runs no test
		}
		checked++
		if !slices.Equal(step.ToolEnv, testTools) {
			t.Errorf("step %q keys tools %q, not testTools %q", step.Name, step.ToolEnv, testTools)
		}
	}
	if checked == 0 {
		t.Fatal("no step runs Go tests")
	}
}

// shebang matches the interpreter a script's #! line names.
var shebang = regexp.MustCompile(`(?m)^#!\s*(\S+)`)

// TestToolsRunByPathAreKeyed checks the executables this package's code and
// tests name by an absolute path, which PATH does not select: the
// interpreter of every script they write (#!), the program of an
// exec.Command or exec.CommandContext call, and the first word of a step's
// Argv. Each must be in testTools, so the tool digest keys its bytes.
func TestToolsRunByPathAreKeyed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	set := token.NewFileSet()
	literal := func(expression ast.Expr) (string, bool) {
		lit, ok := expression.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(lit.Value)
		return value, err == nil
	}
	for _, name := range files {
		file, err := parser.ParseFile(set, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			var program ast.Expr
			switch node := node.(type) {
			case *ast.BasicLit:
				if value, ok := literal(node); ok {
					for _, match := range shebang.FindAllStringSubmatch(value, -1) {
						found[match[1]] = set.Position(node.Pos()).String()
					}
				}
			case *ast.CallExpr:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok && len(node.Args) > 0 {
					if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "exec" {
						switch selector.Sel.Name {
						case "Command":
							program = node.Args[0]
						case "CommandContext":
							if len(node.Args) > 1 {
								program = node.Args[1]
							}
						}
					}
				}
			case *ast.KeyValueExpr:
				if key, ok := node.Key.(*ast.Ident); ok && key.Name == "Argv" {
					if words, ok := node.Value.(*ast.CompositeLit); ok && len(words.Elts) > 0 {
						program = words.Elts[0]
					}
				}
			}
			if value, ok := literal(program); ok && strings.HasPrefix(value, "/") {
				found[value] = set.Position(program.Pos()).String()
			}
			return true
		})
	}
	if len(found) == 0 {
		t.Fatal("found no executable named by path; the scan is broken")
	}
	for program, position := range found {
		if !slices.Contains(testTools, program) {
			t.Errorf("%s runs %s by its path, which testTools does not key", position, program)
		}
	}
}

// A Go test step keys every tool in testTools, so a cached result of a test
// that runs one of them replays only while its bytes are unchanged. Go's test
// cache rechecks only files inside the module root; an executable outside it
// reaches the key only through the step's tool digest (ERGO_TOOL_VERSIONS,
// which testinputs records as read for any test that runs a program).
//
// The probe package's test runs seq, as the output-storage property does, and
// passes only on the bytes it was cached with. The seq on PATH is a probe
// copy, rewritten in place between runs: same path, same PATH, same size,
// other bytes. The step that ran it must run again, and fail, instead of
// replaying the earlier pass.
func TestChangedTestToolRerunsTheGoTest(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	link(t, bin, "seq", probe(t, filepath.Join(t.TempDir(), "seq")))
	root := fixture(t)
	write(t, root, "probe/go.mod", "module probe\n\ngo 1.24\n")
	// A fresh package each run, so the first gate below never finds a result
	// another run of this test cached.
	write(t, root, "probe/probe_test.go", fmt.Sprintf(`package probe

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// run %s %d
func TestSeq(t *testing.T) {
	_ = os.Getenv(%q) // what testinputs records for a test that runs a program
	output, err := exec.Command("seq", "--build").Output()
	if got := strings.TrimSpace(string(output)); err != nil || got != %q {
		t.Fatalf("seq --build printed %%q (%%v)", got, err)
	}
}
`, t.Name(), time.Now().UnixNano(), "ERGO_TOOL_VERSIONS", probeBuildMarker))
	steps := func() []*Step {
		return []*Step{{Name: "go test probe", Argv: []string{"go", "test", "-C", "probe", "."}, ToolEnv: testTools}}
	}
	cache := t.TempDir()
	code, out := runGate(t, root, cache, steps())
	if code != 0 || strings.Contains(out, "(cached)") {
		t.Fatalf("exit %d; the first run did not run the probe package and pass:\n%s", code, out)
	}
	code, out = runGate(t, root, cache, steps())
	if code != 0 || !strings.Contains(out, "(cached)") {
		t.Fatalf("exit %d; an unchanged run did not replay the probe package:\n%s", code, out)
	}
	rebuildProbe(t, filepath.Join(bin, "seq"), "probe-build-marker-b")
	code, out = runGate(t, root, cache, steps())
	if strings.Contains(out, "(cached)") || code == 0 {
		t.Fatalf("exit %d; changing seq's bytes at the same path replayed the earlier pass:\n%s", code, out)
	}
}
