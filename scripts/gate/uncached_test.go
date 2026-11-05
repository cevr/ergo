package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// innerResult is one result line of the inner go test in a run's log.
var innerResult = regexp.MustCompile(`(?m)^ok\s+example\.com/probe/inner\s.*$`)

// TestProgramsStartUncachedGoTests runs the round 3 B1 forms as real gate
// steps, twice, on a small module: a go run -exec, a go test -exec, a go
// build -toolexec, a go vet -toolexec and a go vet -vettool, each a program
// that starts go test ./inner with no count of its own. A static reader of
// argv does not see that go test; the gate's default does reach it. With the
// default (an ordinary step), the inner go test runs on both gate runs and
// never prints (cached). As the control, the same steps run as the hermetic
// exception, without the default: the second run's inner go test is
// (cached), so the inner go test was cacheable and the default is what
// stops the replay.
func TestProgramsStartUncachedGoTests(t *testing.T) {
	// The caller's own GOFLAGS stays out: under the gate it is -count=1,
	// which would make the control vacuous.
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	root := fixture(t)
	write(t, root, "go.mod", "module example.com/probe\n\ngo 1.21\n")
	write(t, root, "inner/inner_test.go", "package inner\n\nimport \"testing\"\n\nfunc TestInner(t *testing.T) {}\n")
	write(t, root, "cmd/probe/main.go", "package main\n\nimport _ \"embed\"\n\n// stamp changes before each run, so the package compiles and vets again.\n//\n//go:embed stamp.txt\nvar stamp string\n\nfunc main() { _ = stamp }\n")
	// Each form's inner go test appends its result to runs/NAME.log.
	inner := func(name string) string {
		return fmt.Sprintf(`(cd %q && go test ./inner) >> %q 2>&1; exec "$0" "$@"`, root, filepath.Join(root, "runs", name+".log"))
	}
	vettool := filepath.Join(root, "runs", "vettool.sh")
	// The vet tool asks the real go for its tool directory: TestMain's
	// stand-in answers go env for fingerprinting only.
	realGo := realTools["go"]
	if realGo == "" {
		t.Skip("no go on PATH")
	}
	write(t, root, "runs/vettool.sh", fmt.Sprintf("#!/bin/sh\n(cd %q && go test ./inner) >> %q 2>&1\nexec \"$(%q env GOTOOLDIR)/vet\" \"$@\"\n", root, filepath.Join(root, "runs", "vettool.log"), realGo))
	if err := os.Chmod(vettool, 0o755); err != nil {
		t.Fatal(err)
	}
	forms := map[string][]string{
		"run-exec":       {"go", "run", "-exec=sh -c '" + inner("run-exec") + "'", "./cmd/probe"},
		"test-exec":      {"go", "test", "-count=1", "-exec", "sh -c '" + inner("test-exec") + "'", "./inner"},
		"build-toolexec": {"go", "build", "-toolexec=sh -c '" + inner("build-toolexec") + "'", "-o", filepath.Join(root, "runs", "probe.bin"), "./cmd/probe"},
		"vet-toolexec":   {"go", "vet", "-toolexec=sh -c '" + inner("vet-toolexec") + "'", "./cmd/probe"},
		"vettool":        {"go", "vet", "-vettool=" + vettool, "./cmd/probe"},
	}
	run := func(hermetic bool) map[string][2]int {
		for name := range forms {
			os.Remove(filepath.Join(root, "runs", name+".log"))
		}
		var steps []*Step
		for name, argv := range forms {
			steps = append(steps, &Step{Name: name, Argv: argv, Hermetic: hermetic})
		}
		results := map[string][2]int{}
		for round := range 2 {
			write(t, root, "cmd/probe/stamp.txt", fmt.Sprintf("%t %d %d\n", hermetic, round, time.Now().UnixNano()))
			if code, out := runGate(t, root, t.TempDir(), steps); code != 0 {
				t.Fatalf("hermetic %t, run %d: exit %d\n%s", hermetic, round+1, code, out)
			}
			for name := range forms {
				log, err := os.ReadFile(filepath.Join(root, "runs", name+".log"))
				if err != nil {
					t.Fatalf("hermetic %t, run %d: %s started no inner go test: %v", hermetic, round+1, name, err)
				}
				lines := innerResult.FindAllString(string(log), -1)
				cached := 0
				for _, line := range lines {
					if strings.Contains(line, "(cached)") {
						cached++
					}
				}
				results[name] = [2]int{len(lines), cached}
				os.Remove(filepath.Join(root, "runs", name+".log"))
			}
		}
		// results holds the second run's counts.
		return results
	}
	for name, counts := range run(false) {
		t.Logf("default: %s: second run: %d inner go test results, %d (cached)", name, counts[0], counts[1])
		if counts[0] == 0 || counts[1] != 0 {
			t.Errorf("%s with the uncached default: the second run's inner go test gave %d results, %d (cached); want results and none cached", name, counts[0], counts[1])
		}
	}
	for name, counts := range run(true) {
		t.Logf("control (hermetic exception): %s: second run: %d inner go test results, %d (cached)", name, counts[0], counts[1])
		if counts[1] == 0 {
			t.Errorf("%s as the hermetic exception: the second run's inner go test gave %d results, none (cached); the control is vacuous", name, counts[0])
		}
	}
}
