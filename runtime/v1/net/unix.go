package net

import (
	"os"
	"strings"
	"sync"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

// NetReceivedFDs is net.ReceivedFDs: data and file descriptors received
// in one RecvFDs call.
type NetReceivedFDs struct {
	Data string
	FDs  []*FD
}

// FD is net.FD: an owned OS file descriptor or in-memory handle that
// may be duplicated, inspected, sent across UnixConn.SendFDs, or
// converted into a listener or connection.
type FD struct {
	mu     sync.Mutex
	closed bool
	file   *os.File

	pipe  *PipeNet
	kind  pipeFDKind
	fdNum int
	plist *pipeListenerState
	pconn *pipeConnEnd
}

// runtimeOnly implements net.FD's private operation (runtime/v1/sealed.go).
func (*FD) runtimeOnly() {}

func (f *FD) pipeHandleLocked(p *PipeNet) (pipeFDHandle, bool) {
	if f == nil {
		return pipeFDHandle{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.pipe != p || p == nil {
		return pipeFDHandle{}, false
	}
	return pipeFDHandle{
		pipe:  f.pipe,
		kind:  f.kind,
		fdNum: f.fdNum,
		plist: f.plist,
		pconn: f.pconn,
	}, true
}

// Close releases f. A second Close does nothing.
//
//ergo:host
//ergo:wait none
func (f *FD) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	file := f.file
	p := f.pipe
	h := pipeFDHandle{pipe: p, kind: f.kind, fdNum: f.fdNum, plist: f.plist, pconn: f.pconn}
	f.file = nil
	f.pipe = nil
	f.plist = nil
	f.pconn = nil
	f.mu.Unlock()

	if p != nil {
		p.mu.Lock()
		h.releaseLocked()
		p.mu.Unlock()
		return
	}
	if file != nil {
		osCloseFile(file)
	}
}

// Fd returns f's integer descriptor, or -1 if f is closed.
//
//ergo:host
//ergo:wait none
func (f *FD) Fd() int {
	if f == nil {
		return -1
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return -1
	}
	if f.pipe != nil {
		return f.fdNum
	}
	if f.file != nil {
		return osFileFd(f.file)
	}
	return -1
}

// Dup returns an independent duplicate FD referring to the same kernel
// descriptor or in-memory endpoint.
//
//ergo:host
//ergo:wait none
func (f *FD) Dup() (*FD, error) {
	if f == nil {
		return nil, NetFailed{Reason: "use of closed FD"}
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, NetFailed{Reason: "use of closed FD"}
	}
	if f.pipe != nil {
		p := f.pipe
		h := pipeFDHandle{pipe: p, kind: f.kind, fdNum: 0, plist: f.plist, pconn: f.pconn}
		f.mu.Unlock()
		p.mu.Lock()
		h.fdNum = p.nextFDLocked()
		h.retainLocked()
		p.mu.Unlock()
		return &FD{
			pipe:  p,
			kind:  h.kind,
			fdNum: h.fdNum,
			plist: h.plist,
			pconn: h.pconn,
		}, nil
	}
	file := f.file
	dup, err := osDupFile(file)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &FD{file: dup}, nil
}

func (f *FD) take() (*os.File, *PipeNet, pipeFDHandle, error) {
	if f == nil {
		return nil, nil, pipeFDHandle{}, NetFailed{Reason: "use of closed FD"}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, nil, pipeFDHandle{}, NetFailed{Reason: "use of closed FD"}
	}
	f.closed = true
	file := f.file
	p := f.pipe
	h := pipeFDHandle{pipe: p, kind: f.kind, fdNum: f.fdNum, plist: f.plist, pconn: f.pconn}
	f.file = nil
	f.pipe = nil
	f.plist = nil
	f.pconn = nil
	return file, p, h, nil
}

// IntoListener converts f into a stream Listener, consuming f.
//
//ergo:host
//ergo:wait none
func (f *FD) IntoListener() (*Listener, error) {
	file, p, h, err := f.take()
	if err != nil {
		return nil, err
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if h.kind != fdTCPListener || h.plist == nil || h.plist.closed {
			h.releaseLocked()
			return nil, NetFailed{Reason: "FD is not an open TCP listener"}
		}
		return &Listener{pipe: p, addr: h.plist.addr, state: h.plist}, nil
	}
	return osIntoListener(file)
}

// IntoUnixListener converts f into a UnixListener, consuming f.
//
//ergo:host
//ergo:wait none
func (f *FD) IntoUnixListener() (*UnixListener, error) {
	file, p, h, err := f.take()
	if err != nil {
		return nil, err
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if h.kind != fdUnixListener || h.plist == nil || h.plist.closed {
			h.releaseLocked()
			return nil, NetFailed{Reason: "FD is not an open Unix listener"}
		}
		return &UnixListener{pipe: p, addr: h.plist.addr, state: h.plist, unlinkOnClose: false}, nil
	}
	return osIntoUnixListener(file)
}

// IntoConn converts f into a stream Conn, consuming f.
//
//ergo:host
//ergo:wait none
func (f *FD) IntoConn() (*Conn, error) {
	file, p, h, err := f.take()
	if err != nil {
		return nil, err
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if h.kind != fdTCPConn || h.pconn == nil || h.pconn.shut {
			h.releaseLocked()
			return nil, NetFailed{Reason: "FD is not an open TCP connection"}
		}
		end := h.pconn
		return &Conn{pipe: p, end: end, in: end.in, out: end.out, local: end.local, remote: end.remote}, nil
	}
	return osIntoConn(file)
}

// IntoUnixConn converts f into a UnixConn, consuming f.
//
//ergo:host
//ergo:wait none
func (f *FD) IntoUnixConn() (*UnixConn, error) {
	file, p, h, err := f.take()
	if err != nil {
		return nil, err
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if h.kind != fdUnixConn || h.pconn == nil || h.pconn.shut {
			h.releaseLocked()
			return nil, NetFailed{Reason: "FD is not an open Unix connection"}
		}
		return &UnixConn{pipe: p, end: h.pconn}, nil
	}
	return osIntoUnixConn(file)
}

// DupFD returns an independent duplicate FD referring to l's socket.
//
//ergo:host
//ergo:wait none
func (l *Listener) DupFD() (*FD, error) {
	if l == nil {
		return nil, errListenerClosed
	}
	if l.host != nil {
		return osDupListenerFD(l.host)
	}
	p := l.pipe
	if p == nil {
		return nil, errListenerClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.closed || l.state == nil || l.state.closed {
		return nil, errListenerClosed
	}
	l.state.refs++
	return &FD{
		pipe:  p,
		kind:  fdTCPListener,
		fdNum: p.nextFDLocked(),
		plist: l.state,
	}, nil
}

// DupFD returns an independent duplicate FD referring to c's socket.
//
//ergo:host
//ergo:wait none
func (c *Conn) DupFD() (*FD, error) {
	if c == nil {
		return nil, errClosed
	}
	if c.host != nil {
		return osDupConnFD(c.host)
	}
	p := c.pipe
	if p == nil {
		return nil, errClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.shut || c.end == nil || c.end.shut {
		return nil, errClosed
	}
	c.end.refs++
	return &FD{
		pipe:  p,
		kind:  fdTCPConn,
		fdNum: p.nextFDLocked(),
		pconn: c.end,
	}, nil
}

// UnixListener is net.UnixListener: a bound Unix domain socket accepting
// UnixConn connections.
type UnixListener struct {
	host *osUnixListener

	pipe          *PipeNet
	addr          string
	state         *pipeListenerState
	unlinkOnClose bool
	closed        bool
}

// runtimeOnly implements net.UnixListener's private operation.
func (*UnixListener) runtimeOnly() {}

// Addr returns the filesystem path l is bound to.
//
//ergo:host
//ergo:wait none
func (l *UnixListener) Addr() string {
	if l == nil {
		return ""
	}
	if l.host != nil {
		return osUnixListenerAddr(l.host)
	}
	return l.addr
}

// SetUnlinkOnClose controls whether Close unlinks the socket path.
//
//ergo:host
//ergo:wait none
func (l *UnixListener) SetUnlinkOnClose(unlink bool) {
	if l == nil {
		return
	}
	if l.host != nil {
		osUnixListenerSetUnlink(l.host, unlink)
		return
	}
	if l.pipe == nil {
		return
	}
	l.pipe.mu.Lock()
	l.unlinkOnClose = unlink
	l.pipe.mu.Unlock()
}

// Accept waits on t for the next connection dialed to l.
//
//ergo:host
//ergo:wait caller
func (l *UnixListener) Accept(t *rt.Task) (*UnixConn, error) {
	t.Check()
	if l == nil {
		return nil, errListenerClosed
	}
	if l.host != nil {
		return osUnixListenerAccept(t, l.host)
	}
	p := l.pipe
	if p == nil {
		return nil, errListenerClosed
	}
	p.mu.Lock()
	closed := l.closed
	st := l.state
	p.mu.Unlock()
	if closed || st == nil {
		return nil, errListenerClosed
	}
	end, err := st.acceptOn(t.Done())
	if err == errStopped {
		t.Check()
	}
	if err != nil {
		return nil, err
	}
	return &UnixConn{pipe: p, end: end}, nil
}

// DupFD returns an independent duplicate FD referring to l's socket.
//
//ergo:host
//ergo:wait none
func (l *UnixListener) DupFD() (*FD, error) {
	if l == nil {
		return nil, errListenerClosed
	}
	if l.host != nil {
		return osDupUnixListenerFD(l.host)
	}
	p := l.pipe
	if p == nil {
		return nil, errListenerClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.closed || l.state == nil || l.state.closed {
		return nil, errListenerClosed
	}
	l.state.refs++
	return &FD{
		pipe:  p,
		kind:  fdUnixListener,
		fdNum: p.nextFDLocked(),
		plist: l.state,
	}, nil
}

// Close releases l. A second Close does nothing.
//
//ergo:host
//ergo:wait none
func (l *UnixListener) Close() {
	if l == nil {
		return
	}
	if l.host != nil {
		osUnixListenerClose(l.host)
		return
	}
	p := l.pipe
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.unlinkOnClose && p.unixListeners[l.addr] == l.state {
		delete(p.unixListeners, l.addr)
		p.signal()
	}
	if l.state != nil {
		l.state.refs--
		if l.state.refs == 0 && !l.state.closed {
			l.state.closed = true
			for _, c := range l.state.backlog {
				c.reset()
			}
			l.state.backlog = nil
			p.signal()
		}
	}
}

// UnixConn is net.UnixConn: one end of a Unix domain socket stream
// supporting both byte streams and SCM_RIGHTS FD passing.
type UnixConn struct {
	host *osUnixConn
	buf  []byte

	pipe   *PipeNet
	end    *pipeConnEnd
	closed bool
}

// runtimeOnly implements net.UnixConn's private operation.
func (*UnixConn) runtimeOnly() {}

// Read returns the next chunk written by the peer, or "" at EOF.
//
//ergo:host
//ergo:wait caller
func (c *UnixConn) Read(t *rt.Task) (string, error) {
	t.Check()
	if c == nil {
		return "", errClosed
	}
	if c.host != nil {
		if c.buf == nil {
			c.buf = make([]byte, readMax)
		}
		return osUnixConnRead(t, c.host, c.buf)
	}
	if c.pipe == nil || c.end == nil {
		return "", errClosed
	}
	return c.end.read(t, &c.closed)
}

// Write writes all bytes of data to the peer.
//
//ergo:host
//ergo:wait caller
func (c *UnixConn) Write(t *rt.Task, data string) error {
	t.Check()
	if c == nil {
		return errClosed
	}
	if c.host != nil {
		return osUnixConnWrite(t, c.host, data)
	}
	if c.pipe == nil || c.end == nil {
		return errClosed
	}
	return c.end.write(t, &c.closed, data)
}

// SendFDs sends a framed data message with attached file descriptors.
//
//ergo:host
//ergo:wait caller
func (c *UnixConn) SendFDs(t *rt.Task, data string, fds []*FD) error {
	t.Check()
	if c == nil {
		return errClosed
	}
	if c.host != nil {
		return osUnixConnSendFDs(t, c.host, data, fds)
	}
	if c.pipe == nil || c.end == nil {
		return errClosed
	}
	return c.end.sendFDs(t, &c.closed, data, fds)
}

// RecvFDs receives one framed message and up to maxFDs attached file
// descriptors.
//
//ergo:host
//ergo:wait caller
func (c *UnixConn) RecvFDs(t *rt.Task, maxFDs int) (NetReceivedFDs, error) {
	t.Check()
	if c == nil {
		return NetReceivedFDs{}, errClosed
	}
	if c.host != nil {
		return osUnixConnRecvFDs(t, c.host, maxFDs)
	}
	if c.pipe == nil || c.end == nil {
		return NetReceivedFDs{}, errClosed
	}
	return c.end.recvFDs(t, &c.closed, maxFDs)
}

// DupFD returns an independent duplicate FD referring to c's socket.
//
//ergo:host
//ergo:wait none
func (c *UnixConn) DupFD() (*FD, error) {
	if c == nil {
		return nil, errClosed
	}
	if c.host != nil {
		return osDupUnixConnFD(c.host)
	}
	p := c.pipe
	if p == nil {
		return nil, errClosed
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.closed || c.end == nil || c.end.shut {
		return nil, errClosed
	}
	c.end.refs++
	return &FD{
		pipe:  p,
		kind:  fdUnixConn,
		fdNum: p.nextFDLocked(),
		pconn: c.end,
	}, nil
}

// LocalAddr returns the local address string of c.
//
//ergo:host
//ergo:wait none
func (c *UnixConn) LocalAddr() string {
	if c == nil {
		return ""
	}
	if c.host != nil {
		return osUnixConnLocalAddr(c.host)
	}
	if c.end == nil {
		return ""
	}
	return c.end.local
}

// RemoteAddr returns the remote address string of c.
//
//ergo:host
//ergo:wait none
func (c *UnixConn) RemoteAddr() string {
	if c == nil {
		return ""
	}
	if c.host != nil {
		return osUnixConnRemoteAddr(c.host)
	}
	if c.end == nil {
		return ""
	}
	return c.end.remote
}

// Close releases c. A second Close does nothing.
//
//ergo:host
//ergo:wait none
func (c *UnixConn) Close() {
	if c == nil {
		return
	}
	if c.host != nil {
		osUnixConnClose(c.host)
		return
	}
	p := c.pipe
	if p == nil || c.end == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.end.refs--
	if c.end.refs == 0 {
		c.end.closeZeroLocked()
	}
	p.signal()
}

// NetListenUnix binds the host's Unix domain socket path.
//
//ergo:host
//ergo:wait none
func NetListenUnix(path string) (*UnixListener, error) {
	return osListenUnix(path)
}

// NetDialUnix connects to the host's Unix domain socket path, waiting on t.
//
//ergo:host
//ergo:wait caller
func NetDialUnix(t *rt.Task, path string) (*UnixConn, error) {
	t.Check()
	return osDialUnix(t, path)
}

// NetAdoptFD duplicates an open OS descriptor fd as an owned FD.
//
//ergo:host
//ergo:wait none
func NetAdoptFD(fd int) (*FD, error) {
	return osAdoptFD(fd)
}

// NetConfineUnix resolves a Unix domain socket path inside root for
// NewTestUnixHost, rejecting paths escaping root, abstract names, or NUL bytes.
//
//ergo:host
//ergo:wait none
func NetConfineUnix(root, path string) (string, error) {
	if strings.HasPrefix(path, "@") || strings.IndexByte(path, 0) >= 0 {
		return "", NetDenied{Addr: path}
	}
	if path == "" {
		return "", nil
	}
	resolved, err := v1.FSConfineLast(root, path)
	if err != nil {
		return "", NetDenied{Addr: path}
	}
	return resolved, nil
}
