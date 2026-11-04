package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// A private root (Cache.private) belongs to the builds of one process. A
// build holds a shared flock lease on the root's lease file from the moment
// it takes the root until its link ends (Module.Release); the kernel
// releases the lease when the process dies, however it dies. Builds of one
// process share one lease file and count their uses; the last release
// closes it, so a long-lived host holds nothing between builds. A sweep
// removes a root only when the root is older than privateLifetime and the
// sweep takes the exclusive lock without waiting: then no build holds it. A
// timestamp alone never removes a root.
//
// The sweep renames the root aside while it holds the exclusive lock, then
// unlinks the lease and removes the rest there. A build that opened the old
// lease file waits for its shared lock, then sees that the root's lease path
// no longer names that file and takes the root again, fresh; a build that
// arrives after the rename makes a new root. So a build never holds a lease
// on a root that is being removed, nor reuses a module inside one.
//
// The sweep is the Root/private part of the cache's trim (trim.go): one
// stamp, one claim and one bounded pass for the whole cache.
//
// Prior art: Go's build cache locks its entries with cmd/go/internal/
// lockedfile, flock on Unix, and trims at most daily behind trim.txt; Cargo
// locks its package cache with a file lock; ccache keeps lock files with
// stale-owner detection.

// leaseName is the lease file inside a private root, beside its modules.
const leaseName = "lease"

// removingPrefix names a directory a trim has renamed aside to remove: a
// root in Root/private, or an entry or staging directory in Root/staging.
const removingPrefix = ".removing-"

// testHookCollecting, when set, runs after a sweep has renamed a root aside
// and unlinked its lease, before it removes the rest. Tests use it to
// let a build arrive in that window.
var testHookCollecting func(root string)

// lease is the open lease file of one private root and the number of
// builds of this process that hold it.
type lease struct {
	file  *os.File
	users int
}

// leases holds this process's lease of each private root some build holds.
var leases struct {
	sync.Mutex
	held map[string]*lease
}

// acquireLease takes one use of root's lease for a build and returns its
// release, which is safe to call more than once. The first use takes the
// lease (takeLease); later uses share it until the last one is released.
func acquireLease(root string) (func(), error) {
	leases.Lock()
	defer leases.Unlock()
	l := leases.held[root]
	if l == nil {
		f, err := takeLease(root)
		if err != nil {
			return nil, err
		}
		l = &lease{file: f}
		if leases.held == nil {
			leases.held = map[string]*lease{}
		}
		leases.held[root] = l
	}
	l.users++
	var once sync.Once
	return func() { once.Do(func() { releaseLease(root, l) }) }, nil
}

// releaseLease gives back one use of l; the last use closes the file, which
// releases the lock.
func releaseLease(root string, l *lease) {
	leases.Lock()
	defer leases.Unlock()
	l.users--
	if l.users == 0 {
		delete(leases.held, root)
		l.file.Close()
	}
}

// heldLease reports whether a build of this process holds root.
func heldLease(root string) bool {
	leases.Lock()
	defer leases.Unlock()
	return leases.held[root] != nil
}

// takeLease makes root, if a sweep has not left it, and returns its lease
// file with a shared lock on it. Closing the file releases the lease.
func takeLease(root string) (*os.File, error) {
	path := filepath.Join(root, leaseName)
	for range 16 {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
		if errors.Is(err, fs.ErrNotExist) {
			continue // a sweep moved the root between the two calls
		}
		if err != nil {
			return nil, err
		}
		if err := lockShared(f); err != nil {
			f.Close()
			return nil, err
		}
		if sameLease(f, path) {
			return f, nil
		}
		f.Close() // a sweep removed the root while this waited
	}
	return nil, fmt.Errorf("build: private root %s keeps being removed", root)
}

// sameLease reports whether path still names the open lease file f.
func sameLease(f *os.File, path string) bool {
	_, same := sameFile(f, path)
	return same
}

// sameFile reports whether path still names the open file or directory f,
// and returns f's status.
func sameFile(f *os.File, path string) (fs.FileInfo, bool) {
	open, err := f.Stat()
	if err != nil {
		return nil, false
	}
	named, err := os.Stat(path)
	return open, err == nil && os.SameFile(open, named)
}

// reclaim removes one entry of Root/private if a trim may: what an earlier
// pass left aside, or a root no build holds that is older than
// privateLifetime. It reports whether it removed it. The old sweep stamp,
// which decides nothing now, is removed when the trim meets it.
func (c Cache) reclaim(now time.Time, root string) bool {
	if filepath.Base(root) == legacyStampName {
		_ = os.Remove(root)
		return false
	}
	if strings.HasPrefix(filepath.Base(root), removingPrefix) {
		return os.RemoveAll(root) == nil // a failed removal stays for a later pass
	}
	if heldLease(root) {
		return false
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || now.Sub(info.ModTime()) <= privateLifetime {
		return false
	}
	return collect(root)
}

// collect removes root if it takes root's lease exclusively without
// waiting, and reports whether it removed it.
func collect(root string) bool {
	path := filepath.Join(root, leaseName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false
	}
	if !tryLockExclusive(f) || !sameLease(f, path) {
		f.Close()
		return false // a live build holds it, or another sweep took it
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		f.Close()
		return false
	}
	aside := filepath.Join(filepath.Dir(root), removingPrefix+filepath.Base(root)+"-"+hex.EncodeToString(nonce[:]))
	if err := os.Rename(root, aside); err != nil {
		f.Close()
		return false
	}
	// The lease goes first: from here no build can lock this root's lease
	// by name, and what remains is a tree no build reaches. So the lock is
	// released before the removal: a build that waits on the old lease file
	// wakes, finds that the root's path no longer names it, and takes a
	// fresh root without waiting for the tree to be removed.
	_ = os.Remove(filepath.Join(aside, leaseName))
	f.Close()
	if testHookCollecting != nil {
		testHookCollecting(root)
	}
	_ = os.RemoveAll(aside)
	return true
}
