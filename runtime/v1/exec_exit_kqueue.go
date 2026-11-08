//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import "syscall"

// execExit waits for the end of a program that ExecRun started, without
// reaping it (exec.go), by a kqueue: EVFILT_PROC with NOTE_EXIT. Go's os
// package does not use waitid with WNOWAIT on Darwin, because there it
// also returns when the process stops (golang/go#19314); NOTE_EXIT fires
// only at the exit.
type execExit struct{ kq int }

// execWatchExit makes the kqueue before the program starts, so that a
// host out of descriptors fails the call before anything runs.
func execWatchExit() (execExit, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return execExit{kq: -1}, err
	}
	syscall.CloseOnExec(kq)
	return execExit{kq: kq}, nil
}

// await returns when the program pid has ended, and leaves it unreaped.
// When the program has already exited, the registration fails with ESRCH
// (the kernel refuses a NOTE_EXIT filter on a process that is exiting or a
// zombie); as only cmd.Wait reaps it, that also means it ended and is
// unreaped. Any return but EINTR ends the wait.
func (w execExit) await(pid int) {
	change := make([]syscall.Kevent_t, 1)
	syscall.SetKevent(&change[0], pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	change[0].Fflags = syscall.NOTE_EXIT
	events := make([]syscall.Kevent_t, 1)
	for {
		n, err := syscall.Kevent(w.kq, change, events, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n > 0 {
			return
		}
	}
}

// close releases the kqueue.
func (w execExit) close() {
	if w.kq >= 0 {
		_ = syscall.Close(w.kq)
	}
}
