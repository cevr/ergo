package lint_test

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func checkedGraph(t *testing.T, files map[string]string) *graphrows.Table {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "app"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		t.Fatalf("module does not check: %v", a.Diagnostics)
	}
	return a.Graph()
}

// TestLayoutRatchetAgreesWithReference verifies monotone evidence comparison
// across base/head checked graph snapshots:
//  1. Self-comparison has 0 added/grown/removed and marks all debt unchanged;
//  2. Adding a new internal edge inside an existing SCC with the SAME members
//     regresses (kills mutant layout-ratchet-allows-new-edge-in-old-scc);
//  3. Splitting/removing an SCC passes with removed > 0 and added == 0;
//  4. Net-count trap: deleting two long files while adding one new long file
//     still regresses (tracks actual additions, not net counts);
//  5. Growing excess on a long file or rising occurrence count on a foreign
//     record field write appears in Grown and sets Regressed == true.
func TestLayoutRatchetAgreesWithReference(t *testing.T) {
	newEdgeHits, splitPasses, netTraps, growthHits := 0, 0, 0, 0
	for _, seed := range propertySeeds(8) {
		r := rand.New(rand.NewPCG(seed, 0x7a7c))
		extraLines := 2 + r.IntN(3)
		policy := lint.LayoutPolicy{MaxFileLines: 9}

		// Base module with a sibling-folder cycle {a, b}, two long files, and a foreign field write.
		baseFiles := map[string]string{
			"ergo.mod":     "module app\n",
			"a/q/q.ergo":   "func Q() int { 2 }\n",
			"b/q/q.ergo":   "func Q() int { 3 }\nfunc Q2() int { 4 }\n",
			"a/p/p.ergo":   "import app/b/q\n\nfunc P() int { q.Q() }\n",
			"b/p/p.ergo":   "import app/a/q\n\nfunc P() int { q.Q() }\n",
			"queue/q.ergo": "type Queue { Items [1]int, head int }\nfunc New() Queue { Queue{Items: [1]int{0}, head: 0} }\n",
			"sched/s.ergo": "import app/queue\n\nfunc Bump(n int) int {\n\tlet mut q = queue.New()\n\tq.Items[0] = n\n\tq.Items[0]\n}\n",
			"long1/l.ergo": "func L1() int { 1 }\n" + strings.Repeat("// pad\n", 10),
			"long2/l.ergo": "func L2() int { 2 }\n" + strings.Repeat("// pad\n", 10),
			"short/s.ergo": "func S() int { 3 }\n",
			"cmd/app/main.ergo": strings.Join([]string{
				"import app/a/p",
				"import bp app/b/p",
				"import app/sched",
				"import app/long1",
				"import app/long2",
				"import app/short",
				"func main() needs Console { Console.Println(\"\\(p.P() + bp.P() + sched.Bump(1) + long1.L1() + long2.L2() + short.S())\") }",
			}, "\n") + "\n",
		}
		base := checkedGraph(t, baseFiles)

		// 1. Self-comparison: 0 delta, all existing debt unchanged.
		selfCmp, err := lint.CompareLayout(base, base, []string{"app"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !selfCmp.Advisory || selfCmp.Regressed || len(selfCmp.Added) != 0 || len(selfCmp.Grown) != 0 || len(selfCmp.Removed) != 0 || len(selfCmp.Unchanged) == 0 {
			t.Fatalf("seed %d: self-comparison unexpected: %+v", seed, selfCmp)
		}

		// 2. Same SCC members {a, b} + NEW internal edge a/p.P -> b/q.Q2!
		// Must regress (kills layout-ratchet-allows-new-edge-in-old-scc).
		newEdgeFiles := cloneMap(baseFiles)
		newEdgeFiles["a/p/p.ergo"] = "import app/b/q\n\nfunc P() int { q.Q() + q.Q2() }\n"
		headNewEdge := checkedGraph(t, newEdgeFiles)
		cmpNewEdge, err := lint.CompareLayout(base, headNewEdge, []string{"app"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !cmpNewEdge.Advisory || !cmpNewEdge.Regressed || len(cmpNewEdge.Added) == 0 {
			t.Fatalf("seed %d: new edge in same SCC must regress, got %+v", seed, cmpNewEdge)
		}
		foundNewEdge := false
		for _, a := range cmpNewEdge.Added {
			if a.Rule == "sibling-folder-cycle" && strings.Contains(a.Key, "app/b/q.Q2") {
				foundNewEdge = true
			}
		}
		if !foundNewEdge {
			t.Fatalf("seed %d: expected new intra-SCC edge app/b/q.Q2 in Added, got %+v", seed, cmpNewEdge.Added)
		}
		newEdgeHits++

		// 3. Genuine removal / SCC split: break b/p -> a/q without adding edges.
		splitFiles := cloneMap(baseFiles)
		splitFiles["b/p/p.ergo"] = "func P() int { 1 }\n"
		headSplit := checkedGraph(t, splitFiles)
		cmpSplit, err := lint.CompareLayout(base, headSplit, []string{"app"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if cmpSplit.Regressed || len(cmpSplit.Added) != 0 || len(cmpSplit.Removed) == 0 {
			t.Fatalf("seed %d: SCC removal must pass with Removed > 0, got %+v", seed, cmpSplit)
		}
		splitPasses++

		// 4. Net-count trap: shorten both long1 and long2 (2 removals), lengthen short (1 addition).
		netFiles := cloneMap(baseFiles)
		netFiles["long1/l.ergo"] = "func L1() int { 1 }\n"
		netFiles["long2/l.ergo"] = "func L2() int { 2 }\n"
		netFiles["short/s.ergo"] = "func S() int { 3 }\n" + strings.Repeat("// pad\n", 9+extraLines)
		headNet := checkedGraph(t, netFiles)
		cmpNet, err := lint.CompareLayout(base, headNet, []string{"app"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !cmpNet.Regressed || len(cmpNet.Added) != 1 || len(cmpNet.Removed) != 2 {
			t.Fatalf("seed %d: net-count trap must regress (1 added, 2 removed), got %+v", seed, cmpNet)
		}
		netTraps++

		// 5. Growing excess (long1 grows) + rising occurrence count (sched.Bump writes q.Items twice).
		growFiles := cloneMap(baseFiles)
		growFiles["long1/l.ergo"] = baseFiles["long1/l.ergo"] + strings.Repeat("// more\n", extraLines)
		growFiles["sched/s.ergo"] = "import app/queue\n\nfunc Bump(n int) int {\n\tlet mut q = queue.New()\n\tq.Items[0] = n\n\tq.Items[0] = n + 1\n\tq.Items[0]\n}\n"
		headGrow := checkedGraph(t, growFiles)
		cmpGrow, err := lint.CompareLayout(base, headGrow, []string{"app"}, policy)
		if err != nil {
			t.Fatal(err)
		}
		if !cmpGrow.Regressed || len(cmpGrow.Grown) != 2 {
			t.Fatalf("seed %d: expected 2 grown items (long-file + foreign-record-field-write), got %+v", seed, cmpGrow)
		}
		growthHits++
	}
	if len(propertySeeds(8)) > 1 && (newEdgeHits == 0 || splitPasses == 0 || netTraps == 0 || growthHits == 0) {
		t.Fatalf("vacuous: newEdge=%d split=%d net=%d growth=%d", newEdgeHits, splitPasses, netTraps, growthHits)
	}
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
