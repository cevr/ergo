package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A program of packages (196b) is a folder with ergo.mod: the runner
// formats every .ergo file of its packages, builds cmd/NAME (a folder), runs
// ergo test on each package folder that declares a test, and runs its cases.
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
	for _, want := range []string{wantFmt, "test " + filepath.Join(root, "walk"), "test " + filepath.Join(root, "walk/deep")} {
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

// A program that installs a module of the repository by its go.mod
// (`replace M => ../../modules/lib`) runs the module's package tests in
// its test leg, since ergo test of the program never runs a dependency's:
// so the module's laws and tests are the examples suite's too. A module
// version, a replacement with no ergo.mod and a nested module are not
// tested. Mutant: xmd-local-modules-untested (testdata/mutants.json).
func TestModuleProgramTestsItsLocalModule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/go.mod", "module walker\n\ngo 1.27\n\nrequire (\n\tex.dev/lib v0.0.0\n\tex.dev/gone v0.0.0\n\tex.dev/far v1.0.0\n)\n\nreplace ex.dev/lib => ../../modules/lib // the repository's\n\nreplace (\n\tex.dev/gone => ../../modules/gone\n\tex.dev/far => ex.dev/other v1.0.0\n)\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/game.ergo", "func f() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	f.write("modules/lib/ergo.mod", "module ex.dev/lib\n")
	f.write("modules/lib/lib.ergo", "test Lib {\n}\n")
	f.write("modules/lib/deep/deep.ergo", "test Deep {\n}\n")
	f.write("modules/lib/inner/ergo.mod", "module ex.dev/inner\n")
	f.write("modules/lib/inner/inner.ergo", "test Inner {\n}\n")
	f.write("modules/gone/gone.ergo", "test Gone {\n}\n")
	code, out := f.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	calls := readCalls(t, f)
	lib := filepath.Join(f.root, "modules/lib")
	for _, want := range []string{"test " + lib, "test " + filepath.Join(lib, "deep")} {
		if !slices.Contains(calls, want) {
			t.Errorf("no call %q in\n%q", want, calls)
		}
	}
	for _, not := range []string{"test " + filepath.Join(lib, "inner"), "test " + filepath.Join(f.root, "modules/gone")} {
		if slices.Contains(calls, not) {
			t.Errorf("call %q in\n%q", not, calls)
		}
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
