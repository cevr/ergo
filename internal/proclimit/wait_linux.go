//go:build linux

package proclimit

import (
	"runtime"
	"syscall"
)

// pidfdOpen is pidfd_open's number (Linux 5.3): 434 in the table every
// architecture shares since 5.1, offset on MIPS as the go command's own
// internal/syscall/unix has it.
func pidfdOpen() uintptr {
	switch runtime.GOARCH {
	case "mips", "mipsle":
		return 4434
	case "mips64", "mips64le":
		return 5434
	}
	return 434
}

// exited blocks until pid has exited and reports true; the program stays
// unreaped (cmd.Wait reaps it after), so its group id is still its own. A
// pidfd is readable once its process has exited, a zombie included, and
// epoll waits for that: no pointer passes through a raw syscall, so the
// file needs no unsafe. It reports false, at once, when it cannot watch
// the exit (a kernel before 5.3): Wait then ends no group.
func exited(pid int) bool {
	fd, _, e := syscall.Syscall(pidfdOpen(), uintptr(pid), 0, 0)
	if e != 0 {
		return false
	}
	pidfd := int(fd)
	defer syscall.Close(pidfd)
	ep, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return false
	}
	defer syscall.Close(ep)
	if err := syscall.EpollCtl(ep, syscall.EPOLL_CTL_ADD, pidfd, &syscall.EpollEvent{Events: syscall.EPOLLIN, Fd: int32(pidfd)}); err != nil {
		return false
	}
	events := make([]syscall.EpollEvent, 1)
	for {
		n, err := syscall.EpollWait(ep, events, -1)
		if err == syscall.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
