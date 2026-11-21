package toolchain

import (
	"bytes"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// ircDir is a test's root for patched compiles: a folder of its own whose
// read-only entries get their write bits back before the test's cleanup
// removes it.
func ircDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(path, 0o755)
			}
			return nil
		})
	})
	return dir
}

// entryOf is the entry folder of a patched GOROOT.
func entryOf(p Patched) string { return filepath.Dir(p.Root) }

// TestSharedEntryProvesItself: an entry is taken only when its manifest
// names the key and each file has its digest (once a process); a missing
// manifest or a changed file is a miss that rebuilds and replaces the
// entry, and the build says so; an entry is read-only. Mutants it alone
// catches: irc-digest-unchecked, irc-reads-without-manifest
// (internal/toolchain/testdata/mutants.json).
func TestSharedEntryProvesItself(t *testing.T) {
	var builds atomic.Int32
	var log bytes.Buffer
	c := PatchConfig{Cache: ircDir(t), Source: fakeRelease(t, "go1.27.0"), lock: plantedLock("go1.27.0", "B"), build: fakeBuild(&builds), Log: &log}
	p, err := Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	compile := filepath.Join(p.Root, "pkg", "tool", hostTools(), "compile"+exe())
	for _, path := range []string{compile, filepath.Join(entryOf(p), manifestName)} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o222 != 0 {
			t.Errorf("%s is writable after the rename: %v %v", path, info.Mode(), err)
		}
	}
	if info, err := os.Stat(entryOf(p)); err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Errorf("the entry folder is writable after the rename: %v", err)
	}
	for _, tc := range []struct {
		name  string
		spoil func(entry string) error
	}{
		{"a changed compile", func(entry string) error {
			os.Chmod(filepath.Dir(compile), 0o755)
			os.Chmod(compile, 0o755)
			return os.WriteFile(compile, []byte("not the compile"), 0o755)
		}},
		{"no manifest", func(entry string) error {
			os.Chmod(entry, 0o755)
			return os.Remove(filepath.Join(entry, manifestName))
		}},
	} {
		if err := tc.spoil(entryOf(p)); err != nil {
			t.Fatal(err)
		}
		verified.Delete(entryOf(p)) // another process: nothing checked yet
		before := builds.Load()
		log.Reset()
		again, err := Patch(c)
		if err != nil || again != p || builds.Load() != before+1 {
			t.Fatalf("%s: Patch %+v %v after %d builds, want a new build of %+v", tc.name, again, err, builds.Load()-before, p)
		}
		if !strings.Contains(log.String(), "ergo: replacing the IR compiler") {
			t.Errorf("%s: the replacement was not said: %q", tc.name, log.String())
		}
		if data, _ := os.ReadFile(compile); string(data) != "compile of a\nB\nc\n" {
			t.Errorf("%s: the compile is %q after the replacement", tc.name, data)
		}
	}
}

// TestSharedKeyHoldsTheBuildEnvironment: a variable the compile's build
// sees that no class names is in the key and named; a location is not; a
// variable the build sets itself is not. Mutant it alone catches:
// irc-key-drops-unknown-env.
func TestSharedKeyHoldsTheBuildEnvironment(t *testing.T) {
	source := fakeRelease(t, "go1.27.0")
	key := func(environ ...string) (string, []string) {
		in, unknown, err := ircInputs(source, "go1.27.0", "d", environ)
		if err != nil {
			t.Fatal(err)
		}
		return keyOf(in), unknown
	}
	base, none := key("HOME=/h")
	if len(none) != 0 {
		t.Errorf("unknown variables of a plain environment: %q", none)
	}
	if k, unknown := key("HOME=/h", "GO386=sse2"); k == base || !slices.Equal(unknown, []string{"GO386"}) {
		t.Errorf("GO386 changes no key (%v) or is not named: %q", k == base, unknown)
	}
	for _, kv := range []string{"GOCACHE=/c", "GOTMPDIR=/t", "GOMAXPROCS=3", "GOFLAGS=-x", "GOAMD64=v3", "GOENV=/e", "GOGC=400"} {
		if k, unknown := key("HOME=/h", kv); k != base || len(unknown) != 0 {
			t.Errorf("%s changes the key or is named: %q", kv, unknown)
		}
	}
}

// TestSharedRootKeepsTheUsedLast: under a seeded set of entries and last
// uses, evict keeps the entry it is told to and the n-1 others used last
// (an entry with no mark counts from its own time), and removes the rest.
// Mutant it alone catches: irc-evicts-current.
func TestSharedRootKeepsTheUsedLast(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		dir := ircDir(t)
		count, n := 1+r.IntN(10), 1+r.IntN(4)
		at := map[string]time.Time{}
		var names []string
		base := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
		for i := range count {
			name := "patched-" + strconv.Itoa(i)
			names = append(names, name)
			entry := filepath.Join(dir, name, "goroot")
			if err := os.MkdirAll(entry, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := readOnly(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			at[name] = base.Add(time.Duration(r.IntN(1000)) * time.Minute)
			if r.IntN(4) == 0 {
				os.Chtimes(filepath.Join(dir, name), at[name], at[name]) // no mark
				continue
			}
			os.MkdirAll(filepath.Join(dir, "used"), 0o755)
			mark := filepath.Join(dir, "used", name)
			os.WriteFile(mark, nil, 0o644)
			os.Chtimes(mark, at[name], at[name])
		}
		keep := names[r.IntN(count)]
		// The model: keep, then the others by newest use (name breaks a tie).
		others := slices.DeleteFunc(slices.Clone(names), func(s string) bool { return s == keep })
		slices.SortFunc(others, func(a, b string) int {
			if c := at[b].Compare(at[a]); c != 0 {
				return c
			}
			return strings.Compare(a, b)
		})
		want := append([]string{keep}, others[:min(len(others), n-1)]...)
		slices.Sort(want)
		if err := evict(dir, keep, n); err != nil {
			t.Fatal(err)
		}
		left, _ := filepath.Glob(filepath.Join(dir, "patched-*"))
		var got []string
		for _, l := range left {
			got = append(got, filepath.Base(l))
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: %d entries, keep %s, n %d: left %q, want %q", seed, count, keep, n, got, want)
		}
	}
}

// raceChild is the variable that makes this test binary one of the two
// processes of TestSharedRootRaceBuildsOnce.
const raceChild = "ERGO_TOOLCHAIN_RACE_CHILD"

// TestSharedRootRaceBuildsOnce: two processes that ask a cold root for one
// key end with one entry, built once, and both use it. The builder waits,
// inside its build, until the other process waits on the lock (a fifo the
// waiter opens), so the two overlap on every run. Mutant: irt-patch-no-lock
// (as TestPatchBuildsOnceForTwoBuilds, in one process); this is the two
// processes the q16 ruling asks for.
func TestSharedRootRaceBuildsOnce(t *testing.T) {
	if dir := os.Getenv(raceChild); dir != "" {
		raceChildMain(t, dir)
		return
	}
	if !locks {
		t.Skip("no install lock on this system")
	}
	dir := t.TempDir()
	cache := ircDir(t)
	source := fakeRelease(t, "go1.27.0")
	if err := syscall.Mkfifo(filepath.Join(dir, "waiting"), 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	var children []*exec.Cmd
	outs := make([]*bytes.Buffer, 2)
	for i := range 2 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSharedRootRaceBuildsOnce$", "-test.count=1")
		cmd.Env = append(os.Environ(), raceChild+"="+dir, "RACE_CACHE="+cache, "RACE_SOURCE="+source)
		outs[i] = &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = outs[i], outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
	}
	var roots []string
	for i, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d: %v\n%s", i, err, outs[i])
		}
		_, root, ok := strings.Cut(outs[i].String(), "root=")
		if !ok {
			t.Fatalf("child %d printed no root:\n%s", i, outs[i])
		}
		root, _, _ = strings.Cut(root, "\n")
		roots = append(roots, root)
	}
	builds, _ := os.ReadFile(filepath.Join(dir, "builds"))
	if roots[0] != roots[1] || strings.Count(string(builds), "\n") != 1 {
		t.Fatalf("roots %q, builds %q: want one build both use", roots, builds)
	}
	entries, _ := filepath.Glob(filepath.Join(cache, "toolchain", "*", "patched-*"))
	if len(entries) != 1 {
		t.Fatalf("entries %q, want one", entries)
	}
	in, _, err := ircInputs(source, "go1.27.0", plantedDigest(t), os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	if why := entryProblem(entries[0], keyOf(in)); why != "" {
		t.Fatalf("the entry does not prove itself: %s", why)
	}
}

// plantedDigest is the digest plantedLock("go1.27.0", "B") locks.
func plantedDigest(t *testing.T) string {
	t.Helper()
	p, err := portOf(plantedLock("go1.27.0", "B"), "go1.27.0")
	if err != nil {
		t.Fatal(err)
	}
	return p.digest
}

// raceChildMain is one process of the race: it builds, holding its build
// until the other waits (opens the fifo for writing), or it waits.
func raceChildMain(t *testing.T, dir string) {
	fifo := filepath.Join(dir, "waiting")
	var builds atomic.Int32
	inner := fakeBuild(&builds)
	c := PatchConfig{Cache: os.Getenv("RACE_CACHE"), Source: os.Getenv("RACE_SOURCE"), lock: plantedLock("go1.27.0", "B"),
		build: func(source, overlay, out string) error {
			f, err := os.OpenFile(filepath.Join(dir, "builds"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			f.WriteString("build\n")
			f.Close()
			// Blocks until the other process waits on the lock.
			w, err := os.OpenFile(fifo, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			w.Close()
			return inner(source, overlay, out)
		},
		waiting: func() {
			if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
				f.Close()
			}
		},
	}
	p, err := Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("root=" + p.Root + "\n")
}
