//go:build linux && (amd64 || arm64)

package testinputs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestWatchSeesChangesAfterALookup: the watch reports a change of a name a
// lookup used, made after the lookup and before finish, also when it is
// undone; not a change before the lookup, nor of a name no lookup used; and
// in the checkout, any change.
func TestWatchSeesChangesAfterALookup(t *testing.T) {
	dir := t.TempDir()
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := newWatcher(func(string) bool { return true })
	if err != nil {
		t.Skip(err)
	}
	if err := w.watchTree(checkout); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		t.Helper()
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "before")) // made before its lookup: what the lookup saw
	w.see(dir, "before")
	w.see(dir, "x")
	w.see(dir, "y")
	w.see(dir, "undone")
	write(filepath.Join(dir, "x"))                                        // a name looked up, made later
	write(filepath.Join(dir, "unseen"))                                   // no lookup used it
	if err := os.Symlink("t", filepath.Join(dir, "undone")); err != nil { // made and removed again
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "undone")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(checkout, "sub", "new")) // the checkout: no lookup needed
	changed, reason := w.finish()
	if reason != "" {
		t.Fatalf("the watch cannot vouch: %s", reason)
	}
	want := []string{filepath.Join(checkout, "sub", "new"), filepath.Join(dir, "undone"), filepath.Join(dir, "x")}
	slices.Sort(want)
	if !slices.Equal(changed, want) {
		t.Fatalf("changed\n got %q\nwant %q", changed, want)
	}
}

// TestWatchSkipsWhatOnlyTheTreeChanges: a directory the tree changes itself
// (a writable mount) or one no process of this user can change gets no
// watch.
func TestWatchSkipsWhatOnlyTheTreeChanges(t *testing.T) {
	dir := t.TempDir()
	w, err := newWatcher(func(string) bool { return false })
	if err != nil {
		t.Skip(err)
	}
	w.see(dir, "x")
	if err := os.WriteFile(filepath.Join(dir, "x"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, reason := w.finish(); len(changed) != 0 || reason != "" {
		t.Fatalf("an unwatched directory reported %q %q", changed, reason)
	}
	// The real rule: this user's temporary directory is on a writable
	// mount here; / is root's and not group or world writable.
	if changeableDir(dir) {
		t.Errorf("%s is on a writable mount, so the tree changes it itself", dir)
	}
	var st syscall.Stat_t
	if syscall.Stat("/", &st) == nil && st.Uid != uint32(os.Geteuid()) && st.Mode&0o022 == 0 && changeableDir("/") {
		t.Errorf("/ cannot change by this user's processes")
	}
}

// TestWatchGroupEndsInTheHolder: the watch's inotify group is held by a
// holder from the start, so finish's close in this process is not the last
// one and does not wait for the kernel's mark reaper; the holder ends once
// finish closed its input. The holder's descriptor 3 lists the watch this
// process added after the holder started, so it is the same group.
func TestWatchGroupEndsInTheHolder(t *testing.T) {
	dir := t.TempDir()
	w, err := newWatcher(func(string) bool { return true })
	if err != nil {
		t.Skip(err)
	}
	if w.holder < 0 || w.holderPid <= 0 {
		w.finish()
		t.Fatal("no holder started: this process's close would be the group's last one")
	}
	// The holder runs until finish closes its input: the pidfd names it.
	pidfd, _, errno := syscall.Syscall(434, uintptr(w.holderPid), 0, 0) // pidfd_open
	if errno != 0 {
		w.finish()
		t.Fatalf("pidfd_open of the holder %d: %v", w.holderPid, errno)
	}
	defer syscall.Close(int(pidfd))
	w.see(dir, "x")
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	info, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/3", w.holderPid))
	if want := fmt.Sprintf("ino:%x ", st.Ino); err != nil || !strings.Contains(string(info), want) {
		t.Errorf("the holder's descriptor 3 does not hold the watch of %s (%s): %v\n%s", dir, want, err, info)
	}
	if _, reason := w.finish(); reason != "" {
		t.Fatalf("the watch cannot vouch: %s", reason)
	}
	// Only the test's deadline bounds the wait for the holder's end (the
	// pidfd turns readable when it exits); with no deadline, no bound.
	pfd := struct {
		fd              int32
		events, revents int16
	}{fd: int32(pidfd), events: 0x1} // POLLIN
	var ts *syscall.Timespec
	if deadline, ok := t.Deadline(); ok {
		left := syscall.NsecToTimespec((time.Until(deadline) * 9 / 10).Nanoseconds())
		ts = &left
	}
	for {
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(ts)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 || n == 0 {
			t.Fatalf("the holder did not end after finish closed its input (%v)", errno)
		}
		return
	}
}

// mountChildVariable runs TestMountTableChangeIsSeen's child.
const mountChildVariable = "ERGO_TESTINPUTS_MOUNT_CHILD"

// TestMountTableChangeIsSeen: a mount made in the namespace after the watch
// opened the mount table is a priority event on it, which finish reports.
// The child runs in a user and mount namespace of its own.
func TestMountTableChangeIsSeen(t *testing.T) {
	if dir := os.Getenv(mountChildVariable); dir != "" {
		w, err := newWatcher(func(string) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := pollPriority(w.mounts); err != nil || changed {
			t.Fatalf("the mount table changed before any mount: %v %v", changed, err)
		}
		if err := syscall.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, reason := w.finish(); reason == "" {
			t.Fatal("a mount during the run was not seen")
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestMountTableChangeIsSeen$", "-test.v")
	cmd.Env = append(os.Environ(), mountChildVariable+"="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Skipf("no user namespace: %v", err)
		}
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestMountTableChangeIsSeen") {
		t.Fatalf("the child did not run the check:\n%s", out)
	}
}
