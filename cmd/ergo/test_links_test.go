package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestWarmModuleTestLinksNothing is fu-test-bins' counted receipt: `ergo
// test ./...` over a module of packages links each package's test binary
// once, and a warm run of the unchanged module runs no link action. It
// kills test-bins-temp-link: a test binary linked to a fresh temporary
// path is linked again on every run.
// It builds Go because the claim is about the go command's link actions,
// which only the -x trace of a real build shows.
func TestWarmModuleTestLinksNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("the link count of real test builds runs in the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module links\n")
	const packages = 3
	for i := range packages {
		smokeWrite(t, root, fmt.Sprintf("p%d/p.ergo", i), fmt.Sprintf("test Same%d { assert %d == %d }\n", i, i, i))
	}
	links := func(run string) int {
		t.Helper()
		command := exec.Command(bin, "test", "./...")
		command.Dir = root
		command.Env = append(os.Environ(), "GOFLAGS=-x")
		out, stderr, code := runSmokeCommand(t, command)
		if code != 0 || strings.Count(string(out), "PASS (1 tests)") != packages {
			t.Fatalf("%s run: exit %d\n%s\n%s", run, code, out, firstLines(stderr))
		}
		return strings.Count(string(stderr), "/link -o ")
	}
	if cold := links("cold"); cold != packages {
		t.Fatalf("the cold run ran %d link actions; want one per package, %d", cold, packages)
	}
	if warm := links("warm"); warm != 0 {
		t.Fatalf("the warm run of an unchanged module ran %d link actions; want none", warm)
	}
}
