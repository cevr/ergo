package http

import (
	"sync"
	"sync/atomic"
	"syscall"
)

// engineWatcher selects how the engine sees a client leave while a
// handler runs (HTTP-FAST-c6). true: one watcher per server waits for
// every watched connection's peer close in the platform's poller (epoll's
// EPOLLRDHUP on Linux, kqueue's EVFILT_READ with EV_EOF on darwin and the
// BSDs), so a watch is a flag, with no goroutine, deadline or read per
// request. A connection that is no socket (a pipe), a platform without
// such a poller, or a poller that fails is watched by net/http's
// background read (startBackgroundRead), which false selects for every
// connection: the reference. go test -args -http.watcher=false runs the
// package's tests on it.
//
// The watcher keeps the background read's rules, as it reads what that
// read would (peek, which consumes nothing). It acts only while a watch
// is armed (the handler has reached an interruption point): an end of
// stream or a failure then stops the request, as the read's EOF does
// (httpConn.Read). Bytes that come first end nothing: net/http's read
// would have read them and stopped watching, so a close behind them goes
// unseen until the engine reads them (pending: a watch armed with bytes
// waiting looks once more, since a close behind them makes no new
// event). An end seen with no watch armed is kept (eof), so the watch
// armed next sees it at once, as the read started then would.
var engineWatcher = true

// peerWatcher is a server's watcher: the platform poller's descriptor,
// served by one goroutine (start, per platform), and the connections
// registered with it, by descriptor. dead: that goroutine ended other
// than by close, so the connections fall back to the background read at
// their next watch.
type peerWatcher struct {
	pfd   int
	sys   peerSys
	mu    sync.Mutex
	conns map[int32]*engineConn
	dead  atomic.Bool
}

// The results of a peek at a connection.
const (
	peekEmpty = iota // nothing to read yet
	peekData         // bytes wait to be read
	peekGone         // the end of the stream, or a failure
)

// peers returns s's watcher, made at its first watch. It is nil when the
// platform has none, or when making it or running it failed.
func (s *httpServer) peers() *peerWatcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peerW == nil && !s.peerFailed && !s.closing {
		w, err := newPeerWatcher()
		if err != nil {
			s.peerFailed = true
			return nil
		}
		s.peerW = w
	}
	if s.peerW != nil && s.peerW.dead.Load() {
		return nil
	}
	return s.peerW
}

func newPeerWatcher() (*peerWatcher, error) {
	pfd, err := newPeerPoll()
	if err != nil {
		return nil, err
	}
	w := &peerWatcher{pfd: pfd, conns: map[int32]*engineConn{}}
	if err := w.start(); err != nil {
		return nil, err
	}
	return w, nil
}

// deliver hands each reported descriptor to its connection.
func (w *peerWatcher) deliver(fds []int32) {
	for _, fd := range fds {
		w.mu.Lock()
		e := w.conns[fd]
		w.mu.Unlock()
		if e != nil {
			e.peerEvent()
		}
	}
}

// registerPeer registers e with its server's watcher, once. false: e is
// watched by the background read.
func (e *engineConn) registerPeer() bool {
	if e.peer != nil {
		if !e.peer.dead.Load() {
			return true
		}
		e.unregisterPeer()
		e.peerFailed = true
		return false
	}
	if e.peerFailed {
		return false
	}
	e.peerFailed = true
	sc, ok := e.c.Conn.(syscall.Conn)
	if !ok {
		return false
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return false
	}
	w := e.s.peers()
	if w == nil {
		return false
	}
	// rc is set before e is in the map, whose lock publishes it to the
	// watcher's goroutine (peek).
	e.rc = rc
	var added error
	err = rc.Control(func(u uintptr) {
		fd := int32(u)
		w.mu.Lock()
		w.conns[fd] = e
		w.mu.Unlock()
		if added = peerPollAdd(w.pfd, int(u)); added != nil {
			w.mu.Lock()
			delete(w.conns, fd)
			w.mu.Unlock()
		}
		e.pfd = fd
	})
	if err != nil || added != nil {
		return false
	}
	e.peer, e.peerFailed = w, false
	return true
}

// unregisterPeer takes e from its watcher before e's connection closes or
// goes to net/http.
func (e *engineConn) unregisterPeer() {
	w := e.peer
	if w == nil {
		return
	}
	e.peer = nil
	_ = e.rc.Control(func(u uintptr) { peerPollDel(w.pfd, int(u)) })
	w.mu.Lock()
	if w.conns[e.pfd] == e {
		delete(w.conns, e.pfd)
	}
	w.mu.Unlock()
}

// peek reads, without consuming, whether e's connection has bytes, has
// ended, or has nothing yet.
func (e *engineConn) peek() int {
	st := peekEmpty
	if err := e.rc.Control(func(u uintptr) { st = peerPeek(int(u)) }); err != nil {
		return peekEmpty // closed by the server: not the peer's leaving
	}
	return st
}

// peerEvent is the watcher's look at e after its poller reported it.
func (e *engineConn) peerEvent() {
	switch e.peek() {
	case peekGone:
		e.eof.Store(true)
		e.peerGone()
	case peekData:
		e.pending.Store(true)
	}
	if engineWatchEvent != nil {
		engineWatchEvent()
	}
}

// engineWatchEvent, when set, is called after each event the watcher
// handles. Only a test sets it.
var engineWatchEvent func()

// peerGone stops the request whose watch is armed, as httpConn.Read does
// for the background read's EOF.
func (e *engineConn) peerGone() {
	c := e.c
	c.mu.Lock()
	if e.armed {
		c.gone = true
		if c.request != nil {
			c.request.Interrupt()
		}
	}
	c.mu.Unlock()
}

// arm starts the watch of the request being answered.
func (e *engineConn) arm() {
	e.c.mu.Lock()
	e.armed = true
	e.c.mu.Unlock()
	if !e.eof.Load() && e.pending.Swap(false) {
		switch e.peek() {
		case peekGone:
			e.eof.Store(true)
		case peekData:
			e.pending.Store(true)
		}
	}
	if e.eof.Load() {
		e.peerGone()
	}
}

// disarm ends the watch once the handler has returned.
func (e *engineConn) disarm() {
	e.c.mu.Lock()
	e.armed = false
	e.c.mu.Unlock()
}
