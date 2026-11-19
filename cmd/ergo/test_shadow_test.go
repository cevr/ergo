package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestWritesShadowRefutation is ergo test's write of a rewrite
// shadow's refutation (LF6d), end to end. DecEnc is false only at "zq-17",
// which test G reaches through F's site: the run fails with the shadow's
// defect, and ergo test writes `refuted by shadow` into ergo.laws and the
// case into ergo.cases. It builds Go because the refutation reaches ergo
// test as the test binary's own standard error: the in-process tests
// cover the line's reader (TestShadowLinesReadAcrossWrites) and the
// ledger writer (sketchfront's TestShadowRefutationJoinsTheLedger), and
// only a real test binary shows the two are wired together.
//
// The program is a module's binary, whose ledger names its package by its
// folder's import path while the test build's shadow names it main (the
// bug this test found: the writer took the law for another module's).
//
// Mutants: lf6d-shadow-lines-unwired (the test binary's standard error does
// not reach the reader, so the refutation is never written),
// lf6d-refutation-main-unnamed (the writer does not know a binary's law by
// the shadow's name for it).
func TestTestWritesShadowRefutation(t *testing.T) {
	needsBuild(t)
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module shadowref\n")
	smokeWrite(t, root, "main.ergo", "func Enc(s string) string { s + \"!\" }\n\nfunc Dec(s string) string {\n\tif s == \"zq-17!\" {\n\t\treturn \"other\"\n\t}\n\ts[:len(s) - 1]\n}\n\nlaw DecEnc(s string) { Dec(Enc(s)) } == { s }\n\nfunc F(t string) string { Dec(Enc(t)) }\n\ntest G {\n\tassert F(\"zq-17\") == \"zq-17\"\n}\n\nfunc main() needs Console {\n\tConsole.Println(F(\"hello\"))\n}\n")
	command := exec.Command(bin, "test", ".")
	command.Dir = root
	command.Env = append(os.Environ(), "GOFLAGS=")
	out, stderr, code := runSmokeCommand(t, command)
	if code == 0 || !strings.Contains(string(stderr), `for s = "zq-17"`) {
		t.Fatalf("ergo test: exit %d\n%s\n%s\nwant a failure with the shadow's defect", code, out, stderr)
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	if l := read("ergo.laws"); !strings.Contains(l, ".DecEnc refuted by shadow h1:") {
		t.Errorf("ergo.laws %q; want DecEnc refuted by shadow\nstderr:\n%s", l, stderr)
	}
	if c := read("ergo.cases"); !strings.Contains(c, `.DecEnc ("zq-17")`) {
		t.Errorf("ergo.cases %q; want the shadow's case", c)
	}
}
