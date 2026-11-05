package build

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdHelper is helper mode "hold": it builds baseInput into the cache at
// $ERGO_BUILD_TEST_ROOT, prints the module directory, and holds the module,
// never released, until its standard input ends, as a build whose link
// runs would.
func holdHelper() int {
	in := baseInput()
	module, err := Cache{Root: os.Getenv(rootVariable)}.Module(in, emitFor(in, nil))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(module.Dir)
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// trimCrashHelper is helper mode "trim-crash": it runs a due trim of the
// cache at $ERGO_BUILD_TEST_ROOT and exits with status 3 right after the
// trim renames the first directory aside, as a process killed between the
// rename and the removal would.
func trimCrashHelper() int {
	testHookTrimAside = func(string) { os.Exit(3) }
	Cache{Root: os.Getenv(rootVariable)}.maybeTrim(time.Now())
	return 0
}

// needLocks skips a test that needs flock.
func needLocks(t *testing.T) {
	t.Helper()
	if !leasesSupported {
		t.Skip("no flock here: nothing is trimmed")
	}
}

// publishBase publishes baseInput into root, releases it, and returns its
// directory.
func publishBase(t *testing.T, root string) string {
	t.Helper()
	in := baseInput()
	m, err := Cache{Root: root}.Module(in, emitFor(in, nil))
	if err != nil {
		t.Fatal(err)
	}
	m.Release()
	return m.Dir
}

// ageBy sets path's times d before now.
func ageBy(t *testing.T, path string, d time.Duration) {
	t.Helper()
	then := time.Now().Add(-d)
	if err := os.Chtimes(path, then, then); err != nil {
		t.Fatal(err)
	}
}

// writeTrimStamp writes Root/trim.txt for a complete trim that ended ago.
func writeTrimStamp(t *testing.T, root string, ago time.Duration) {
	t.Helper()
	s := stamp{completed: time.Now().Add(-ago).Unix()}
	if err := os.WriteFile(filepath.Join(root, trimStampName), s.bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readTrimStamp reads Root/trim.txt.
func readTrimStamp(t *testing.T, root string) stamp {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, trimStampName))
	if err != nil {
		t.Fatal(err)
	}
	return readStamp(data)
}

// dropTrimStamp makes a trim due from the start.
func dropTrimStamp(t *testing.T, root string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, trimStampName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func there(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// free reports whether no build holds the directory at path: the trim's
// exclusive lock is free.
func free(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return tryLockExclusive(f)
}

// openUnder lists the targets of this process's open descriptors that name
// root or a path inside it, from /proc/self/fd. It sees only the cache's
// own descriptors: one that another goroutine, an earlier test's dropped
// file or the runtime opens or closes elsewhere does not change the list.
func openUnder(t *testing.T, root string) []string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	var open []string
	for _, fd := range list {
		// A descriptor closed since ReadDir is gone, and was not root's:
		// the cache's calls have returned.
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err != nil {
			continue
		}
		if target == resolved || strings.HasPrefix(target, resolved+string(filepath.Separator)) {
			open = append(open, fd.Name()+" -> "+target)
		}
	}
	return open
}

// once makes a hook that runs fn on its first call only.
func once[T any](fn func(T)) func(T) {
	var o sync.Once
	return func(v T) { o.Do(func() { fn(v) }) }
}

// asides lists the directories a trim renamed aside into Root/staging.
func asides(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	for _, name := range entries(t, root, "staging") {
		if strings.HasPrefix(name, removingPrefix) {
			names = append(names, name)
		}
	}
	return names
}

// Required test 1 (spec): an entry a build in another process holds, from
// Module and without Release, survives a due trim although it looks unused
// for two days; once that process exits, the next due trim removes it.
func TestHeldEntrySurvivesTrim(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	c := Cache{Root: root}
	holder := helper(root, "hold")
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	holder.Stderr = os.Stderr
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	defer func() {
		if !done {
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	}()
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() {
		t.Fatalf("the holder printed nothing: %v", lines.Err())
	}
	dir := lines.Text()
	if dir != filepath.Join(root, "modules", mustVerify(t, root, baseInput()).Key) {
		t.Fatalf("the holder built %s", dir)
	}

	ageBy(t, dir, 2*unusedLimit)
	dropTrimStamp(t, root)
	c.maybeTrim(time.Now())
	if !there(filepath.Join(dir, ManifestName)) || !readTrimStamp(t, root).recent(time.Now()) {
		t.Fatalf("a due trim removed an entry another process holds (entry present %v)", there(dir))
	}
	mustVerify(t, root, baseInput())

	stdin.Close()
	if err := holder.Wait(); err != nil {
		t.Fatal(err)
	}
	done = true
	dropTrimStamp(t, root)
	c.maybeTrim(time.Now())
	if there(dir) {
		t.Fatal("the next due trim kept the entry after its holder exited")
	}
}

// Required test 2 (spec): the trim's window between its lock and its
// rename. A build that opens the entry in that window waits for the lock,
// finds that the path no longer names the directory it opened, and builds a
// whole entry. And when the path names another directory by the time the
// trim holds its lock (here a discard moved the old entry and a build
// published a new one, which it holds), the trim leaves the path alone.
func TestBuildArrivingAtATrim(t *testing.T) {
	needLocks(t)
	t.Cleanup(func() { testHookTrimLocked, testHookEntryOpened = nil, nil })
	base := baseInput()

	t.Run("a build that opens the entry waits and rebuilds", func(t *testing.T) {
		root := t.TempDir()
		c := Cache{Root: root}
		dir := publishBase(t, root)
		ageBy(t, dir, 2*unusedLimit)
		writeTrimStamp(t, root, 0)
		var calls atomic.Int32
		var built Module
		var buildErr error
		finished := make(chan struct{})
		opened := make(chan struct{})
		testHookEntryOpened = once(func(string) { close(opened) })
		testHookTrimLocked = once(func(string) {
			go func() {
				defer close(finished)
				built, buildErr = c.Module(base, emitFor(base, &calls))
			}()
			<-opened
		})
		p := c.trim(time.Now(), 0)
		<-finished
		testHookTrimLocked, testHookEntryOpened = nil, nil
		if buildErr != nil || p.removed != 1 {
			t.Fatalf("build error %v; the trim removed %d", buildErr, p.removed)
		}
		defer built.Release()
		if built.Reused || calls.Load() != 1 || built.Dir != dir {
			t.Fatalf("the arriving build: %+v after %d emissions; want a fresh module at %s", built, calls.Load(), dir)
		}
		mustVerify(t, root, base)
		if len(asides(t, root)) != 0 {
			t.Fatalf("the trim left %v", asides(t, root))
		}
	})

	t.Run("a trim whose path changed under its lock leaves it", func(t *testing.T) {
		root := t.TempDir()
		c := Cache{Root: root}
		dir := publishBase(t, root)
		ageBy(t, dir, 2*unusedLimit)
		writeTrimStamp(t, root, 0)
		var holder Module
		var holdErr error
		testHookTrimLocked = once(func(string) {
			if err := os.Rename(dir, filepath.Join(root, "moved")); err != nil {
				holdErr = err
				return
			}
			holder, holdErr = c.Module(base, emitFor(base, nil))
		})
		p := c.trim(time.Now(), 0)
		testHookTrimLocked = nil
		if holdErr != nil {
			t.Fatal(holdErr)
		}
		defer holder.Release()
		if p.removed != 0 || !there(dir) {
			t.Fatalf("the trim removed %d, entry present %v: it moved the new entry a build holds", p.removed, there(dir))
		}
		mustVerify(t, root, base)
	})
}

// Required test 3 (spec): a build opens an entry, and before it locks it a
// trim moves the entry and another builder publishes a new one at the same
// path. The build must open the path again and hold the new entry; it must
// never verify and use the new path while it holds the old directory's
// lock. So once it returns, a trim cannot take the new entry until it
// releases it.
func TestBuildThatLockedAMovedEntry(t *testing.T) {
	needLocks(t)
	t.Cleanup(func() { testHookEntryOpened = nil })
	root := t.TempDir()
	c := Cache{Root: root}
	base := baseInput()
	dir := publishBase(t, root)
	ageBy(t, dir, 2*unusedLimit)
	writeTrimStamp(t, root, 0)
	var calls atomic.Int32
	var removed int
	var otherErr error
	testHookEntryOpened = once(func(string) {
		removed = c.trim(time.Now(), 0).removed
		other, err := c.Module(base, emitFor(base, &calls))
		otherErr = err
		other.Release()
	})
	m, err := c.Module(base, emitFor(base, &calls))
	testHookEntryOpened = nil
	if err != nil || otherErr != nil || removed != 1 {
		t.Fatalf("build %v, other build %v, trim removed %d", err, otherErr, removed)
	}
	if !m.Reused || calls.Load() != 1 || m.Dir != dir {
		t.Fatalf("the build: %+v after %d emissions; want the other builder's entry reused", m, calls.Load())
	}
	ageBy(t, dir, 2*unusedLimit)
	c.trim(time.Now(), 0)
	if !there(dir) {
		m.Release()
		t.Fatal("a trim removed the entry the build returned: the build holds the lock of another directory")
	}
	mustVerify(t, root, base)
	m.Release()
	ageBy(t, dir, 2*unusedLimit)
	c.trim(time.Now(), 0)
	if there(dir) {
		t.Fatal("the released entry stayed")
	}
}

// Required test 4 (spec): a builder holds its staging directory from its
// first byte through the rename that publishes it. A due trim that runs
// while the staging directory looks two hours old leaves it, and the
// publication succeeds.
func TestStagingHeldThroughPublication(t *testing.T) {
	needLocks(t)
	t.Cleanup(func() { testHookStaged = nil })
	root := t.TempDir()
	c := Cache{Root: root}
	base := baseInput()
	var kept bool
	var p pass
	testHookStaged = func(staging string) error {
		ageBy(t, staging, 2*stagingLifetime)
		p = c.trim(time.Now(), 0)
		kept = there(filepath.Join(staging, ManifestName))
		return nil
	}
	m, err := c.Module(base, emitFor(base, nil))
	testHookStaged = nil
	if err != nil || !kept || p.removed != 0 {
		t.Fatalf("publication: %v; staging kept %v, trim removed %d", err, kept, p.removed)
	}
	defer m.Release()
	mustVerify(t, root, base)
}

// Required test 5 (spec): a trim killed between its rename aside and the
// removal leaves Root/modules/KEY naming nothing, never a half entry; the
// next build emits a whole entry; and a later due pass removes the aside.
func TestTrimCrashAfterRenameAside(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	c := Cache{Root: root}
	base := baseInput()
	dir := publishBase(t, root)
	ageBy(t, dir, 2*unusedLimit)
	dropTrimStamp(t, root)
	out, err := helper(root, "trim-crash").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("trim-crash helper: %v %s", err, out)
	}
	if there(dir) {
		t.Fatal("a trim killed after its rename aside left the entry's path naming a directory")
	}
	if got := asides(t, root); len(got) != 1 {
		t.Fatalf("the killed trim left %v aside, want one", got)
	}
	var calls atomic.Int32
	m, err := c.Module(base, emitFor(base, &calls))
	if err != nil || m.Reused || calls.Load() != 1 {
		t.Fatalf("after the killed trim: %+v %v, %d emissions", m, err, calls.Load())
	}
	m.Release()
	mustVerify(t, root, base)
	if p := c.trim(time.Now().Add(2*stagingLifetime), 0); !p.complete || len(asides(t, root)) != 0 {
		t.Fatalf("a later pass left %v aside", asides(t, root))
	}
	mustVerify(t, root, base)
}

// Required test 6 (spec): a hit marks its entry used at most once per
// markInterval; a miss never marks its fresh entry; a mark in the future
// counts as a use now; and a failed mark does not fail the build.
func TestUseMark(t *testing.T) {
	root := t.TempDir()
	c := Cache{Root: root}
	base := baseInput()
	var marks atomic.Int32
	var failing atomic.Bool
	markTimes = func(path string, a, m time.Time) error {
		marks.Add(1)
		if failing.Load() {
			return errors.New("read-only file system")
		}
		return os.Chtimes(path, a, m)
	}
	t.Cleanup(func() { markTimes = os.Chtimes })
	build := func() Module {
		t.Helper()
		m, err := c.Module(base, emitFor(base, nil))
		if err != nil {
			t.Fatal(err)
		}
		m.Release()
		return m
	}
	mtime := func(dir string) time.Time {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		return info.ModTime()
	}

	m := build()
	if m.Reused || marks.Load() != 0 {
		t.Fatalf("a miss marked its fresh entry %d times", marks.Load())
	}
	ageBy(t, m.Dir, markInterval+30*time.Minute)
	if build(); marks.Load() != 1 || time.Since(mtime(m.Dir)) > time.Minute {
		t.Fatalf("a hit on an entry marked 90 minutes ago: %d marks, mark %v ago", marks.Load(), time.Since(mtime(m.Dir)))
	}
	ageBy(t, m.Dir, markInterval/2)
	before := mtime(m.Dir)
	if build(); marks.Load() != 1 || !mtime(m.Dir).Equal(before) {
		t.Fatalf("a hit on an entry marked 30 minutes ago wrote a mark (%d marks)", marks.Load())
	}
	ageBy(t, m.Dir, -2*time.Hour)
	if build(); marks.Load() != 1 {
		t.Fatal("a hit on an entry marked in the future wrote a mark")
	}
	failing.Store(true)
	ageBy(t, m.Dir, 2*markInterval)
	if again := build(); !again.Reused || marks.Load() != 2 {
		t.Fatalf("a hit whose mark failed: %+v, %d marks", again, marks.Load())
	}
}

// Required test 7 (spec): Root/trim.txt decides when a trim is due. A
// missing, torn, foreign or future stamp makes it due; a recent one does
// not. The claim (the stamp's lock) keeps a second process out. The old
// Root/private/.swept decides nothing, and a trim removes it.
func TestTrimStamp(t *testing.T) {
	now := time.Now()
	for text, due := range map[string]bool{
		"":                            true, // missing
		"1":                           true, // torn
		fmt.Sprintf("%d", now.Unix()): true,
		"x 0":                         true, // foreign
		fmt.Sprintf("%d 0", now.Add(10*time.Minute).Unix()):  true, // future
		fmt.Sprintf("%d 0", now.Add(-2*trimInterval).Unix()): true,
		fmt.Sprintf("%d 0", now.Add(-30*time.Minute).Unix()): false,
		fmt.Sprintf("%d 12", now.Add(-time.Minute).Unix()):   false,
	} {
		if got := trimDue(true, readStamp([]byte(text)), now); got != due {
			t.Errorf("stamp %q: due %v, want %v", text, got, due)
		}
	}
	needLocks(t)

	t.Run("the claim", func(t *testing.T) {
		root := t.TempDir()
		c := Cache{Root: root}
		dir := publishBase(t, root)
		ageBy(t, dir, 2*unusedLimit)
		dropTrimStamp(t, root)
		claim, err := os.OpenFile(filepath.Join(root, trimStampName), os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if !tryLockExclusive(claim) {
			t.Fatal("the test could not claim the stamp")
		}
		c.maybeTrim(time.Now())
		claim.Close()
		if !there(dir) {
			t.Fatal("a trim ran while another process held the claim")
		}
		c.maybeTrim(time.Now())
		if there(dir) || !readTrimStamp(t, root).recent(time.Now()) {
			t.Fatalf("the due trim after the claim ended: entry present %v, stamp %+v", there(dir), readTrimStamp(t, root))
		}
	})

	t.Run("the old sweep stamp decides nothing", func(t *testing.T) {
		root := t.TempDir()
		c := Cache{Root: root}
		dir := publishBase(t, root)
		ageBy(t, dir, 2*unusedLimit)
		dropTrimStamp(t, root)
		legacy := filepath.Join(root, "private", legacyStampName)
		mustWrite(t, legacy, string(stamp{completed: time.Now().Unix()}.bytes()))
		c.maybeTrim(time.Now())
		if there(dir) || there(legacy) {
			t.Fatalf("with a recent %s and no trim.txt: entry present %v, %s present %v", legacyStampName, there(dir), legacyStampName, there(legacy))
		}
	})
}

// Required test 9 (spec): where flock does not exist, no trim is due, so
// nothing is removed (the build-tagged trim_other_test.go runs a build
// there).
func TestNoTrimWithoutLocks(t *testing.T) {
	now := time.Now()
	for _, s := range []stamp{{}, {completed: now.Add(-48 * time.Hour).Unix()}, {completed: now.Add(time.Hour).Unix(), cursor: 3}} {
		if trimDue(false, s, now) {
			t.Fatalf("a trim is due without locks for stamp %+v", s)
		}
		if !trimDue(true, s, now) {
			t.Fatalf("a trim is not due with locks for stamp %+v", s)
		}
	}
}

// TestTrimKeepsYoungAndRemovesOld: one complete pass over entries and
// staging aged on both sides of their limits removes exactly the old ones
// (the acceptance check's P1 and P2, in process).
func TestTrimKeepsYoungAndRemovesOld(t *testing.T) {
	needLocks(t)
	root := t.TempDir()
	c := Cache{Root: root}
	plant := func(sub, name string, age time.Duration) string {
		dir := filepath.Join(root, sub, name)
		mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
		ageBy(t, dir, age)
		return dir
	}
	want := map[string]bool{}
	for i, age := range []time.Duration{0, time.Hour, unusedLimit - 2*time.Minute, unusedLimit + 2*time.Minute, 3 * unusedLimit} {
		want[plant("modules", fmt.Sprintf("entry-%d", i), age)] = age < unusedLimit
	}
	for i, age := range []time.Duration{0, stagingLifetime - 2*time.Minute, stagingLifetime + 2*time.Minute, 3 * stagingLifetime} {
		want[plant("staging", fmt.Sprintf("key.%d", i), age)] = age < stagingLifetime
	}
	want[plant("staging", "key.discard.0011", 2*stagingLifetime)] = false
	if p := c.trim(time.Now(), 0); !p.complete {
		t.Fatalf("pass %+v", p)
	}
	for dir, kept := range want {
		if there(dir) != kept {
			t.Errorf("%s: present %v, want %v", dir, there(dir), kept)
		}
	}
	if len(asides(t, root)) != 0 {
		t.Fatalf("the pass left %v", asides(t, root))
	}
}

// TestEntryLockReleasedOnEveryPath (spec risk 4): every path of Module that
// returns a module holds exactly its directory's lock until Release, and
// every path that fails holds nothing. After each one, the trim's
// exclusive lock on the entry is free, and no descriptor of the cache stays
// open.
func TestEntryLockReleasedOnEveryPath(t *testing.T) {
	needLocks(t)
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd to count descriptors")
	}
	t.Cleanup(func() { testHookStaged = nil })
	// No collection runs while the test does, so a descriptor that a path
	// drops without closing stays open until openUnder sees it: a finalizer
	// never closes the leak first.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	root := t.TempDir()
	c := Cache{Root: root}
	base := baseInput()
	publishBase(t, root)
	check := func(name string, build func() (Module, error), succeeds bool) {
		t.Helper()
		m, err := build()
		if (err == nil) != succeeds {
			t.Fatalf("%s: error %v", name, err)
		}
		if succeeds {
			if free(t, m.Dir) {
				t.Fatalf("%s: the returned module is not held", name)
			}
			m.Release()
			m.Release()
			if !free(t, m.Dir) {
				t.Fatalf("%s: the module is still held after Release", name)
			}
		}
		if open := openUnder(t, root); len(open) != 0 {
			t.Fatalf("%s: descriptors still open in the cache: %v", name, open)
		}
	}
	if open := openUnder(t, root); len(open) != 0 {
		t.Fatalf("publishBase left descriptors open in the cache: %v", open)
	}
	check("hit", func() (Module, error) { return c.Module(base, emitFor(base, nil)) }, true)
	other := base
	other.Emitter = "test-emit/2"
	check("miss", func() (Module, error) { return c.Module(other, emitFor(other, nil)) }, true)
	check("corrupt then rebuilt", func() (Module, error) {
		appendTo(t, filepath.Join(root, "modules", mustVerify(t, root, base).Key, "main.go"), "// edited\n")
		return c.Module(base, emitFor(base, nil))
	}, true)
	lost := base
	lost.Emitter = "test-emit/3"
	check("lost race", func() (Module, error) {
		testHookStaged = func(string) error {
			testHookStaged = nil
			winner, err := c.Module(lost, emitFor(lost, nil))
			winner.Release()
			return err
		}
		return c.Module(lost, emitFor(lost, nil))
	}, true)
	failing := base
	failing.Emitter = "test-emit/4"
	check("failed emission", func() (Module, error) {
		return c.Module(failing, func() ([]File, error) { return nil, errors.New("emission failed") })
	}, false)
	check("panicking emission", func() (m Module, err error) {
		defer func() {
			if recover() != nil {
				err = errors.New("panicked")
			}
		}()
		return c.Module(failing, func() ([]File, error) { panic("emission panicked") })
	}, false)
	private := base
	private.Compiler = "process:" + strings.Repeat("e", 64)
	check("private miss", func() (Module, error) { return c.Module(private, emitFor(private, nil)) }, true)
	check("private hit", func() (Module, error) { return c.Module(private, emitFor(private, nil)) }, true)
}
