//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package exec

import (
	"os"
	"syscall"

	"ergo.local/prototype/runtime/rt"
)

func awaitFDReadableOS(t *rt.Task, f *os.File, closeCh <-chan struct{}, name string) error {
	rawFD := osFileFd(f)
	if rawFD < 0 {
		return ExecFailed{Name: name, Reason: name + " is closed"}
	}
	kq, err := syscall.Kqueue()
	if err != nil {
		return ExecFailed{Name: name, Reason: err.Error()}
	}
	defer func() { _ = syscall.Close(kq) }()
	syscall.CloseOnExec(kq)

	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return ExecFailed{Name: name, Reason: fsReason(err)}
	}
	defer func() {
		_ = wakeR.Close()
		_ = wakeW.Close()
	}()

	wakeRFD := int(wakeR.Fd())
	changes := make([]syscall.Kevent_t, 2)
	syscall.SetKevent(&changes[0], rawFD, syscall.EVFILT_READ, syscall.EV_ADD|syscall.EV_ONESHOT)
	syscall.SetKevent(&changes[1], wakeRFD, syscall.EVFILT_READ, syscall.EV_ADD|syscall.EV_ONESHOT)

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

	events := make([]syscall.Kevent_t, 2)
	for {
		n, err := syscall.Kevent(kq, changes, events, nil)
		changes = nil
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return ExecFailed{Name: name, Reason: err.Error()}
		}
		for i := range n {
			if int(events[i].Ident) == wakeRFD {
				t.Check()
				select {
				case <-closeCh:
					return ExecFailed{Name: name, Reason: name + " is closed"}
				default:
				}
			}
		}
		return nil
	}
}

func awaitDetachedNonChildOS(t *rt.Task, pid int, start int64) (int, int, int, bool, string, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return 0, 0, 0, false, "", ExecFailed{Name: "wait", Reason: err.Error()}
	}
	defer func() { _ = syscall.Close(kq) }()
	syscall.CloseOnExec(kq)

	wakeR, wakeW, err := os.Pipe()
	if err != nil {
		return 0, 0, 0, false, "", ExecFailed{Name: "wait", Reason: fsReason(err)}
	}
	defer func() {
		_ = wakeR.Close()
		_ = wakeW.Close()
	}()

	wakeRFD := int(wakeR.Fd())
	changes := make([]syscall.Kevent_t, 2)
	syscall.SetKevent(&changes[0], pid, syscall.EVFILT_PROC, syscall.EV_ADD|syscall.EV_ONESHOT)
	changes[0].Fflags = execNoteExitFlags()
	syscall.SetKevent(&changes[1], wakeRFD, syscall.EVFILT_READ, syscall.EV_ADD|syscall.EV_ONESHOT)

	events := make([]syscall.Kevent_t, 2)
	// Register EVFILT_PROC synchronously before blocking so we immediately catch ESRCH
	// and re-verify start time against PID reuse.
	if _, err := syscall.Kevent(kq, changes, nil, nil); err != nil {
		if err == syscall.ESRCH {
			if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
				return execOutcomeExited, st, sg, false, "", nil
			}
			return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
		}
		return 0, 0, 0, false, "", ExecFailed{Name: "wait", Reason: err.Error()}
	}
	if start != 0 && !execRunning(pid, start) {
		if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
			return execOutcomeExited, st, sg, false, "", nil
		}
		return execOutcomeUnavailable, 0, 0, true, "detached process exited or start time mismatch", nil
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

	for {
		n, err := syscall.Kevent(kq, nil, events, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return 0, 0, 0, false, "", ExecFailed{Name: "wait", Reason: err.Error()}
		}
		for i := range n {
			if int(events[i].Ident) == wakeRFD {
				t.Check()
				continue
			}
			if int(events[i].Ident) == pid {
				if st, sg, reaped := tryReapChildIfMatch(pid, start); reaped {
					return execOutcomeExited, st, sg, false, "", nil
				}
				if st, sg, ok := execKqueueExitStatus(events[i].Fflags, int64(events[i].Data)); ok {
					return execOutcomeExited, st, sg, false, "", nil
				}
				return execOutcomeUnavailable, 0, 0, true, "detached process exited outside this process (not a child / already reaped)", nil
			}
		}
	}
}
