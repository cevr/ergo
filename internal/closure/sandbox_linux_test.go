//go:build linux

package closure

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// Capabilities (linux/capability.h) the helper holds in its user namespace,
// as ambient capabilities that survive its exec: CAP_SYS_ADMIN to mount,
// CAP_SYS_RESOURCE to set the namespace's limit of user namespaces, and
// CAP_SETPCAP to empty the bounding set before it runs the tests.
const (
	capSetpcap     = 8
	capSysAdmin    = 21
	capSysResource = 24
)

// prctl options (linux/prctl.h), capset (linux/capability.h) and
// mount_setattr (linux/mount.h, Linux 5.12; one number on every
// architecture).
const (
	prCapbsetDrop           = 24
	prSetNoNewPrivs         = 38
	prCapAmbient            = 47
	prCapAmbientClearAll    = 4
	linuxCapabilityVersion3 = 0x20080522
	sysMountSetattr         = 442
	atRecursive             = 0x8000
	mountAttrReadOnly       = 0x1
	statfsReadOnly          = 0x1 // ST_RDONLY
)

// maxUserNamespaces is the limit, per user namespace, of the user
// namespaces its processes may make. The helper sets it to 0 in its own
// namespace.
const maxUserNamespaces = "/proc/sys/user/max_user_namespaces"

func sandboxSupported() error { return nil }

// sandboxCommand returns the helper command: this test binary, started in
// a new user and mount namespace. The user namespace maps only the
// current user and group to themselves, so the tests run as the same user
// as without it. The kernel denies a process of the new user namespace
// access to the /proc entries (root, cwd, fd, mem) of every process
// outside it, the sweep included (ptrace access mode across user
// namespaces), so no process of the run reaches the writable mount
// namespace it came from.
func sandboxCommand(binary string, args ...string) (*exec.Cmd, error) {
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
		AmbientCaps: []uintptr{capSetpcap, capSysAdmin, capSysResource},
	}
	return cmd, nil
}

// makeSandbox builds the sandbox in this process's new namespaces:
//
//   - no process of the user namespace can make another user namespace
//     (S3, with dropPrivileges);
//   - each scratch directory is bound onto itself (one mount each, no
//     submount), and a fresh tmpfs covers /dev/shm;
//   - every mount of the namespace is made read-only, recursively from
//     the root of the file system, in one call (S1);
//   - the scratch mounts and /dev/shm are made writable again (S2), and
//     each declared cgroup2 mount.
//
// The namespace's mounts are made private first, so nothing propagates out
// of it.
func makeSandbox(sb sandbox) error {
	if err := checkScratch(sb); err != nil {
		return err
	}
	if err := os.WriteFile(maxUserNamespaces, []byte("0\n"), 0); err != nil {
		return fmt.Errorf("forbid new user namespaces: %w", err)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make the namespace's mounts private: %w", err)
	}
	for _, dir := range sb.Scratch {
		if err := syscall.Mount(dir, dir, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind the scratch directory %s: %w", dir, err)
		}
	}
	// POSIX shared memory gets a fresh, empty tmpfs of its own: a scratch
	// directory, not the host's /dev/shm.
	if err := syscall.Mount("tmpfs", sharedMemory, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("mount a fresh %s: %w", sharedMemory, err)
	}
	if err := mountSetattr("/", atRecursive, mountAttr{set: mountAttrReadOnly}); err != nil {
		return fmt.Errorf("make every mount read-only: %w", err)
	}
	for _, dir := range append(slices.Clone(sb.Scratch), sharedMemory) {
		if err := mountSetattr(dir, 0, mountAttr{clr: mountAttrReadOnly}); err != nil {
			return fmt.Errorf("make the scratch directory %s writable: %w", dir, err)
		}
	}
	for _, point := range sb.Kernel {
		var st syscall.Statfs_t
		if err := syscall.Statfs(point, &st); err != nil {
			return err
		}
		if st.Type != cgroup2SuperMagic {
			return fmt.Errorf("the declared kernel write %s is not a cgroup2 file system", point)
		}
		if err := mountSetattr(point, 0, mountAttr{clr: mountAttrReadOnly}); err != nil {
			return fmt.Errorf("make the declared cgroup2 mount %s writable: %w", point, err)
		}
	}
	return nil
}

// cgroup2SuperMagic is CGROUP2_SUPER_MAGIC (linux/magic.h).
const cgroup2SuperMagic = 0x63677270

// checkScratch is S2's rule for the scratch directories, before any is
// mounted: each is a physical, absolute directory, empty (fresh), outside
// the checkout and not an ancestor of it, and none lies in another.
func checkScratch(sb sandbox) error {
	if !filepath.IsAbs(sb.Root) || filepath.Clean(sb.Root) != sb.Root {
		return fmt.Errorf("the checkout %q is not a clean absolute path", sb.Root)
	}
	if physical, err := filepath.EvalSymlinks(sb.Root); err != nil {
		return err
	} else if physical != sb.Root {
		return fmt.Errorf("the checkout %s is at %s: name it by its physical path", sb.Root, physical)
	}
	if len(sb.Scratch) == 0 {
		return errors.New("no scratch directory: the tests need a writable TMPDIR")
	}
	for i, dir := range sb.Scratch {
		if physical, err := filepath.EvalSymlinks(dir); err != nil {
			return fmt.Errorf("the scratch directory: %w", err)
		} else if physical != dir || !filepath.IsAbs(dir) {
			return fmt.Errorf("the scratch directory %s is at %s: name it by its physical path", dir, physical)
		}
		if below(dir, sb.Root) {
			return fmt.Errorf("the scratch directory %s lies in the checkout %s", dir, sb.Root)
		}
		if below(sb.Root, dir) {
			return fmt.Errorf("the scratch directory %s is an ancestor of the checkout %s", dir, sb.Root)
		}
		for _, other := range sb.Scratch[:i] {
			if below(dir, other) || below(other, dir) {
				return fmt.Errorf("the scratch directories %s and %s overlap", other, dir)
			}
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("the scratch directory: %w", err)
		}
		if len(entries) != 0 {
			return fmt.Errorf("the scratch directory %s is not empty (%s): the sweep makes it fresh for each run", dir, entries[0].Name())
		}
	}
	return nil
}

// mountAttr is struct mount_attr (linux/mount.h).
type mountAttr struct {
	set, clr, propagation, usernsFD uint64
}

func mountSetattr(path string, flags int, attr mountAttr) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	cwd := -100 // AT_FDCWD
	_, _, e := syscall.Syscall6(sysMountSetattr, uintptr(cwd), uintptr(unsafe.Pointer(p)), uintptr(flags), uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	runtime.KeepAlive(p)
	if e != 0 {
		return &os.PathError{Op: "mount_setattr", Path: path, Err: e}
	}
	return nil
}

// checkSandbox verifies the sandbox as this process sees it (S1, S2): the
// mounts of the namespace (checkMounts), the checkout's read-only view and
// each scratch directory's writable one, and that no file of the checkout
// has a name outside it (checkHardLinks).
func checkSandbox(sb sandbox) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	mounts, err := parseMountinfo(string(data))
	if err != nil {
		return err
	}
	if err := checkMounts(mounts, sb); err != nil {
		return err
	}
	if err := readOnly(sb.Root); err != nil {
		return err
	}
	if entries, err := os.ReadDir(sharedMemory); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("%s is not the sandbox's fresh tmpfs: it holds %s", sharedMemory, entries[0].Name())
	}
	for _, dir := range sb.Scratch {
		if err := readOnly(dir); err == nil {
			return fmt.Errorf("the scratch directory %s is read-only", dir)
		}
	}
	return checkHardLinks(sb.Root)
}

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

// checkHardLinks fails when a file of the checkout has a name outside it:
// a hard link the sweep cannot see, through which a test could read the
// file by another name. It counts each file's names in the checkout and
// compares them with the file's link count.
func checkHardLinks(root string) error {
	type key struct{ dev, ino uint64 }
	names := map[key]int{}
	links := map[key]uint64{}
	first := map[key]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Nlink < 2 {
			return nil
		}
		k := key{uint64(st.Dev), st.Ino}
		names[k]++
		links[k] = uint64(st.Nlink)
		if _, ok := first[k]; !ok {
			first[k] = p
		}
		return nil
	})
	if err != nil {
		return err
	}
	for k, n := range names {
		if uint64(n) < links[k] {
			return fmt.Errorf("%s has %d names, %d of them in the checkout: a hard link outside the checkout is another name of a checkout file, which the sweep cannot classify", first[k], links[k], n)
		}
	}
	return nil
}

// reachedBy returns the location of the object a path names, as the
// kernel resolved it when it opened the path (the name of the open
// descriptor in /proc/self/fd), and the object's device and inode. A
// lookup only (stat) opens with O_PATH, which resolves as stat does.
func reachedBy(path string, lookupOnly bool) (string, [2]uint64, error) {
	flags := syscall.O_RDONLY | syscall.O_CLOEXEC
	if lookupOnly {
		flags = oPath | syscall.O_CLOEXEC
	}
	fd, err := syscall.Open(path, flags, 0)
	if err != nil {
		return "", [2]uint64{}, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return "", [2]uint64{}, err
	}
	reached, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	return reached, [2]uint64{uint64(st.Dev), st.Ino}, err
}

// oPath is O_PATH (asm-generic/fcntl.h), which package syscall lacks on
// some architectures.
const oPath = 0x200000

// identity returns the device and inode of the object at path, without
// following a final link.
func identity(path string) ([2]uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return [2]uint64{}, err
	}
	return [2]uint64{uint64(st.Dev), st.Ino}, nil
}

// dropPrivileges empties every capability set of this thread (bounding,
// ambient, inheritable, permitted, effective) and sets no_new_privs, and
// locks the goroutine to the thread. An exec from this thread then runs
// the tests with no capability and no way to gain one (S3): they cannot
// remount, unmount or mount, and the namespace's limit stops them from
// making a user namespace that would give them capabilities again.
func dropPrivileges() error {
	runtime.LockOSThread()
	data, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return err
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	for c := 0; c <= last; c++ {
		if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapbsetDrop, uintptr(c), 0, 0, 0, 0); e != 0 {
			return fmt.Errorf("drop capability %d from the bounding set: %w", c, e)
		}
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClearAll, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("clear the ambient capabilities: %w", e)
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("set no_new_privs: %w", e)
	}
	header := struct {
		version uint32
		pid     int32
	}{version: linuxCapabilityVersion3}
	var sets [2]struct{ effective, permitted, inheritable uint32 }
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&sets[0])), 0); e != 0 {
		return fmt.Errorf("clear the capability sets: %w", e)
	}
	return nil
}

// bindMount mounts source at target, as a bind mount.
func bindMount(source, target string) error {
	return syscall.Mount(source, target, "", syscall.MS_BIND, "")
}

// checkPrivileges reports, from inside the sandbox, each way the process
// could undo it that is open (S3): a capability in any set, no_new_privs
// unset, a remount or unmount the kernel allows, a new user namespace.
// self runs as the child of the user-namespace attempt. It refuses to try
// in the initial user namespace, where a privileged caller's attempts
// would change the host's mounts.
func checkPrivileges(sb sandbox, self string) error {
	uidMap, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return err
	}
	if fields := strings.Fields(string(uidMap)); len(fields) == 3 && fields[0] == "0" && fields[1] == "0" && fields[2] == "4294967295" {
		return errors.New("checkPrivileges runs only in the sandbox's user namespace")
	}
	if len(sb.Scratch) == 0 {
		return errors.New("no scratch directory")
	}
	var open []string
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch name {
		case "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb":
			if strings.Trim(value, "0") != "" {
				open = append(open, name+" "+value)
			}
		case "NoNewPrivs":
			if value != "1" {
				open = append(open, "NoNewPrivs "+value)
			}
		}
	}
	if err := syscall.Mount("", "/", "", syscall.MS_BIND|syscall.MS_REMOUNT, ""); err == nil {
		open = append(open, "a remount of / writable succeeded")
	}
	if err := mountSetattr("/", atRecursive, mountAttr{clr: mountAttrReadOnly}); err == nil {
		open = append(open, "mount_setattr cleared read-only on every mount")
	}
	if err := syscall.Unmount(sb.Scratch[0], syscall.MNT_DETACH); err == nil {
		open = append(open, "an unmount of "+sb.Scratch[0]+" succeeded")
	}
	child := exec.Command(self)
	child.Env = []string{childVariable + `={"Op":"noop"}`}
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER}
	if err := child.Run(); err == nil {
		open = append(open, "a child in a new user namespace ran")
	}
	if len(open) > 0 {
		return fmt.Errorf("the tests could undo the sandbox: %s", strings.Join(open, "; "))
	}
	return nil
}
