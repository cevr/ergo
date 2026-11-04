//go:build linux

package closure

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

// capSysAdmin is CAP_SYS_ADMIN, which mount needs in the new namespace.
const capSysAdmin = 21

// prctl options (linux/prctl.h).
const (
	prSetNoNewPrivs      = 38
	prCapAmbient         = 47
	prCapAmbientClearAll = 4
	statfsReadOnly       = 0x1    // ST_RDONLY
	statfsNoSuid         = 0x2    // ST_NOSUID
	statfsNoDev          = 0x4    // ST_NODEV
	statfsNoExec         = 0x8    // ST_NOEXEC
	statfsNoAtime        = 0x400  // ST_NOATIME
	statfsNoDiratime     = 0x800  // ST_NODIRATIME
	statfsRelatime       = 0x1000 // ST_RELATIME
)

func sandboxSupported() error { return nil }

// readOnly reports whether the file system that holds path is mounted
// read-only, as this process sees it.
func readOnly(path string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return err
	}
	if st.Flags&statfsReadOnly == 0 {
		return fmt.Errorf("%s is writable", path)
	}
	return nil
}

// reachedBy returns the location of the object a path names, as the
// kernel resolved it when it opened the path: the name of the open
// descriptor in /proc/self/fd. A lookup only (stat) opens with O_PATH,
// which resolves as stat does.
func reachedBy(path string, lookupOnly bool) (string, error) {
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC
	if lookupOnly {
		flags = oPath | syscall.O_CLOEXEC
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	return os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
}

// oPath is O_PATH (asm-generic/fcntl.h), which package syscall lacks on
// some architectures.
const oPath = 0x200000

// sandboxCommand returns the helper command: this test binary, started in
// a new user and mount namespace. The user namespace maps only the
// current user and group to themselves, so the tests run as the same user
// as without it; the helper holds CAP_SYS_ADMIN in the namespace as an
// ambient capability, which survives its exec, to make the mount.
func sandboxCommand(protect, binary string, args ...string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, append([]string{binary}, args...)...)
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		AmbientCaps: []uintptr{capSysAdmin},
	}
	return cmd, nil
}

// makeReadOnly makes root and every mount below it read-only in this
// process's mount namespace, and checks that each is. The namespace's
// mounts are made private first, so nothing propagates out of it.
func makeReadOnly(root string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the namespace's mounts private: %w", err)
	}
	if err := syscall.Mount(root, root, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	points, err := mountsUnder(root)
	if err != nil {
		return err
	}
	if len(points) == 0 {
		return fmt.Errorf("the bind mount of %s is not in /proc/self/mountinfo", root)
	}
	for _, p := range points {
		var st syscall.Statfs_t
		if err := syscall.Statfs(p, &st); err != nil {
			return err
		}
		// A remount in a user namespace must keep the flags the outer
		// namespace locked.
		flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY)
		for _, f := range []struct{ statfs, mount uintptr }{
			{statfsNoSuid, syscall.MS_NOSUID},
			{statfsNoDev, syscall.MS_NODEV},
			{statfsNoExec, syscall.MS_NOEXEC},
			{statfsNoAtime, syscall.MS_NOATIME},
			{statfsNoDiratime, syscall.MS_NODIRATIME},
			{statfsRelatime, syscall.MS_RELATIME},
		} {
			if uintptr(st.Flags)&f.statfs != 0 {
				flags |= f.mount
			}
		}
		if err := syscall.Mount("", p, "", flags, ""); err != nil {
			return fmt.Errorf("remount %s read-only: %w", p, err)
		}
		if err := syscall.Statfs(p, &st); err != nil {
			return err
		}
		if st.Flags&statfsReadOnly == 0 {
			return fmt.Errorf("%s is still writable after the remount", p)
		}
	}
	return nil
}

// mountsUnder returns the mount points at or below root, in the order of
// /proc/self/mountinfo (parents before children).
func mountsUnder(root string) ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		p := unescapeMountinfo(fields[4])
		if (p == root || strings.HasPrefix(p, root+"/")) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// unescapeMountinfo decodes the octal escapes (\040 and the like) the
// kernel writes for space, tab, newline and backslash in mountinfo.
func unescapeMountinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return '0' <= c && c <= '7' }

// dropCapabilities clears this thread's ambient capabilities and sets
// no_new_privs, and locks the goroutine to the thread. An exec from this
// thread then runs the program as the mapped user with no capability, so
// the tests cannot remount the checkout writable.
func dropCapabilities() error {
	runtime.LockOSThread()
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("clear the ambient capabilities: %w", e)
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("set no_new_privs: %w", e)
	}
	return nil
}
