package main

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

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
