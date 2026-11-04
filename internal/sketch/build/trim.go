package build

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
// aside that a crash left), and the private roots of lease.go.
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
func (c Cache) maybeTrim(now time.Time) {
	path := filepath.Join(c.Root, trimStampName)
	data, _ := os.ReadFile(path)
	if !trimDue(leasesSupported, readStamp(data), now) {
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
	if !trimDue(leasesSupported, s, now) {
		return // a trim completed since the first read
	}
	p := c.trim(now, s.cursor)
	if p.complete {
		s = stamp{completed: now.Add(time.Since(began)).Unix()}
	} else {
		s.cursor = p.cursor
	}
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt(s.bytes(), 0)
	}
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
	return c.takeAside(now, path, unusedLimit)
}

// trimStaging removes the staging directory at path when it is older than
// stagingLifetime and no builder holds it, and reports whether it did.
func (c Cache) trimStaging(now time.Time, path string) bool {
	return c.takeAside(now, path, stagingLifetime)
}

// takeAside removes the directory at path when it is older than limit and
// the trim takes its lock exclusively without waiting. Under the lock it
// checks that path still names the locked directory and, through the
// descriptor, its age again; then it renames the directory aside into
// Root/staging, releases the lock and removes the aside. It reports whether
// the directory left path.
func (c Cache) takeAside(now time.Time, path string, limit time.Duration) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || now.Sub(info.ModTime()) <= limit {
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
	if !same || now.Sub(locked.ModTime()) <= limit {
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
