package v1

import (
	"path/filepath"
	"testing"
)

// TestFSLockFileExcludes checks fs.LockFile's body in one process, as
// flock excludes two open file descriptions of one process: a second
// lock of a held file is busy (-1, no error) after its wait, a lock
// after the holder's unlock is taken, a second unlock of one lock closes
// nothing, and a missing folder is NotFound. Mutants (runtime/v1
// ledger): a shared lock (LOCK_SH) lets the second lock in; an unlock
// that forgets to close keeps the file locked; a lock numbered by its
// descriptor lets a second unlock close the next lock's descriptor.
func TestFSLockFileExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := FSLockFile(path, 0)
	if err != nil || first < 0 {
		t.Fatalf("FSLockFile(free) = %d, %v; want a lock", first, err)
	}
	if second, err := FSLockFile(path, 0); err != nil || second != -1 {
		t.Fatalf("FSLockFile(held) = %d, %v; want -1 (busy)", second, err)
	}
	if second, err := FSLockFile(path, 3_000_000); err != nil || second != -1 {
		t.Fatalf("FSLockFile(held, 3ms) = %d, %v; want -1 (busy)", second, err)
	}
	FSUnlockFile(first)
	again, err := FSLockFile(path, 0)
	if err != nil || again < 0 {
		t.Fatalf("FSLockFile(after unlock) = %d, %v; want a lock", again, err)
	}
	// The descriptor first had is again's now (the lowest free number):
	// a second unlock of first must leave again's lock held.
	FSUnlockFile(first)
	if other, err := FSLockFile(path, 0); err != nil || other != -1 {
		t.Fatalf("FSLockFile(held again) = %d, %v; want -1 (busy)", other, err)
	}
	FSUnlockFile(again)
	if _, err := FSLockFile(filepath.Join(path+".d", "x"), 0); err != (NotFound{Path: filepath.Join(path+".d", "x")}) {
		t.Fatalf("FSLockFile(missing folder) error = %#v; want NotFound", err)
	}
}
