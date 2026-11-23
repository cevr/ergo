package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/stdlib"
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
	want := []string{stdlib.PreludePath, "memory"}
	if got := nativePackages([]string{key}, &run); !slices.Equal(got, want) {
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
	if n := nativeVerdicts("a", []byte(out), &run); n != 3 {
		t.Errorf("read %d verdict lines, want 3", n)
	}
	nativeVerdicts("w/memory", []byte("ok law w.Store.Kept for Store (100 cases)\n"), &run)
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
