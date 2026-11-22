//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package proclimit

import "syscall"

// exited blocks until pid has exited and reports true, leaving it
// unreaped: a kqueue EVFILT_PROC NOTE_EXIT event, which the kernel posts at
// the exit, before any wait. A pid that has already exited (a zombie: only
// cmd.Wait reaps it, after this returns) is refused with ESRCH, which is
// the same event. It reports false when it cannot watch the exit: Wait
// then ends no group, so a running program is never killed early.
func exited(pid int) bool {
	kq, err := syscall.Kqueue()
	if err != nil {
		return false
	}
	defer syscall.Close(kq)
	var change [1]syscall.Kevent_t
	syscall.SetKevent(&change[0], pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	change[0].Fflags = syscall.NOTE_EXIT
	var event [1]syscall.Kevent_t
	for {
		n, err := syscall.Kevent(kq, change[:], event[:], nil)
		switch {
		case err == syscall.EINTR:
			continue
		case err == syscall.ESRCH:
			return true
		}
		return err == nil && n > 0
	}
}
