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
// steps on a small module: a go run -exec, a go test -exec, a go build
// -toolexec, a go vet -toolexec and a go vet -vettool, each a program that
// starts go test ./inner with no count of its own. A static reader of argv
// does not see that go test; the gate's default does reach it. The gate
// expires every earlier result before its first step (testexpire.go), so
// this test checks what the default adds within one run: each form runs
// twice in one gate, the second copy after every first one. With the
// default (an ordinary step), no inner go test of a second copy prints
// (cached). As the control, the same steps run as the hermetic exception,
// without the default: every inner go test of a second copy is (cached), so
// the inner go test was cacheable and the default is what stops the replay.
func TestProgramsStartUncachedGoTests(t *testing.T) {
	// The caller's own GOFLAGS stays out: under the gate it is -count=1,
	// which would make the control vacuous.
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOWORK", "off")
	realGo := realTools["go"]
	if realGo == "" {
		t.Skip("no go on PATH")
	}
	root := fixture(t)
	write(t, root, "go.mod", "module example.com/probe\n\ngo 1.21\n")
	write(t, root, "inner/inner_test.go", "package inner\n\nimport \"testing\"\n\nfunc TestInner(t *testing.T) {}\n")
	// Each copy N has its own program cmd/probeN, whose stamp changes
	// before each run, so the package compiles and vets again, and its own
	// log runs/NAMEN.log, where each inner go test appends its result.
	forms := func(copy string) map[string][]string {
		write(t, root, "cmd/probe"+copy+"/main.go", "package main\n\nimport _ \"embed\"\n\n//go:embed stamp.txt\nvar stamp string\n\nfunc main() { _ = stamp }\n")
		write(t, root, "cmd/probe"+copy+"/stamp.txt", fmt.Sprintf("%s %d\n", copy, time.Now().UnixNano()))
		log := func(name string) string { return filepath.Join(root, "runs", name+copy+".log") }
		inner := func(name string) string {
			return fmt.Sprintf(`(cd %q && go test ./inner) >> %q 2>&1; exec "$0" "$@"`, root, log(name))
		}
		vettool := filepath.Join(root, "runs", "vettool"+copy+".sh")
		write(t, root, "runs/vettool"+copy+".sh", fmt.Sprintf("#!/bin/sh\n(cd %q && go test ./inner) >> %q 2>&1\nexec \"$(%q env GOTOOLDIR)/vet\" \"$@\"\n", root, log("vettool"), realGo))
		if err := os.Chmod(vettool, 0o755); err != nil {
			t.Fatal(err)
		}
		probe := "./cmd/probe" + copy
		return map[string][]string{
			"run-exec":       {"go", "run", "-exec=sh -c '" + inner("run-exec") + "'", probe},
			"test-exec":      {"go", "test", "-count=1", "-exec", "sh -c '" + inner("test-exec") + "'", "./inner"},
			"build-toolexec": {"go", "build", "-toolexec=sh -c '" + inner("build-toolexec") + "'", "-o", filepath.Join(root, "runs", "probe"+copy+".bin"), probe},
			"vet-toolexec":   {"go", "vet", "-toolexec=sh -c '" + inner("vet-toolexec") + "'", probe},
			"vettool":        {"go", "vet", "-vettool=" + vettool, probe},
		}
	}
	run := func(hermetic bool) map[string][2]int {
		os.RemoveAll(filepath.Join(root, "runs"))
		var steps, firsts []*Step
		for name, argv := range forms("1") {
			first := &Step{Name: name + "1", Argv: argv, Hermetic: hermetic}
			firsts = append(firsts, first)
			steps = append(steps, first)
		}
		var needs []string
		for _, first := range firsts {
			needs = append(needs, first.Name)
		}
		for name, argv := range forms("2") {
			steps = append(steps, &Step{Name: name + "2", Needs: needs, Argv: argv, Hermetic: hermetic})
		}
		if code, out := runGate(t, root, t.TempDir(), steps); code != 0 {
			t.Fatalf("hermetic %t: exit %d\n%s", hermetic, code, out)
		}
		results := map[string][2]int{}
		for name := range forms("1") {
			log, err := os.ReadFile(filepath.Join(root, "runs", name+"2.log"))
			if err != nil {
				t.Fatalf("hermetic %t: %s2 started no inner go test: %v", hermetic, name, err)
			}
			lines := innerResult.FindAllString(string(log), -1)
			cached := 0
			for _, line := range lines {
				if strings.Contains(line, "(cached)") {
					cached++
				}
			}
			results[name] = [2]int{len(lines), cached}
		}
		// results holds the second copies' counts.
		return results
	}
	for name, counts := range run(false) {
		t.Logf("default: %s, second copy: %d inner go test results, %d (cached)", name, counts[0], counts[1])
		if counts[0] == 0 || counts[1] != 0 {
			t.Errorf("%s with the uncached default: the second copy's inner go test gave %d results, %d (cached); want results and none cached", name, counts[0], counts[1])
		}
	}
	for name, counts := range run(true) {
		t.Logf("control (hermetic exception): %s, second copy: %d inner go test results, %d (cached)", name, counts[0], counts[1])
		if counts[0] == 0 || counts[1] != counts[0] {
			t.Errorf("%s as the hermetic exception: the second copy's inner go test gave %d results, %d (cached); want every one cached, or the control is vacuous", name, counts[0], counts[1])
		}
	}
}
