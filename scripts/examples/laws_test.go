package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lawsIn writes source as the one file x.ergo of a package folder and
// returns lawlessPackage's types ("LINE NAME") and problems.
func lawsIn(t *testing.T, source string) ([]string, []string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.ergo"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	found, problems := lawlessPackage(dir, dir)
	var got []string
	for _, l := range found {
		if l.file != "x.ergo" {
			t.Errorf("%q: file %q, want x.ergo", source, l.file)
		}
		got = append(got, fmt.Sprintf("%d %s", l.line, l.name))
	}
	return got, problems
}

// lawlessPackage finds the operation types with no member law, as the
// checker models them, and nothing else: records (one with a function
// field too), unions, machines, types with a law, and words inside
// comments and strings are not operation types.
func TestLawlessTypes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		source string
		want   []string // "LINE NAME"
	}{
		{"operation, no law", "// x\ntype Library {\n\tReplays() []string,\n}\n", []string{"2 Library"}},
		{"operation and law", "type Library {\n\tReplays() []string,\n\tlaw Named() { len(Replays()) >= 0 },\n}\n", nil},
		{"law body over lines", "type Store {\n\tGet(k string) int,\n\n\tlaw L(k string) {\n\t\tlet v = Get(k)\n\t\tv == v\n\t},\n}\n", nil},
		{"one line", "type Bad {}\ntype Codec { Encode(v int) string, Decode(s string) int fails Bad }\n", []string{"2 Codec"}},
		{"generic operation type", "type Box[K comparable, V] {\n\tGet(k K) V,\n}\n", []string{"1 Box"}},
		{"record", "type Replay { Name string, Moves string }\ntype F {\n\tRun func(int) bool,\n\tAt int,\n}\n", nil},
		{"empty record", "type Fixed {}\n", nil},
		{"union", "type North {}\ntype East {}\ntype Move = North | East\n", nil},
		{"law in a comment", "type Library {\n\tReplays() []string, // law Named() { true }\n\t/* law X() { true } */\n}\n", []string{"1 Library"}},
		{"type in a raw string", "let doc = `\ntype Fake {\n\tGo(),\n}\n`\n", nil},
		{"brace in a string", "func f() string { \"type X {\" }\ntype Y {\n\tGo(s string) string,\n}\n", []string{"2 Y"}},
		{"two types", "type A {\n\tF(),\n}\ntype B {\n\tG(),\n\tlaw L() { true },\n}\ntype C { H() }\n", []string{"1 A", "8 C"}},
		{"parameters over lines", "type Store {\n\tGet(\n\t\tk string,\n\t) int,\n}\n", []string{"1 Store"}},
		{"comment in the header", "type Store /* the store */ {\n\tGet(k string) int,\n}\n", []string{"1 Store"}},
		// The text scan read these wrongly (fu-lawcheck-compiler).
		{"indented declaration", "\ttype Indented {\n\t\tRun(n int) int,\n\t}\n", []string{"1 Indented"}},
		{"name outside ASCII", "type Café {\n\tBrew(n int) int,\n}\n", []string{"1 Café"}},
		{"operation outside ASCII", "type Pantry {\n\tRécupérer(n int) int,\n}\n", []string{"1 Pantry"}},
		{"law split over lines", "type Signal {\n\tSend(n int),\n\tlaw\n\tSent() { true },\n}\n", nil},
	} {
		got, problems := lawsIn(t, c.source)
		if len(problems) > 0 {
			t.Errorf("%s: problems %q", c.name, problems)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A package the checker refuses is a problem that names the diagnostic,
// so a type error never hides a lawless type.
func TestLawlessPackageReportsCheckErrors(t *testing.T) {
	t.Parallel()
	got, problems := lawsIn(t, "type Library {\n\tReplays() []Replay,\n}\n")
	if len(got) != 0 || len(problems) != 1 || !strings.Contains(problems[0], "the checker reports") || !strings.Contains(problems[0], "x.ergo:2:") {
		t.Errorf("types %q, problems %q; want one problem naming the diagnostic at x.ergo:2", got, problems)
	}
}

// A program of packages whose domain package declares an operation type
// with no law is a layout error, at its file and line. The binary's
// package, cmd/NAME, is not checked: it binds providers.
func TestModuleProgramOperationTypeNeedsALaw(t *testing.T) {
	t.Parallel()
	setup := func(f *fixture) {
		dir := "examples/walker"
		f.write(dir+"/ergo.mod", "module walker\n")
		f.write(dir+"/README.md", "# walker\n")
		f.write(dir+"/tags", "default\n")
		f.write(dir+"/cases/basic/expected.out", "hi\n")
		f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\nexit 0\ntype Local {\n\tGo(),\n}\n")
	}
	lawless := newFixture(t)
	setup(lawless)
	lawless.write("examples/walker/walk/replays.ergo", "type Replay { Name string }\n\n// Library is a contract.\ntype Library {\n\tReplays() []Replay,\n}\n")
	code, out := lawless.run()
	want := "examples/walker: walk/replays.ergo:4: operation type Library has no law"
	if code != 2 || !strings.Contains(out, want) {
		t.Errorf("lawless: exit %d, want 2 and %q\n%s", code, want, out)
	}
	lawful := newFixture(t)
	setup(lawful)
	lawful.write("examples/walker/walk/replays.ergo", "type Replay { Name string }\n\ntype Library {\n\tReplays() []Replay,\n\tlaw Named() { len(Replays()) >= 0 },\n}\n")
	if code, out := lawful.run(); code != 0 {
		t.Errorf("lawful: exit %d, want 0\n%s", code, out)
	}
	broken := newFixture(t)
	setup(broken)
	broken.write("examples/walker/walk/replays.ergo", "type Library {\n\tReplays() []Missing,\n}\n")
	code, out = broken.run()
	want = "examples/walker: walk: the law check reads the checked package, and the checker reports"
	if code != 2 || !strings.Contains(out, want) {
		t.Errorf("broken: exit %d, want 2 and %q\n%s", code, want, out)
	}
}

// The repository's programs of packages (examples/tidewalk-replay) load
// with no layout error, so each operation type of their domain packages
// has a law.
func TestRepoExamplesHoldTheirLaws(t *testing.T) {
	t.Parallel()
	programs, problems := loadExamples(filepath.Join("..", ".."), false)
	if len(problems) > 0 {
		t.Fatalf("layout errors:\n%s", strings.Join(problems, "\n"))
	}
	modules := 0
	for _, p := range programs {
		if p.module {
			modules++
		}
	}
	if modules == 0 {
		t.Fatal("no program of packages under examples/")
	}
}

// -lawless reports every lawless operation type under examples/, of
// programs of one folder too, runs nothing and exits 0.
func TestLawlessReport(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.program("checkout", "type Payments {\n\tCharge(n int) int,\n}\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
	f.write("examples/walker/ergo.mod", "module walker\n")
	f.write("examples/walker/walk/replays.ergo", "type Library {\n\tReplays() []Replay,\n}\n\ntype Replay { Name string }\n")
	code, out := f.run("-lawless")
	for _, want := range []string{
		"examples/checkout/main.ergo:1: operation type Payments has no law",
		"examples/walker/walk/replays.ergo:1: operation type Library has no law",
		"examples: 2 operation types with no law (report only)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q\n%s", want, out)
		}
	}
	if code != 0 || strings.Contains(out, "ok ") {
		t.Errorf("exit %d, want 0 and no run\n%s", code, out)
	}
}
