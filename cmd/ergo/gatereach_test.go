package main

import (
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/task"
)

// TestGateKeyReachesReplacedModules is GATE-KEY-REACHES's red check: app
// builds against lib through a local replace, and an edit of lib that
// breaks app's law must fail app's check. Before the fix, app's module key
// held only app's own files, so the second run replayed app's stale pass.
// It kills task-module-key-reach-dropped and task-module-key-reach-files-dropped.
func TestGateKeyReachesReplacedModules(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv("ERGO_LAWCACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	root := t.TempDir()
	deps := [][]int{nil, {0}}
	writeTaskWorkspace(t, root, []taskEdit{{c: 1}, {c: 1}}, deps)
	if first := runTaskJSON(t, "gate", root); first.failed {
		t.Fatalf("the first run failed: %+v", first.summary.Failed)
	}
	// lib's V is 2 now, and app's law still pins V() == 2 (1 + lib's 1).
	smokeWrite(t, root, "w0/v0/v0.ergo", "// V is this member's value.\nfunc V() int { 2 }\n\n// VHolds holds while V is what the generator computed.\nlaw VHolds(n int) { V() + n == 2 + n }\n")
	smokeWrite(t, root, "w1/v1/v1.ergo", "import ws.test/w0/v0\n\n// V is this member's value.\nfunc V() int { 1 + v0.V() }\n\n// VHolds holds while V is what the generator computed.\nlaw VHolds(n int) { V() + n == 2 + n }\n")
	for _, m := range []string{"w0", "w1"} {
		if err := formatCommand([]string{root + "/" + m + "/..."}); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := runTaskJSON(t, "gate", root).verdict("check", "w1"); v.Verdict != task.Fail {
		t.Fatalf("lib's edit breaks app's law, but app's check is %+v (a stale pass replayed)", v)
	}
}
