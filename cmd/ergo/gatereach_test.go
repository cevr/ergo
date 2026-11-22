package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
)

// gateReachWorkspace writes a workspace of n members, w0 … w(n-1), into
// root. Member i's package vi holds V() = c[i] plus V() of each member it
// imports (deps[i], all below i), and a law that V() is the value the
// generator computed. Its go.mod replaces every member it reaches,
// transitively, with that member's folder, as a build of it needs. The
// laws run at check time, so no package is built.
func gateReachWorkspace(t *testing.T, root string, c []int, deps [][]int, want []int) {
	t.Helper()
	n := len(c)
	reach := make([]map[int]bool, n)
	for i := range n {
		reach[i] = map[int]bool{}
		for _, j := range deps[i] {
			reach[i][j] = true
			for k := range reach[j] {
				reach[i][k] = true
			}
		}
	}
	for i := range n {
		mod := fmt.Sprintf("ws.test/w%d", i)
		smokeWrite(t, root, fmt.Sprintf("w%d/ergo.mod", i), "module "+mod+"\n")
		gomod := "module " + mod + "\n\ngo 1.27\n"
		for j := range n {
			if reach[i][j] {
				gomod += fmt.Sprintf("\nrequire ws.test/w%d v0.0.0\n\nreplace ws.test/w%d => ../w%d\n", j, j, j)
			}
		}
		smokeWrite(t, root, fmt.Sprintf("w%d/go.mod", i), gomod)
		var src strings.Builder
		for _, j := range deps[i] {
			fmt.Fprintf(&src, "import ws.test/w%d/v%d\n", j, j)
		}
		if len(deps[i]) > 0 {
			src.WriteString("\n")
		}
		fmt.Fprintf(&src, "// V is this member's value.\nfunc V() int { %d", c[i])
		for _, j := range deps[i] {
			fmt.Fprintf(&src, " + v%d.V()", j)
		}
		fmt.Fprintf(&src, " }\n\n// VHolds holds while V is what the generator computed.\nlaw VHolds(n int) { V() + n == %d + n }\n", want[i])
		smokeWrite(t, root, fmt.Sprintf("w%d/v%d/v%d.ergo", i, i, i), src.String())
		// In ergo fmt's layout, so fmt --check holds.
		if err := formatCommand([]string{fmt.Sprintf("%s/w%d/...", root, i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// gateReachValues are each member's V() for constants c.
func gateReachValues(c []int, deps [][]int) []int {
	v := make([]int, len(c))
	for i := range c {
		v[i] = c[i]
		for _, j := range deps[i] {
			v[i] += v[j]
		}
	}
	return v
}

// TestGateKeyReachesReplacedModules is GATE-KEY-REACHES's red check: app
// builds against lib through a local replace, and an edit of lib that
// breaks app's law must fail app's gate. Before the fix, app's module key
// held only app's own files, so the second run replayed app's stale pass.
// It kills gate-key-replace-dropped and gate-key-replace-files-dropped.
func TestGateKeyReachesReplacedModules(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv("ERGO_LAWCACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	root := t.TempDir()
	deps := [][]int{nil, {0}}
	gateReachWorkspace(t, root, []int{1, 1}, deps, []int{1, 2})
	app := root + "/w1"
	if failed := runGate(t, app).Failed; len(failed) > 0 {
		t.Fatalf("the first run failed: %v", failed)
	}
	// lib's V is 2 now: app's law VHolds (V() == 2) breaks.
	gateReachWorkspace(t, root, []int{2, 1}, deps, []int{2, 2})
	if failed := runGate(t, app).Failed; !hasFailure(failed, "check", "v1") {
		t.Fatalf("lib's edit breaks app's law, but app's gate reported %v (a stale pass replayed)", failed)
	}
}

// TestGateKeyAgreesWithNoStore: over seeded workspaces (members importing
// lower members, go.mod replaces for every member reached) and seeded
// edits of a member's constant, each member's gate verdict with the store
// equals its verdict with the store off (ERGO_LAWCACHE=off). An edit
// changes the values of the edited member and every member that reaches
// it; the laws keep the values the generator computed first, so a member
// that reaches an edited one fails, and a store that keys it by its own
// files alone replays its pass (gate-key-replace-dropped and
// gate-key-replace-files-dropped, over random member graphs).
func TestGateKeyAgreesWithNoStore(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	for seed := uint64(1); seed <= 2; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		n := 3 + r.IntN(2)
		deps := make([][]int, n)
		for i := 1; i < n; i++ {
			for j := range i {
				if r.IntN(2) == 0 || j == i-1 && len(deps[i]) == 0 {
					deps[i] = append(deps[i], j)
				}
			}
		}
		c := make([]int, n)
		for i := range c {
			c[i] = 1 + r.IntN(5)
		}
		want := gateReachValues(c, deps)
		root := t.TempDir()
		gateReachWorkspace(t, root, c, deps, want)
		var steps []string
		for step := range 4 {
			k := r.IntN(n)
			if step == 0 {
				k = 0 // the member every other one reaches, at least once
			}
			edited := append([]int(nil), c...)
			edited[k] += 1 + r.IntN(3)
			if step%2 == 1 {
				edited = c // undo: every verdict holds again
			}
			steps = append(steps, fmt.Sprint(edited))
			gateReachWorkspace(t, root, edited, deps, want)
			for i := range n {
				dir := fmt.Sprintf("%s/w%d", root, i)
				t.Setenv("ERGO_LAWCACHE", "")
				got := runGate(t, dir).Failed
				t.Setenv("ERGO_LAWCACHE", "off")
				ref := runGate(t, dir).Failed
				if (len(got) == 0) != (len(ref) == 0) {
					t.Fatalf("seed %d, deps %v, constants %v: member w%d's gate with the store failed %v, with the store off %v",
						seed, deps, steps, i, got, ref)
				}
			}
		}
	}
}
