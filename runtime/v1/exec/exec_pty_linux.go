//go:build linux

package exec

import (
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

const sysPidfdOpen = 434

// ptyUint32Ioctl performs a Linux ioctl taking an int32/uint32 pointer
// argument (TIOCSPTLCK or TIOCGPTN). Safety invariant: val points to a
// live 32-bit local variable kept for the call and converted in the
// Syscall call expression (unsafe.Pointer rule 4).
func ptyUint32Ioctl(fd uintptr, req uintptr, val *uint32) syscall.Errno {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(val)))
	return errno
}

func openOSPTY() (master, slave *os.File, slavePath string, err error) {
	mfd, serr := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if serr != nil {
		return nil, nil, "", ExecFailed{Name: "pty", Reason: serr.Error()}
	}
	if nerr := syscall.SetNonblock(mfd, true); nerr != nil {
		_ = syscall.Close(mfd)
		return nil, nil, "", ExecFailed{Name: "pty", Reason: nerr.Error()}
	}
	master = os.NewFile(uintptr(mfd), "pty-master")
	var unlock uint32
	if errno := ptyUint32Ioctl(uintptr(mfd), syscall.TIOCSPTLCK, &unlock); errno != 0 {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	var ptn uint32
	if errno := ptyUint32Ioctl(uintptr(mfd), syscall.TIOCGPTN, &ptn); errno != 0 {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: errno.Error()}
	}
	slavePath = "/dev/pts/" + strconv.Itoa(int(ptn))
	sfd, serr := syscall.Open(slavePath, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if serr != nil {
		_ = master.Close()
		return nil, nil, "", ExecFailed{Name: "pty", Reason: serr.Error()}
	}
	slave = os.NewFile(uintptr(sfd), slavePath)
	return master, slave, slavePath, nil
}

func awaitFDReadableOS(t *rt.Task, f *os.File, closeCh <-chan struct{}, name string) error {
	rawFD := osFileFd(f)
	if rawFD < 0 {
		return ExecFailed{Name: name, Reason: name + " is closed"}
	}
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return ExecFailed{Name: name, Reason: err.Error()}
	}
	defer func() { _ = syscall.Close(epfd) }()

	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return ExecFailed{Name: name, Reason: fsReason(err)}
	}
	defer func() {
		_ = wakeR.Close()
		_ = wakeW.Close()
	}()

	evTarget := syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLHUP | syscall.EPOLLERR, Fd: int32(rawFD)}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, rawFD, &evTarget); err != nil {
		return ExecFailed{Name: name, Reason: err.Error()}
	}
	wakeRFD := int(wakeR.Fd())
	evWake := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(wakeRFD)}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, wakeRFD, &evWake); err != nil {
		return ExecFailed{Name: name, Reason: err.Error()}
	}

	quit := make(chan struct{})
	defer close(quit)
	go func() {
		select {
		case <-t.Done():
			_, _ = wakeW.Write([]byte{1})
		case <-closeCh:
			_, _ = wakeW.Write([]byte{2})
		case <-quit:
		}
	}()

	events := make([]syscall.EpollEvent, 2)
	for {
		n, err := syscall.EpollWait(epfd, events, -1)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return ExecFailed{Name: name, Reason: err.Error()}
		}
		wokeByPipe := false
		for i := range n {
			if int(events[i].Fd) == wakeRFD {
				wokeByPipe = true
				var b [16]byte
				_, _ = wakeR.Read(b[:])
				t.Check()
				select {
				case <-closeCh:
					return ExecFailed{Name: name, Reason: name + " is closed"}
				default:
				}
			}
		}
		if !wokeByPipe || n > 1 {
			return nil
		}
	}
}

func awaitDetachedNonChildOS(t *rt.Task, pid int, start int64) (int, int, int, bool, string, error) {
	pidfd, _, errno := syscall.Syscall(sysPidfdOpen, uintptr(pid), 0, 0)
	if errno == 0 {
		fd := int(pidfd)
		syscall.CloseOnExec(fd)
		defer func() { _ = syscall.Close(fd) }()
		if start != 0 && !execRunning(pid, start) {
			if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
				return execOutcomeExited, st, sg, false, "", nil
			}
			return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
		}
		if err := awaitPidfdLinux(t, fd); err != nil {
			return 0, 0, 0, false, "", err
		}
		if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
			return execOutcomeExited, st, sg, false, "", nil
		}
		return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
	}
	if errno == syscall.ESRCH {
		if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
			return execOutcomeExited, st, sg, false, "", nil
		}
		return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
	}
	// Fallback when pidfd_open is unavailable (ENOSYS / EPERM).
	return awaitDetachedPollFallback(t, pid, start)
}

func awaitPidfdLinux(t *rt.Task, pidfd int) error {
	epfd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return ExecFailed{Name: "wait", Reason: err.Error()}
	}
	defer func() { _ = syscall.Close(epfd) }()

	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return ExecFailed{Name: "wait", Reason: fsReason(err)}
	}
	defer func() {
		_ = wakeR.Close()
		_ = wakeW.Close()
	}()

	evPid := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(pidfd)}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, pidfd, &evPid); err != nil {
		return ExecFailed{Name: "wait", Reason: err.Error()}
	}
	wakeRFD := int(wakeR.Fd())
	evWake := syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(wakeRFD)}
	if err := syscall.EpollCtl(epfd, syscall.EPOLL_CTL_ADD, wakeRFD, &evWake); err != nil {
		return ExecFailed{Name: "wait", Reason: err.Error()}
	}

	quit := make(chan struct{})
	defer close(quit)
	go func() {
		select {
		case <-t.Done():
			_, _ = wakeW.Write([]byte{1})
		case <-quit:
		}
	}()

	events := make([]syscall.EpollEvent, 2)
	for {
		n, err := syscall.EpollWait(epfd, events, -1)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return ExecFailed{Name: "wait", Reason: err.Error()}
		}
		wokeByPipe := false
		for i := range n {
			if int(events[i].Fd) == wakeRFD {
				wokeByPipe = true
				var b [16]byte
				_, _ = wakeR.Read(b[:])
				t.Check()
			}
		}
		if !wokeByPipe || n > 1 {
			return nil
		}
	}
}

func awaitDetachedPollFallback(t *rt.Task, pid int, start int64) (int, int, int, bool, string, error) {
	for pause := execSpawnPoll; execRunning(pid, start); pause = min(2*pause, execSpawnPollMax) {
		select {
		case <-t.Done():
			t.Check()
		case <-time.After(pause):
		}
	}
	if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
		return execOutcomeExited, st, sg, false, "", nil
	}
	return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
}
