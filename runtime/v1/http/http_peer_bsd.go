//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package http

import "syscall"

// The BSD watcher is a kqueue. A connection is registered for EVFILT_READ,
// edge-triggered (EV_CLEAR), with a low-water mark above any receive
// buffer (NOTE_LOWAT; the kernel caps it at the buffer's size): the end
// of the stream (EV_EOF, and a failure, which sets it) makes an event
// whatever the mark, and arriving bytes make one only when they fill the
// buffer, so the watcher wakes when a client leaves.

type peerEvents [64]syscall.Kevent_t

func newPeerPoll() (int, error) {
	kq, err := syscall.Kqueue()
	if err != nil {
		return -1, err
	}
	syscall.CloseOnExec(kq)
	if err := syscall.SetNonblock(kq, true); err != nil {
		_ = syscall.Close(kq)
		return -1, err
	}
	return kq, nil
}

func peerPollAdd(pfd, fd int) error {
	var ch [1]syscall.Kevent_t
	syscall.SetKevent(&ch[0], fd, syscall.EVFILT_READ, syscall.EV_ADD|syscall.EV_CLEAR)
	ch[0].Fflags = syscall.NOTE_LOWAT
	ch[0].Data = 1 << 30
	_, err := syscall.Kevent(pfd, ch[:], nil, nil)
	return err
}

func peerPollDel(pfd, fd int) {
	var ch [1]syscall.Kevent_t
	syscall.SetKevent(&ch[0], fd, syscall.EVFILT_READ, syscall.EV_DELETE)
	_, _ = syscall.Kevent(pfd, ch[:], nil, nil)
}

// peerPollWait takes the ready connections without waiting: none, with a
// nil error, when there are none yet.
func peerPollWait(pfd int, ev *peerEvents, fds []int32) ([]int32, error) {
	var now syscall.Timespec
	n, err := syscall.Kevent(pfd, nil, ev[:], &now)
	if err == syscall.EINTR || err == syscall.EAGAIN {
		return fds, nil
	}
	for i := 0; i < n; i++ {
		fds = append(fds, int32(ev[i].Ident))
	}
	return fds, err
}

// peerPeek reads one byte of fd without consuming it and without waiting.
func peerPeek(fd int) int {
	var b [1]byte
	for {
		n, _, err := syscall.Recvfrom(fd, b[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		switch {
		case err == syscall.EINTR:
			continue
		case err == syscall.EAGAIN:
			return peekEmpty
		case err != nil || n == 0:
			return peekGone
		}
		return peekData
	}
}
