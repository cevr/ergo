package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tsProgram writes a program of packages tagged ts, with a ts/ folder.
func (f *fixture) tsProgram(name string) {
	dir := "examples/" + name
	f.write(dir+"/ergo.mod", "module "+name+"\n")
	f.write(dir+"/README.md", "# "+name+"\n")
	f.write(dir+"/tags", "ts\n")
	f.write(dir+"/cmd/"+name+"/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/ts/main.ts", "console.log(\"hi\")\n")
	f.write(dir+"/cases/basic/args", "ts/main.ts\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
}

// withoutBun is PATH with every folder that holds a bun removed.
func withoutBun() string {
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "bun")); err != nil {
			kept = append(kept, dir)
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// A ts program on a host without Bun fails, naming what it needs; it is
// never a pass with its cases unrun. Mutant ts-skip (the ts error branch
// of runProgram returns without r.fail) exits 0 here; the limiter
// example under -tags ts kills the mutants of the install and the copy.
// Not parallel: it sets PATH.
func TestTsLegWithoutBunFails(t *testing.T) {
	t.Setenv("PATH", withoutBun())
	f := newFixture(t)
	f.tsProgram("bridge")
	code, out := f.run("-tags", "ts")
	if code != 1 || !strings.Contains(out, "FAIL bridge: ts: bun is not on PATH") || strings.Contains(out, "bridge/basic") {
		t.Fatalf("want exit 1 and the ts failure with no case run; exit %d\n%s", code, out)
	}
}

// The ts/ folder and the tag ts go together, and ts/ holds no .ergo file.
// Mutant ts-layout (finishProgram drops the check) lets a ts/ folder hide
// in a default program, which the default gate runs without its leg.
func TestTsLayout(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		edit func(f *fixture)
	}{
		{"untagged", func(f *fixture) { f.write("examples/untagged/tags", "default\n") }},
		{"nofolder", func(f *fixture) {
			if err := os.RemoveAll(filepath.Join(f.root, "examples/nofolder/ts")); err != nil {
				t.Fatal(err)
			}
		}},
		{"stray", func(f *fixture) { f.write("examples/stray/ts/x.ergo", "func f() {}\n") }},
	} {
		f := newFixture(t)
		f.tsProgram(c.name)
		c.edit(f)
		code, out := f.run("-tags", "ts")
		if code != 2 || !strings.Contains(out, "examples/"+c.name) {
			t.Errorf("%s: want exit 2 naming examples/%s; exit %d\n%s", c.name, c.name, code, out)
		}
	}
}
