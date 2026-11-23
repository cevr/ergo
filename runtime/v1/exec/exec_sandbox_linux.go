//go:build linux

package exec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// The test's folder sandbox (docs/spec/rows.md, Test's ambient 5; TH1b,
// TH1c). A command that exec.NewTestHost runs (ExecRunIn, ExecStartIn with
// a folder) starts in a user and mount namespace of the case, as
// internal/closure's sweep sandbox does: every mount is read-only, the
// case's folder is bound onto itself and made writable again, and the
// program runs with no capability. So the folder is the only path the
// program can write, the paths it computes for itself included.
//
// Go cannot run code between the clone and the exec of a program, so the
// namespaces are built by this binary again, the case's helper, started
// once per folder at its first command (execHelperFor): execHelperStart
// runs /proc/self/exe with execSandboxArg and the folder, and init below,
// which sees that argument, builds the sandbox, drops every capability and
// then serves the case's commands (execHelperMain). For each command the
// parent sends the program's path, directory, argv and environment, and
// its three standard files as SCM_RIGHTS, on a socket; the helper forks
// the program with CLONE_PARENT, so the program is in the helper's
// namespaces but is a child of this process, in a group of its own, and
// replies with its pid. The process id, the group, the exit and the reap
// are the program's and this process's, so ExecRun's stop protocol is
// unchanged. The case's end (TestFolderRemove, execSandboxEnd) closes the
// socket; the helper then exits, and the parent reaps it. A parent that
// ends closes the socket too.

// execSandboxArg is the helper's first argument; no program is started
// with it by hand.
const execSandboxArg = "-ergo.runtime.test-folder-sandbox"

// Capabilities (linux/capability.h) the helper holds in its user
// namespace while it builds the sandbox, as ambient capabilities that
// survive its exec: CAP_SYS_ADMIN to mount, CAP_SYS_RESOURCE to set the
// namespace's limit of user namespaces, CAP_SETPCAP to empty the bounding
// set.
const (
	sbCapSetpcap     = 8
	sbCapSysAdmin    = 21
	sbCapSysResource = 24
)

// prctl options (linux/prctl.h), capset (linux/capability.h) and
// mount_setattr (linux/mount.h, Linux 5.12; one number on every
// architecture).
const (
	sbPrSetDumpable        = 4
	sbPrCapbsetDrop        = 24
	sbPrSetNoNewPrivs      = 38
	sbPrCapAmbient         = 47
	sbPrCapAmbientClearAll = 4
	sbCapabilityVersion3   = 0x20080522
	sbSysMountSetattr      = 442
	sbAtRecursive          = 0x8000
	sbMountAttrReadOnly    = 0x1
)

// execSandboxReexecs counts the helpers started: one re-exec of this
// binary per case's folder that runs a command, however many it runs
// (TestExecSandboxReexecsOncePerCase).
var execSandboxReexecs atomic.Int64

// execSandboxError is a failure of the sandbox itself (no user namespace,
// a helper that could not build it or that ended), which ExecRun reports
// as exec.Failed with sandboxReason; the program never runs unconfined.
type execSandboxError struct{ err error }

func (e execSandboxError) Error() string { return e.err.Error() }

// sandboxReason is the exec.Failed reason of a sandboxed start that failed.
func sandboxReason(err error) string {
	return "the test's folder sandbox: " + err.Error()
}

// execHelper is the parent's end of one case's helper.
type execHelper struct {
	ready chan struct{} // closed once cmd, conn and err are set
	err   error         // the helper could not start or build the sandbox
	cmd   *exec.Cmd
	mu    sync.Mutex // one command at a time on conn
	conn  *net.UnixConn
}

// execHelpers holds the helper of each folder that ran a command and has
// not ended.
var execHelpers = struct {
	sync.Mutex
	m map[string]*execHelper
}{m: map[string]*execHelper{}}

// execHelperFor is the helper of the folder root, started at its first
// command.
func execHelperFor(root string) (*execHelper, error) {
	execHelpers.Lock()
	h, ok := execHelpers.m[root]
	if !ok {
		h = &execHelper{ready: make(chan struct{})}
		execHelpers.m[root] = h
	}
	execHelpers.Unlock()
	if !ok {
		h.start(root)
		close(h.ready)
	}
	<-h.ready
	if h.err != nil {
		return nil, h.err
	}
	return h, nil
}

// execSandboxEnd ends the helper of the folder root, if it has one: the
// case's end (TestFolderRemove). It returns once the helper is reaped.
func execSandboxEnd(root string) {
	execHelpers.Lock()
	h, ok := execHelpers.m[root]
	delete(execHelpers.m, root)
	execHelpers.Unlock()
	if !ok {
		return
	}
	<-h.ready
	if h.conn == nil {
		return
	}
	h.mu.Lock()
	_ = h.conn.Close() // the helper reads end of file and exits
	h.mu.Unlock()
	_ = h.cmd.Wait()
}

// start runs the helper for root and reads its first report: an empty one
// once the sandbox is built, or the reason it could not be.
func (h *execHelper) start(root string) {
	fail := func(err error) { h.err = execSandboxError{err} }
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		fail(err)
		return
	}
	parent, child := os.NewFile(uintptr(fds[0]), "sandbox-helper"), os.NewFile(uintptr(fds[1]), "sandbox-helper")
	defer parent.Close()
	cmd := exec.Command("/proc/self/exe", execSandboxArg, root)
	cmd.ExtraFiles = []*os.File{child} // fd 3 in the helper
	uid, gid := os.Getuid(), os.Getgid()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		AmbientCaps: []uintptr{sbCapSetpcap, sbCapSysAdmin, sbCapSysResource},
	}
	err = cmd.Start()
	child.Close()
	if err != nil {
		// The clone into new namespaces failed: the host gives no user
		// namespace (or a sandbox around this process forbids one).
		fail(err)
		return
	}
	execSandboxReexecs.Add(1)
	c, err := net.FileConn(parent)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		fail(err)
		return
	}
	conn := c.(*net.UnixConn)
	var n [4]byte
	_, err = io.ReadFull(conn, n[:])
	if err == nil && binary.LittleEndian.Uint32(n[:]) > 0 {
		text := make([]byte, binary.LittleEndian.Uint32(n[:]))
		_, err = io.ReadFull(conn, text)
		if err == nil {
			err = errors.New(string(text))
		}
	}
	if err != nil {
		_ = conn.Close()
		_ = cmd.Wait()
		fail(err)
		return
	}
	h.cmd, h.conn = cmd, conn
}

// execSandboxStart starts cmd, prepared as for its Start (Path, Args, Env,
// Dir, Stdin, Stdout, Stderr; Setpgid), through the helper of the folder
// root: the program is a child of this process in the case's sandbox. A
// cmd whose program was not found fails as its Start does.
func execSandboxStart(cmd *exec.Cmd, root string) (*execChild, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	h, err := execHelperFor(root)
	if err != nil {
		return nil, err
	}
	var files [3]*os.File
	var opened []*os.File // closed once the helper holds its copies
	defer func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}()
	null := func(flag int) (*os.File, error) {
		f, err := os.OpenFile(os.DevNull, flag, 0)
		if err == nil {
			opened = append(opened, f)
		}
		return f, err
	}
	child := &execChild{}
	switch in := cmd.Stdin.(type) {
	case nil:
		if files[0], err = null(os.O_RDONLY); err != nil {
			return nil, err
		}
	case *os.File:
		files[0] = in
	default:
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		opened = append(opened, r)
		files[0], child.stdin = r, w
	}
	for i, out := range []io.Writer{cmd.Stdout, cmd.Stderr} {
		switch out := out.(type) {
		case nil:
			if files[i+1], err = null(os.O_WRONLY); err != nil {
				child.closeStdin()
				return nil, err
			}
		case *os.File:
			files[i+1] = out
		default:
			panic("exec: a sandboxed program's output is a file")
		}
	}
	dir := cmd.Dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			child.closeStdin()
			return nil, err
		}
	}
	// The program's whole environment is cmd.Env (ExecCommand): nil is
	// none, never this process's.
	env := cmd.Env
	pid, err := h.spawn(cmd.Path, dir, cmd.Args, env, files)
	if err != nil {
		child.closeStdin()
		return nil, err
	}
	child.pid = pid
	if child.proc, err = os.FindProcess(pid); err != nil {
		panic("exec: the sandboxed program " + strconv.Itoa(pid) + ": " + err.Error())
	}
	if child.stdin != nil {
		go func(w *os.File, in io.Reader) {
			_, _ = io.Copy(w, in)
			child.closeStdin()
		}(child.stdin, cmd.Stdin)
	}
	return child, nil
}

// spawn sends one command to the helper and reads its reply: the program's
// pid, or the errno of a start that failed (a failure of the program's
// exec, reported as os/exec reports it), or an execSandboxError when the
// helper has ended.
func (h *execHelper) spawn(path, dir string, argv, env []string, files [3]*os.File) (int, error) {
	var payload []byte
	put := func(s string) { payload = append(append(payload, s...), 0) }
	put(path)
	put(dir)
	put(strconv.Itoa(len(argv)))
	for _, a := range argv {
		put(a)
	}
	for _, e := range env {
		put(e)
	}
	var head [4]byte
	binary.LittleEndian.PutUint32(head[:], uint32(len(payload)))
	rights := syscall.UnixRights(int(files[0].Fd()), int(files[1].Fd()), int(files[2].Fd()))
	h.mu.Lock()
	defer h.mu.Unlock()
	ended := func(err error) (int, error) {
		return 0, execSandboxError{fmt.Errorf("the case's helper ended: %w", err)}
	}
	if _, _, err := h.conn.WriteMsgUnix(head[:], rights, nil); err != nil {
		return ended(err)
	}
	runtime.KeepAlive(files)
	if _, err := h.conn.Write(payload); err != nil {
		return ended(err)
	}
	var reply [8]byte
	if _, err := io.ReadFull(h.conn, reply[:]); err != nil {
		return ended(err)
	}
	pid := int32(binary.LittleEndian.Uint32(reply[:4]))
	if errno := syscall.Errno(binary.LittleEndian.Uint32(reply[4:])); errno != 0 {
		return 0, &os.PathError{Op: "fork/exec", Path: path, Err: errno}
	}
	return int(pid), nil
}

func init() {
	if len(os.Args) == 3 && os.Args[1] == execSandboxArg {
		execHelperMain(os.Args[2])
	}
}

// execHelperFD is the helper's end of the socket.
const execHelperFD = 3

// execHelperMain is the helper: in its new namespaces it makes the
// sandbox, drops every capability, reports that it is ready and serves
// the case's commands until the parent closes the socket. It never
// returns.
func execHelperMain(root string) {
	syscall.CloseOnExec(execHelperFD) // no program inherits it
	tell := func(text string) {
		var n [4]byte
		binary.LittleEndian.PutUint32(n[:], uint32(len(text)))
		_ = sbWriteAll(append(n[:], text...))
	}
	fail := func(err error) {
		tell(err.Error())
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
	// The helper's own directory is the folder's writable mount; each
	// program enters its directory after its fork, inside the namespace.
	if err := syscall.Chdir(root); err != nil {
		fail(fmt.Errorf("enter the folder %s: %w", root, err))
	}
	// A program runs as the helper's user: not dumpable, the helper is out
	// of its reach (no ptrace, no /proc/<helper>/fd or mem), so it cannot
	// use a thread of the helper that still holds a capability.
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, sbPrSetDumpable, 0, 0, 0, 0, 0); e != 0 {
		fail(fmt.Errorf("make the helper not dumpable: %w", e))
	}
	// Capabilities are a thread's: this goroutine stays on the thread
	// that drops them, and each fork below is from that thread.
	if err := sbDropPrivileges(); err != nil {
		fail(err)
	}
	tell("")
	for {
		path, dir, argv, env, files, err := execHelperRead()
		if err != nil {
			os.Exit(0) // the parent closed the socket: the case ended
		}
		pid, errno := execHelperFork(path, dir, argv, env, files)
		for _, fd := range files {
			_ = syscall.Close(fd)
		}
		var reply [8]byte
		binary.LittleEndian.PutUint32(reply[:4], uint32(pid))
		binary.LittleEndian.PutUint32(reply[4:], uint32(errno))
		if sbWriteAll(reply[:]) != nil {
			os.Exit(0)
		}
	}
}

// execHelperFork starts one program, a child of the helper's parent
// (CLONE_PARENT) in a group of its own. A program that is missing or
// cannot be run is refused before the fork, so that no failed child is
// left unreaped: the parent never learns its pid.
func execHelperFork(path, dir string, argv, env []string, files []int) (int, syscall.Errno) {
	const xOK = 1
	if err := syscall.Access(path, xOK); err != nil {
		return 0, sbErrno(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, sbErrno(err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return 0, syscall.EACCES // execve's answer for what is not a file
	}
	if len(files) != 3 {
		return 0, syscall.EBADF
	}
	pid, err := syscall.ForkExec(path, argv, &syscall.ProcAttr{
		Dir:   dir,
		Env:   env,
		Files: []uintptr{uintptr(files[0]), uintptr(files[1]), uintptr(files[2])},
		Sys:   &syscall.SysProcAttr{Setpgid: true, Cloneflags: syscall.CLONE_PARENT},
	})
	if err != nil {
		return 0, sbErrno(err)
	}
	return pid, 0
}

func sbErrno(err error) syscall.Errno {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	return syscall.EINVAL
}

// execHelperRead reads one command from the socket: a 4-byte length with
// the three files as SCM_RIGHTS, then the NUL-terminated path, directory,
// argument count, arguments and environment.
func execHelperRead() (path, dir string, argv, env []string, files []int, err error) {
	var head [4]byte
	oob := make([]byte, syscall.CmsgSpace(3*4))
	var n, oobn int
	for {
		n, oobn, _, _, err = syscall.Recvmsg(execHelperFD, head[:], oob, syscall.MSG_CMSG_CLOEXEC)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return
	}
	if n == 0 {
		err = io.EOF
		return
	}
	msgs, perr := syscall.ParseSocketControlMessage(oob[:oobn])
	if perr == nil {
		for _, m := range msgs {
			fds, ferr := syscall.ParseUnixRights(&m)
			if ferr == nil {
				files = append(files, fds...)
			}
		}
	}
	if err = sbReadFull(head[n:]); err != nil {
		return
	}
	payload := make([]byte, binary.LittleEndian.Uint32(head[:]))
	if err = sbReadFull(payload); err != nil {
		return
	}
	fields := strings.Split(strings.TrimSuffix(string(payload), "\x00"), "\x00")
	if len(fields) < 3 {
		err = io.ErrUnexpectedEOF
		return
	}
	argc, aerr := strconv.Atoi(fields[2])
	if aerr != nil || argc < 0 || 3+argc > len(fields) {
		err = io.ErrUnexpectedEOF
		return
	}
	path, dir = fields[0], fields[1]
	argv = fields[3 : 3+argc]
	env = fields[3+argc:]
	return
}

// sbReadFull and sbWriteAll are io.ReadFull and a full write on the
// helper's socket, with blocking syscalls on the helper's own thread.
func sbReadFull(b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Read(execHelperFD, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		b = b[n:]
	}
	return nil
}

func sbWriteAll(b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(execHelperFD, b)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
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
// the goroutine to the thread, so each program forked from it runs with no
// capability and no way to gain one: it cannot remount, unmount or mount.
// internal/closure's dropPrivileges, the sweep's.
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
