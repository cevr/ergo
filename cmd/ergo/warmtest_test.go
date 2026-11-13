package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// verdictWorld is the state TestCachedVerdictAgrees edits: a module whose
// app package tests a body of its own and one of the lib package it
// imports.
type verdictWorld struct {
	root    string
	limit   int // the app test's bound: an edit of the test
	offset  int // app's Offset(): an edit of a tested body
	value   int // lib's Value(): an edit of an imported body
	seed    int // --seed
	run     bool
	explore bool
}

func (w verdictWorld) write(t *testing.T) {
	t.Helper()
	smokeWrite(t, w.root, "ergo.mod", "module verdict\n")
	smokeWrite(t, w.root, "lib/lib.ergo", fmt.Sprintf("// Package lib is the verdict tests' library.\n\n// Value is the library's number.\nfunc Value() int {\n\t%d\n}\n", w.value))
	smokeWrite(t, w.root, "app/app.ergo", fmt.Sprintf("import verdict/lib\n\n// Offset is the app's number.\nfunc Offset() int {\n\t%d\n}\n\ntest Bounded {\n\tassert lib.Value() + Offset() < %d\n}\n", w.offset, w.limit))
}

func (w verdictWorld) args() []string {
	args := []string{"test", "./app", "--seed", fmt.Sprint(w.seed)}
	if w.run {
		args = append(args, "--run", "Bounded")
	}
	if w.explore {
		args = append(args, "--explore")
	}
	return args
}

// TestWarmTestRunsNothing is compile-speed CS8's counted receipt: a
// second `ergo test` of an unchanged package folder whose first run
// passed is a warm answer. It prints the first run's output and the
// replay note, and every work counter is 0 (nothing parsed, checked,
// lowered, compiled, linked, started or written); the bytes read and
// hashed are the inputs' bytes, each read once. It kills
// cs-verdict-not-warm, which only these counts see. It builds Go because
// the cold run's verdict is a real test binary's.
func TestWarmTestRunsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	w := verdictWorld{root: physicalTempDir(t), limit: 9, offset: 1, value: 1, seed: 1}
	w.write(t)
	xdg := physicalTempDir(t)
	var inputs int64
	for _, name := range []string{"ergo.mod", "lib/lib.ergo", "app/app.ergo"} {
		info, err := os.Stat(filepath.Join(w.root, name))
		if err != nil {
			t.Fatal(err)
		}
		inputs += info.Size()
	}
	cold := runWarm(t, bin, w.root, xdg, nil, w.args()...)
	warm := runWarm(t, bin, w.root, xdg, nil, w.args()...)
	if cold.code != 0 || warm.code != 0 || warm.stdout != cold.stdout || !strings.Contains(cold.stdout, "PASS") {
		t.Fatalf("cold exit %d, warm exit %d; stdout %q, then %q", cold.code, warm.code, cold.stdout, warm.stdout)
	}
	for k, v := range warm.counts {
		want := int64(0)
		switch k {
		case "files_read":
			want = 3
		case "bytes_read", "bytes_hashed":
			want = inputs
		}
		if v != want {
			t.Errorf("warm test: %s = %d, want %d", k, v, want)
		}
	}
}

// TestCachedVerdictAgrees is the model-based property of the warm verdict
// (models/warmstamp, as TestWarmAnswerAgreesWithFullBuild is for build and
// check): seeded sequences of edits, and after each one an `ergo test` of
// the app package run twice on the warm path, against the reference, a
// fresh run (ERGO_BUILD=full --count 1 in a cache of its own). Each run's
// exit status and output are the reference's. The second run of a request
// whose first passed is a warm answer and does no work; a request that
// changed (an edit of the test, of the tested body or of the imported
// library's body, another --seed or --run, --explore) or whose last run
// failed runs the normal path. A failing seed prints its steps; replay
// with ERGO_VERDICT_SEED=N.
//
// It kills cs-verdict-key-omits-seed, cs-verdict-key-omits-dep-body,
// cs-verdict-caches-failure-as-pass and cs-verdict-key-omits-explore-tag.
// It builds Go because the verdicts are real test binaries'.
func TestCachedVerdictAgrees(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	seeds := []uint64{1, 2}
	if s := os.Getenv("ERGO_VERDICT_SEED"); s != "" {
		var seed uint64
		fmt.Sscan(s, &seed)
		seeds = []uint64{seed}
	}
	for _, seed := range seeds {
		verdictSequence(t, seed, bin)
	}
}

func verdictSequence(t *testing.T, seed uint64, bin string) {
	r := rand.New(rand.NewPCG(seed, 11))
	w := verdictWorld{root: physicalTempDir(t), limit: 9, offset: 1, value: 1, seed: 1}
	w.write(t)
	warmXDG, refXDG := physicalTempDir(t), physicalTempDir(t)
	var steps []string
	passed := map[string]bool{}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d, after %s: %s (replay with ERGO_VERDICT_SEED=%d)", seed, strings.Join(steps, ", "), fmt.Sprintf(format, args...), seed)
	}
	// The edits keep each file's size and time, so only its bytes tell.
	keep := func(edit func()) {
		var stamps []time.Time
		names := []string{"lib/lib.ergo", "app/app.ergo"}
		for _, name := range names {
			info, err := os.Stat(filepath.Join(w.root, name))
			if err != nil {
				t.Fatal(err)
			}
			stamps = append(stamps, info.ModTime())
		}
		edit()
		w.write(t)
		for i, name := range names {
			os.Chtimes(filepath.Join(w.root, name), stamps[i], stamps[i])
		}
	}
	kinds := r.Perm(7)
	for i := range 1 + 2*len(kinds) {
		var step string
		switch k := kinds[(i+2*len(kinds)-1)%len(kinds)]; {
		case i == 0:
			step = "first test"
		case k == 0:
			// 9 passes, 2 fails: lib's Value and Offset are at least 1.
			keep(func() { w.limit = 11 - w.limit })
			step = fmt.Sprintf("the test's bound %d", w.limit)
		case k == 1:
			keep(func() { w.offset = 1 + (w.offset+r.IntN(8))%9 })
			step = fmt.Sprintf("Offset %d", w.offset)
		case k == 2:
			keep(func() { w.value = 1 + (w.value+r.IntN(8))%9 })
			step = fmt.Sprintf("lib's Value %d", w.value)
		case k == 3:
			w.seed = 3 - w.seed
			step = fmt.Sprintf("--seed %d", w.seed)
		case k == 4:
			w.run = !w.run
			step = fmt.Sprintf("--run %t", w.run)
		case k == 5:
			w.explore = !w.explore
			step = fmt.Sprintf("--explore %t", w.explore)
		default:
			step = "touch"
			later := time.Now().Add(time.Duration(1+r.IntN(5)) * time.Second)
			os.Chtimes(filepath.Join(w.root, "lib", "lib.ergo"), later, later)
		}
		steps = append(steps, step)
		ref := runWarm(t, bin, w.root, refXDG, []string{"ERGO_BUILD=full"}, append(w.args(), "--count", "1")...)
		first := runWarm(t, bin, w.root, warmXDG, nil, w.args()...)
		second := runWarm(t, bin, w.root, warmXDG, nil, w.args()...)
		for n, got := range []warmRun{first, second} {
			if got.code != ref.code || got.stdout != ref.stdout {
				fail("run %d: exit %d %q, the reference exit %d %q", n+1, got.code, got.stdout, ref.code, ref.stdout)
			}
		}
		work := second.counts["files_parsed"] + second.counts["go_compiles"] + second.counts["links"] + second.counts["processes"]
		switch {
		case first.code == 0 && !w.explore:
			if work != 0 {
				fail("an unchanged passing request did work: %v", second.counts)
			}
		case second.counts["files_parsed"] == 0:
			fail("a failing or explored request was answered warm: %v", second.counts)
		}
		// A request in a state no run has passed in (its sources, seed, run
		// name and explore flag) is never answered warm: its first run
		// parses.
		state := fmt.Sprint(w)
		if !passed[state] && first.counts["files_parsed"] == 0 {
			fail("a changed request was answered warm: %v", first.counts)
		}
		if first.code == 0 {
			passed[state] = true
		}
	}
}
