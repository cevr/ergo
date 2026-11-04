package build

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sweepStallCompiler is the process-scoped identity of the sweep-stall
// helper.
var sweepStallCompiler = "process:" + strings.Repeat("c", 64)

// sweepStallHelper is helper mode "sweep-stall": it builds baseInput under
// sweepStallCompiler into the cache at $ERGO_BUILD_TEST_ROOT, and when its
// sweep has renamed the first root aside it prints "collecting" and blocks
// until it is killed, as a process killed during a sweep pass would.
func sweepStallHelper() int {
	testHookCollecting = func(string) {
		fmt.Println("collecting")
		select {}
	}
	in := baseInput()
	in.Compiler = sweepStallCompiler
	if _, err := (Cache{Root: os.Getenv(rootVariable)}).Module(in, emitFor(in, nil)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// sweepFixture is a cache with a process-scoped build and helpers to plant
// roots and read the stamp.
type sweepFixture struct {
	t    *testing.T
	root string
	c    Cache
	in   Input
}

func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	if !leasesSupported {
		t.Skip("no flock here")
	}
	root := t.TempDir()
	in := baseInput()
	in.Compiler = "process:" + strings.Repeat("a", 64)
	return &sweepFixture{t: t, root: root, c: Cache{Root: root}, in: in}
}

// build is one build of the fixture's input, released after it returns.
func (f *sweepFixture) build() {
	f.t.Helper()
	m, err := f.c.Module(f.in, emitFor(f.in, nil))
	if err != nil {
		f.t.Fatal(err)
	}
	m.Release()
}

// plant makes n private roots named prefix-NNNN, each aged past
// privateLifetime when old is set and just used otherwise.
func (f *sweepFixture) plant(prefix string, n int, old bool) []string {
	f.t.Helper()
	var roots []string
	for i := range n {
		r := filepath.Join(f.root, "private", fmt.Sprintf("%s-%04d", prefix, i))
		if err := os.MkdirAll(filepath.Join(r, "modules"), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if old {
			age(f.t, r)
		}
		roots = append(roots, r)
	}
	return roots
}

// present counts the roots that still exist.
func present(roots []string) int {
	n := 0
	for _, r := range roots {
		if _, err := os.Stat(r); err == nil {
			n++
		}
	}
	return n
}

func (f *sweepFixture) stampPath() string { return filepath.Join(f.root, trimStampName) }

// stamp reads the stamp file.
func (f *sweepFixture) stamp() stamp {
	f.t.Helper()
	data, err := os.ReadFile(f.stampPath())
	if err != nil {
		f.t.Fatal(err)
	}
	return readStamp(data)
}

// due makes the sweep due from the start: the last complete sweep ended
// more than trimInterval ago.
func (f *sweepFixture) due() {
	f.t.Helper()
	old := stamp{completed: time.Now().Add(-trimInterval - time.Hour).Unix()}
	if err := os.WriteFile(f.stampPath(), old.bytes(), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// leftovers lists the roots a sweep renamed aside and did not finish.
func (f *sweepFixture) leftovers() []string {
	f.t.Helper()
	var aside []string
	for _, name := range entries(f.t, f.root, "private") {
		if strings.HasPrefix(name, removingPrefix) {
			aside = append(aside, name)
		}
	}
	return aside
}

// TestSweepIsRateLimited (round 2 review O1, round 3 review M1): a complete
// sweep starts at most once per trimInterval per cache. A recent stamp
// means no pass, so an aged, ownerless root stays; a due stamp runs a pass,
// which removes it and records the sweep complete; a pass another process
// has claimed (it holds the stamp's lock) is not run twice; and a pass that
// hits its removal bound leaves the sweep due, so the next build, with no
// help, continues it.
func TestSweepIsRateLimited(t *testing.T) {
	f := newSweepFixture(t)
	f.build() // the first build in a cache sweeps and records it complete
	if s := f.stamp(); !s.recent(time.Now()) || s.cursor != 0 {
		t.Fatalf("after the first build: stamp %+v, want a recent complete sweep", s)
	}

	roots := f.plant("abandoned", 1, true)
	f.build()
	if present(roots) != 1 {
		t.Fatal("a build with a recent stamp swept")
	}

	f.due()
	claim, err := os.OpenFile(f.stampPath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !tryLockExclusive(claim) {
		t.Fatal("the test could not claim the stamp")
	}
	f.build()
	claim.Close()
	if present(roots) != 1 {
		t.Fatal("a build swept while another process held the claim")
	}

	f.build()
	if present(roots) != 0 || !f.stamp().recent(time.Now()) {
		t.Fatalf("a build with a due stamp: %d roots left, stamp %+v; want none, complete", present(roots), f.stamp())
	}

	many := f.plant("many", sweepRemovals+5, true)
	f.due()
	f.build()
	if left := present(many); left != 5 || f.stamp().recent(time.Now()) {
		t.Fatalf("one pass left %d of %d roots, stamp %+v; want 5 left (at most %d removals) and the sweep still due", left, len(many), f.stamp(), sweepRemovals)
	}
	f.build()
	if left := present(many); left != 0 || !f.stamp().recent(time.Now()) {
		t.Fatalf("the next build left %d roots, stamp %+v; want none, complete", left, f.stamp())
	}
}

// TestSweepCatchesUpWithABacklog (round 3 review M1): a cache holding three
// passes' worth of aged, ownerless roots behind more young roots than one
// pass examines reaches zero aged roots within a bounded number of builds on
// the same day, each pass removing at most sweepRemovals; young roots stay;
// and once the sweep completes, a recent stamp again means no scan.
func TestSweepCatchesUpWithABacklog(t *testing.T) {
	f := newSweepFixture(t)
	f.build()
	young := f.plant("young", sweepEntries+76, false)
	aged := f.plant("aged", 3*sweepRemovals, true)
	f.due()
	// Each pass removes up to sweepRemovals or examines up to sweepEntries:
	// three passes for removals and two for the names bound the work.
	const bound = 3 + 2 + 1
	builds := 0
	for builds < bound && !f.stamp().recent(time.Now()) {
		before := present(aged)
		f.build()
		builds++
		if removed := before - present(aged); removed > sweepRemovals {
			t.Fatalf("build %d removed %d roots, more than one pass's %d", builds, removed, sweepRemovals)
		}
	}
	t.Logf("the backlog cleared in %d builds", builds)
	if left := present(aged); left != 0 || !f.stamp().recent(time.Now()) {
		t.Fatalf("after %d builds: %d aged roots left, stamp %+v; want none and a complete sweep", builds, left, f.stamp())
	}
	if left := present(young); left != len(young) {
		t.Fatalf("the sweep removed %d young roots", len(young)-left)
	}
	late := f.plant("late", 1, true)
	f.build()
	if present(late) != 1 {
		t.Fatal("a build swept although the sweep had just completed")
	}

	// Published entries and staging share the trim, its stamp and its
	// bounds (required test 8): a backlog of three passes' worth of
	// unused entries behind more young entries than one pass examines
	// drains over several builds, each pass within its bounds, and the
	// stamp records the trim complete only at the end.
	plantEntries := func(prefix string, n int, age time.Duration) []string {
		var dirs []string
		for i := range n {
			dir := filepath.Join(f.root, "modules", fmt.Sprintf("%s-%04d", prefix, i))
			if err := os.MkdirAll(filepath.Join(dir, "rt"), 0o755); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, "rt", "rt.go"), "package rt\n")
			then := time.Now().Add(-age)
			if err := os.Chtimes(dir, then, then); err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, dir)
		}
		return dirs
	}
	youngEntries := plantEntries("young", sweepEntries+76, time.Hour)
	unused := plantEntries("unused", 3*sweepRemovals+10, 2*unusedLimit)
	if p := f.c.trim(time.Now(), 0); p.complete || p.examined > sweepEntries || p.removed != sweepRemovals {
		t.Fatalf("one pass over the backlog: %+v; want an unfinished pass within %d names and %d removals", p, sweepEntries, sweepRemovals)
	}
	f.due()
	builds = 0
	// About 2400 names (young private roots and entries) and 138 removals:
	// three passes for the removals and three for the names bound the work.
	for builds < 3+3+2 && !f.stamp().recent(time.Now()) {
		before := present(unused)
		f.build()
		builds++
		if removed := before - present(unused); removed > sweepRemovals {
			t.Fatalf("build %d removed %d entries, more than one pass's %d", builds, removed, sweepRemovals)
		}
		if present(unused) > 0 && f.stamp().recent(time.Now()) {
			t.Fatalf("build %d recorded the trim complete with %d unused entries left", builds, present(unused))
		}
	}
	t.Logf("the entry backlog cleared in %d builds", builds)
	if left := present(unused); left != 0 || !f.stamp().recent(time.Now()) {
		t.Fatalf("after %d builds: %d unused entries left, stamp %+v; want none and a complete trim", builds, left, f.stamp())
	}
	if left := present(youngEntries); left != len(youngEntries) {
		t.Fatalf("the trim removed %d young entries", len(youngEntries)-left)
	}
}

// TestKilledSweepIsRetried (round 3 review m2): a process killed during a
// sweep pass, after it renamed a root aside, leaves the sweep due, and the
// kernel drops its claim with it; the next build claims the sweep at once,
// removes the rest and the root left aside, and records it complete.
func TestKilledSweepIsRetried(t *testing.T) {
	f := newSweepFixture(t)
	f.build()
	aged := f.plant("aged", 10, true)
	f.due()

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stall := exec.Command(binary, "-test.run=^$")
	stall.Env = append(os.Environ(), helperVariable+"=sweep-stall", rootVariable+"="+f.root)
	stdout, err := stall.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stall.Stderr = os.Stderr
	if err := stall.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = stall.Process.Kill()
			_ = stall.Wait()
		}
	}()
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || lines.Text() != "collecting" {
		t.Fatalf("the helper did not reach its sweep: %q %v", lines.Text(), lines.Err())
	}
	if err := stall.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = stall.Wait()
	killed = true
	if s := f.stamp(); s.recent(time.Now()) {
		t.Fatalf("a killed pass recorded the sweep complete: %+v", s)
	}
	if len(f.leftovers()) != 1 || present(aged) != len(aged)-1 {
		t.Fatalf("the killed pass left %v aside and %d of %d roots; want one aside and the rest", f.leftovers(), present(aged), len(aged))
	}

	f.build()
	if present(aged) != 0 || len(f.leftovers()) != 0 || !f.stamp().recent(time.Now()) {
		t.Fatalf("the next build left %d roots and %v aside, stamp %+v; want none and a complete sweep", present(aged), f.leftovers(), f.stamp())
	}
}

// TestStampReading: a stamp is "SECONDS CURSOR"; anything else is a sweep
// that never completed, due from the start.
func TestStampReading(t *testing.T) {
	now := time.Now()
	for text, want := range map[string]stamp{
		fmt.Sprintf("%d 0", now.Unix()):    {completed: now.Unix()},
		fmt.Sprintf("%d 1500", now.Unix()): {completed: now.Unix(), cursor: 1500},
		"":                                 {},
		"12":                               {},
		"x 1":                              {},
		"12 -1":                            {},
		"12 1 3":                           {},
	} {
		if got := readStamp([]byte(text)); got != want {
			t.Errorf("readStamp(%q) = %+v, want %+v", text, got, want)
		}
	}
	if !(stamp{completed: now.Unix()}).recent(now) || (stamp{}).recent(now) || (stamp{completed: now.Add(-trimInterval).Unix()}).recent(now) || (stamp{completed: now.Add(time.Minute).Unix()}).recent(now) {
		t.Fatal("recent misjudges a stamp")
	}
}

// TestFailedLeftoverRemovalIsNotCounted (stage 3b-runtime item 4): a root
// left aside that the sweep cannot remove (here a directory inside it
// without write permission) is not counted as removed: reclaim reports
// false, the leftover stays for a later pass, and a pass with sweepRemovals
// aged roots besides it still removes every one of them.
func TestFailedLeftoverRemovalIsNotCounted(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root removes a directory without write permission")
	}
	f := newSweepFixture(t)
	f.build()
	stuck := filepath.Join(f.root, "private", removingPrefix+"stuck")
	locked := filepath.Join(stuck, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if f.c.reclaim(time.Now(), stuck) {
		t.Fatal("reclaim counted a leftover it could not remove")
	}
	if _, err := os.Stat(stuck); err != nil {
		t.Fatalf("the leftover is gone: %v", err)
	}
	aged := f.plant("aged", sweepRemovals, true)
	if p := f.c.trim(time.Now(), 0); !p.complete || present(aged) != 0 {
		t.Fatalf("a pass with a stuck leftover: complete %v, %d of %d aged roots left; want all removed", p.complete, present(aged), len(aged))
	}
}
