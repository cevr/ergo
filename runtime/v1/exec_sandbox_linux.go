//go:build linux

package v1

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// The test's folder sandbox (docs/spec/rows.md, Test's ambient 5; TH1b).
// A command that exec.NewTestHost runs (ExecRunIn, ExecStartIn with a
// folder) starts in a new user and mount namespace, as internal/closure's
// sweep sandbox does: every mount is read-only, the case's folder is
// bound onto itself and made writable again, and the program then runs
// with no capability. So the folder is the only path the program can
// write, the paths it computes for itself included.
//
// Go cannot run code between the clone and the exec of a program, so the
// child is this binary again, the helper: execSandbox points cmd at
// /proc/self/exe with execSandboxArg, root, the program's path and its
// argv, and init below, which sees that argument, builds the sandbox and
// execs the program in place (execSandboxMain). The process id, the group
// and the exit are the program's, so ExecRun's stop protocol is unchanged.
// The helper reports a failure before the exec on fd 3 (a pipe whose write
// end is close-on-exec once the sandbox is built), so an empty read means
// the program runs.

// execSandboxArg is the helper's first argument; no program is started
// with it by hand.
const execSandboxArg = "-ergo.runtime.test-folder-sandbox"

// Capabilities (linux/capability.h) the helper holds in its user
// namespace, as ambient capabilities that survive its exec: CAP_SYS_ADMIN
// to mount, CAP_SYS_RESOURCE to set the namespace's limit of user
// namespaces, CAP_SETPCAP to empty the bounding set before the exec.
const (
	sbCapSetpcap     = 8
	sbCapSysAdmin    = 21
	sbCapSysResource = 24
)

// prctl options (linux/prctl.h), capset (linux/capability.h) and
// mount_setattr (linux/mount.h, Linux 5.12; one number on every
// architecture).
const (
	sbPrCapbsetDrop        = 24
	sbPrSetNoNewPrivs      = 38
	sbPrCapAmbient         = 47
	sbPrCapAmbientClearAll = 4
	sbCapabilityVersion3   = 0x20080522
	sbSysMountSetattr      = 442
	sbAtRecursive          = 0x8000
	sbMountAttrReadOnly    = 0x1
)

// execSandboxed is the parent's end of one sandboxed start.
type execSandboxed struct {
	status, w *os.File // the helper's report: read end, and the write end the parent closes after the start
}

// execSandbox rewrites cmd, a command of exec.NewTestHost's folder root,
// to start through the helper in new user and mount namespaces. A cmd
// whose program was not found is left as it is: its Start fails as
// ExecRun's does.
func execSandbox(cmd *exec.Cmd, root string) (execSandboxed, error) {
	if cmd.Err != nil {
		return execSandboxed{}, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		return execSandboxed{}, err
	}
	cmd.Args = append([]string{"/proc/self/exe", execSandboxArg, root, cmd.Path}, cmd.Args...)
	cmd.Path = "/proc/self/exe"
	cmd.ExtraFiles = []*os.File{w} // fd 3 in the helper
	uid, gid := os.Getuid(), os.Getgid()
	attr := cmd.SysProcAttr
	attr.Cloneflags = syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS
	attr.UidMappings = []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}}
	attr.GidMappings = []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}}
	attr.AmbientCaps = []uintptr{sbCapSetpcap, sbCapSysAdmin, sbCapSysResource}
	return execSandboxed{status: r, w: w}, nil
}

// on reports whether the start goes through the helper.
func (s execSandboxed) on() bool { return s.status != nil }

// abandon closes both ends: the start failed.
func (s execSandboxed) abandon() {
	if s.status != nil {
		_ = s.status.Close()
		_ = s.w.Close()
	}
}

// started reads the helper's report after a successful Start: nil once the
// program runs in the sandbox, or the reason the helper could not build it
// or exec the program, after which the helper exits.
func (s execSandboxed) started() error {
	if s.status == nil {
		return nil
	}
	_ = s.w.Close()
	defer s.status.Close()
	var b strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := s.status.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return fmt.Errorf("%s", b.String())
}

// sandboxReason is the exec.Failed reason of a sandboxed start that failed.
func sandboxReason(err error) string {
	return "the test's folder sandbox: " + err.Error()
}

func init() {
	if len(os.Args) >= 5 && os.Args[1] == execSandboxArg {
		execSandboxMain(os.Args[2], os.Args[3], os.Args[4:])
	}
}

// execSandboxMain is the helper: in its new namespaces it makes the
// sandbox, drops every capability and execs path with argv. It never
// returns.
func execSandboxMain(root, path string, argv []string) {
	report := os.NewFile(3, "sandbox-report")
	fail := func(err error) {
		_, _ = report.WriteString(err.Error())
		os.Exit(127)
	}
	// No process of the namespace makes another user namespace, in which
	// it would hold capabilities again (closure's S3).
	if err := os.WriteFile("/proc/sys/user/max_user_namespaces", []byte("0\n"), 0); err != nil {
		fail(fmt.Errorf("forbid new user namespaces: %w", err))
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		fail(fmt.Errorf("make the mounts private: %w", err))
	}
	if err := syscall.Mount(root, root, "", syscall.MS_BIND, ""); err != nil {
		fail(fmt.Errorf("bind the folder %s: %w", root, err))
	}
	if err := sbMountSetattr("/", sbAtRecursive, sbMountAttr{set: sbMountAttrReadOnly}); err != nil {
		fail(fmt.Errorf("make every mount read-only: %w", err))
	}
	if err := sbMountSetattr(root, 0, sbMountAttr{clr: sbMountAttrReadOnly}); err != nil {
		fail(fmt.Errorf("make the folder %s writable: %w", root, err))
	}
	// The working directory was entered before the folder's mount went on
	// top of it: enter it again, so it is the writable mount's.
	if wd, err := os.Getwd(); err != nil {
		fail(fmt.Errorf("the working directory: %w", err))
	} else if err := syscall.Chdir(wd); err != nil {
		fail(fmt.Errorf("enter the working directory %s again: %w", wd, err))
	}
	if err := sbDropPrivileges(); err != nil {
		fail(err)
	}
	syscall.CloseOnExec(3)
	err := syscall.Exec(path, argv, os.Environ())
	fail(fmt.Errorf("exec %s: %w", path, err))
}

// sbMountAttr is struct mount_attr (linux/mount.h).
type sbMountAttr struct {
	set, clr, propagation, usernsFD uint64
}

func sbMountSetattr(path string, flags int, attr sbMountAttr) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	cwd := -100 // AT_FDCWD
	_, _, e := syscall.Syscall6(sbSysMountSetattr, uintptr(cwd), uintptr(unsafe.Pointer(p)), uintptr(flags), uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	runtime.KeepAlive(p)
	if e != 0 {
		return &os.PathError{Op: "mount_setattr", Path: path, Err: e}
	}
	return nil
}

// sbDropPrivileges empties every capability set of this thread (bounding,
// ambient, inheritable, permitted, effective), sets no_new_privs, and locks
// the goroutine to the thread, so the exec from it runs the program with
// no capability and no way to gain one: it cannot remount, unmount or
// mount. internal/closure's dropPrivileges, the sweep's.
func sbDropPrivileges() error {
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
		if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, sbPrCapbsetDrop, uintptr(c), 0, 0, 0, 0); e != 0 {
			return fmt.Errorf("drop capability %d from the bounding set: %w", c, e)
		}
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, sbPrCapAmbient, sbPrCapAmbientClearAll, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("clear the ambient capabilities: %w", e)
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, sbPrSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("set no_new_privs: %w", e)
	}
	header := struct {
		version uint32
		pid     int32
	}{version: sbCapabilityVersion3}
	var sets [2]struct{ effective, permitted, inheritable uint32 }
	if _, _, e := syscall.RawSyscall(syscall.SYS_CAPSET, uintptr(unsafe.Pointer(&header)), uintptr(unsafe.Pointer(&sets[0])), 0); e != 0 {
		return fmt.Errorf("clear the capability sets: %w", e)
	}
	return nil
}
