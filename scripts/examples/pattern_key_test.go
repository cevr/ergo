package main

import (
	"os"
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
	p := &program{dir: dir, module: true}
	targets := []string{filepath.Join(dir, "ast"), filepath.Join(dir, "cmd", "calc"), dir}
	emitted := func(paths ...string) []byte {
		var b strings.Builder
		for _, path := range paths {
			b.WriteString("package " + path + "\nemitted 00 linux/amd64\n")
		}
		return []byte(b.String())
	}
	if why := coveredPackages(p, targets, emitted("calc", "calc/ast", "calc/cmd/calc")); why != "" {
		t.Errorf("every target named: %s", why)
	}
	for name, out := range map[string][]byte{
		"a target missing": emitted("calc", "calc/ast"),
		"another package":  emitted("calc", "calc/ast", "calc/cmd/calc", "calc/extra"),
		"no package line":  []byte("emitted 00 linux/amd64\n"),
	} {
		if why := coveredPackages(p, targets, out); why == "" {
			t.Errorf("%s: the key was accepted", name)
		}
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
	if !slices.Contains(readCalls(t, f), "test --emitted "+filepath.Join(f.root, dir, "...")) {
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
