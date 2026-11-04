//go:build linux && (amd64 || arm64)

package testinputs

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func inode(info fs.FileInfo) (dev, ino uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Dev, st.Ino
	}
	return 0, 0
}

// The watch (inotify) closes the gap between the observer's lookup of a
// name and the kernel's: the observer adds a watch on a directory before it
// looks a name up in it, so a change of that name after the lookup is an
// event, whenever it happens before the tree exits. The kernel queues the
// event when the change is made, by any process and through any name of the
// directory, so a change undone before the end of the run is seen too.
//
// What is watched:
//
//   - every directory of the checkout, from before the tests start: any
//     change in it is a changed location, since the sandbox keeps the
//     checkout read-only for the tree; a new hard link of a checkout file
//     counts (its link count is an attribute);
//   - every other directory a lookup passes that a process of this user can
//     change, on a read-only mount: a directory on a writable mount is the
//     tree's own scratch, whose changes the tree makes itself (a change
//     there by a process outside the tree is not told apart: a known gap,
//     which only cutting the tree off from such processes closes), and a
//     directory that another user owns and no group or other may write
//     cannot change by this user's processes;
//   - the mount table of the namespace (/proc/self/mountinfo, which poll
//     reports changed): the tree cannot mount, so a change of it during the
//     run comes from outside, from a process that entered the namespace.
//
// In a watched directory outside the checkout, only a change of a name a
// lookup used counts: a name added, removed or replaced, a directory's
// attributes, or the directory itself removed, moved or unmounted.

// watchMask is what a watched directory outside the checkout reports;
// checkoutMask adds writes of a file's content.
const (
	watchMask = syscall.IN_ATTRIB | syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_DELETE_SELF |
		syscall.IN_MOVE_SELF | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO
	checkoutMask = watchMask | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE
)

// bindingEvents change what a name in a directory names.
const bindingEvents = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO

// selfEvents change the watched directory itself.
const selfEvents = syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_UNMOUNT | syscall.IN_IGNORED | syscall.IN_ATTRIB

type watchedDir struct {
	at       string
	checkout bool
}

// inotifyWatcher is the watcher of identity mode on Linux.
type inotifyWatcher struct {
	fd     int
	file   *os.File // the same descriptor, read by the poller
	mounts int      // /proc/self/mountinfo, open since the start
	done   chan struct{}
	// changeable reports whether a process outside the tree may change a
	// directory, so that it needs a watch.
	changeable func(dir string) bool

	addMu   sync.Mutex
	watched sync.Map // directory → its watch descriptor, or -1 when it needs none
	dirs    sync.Map // watch descriptor → watchedDir
	visited sync.Map // directory + "\x00" + name → struct{}

	mu      sync.Mutex
	changed []string
	failure string
}

// newIdentity starts identity mode for the checkout at root: the watch of
// every directory of the checkout and of the mount table. The tests have not
// started yet.
func newIdentity(file string, r *recorder) (*identity, error) {
	w, err := newWatcher(changeableDir)
	if err != nil {
		return nil, err
	}
	if err := w.watchTree(r.physical); err != nil {
		w.close()
		return nil, err
	}
	return newIdentityRecord(file, w), nil
}

func newWatcher(changeable func(string) bool) (*inotifyWatcher, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("inotify_init1: %w", err)
	}
	mounts, err := syscall.Open("/proc/self/mountinfo", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("open the mount table: %w", err)
	}
	w := &inotifyWatcher{fd: fd, mounts: mounts, done: make(chan struct{}), changeable: changeable}
	// The descriptor is non-blocking, so package os reads it through the
	// poller, and a read deadline ends the reader (finish).
	w.file = os.NewFile(uintptr(fd), "inotify")
	go w.read()
	return w, nil
}

func (w *inotifyWatcher) close() {
	w.file.SetReadDeadline(time.Now())
	<-w.done
	w.file.Close()
	syscall.Close(w.mounts)
}

// watchTree watches every directory below root, links not followed.
func (w *inotifyWatcher) watchTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		wd, err := syscall.InotifyAddWatch(w.fd, p, checkoutMask|syscall.IN_DONT_FOLLOW|syscall.IN_ONLYDIR)
		if err != nil {
			return fmt.Errorf("watch the checkout's directory %s: %w", p, err)
		}
		w.dirs.Store(int32(wd), watchedDir{at: p, checkout: true})
		w.watched.Store(p, int32(wd))
		return nil
	})
}

// changeableDir reports whether a process of this user outside the tree may
// change the directory: it is on a read-only mount (a writable one is the
// tree's scratch), and this user owns it or a group or others may write it.
func changeableDir(dir string) bool {
	var fsInfo syscall.Statfs_t
	if err := syscall.Statfs(dir, &fsInfo); err == nil && fsInfo.Flags&stRdonly == 0 {
		return false
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(dir, &st); err != nil {
		return true // the watch then fails, and says why
	}
	return int(st.Uid) == os.Geteuid() || st.Mode&0o022 != 0
}

// stRdonly is ST_RDONLY in struct statfs's f_flags.
const stRdonly = 1

func (w *inotifyWatcher) see(dir, name string) {
	wd, ok := w.watched.Load(dir)
	if !ok {
		wd = w.add(dir)
	}
	if wd.(int32) >= 0 {
		w.visited.Store(dir+"\x00"+name, struct{}{})
	}
}

// add decides whether a directory needs a watch and adds it. The watch is in
// place when add returns, before the caller's lookup.
func (w *inotifyWatcher) add(dir string) any {
	w.addMu.Lock()
	defer w.addMu.Unlock()
	if wd, ok := w.watched.Load(dir); ok {
		return wd
	}
	wd := int32(-1)
	if w.changeable(dir) {
		n, err := syscall.InotifyAddWatch(w.fd, dir, watchMask|syscall.IN_DONT_FOLLOW|syscall.IN_ONLYDIR|syscall.IN_MASK_ADD)
		if err != nil {
			// The lookup goes on; the run is refused at the end.
			w.fail(fmt.Sprintf("watch %s: %v", dir, err))
		} else {
			wd = int32(n)
			w.dirs.LoadOrStore(wd, watchedDir{at: dir})
		}
	}
	w.watched.Store(dir, wd)
	return wd
}

func (w *inotifyWatcher) fail(reason string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failure == "" {
		w.failure = reason
	}
}

// read collects events until finish sets a deadline.
func (w *inotifyWatcher) read() {
	defer close(w.done)
	buf := make([]byte, 64<<10)
	for {
		n, err := w.file.Read(buf)
		if err != nil {
			return
		}
		w.parse(buf[:n])
	}
}

// parse reads struct inotify_event records. An event is checked against the
// visited names when it is read: a change made after a lookup is queued
// after the lookup marked its name, so it is never missed; a change made
// before the lookup is what the lookup saw.
func (w *inotifyWatcher) parse(buf []byte) {
	for len(buf) >= 16 {
		wd := int32(binary.NativeEndian.Uint32(buf[0:]))
		mask := binary.NativeEndian.Uint32(buf[4:])
		size := int(binary.NativeEndian.Uint32(buf[12:]))
		if 16+size > len(buf) {
			w.fail("a truncated inotify event")
			return
		}
		name := strings.TrimRight(string(buf[16:16+size]), "\x00")
		buf = buf[16+size:]
		if mask&syscall.IN_Q_OVERFLOW != 0 {
			w.fail("the queue of changes overflowed, so a change may be missing")
			continue
		}
		v, ok := w.dirs.Load(wd)
		if !ok {
			continue
		}
		dir := v.(watchedDir)
		at := dir.at
		if name != "" {
			at = filepath.Join(dir.at, name)
		}
		switch {
		case dir.checkout:
			// The checkout is read-only to the tree: any change is from
			// outside.
		case name == "":
			if mask&selfEvents == 0 {
				continue
			}
		default:
			if _, seen := w.visited.Load(dir.at + "\x00" + name); !seen {
				continue
			}
			if mask&bindingEvents == 0 && (mask&syscall.IN_ATTRIB == 0 || mask&syscall.IN_ISDIR == 0) {
				continue // a file's content or attributes: not what a name names
			}
		}
		w.mu.Lock()
		if !slices.Contains(w.changed, at) {
			w.changed = append(w.changed, at)
		}
		w.mu.Unlock()
	}
}

// finish collects the events queued before now (the tree has exited) and
// checks the mount table.
func (w *inotifyWatcher) finish() ([]string, string) {
	w.file.SetReadDeadline(time.Now())
	<-w.done
	buf := make([]byte, 64<<10)
	for {
		n, err := syscall.Read(w.fd, buf)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			break // EAGAIN: the queue is empty
		}
		w.parse(buf[:n])
	}
	w.file.Close()
	mountsChanged, err := pollPriority(w.mounts)
	syscall.Close(w.mounts)
	switch {
	case err != nil:
		w.fail("poll the mount table: " + err.Error())
	case mountsChanged:
		w.fail("the mount table changed during the run, and the tree cannot mount: a process outside it did")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	slices.Sort(w.changed)
	return w.changed, w.failure
}

// pollPriority reports whether poll finds a priority event (POLLPRI or
// POLLERR) on a descriptor now; for a mount table it means the table changed
// since the descriptor was opened.
func pollPriority(fd int) (bool, error) {
	const pollPri, pollErr = 0x2, 0x8
	pfd := struct {
		fd              int32
		events, revents int16
	}{fd: int32(fd), events: pollPri}
	var timeout syscall.Timespec
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(&timeout)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return false, errno
		}
		return pfd.revents&(pollPri|pollErr) != 0, nil
	}
}
