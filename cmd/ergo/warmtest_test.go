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
	// recursive is the request `ergo test ./...`, which ignores run (lib
	// has no test of that name); extra adds the package more, which tests
	// lib when moreTests and has nothing to test otherwise.
	recursive, extra, moreTests bool
}

func (w verdictWorld) write(t *testing.T) {
	t.Helper()
	smokeWrite(t, w.root, "ergo.mod", "module verdict\n")
	smokeWrite(t, w.root, "lib/lib.ergo", fmt.Sprintf("// Package lib is the verdict tests' library.\n\n// Value is the library's number.\nfunc Value() int {\n\t%d\n}\n", w.value))
	smokeWrite(t, w.root, "app/app.ergo", fmt.Sprintf("import verdict/lib\n\n// Offset is the app's number.\nfunc Offset() int {\n\t%d\n}\n\ntest Bounded {\n\tassert lib.Value() + Offset() < %d\n}\n", w.offset, w.limit))
	if w.extra {
		more := "// Nine is nine.\nfunc Nine() int {\n\t9\n}\n"
		if w.moreTests {
			more = "import verdict/lib\n\ntest Below {\n\tassert lib.Value() < 9\n}\n"
		}
		smokeWrite(t, w.root, "more/more.ergo", more)
	}
}

func (w verdictWorld) args() []string {
	if w.recursive {
		args := []string{"test", "./...", "--seed", fmt.Sprint(w.seed)}
		if w.explore {
			args = append(args, "--explore")
		}
		return args
	}
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
// second `ergo test` of an unchanged package folder, or `ergo test ./...`
// of an unchanged module, whose first run passed is a warm answer. It prints the first run's output and the
// replay note, and every work counter is 0 (nothing parsed, checked,
// lowered, compiled, linked, started or written); the bytes read and
// hashed are the inputs' bytes, each read once. It kills
// cs-verdict-not-warm and cs-verdict-module-not-warm, which only these
// counts see, and cs-verdict-module-discovery-unrecorded: a package added
// after the warm answer is tested. It builds Go because
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
	for _, recursive := range []bool{false, true} {
		w.recursive = recursive
		cold := runWarm(t, bin, w.root, xdg, nil, w.args()...)
		warm := runWarm(t, bin, w.root, xdg, nil, w.args()...)
		if cold.code != 0 || warm.code != 0 || warm.stdout != cold.stdout || !strings.Contains(cold.stdout, "PASS") {
			t.Fatalf("%v: cold exit %d, warm exit %d; stdout %q, then %q", w.args(), cold.code, warm.code, cold.stdout, warm.stdout)
		}
		for k, v := range warm.counts {
			want := int64(0)
			switch k {
			case "files_read":
				want = 3
			case "bytes_read", "bytes_hashed":
				want = inputs
			case "obligations_stored":
				want = 1 // app's one test, answered by the stored verdict
			}
			if v != want {
				t.Errorf("warm %v: %s = %d, want %d", w.args(), k, v, want)
			}
		}
	}
	// A package added below the module is a changed request: the module
	// run discovers and tests it.
	w.extra, w.moreTests = true, true
	w.write(t)
	added := runWarm(t, bin, w.root, xdg, nil, w.args()...)
	if added.code != 0 || !strings.Contains(added.stdout, "package verdict/more") || added.counts["files_parsed"] == 0 {
		t.Fatalf("after adding a package: exit %d, %v; stdout %q", added.code, added.counts, added.stdout)
	}
}

// TestCachedVerdictAgrees is the model-based property of the warm verdict
// (models/warmstamp, as TestWarmAnswerAgreesWithFullBuild is for build and
// check): seeded sequences of edits, and after each one an `ergo test` of
// the app package, or `ergo test ./...` of the module, run twice on the
// warm path, against the reference, a
// fresh run (ERGO_BUILD=full --count 1 in a cache of its own). Each run's
// exit status and output are the reference's. The second run of a request
// whose first passed is a warm answer and does no work; a request that
// changed (an edit of the test, of the tested body or of the imported
// library's body, another --seed or --run, --explore, a package added to
// or removed from the module, with or without a test) or whose last run
// failed runs the normal path. A failing seed prints its steps; replay
// with ERGO_VERDICT_SEED=N.
//
// It kills cs-verdict-key-omits-seed, cs-verdict-key-omits-dep-body,
// cs-verdict-caches-failure-as-pass, cs-verdict-key-omits-explore-tag,
// cs-verdict-module-discovery-unrecorded, cs-verdict-module-records-failure
// and cs-verdict-module-empty-dropped.
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
	// prime runs the module request before a package is added or removed,
	// so the warm store holds its answer from before the edit.
	prime := func() {
		w.recursive = true
		runWarm(t, bin, w.root, warmXDG, nil, w.args()...)
	}
	kinds := r.Perm(9)
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
		case k == 6:
			w.recursive = !w.recursive
			step = fmt.Sprintf("test ./... %t", w.recursive)
		case k == 7 && w.extra:
			prime()
			w.extra = false
			step = "remove the package more"
			os.RemoveAll(filepath.Join(w.root, "more"))
		case k == 7:
			prime()
			w.extra, w.moreTests = true, r.IntN(2) == 0
			step = fmt.Sprintf("add the package more, with a test %t", w.moreTests)
			w.write(t)
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
			// A verdict the evaluator wrote is marked (TESTSPEED FT1); the
			// module's one test binary, which the reference may run,
			// writes the same line unmarked. A verdict read from the
			// verdict store is marked " (cached)" (FT2), as a law's is.
			unmark := strings.NewReplacer(" (evaluated)\n", "\n", " (cached)\n", "\n").Replace
			if got.code != ref.code || unmark(got.stdout) != unmark(ref.stdout) {
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
		state := fmt.Sprint(w.limit, w.offset, w.value, w.args())
		if w.recursive && w.extra {
			state += fmt.Sprint(" more ", w.moreTests)
		}
		if !passed[state] && first.counts["files_parsed"] == 0 {
			fail("a changed request was answered warm: %v", first.counts)
		}
		if first.code == 0 {
			passed[state] = true
		}
	}
}
