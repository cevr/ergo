package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The mod leg (MD2): a program tagged mod whose module cache does not hold
// a module its go.mod requires fails, before its build, and is never
// skipped; one whose go.mod requires nothing passes the leg. It runs the go
// command (go mod verify), whose verdict on the cache is the claim; it
// builds nothing. Not parallel: the module cache is GOMODCACHE.
//
// Mutants it alone catches: a cold cache skipped (md2-mod-cold-skipped)
// and the leg never run (md2-mod-leg-unrun).
func TestModLegColdCacheFails(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatal("the mod leg runs go mod verify; no go on PATH")
	}
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOTOOLCHAIN", "local")
	f := newFixture(t)
	for name, gomod := range map[string]string{
		"cold":  "module cold\n\ngo 1.21\n\nrequire example.com/counter v1.0.0\n",
		"empty": "module empty\n\ngo 1.21\n",
	} {
		dir := "examples/" + name
		f.write(dir+"/ergo.mod", "module "+name+"\n")
		f.write(dir+"/go.mod", gomod)
		f.write(dir+"/README.md", "# "+name+"\n")
		f.write(dir+"/tags", "mod\n")
		f.write(dir+"/cmd/"+name+"/main.ergo", "#!/bin/sh\necho hi\n")
		f.write(dir+"/cases/basic/expected.out", "hi\n")
	}
	code, out := f.run("-tags", "mod")
	oks, fails := verdicts(out)
	if code == 0 || !strings.HasPrefix(fails["cold"], " mod: ") || !oks["empty/basic"] {
		t.Fatalf("exit %d; want FAIL cold: mod and ok empty/basic\n%s", code, out)
	}
	for _, call := range readCalls(t, f) {
		if strings.Contains(call, "examples/cold/") {
			t.Errorf("the compiler ran for a program whose cache is cold: %q", call)
		}
	}
}
