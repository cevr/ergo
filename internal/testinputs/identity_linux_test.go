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
// finish closed its input. Before finish, the holder has reported that it
// is up and about to wait (its ready byte), and it is still alive: a holder
// that ends early would leave this process's close as the last one. The
// holder's descriptor 3 lists the watch this process added after the holder
// started, so it is the same group.
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
	// The event: the holder's ready byte, written just before its read of
	// its input. EOF instead means the holder ended without reporting.
	if ready, err := pollIn(t, w.holderReady, deadlineLeft(t)); err != nil || !ready {
		w.finish()
		t.Fatalf("the holder never reported that it holds the watch: %v", err)
	}
	if n, err := syscall.Read(w.holderReady, make([]byte, 1)); n != 1 {
		w.finish()
		t.Fatalf("the holder ended before it reported that it holds the watch (read %d, %v)", n, err)
	}
	// Now the holder only waits on its input, which only finish closes: it
	// must be alive (its pidfd is not readable).
	if ended, err := pollIn(t, int(pidfd), 0); err != nil || ended {
		w.finish()
		t.Fatalf("the holder ended before finish (%v): this process's close would be the group's last one", err)
	}
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
	// pidfd turns readable when it exits).
	if ended, err := pollIn(t, int(pidfd), deadlineLeft(t)); err != nil || !ended {
		t.Fatalf("the holder did not end after finish closed its input (%v)", err)
	}
}

// deadlineLeft is 9/10 of the time left before the test's deadline, or -1
// (no bound) when the test has none.
func deadlineLeft(t *testing.T) time.Duration {
	if deadline, ok := t.Deadline(); ok {
		return max(time.Until(deadline)*9/10, 0)
	}
	return -1
}

// pollIn reports whether fd turns readable (POLLIN or POLLHUP) within d;
// d < 0 waits with no bound, d == 0 does not wait.
func pollIn(t *testing.T, fd int, d time.Duration) (bool, error) {
	t.Helper()
	pfd := struct {
		fd              int32
		events, revents int16
	}{fd: int32(fd), events: 0x1} // POLLIN
	var ts *syscall.Timespec
	if d >= 0 {
		left := syscall.NsecToTimespec(d.Nanoseconds())
		ts = &left
	}
	for {
		n, _, errno := syscall.Syscall6(syscall.SYS_PPOLL, uintptr(unsafe.Pointer(&pfd)), 1, uintptr(unsafe.Pointer(ts)), 0, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return false, errno
		}
		return n > 0, nil
	}
}

// TestMountVerdict: which change of the mount table between the start and
// the end of a run leaves its inputs unknown (fu-sweep-mounts). The run
// looked in the checkout /home/u/src/m, in /tmp/ec1/t, and up to its own
// Rift under /workspaces/.rifts/ergo. A Rift mount another lane makes or
// removes there mid-run is no reason, nor is a mount below a directory no
// lookup reached; a mount over a directory a lookup passed, one above it,
// one at a name looked up (this lane's Rift), a removed or remounted one
// there, a change undone, and an unreadable table are. Mutant mount-verdict-any-point (lookedIn never consulted, every
// changed mount a reason): the Rift rows are refused. Mutant
// mount-verdict-no-point (no changed mount is a reason): the rows over the
// checkout are accepted.
func TestMountVerdict(t *testing.T) {
	start := strings.Join([]string{
		"23 1 254:0 / / ro,relatime shared:1 - ext4 /dev/root rw",
		"40 23 0:33 / /workspaces ro,noatime - btrfs /dev/loop0 rw",
		"41 40 0:33 /.rifts/ergo/old /workspaces/.rifts/ergo/old ro - btrfs /dev/loop0 rw",
		"90 23 254:0 /tmp/ec1/t /tmp/ec1/t rw,relatime - ext4 /dev/root rw",
		"95 23 0:40 / /home/u/src/m/cache ro - tmpfs tmpfs rw",
	}, "\n")
	w := &inotifyWatcher{}
	for _, d := range []string{"/", "/home", "/home/u", "/home/u/src", "/home/u/src/m", "/home/u/src/m/cache", "/home/u/src/m/docs", "/tmp", "/tmp/ec1/t", "/workspaces", "/workspaces/.rifts", "/workspaces/.rifts/ergo"} {
		w.watched.Store(d, int32(-1))
	}
	// The names looked up: a Rift of this lane, and two names of the
	// checkout.
	for _, n := range []string{"/workspaces/.rifts/ergo\x00wf10-this", "/home/u/src/m\x00new", "/home/u/src/m\x00a b"} {
		w.visited.Store(n, struct{}{})
	}
	lookedIn := w.lookedIn
	with := func(extra ...string) string { return start + "\n" + strings.Join(extra, "\n") }
	without := func(id string) string {
		var out []string
		for _, l := range strings.Split(start, "\n") {
			if !strings.HasPrefix(l, id+" ") {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	for _, c := range []struct{ name, end, want string }{
		{"a Rift mounted elsewhere", with("97 40 0:33 /.rifts/ergo/new /workspaces/.rifts/ergo/new ro - btrfs /dev/loop0 rw"), ""},
		{"a Rift removed elsewhere", without("41"), ""},
		{"a mount at a name no lookup used, in a directory looked in", with("97 40 0:33 /.rifts/ergo/other /workspaces/.rifts/ergo/other ro - btrfs /dev/loop0 rw"), ""},
		{"a mount at this lane's Rift", with("97 40 0:33 /.rifts/ergo/x /workspaces/.rifts/ergo/wf10-this ro - btrfs /dev/loop0 rw"), "the mount at /workspaces/.rifts/ergo/wf10-this changed"},
		{"a mount below a directory no lookup reached", with("97 90 0:41 / /tmp/ec1/t/a/b ro - tmpfs tmpfs rw"), ""},
		{"a mount over a directory looked in", with("97 23 0:41 / /home/u/src/m/docs ro - tmpfs tmpfs rw"), "the mount at /home/u/src/m/docs changed"},
		{"a mount at a name looked up", with("97 23 0:41 / /home/u/src/m/new ro - tmpfs tmpfs rw"), "the mount at /home/u/src/m/new changed"},
		{"a mount above the checkout", with("97 23 0:41 / /home/u ro - tmpfs tmpfs rw"), "the mount at /home/u changed"},
		{"a mount point with a space", with(`97 23 0:41 / /home/u/src/m/a\040b ro - tmpfs tmpfs rw`), "the mount at /home/u/src/m/a b changed"},
		{"a mount removed in the checkout", without("95"), "the mount at /home/u/src/m/cache changed"},
		{"a mount made writable in the checkout", strings.Replace(start, "/home/u/src/m/cache ro", "/home/u/src/m/cache rw", 1), "the mount at /home/u/src/m/cache changed"},
		{"a change undone", start, "changed back, so where is unknown"},
		{"an unreadable table", "", "the table cannot be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := mountVerdict(start, c.end, lookedIn)
			if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// mountChildVariable runs TestMountTableChangeIsSeen's child.
const mountChildVariable = "ERGO_TESTINPUTS_MOUNT_CHILD"

// TestMountTableChangeIsSeen: a mount made in the namespace after the watch
// opened the mount table is a priority event on it, which finish reports
// when a lookup of the run looked in the mount point's parent. A mount at a
// directory no lookup passed is no reason (a host's mount far from the
// run's inputs, fu-sweep-mounts). The child runs in a user and mount
// namespace of its own. Mutant mount-change-ignored (finish skips the
// mount table): the first watcher's mount is accepted.
func TestMountTableChangeIsSeen(t *testing.T) {
	if dir := os.Getenv(mountChildVariable); dir != "" {
		read, far := filepath.Join(dir, "read"), filepath.Join(dir, "far", "away")
		for _, d := range []string{read, far} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		w, err := newWatcher(func(string) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := pollPriority(w.mounts); err != nil || changed {
			t.Fatalf("the mount table changed before any mount: %v %v", changed, err)
		}
		w.see(dir, "read")
		w.see(read, "x")
		if err := syscall.Mount("tmpfs", read, "tmpfs", 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, reason := w.finish(); !strings.Contains(reason, "the mount at "+read+" changed") {
			t.Fatalf("a mount during the run over a directory a lookup passed was not seen: %q", reason)
		}
		w, err = newWatcher(func(string) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		w.see(dir, "read")
		w.see(read, "x")
		if err := syscall.Mount("tmpfs", far, "tmpfs", 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, reason := w.finish(); reason != "" {
			t.Fatalf("a mount no lookup passed is a reason: %q", reason)
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
