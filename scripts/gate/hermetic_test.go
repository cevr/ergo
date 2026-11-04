package main

import (
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/hermetic"
)

// TestCachedGatePackagesAreHermetic checks every gate package (this one and
// the packages below it) that a gate step lets Go's test cache replay. Go's
// test cache records files, environment variables and the module's source,
// not the programs a test runs or the libraries they load, so a gate package
// may replay only when its test binary is hermetic (package hermetic), built
// as its step builds it, and the step builds it with CGO_ENABLED=0. A host
// package runs with -count=1 on every gate, so it is not checked: it never
// replays. This test is itself a host test: it runs the go command.
func TestCachedGatePackagesAreHermetic(t *testing.T) {
	t.Parallel()
	goCommand := realTools["go"]
	if goCommand == "" {
		t.Fatal("go is required")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	module := modulePath(root)
	checked := 0
	for _, step := range steps {
		if !runsGoTest(step) || slices.Contains(step.Argv, "-count=1") {
			continue
		}
		environment := setEnvironmentVariable(stepCommandEnvironment(root, step), "TMPDIR", t.TempDir())
		for _, pkg := range step.Argv[2:] {
			relative, ok := strings.CutPrefix(pkg, module+"/")
			if !ok || relative+"/" != hermeticTestRoot && !strings.HasPrefix(relative, hermeticTestRoot) {
				continue
			}
			checked++
			if cgo, _ := environmentVariable(environment, "CGO_ENABLED"); cgo != "0" {
				t.Errorf("%s replays from Go's test cache (step %q), but its step builds it with CGO_ENABLED=%q, not 0, so it may load shared libraries", pkg, step.Name, cgo)
			}
			violations, err := hermetic.Closure(testBinaryPackages(t, goCommand, root, environment, pkg), module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range violations {
				t.Errorf("%s replays from Go's test cache (step %q), but %s", pkg, step.Name, violation)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no gate step lets Go's test cache replay a package below %s, so nothing was checked", hermeticTestRoot)
	}
}

// testBinaryPackages lists the packages of pkg's test binary as go list sees
// them in environment.
func testBinaryPackages(t *testing.T, goCommand, root string, environment []string, pkg string) []hermetic.Package {
	t.Helper()
	command := exec.Command(goCommand, "list", "-deps", "-test", "-json", pkg)
	command.Dir = root
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("go list -deps -test %s: %v\n%s", pkg, err, exit.Stderr)
		}
		t.Fatalf("go list -deps -test %s: %v", pkg, err)
	}
	var packages []hermetic.Package
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var p hermetic.Package
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p)
	}
	return packages
}
