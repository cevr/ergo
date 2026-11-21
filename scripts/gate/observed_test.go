package main

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestObservedTimesMoveSlowTestsToTheFullTier is the size rule over the
// observed store (gate-speed GS33b), against a reference model, over
// seeded runs. Each run is fast or full: it selects with the store
// (observedRow, fastSelection), then observes a time (observeTests) for
// each test it ran, the fast selection's tests in a fast run and every
// test in a full run. Before every run a test is large exactly when the
// model's time is at least sizeCutoff: the largest of its last
// observedKeep observed times, else its recorded time, else none (it runs
// in the fast tier once and is observed). So a test leaves the fast tier on
// one time over the cutoff and comes back after observedKeep times under
// it, from full runs. A failing seed prints the replay. It catches
// gs33b-store-ignored, gs33b-keep-first and gs33b-last-time.
func TestObservedTimesMoveSlowTestsToTheFullTier(t *testing.T) {
	seed := uint64(time.Now().UnixNano())
	random := rand.New(rand.NewPCG(seed, seed^0x33b))
	const importPath = "example.test/observed"
	returned := 0
	for trial := range 300 {
		cacheDir := t.TempDir()
		var names []string
		recorded := map[string]float64{}
		for i := range 1 + random.IntN(12) {
			name := fmt.Sprintf("Test%d", i)
			names = append(names, name)
			switch random.IntN(3) {
			case 0:
				recorded[name] = math2(random.Float64() * 2 * sizeCutoff)
			case 1:
				recorded[name] = math2(random.Float64() * sizeCutoff)
			}
		}
		model := map[string][]float64{}
		wasLarge := map[string]bool{}
		for run := range 1 + random.IntN(12) {
			fast, large := fastSelection(names, observedRow(cacheDir, importPath, recorded))
			for _, name := range names {
				want := false
				if times := model[name]; len(times) > 0 {
					want = slices.Max(times) >= sizeCutoff
				} else if d, ok := recorded[name]; ok {
					want = d >= sizeCutoff
				}
				got := slices.Contains(large, name)
				if got != want || got == slices.Contains(fast, name) {
					t.Fatalf("seed %d trial %d run %d: %s large %t, want %t (observed %v, recorded %v)\nreplay: set seed to %d in TestObservedTimesMoveSlowTestsToTheFullTier", seed, trial, run, name, got, want, model[name], recorded, seed)
				}
				if wasLarge[name] && !got {
					returned++
				}
				wasLarge[name] = got
			}
			ran := fast
			if random.IntN(3) == 0 {
				ran = names
			}
			times := map[string]float64{}
			for _, name := range ran {
				s := math2(random.Float64() * sizeCutoff)
				if random.IntN(4) == 0 {
					s += sizeCutoff
				}
				times[name] = s
				kept := append(model[name], s)
				model[name] = kept[max(0, len(kept)-observedKeep):]
			}
			if err := observeTests(cacheDir, importPath, times); err != nil {
				t.Fatal(err)
			}
		}
	}
	if returned == 0 {
		t.Errorf("seed %d: no large test came back to the fast tier; the runs reach no return", seed)
	}
}

// TestAnObservedSlowTestLeavesTheFastTier: the tiers the gate declares
// observe and read the store. Each fast-tier test step and each full-tier
// package test step runs go test -json with Step.Observe set, so its times
// reach the store; and a test of the checkout the store has seen at
// sizeCutoff runs in no fast step of the next declaration, while it ran in
// one before. It catches gs33b-json-dropped, gs33b-observe-unset,
// gs33b-declared-from-seed and gs34-full-unobserved.
func TestAnObservedSlowTestLeavesTheFastTier(t *testing.T) {
	root := checkoutRoot(t)
	cacheDir := t.TempDir()
	module := modulePath(root)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	for _, step := range full {
		if !fullPackageStep.MatchString(step.Name) && step.Name != "go test (other packages)" {
			continue
		}
		observed++
		if !step.Observe || !slices.Contains(step.Argv, "-json") {
			t.Errorf("full tier %s: observe %t, argv %q; want it observed with -json", step.Name, step.Observe, step.Argv)
		}
	}
	if observed == 0 {
		t.Error("the full tier declares no package test step")
	}
	steps, _, err := fastStepsSelected(root, cacheDir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var probe *Step
	var test string
	for _, step := range steps {
		if step.Package == nil || step.Sweep != "" {
			continue
		}
		if !step.Observe || !slices.Contains(step.Argv, "-json") {
			t.Errorf("%s: observe %t, argv %q; want it observed with -json", step.Name, step.Observe, step.Argv)
		}
		if probe != nil {
			continue
		}
		names, err := testNames(filepath.Join(root, filepath.FromSlash(step.Package.Dir)))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			if runsIn(testSelection(step.Argv), name) {
				probe, test = step, name
				break
			}
		}
	}
	if probe == nil {
		t.Fatal("no fast step runs a test")
	}
	if err := observeTests(cacheDir, module+"/"+probe.Package.Dir, map[string]float64{test: sizeCutoff}); err != nil {
		t.Fatal(err)
	}
	steps, _, err = fastStepsSelected(root, cacheDir, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Package != nil && step.Package.Dir == probe.Package.Dir && runsIn(testSelection(step.Argv), test) {
			t.Errorf("%s observed at %.1fs still runs in %s: %q", test, sizeCutoff, step.Name, step.Argv)
		}
	}
}

// fullPackageStep names a full-tier step of one shard of a package's tests;
// a host package's steps ("(host, every run)") have no fast step to return
// to and are not observed.
var fullPackageStep = regexp.MustCompile(`^go test \./\S+ \[\d+/\d+\]$`)

// math2 rounds to the hundredths a time is kept at.
func math2(s float64) float64 { return float64(int(s*100)) / 100 }

// TestRenderedTestTextIsPlainGoTest: a fast step's -json output prints as
// plain go test prints it: the package's lines, a failed test's lines with
// its subtests', a test that never ended (a timeout, a crash) whole, and a
// line that is not JSON (stderr) as it is; no line of a passed or skipped
// test, and none that -json's implied -v adds. It catches
// gs33b-fail-output-dropped, gs33b-pass-output-shown,
// gs33b-unended-dropped and gs33b-verbose-lines-kept.
func TestRenderedTestTextIsPlainGoTest(t *testing.T) {
	event := func(action, test, output string) string {
		return fmt.Sprintf(`{"Time":"2026-11-15T21:00:00Z","Action":%q,"Package":"p","Test":%q,"Output":%q}`, action, test, output) + "\n"
	}
	stream := "# p\nbuild note on stderr\n" +
		event("start", "", "") +
		event("run", "TestPass", "") + event("output", "TestPass", "=== RUN   TestPass\n") +
		event("output", "TestPass", "    pass_test.go:3: passing log\n") +
		event("output", "TestPass", "--- PASS: TestPass (0.00s)\n") + event("pass", "TestPass", "") +
		event("run", "TestSkip", "") + event("output", "TestSkip", "=== RUN   TestSkip\n") +
		event("output", "TestSkip", "--- SKIP: TestSkip (0.00s)\n") + event("skip", "TestSkip", "") +
		event("run", "TestFail", "") + event("output", "TestFail", "=== RUN   TestFail\n") +
		event("run", "TestFail/sub", "") + event("output", "TestFail/sub", "=== RUN   TestFail/sub\n") +
		event("output", "TestFail/sub", "    fail_test.go:9: wrong\n") +
		event("output", "TestFail/sub", "    --- FAIL: TestFail/sub (0.00s)\n") + event("fail", "TestFail/sub", "") +
		event("output", "TestFail", "--- FAIL: TestFail (0.00s)\n") + event("fail", "TestFail", "") +
		event("run", "TestHang", "") + event("output", "TestHang", "=== RUN   TestHang\n") +
		event("output", "TestHang", "panic: test timed out after 1m0s\n") +
		event("output", "", "FAIL\n") + event("output", "", "FAIL\tp\t60.0s\n") + event("fail", "", "")
	var out strings.Builder
	if err := renderTestText(strings.NewReader(stream), &out); err != nil {
		t.Fatal(err)
	}
	want := "# p\nbuild note on stderr\n" +
		"    fail_test.go:9: wrong\n    --- FAIL: TestFail/sub (0.00s)\n--- FAIL: TestFail (0.00s)\n" +
		"FAIL\nFAIL\tp\t60.0s\n" +
		"panic: test timed out after 1m0s\n"
	if out.String() != want {
		t.Errorf("rendered\n%s\nwant\n%s", out.String(), want)
	}
}
