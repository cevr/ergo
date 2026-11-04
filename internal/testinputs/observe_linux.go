//go:build linux && (amd64 || arm64)

package testinputs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// The observation uses seccomp user notification (seccomp_unotify(2)). The
// test process installs a filter that suspends each file system call of
// interest, in itself and in every process it starts, and hands the
// filter's listener to the observing parent. The parent reads the call's
// path from the suspended thread and lets the call continue unchanged. No
// thread is ptrace-stopped, so signals, threads and debuggers behave as
// without observation, and several parent threads serve calls at once.

// A kind says what a notified system call reveals.
type kind uint8

const (
	kindOpen           kind = iota + 1 // reads a file or lists a directory, unless opened write-only
	kindOpenat2                        // openat with its flags in a struct open_how
	kindStat                           // consults a path's existence or metadata
	kindChdir                          // looks a directory up and makes it the working directory
	kindSocket                         // looks up the AF_UNIX socket a struct sockaddr names
	kindSocketMsg                      // kindSocket with the address in a struct msghdr
	kindSocketMsgs                     // kindSocket with addresses in an array of struct mmsghdr
	kindExec                           // runs a file with an environment
	kindClone3                         // may create namespaces; its flags are in memory
	kindSeccomp                        // may install a listener that takes calls from this one
	kindNamespaceClone                 // clone with namespace flags (the filter passes others)
	kindUnobservable                   // makes later paths mean something the observer cannot see
	kindRefused                        // fails with ENOSYS in the process, without a notification
)

// A call describes a notified system call: which argument holds the
// directory descriptor (-1 for the working directory), the path, the open
// flags (or struct open_how), the AT_* flags (0 for none) and the
// environment. A call that names two paths (rename, link) has second set,
// with the second path's directory descriptor and path in dirfd2 and path2.
// A socket call has its address (kindSocket), struct msghdr
// (kindSocketMsg) or array of struct mmsghdr (kindSocketMsgs) in path, and
// the address's length or the array's length in flags.
type call struct {
	nr                               uint32
	kind                             kind
	dirfd, path, flags, atFlags, env int
	second                           bool
	dirfd2, path2                    int
	name                             string
}

const (
	prSetNoNewPrivs           = 38
	seccompModeFilter         = 1
	seccompFlagTsync          = 1 << 0
	seccompFlagNewListener    = 1 << 3
	seccompFlagTsyncESRCH     = 1 << 4
	seccompFlagWaitKillable   = 1 << 5
	seccompRetAllow           = 0x7fff0000
	seccompRetUserNotif       = 0x7fc00000
	seccompRetErrno           = 0x00050000
	seccompIoctlNotifRecv     = 0xc0502100
	seccompIoctlNotifSend     = 0xc0182101
	seccompIoctlNotifIDValid  = 0x40082102
	seccompNotifContinue      = 1
	seccompIoctlNotifSetFlags = 0x40082104
	seccompNotifSyncWakeUp    = 1
	atFdcwd                   = -100
	atEmptyPath               = 0x1000
	resolveInRoot             = 0x10
	pageSize                  = 4096
	pathMax                   = 4096
	afUnix                    = 1
	sockaddrUnSize            = 110 // struct sockaddr_un: a u16 family and a 108-byte path
	mmsghdrSize               = 64  // struct mmsghdr: a struct msghdr (56 bytes) and a u32, padded
	maxMessages               = 1024
	// childSocket is the child's end of the socket to the observer, its
	// first extra file.
	childSocket = 3
	// System calls from 424 (Linux 5.1) on have one number on every
	// architecture.
	sysPidfdOpen  = 434
	sysPidfdGetfd = 438
	// namespaceFlags are the clone flags that make a namespace: CLONE_NEWNS,
	// NEWCGROUP, NEWUTS, NEWIPC, NEWUSER, NEWPID and NEWNET.
	namespaceFlags = 0x00020000 | 0x02000000 | 0x04000000 | 0x08000000 | 0x10000000 | 0x20000000 | 0x40000000
)

// listenerVariable names the descriptor on which the test process sends its
// filter's listener to the observing parent.
const listenerVariable = "ERGO_TESTINPUTS_LISTENER"

// filteredVariable marks the test process that the filtered child started
// by exec, with the filter in place from its first instruction. Its value is
// the descriptor of the child's end of the socket to the observer, kept open
// across the exec as proof that the observer is there.
const filteredVariable = "ERGO_TESTINPUTS_FILTERED"

// armedPath is the path the test process looks up once the observer holds
// its listener. The parent sees the lookup only if the filter reports it.
const armedPath = "/nonexistent/ergo-testinputs/armed"

// observed is true in the observed test process, which runs the tests itself.
var observed bool

// impostor, when not empty, says why this process carries one of the
// observer's variables without an observer behind it: inherited from a shell
// or a runner, say. Such a process is not observed, and MainFunc refuses its
// result.
var impostor string

// init runs in the child the observer starts. It installs the filter, lets
// the observer take its listener, and runs the test binary again by exec:
// the filter survives exec, so the package initializers that Go ran before
// this one run again observed. Only the exec'd process reports itself
// armed; if the exec fails, the observer finds no armed report and refuses
// the result.
//
// The observer takes the listener itself, with pidfd_getfd, once the child
// writes its number on the socket. Sending it by SCM_RIGHTS would need a
// sendmsg the filter lets through unnotified, since no one serves the
// listener before it arrives, and every process the child started before a
// second filter closed that gap could pass a socket address by the
// observer. write and read are not filtered, so the filter has no exception.
// The observer first takes a descriptor it knows the child holds, and says
// whether it can before the child installs anything: where it may not
// (Yama's ptrace_scope 2 or 3, an undumpable binary), the tests run
// unfiltered and the result is refused, as on a kernel without seccomp.
//
// Both variables are trusted only once the process proves its observer
// exists: the descriptor a variable names is a socket whose peer is this
// process's parent, which created the pair (SO_PEERCRED), and the exec'd
// process also runs under a seccomp filter.
func init() {
	if value, ok := os.LookupEnv(filteredVariable); ok {
		os.Unsetenv(filteredVariable)
		os.Unsetenv(listenerVariable) // a runner that set both leaves neither to the tests
		socket, err := observerSocket(value)
		if err == nil && !filtered() {
			err = errors.New("no seccomp filter")
		}
		if err != nil {
			impostor = filteredVariable + " is set, but " + err.Error()
			return
		}
		syscall.Close(socket)
		observed = true
		syscall.Access(armedPath, 0)
		return
	}
	value, ok := os.LookupEnv(listenerVariable)
	if !ok {
		return
	}
	os.Unsetenv(listenerVariable)
	socket, err := observerSocket(value)
	if err != nil {
		impostor = listenerVariable + " is set, but " + err.Error()
		return
	}
	observed = true
	defer syscall.Close(socket) // reached only if the tests run here, unobserved
	if verdict, _ := receive(socket); verdict != "go" {
		return // the observer cannot take a descriptor from this process, and says why itself
	}
	listener, err := installFilter(filter())
	if err != nil {
		syscall.Write(socket, []byte(noFilter+err.Error()))
		return
	}
	syscall.Write(socket, []byte(listenerMessage+strconv.Itoa(listener)))
	reply, _ := receive(socket)
	syscall.Close(listener) // the observer holds its own copy
	if reply != "go" {
		// The filter waits on a listener no one serves: every filtered call
		// of this process would fail or hang.
		fmt.Fprintln(os.Stderr, "testinputs: the observer did not take the filter's listener: "+reply)
		os.Exit(1)
	}
	if executable, err := os.Executable(); err == nil {
		syscall.Exec(executable, os.Args, append(os.Environ(), filteredVariable+"="+value))
	}
}

// The messages on the socket between the observer and its child. The
// observer sends "go", or the reason it refuses, before the child installs
// its filter and again once it holds the listener; the child sends
// listenerMessage and its listener's number, or noFilter and the reason.
const (
	listenerMessage = "listener "
	noFilter        = "no filter: "
)

// receive reads one message from the socket; the empty string means the
// peer closed it.
func receive(socket int) (string, error) {
	message := make([]byte, 512)
	for {
		n, err := syscall.Read(socket, message)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return "", err
		}
		return string(message[:n]), nil
	}
}

// observerSocket returns the descriptor a variable names if it is a socket
// whose peer is this process's parent.
func observerSocket(value string) (int, error) {
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 0 {
		return -1, fmt.Errorf("%q names no descriptor", value)
	}
	cred, err := syscall.GetsockoptUcred(fd, syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	if err != nil {
		return -1, fmt.Errorf("descriptor %d is no socket to an observer: %v", fd, err)
	}
	if int(cred.Pid) != os.Getppid() {
		return -1, fmt.Errorf("descriptor %d leads to process %d, not to the parent %d", fd, cred.Pid, os.Getppid())
	}
	return fd, nil
}

// filtered reports whether this process runs under a seccomp filter.
func filtered() bool {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "Seccomp:"); ok {
			return strings.TrimSpace(value) == "2"
		}
	}
	return false
}

type sockFilter struct {
	code   uint16
	jt, jf uint8
	k      uint32
}

type sockFprog struct {
	len    uint16
	filter *sockFilter
}

// Classic BPF instructions and struct seccomp_data offsets.
const (
	bpfLdAbs  = 0x20 // BPF_LD | BPF_W | BPF_ABS
	bpfJeqK   = 0x15 // BPF_JMP | BPF_JEQ | BPF_K
	bpfJgtK   = 0x25 // BPF_JMP | BPF_JGT | BPF_K
	bpfJgeK   = 0x35 // BPF_JMP | BPF_JGE | BPF_K
	bpfJsetK  = 0x45 // BPF_JMP | BPF_JSET | BPF_K
	bpfRetK   = 0x06 // BPF_RET | BPF_K
	offNr     = 0
	offArc    = 4
	offArg    = 16 // args[0], its low half on a little-endian machine
	argLength = 8
)

// filter is the seccomp program: notify each call in calls; notify calls of
// another architecture or ABI, which the observer cannot decode, and calls
// newer than the table, which it cannot classify; fail io_uring_setup;
// allow the rest. Every branch that loads an argument ends in a return, so
// the number stays loaded for the comparisons after it.
func filter() []sockFilter {
	program := []sockFilter{
		{code: bpfLdAbs, k: offArc},
		{code: bpfJeqK, jt: 1, k: auditArch},
		{code: bpfRetK, k: seccompRetUserNotif},
		{code: bpfLdAbs, k: offNr},
	}
	if checkX32SyscallBits {
		program = append(program,
			sockFilter{code: bpfJgeK, jf: 1, k: x32SyscallBit},
			sockFilter{code: bpfRetK, k: seccompRetUserNotif})
	}
	for _, c := range calls {
		switch {
		case c.kind == kindRefused:
			program = append(program,
				sockFilter{code: bpfJeqK, jf: 1, k: c.nr},
				sockFilter{code: bpfRetK, k: seccompRetErrno | uint32(syscall.ENOSYS)})
		case c.kind == kindNamespaceClone:
			// Notify if args[0] asks for a namespace.
			program = append(program,
				sockFilter{code: bpfJeqK, jf: 4, k: c.nr},
				sockFilter{code: bpfLdAbs, k: offArg},
				sockFilter{code: bpfJsetK, jf: 1, k: namespaceFlags},
				sockFilter{code: bpfRetK, k: seccompRetUserNotif},
				sockFilter{code: bpfRetK, k: seccompRetAllow})
		case c.kind == kindSocket:
			// Notify only if the address pointer is not NULL: sendto on a
			// connected socket names no address.
			program = append(program,
				sockFilter{code: bpfJeqK, jf: 6, k: c.nr},
				sockFilter{code: bpfLdAbs, k: offArg + uint32(c.path)*argLength},
				sockFilter{code: bpfJeqK, jf: 3, k: 0},
				sockFilter{code: bpfLdAbs, k: offArg + uint32(c.path)*argLength + 4},
				sockFilter{code: bpfJeqK, jf: 1, k: 0},
				sockFilter{code: bpfRetK, k: seccompRetAllow},
				sockFilter{code: bpfRetK, k: seccompRetUserNotif})
		default:
			program = append(program,
				sockFilter{code: bpfJeqK, jf: 1, k: c.nr},
				sockFilter{code: bpfRetK, k: seccompRetUserNotif})
		}
	}
	return append(program,
		sockFilter{code: bpfJgtK, jf: 1, k: highestKnownCall},
		sockFilter{code: bpfRetK, k: seccompRetUserNotif},
		sockFilter{code: bpfRetK, k: seccompRetAllow})
}

// installFilter installs a filter on every thread of this process and
// returns its listener.
func installFilter(program []sockFilter) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return -1, fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	prog := sockFprog{len: uint16(len(program)), filter: &program[0]}
	flags := uintptr(seccompFlagTsync | seccompFlagTsyncESRCH | seccompFlagNewListener)
	// A thread waiting on the parent then ignores all but fatal signals, so
	// the runtime's preemption signals do not restart every call; kernels
	// before 5.19 lack the flag.
	listener, _, errno := syscall.RawSyscall(sysSeccomp, seccompModeFilter, flags|seccompFlagWaitKillable, uintptr(unsafe.Pointer(&prog)))
	if errno == syscall.EINVAL {
		listener, _, errno = syscall.RawSyscall(sysSeccomp, seccompModeFilter, flags, uintptr(unsafe.Pointer(&prog)))
	}
	runtime.KeepAlive(program)
	if errno != 0 {
		return -1, fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER): %w", errno)
	}
	return int(listener), nil
}

// observe runs this test binary again, with the same arguments, as an
// observed child, and returns its exit code and what its process tree
// accessed. An error means the child did not run the tests.
func observe(root string) (int, *recorder, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, nil, err
	}
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, nil, err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "testinputs"), os.NewFile(uintptr(pair[1]), "testinputs")
	defer parent.Close()
	command := exec.Command(executable, os.Args[1:]...)
	command.Args[0] = os.Args[0]
	identityFile := os.Getenv(IdentityVariable)
	// The identity file is the observer's; the tests do not receive it.
	command.Env = nil
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, IdentityVariable+"=") {
			command.Env = append(command.Env, kv)
		}
	}
	command.Env = append(command.Env, listenerVariable+"="+strconv.Itoa(childSocket))
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.ExtraFiles = []*os.File{child}
	// The test process dies with this one: cmd/go kills only the process it
	// started when a test times out, and an orphan's filtered calls would
	// fail once the listener closes. The death signal follows the thread
	// that starts the child, so that thread stays locked until the child is
	// reaped.
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err = command.Start()
	child.Close()
	if err != nil {
		return 0, nil, err
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		// The tests run in the child: cmd/go's timeout (SIGQUIT) and
		// interrupts go to it.
		for sig := range signals {
			command.Process.Signal(sig)
		}
	}()
	o := newObserver(root)
	o.child, o.executable = command.Process.Pid, executable
	if identityFile != "" {
		// Identity mode starts before the child takes its filter, so
		// before the tests start.
		if id, err := newIdentity(identityFile, o.rec); err != nil {
			o.rec.refuse("identity mode: " + err.Error())
		} else {
			o.rec.ident = id
		}
	}
	listener, err := takeListener(int(parent.Fd()), command.Process.Pid)
	if err != nil {
		o.rec.fail(err.Error())
	} else {
		o.serve(listener)
	}
	err = command.Wait()
	o.rec.stop()
	o.rec.finishIdentity()
	if !o.armed.Load() {
		o.rec.refuse("the test process never reported its armed filter")
	}
	state := command.ProcessState
	if state == nil {
		o.rec.refuse(fmt.Sprintf("wait for the test process: %v", err))
		return 1, o.rec, nil
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		fmt.Fprintf(os.Stderr, "testinputs: test process: %v\n", status.Signal())
		return 128 + int(status.Signal()), o.rec, nil
	}
	return state.ExitCode(), o.rec, nil
}

// takeListener takes the child's filter listener with pidfd_getfd (Linux
// 5.6), which needs the access ptrace would (PTRACE_MODE_ATTACH_REALCREDS):
// the same user, a dumpable child, and Yama's consent, which ptrace_scope 1
// gives a parent. It first takes the child's end of the socket, which the
// child holds from its start, and tells the child whether to install its
// filter, so a refusal leaves the child unfiltered instead of waiting on a
// listener no one serves. The child is this process's unreaped child, so
// its pid names it until Wait.
func takeListener(socket, pid int) (int, error) {
	refuse := func(err error) (int, error) {
		syscall.Write(socket, []byte(err.Error()))
		return -1, err
	}
	pidfd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if errno != 0 {
		return refuse(fmt.Errorf("pidfd_open on the test process: %w", errno))
	}
	defer syscall.Close(int(pidfd))
	probe, err := takeDescriptor(int(pidfd), childSocket)
	if err != nil {
		return refuse(err)
	}
	syscall.Close(probe) // its end of the socket would hide the child's exit
	if _, err := syscall.Write(socket, []byte("go")); err != nil {
		return -1, fmt.Errorf("start the test process's filter: %w", err)
	}
	message, err := receive(socket)
	if err != nil {
		return -1, fmt.Errorf("receive the filter listener: %w", err)
	}
	number, ok := strings.CutPrefix(message, listenerMessage)
	if !ok {
		if reason, ok := strings.CutPrefix(message, noFilter); ok {
			return -1, errors.New("the test process installed no filter: " + reason)
		}
		return -1, errors.New("the test process sent no filter listener")
	}
	descriptor, err := strconv.Atoi(number)
	if err != nil {
		return refuse(fmt.Errorf("the test process sent no filter listener, but %q", message))
	}
	listener, err := takeDescriptor(int(pidfd), descriptor)
	if err != nil {
		return refuse(err)
	}
	if _, err := syscall.Write(socket, []byte("go")); err != nil {
		syscall.Close(listener)
		return -1, fmt.Errorf("release the test process: %w", err)
	}
	return listener, nil
}

// takeDescriptor copies one of the child's descriptors into this process.
func takeDescriptor(pidfd, descriptor int) (int, error) {
	fd, _, errno := syscall.Syscall(sysPidfdGetfd, uintptr(pidfd), uintptr(descriptor), 0)
	if errno != 0 {
		return -1, fmt.Errorf("take descriptor %d from the test process (pidfd_getfd): %w", descriptor, errno)
	}
	return int(fd), nil
}

// An observer serves the listener's notifications.
type observer struct {
	rec   *recorder
	dirs  linkedDirs
	armed atomic.Bool

	// The child's exec of the test binary from its init (see init) is not
	// the test's: it is skipped once, from the child's main thread, where Go
	// runs initializers.
	child      int
	executable string
	refiltered atomic.Bool
}

func newObserver(root string) *observer {
	return &observer{rec: newRecorder(root), dirs: linkedDirs{aliases: map[string][]string{}}}
}

// notification is struct seccomp_notif.
type notification struct {
	id    uint64
	pid   uint32
	flags uint32
	nr    int32
	arch  uint32
	ip    uint64
	args  [6]uint64
}

// response is struct seccomp_notif_resp.
type response struct {
	id    uint64
	val   int64
	error int32
	flags uint32
}

// serve starts the workers, each on its own thread: wait for a
// notification, record it, let the call continue. With synchronous wake-up
// (Linux 6.6) the kernel switches between a notifying thread and a waiting
// worker on one CPU, without a trip through the scheduler. The workers
// outlive the test process, continuing the calls of processes it left
// running until this process exits; what they record after stop is ignored.
func (o *observer) serve(listener int) {
	// The flags go by value. Older kernels refuse them and wake through the
	// scheduler.
	syscall.Syscall(syscall.SYS_IOCTL, uintptr(listener), seccompIoctlNotifSetFlags, seccompNotifSyncWakeUp)
	// Eight workers measured fastest on the 16-CPU workbox (2026-10-11, a
	// subset of the legacy compiler's tests, removed by #205: 2 workers
	// 40-47 s, 4 35-42 s, 8 34-39 s, 16 36-64 s, 32 42-44 s, unobserved
	// 25-33 s).
	for range min(runtime.NumCPU(), 8) {
		go func() {
			runtime.LockOSThread()
			buffer := make([]byte, pageSize)
			for {
				var n notification
				if err := ioctl(listener, seccompIoctlNotifRecv, unsafe.Pointer(&n)); err != nil {
					if err == syscall.EINTR || err == syscall.ENOENT {
						continue // interrupted, or the caller died first
					}
					o.rec.fail(fmt.Sprintf("receive a notification: %v", err))
					return
				}
				o.handle(listener, &n, buffer)
				r := response{id: n.id, flags: seccompNotifContinue}
				err := ioctl(listener, seccompIoctlNotifSend, unsafe.Pointer(&r))
				for err == syscall.EINTR {
					err = ioctl(listener, seccompIoctlNotifSend, unsafe.Pointer(&r))
				}
				if err != nil && err != syscall.ENOENT {
					o.rec.fail(fmt.Sprintf("continue a notified call: %v", err))
				}
			}
		}()
	}
}

// handle records one notified call. A read that fails because the thread
// died or was interrupted meanwhile (the notification is no longer valid)
// is dropped: the call made no access, and a restarted call is notified
// again. A read that succeeds is recorded without that check: a thread
// waiting on its notification cannot exit, so its ID names it.
func (o *observer) handle(listener int, n *notification, buffer []byte) {
	tid := int(n.pid)
	if n.arch != auditArch || (checkX32SyscallBits && n.nr&x32SyscallBit != 0) {
		o.rec.fail("a system call of another architecture or ABI")
		return
	}
	c, found := callsByNumber[n.nr]
	if !found {
		if n.nr > highestKnownCall {
			o.rec.fail(fmt.Sprintf("system call %d is newer than the observer's table", n.nr))
		} else {
			o.rec.fail(fmt.Sprintf("unexpected system call %d", n.nr))
		}
		return
	}
	switch c.kind {
	case kindUnobservable, kindNamespaceClone:
		o.rec.fail(c.name)
		return
	case kindSocket, kindSocketMsg, kindSocketMsgs:
		o.socket(listener, n, c, buffer)
		return
	case kindSeccomp:
		// seccomp(SECCOMP_SET_MODE_FILTER, flags) with a new listener.
		if n.args[0] == seccompModeFilter && n.args[1]&seccompFlagNewListener != 0 {
			o.rec.fail("a process installed its own seccomp listener")
		}
		return
	case kindClone3:
		// struct clone_args begins with the u64 flags.
		var flags [8]byte
		if got, err := readMemory(tid, n.args[0], flags[:]); err != nil || got < 8 {
			return // the kernel faults the call too
		}
		if *(*uint64)(unsafe.Pointer(&flags[0]))&namespaceFlags != 0 {
			o.rec.fail("clone3 made a namespace")
		}
		return
	}
	a := access{tid: tid, args: n.args, call: c, buffer: buffer}
	first, ok, err := a.path()
	if err != nil {
		if valid(listener, n.id) {
			o.rec.fail(err.Error())
		}
		return
	}
	if ok && first.path == armedPath {
		o.armed.Store(true)
		return
	}
	if c.kind == kindExec && ok && tid == o.child && first.path == o.executable && o.refiltered.CompareAndSwap(false, true) {
		return
	}
	read := true
	switch c.kind {
	case kindOpen:
		read = int(n.args[c.flags])&syscall.O_ACCMODE != syscall.O_WRONLY
	case kindOpenat2:
		var how [8]byte // struct open_how begins with the u64 flags
		if got, err := readMemory(tid, n.args[c.flags], how[:]); err == nil && got == 8 {
			read = int(*(*uint64)(unsafe.Pointer(&how[0])))&syscall.O_ACCMODE != syscall.O_WRONLY
		}
	}
	// The second path of rename and link is decoded even when the first names
	// none: linkat(fd, "", newdirfd, newpath, AT_EMPTY_PATH) still looks the
	// new path up.
	var second []string
	if c.second {
		b := access{tid: tid, args: n.args, call: call{kind: kindStat, dirfd: c.dirfd2, path: c.path2}, buffer: buffer}
		other, ok, err := b.path()
		if err != nil {
			if valid(listener, n.id) {
				o.rec.fail(err.Error())
			}
			return
		}
		if ok {
			second = o.names(tid, other)
		}
	}
	var paths []string
	if ok {
		paths = o.names(tid, first)
	}
	var names []string
	if c.kind == kindExec && ok {
		names, err = a.environment(n.args[c.env])
		if err != nil && valid(listener, n.id) {
			o.rec.fail(err.Error())
			return
		}
	}
	switch c.kind {
	case kindOpen, kindOpenat2:
		for _, path := range paths {
			o.rec.open(path, read)
		}
		if read { // a directory opens for reading only
			for _, path := range paths {
				o.enter(path, true)
			}
		}
	case kindStat:
		for _, path := range append(paths, second...) {
			o.rec.stat(path)
		}
	case kindChdir:
		for _, path := range paths {
			o.rec.stat(path)
		}
		for _, path := range paths {
			o.enter(path, false)
		}
	case kindExec:
		for i, path := range paths {
			if i == 0 {
				o.rec.exec(path, names)
			} else {
				o.rec.open(path, true)
			}
		}
	}
}

// names returns the paths under which to record a located path: the path
// with /proc and /dev/fd magic links read, and, for a name relative to a
// working directory or a directory descriptor a process reached through a
// link (linkedDirs), the same name under that link too. The kernel resolved
// the name against the directory itself, which the first path records; the
// second records the link, so retargeting it changes the key.
func (o *observer) names(tid int, l located) []string {
	paths := []string{magic(tid, l.path)}
	if l.base != "" {
		for _, logical := range o.dirs.logical(l.base) {
			paths = append(paths, strings.TrimSuffix(logical, "/")+"/"+l.name)
		}
	}
	return paths
}

// socket records the AF_UNIX socket file a call connects to, binds or sends
// to, as a lookup: the kernel resolves the address's path like any other,
// relative to the working directory. An abstract or unnamed address, or
// another family, names no file.
func (o *observer) socket(listener int, n *notification, c call, buffer []byte) {
	tid := int(n.pid)
	var addresses [][2]uint64 // pointer and length
	var headers []uint64      // struct msghdr addresses
	switch c.kind {
	case kindSocket:
		addresses = append(addresses, [2]uint64{n.args[c.path], uint64(uint32(n.args[c.flags]))})
	case kindSocketMsg:
		headers = append(headers, n.args[c.path])
	case kindSocketMsgs:
		for i := range min(uint64(uint32(n.args[c.flags])), maxMessages) {
			headers = append(headers, n.args[c.path]+i*mmsghdrSize)
		}
	}
	for _, header := range headers {
		address, ok, err := messageAddress(tid, header)
		if err != nil {
			if valid(listener, n.id) {
				o.rec.fail(err.Error())
			}
			return
		}
		if ok {
			addresses = append(addresses, address)
		}
	}
	a := access{tid: tid, args: n.args, call: c, buffer: buffer}
	for _, address := range addresses {
		name, err := socketPath(tid, address[0], address[1])
		if err == nil && name == "" {
			continue
		}
		var l located
		ok := true
		if err == nil {
			l, ok, err = a.locate(name, atFdcwd, false)
		}
		if err != nil {
			if valid(listener, n.id) {
				o.rec.fail(err.Error())
			}
			return
		}
		if ok {
			for _, path := range o.names(tid, l) {
				o.rec.stat(path)
			}
		}
	}
}

// messageAddress reads the address and its length from a struct msghdr, or
// false if it names none. Memory the kernel cannot read either, or a thread
// that died, names none: the call faults or never happens. Any other error,
// such as EPERM from a process that made itself undumpable, leaves the
// header unread while the kernel reads it, so it is returned.
func messageAddress(tid int, header uint64) ([2]uint64, bool, error) {
	var raw [16]byte // msg_name, then the u32 msg_namelen
	got, err := readMemory(tid, header, raw[:])
	if err != nil {
		if errors.Is(err, errFault) || errors.Is(err, syscall.ESRCH) {
			return [2]uint64{}, false, nil
		}
		return [2]uint64{}, false, fmt.Errorf("read a message header: %w", err)
	}
	if got < len(raw) {
		return [2]uint64{}, false, nil // the header runs into unmapped memory: the call faults
	}
	pointer := *(*uint64)(unsafe.Pointer(&raw[0]))
	length := uint64(*(*uint32)(unsafe.Pointer(&raw[8])))
	return [2]uint64{pointer, length}, pointer != 0, nil
}

// socketPath returns the path an AF_UNIX struct sockaddr_un names, or "".
func socketPath(tid int, address, length uint64) (string, error) {
	if address == 0 || length <= 2 {
		return "", nil
	}
	var raw [sockaddrUnSize]byte
	got, err := readMemory(tid, address, raw[:min(length, sockaddrUnSize)])
	if err != nil {
		if errors.Is(err, errFault) || errors.Is(err, syscall.ESRCH) {
			return "", nil // the kernel faults the call too, or the thread died
		}
		return "", err
	}
	if got <= 2 || raw[0] != afUnix || raw[1] != 0 { // a little-endian u16 family
		return "", nil
	}
	path := raw[2:got]
	if end := bytes.IndexByte(path, 0); end >= 0 {
		path = path[:end] // empty for an abstract address
	}
	return string(path), nil
}

var callsByNumber = func() map[int32]call {
	byNumber := make(map[int32]call, len(calls))
	for _, c := range calls {
		byNumber[int32(c.nr)] = c
	}
	return byNumber
}()

// valid reports whether a notification's thread still waits on it, so what
// was read from it belongs to that call.
func valid(listener int, id uint64) bool {
	return ioctl(listener, seccompIoctlNotifIDValid, unsafe.Pointer(&id)) == nil
}

// An access is one notified call being decoded.
type access struct {
	tid    int
	args   [6]uint64
	call   call
	buffer []byte
}

var errFault = errors.New("unreadable process memory")

// A located path is the absolute path a call names. A name relative to the
// working directory or a directory descriptor also keeps that directory, as
// /proc shows it (its physical location), and the name.
type located struct {
	path       string
	base, name string
}

// path returns the absolute path the call names, or false when it names none
// (an empty path that refers to the descriptor itself, or memory or a
// descriptor the kernel rejects too).
func (a *access) path() (located, bool, error) {
	c := a.call
	name, err := a.prefix(a.args[c.path], 0)
	if err != nil {
		if errors.Is(err, errFault) || errors.Is(err, syscall.ESRCH) {
			return located{}, false, nil
		}
		return located{}, false, err
	}
	dirfd := atFdcwd
	if c.dirfd >= 0 {
		dirfd = int(int32(a.args[c.dirfd]))
	}
	inRoot := false
	if c.kind == kindOpenat2 {
		var how [24]byte
		if got, err := readMemory(a.tid, a.args[c.flags], how[:]); err == nil && got == 24 {
			inRoot = *(*uint64)(unsafe.Pointer(&how[16]))&resolveInRoot != 0
		}
	}
	if name == "" {
		if c.kind != kindExec || c.atFlags == 0 || a.args[c.atFlags]&atEmptyPath == 0 || dirfd == atFdcwd {
			return located{}, false, nil // the descriptor's file was opened by path earlier
		}
		path, ok, err := a.link("fd/" + strconv.Itoa(dirfd))
		return located{path: path}, ok, err
	}
	return a.locate(name, dirfd, inRoot)
}

// locate returns the absolute path a name means for the thread: as it is
// if absolute, otherwise relative to a directory descriptor, or to the
// working directory for atFdcwd. With inRoot an absolute name is relative
// too (RESOLVE_IN_ROOT).
func (a *access) locate(name string, dirfd int, inRoot bool) (located, bool, error) {
	if name[0] == '/' && !inRoot {
		return located{path: name}, true, nil
	}
	link := "cwd"
	if dirfd != atFdcwd {
		link = "fd/" + strconv.Itoa(dirfd)
	}
	base, ok, err := a.link(link)
	if !ok || err != nil {
		return located{}, false, err
	}
	name = strings.TrimPrefix(name, "/")
	return located{path: strings.TrimSuffix(base, "/") + "/" + name, base: base, name: name}, true, nil
}

// link reads the thread's working directory or one of its descriptors.
func (a *access) link(name string) (string, bool, error) {
	target, err := os.Readlink("/proc/" + strconv.Itoa(a.tid) + "/" + name)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH) {
			return "", false, nil // a closed descriptor or a dead thread: the call fails too
		}
		return "", false, err
	}
	if !strings.HasPrefix(target, "/") {
		return "", false, nil // a pipe, socket or anonymous file: the call fails with ENOTDIR
	}
	return undeleted(target), true, nil
}

// undeleted removes the " (deleted)" the kernel appends to the target of a
// /proc link whose file was removed, unless a file of that very name exists.
func undeleted(target string) string {
	stripped, ok := strings.CutSuffix(target, " (deleted)")
	if !ok {
		return target
	}
	if _, err := os.Lstat(target); err == nil {
		return target
	}
	return stripped
}

// magic returns the file a path through a /proc magic link names: a
// process's working directory, root or descriptor (/proc/self/cwd/...,
// /proc/<pid>/fd/<n>/..., /proc/thread-self/root/..., /dev/fd/<n>/...,
// /dev/stdin). The kernel follows such a link to the file itself, not to
// the text the link shows, so the recorder skips /proc and /dev; read here,
// the path is recorded like any other. The links of "self" are the calling
// thread's. Any other path is returned unchanged.
func magic(tid int, path string) string {
	rest, ok := strings.CutPrefix(path, "/dev/")
	if ok {
		switch {
		case rest == "stdin", rest == "stdout", rest == "stderr":
			rest = "self/fd/" + strconv.Itoa(slices.Index([]string{"stdin", "stdout", "stderr"}, rest))
		case strings.HasPrefix(rest, "fd/"):
			rest = "self/" + rest
		default:
			return path
		}
	} else if rest, ok = strings.CutPrefix(path, "/proc/"); !ok {
		return path
	}
	process, rest, _ := strings.Cut(rest, "/")
	switch {
	case process == "self" || process == "thread-self":
		process = strconv.Itoa(tid)
	case strings.Trim(process, "0123456789") != "" || process == "":
		return path
	}
	if task, ok := strings.CutPrefix(rest, "task/"); ok {
		process, rest, _ = strings.Cut(task, "/")
		if strings.Trim(process, "0123456789") != "" || process == "" {
			return path
		}
	}
	link, rest, _ := strings.Cut(rest, "/")
	if link == "fd" {
		var fd string
		fd, rest, _ = strings.Cut(rest, "/")
		if strings.Trim(fd, "0123456789") != "" || fd == "" {
			return path
		}
		link = "fd/" + fd
	} else if link != "cwd" && link != "root" {
		return path
	}
	if rest == "" {
		// The link itself: reading it looks nothing up, and reopening a
		// descriptor reads a file whose open was recorded by its path.
		return path
	}
	target, err := os.Readlink("/proc/" + process + "/" + link)
	if err != nil || !strings.HasPrefix(target, "/") {
		return path // a dead process, a closed descriptor or a pipe: nothing to record
	}
	return strings.TrimSuffix(undeleted(target), "/") + "/" + rest
}

// environment returns the name of every variable in an execve environment.
func (a *access) environment(envp uint64) ([]string, error) {
	var names []string
	var pointer [8]byte
	for i := uint64(0); ; i++ {
		if got, err := readMemory(a.tid, envp+8*i, pointer[:]); err != nil || got < 8 {
			if err != nil && !errors.Is(err, errFault) && !errors.Is(err, syscall.ESRCH) {
				return nil, err
			}
			return names, nil
		}
		address := *(*uint64)(unsafe.Pointer(&pointer[0]))
		if address == 0 {
			return names, nil
		}
		name, err := a.prefix(address, '=')
		if err != nil {
			if errors.Is(err, errFault) || errors.Is(err, syscall.ESRCH) {
				return names, nil
			}
			return nil, err
		}
		names = append(names, name)
	}
}

// prefix reads a NUL-terminated string, up to its first stop byte if stop is
// not 0, of at most pathMax bytes.
func (a *access) prefix(address uint64, stop byte) (string, error) {
	var out []byte
	for len(out) <= pathMax {
		chunk := a.buffer[:pageSize-address%pageSize]
		n, err := readMemory(a.tid, address, chunk)
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "", errFault
		}
		chunk = chunk[:n]
		end := bytes.IndexByte(chunk, 0)
		if stop != 0 {
			if j := bytes.IndexByte(chunk, stop); j >= 0 && (end < 0 || j < end) {
				end = j
			}
		}
		if end >= 0 {
			return string(append(out, chunk[:end]...)), nil
		}
		out = append(out, chunk...)
		address += uint64(n)
	}
	return "", errFault // longer than any path the kernel accepts
}

// remoteIovec is struct iovec naming memory in another process.
type remoteIovec struct {
	base   uintptr
	length uint64
}

// readMemory copies another process's memory with process_vm_readv.
func readMemory(tid int, address uint64, into []byte) (int, error) {
	local := syscall.Iovec{Base: &into[0], Len: uint64(len(into))}
	remote := remoteIovec{base: uintptr(address), length: uint64(len(into))}
	n, _, errno := syscall.Syscall6(sysProcessVMReadv, uintptr(tid), uintptr(unsafe.Pointer(&local)), 1, uintptr(unsafe.Pointer(&remote)), 1, 0)
	switch errno {
	case 0:
		return int(n), nil
	case syscall.EFAULT:
		return 0, errFault
	default:
		return 0, errno
	}
}

func ioctl(fd int, request uintptr, argument unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(argument)); errno != 0 {
		return errno
	}
	return nil
}

// writable reports whether this process may write the directory.
func writable(dir string) bool { return syscall.Access(dir, 2) == nil } // W_OK

// foreignDir reports whether a path names a directory, not a link, that
// another user owns.
func foreignDir(dir string) bool {
	var info syscall.Stat_t
	return syscall.Lstat(dir, &info) == nil && info.Mode&syscall.S_IFMT == syscall.S_IFDIR && int(info.Uid) != os.Geteuid()
}

// physicalDir returns where an existing path leads, links and ".." resolved,
// for the price of an open and a readlink.
func physicalDir(dir string) (string, bool) {
	const oPath = 0x200000 // O_PATH on amd64 and arm64; package syscall lacks it
	fd, err := syscall.Open(dir, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", false
	}
	physical, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	syscall.Close(fd)
	return physical, err == nil
}

// privilegedCaps are the capabilities that let a process write or chmod a
// directory regardless of its owner and mode, or become another user who
// may: CAP_CHOWN, CAP_DAC_OVERRIDE, CAP_FOWNER, CAP_SETGID, CAP_SETUID and
// CAP_SYS_ADMIN (bits 0, 1, 3, 6, 7 and 21).
const privilegedCaps = 1<<0 | 1<<1 | 1<<3 | 1<<6 | 1<<7 | 1<<21

// privileged reports whether this process, and so the tree it observes, may
// change a directory another user owns and this user cannot write: it runs
// as root, its user or group IDs (real, effective, saved, file system)
// differ, so it may switch between them, or it holds one of
// privilegedCaps in its permitted or effective set. An unreadable status
// counts as privileged.
func privileged() bool {
	if os.Geteuid() == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		return true
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return true
	}
	seen := 0
	for line := range strings.SplitSeq(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "Uid", "Gid":
			ids := strings.Fields(value)
			if len(ids) != 4 || slices.ContainsFunc(ids, func(id string) bool { return id != ids[0] }) {
				return true
			}
		case "CapPrm", "CapEff":
			caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err != nil || caps&privilegedCaps != 0 {
				return true
			}
		default:
			continue
		}
		seen++
	}
	return seen != 4
}

// linkedDirs follows the directories processes reached through a link
// inside the root: by chdir, or by opening the directory, whose descriptor
// later calls name paths relative to (openat, fchdir to it). /proc shows a
// working directory or a descriptor by its physical location, so a name
// relative to it would be recorded there, and retargeting the link would go
// unseen. Each such directory is remembered under its physical location,
// for the rest of the run. A later name relative to a working directory or
// a descriptor at that physical location is recorded under each remembered
// name too, whichever process makes the call. fchdir needs no notification:
// it changes to a directory the open already remembered.
//
// fabricate.py copies a process's working directory to its child at clone,
// fork and vfork, which strace reports with the child's pid. A seccomp
// notification comes before the call, so the observer never learns the
// child's pid at the fork, and by the child's first lookup its parent may
// have exited and the child been reparented. Keying the names on the
// physical directory instead of on the process or the descriptor needs no
// pid: it covers a reparented child, a descriptor passed to another
// process, and any process that reaches the directory otherwise, at the
// cost of an extra entry for a name a process did not use, which can only
// make a result rerun.
type linkedDirs struct {
	active atomic.Bool // a name was ever remembered; until then nothing is looked up

	mu      sync.Mutex
	aliases map[string][]string // physical directory → the names it was reached by
}

// enter remembers a directory a process changes to (chdir) or opens, named
// by an absolute path, if a link inside the root leads to it. The call is
// notified before it runs and may fail, so the directory is looked up here;
// remembering a name the call did not reach costs only an extra entry. An
// open's path is first checked to name a directory, by the file it leads
// to, since a directory opens without O_DIRECTORY too.
func (o *observer) enter(path string, opened bool) {
	for _, prefix := range []string{"/proc/", "/dev/"} {
		if strings.HasPrefix(path, prefix) {
			return // still a magic link, which would resolve in this process
		}
	}
	name, ok := o.rec.inside(clean(path))
	if !ok {
		return // a link outside the root is not an input cmd/go rechecks
	}
	rest, _ := under(name, o.rec.logical)
	if opened {
		if info, err := os.Stat(name); err != nil || !info.IsDir() {
			return
		}
	}
	physical, ok := physicalDir(name)
	if !ok || physical == clean(o.rec.physical+rest) {
		return // the call fails, or crosses no link inside the root
	}
	o.dirs.add(physical, name)
}

// add remembers that a name leads to a physical directory.
func (d *linkedDirs) add(physical, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !slices.Contains(d.aliases[physical], name) {
		d.aliases[physical] = append(d.aliases[physical], name)
	}
	d.active.Store(true)
}

// logical returns the names through which a link reached a physical
// directory.
func (d *linkedDirs) logical(physical string) []string {
	if !d.active.Load() {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.aliases[physical])
}
