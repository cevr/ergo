package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPatternKeyCoversEveryTarget: a program of packages is keyed by one
// test --emitted over DIR/... (GS27), and the key holds only when the
// packages it names are the program's targets: one it misses would not
// change the key when its emitted Go does. It catches
// gs27-coverage-unchecked and gs27-coverage-subset.
func TestPatternKeyCoversEveryTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module calc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &program{dir: dir, module: true, main: filepath.Join(dir, "cmd", "calc")}
	targets := []string{filepath.Join(dir, "ast"), filepath.Join(dir, "cmd", "calc"), dir}
	emitted := func(paths ...string) []emittedLine {
		var out []emittedLine
		for _, path := range paths {
			out = append(out, emittedLine{Package: path, Test: "00", Build: "11"})
		}
		return out
	}
	if why := coveredPackages(p, targets, emitted("calc", "calc/ast", "calc/cmd/calc")); why != "" {
		t.Errorf("every target named: %s", why)
	}
	unbuilt := emitted("calc", "calc/ast", "calc/cmd/calc")
	unbuilt[2].Build = ""
	for name, out := range map[string][]emittedLine{
		"a target missing":   emitted("calc", "calc/ast"),
		"another package":    emitted("calc", "calc/ast", "calc/cmd/calc", "calc/extra"),
		"no package line":    nil,
		"main has no digest": unbuilt,
	} {
		if why := coveredPackages(p, targets, out); why == "" {
			t.Errorf("%s: the key was accepted", name)
		}
	}
}

// TestPatternKeyIsOneCall: a program of packages is keyed by one compiler
// call beside fmt, `test --emitted --json DIR/...`, and its check leg takes
// the call's envelopes, so a run that misses checks the module once
// (GS31b). Each digest of the line is keyed on its own, and the check's
// producer is not: in a checkout with a driver to list, a compiler with
// other bytes (another producer) replays, and one that emits another main,
// another test module, or fails the check, the rest alike, is not
// replayed. It catches gs31b-key-calls-three, gs31b-check-not-keyed,
// gs31b-build-digest-unkeyed, gs31b-test-digest-unkeyed and
// gs31b-producer-keyed, and that the test leg linked the modules the key
// hashed: gs32a-path-ignored.
//
// It runs go list in the fixture module, the driver's listing.
func TestPatternKeyIsOneCall(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go")
	}
	t.Setenv(cacheVariable, "")
	t.Setenv("FAKE_ERGO_PATTERN_EXTRA", "")
	t.Setenv("GOWORK", "off")
	f := newFixture(t)
	f.write("go.mod", "module example.test/pattern\n\ngo 1.24\n")
	f.write("cmd/ergo/main.go", "package main\n\nfunc main() {}\n")
	f.write("scripts/examples/main.go", "package main\n\nfunc main() {}\n")
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/tests.ergo", "test Walks {\n}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	fake, err := filepath.EvalSymlinks(f.ergo)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(f.ergo)
	for name, env := range map[string]string{
		"a":      "FAKE_ERGO_PRODUCER=a",
		"a2":     "FAKE_ERGO_PRODUCER=a2",
		"build":  "FAKE_ERGO_PRODUCER=a FAKE_ERGO_EMIT_BUILD=other",
		"test":   "FAKE_ERGO_PRODUCER=a FAKE_ERGO_EMIT_TEST=other",
		"check":  "FAKE_ERGO_PRODUCER=a FAKE_ERGO_CHECKFAIL=1",
		"shared": "FAKE_ERGO_PRODUCER=a FAKE_ERGO_EMIT_TEST=shared FAKE_ERGO_SHARED_TEST=1",
	} {
		installExecutable(t, filepath.Join(tools, "ergo-"+name), fmt.Sprintf("#!/bin/sh\nFAKE_ERGO_TOOLS=%s %s exec %s \"$@\"\n", tools, env, fake))
	}
	cache := filepath.Join(t.TempDir(), "cache")
	timeout := caseTimeout(t)
	run := func(compiler, want string) {
		t.Helper()
		pointTo(t, f.ergo, filepath.Join(tools, "ergo-"+compiler))
		_, out := f.run("-timeout", timeout, "-budget", "1h", "-cache", cache)
		if !strings.Contains(out, want) {
			t.Fatalf("compiler %s: want %q\n%s", compiler, want, out)
		}
	}
	run("a", "0 programs replayed, 1 run")
	pattern := filepath.Join(f.root, dir, "...")
	counts := map[string]int{}
	for _, c := range readCalls(t, f) {
		counts[c]++
	}
	if n := counts["test --emitted --json "+pattern]; n != 1 {
		t.Errorf("the key made test --emitted --json %d times; want 1: %q", n, readCalls(t, f))
	}
	for _, other := range []string{"check " + pattern, "test --emitted " + pattern, "build --emitted " + filepath.Join(f.root, dir, "cmd", "walker")} {
		if counts[other] != 0 {
			t.Errorf("a missed run made ergo %s %d times; want none (the key's one call holds it): %q", other, counts[other], readCalls(t, f))
		}
	}
	checkLawCacheOff(t, f)
	run("a2", "1 programs replayed, 0 run")
	run("build", "0 programs replayed, 1 run")
	run("test", "0 programs replayed, 1 run")
	run("check", "0 programs replayed, 1 run")
	// A test leg that linked the shared test module ran Go the key does not
	// hold: the program fails, and is not kept (GS32a′).
	run("shared", "FAIL walker: test: ergo test linked 1 shared test module; its key holds each package's own test module")
	run("shared", "0 programs replayed, 1 run")
}

// TestToolEnvironmentKeepsTheKeyedTestPath: no compiler call gets the
// caller's ERGO_BUILD, whose "full" makes `ergo test DIR/...` link the
// shared test module where the key hashed each package's own, nor the
// caller's stage report (GS32a′). It catches gs32a-warm-variable-kept.
func TestToolEnvironmentKeepsTheKeyedTestPath(t *testing.T) {
	env := toolEnvironment([]string{"ERGO_BUILD=full", "ERGO_STAGE_REPORT=/nonexistent/r.json", "ERGO_CACHE=/nonexistent/c", "KEEP=1"}, "/nonexistent/scratch")
	want := []string{"KEEP=1", "ERGO_CACHE=" + filepath.Join("/nonexistent/scratch", "cache")}
	if !slices.Equal(env, want) {
		t.Errorf("toolEnvironment = %q, want %q", env, want)
	}
}

// TestPatternReachingAnotherPackageIsNotCached: a program of packages whose
// DIR/... names a package that is not one of its targets runs, is not
// replayed, and says why; the same program replays while the pattern names
// exactly its targets (GS27). It catches gs27-coverage-unchecked.
func TestPatternReachingAnotherPackageIsNotCached(t *testing.T) {
	t.Setenv(cacheVariable, "")
	t.Setenv("FAKE_ERGO_PATTERN_EXTRA", "")
	t.Setenv("GOWORK", "off")
	f := newFixture(t)
	// A root with a driver to list, so the key is the early cutoff's.
	f.write("go.mod", "module example.test/pattern\n\ngo 1.24\n")
	f.write("cmd/ergo/main.go", "package main\n\nfunc main() {}\n")
	f.write("scripts/examples/main.go", "package main\n\nfunc main() {}\n")
	dir := "examples/walker"
	f.write(dir+"/ergo.mod", "module walker\n")
	f.write(dir+"/README.md", "# walker\n")
	f.write(dir+"/tags", "default\n")
	f.write(dir+"/cmd/walker/main.ergo", "#!/bin/sh\necho hi\n")
	f.write(dir+"/walk/tests.ergo", "test Walks {\n}\n")
	f.write(dir+"/frame/frame.ergo", "func g() {}\n")
	f.write(dir+"/cases/basic/expected.out", "hi\n")
	cache := filepath.Join(t.TempDir(), "cache")
	timeout := caseTimeout(t)
	run := func(want string) string {
		t.Helper()
		code, out := f.run("-timeout", timeout, "-budget", "1h", "-cache", cache)
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("exit %d; want %q\n%s", code, want, out)
		}
		return out
	}
	run("0 programs replayed, 1 run")
	if !slices.Contains(readCalls(t, f), "test --emitted --json "+filepath.Join(f.root, dir, "...")) {
		t.Fatalf("the key made no pattern call: %q", readCalls(t, f))
	}
	run("1 programs replayed, 0 run")
	t.Setenv("FAKE_ERGO_PATTERN_EXTRA", "extra")
	out := run("0 programs replayed, 1 run")
	if !strings.Contains(out, "walker: not cached: ergo test --emitted over the program named packages") {
		t.Errorf("no reason the program was not cached\n%s", out)
	}
}

// TestPatternCheckKeyDropsEveryProducer: a pattern's check prints one
// envelope per package, and the key drops the producer of each, as it does
// for one package's envelope; otherwise every compiler edit would miss
// every program of packages (GS27). It catches gs27-stream-kept-whole.
func TestPatternCheckKeyDropsEveryProducer(t *testing.T) {
	stream := func(producer string) []byte {
		one := `{"checked":true,"producer":"` + producer + `","snapshot":{"producer":"` + producer + `","uri":"u"}}`
		return []byte(one + "\n" + one + "\n")
	}
	a, b := withoutProducer(stream("a")), withoutProducer(stream("b"))
	if string(a) != string(b) || strings.Contains(string(a), `"producer"`) {
		t.Errorf("stripped streams:\n%s\n%s", a, b)
	}
	one := withoutProducer([]byte(`{"checked":true,"producer":"a"}`))
	if string(one) != `{"checked":true}` {
		t.Errorf("one envelope: %s", one)
	}
	if text := []byte("check failed\n"); string(withoutProducer(text)) != string(text) {
		t.Errorf("text is kept whole: %s", withoutProducer(text))
	}
}
