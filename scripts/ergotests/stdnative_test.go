package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/testplan"
)

// TestStdNativeTargetsTheBinding uses a module-shaped fixture: the law is
// declared in contract, memory binds it in a named context and viewer only
// imports it. The declaration's placeholder skip cannot schedule contract in
// place of memory. Mutant: nl-native-targets-declaration.
func TestStdNativeTargetsTheBinding(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"ergo.mod":            "module nativefixture\n",
		"contract/store.ergo": "type Store {\n\tGet() int,\n\tlaw ReadsOne() { Get() == 1 },\n}\n",
		"memory/store.ergo":   "import nativefixture/contract\n\ntype Value { get func() int } implements contract.Store\n\nfunc Get(v Value) int { v.get() }\n",
		"memory/tests.ergo":   "import nativefixture/contract\n\nlet fresh = context { contract.Store: Value{get: func() int { 1 }} }\n",
		"viewer/viewer.ergo":  "import nativefixture/contract\n\nfunc Inspect(s contract.Store) int { s.Get() }\n",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const key = "contract Store.ReadsOne"
	run := stdRun{
		declared: map[string]string{key: "contract"},
		verdicts: map[string][]eval.Status{},
		bindings: map[string][]string{},
	}
	for _, pkg := range []string{"contract", "memory", "viewer"} {
		report, err := evaluate(filepath.Join(root, pkg))
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
		for _, result := range report.Results {
			if law, ok := lawOf(result.Name); ok {
				run.recordLaw(pkg, law, result)
			}
		}
	}
	if got := run.verdicts[key]; !slices.Equal(got, []eval.Status{eval.Skipped, eval.Passed}) {
		t.Fatalf("%s: evaluator outcomes %v; want declaration skip and provider pass", key, got)
	}
	nativeOnly := []testplan.NativeRoot{{Path: stdlib.PreludePath, Required: true}}
	want := []testplan.NativeRoot{nativeOnly[0], {Path: "memory"}}
	if got := nativePackages(nativeOnly, []string{key}, &run); !slices.Equal(got, want) {
		t.Fatalf("native packages %v, want %v", got, want)
	}
}

// TestStdNativeVerdictsCount: a no-runner law the evaluator skipped counts
// as run when its package's native run (ergo test std:PKG) prints `ok law`
// for it, a member law's qualified run counting for its declaring
// package; a native skip still runs nothing, and a native FAIL is a
// failure of class "native fail" (NATIVE-LAWS slice 2a). Mutant:
// nl-native-leg-dropped.
func TestStdNativeVerdictsCount(t *testing.T) {
	run := stdRun{
		verdicts: map[string][]eval.Status{"a Ran": {eval.Skipped}, "a Skips": {eval.Skipped}, "a Fails": {eval.Skipped}, "w Store.Kept": {eval.Skipped}},
		failed:   map[string]string{},
		native:   map[string][]bool{},
		declared: map[string]string{"w Store.Kept": "w"},
	}
	out := "ok law Ran (100 cases)\nskip law Skips: needs a host\nFAIL law Fails: refuted at seed 3\nnoise\n"
	if n := nativeVerdicts(testplan.NativeRoot{Path: "a"}, []byte(out), &run); n != 3 {
		t.Errorf("read %d verdict lines, want 3", n)
	}
	nativeVerdicts(testplan.NativeRoot{Path: "w/memory"}, []byte("ok law w.Store.Kept for Store (100 cases)\n"), &run)
	got := strings.Join(redProblems([]string{"a Ran", "a Skips", "a Fails", "w Store.Kept"}, run, map[string]redLine{}), "\n")
	for _, want := range []string{"a Skips is not run", "a Fails is native fail"} {
		if !strings.Contains(got, want) {
			t.Errorf("the problems do not name %q:\n%s", want, got)
		}
	}
	for _, quiet := range []string{"a Ran", "Kept"} {
		if strings.Contains(got, quiet) {
			t.Errorf("the problems name %q, which ran natively:\n%s", quiet, got)
		}
	}
}

// TestNativeRootOutcomeFollowsPlan catches the path-keyed native failure
// and missing-note mutants. A fixture required root behaves exactly like
// another required root; an ordinary run with the old name does not.
func TestNativeRootOutcomeFollowsPlan(t *testing.T) {
	const note = "law Contract.Law runs where Contract has a provider: none is in this package or its imports"
	for _, root := range []testplan.NativeRoot{
		{Path: "fixture/contracts", Required: true},
		{Path: stdlib.PreludePath},
	} {
		var out bytes.Buffer
		run := stdRun{nativeMissing: map[string]string{}}
		got := nativeResult(&out, root, errors.New("cannot invoke"), []byte(note), &run)
		prefix, want := "red ", 0
		if root.Required {
			prefix, want = "FAIL ", 1
		}
		if got != want || !strings.HasPrefix(out.String(), prefix) {
			t.Errorf("%+v: exit %d, output %q, want %d and %q", root, got, &out, want, prefix)
		}
		if missing := run.nativeMissing[root.Path+" Contract.Law"]; (missing == note) != root.Required {
			t.Errorf("%+v: accounted for %q, want the missing note only for the required root", root, missing)
		}
	}
}
