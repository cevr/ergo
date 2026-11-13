package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestEarlyCutoffKeysWhatTheCompilerChanges is the early cutoff's law
// (cache.go; gate-speed GS8): in a checkout with a driver to list, a
// cached run's verdict equals a fresh run's after each kind of compiler
// change, and a compiler change that reaches no leg replays. The fixture
// root is a Go module with a cmd/ergo and a scripts/examples package (the
// driver), and the compiler is a wrapper of the fake one. In turn it:
//   - records a pass with compiler a;
//   - swaps in a2, other bytes with a's behaviour: every program replays;
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
// gs8-build-emission-unkeyed, gs8-check-unkeyed, gs8-fmt-unkeyed and
// gs8-bytes-keyed (scripts/examples' ledger).
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
		installExecutable(t, filepath.Join(tools, "ergo-"+name), fmt.Sprintf("#!/bin/sh\nFAKE_ERGO_TOOLS=%s %s exec %s \"$@\"\n", tools, env, fake))
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
	}
}
