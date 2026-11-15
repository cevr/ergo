package main

import (
	"os"
	"path/filepath"
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
