package build

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The trim bounds the emission cache by age, as Go's build cache does. A
// build marks the entry it reuses as used (at most once per markInterval,
// through the entry directory's modification time), and a build starts a
// trim at most once per trimInterval. The trim removes the published
// entries that no build used for unusedLimit, the staging directories older
// than stagingLifetime (a crashed builder's, a discard's aside, a trim's own
// aside that a crash left), and the private roots of lease.go. A count
// bounds the published entries too: past entryCap, the cap pass removes the
// least recently used (capEntries), after each build that published one
// and after each due trim. The same passes bound the law verdict store,
// Root/laws (lawtrim.go).
//
// Ages decide what to remove, never whether a removal is safe. Safety rests
// on directory locks:
//
//   - A build holds a shared flock on the entry it uses, from before it
//     verifies the entry until Module.Release, after its link. A builder
//     locks its staging directory before it writes a byte and keeps the
//     descriptor through the rename that publishes it; the lock follows the
//     inode, so a new entry is held from its first byte.
//   - The trim moves a directory only while it holds an exclusive flock on
//     it, taken without waiting, and after it checked under the lock that
//     the path still names the locked directory. So no build is between its
//     verification and its release on a directory the trim moves.
//   - The trim renames a directory aside into Root/staging, under a name no
//     builder uses, before it removes anything. So Root/modules/KEY names a
//     whole entry or nothing.
//   - Where flock does not exist, nothing is removed.
//
// One stamp, Root/trim.txt (Go's name), records when the last complete
// trim ended and where an unfinished one continues. Its lock claims a pass,
// and the kernel drops the claim if the claimant dies. A build that finds
// the stamp recent reads only the stamp. Each pass is bounded; a pass that
// hits a bound, or dies, leaves the trim due, so the next build continues
// it from the recorded cursor.
//
// Prior art: Go's cmd/go/internal/cache (used() marks at most hourly;
// Trim runs at most daily behind trim.txt and removes entries unused for
// five days); Cargo's automatic clean of its home cache by last use.

// trimStampName is the trim stamp in Root (type stamp). Its lock claims a
// pass.
const trimStampName = "trim.txt"

// legacyStampName is the sweep stamp of private roots before trim.txt. It
// decides nothing; a trim removes it when it meets it in Root/private.
const legacyStampName = ".swept"

const (
	// markInterval is the least time between two use marks of an entry.
	markInterval = time.Hour
	// trimInterval is the least time between the end of one complete trim
	// and the start of the next.
	trimInterval = time.Hour
	// unusedLimit is how long a published entry stays after its last use.
	unusedLimit = 24 * time.Hour
	// stagingLifetime is how long a staging directory stays. It covers the
	// window between a builder's MkdirTemp and its lock.
	stagingLifetime = time.Hour
)

// A trim pass reads names sweepChunk at a time, examines at most
// sweepEntries of them and removes at most sweepRemovals directories; a
// pass that hits a bound leaves the rest to the next build.
const (
	sweepChunk    = 256
	sweepEntries  = 1024
	sweepRemovals = 64
)

// markTimes sets an entry's use mark. Tests replace it.
var markTimes = os.Chtimes

// testHookTrimLocked, when set, runs after the trim has taken a
// directory's exclusive lock and before it checks the path and renames the
// directory aside. testHookTrimAside, when set, runs after the rename aside
// and before the removal.
var (
	testHookTrimLocked func(path string)
	testHookTrimAside  func(aside string)
)

// mark records that a build used the entry at dir now, unless its last
// mark (info, the locked entry's status) is less than markInterval old. A
// future mark counts as a use now. A failed mark does not fail the build;
// the entry then ages from its last mark.
func mark(dir string, info fs.FileInfo, now time.Time) {
	if now.Sub(info.ModTime()) >= markInterval {
		_ = markTimes(dir, now, now)
	}
}

// trimDue decides whether a build starts a trim pass: only where locks
// exist, and only when the last complete trim is not recent.
func trimDue(locks bool, s stamp, now time.Time) bool {
	return locks && !s.recent(now)
}

// maybeTrim runs a trim pass of c when one is due and no other process
// holds the claim. A recent stamp costs one file read and no directory
// scan. The stamp records a trim as complete only after a pass that reached
// the end of every directory without hitting a bound.
func (c Cache) maybeTrim(now time.Time) { c.trimAfter(now, false) }

// trimAfter is maybeTrim after a build; published reports that the build
// published a new entry. Then it also counts the entries of Root/modules,
// at most entryCap+1 names, and when there are more than entryCap it runs
// the cap pass (capEntries) under the claim, due or not. A due trim runs
// the cap pass too, after its own pass. So a cache that only grows by
// publication stays within entryCap after each build that the claim lets
// trim, and a build that only reuses still reads only the stamp.
func (c Cache) trimAfter(now time.Time, published bool) { c.trimPass(now, published, false) }

// trimPass is trimAfter; laws reports that the command wrote law
// verdicts (lawtrim.go), so the pass also counts Root/laws against
// lawCap. A due trim and every cap pass bound Root/laws too (trimLaws).
func (c Cache) trimPass(now time.Time, published, laws bool) {
	path := filepath.Join(c.Root, trimStampName)
	data, _ := os.ReadFile(path)
	modulesOver := leasesSupported && published && c.overCap()
	lawsOver := leasesSupported && laws && c.lawsOverCap()
	over := modulesOver || lawsOver
	if !over && !trimDue(leasesSupported, readStamp(data), now) {
		return
	}
	began := time.Now()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return // no cache yet, or no access: nothing to do
	}
	defer f.Close()
	if !tryLockExclusive(f) {
		return // another process is trimming
	}
	if data, err = io.ReadAll(f); err != nil {
		return
	}
	s := readStamp(data)
	due := trimDue(leasesSupported, s, now)
	if !due && !over {
		return // a trim completed since the first read
	}
	if !due {
		c.capEntries()
		if lawsOver {
			c.trimLaws(now, false)
		}
		return
	}
	p := c.trim(now, s.cursor)
	c.capEntries()
	c.trimLaws(now, true)
	if p.complete {
		s = stamp{completed: now.Add(time.Since(began)).Unix()}
	} else {
		s.cursor = p.cursor
	}
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt(s.bytes(), 0)
	}
}

// entryCap is the most published entries Root/modules keeps (type
// variable: tests set it small). An entry of an example program is about
// 1.1 MB, its runtime copy included (measured 2026-11-12), so the cap
// bounds the published entries near 2.3 GB. Go's build cache trims by age
// alone; the cap is Cargo's size limit of its automatic clean, counted in
// entries, which a build counts cheaply.
var entryCap = 2048

// overCap reports whether Root/modules holds more than entryCap names. It
// reads at most entryCap+1 names and stats none.
func (c Cache) overCap() bool {
	dir, err := os.Open(filepath.Join(c.Root, "modules"))
	if err != nil {
		return false
	}
	defer dir.Close()
	seen := 0
	for seen <= entryCap {
		names, err := dir.Readdirnames(sweepChunk)
		seen += len(names)
		if err != nil || len(names) == 0 {
			break
		}
	}
	return seen > entryCap
}

// capEntries is the cap pass: when Root/modules holds more than entryCap
// entries, it removes the least recently used ones (the oldest use marks),
// the whole excess in one pass, and returns how many it removed. It has no
// per-pass bound, unlike the age pass: a cache far past the cap (one from
// before the cap, which reached 18,357 entries) converges in one pass
// instead of lingering at 64 removals a pass; a cache within the cap costs
// one listing and pays per removal only past it. It removes an entry as the age pass does (takeAside): only
// under the entry's exclusive lock, taken without waiting, so never one a
// build holds, and only when the entry was not marked used since the pass
// read its mark. A held entry is passed over for the next oldest. The pass
// stats each entry once; it runs under the trim's claim.
func (c Cache) capEntries() int {
	dir := filepath.Join(c.Root, "modules")
	listing, err := os.ReadDir(dir)
	if err != nil || len(listing) <= entryCap {
		return 0
	}
	type used struct {
		path string
		mark time.Time
	}
	var all []used
	for _, entry := range listing {
		path := filepath.Join(dir, entry.Name())
		if info, err := os.Lstat(path); err == nil && info.IsDir() {
			all = append(all, used{path, info.ModTime()})
		}
	}
	slices.SortStableFunc(all, func(a, b used) int { return a.mark.Compare(b.mark) })
	excess := len(all) - entryCap
	removed := 0
	for _, e := range all {
		if removed >= excess {
			break
		}
		if c.takeAside(e.path, func(mark time.Time) bool { return !mark.After(e.mark) }) {
			removed++
		}
	}
	return removed
}

// stamp is the content of trim.txt: the Unix time, in seconds, at which
// the last complete trim ended, and the cursor at which an unfinished trim
// continues (names to pass over), as "SECONDS CURSOR".
type stamp struct {
	completed int64
	cursor    int
}

// readStamp parses a stamp; a missing, torn or foreign one is a trim that
// never completed, due at once from the start.
func readStamp(data []byte) stamp {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return stamp{}
	}
	completed, err1 := strconv.ParseInt(fields[0], 10, 64)
	cursor, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil || cursor < 0 {
		return stamp{}
	}
	return stamp{completed: completed, cursor: cursor}
}

func (s stamp) bytes() []byte {
	return []byte(strconv.FormatInt(s.completed, 10) + " " + strconv.Itoa(s.cursor))
}

// recent reports whether the last complete trim ended less than
// trimInterval before now. A stamp in the future is not recent.
func (s stamp) recent(now time.Time) bool {
	if s.completed == 0 {
		return false
	}
	age := now.Sub(time.Unix(s.completed, 0))
	return age >= 0 && age < trimInterval
}

// pass is the result of one trim pass: whether it reached the end of every
// directory, the cursor at which the next pass continues, and what it
// examined and removed.
type pass struct {
	complete          bool
	cursor            int
	examined, removed int
}

// trim is one bounded pass from cursor over Root/modules, Root/staging and
// Root/private, in that order, as one sequence of names. It passes over the
// first cursor names, which earlier passes of this trim examined, then
// examines at most sweepEntries names and removes at most sweepRemovals
// directories. The next cursor counts the names it passed over and
// examined, less those it removed. Passing over a name costs only its read,
// never a stat. Names created during a trim may shift the order, so one
// trim can miss a name or examine one twice; the next trim reaches it. It
// never waits for a lock.
func (c Cache) trim(now time.Time, cursor int) pass {
	if !leasesSupported {
		return pass{complete: true}
	}
	parts := []struct {
		dir     string
		reclaim func(time.Time, string) bool
	}{
		{filepath.Join(c.Root, "modules"), c.trimEntry},
		{filepath.Join(c.Root, "staging"), c.trimStaging},
		{filepath.Join(c.Root, "private"), c.reclaim},
	}
	var p pass
	passed := 0
	for _, part := range parts {
		dir, err := os.Open(part.dir)
		if err != nil {
			continue
		}
		for {
			names, err := dir.Readdirnames(sweepChunk)
			for _, name := range names {
				if passed < cursor {
					passed++
					continue
				}
				if p.examined == sweepEntries || p.removed == sweepRemovals {
					dir.Close()
					p.cursor = cursor + p.examined - p.removed
					return p
				}
				p.examined++
				if part.reclaim(now, filepath.Join(part.dir, name)) {
					p.removed++
				}
			}
			if err != nil || len(names) == 0 {
				break
			}
		}
		dir.Close()
	}
	p.complete = true
	return p
}

// trimEntry removes the published entry at path when no build used it for
// unusedLimit and no build holds it, and reports whether it did.
func (c Cache) trimEntry(now time.Time, path string) bool {
	return c.takeAside(path, olderThan(now, unusedLimit))
}

// trimStaging removes the staging directory at path when it is older than
// stagingLifetime and no builder holds it, and reports whether it did.
func (c Cache) trimStaging(now time.Time, path string) bool {
	return c.takeAside(path, olderThan(now, stagingLifetime))
}

// olderThan is the age test of the age pass: a modification time more
// than limit before now.
func olderThan(now time.Time, limit time.Duration) func(time.Time) bool {
	return func(mark time.Time) bool { return now.Sub(mark) > limit }
}

// takeAside removes the directory at path when stale holds for its
// modification time and the trim takes its lock exclusively without
// waiting. Under the lock it checks that path still names the locked
// directory and, through the descriptor, stale again; then it renames the
// directory aside into Root/staging, releases the lock and removes the
// aside. It reports whether the directory left path.
func (c Cache) takeAside(path string, stale func(time.Time) bool) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !stale(info.ModTime()) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	if !tryLockExclusive(f) {
		f.Close()
		return false // a build holds it
	}
	if testHookTrimLocked != nil {
		testHookTrimLocked(path)
	}
	locked, same := sameFile(f, path)
	if !same || !stale(locked.ModTime()) {
		f.Close()
		return false // moved, or used, since the trim read it
	}
	stagingRoot := filepath.Join(c.Root, "staging")
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		f.Close()
		return false
	}
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		f.Close()
		return false
	}
	aside := filepath.Join(stagingRoot, removingPrefix+hex.EncodeToString(nonce[:]))
	if err := os.Rename(path, aside); err != nil {
		f.Close()
		return false
	}
	f.Close()
	if testHookTrimAside != nil {
		testHookTrimAside(aside)
	}
	_ = os.RemoveAll(aside) // what stays is staging a later pass removes
	return true
}
