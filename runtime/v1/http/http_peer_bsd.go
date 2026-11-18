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
	return kq, nil
}

// peerSys is the pipe whose write wakes the watcher's goroutine for
// close. Go's poller does not serve a kqueue descriptor on darwin ("waiting
// for unsupported file type"), so the goroutine waits in kevent itself, a
// blocking system call that holds one thread per server while it waits.
type peerSys struct{ rd, wr int }

func (w *peerWatcher) start() error {
	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		_ = syscall.Close(w.pfd)
		return err
	}
	syscall.CloseOnExec(p[0])
	syscall.CloseOnExec(p[1])
	var ch [1]syscall.Kevent_t
	syscall.SetKevent(&ch[0], p[0], syscall.EVFILT_READ, syscall.EV_ADD)
	if _, err := syscall.Kevent(w.pfd, ch[:], nil, nil); err != nil {
		_ = syscall.Close(p[0])
		_ = syscall.Close(p[1])
		_ = syscall.Close(w.pfd)
		return err
	}
	w.sys = peerSys{rd: p[0], wr: p[1]}
	go w.run()
	return nil
}

// run waits in kevent until the kqueue has events and hands each to its
// connection, until close's byte on the pipe ends it.
func (w *peerWatcher) run() {
	defer func() {
		_ = syscall.Close(w.sys.rd)
		_ = syscall.Close(w.sys.wr)
		_ = syscall.Close(w.pfd)
	}()
	var ev peerEvents
	fds := make([]int32, 0, len(ev))
	for {
		n, err := syscall.Kevent(w.pfd, nil, ev[:], nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			w.dead.Store(true)
			return
		}
		fds = fds[:0]
		for i := 0; i < n; i++ {
			if int(ev[i].Ident) == w.sys.rd {
				return // close
			}
			fds = append(fds, int32(ev[i].Ident))
		}
		w.deliver(fds)
	}
}

// close ends the watcher once its server's connections have ended.
func (w *peerWatcher) close() { _, _ = syscall.Write(w.sys.wr, []byte{0}) }

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
