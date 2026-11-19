package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/cases"
)

// TestEarlyCutoffKeysWhatTheCompilerChanges is the early cutoff's law
// (cache.go; gate-speed GS8): in a checkout with a driver to list, a
// cached run's verdict equals a fresh run's after each kind of compiler
// change, and a compiler change that reaches no leg replays. The fixture
// root is a Go module with a cmd/ergo and a scripts/examples package (the
// driver), and the compiler is a wrapper of the fake one. In turn it:
//   - records a pass with compiler a;
//   - swaps in a2, other bytes with a's behaviour (its check envelope
//     names another producer): every program replays;
//   - swaps in b, which fails every test, with an edit of cmd/ergo (b is
//     built from other driver sources): the verdict is a failure;
//   - restores a and the driver: every program replays;
//   - swaps in c, which emits another test binary and fails every test;
//   - swaps in f, which emits another main and fails every build;
//   - swaps in d, which fails every check;
//   - swaps in e, which fails every fmt --check.
//
// Mutants it catches, which TestCachedVerdictEqualsRerun does not (its
// fixture has no driver, so it keys the compiler's bytes):
// gs8-driver-unkeyed, gs8-test-emission-unkeyed,
// gs8-build-emission-unkeyed, gs8-check-unkeyed, gs8-fmt-unkeyed,
// gs8-bytes-keyed, gs8-producer-keyed and gs8-snapshot-producer-keyed
// (scripts/examples' ledger).
//
// It runs go list in the fixture module, the driver's listing.
func TestEarlyCutoffKeysWhatTheCompilerChanges(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	t.Setenv(cacheVariable, "")
	t.Setenv("FAKE_ERGO_TESTFAIL", "")
	t.Setenv("GOWORK", "off")
	f := newFixture(t)
	f.write("go.mod", "module example.test/cutoff\n\ngo 1.24\n")
	driver := func(text string) { f.write("cmd/ergo/main.go", "package main\n\n// "+text+"\nfunc main() {}\n") }
	driver("driver one")
	f.write("scripts/examples/main.go", "package main\n\nfunc main() {}\n")
	fake, err := filepath.EvalSymlinks(f.ergo)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(f.ergo)
	compilers := map[string]string{
		"a":  "",
		"a2": "FAKE_ERGO_OTHER_BYTES=1",
		"b":  "FAKE_ERGO_TESTFAIL=1",
		"c":  "FAKE_ERGO_EMIT_TEST=c FAKE_ERGO_TESTFAIL=1",
		"f":  "FAKE_ERGO_EMIT_BUILD=f FAKE_ERGO_BUILDFAIL=1",
		"d":  "FAKE_ERGO_CHECKFAIL=1",
		"e":  "FAKE_ERGO_FMTFAIL=1",
	}
	for name, env := range compilers {
		// Each compiler's check envelope names its own bytes, as ergo
		// check's producer does.
		installExecutable(t, filepath.Join(tools, "ergo-"+name), fmt.Sprintf("#!/bin/sh\nFAKE_ERGO_TOOLS=%s FAKE_ERGO_PRODUCER=%s %s exec %s \"$@\"\n", tools, name, env, fake))
	}
	names := []string{"alpha", "beta"}
	for _, n := range names {
		f.program(n, "#!/bin/sh\necho "+n+"\n", "default\n", map[string]map[string]string{"basic": {"expected.out": n + "\n"}}, nil)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	timeout := caseTimeout(t)
	steps := []struct {
		compiler, driver string
		replayed         int // -1: not counted
		fails            bool
	}{
		{"a", "driver one", 0, false},
		{"a2", "driver one", len(names), false},
		{"b", "driver two", -1, true},
		{"a", "driver one", len(names), false},
		{"c", "driver one", -1, true},
		{"f", "driver one", -1, true},
		{"d", "driver one", -1, true},
		{"e", "driver one", -1, true},
	}
	for i, s := range steps {
		pointTo(t, f.ergo, filepath.Join(tools, "ergo-"+s.compiler))
		driver(s.driver)
		code, out := f.run("-timeout", timeout, "-budget", "1h", "-cache", cache)
		freshCode, freshOut := f.run("-timeout", timeout, "-budget", "1h")
		if got, want := verdictOf(code, out), verdictOf(freshCode, freshOut); got != want {
			t.Fatalf("step %d (compiler %s, %s): cached run:\n%s\nrerun:\n%s\ncached output:\n%s", i, s.compiler, s.driver, got, want, out)
		}
		if (freshCode != 0) != s.fails {
			t.Fatalf("step %d (compiler %s): the fixture's fresh run exits %d\n%s", i, s.compiler, freshCode, freshOut)
		}
		m := regexp.MustCompile(`(\d+) programs replayed`).FindStringSubmatch(out)
		if m == nil {
			t.Fatalf("step %d: no cache line\n%s", i, out)
		}
		if n, _ := strconv.Atoi(m[1]); s.replayed >= 0 && n != s.replayed {
			t.Fatalf("step %d (compiler %s, %s): %d programs replayed, want %d\n%s", i, s.compiler, s.driver, n, s.replayed, out)
		}
		if i == 0 {
			checkKeyCallsRunOnce(t, readCalls(t, f))
			checkLawCacheOff(t, f)
		}
	}
}

// checkLawCacheOff: every check the runner made, the key's included, ran
// with the law-verdict store off, so none stored a verdict the native test
// leg would print cached (lawCacheOff, GS30). It catches
// gs30-key-check-stores.
func checkLawCacheOff(t *testing.T, f *fixture) {
	t.Helper()
	content, _ := read(t, filepath.Join(filepath.Dir(f.calls), "lawcache.log"))
	got := cases.Lines([]byte(content))
	if len(got) == 0 {
		t.Fatal("no check call logged its law-verdict store")
	}
	for _, v := range got {
		if v != "off" {
			t.Fatalf("a check ran with ERGO_LAWCACHE=%s; want off: %q", v, got)
		}
	}
}

// checkKeyCallsRunOnce counts the fmt and check calls of the first step: a
// missed program's key runs them and its legs take the key's results
// (runner.keyed, GS20; the check with the law-verdict store off, GS30), so
// the cached run makes each call once, as the fresh run does: twice in
// all. It catches gs20-key-results-dropped and gs30-check-leg-own.
func checkKeyCallsRunOnce(t *testing.T, calls []string) {
	t.Helper()
	counts := map[string]int{}
	for _, c := range calls {
		if strings.HasPrefix(c, "fmt ") || strings.HasPrefix(c, "check ") {
			counts[c]++
		}
	}
	if len(counts) == 0 {
		t.Fatalf("no fmt or check call in %q", calls)
	}
	for c, n := range counts {
		if n != 2 {
			t.Errorf("ergo %s ran %d times in a cached and a fresh run of a miss; want 2 (the legs take the key's result)", c, n)
		}
	}
}
