package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTestTakesSeveralTargets runs `ergo test B A` in process over two
// packages that build nothing: B does not check, A has nothing to test.
// Each prints under its own package line, B's failure does not stop A,
// and the run exits 1; a pattern among the targets tests each package
// below it. A target named twice is a usage error. Mutant
// test-many-stops-at-a-failure (internal/sketch/lower/testdata/mutants.json).
func TestTestTakesSeveralTargets(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"ergo.mod":   "module m\n",
		"a/a.ergo":   "func Double(n int) int { n * 2 }\n",
		"b/b.ergo":   "func Broken() int { \"text\" }\n",
		"c/c.ergo":   "func Triple(n int) int { n * 3 }\n",
		"c/d/d.ergo": "func Four() int { \"four\" }\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	stdout, err := testCommandOutput(t, []string{b, a, "--count", "1"})
	var status exitStatusError
	if !errors.As(err, &status) || status.code != 1 {
		t.Fatalf("ergo test B A: %v, want exit 1", err)
	}
	if want := "package " + b + "\npackage " + a + "\nPASS (0 tests)\n"; stdout != want {
		t.Fatalf("ergo test B A printed %q, want %q", stdout, want)
	}
	// c/d does not check, so the pattern links no test binary.
	stdout, err = testCommandOutput(t, []string{a, filepath.Join(dir, "c", "..."), "--count", "1"})
	want := "package " + a + "\nPASS (0 tests)\npackage m/c\nPASS (0 tests)\npackage m/c/d\n"
	if !errors.As(err, &status) || status.code != 1 || stdout != want {
		t.Fatalf("ergo test A C/...: %v %q, want exit 1 and %q", err, stdout, want)
	}
	if _, err := fileOptionsOf("test", []string{a, a}); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("ergo test A A: %v, want a target named twice", err)
	}
}

// TestCheckTakesSeveralTargets runs `ergo check B A C/...` in process: B
// does not check, A and the packages below C do. Each target prints its
// own check envelopes in the order given (a pattern one per package), B's
// failure does not stop the rest, and the run exits 1. Mutant
// check-many-stops-at-a-failure (internal/sketch/lower/testdata/mutants.json).
func TestCheckTakesSeveralTargets(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"ergo.mod":   "module m\n",
		"a/a.ergo":   "func Double(n int) int { n * 2 }\n",
		"b/b.ergo":   "func Broken() int { \"text\" }\n",
		"c/c.ergo":   "func Triple(n int) int { n * 3 }\n",
		"c/d/d.ergo": "func Four() int { 4 }\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	stdout, err := commandOutput(t, checkCommand, []string{b, a, filepath.Join(dir, "c", "...")})
	var status exitStatusError
	if !errors.As(err, &status) || status.code != 1 {
		t.Fatalf("ergo check B A C/...: %v, want exit 1", err)
	}
	var checked []bool
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var envelope struct{ Checked bool }
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("ergo check B A C/... line %q: %v", line, err)
		}
		checked = append(checked, envelope.Checked)
	}
	if want := []bool{false, true, true, true}; !slices.Equal(checked, want) {
		t.Fatalf("ergo check B A C/... envelopes checked %v, want %v:\n%s", checked, want, stdout)
	}
	if _, err := fileOptionsOf("check", []string{a, a}); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("ergo check A A: %v, want a target named twice", err)
	}
}

// testCommandOutput is what testCommand prints on standard output for
// args, with standard error discarded.
func testCommandOutput(t *testing.T, args []string) (string, error) {
	t.Helper()
	return commandOutput(t, testCommand, args)
}

// commandOutput is what run prints on standard output for args, with
// standard error discarded.
func commandOutput(t *testing.T, run func([]string) error, args []string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	saved, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, null
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	err = run(args)
	os.Stdout, os.Stderr = saved, savedErr
	w.Close()
	return <-done, err
}
