//go:build linux

package http

import (
	"errors"
	"os"
	"syscall"
)

// peerSys is the epoll instance as a file of Go's own poller, which
// serves an epoll descriptor (one epoll waits on another), so the
// watcher's goroutine parks with no thread of its own.
type peerSys struct{ f *os.File }

func (w *peerWatcher) start() error {
	f := os.NewFile(uintptr(w.pfd), "http peer watcher")
	rc, err := f.SyscallConn()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.sys.f = f
	go w.run(rc)
	return nil
}

// run waits, parked in Go's poller, until the epoll instance has events,
// takes them without waiting, and hands each to its connection, until
// close.
func (w *peerWatcher) run(rc syscall.RawConn) {
	var ev peerEvents
	var fds []int32
	for {
		var werr error
		err := rc.Read(func(pfd uintptr) bool {
			fds, werr = peerPollWait(int(pfd), &ev, fds[:0])
			return werr != nil || len(fds) > 0
		})
		if errors.Is(err, os.ErrClosed) {
			return
		}
		if err != nil || werr != nil {
			w.dead.Store(true)
			return
		}
		w.deliver(fds)
	}
}

// close ends the watcher once its server's connections have ended.
func (w *peerWatcher) close() { _ = w.sys.f.Close() }

// The Linux watcher is an epoll instance. A connection is registered for
// EPOLLRDHUP alone, edge-triggered: the peer's close (and a failure, which
// epoll always reports) makes an event, bytes that arrive make none, so
// the watcher wakes only when a client leaves.

const (
	epollRDHUP = 0x2000
	epollET    = 1 << 31
)

type peerEvents [64]syscall.EpollEvent

func newPeerPoll() (int, error) {
	ep, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		return -1, err
	}
	if err := syscall.SetNonblock(ep, true); err != nil {
		_ = syscall.Close(ep)
		return -1, err
	}
	return ep, nil
}

func peerPollAdd(pfd, fd int) error {
	ev := syscall.EpollEvent{Events: epollRDHUP | epollET, Fd: int32(fd)}
	return syscall.EpollCtl(pfd, syscall.EPOLL_CTL_ADD, fd, &ev)
}

func peerPollDel(pfd, fd int) {
	_ = syscall.EpollCtl(pfd, syscall.EPOLL_CTL_DEL, fd, &syscall.EpollEvent{})
}

// peerPollWait takes the ready connections without waiting: none, with a
// nil error, when there are none yet.
func peerPollWait(pfd int, ev *peerEvents, fds []int32) ([]int32, error) {
	n, err := syscall.EpollWait(pfd, ev[:], 0)
	if err == syscall.EINTR || err == syscall.EAGAIN {
		return fds, nil
	}
	for i := 0; i < n; i++ {
		fds = append(fds, ev[i].Fd)
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
