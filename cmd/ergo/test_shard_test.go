package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShardFlagValidation(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "a.ergo")
	if err := os.WriteFile(pkg, []byte("func F() int { 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range [][]string{
		{"--shard=0/2", pkg},
		{"--shard=3/2", pkg},
		{"--shard=1/0", pkg},
		{"--shard=abc", pkg},
		{"--shard=1/2/3", pkg},
		{"--shard=-1/2", pkg},
		{"-j", "0", pkg},
		{"-j=-1", pkg},
		{"--jobs=abc", pkg},
		{"--update-timings", pkg},
		{"--timings=", pkg},
	} {
		err := command(append([]string{"test"}, bad...))
		if code := exitCode(err); code != 2 {
			t.Errorf("ergo test %v: exit %d (%v), want 2", bad, code, err)
		}
	}

	// Other commands refuse test sharding flags (exit 2).
	if code := exitCode(command([]string{"check", "--shard=1/2", pkg})); code != 2 {
		t.Errorf("ergo check --shard=1/2: exit %d, want 2", code)
	}
	if code := exitCode(command([]string{"build", "--parallel", pkg})); code != 2 {
		t.Errorf("ergo build --parallel: exit %d, want 2", code)
	}

	// Malformed timings JSON exits 2 cleanly.
	badTimings := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badTimings, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := exitCode(command([]string{"test", "--timings=" + badTimings, pkg})); code != 2 {
		t.Errorf("malformed --timings JSON: exit %d, want 2", code)
	}
}

func TestShardExactPartition(t *testing.T) {
	for _, count := range []int{0, 1, 2, 5, 13, 25} {
		targets := make([]testTarget, count)
		timings := TestTimings{Version: 1, Packages: map[string]float64{}}
		for i := 0; i < count; i++ {
			k := fmt.Sprintf("mod%d/pkg%02d", i%3, i)
			targets[i] = testTarget{label: k, dir: k, modRoot: fmt.Sprintf("mod%d", i%3), timingKey: k}
			if i%2 == 0 {
				timings.Packages[k] = float64((i*7)%11 + 1)
			}
		}
		for n := 1; n <= 16; n++ {
			shards := planShards(targets, n, timings)
			if len(shards) != n {
				t.Fatalf("count=%d n=%d: got %d shards, want %d", count, n, len(shards), n)
			}
			seen := map[string]int{}
			for _, sh := range shards {
				for _, tgt := range sh {
					seen[tgt.timingKey]++
				}
			}
			if len(seen) != count {
				t.Fatalf("count=%d n=%d: partition saw %d unique targets, want %d", count, n, len(seen), count)
			}
			for _, tgt := range targets {
				if seen[tgt.timingKey] != 1 {
					t.Fatalf("count=%d n=%d: target %q appeared %d times, want 1", count, n, tgt.timingKey, seen[tgt.timingKey])
				}
			}
			// Determinism check: running planShards again produces identical shards.
			again := planShards(targets, n, timings)
			for s := 0; s < n; s++ {
				if !slices.Equal(shards[s], again[s]) {
					t.Fatalf("count=%d n=%d shard %d not deterministic", count, n, s)
				}
			}
		}
	}
}

func TestShardLPTBalance(t *testing.T) {
	// Skewed durations [10, 9, 2, 2, 1, 1] across 2 shards:
	// Naive half-split gives 21s vs 4s; LPT MUST give 13s vs 12s.
	durs := []float64{10, 9, 2, 2, 1, 1}
	targets := make([]testTarget, len(durs))
	timings := TestTimings{Version: 1, Packages: map[string]float64{}}
	for i, d := range durs {
		k := fmt.Sprintf("m/p%d", i)
		targets[i] = testTarget{label: k, dir: k, modRoot: "m", timingKey: k}
		timings.Packages[k] = d
	}
	shards := planShards(targets, 2, timings)
	load := func(sh []testTarget) float64 {
		sum := 0.0
		for _, tgt := range sh {
			sum += timings.Packages[tgt.timingKey]
		}
		return sum
	}
	l0, l1 := load(shards[0]), load(shards[1])
	hi, lo := max(l0, l1), min(l0, l1)
	if hi != 13 || lo != 12 {
		t.Fatalf("LPT shard loads = (%.1f, %.1f), want (13.0, 12.0); shards=%+v", l0, l1, shards)
	}

	// Strictly distinguishes LPT (descending: 12 vs 12) from SPT (ascending: 13 vs 11).
	durs2 := []float64{10, 9, 2, 2, 1}
	targets2 := make([]testTarget, len(durs2))
	timings2 := TestTimings{Version: 1, Packages: map[string]float64{}}
	for i, d := range durs2 {
		k := fmt.Sprintf("m2/p%d", i)
		targets2[i] = testTarget{label: k, dir: k, modRoot: "m2", timingKey: k}
		timings2.Packages[k] = d
	}
	shards2 := planShards(targets2, 2, timings2)
	sum2 := func(sh []testTarget) float64 {
		sum := 0.0
		for _, tgt := range sh {
			sum += timings2.Packages[tgt.timingKey]
		}
		return sum
	}
	if a, b := sum2(shards2[0]), sum2(shards2[1]); a != 12 || b != 12 {
		t.Fatalf("LPT shard loads on [10,9,2,2,1] = (%.1f, %.1f), want (12.0, 12.0)", a, b)
	}
}

func TestShardPathLocalityTieBreaking(t *testing.T) {
	// Equal-weight unseen packages across 2 modules whose timingKeys interleave
	// alphabetically: localityGroup (modRoot) must keep siblings co-located in
	// the same shard rather than splitting them by raw key order.
	targets := []testTarget{
		{label: "alpha/p1", dir: "z_mod/alpha/p1", modRoot: "z_mod", timingKey: "alpha/p1"},
		{label: "beta/p1", dir: "a_mod/beta/p1", modRoot: "a_mod", timingKey: "beta/p1"},
		{label: "mid/p2", dir: "a_mod/mid/p2", modRoot: "a_mod", timingKey: "mid/p2"},
		{label: "zebra/p2", dir: "z_mod/zebra/p2", modRoot: "z_mod", timingKey: "zebra/p2"},
	}
	shards := planShards(targets, 2, TestTimings{Version: 1, Packages: map[string]float64{}})
	if len(shards[0]) != 2 || len(shards[1]) != 2 {
		t.Fatalf("expected 2 targets per shard, got %d and %d", len(shards[0]), len(shards[1]))
	}
	if shards[0][0].modRoot != shards[0][1].modRoot || shards[1][0].modRoot != shards[1][1].modRoot {
		t.Fatalf("sibling packages were split across shards: shard0=%+v shard1=%+v", shards[0], shards[1])
	}
}

func TestTimingsRoundTripAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "timings.json")

	// Missing file loads cleanly as empty history.
	loaded, err := loadTestTimings(path)
	if err != nil || len(loaded.Packages) != 0 || loaded.Version != 1 {
		t.Fatalf("missing timings file: got %+v err=%v", loaded, err)
	}

	// Shard 1 writes its packages.
	if err := updateTestTimings(path, map[string]float64{"m/a": 1.2345, "m/b": 0.5}); err != nil {
		t.Fatalf("first updateTestTimings: %v", err)
	}
	// Shard 2 writes its packages; must merge without clobbering m/a or m/b!
	if err := updateTestTimings(path, map[string]float64{"m/c": 2.75, "m/b": 0.6}); err != nil {
		t.Fatalf("second updateTestTimings: %v", err)
	}

	merged, err := loadTestTimings(path)
	if err != nil {
		t.Fatalf("reload merged timings: %v", err)
	}
	if len(merged.Packages) != 3 || merged.Packages["m/a"] != 1.235 || merged.Packages["m/b"] != 0.6 || merged.Packages["m/c"] != 2.75 {
		t.Fatalf("merged timings mismatch: %+v", merged.Packages)
	}
}

func TestParallelExecutionAndFailureSemantics(t *testing.T) {
	targets := []testTarget{
		{label: "m/slow", dir: "m/slow", timingKey: "m/slow"},
		{label: "m/fail", dir: "m/fail", timingKey: "m/fail"},
		{label: "m/fast", dir: "m/fast", timingKey: "m/fast"},
	}
	var ran atomic.Int32
	outcomes := runTargetsParallel(targets, 2, TestTimings{Version: 1, Packages: map[string]float64{"m/slow": 2.0}}, func(i int, outW, errW io.Writer) error {
		ran.Add(1)
		switch i {
		case 0:
			time.Sleep(30 * time.Millisecond)
			fmt.Fprintln(outW, "PASS slow")
			return nil
		case 1:
			fmt.Fprintln(outW, "FAIL fail")
			return exitStatusError{code: 1, handled: true}
		default:
			fmt.Fprintln(outW, "PASS fast")
			return nil
		}
	})
	if ran.Load() != 3 {
		t.Fatalf("expected all 3 packages to run despite failure, ran=%d", ran.Load())
	}

	var stdout, stderr bytes.Buffer
	err := aggregateOutcomes(targets, outcomes, true, &stdout, &stderr)
	var status exitStatusError
	if !errors.As(err, &status) || status.code != 1 {
		t.Fatalf("aggregateOutcomes err=%v, want exit 1", err)
	}
	wantOut := "package m/slow\nPASS slow\npackage m/fail\nFAIL fail\npackage m/fast\nPASS fast\n"
	if stdout.String() != wantOut {
		t.Fatalf("deterministic flush mismatch:\ngot:\n%s\nwant:\n%s", stdout.String(), wantOut)
	}

	// Defect (exit 2) takes precedence over failure (exit 1).
	outcomes[2].err = exitStatusError{code: 2, handled: true}
	stdout.Reset()
	stderr.Reset()
	err = aggregateOutcomes(targets, outcomes, true, &stdout, &stderr)
	if !errors.As(err, &status) || status.code != 2 {
		t.Fatalf("defect precedence err=%v, want exit 2", err)
	}
}

func TestParallelShardedCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	for rel, src := range map[string]string{
		"ergo.mod":   "module demo\n",
		"p1/p1.ergo": "func Add(a int, b int) int {\n\ta + b\n}\n\nlaw AddComm(a int, b int) {\n\tAdd(a, b) == Add(b, a)\n}\n",
		"p2/p2.ergo": "func Mul(a int, b int) int {\n\ta * b\n}\n\nlaw MulComm(a int, b int) {\n\tMul(a, b) == Mul(b, a)\n}\n",
		"p3/p3.ergo": "func Sub(a int, b int) int {\n\ta - b\n}\n\nlaw SubZero(a int) {\n\tSub(a, 0) == a\n}\n",
		"p4/p4.ergo": "func Inc(a int) int {\n\ta + 1\n}\n\nlaw IncGt(a int) {\n\tInc(a) == a + 1\n}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	timingsFile := filepath.Join(dir, "timings.json")
	pattern := filepath.Join(dir, "...")

	out1, err := testCommandOutput(t, []string{pattern, "--count", "1", "--parallel", "-j", "2", "--shard=1/2", "--timings=" + timingsFile, "--update-timings"})
	if err != nil {
		t.Fatalf("shard 1/2 failed: %v\nstdout:\n%s", err, out1)
	}
	out2, err := testCommandOutput(t, []string{pattern, "--count", "1", "--parallel", "-j", "2", "--shard=2/2", "--timings=" + timingsFile, "--update-timings"})
	if err != nil {
		t.Fatalf("shard 2/2 failed: %v\nstdout:\n%s", err, out2)
	}

	extractPkgs := func(s string) []string {
		var pkgs []string
		for _, line := range strings.Split(s, "\n") {
			if p, ok := strings.CutPrefix(line, "package "); ok {
				pkgs = append(pkgs, strings.TrimSpace(p))
			}
		}
		return pkgs
	}
	pkgs1, pkgs2 := extractPkgs(out1), extractPkgs(out2)
	if len(pkgs1) == 0 || len(pkgs2) == 0 {
		t.Fatalf("expected both shards to run packages; shard1=%v shard2=%v", pkgs1, pkgs2)
	}
	union := append(slices.Clone(pkgs1), pkgs2...)
	slices.Sort(union)
	want := []string{"demo/p1", "demo/p2", "demo/p3", "demo/p4"}
	if !slices.Equal(union, want) {
		t.Fatalf("union of shards = %v, want %v (s1=%v, s2=%v)", union, want, pkgs1, pkgs2)
	}

	// Verify timings file contains all 4 merged packages.
	timings, err := loadTestTimings(timingsFile)
	if err != nil {
		t.Fatalf("load timings: %v", err)
	}
	for _, w := range want {
		if _, ok := timings.Packages[w]; !ok {
			t.Errorf("timings file missing %q: %+v", w, timings.Packages)
		}
	}

	// Empty shard (n > len(pkgs)) prints clean summary and exits 0.
	emptyOut, err := testCommandOutput(t, []string{pattern, "--shard=5/5"})
	if err != nil {
		t.Fatalf("empty shard 5/5 failed: %v\nstdout:\n%s", err, emptyOut)
	}
	if !strings.Contains(emptyOut, "shard 5/5: 0 packages") {
		t.Errorf("empty shard 5/5 output = %q, want shard 5/5: 0 packages", emptyOut)
	}
}
