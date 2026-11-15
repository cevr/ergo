package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// cacheSeedVariable replays one seed of TestCachedVerdictEqualsRerun.
const cacheSeedVariable = "ERGO_EXAMPLES_CACHE_SEED"

// TestCachedVerdictEqualsRerun is the verdict cache's law: after any edit
// of what a program's verdict reads, a run with the cache gives the verdict
// a run without it gives: the exit status, every ok and FAIL line, the
// failure list and the case count. The edits are drawn from those that
// change a verdict: a program's main.ergo (its test or build fails, or it
// prints another line), a case's expected output, the compiler's bytes
// (bin/ergo swapped for one that fails every test, at the same path), an
// environment variable the compiler reads, and an unreadable file in a
// program's folder (no key: the program runs, and says it was not
// cached). Some steps edit nothing, so the cache replays; the test fails if
// no step replayed. A failing seed prints its edits and the replay
// variable. It kills the mutants of cache.go in the package ledger
// (gs1-key-*, gs1-record-failure, gs1-lookup-any-key).
func TestCachedVerdictEqualsRerun(t *testing.T) {
	seeds := []uint64{1}
	if s := os.Getenv(cacheSeedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { checkCacheLaw(t, seed) })
	}
}

// installExecutable writes an executable at path through a child process
// (install), so this process never holds a write descriptor of a file it
// runs (ETXTBSY, golang/go#22315; newFixture).
func installExecutable(t *testing.T, path, content string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("install", "-m", "755", src, path).CombinedOutput(); err != nil {
		t.Fatalf("install %s: %v\n%s", path, err, out)
	}
}

// pointTo makes link a symlink to target, replacing it in one rename.
func pointTo(t *testing.T, link, target string) {
	t.Helper()
	next := link + ".next"
	_ = os.Remove(next)
	if err := os.Symlink(target, next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, link); err != nil {
		t.Fatal(err)
	}
}

// verdictOf is what the law compares of a run.
func verdictOf(code int, out string) string {
	oks, fails := verdicts(out)
	var b strings.Builder
	fmt.Fprintf(&b, "exit %d\n", code)
	for _, k := range sortedKeys(boolsToStrings(oks)) {
		fmt.Fprintf(&b, "ok %s\n", k)
	}
	for _, k := range sortedKeys(fails) {
		fmt.Fprintf(&b, "FAIL %s:%s\n", k, fails[k])
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "examples: FAILED") || strings.HasPrefix(line, "examples: passed") || regexp.MustCompile(`^ok \S+: test`).MatchString(line) {
			fmt.Fprintf(&b, "%s\n", regexp.MustCompile(` \([0-9.]+s\)$`).ReplaceAllString(line, ""))
		}
	}
	return b.String()
}

func boolsToStrings(m map[string]bool) map[string]string {
	out := map[string]string{}
	for k := range m {
		out[k] = ""
	}
	return out
}

func checkCacheLaw(t *testing.T, seed uint64) {
	t.Setenv(cacheVariable, "")
	t.Setenv("FAKE_ERGO_TESTFAIL", "")
	r := rand.New(rand.NewPCG(seed, 0x6573))
	f := newFixture(t)
	fake, err := filepath.EvalSymlinks(f.ergo)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(f.ergo)
	passing, failing := filepath.Join(tools, "ergo-a"), filepath.Join(tools, "ergo-b")
	installExecutable(t, passing, fmt.Sprintf("#!/bin/sh\nFAKE_ERGO_TOOLS=%s exec %s \"$@\"\n", tools, fake))
	installExecutable(t, failing, fmt.Sprintf("#!/bin/sh\nFAKE_ERGO_TOOLS=%s FAKE_ERGO_TESTFAIL=1 exec %s \"$@\"\n", tools, fake))
	pointTo(t, f.ergo, passing)
	names := []string{"alpha", "beta", "gamma"}
	mains := map[string][]string{}
	for _, n := range names {
		mains[n] = []string{"#!/bin/sh", "echo " + n}
		f.program(n, strings.Join(mains[n], "\n")+"\n", "default\n", map[string]map[string]string{"basic": {"expected.out": n + "\n"}}, nil)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	timeout := caseTimeout(t)
	var edits []string
	replayed := 0
	// Each kind of edit first meets a recorded pass: the compiler, the
	// environment and the unreadable file are toggled from the passing
	// state and back, each after a step that records; then the edits of
	// the program's own files; then random edits.
	schedule := []int{0, 5, 5, 0, 6, 6, 0, 7, 7, 0, 2, 3, 4}
	for step := range len(schedule) + 4 {
		n := names[r.IntN(len(names))]
		dir := filepath.Join(f.root, "examples", n)
		edit := "none"
		k := r.IntN(8)
		if step < len(schedule) {
			k = schedule[step]
		}
		switch k {
		case 0, 1:
		case 2:
			line := []string{"# TESTFAIL", "# BUILDFAIL"}[r.IntN(2)]
			if i := indexOf(mains[n], line); i >= 0 {
				mains[n] = append(mains[n][:i], mains[n][i+1:]...)
				edit = n + ": drop " + line
			} else {
				mains[n] = append(mains[n], line)
				edit = n + ": add " + line
			}
			f.write("examples/"+n+"/main.ergo", strings.Join(mains[n], "\n")+"\n")
		case 3:
			text := []string{n, n + "!", "other"}[r.IntN(3)]
			mains[n][1] = "echo " + text
			f.write("examples/"+n+"/main.ergo", strings.Join(mains[n], "\n")+"\n")
			edit = n + ": prints " + text
		case 4:
			text := []string{n, n + "!", "other"}[r.IntN(3)]
			f.write("examples/"+n+"/cases/basic/expected.out", text+"\n")
			edit = n + ": expects " + text
		case 5:
			if target, _ := os.Readlink(f.ergo); target == passing {
				pointTo(t, f.ergo, failing)
				edit = "ergo: fails every test"
			} else {
				pointTo(t, f.ergo, passing)
				edit = "ergo: passes"
			}
		case 6:
			if os.Getenv("FAKE_ERGO_TESTFAIL") == "" {
				t.Setenv("FAKE_ERGO_TESTFAIL", "1")
				edit = "env: FAKE_ERGO_TESTFAIL=1"
			} else {
				t.Setenv("FAKE_ERGO_TESTFAIL", "")
				edit = "env: FAKE_ERGO_TESTFAIL="
			}
		case 7:
			readme := filepath.Join(dir, "README.md")
			info, err := os.Stat(readme)
			if err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o644)
			if info.Mode().Perm() == mode {
				mode = 0
			}
			if err := os.Chmod(readme, mode); err != nil {
				t.Fatal(err)
			}
			edit = fmt.Sprintf("%s: README.md mode %o", n, mode)
		}
		edits = append(edits, edit)
		// Every run has a new unit identifier, as each systemd-run does;
		// no verdict reads it, so it must not cost a replay (volatileEnv).
		t.Setenv("INVOCATION_ID", fmt.Sprint("step", step))
		// The settings are part of the key, so every run takes the same
		// ones: the test's time left, read once (caseTimeout), stops only a
		// hang.
		code, out := f.run("-timeout", timeout, "-budget", "1h", "-cache", cache)
		freshCode, freshOut := f.run("-timeout", timeout, "-budget", "1h")
		if got, want := verdictOf(code, out), verdictOf(freshCode, freshOut); got != want {
			t.Fatalf("seed %d step %d (replay: %s=%d go test -run TestCachedVerdictEqualsRerun ./scripts/examples)\nedits: %s\ncached run:\n%s\nrerun:\n%s\ncached output:\n%s",
				seed, step, cacheSeedVariable, seed, strings.Join(edits, "; "), got, want, out)
		}
		if m := regexp.MustCompile(`(\d+) programs replayed`).FindStringSubmatch(out); m != nil {
			n, _ := strconv.Atoi(m[1])
			replayed += n
		}
		for _, p := range names {
			readme := filepath.Join(f.root, "examples", p, "README.md")
			if info, err := os.Stat(readme); err == nil && info.Mode().Perm() == 0 && !strings.Contains(out, "examples: "+p+": not cached:") {
				t.Errorf("seed %d step %d: %s has an unreadable file and no key, but the run does not say it was not cached:\n%s", seed, step, p, out)
			}
		}
	}
	if replayed == 0 {
		t.Errorf("seed %d: no step replayed a program (edits: %s): the law held vacuously", seed, strings.Join(edits, "; "))
	}
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return -1
}
