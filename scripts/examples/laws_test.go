package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lawlessTypes finds the operation types with no member law, and nothing
// else: records, unions, machines, types with a law, and words inside
// comments and strings are not operation types.
func TestLawlessTypes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		source string
		want   []string // "LINE NAME"
	}{
		{"operation, no law", "// x\ntype Library {\n\tReplays() []Replay,\n}\n", []string{"2 Library"}},
		{"operation and law", "type Library {\n\tReplays() []Replay,\n\tlaw Named() { len(Replays()) >= 0 },\n}\n", nil},
		{"law body over lines", "type Store {\n\tGet(k string) int,\n\n\tlaw L(k string) {\n\t\tlet v = Get(k)\n\t\tv == v\n\t},\n}\n", nil},
		{"one line", "type Codec { Encode(v int) string, Decode(s string) int fails Bad }\n", []string{"1 Codec"}},
		{"generic type and operation", "type Box[T] {\n\tPut[K](k K, v T),\n}\n", []string{"1 Box"}},
		{"record", "type Replay { Name string, Moves string }\ntype F {\n\tRun func(int) bool,\n\tAt int,\n}\n", nil},
		{"empty record", "type Fixed {}\n", nil},
		{"union and machine", "type Move = North{} | East{}\ntype M machine[G, Move] { initial: Start(), step: Step }\n", nil},
		{"law in a comment", "type Library {\n\tReplays() []Replay, // law Named() { true }\n\t/* law X() { true } */\n}\n", []string{"1 Library"}},
		{"brace in a comment", "type Library {\n\tReplays() []Replay, // ends with }\n\tlaw Named() { true },\n}\n", nil},
		{"type in a raw string", "let doc = `\ntype Fake {\n\tGo(),\n}\n`\n", nil},
		{"brace in a string", "func f() string { return \"type X {\" }\ntype Y {\n\tGo(s string) string,\n}\n", []string{"2 Y"}},
		{"two types", "type A {\n\tF(),\n}\ntype B {\n\tG(),\n\tlaw L() { true },\n}\ntype C { H() }\n", []string{"1 A", "8 C"}},
		{"nested constraint", "type Store[T Codec[int]] { Put(v T), }\n", []string{"1 Store"}},
		{"nested constraints, two levels", "type Cache[K Map[string, List[int]], V] {\n\tGet(k K) V,\n}\n", []string{"1 Cache"}},
		{"constraint with parentheses", "type Runner[F func(Codec[int]) bool] {\n\tRun(f F),\n}\n", []string{"1 Runner"}},
		{"type parameters over lines", "type Index[\n\tK Codec[int],\n\tV,\n] {\n\tAt(k K) V,\n}\n", []string{"1 Index"}},
		{"operation with a nested constraint", "type Pool[T] {\n\tTake[K Codec[T]](k K) T,\n}\n", []string{"1 Pool"}},
		{"nested constraint and law", "type Store[T Codec[int]] {\n\tPut(v T),\n\tlaw L() { true },\n}\n", nil},
		{"nested record", "type Pair[T Codec[int]] { Left T, Right List[T] }\n", nil},
	} {
		var got []string
		for _, l := range lawlessTypes("x.ergo", []byte(c.source)) {
			got = append(got, fmt.Sprintf("%d %s", l.line, l.name))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
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
	lawful.write("examples/walker/walk/replays.ergo", "type Replay { Name string }\n\ntype Library {\n\tReplays() []Replay,\n\tlaw Named() { NamedOnce(Replays()) },\n}\n")
	if code, out := lawful.run(); code != 0 {
		t.Errorf("lawful: exit %d, want 0\n%s", code, out)
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
	f.write("examples/walker/walk/replays.ergo", "type Library {\n\tReplays() []Replay,\n}\n")
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
