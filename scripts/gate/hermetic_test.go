package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/importer"
	"go/token"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/hermetic"
)

// TestCachedGatePackagesAreHermetic checks every package of the steps that
// keep Go's test cache: the hermetic exception (Step.Hermetic) to the gate's
// uncached default. Go's test cache records files, environment variables and
// the module's source, not the programs a test runs or the libraries they
// load, so a package may replay only when it is a gate package below
// hermeticTestRoot, its test binary is hermetic (package hermetic), built as
// its step builds it, and the step builds it with CGO_ENABLED=0. Every other
// step runs with -count=1 (TestTestCommandsHaveCacheCoverage), so it never
// replays. This test is itself a host test: it runs the go command.
func TestCachedGatePackagesAreHermetic(t *testing.T) {
	t.Parallel()
	goCommand := realTools["go"]
	if goCommand == "" {
		t.Fatal("go is required")
	}
	root := checkoutRoot(t)
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(root)
	checked := 0
	for _, step := range steps {
		if !step.Hermetic {
			continue
		}
		if !runsGoTest(step) {
			t.Errorf("step %q keeps Go's test cache, but it is not a go test of hermetic gate packages: %q", step.Name, step.Argv)
			continue
		}
		environment := setEnvironmentVariable(stepCommandEnvironment(root, step), "TMPDIR", t.TempDir())
		for _, pkg := range step.Argv[2:] {
			if strings.HasPrefix(pkg, "-") {
				continue
			}
			relative, ok := strings.CutPrefix(pkg, module+"/")
			if !ok || !strings.HasPrefix(relative, hermeticTestRoot) || slices.Contains(hostTestPackages, relative) || strings.Contains(relative, "...") {
				t.Errorf("step %q keeps Go's test cache, but %s is not a gate package below %s", step.Name, pkg, hermeticTestRoot)
				continue
			}
			checked++
			if cgo, _ := environmentVariable(environment, "CGO_ENABLED"); cgo != "0" {
				t.Errorf("%s replays from Go's test cache (step %q), but its step builds it with CGO_ENABLED=%q, not 0, so it may load shared libraries", pkg, step.Name, cgo)
			}
			violations, err := hermetic.Closure(hermeticBuilds(t, goCommand, root, environment, pkg), module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range violations {
				t.Errorf("%s replays from Go's test cache (step %q), but %s", pkg, step.Name, violation)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no hermetic step lets Go's test cache replay a package below %s, so nothing was checked", hermeticTestRoot)
	}
}

// shadowFixtures are compiling test packages that bind a program to a name
// spelled os.Executable which is not the os package's function (round 4 B1,
// round-4-receipts/verify_shadow_test.go, and its variants), with the
// packages that do run the test binary as controls. A fixture that the check
// admitted would replay its result after the program it runs changed.
var shadowFixtures = map[string]struct{ source, refusal string }{
	"shadow": {`package shadow

import (
	"os"
	"os/exec"
	"testing"
)

func TestShadow(t *testing.T) {
	_ = os.ErrNotExist
	os := struct{ Executable func() (string, error) }{
		Executable: func() (string, error) { return "/nonexistent/loop-probe-x/external-tool", nil },
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_ = exec.Command(self).Run()
}
`, "self is not bound to os.Executable()"},
	"dotlocal": {`package dotlocal

import (
	. "os"
	"os/exec"
	"testing"
)

func TestDotLocal(t *testing.T) {
	_ = Getpid()
	Executable := func() (string, error) { return "/nonexistent/loop-probe-x", nil }
	self, _ := Executable()
	_ = exec.Command(self).Run()
}
`, "self is not bound to os.Executable()"},
	"parameter": {`package parameter

import (
	"os"
	"os/exec"
	"testing"
)

func run(os struct{ Executable func() (string, error) }) {
	self, _ := os.Executable()
	_ = exec.Command(self).Run()
}

func TestParameter(t *testing.T) {
	_ = os.ErrNotExist
	run(struct{ Executable func() (string, error) }{func() (string, error) { return "/nonexistent/loop-probe-x", nil }})
}
`, "self is not bound to os.Executable()"},
	"alias": {`package alias

import (
	os "example.com/shadow/fake"
	"os/exec"
	"testing"
)

func TestAlias(t *testing.T) {
	self, _ := os.Executable()
	_ = exec.Command(self).Run()
}
`, "self is not bound to os.Executable()"},
	"packagevar": {`package packagevar

import (
	"os/exec"
	"testing"
)

var os = struct{ Executable func() (string, error) }{func() (string, error) { return "/nonexistent/loop-probe-x", nil }}

func TestPackageVar(t *testing.T) {
	self, _ := os.Executable()
	_ = exec.Command(self).Run()
}
`, "self is not bound to os.Executable()"},
	"method": {`package method

import (
	"os/exec"
	"testing"
)

type system struct{}

func (system) Executable() (string, error) { return "/nonexistent/loop-probe-x", nil }

func TestMethod(t *testing.T) {
	executable := system{}.Executable
	self, _ := executable()
	_ = exec.Command(self).Run()
}
`, "self is not bound to os.Executable()"},
	"control": {`package control

import (
	"os"
	"os/exec"
	"testing"
)

func TestControl(t *testing.T) {
	if os.Getenv("CONTROL_CHILD") != "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_ = exec.Command(self, "-test.run=^$").Run()
}
`, ""},
	"dotos": {`package dotos

import (
	. "os"
	"os/exec"
	"testing"
)

func TestDotOS(t *testing.T) {
	self, err := Executable()
	if err != nil {
		t.Fatal(err)
	}
	_ = exec.Command(self, "-test.run=^$").Run()
}
`, ""},
}

// TestHermeticCheckResolvesExecutableByIdentity runs the gate's check, with
// the real standard library, on the compiling shadow fixtures: each one that
// binds another program is refused, and the real os.Executable, also through
// a dot import, is admitted. go list -export compiles every fixture first.
func TestHermeticCheckResolvesExecutableByIdentity(t *testing.T) {
	t.Parallel()
	goCommand := realTools["go"]
	if goCommand == "" {
		t.Fatal("go is required")
	}
	root := checkoutRoot(t)
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	var environment []string
	for _, step := range steps {
		if slices.Contains(step.Argv, modulePath(root)+"/"+strings.TrimSuffix(hermeticTestRoot, "/")) {
			environment = stepCommandEnvironment(root, step)
		}
	}
	if environment == nil {
		t.Fatal("no gate step tests the hermetic package")
	}
	environment = setEnvironmentVariable(environment, "TMPDIR", t.TempDir())
	module := t.TempDir()
	write(t, module, "go.mod", "module example.com/shadow\n\ngo 1.27\n")
	write(t, module, "fake/fake.go", "package fake\n\nfunc Executable() (string, error) { return \"/nonexistent/loop-probe-x\", nil }\n")
	for name, fixture := range shadowFixtures {
		write(t, module, name+"/"+name+"_test.go", fixture.source)
	}
	linux := []struct{ goos, goarch string }{{"linux", "amd64"}}
	for _, name := range slices.Sorted(maps.Keys(shadowFixtures)) {
		fixture := shadowFixtures[name]
		pkg := "example.com/shadow/" + name
		violations, err := hermetic.Closure(configuredBuilds(t, goCommand, module, environment, pkg, linux), "example.com/shadow", pkg)
		if err != nil {
			t.Fatal(err)
		}
		if fixture.refusal == "" {
			if len(violations) != 0 {
				t.Errorf("%s runs the test binary, but the check refused it: %v", name, violations)
			}
			continue
		}
		if !slices.ContainsFunc(violations, func(v hermetic.Violation) bool { return strings.Contains(v.Reason, fixture.refusal) }) {
			t.Errorf("%s binds another program, but the check did not refuse it with %q: %v", name, fixture.refusal, violations)
		}
	}
}

// hermeticConfigurations are the build configurations the check type-checks
// a hermetic package in: the platforms the gate runs on (Linux on amd64, the
// workbox; macOS on arm64) and Linux on arm64, where testinputs observes too.
// Every file of a module package in the binary must be built in one of them.
var hermeticConfigurations = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
}

// hermeticBuilds lists pkg's test binary in each of hermeticConfigurations,
// in environment, with an importer of that configuration's standard library
// from the export data go list builds.
func hermeticBuilds(t *testing.T, goCommand, root string, environment []string, pkg string) []hermetic.Build {
	return configuredBuilds(t, goCommand, root, environment, pkg, hermeticConfigurations)
}

// configuredBuilds lists pkg's test binary in each of configurations.
func configuredBuilds(t *testing.T, goCommand, root string, environment []string, pkg string, configurations []struct{ goos, goarch string }) []hermetic.Build {
	t.Helper()
	var builds []hermetic.Build
	for _, c := range configurations {
		configured := setEnvironmentVariable(setEnvironmentVariable(environment, "GOOS", c.goos), "GOARCH", c.goarch)
		packages, exports := testBinaryPackages(t, goCommand, root, configured, pkg)
		lookup := func(path string) (io.ReadCloser, error) {
			file := exports[path]
			if file == "" {
				return nil, fmt.Errorf("no export data for %s in %s/%s", path, c.goos, c.goarch)
			}
			return os.Open(file)
		}
		builds = append(builds, hermetic.Build{GOOS: c.goos, GOARCH: c.goarch, Packages: packages, Std: importer.ForCompiler(token.NewFileSet(), "gc", lookup)})
	}
	return builds
}

// testBinaryPackages lists the packages of pkg's test binary as go list sees
// them in environment, and the export data files of its standard packages.
func testBinaryPackages(t *testing.T, goCommand, root string, environment []string, pkg string) ([]hermetic.Package, map[string]string) {
	t.Helper()
	command := exec.Command(goCommand, "list", "-deps", "-test", "-export", "-json", pkg)
	command.Dir = root
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("go list -deps -test -export %s: %v\n%s", pkg, err, exit.Stderr)
		}
		t.Fatalf("go list -deps -test -export %s: %v", pkg, err)
	}
	var packages []hermetic.Package
	exports := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var p struct {
			hermetic.Package
			Export string
		}
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p.Package)
		if p.Standard {
			exports[p.ImportPath] = p.Export
		}
	}
	return packages, exports
}
