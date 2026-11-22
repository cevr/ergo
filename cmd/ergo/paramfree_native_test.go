package main

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower/paramfreegen"
)

// TestParamFreeArmsNative is the IR build's leg of the param-free arm
// property (paramfreegen; GEN9-GAP42 D): the admitted seeds of 1 to 60,
// in one file, run by `ergo test --native`, the build a user's tests run
// (the IR bodies through the pinned toolchain); every test, which asserts
// the model's journal for the generic pick and its concrete twin pickRes,
// passes. internal/sketch/lower's TestParamFreeArmsVerdict and
// TestParamFreeArmsRun are the checker's, the text lowering's and the
// evaluator's legs.
//
// It builds Go because its subject is the release the IR emits on a
// dropped arm: no in-process check runs it.
//
// Mutant it catches (check mutants.json): gap42-concrete-release-skipped,
// with TestParamFreeArmsRun.
func TestParamFreeArmsNative(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a Go program (full tier)")
	}
	var admitted []*paramfreegen.Seed
	var names []string
	for n := 1; n <= 60; n++ {
		if s := paramfreegen.Generate(n); s.Admitted {
			admitted = append(admitted, s)
			names = append(names, s.TestNames...)
		}
	}
	binary := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := testXDG(t, t.TempDir())
	tmp := t.TempDir()
	src := paramfreegen.Source(admitted, true)
	file := smokeWrite(t, root, "paramfree.ergo", src+"\nfunc main() {}\n")
	stdout, stderr, code := testRunCLI(t, binary, root, xdg, tmp, "test", file, "--native")
	for _, name := range names {
		if !strings.Contains(stdout, "ok "+name+"\n") {
			t.Errorf("test %s did not pass in the IR build (exit %d)", name, code)
		}
	}
	if t.Failed() {
		t.Logf("stdout:\n%s\nstderr:\n%s\nsource:\n%s", stdout, stderr, src)
	}
}
