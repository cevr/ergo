package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// scopeSandboxTests are the tests that start the merge tier's sandbox and
// also start gates in systemd scopes, with the reason they stay in the host
// package's step: the scope tests of scripts/gate run one at a time there
// (maxHostTestParallel).
var scopeSandboxTests = map[string]string{
	"TestGateStopsItsStepsOnSignal": "its cases run gates in systemd scopes",
}

// sandboxTestsOf lists the top-level tests of the package in dir that call
// startsSandbox, from every _test.go file whatever its build constraint.
func sandboxTestsOf(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var tests []string
	fset := token.NewFileSet()
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || function.Recv != nil || !strings.HasPrefix(function.Name.Name, "Test") {
				continue
			}
			calls := false
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "startsSandbox" {
						calls = true
					}
				}
				return !calls
			})
			if calls {
				tests = append(tests, function.Name.Name)
			}
		}
	}
	slices.Sort(tests)
	return tests
}

// selects reports whether a go test command line selects the top-level
// test name: it matches the -run pattern, if any, and not the -skip one.
func selects(t *testing.T, argv []string, name string) bool {
	t.Helper()
	pattern := func(flag string) (*regexp.Regexp, bool) {
		i := slices.Index(argv, flag)
		if i < 0 || i+1 >= len(argv) {
			return nil, false
		}
		re, err := regexp.Compile(argv[i+1])
		if err != nil {
			t.Fatalf("%s %q: %v", flag, argv[i+1], err)
		}
		return re, true
	}
	if run, ok := pattern("-run"); ok && !run.MatchString(name) {
		return false
	}
	if skip, ok := pattern("-skip"); ok && skip.MatchString(name) {
		return false
	}
	return true
}

// The full tier runs each test of scripts/gate that starts the merge
// tier's sandbox, and no gate in a systemd scope, in a step of its own
// (sandboxHostTests), and every test of the package in exactly one step:
// the serial sandbox tests no longer add up in one step, which at batch 9
// passed the 15 minute testTimeout under load (b9-fix-gate).
func TestSandboxHostTestsRunInStepsOfTheirOwn(t *testing.T) {
	root := checkoutRoot(t)
	sandboxTests := sandboxTestsOf(t, ".")
	if len(sandboxTests) == 0 {
		t.Fatal("no test of scripts/gate calls startsSandbox; the check is vacuous")
	}
	for name := range scopeSandboxTests {
		if !slices.Contains(sandboxTests, name) {
			t.Errorf("scopeSandboxTests names %s, which does not call startsSandbox; remove it", name)
		}
	}
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	pkg := modulePath(root) + "/scripts/gate"
	var own []*Step
	for _, step := range steps {
		if runsGoTest(step) && slices.Contains(step.Argv, pkg) {
			own = append(own, step)
		}
	}
	names, err := testNames(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		var selecting []*Step
		for _, step := range own {
			if selects(t, step.Argv, name) {
				selecting = append(selecting, step)
			}
		}
		if len(selecting) != 1 {
			t.Errorf("%s runs in %d steps of the full tier, want 1", name, len(selecting))
			continue
		}
		_, scoped := scopeSandboxTests[name]
		isolated := slices.Contains(selecting[0].Argv, "-run")
		want := slices.Contains(sandboxTests, name) && !scoped
		if isolated != want {
			t.Errorf("%s: in a step of its own %t, want %t (it starts the merge tier's sandbox: %t; it starts scopes: %t): step %q %q", name, isolated, want, slices.Contains(sandboxTests, name), scoped, selecting[0].Name, selecting[0].Argv)
		}
		if isolated && !slices.Equal(selecting[0].Argv[slices.Index(selecting[0].Argv, "-run")+1:][:1], []string{exactNames([]string{name})}) {
			t.Errorf("%s: the step %q runs %q, not that test alone", name, selecting[0].Name, selecting[0].Argv)
		}
	}
}
