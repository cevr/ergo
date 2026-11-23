package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/sketchfront"
)

// A program of packages (196b) is a folder with ergo.mod: the runner
// formats every .ergo file of its packages, builds cmd/NAME (a folder) and
// each other binary cmd/OTHER, runs one ergo test and one ergo check over
// every package folder (TEST-one), reports each package's leg, and runs its
// cases.
func TestModuleProgram(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/cmd/walkd/main.ergo", "#!/bin/sh\necho served\n")
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
		filepath.Join(root, "cmd/walkd/main.ergo"), filepath.Join(root, "cmd/walker/main.ergo"), filepath.Join(root, "frame/frame.ergo"),
		filepath.Join(root, "walk/deep/more.ergo"), filepath.Join(root, "walk/game.ergo"), filepath.Join(root, "walk/tests.ergo"),
	}, " ")
	for _, want := range []string{wantFmt, "test " + filepath.Join(root, "..."), "check " + filepath.Join(root, "...")} {
		if !slices.Contains(calls, want) {
			t.Errorf("no call %q in\n%q", want, calls)
		}
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "check ") && call != "check "+filepath.Join(root, "...") {
			t.Errorf("a package checked alone after one check of the module: %q", call)
		}
	}
	for _, binary := range []string{"walker", "walkd"} {
		build := false
		for _, call := range calls {
			build = build || strings.HasPrefix(call, "build "+filepath.Join(root, "cmd", binary)+" -o ")
		}
		if !build {
			t.Errorf("no build of cmd/%s in\n%q", binary, calls)
		}
	}
}

// TestModuleProgramLoadsItsPackagesOnce: the runner reads a program of
// packages (examples/calc, an example) with one load of its package
// folders, and gets the same program as with each folder checked alone:
// sources, targets, main and no problem. A growth guard on the loads
// sketchfront counts (gate-speed GS31a). It catches gs31a-folders-unshared,
// which reads every folder alone again; not parallel, so the count is
// this load's.
func TestModuleProgramLoadsItsPackagesOnce(t *testing.T) {
	dir, err := filepath.Abs("../../examples/calc")
	if err != nil {
		t.Fatal(err)
	}
	load := func() (*program, []string, int64) {
		t.Helper()
		before := sketchfront.WorkDone().Loads
		p, problems := loadProgram(dir, "calc", false)
		return p, problems, sketchfront.WorkDone().Loads - before
	}
	shared, sharedProblems, sharedLoads := load()
	prior := loadPackageFolders
	loadPackageFolders = func(string, []os.DirEntry) map[string]sketchfront.Analysis { return nil }
	alone, aloneProblems, aloneLoads := load()
	loadPackageFolders = prior
	if shared == nil || alone == nil || len(sharedProblems)+len(aloneProblems) > 0 {
		t.Fatalf("calc does not load: %q / %q", sharedProblems, aloneProblems)
	}
	if !slices.Equal(shared.sources, alone.sources) || !slices.Equal(shared.tests, alone.tests) || shared.main != alone.main {
		t.Errorf("one load reads another program than a check of each folder:\n%v %v %s\n%v %v %s", shared.sources, shared.tests, shared.main, alone.sources, alone.tests, alone.main)
	}
	if sharedLoads != 1 {
		t.Errorf("calc's load read its packages %d times (each folder alone: %d); want 1", sharedLoads, aloneLoads)
	}
}

// One ergo test of a program of packages still judges each package's part
// of the report: its check-time verdicts against its native ones
// (TEST-one). It kills one-module-ignores-evaluation.
func TestModuleProgramJudgesEachPackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/walk.ergo", "// CHECKJSON {\"checked\":true,\"evaluation\":{\"schemaVersion\":1,\"status\":\"passed\",\"obligations\":[{\"name\":\"Fixture\",\"status\":\"passed\",\"evidence\":\"evaluated\",\"cases\":1}]}}\n// NATIVE ok Different\nfunc w() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	code, out := f.run()
	for _, want := range []string{"FAIL walker: check walk: Fixture: "} {
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("exit %d, want a failure with %q\n%s", code, want, out)
		}
	}
}

// A program of packages may hold a workload file (scripts/opcount's sizes
// and arguments): the runner admits one that reads and names the line of
// one that does not, as a layout error. internal/opcount's property covers
// the kinds of bad line; this test covers that the runner reads the file.
// A workload package holds the file as workload/workload, and one without
// it is a layout error, so opcount counts every program with a workload.
// Mutants: examples-workload-unexpected, examples-workload-unchecked,
// examples-workload-package-unchecked.
func TestModuleProgramWorkload(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		at, workload string
		code         int
		want         string
	}{
		{"workload", "unit op\nsizes 10 20\nargs bench {n}\n", 0, "ok walker/basic ("},
		{"workload", "unit op\nsizes 20 10\nargs bench {n}\n", 2, "examples/walker: workload: line 2: sizes 20 10"},
		{"workload/workload", "unit op\nsizes 10 20\nargs bench {n}\n", 0, "ok walker/basic ("},
		{"workload/workload.ergo", "func Doc(n int) string { \"\" }\n", 2, "examples/walker: workload/workload: a workload package holds its workload file"},
	} {
		f := newFixture(t)
		dir := "examples/walker"
		f.write(dir+"/ergo.mod", "module walker\n")
		f.write(dir+"/README.md", "# walker\n")
		f.write(dir+"/tags", "default\n")
		f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
		f.write(dir+"/cases/basic/expected.out", "hi\n")
		f.write(dir+"/"+c.at, c.workload)
		if c.at == "workload/workload" {
			f.write(dir+"/workload/workload.ergo", "func Doc(n int) string { \"\" }\n")
		}
		code, out := f.run()
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("workload %q: exit %d, want %d and %q\n%s", c.workload, code, c.code, c.want, out)
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

// A program of packages with generated files runs `ergo gen KIND --check`
// once per contract, named by the files' contract line, and a stale file
// fails the program before its build.
func TestModuleProgramGenCheck(t *testing.T) {
	t.Parallel()
	for _, stale := range []bool{false, true} {
		f := newFixture(t)
		dir := "examples/walker"
		f.write(dir+"/ergo.mod", "module walker\n")
		f.write(dir+"/README.md", "# walker\n")
		f.write(dir+"/tags", "default\n")
		f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
		f.write(dir+"/cases/basic/expected.out", "hi\n")
		f.write(dir+"/walk/walk.ergo", "type Walk {}\n")
		f.write(dir+"/walk/walkrpc/"+genmark.Name, genmark.Marker{Kind: "rpc", Generator: "ergo gen rpc", Subjects: []string{"walker/walk.Walk"}, Digest: "sha256:00"}.Text())
		f.write(dir+"/walk/walkrpc/wire.ergo", "type Request {}\n")
		f.write(dir+"/walk/walkrpc/client.ergo", "type Client {}\n")
		if stale {
			f.write(dir+"/walk/walkrpc/server.ergo", "type Server {}\n// GENSTALE\n")
		}
		code, out := f.run()
		calls := readCalls(t, f)
		want := "gen rpc --check " + filepath.Join(f.root, dir, "walk") + ".Walk"
		gens := 0
		for _, call := range calls {
			if strings.HasPrefix(call, "gen ") {
				gens++
				if call != want {
					t.Errorf("stale %v: call %q, want %q", stale, call, want)
				}
			}
		}
		if gens != 1 {
			t.Errorf("stale %v: %d gen calls, want 1 in\n%q", stale, gens, calls)
		}
		if failed := code != 0 && strings.Contains(out, "FAIL walker: gen rpc walk.Walk"); failed != stale {
			t.Errorf("stale %v: exit %d\n%s", stale, code, out)
		}
	}
}

// A program that installs a local module by its go.mod
// (`replace M => ../../vendor/lib`; a module of modules/ runs as a program
// of its own) runs the module's package tests in its test leg, since ergo
// test of the program never runs a dependency's: so the module's laws and
// tests are the examples suite's too. A module
// version, a replacement with no ergo.mod and a nested module are not
// tested. Mutant: xmd-local-modules-untested (testdata/mutants.json).
func TestModuleProgramTestsItsLocalModule(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/go.mod", "module walker\n\ngo 1.27\n\nrequire (\n\tex.dev/lib v0.0.0\n\tex.dev/gone v0.0.0\n\tex.dev/far v1.0.0\n)\n\nreplace ex.dev/lib => ../../vendor/lib // the repository's\n\nreplace (\n\tex.dev/gone => ../../vendor/gone\n\tex.dev/far => ex.dev/other v1.0.0\n)\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/game.ergo", "func f() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	f.write("vendor/lib/ergo.mod", "module ex.dev/lib\n")
	f.write("vendor/lib/lib.ergo", "test Lib {\n}\n")
	f.write("vendor/lib/deep/deep.ergo", "test Deep {\n}\n")
	f.write("vendor/lib/inner/ergo.mod", "module ex.dev/inner\n")
	f.write("vendor/lib/inner/inner.ergo", "test Inner {\n}\n")
	f.write("vendor/gone/gone.ergo", "test Gone {\n}\n")
	code, out := f.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	calls := readCalls(t, f)
	lib := filepath.Join(f.root, "vendor/lib")
	for _, want := range []string{"test " + lib, "test " + filepath.Join(lib, "deep")} {
		if !slices.Contains(calls, want) {
			t.Errorf("no call %q in\n%q", want, calls)
		}
	}
	for _, not := range []string{"test " + filepath.Join(lib, "inner"), "test " + filepath.Join(f.root, "vendor/gone")} {
		if slices.Contains(calls, not) {
			t.Errorf("call %q in\n%q", not, calls)
		}
	}
}

// A package folder named workload is a package, not opcount's workload
// file: the bug was examples/vt's workload package read as a workload
// file that is not a regular file (SL1, merging opcount with DT1).
func TestModuleProgramWorkloadPackage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	f.write(dir+"/workload/load.ergo", "func f() {}\n")
	// A workload package holds its workload file (TOK-BISECT), which
	// this fixture of SL1's bug predates (batch 23 fallout).
	f.write(dir+"/workload/workload", "unit op\nsizes 10 20\nargs bench {n}\n")
	if code, out := f.run(); code != 0 || !strings.Contains(out, "ok walker: test workload") {
		t.Errorf("a workload package folder: exit %d\n%s", code, out)
	}
}

// The layout of a program of packages: its binary is cmd/NAME/main.ergo,
// cmd holds only binaries' folders, each with its main.ergo, and a package
// folder holds .ergo files and package folders only.
func TestModuleProgramLayout(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"walk/a.ergo": "func f() {}\n"}, "a program of packages has no cmd/walker/main.ergo"},
		{map[string]string{"cmd/other/main.ergo": "#!/bin/sh\n"}, "cmd holds the program's binary, cmd/walker"},
		{map[string]string{"cmd/walker/main.ergo": "#!/bin/sh\n", "cmd/other/lib.ergo": "#!/bin/sh\n"}, "no cmd/other/main.ergo"},
		{map[string]string{"cmd/walker/main.ergo": "#!/bin/sh\n", "cmd/notes.txt": "x\n"}, "cmd/notes.txt is not a folder"},
		{map[string]string{"cmd/walker/lib.ergo": "#!/bin/sh\n"}, "no cmd/walker/main.ergo"},
		{map[string]string{"cmd/walker/main.ergo": "#!/bin/sh\n", "cmd/serve/lib.ergo": "#!/bin/sh\n"}, "no cmd/serve/main.ergo"},
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
