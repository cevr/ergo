package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A program of packages (196b) is a folder with ergo.mod: the runner
// formats every .ergo file of its packages, builds cmd/NAME (a folder), runs
// one ergo test over every package folder (TEST-one), reports each package's
// leg, and runs its cases.
func TestModuleProgram(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/game.ergo", "func f() {}\n")
	f.write(dir+"/walk/tests.ergo", "test Walks {\n}\n")
	f.write(dir+"/walk/deep/more.ergo", "test Deep {\n}\n")
	f.write(dir+"/frame/frame.ergo", "func g() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	code, out := f.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"ok walker: test walk (", "ok walker: test walk/deep (", "ok walker/basic ("} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q\n%s", want, out)
		}
	}
	root := filepath.Join(f.root, dir)
	calls := readCalls(t, f)
	wantFmt := "fmt --check " + strings.Join([]string{
		filepath.Join(root, "cmd/walker/main.ergo"), filepath.Join(root, "frame/frame.ergo"),
		filepath.Join(root, "walk/deep/more.ergo"), filepath.Join(root, "walk/game.ergo"), filepath.Join(root, "walk/tests.ergo"),
	}, " ")
	for _, want := range []string{wantFmt, "test " + filepath.Join(root, "...")} {
		if !slices.Contains(calls, want) {
			t.Errorf("no call %q in\n%q", want, calls)
		}
	}
	build := false
	for _, call := range calls {
		build = build || strings.HasPrefix(call, "build "+filepath.Join(root, "cmd", "walker")+" -o ")
	}
	if !build {
		t.Errorf("no build of cmd/walker in\n%q", calls)
	}
}

// One ergo test of a program of packages still judges each package's part
// of the report: its check-time verdicts against its native ones, and its
// skip lines against expected-skips (TEST-one). It kills one-module-
// ignores-evaluation and one-module-ignores-skips.
func TestModuleProgramJudgesEachPackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/walk.ergo", "// CHECKJSON {\"checked\":true,\"evaluation\":{\"schemaVersion\":1,\"status\":\"passed\",\"obligations\":[{\"name\":\"Fixture\",\"status\":\"passed\",\"evidence\":\"evaluated\",\"cases\":1}]}}\n// NATIVE ok Different\nfunc w() {}\n")
	f.write(dir+"/keep/keep.ergo", "// TESTSKIP\nfunc k() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	code, out := f.run()
	for _, want := range []string{"FAIL walker: check walk: Fixture: ", "FAIL walker: test keep: 1 unexpected skips"} {
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, want a failure with %q\n%s", code, want, out)
		}
	}
}

// One ergo test of a program of packages that did not test every package
// of the program fails it: a package with no evaluated obligation has an
// empty part of the report either way, so only the package list shows
// that it never ran. It kills one-module-any-packages.
func TestModuleProgramRunsEveryPackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/walk.ergo", "func w() {}\n")
	f.write(dir+"/hidden/hidden.ergo", "// MODULEHIDDEN\n// CHECKJSON {\"checked\":true,\"evaluation\":{\"schemaVersion\":1,\"status\":\"none\",\"obligations\":[]}}\nfunc h() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	code, out := f.run()
	want := "FAIL walker: test: ergo test ran the packages walker/cmd/walker walker/walk; the program's packages are walker/cmd/walker walker/hidden walker/walk"
	if code == 0 || !strings.Contains(out, want) {
		t.Errorf("exit %d, want a failure with %q\n%s", code, want, out)
	}
}

// The layout of a program of packages: its binary is cmd/NAME/main.ergo,
// cmd holds nothing else, and a package folder holds .ergo files and
// package folders only.
func TestModuleProgramLayout(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"walk/a.ergo": "func f() {}\n"}, "a program of packages has no cmd/walker/main.ergo"},
		{map[string]string{"cmd/other/main.ergo": "#!/bin/sh\n"}, "cmd holds one folder, cmd/walker, the program's binary"},
		{map[string]string{"cmd/walker/lib.ergo": "#!/bin/sh\n"}, "no cmd/walker/main.ergo"},
		{map[string]string{"cmd/walker/main.ergo": "#!/bin/sh\n", "walk/notes.txt": "x\n"}, "unexpected walk/notes.txt; a package folder holds .ergo files and package folders"},
		{map[string]string{"cmd/walker/main.ergo": "#!/bin/sh\n", "stray.txt": "x\n"}, "unexpected stray.txt; a program of packages holds"},
	} {
		f := newFixture(t)
		dir := "examples/walker"
		f.write(dir+"/ergo.mod", "module walker\n")
		f.write(dir+"/README.md", "# walker\n")
		f.write(dir+"/tags", "default\n")
		f.write(dir+"/cases/basic/expected.out", "hi\n")
		for name, text := range c.files {
			f.write(dir+"/"+name, text)
		}
		code, out := f.run()
		if code != 2 || !strings.Contains(out, "examples/walker: "+c.want) {
			t.Errorf("%v: exit %d, want 2 and %q\n%s", c.files, code, c.want, out)
		}
	}
}
