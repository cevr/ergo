// Package net holds the bodies of std net (docs/spec/stdlib.md, net): a
// stream connection (Conn) and a listener that accepts them (Listener),
// over the host's TCP (NetListen, NetDial) or over memory (PipeNet, the
// body of nettest.Pipe). Both kinds keep one contract, which
// TestNetHostAgrees holds: the bytes a side writes reach the other side's
// reads in order, a read after the peer closed gives the bytes left and
// then the end (""), a dial to an address with no listener fails, and a
// listener accepts its connections in the order they were dialed.
//
// A blocked Accept, Read, Write or Dial is an interruption point: a stop
// of the caller's task ends the wait with the interruption (I1). On the
// host the wait is a system call, so a stop moves the call's deadline into
// the past (Go's net/http stops a blocked read the same way); inside a
// mask (Done is nil) nothing is armed and the wait is uninterruptible (I2).
//
// It is a leaf of runtime/v1 (binsize BS1): only a program that names one
// of its bodies links Go's net.
//
// Prior art: Go's net.Conn and net.Pipe; Effect platform's Socket and
// SocketServer; Erlang's gen_tcp, whose recv(Socket, 0) gives the bytes
// available, as Read does.
package net

import (
	"context"
	"errors"
	"io"
	gonet "net"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// readMax is the most bytes one Read gives, and the bytes a pipe holds in
// each direction before a Write waits for the reader (a socket buffer's
// order of size).
const readMax = 64 << 10

// NetListenFailed is net.ListenFailed: Addr could not be bound.
type NetListenFailed struct{ Addr, Reason string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (e NetListenFailed) Error() string {
	return "net.ListenFailed{Addr:" + e.Addr + " Reason:" + e.Reason + "}"
}

// NetDialFailed is net.DialFailed: no connection to Addr was made.
type NetDialFailed struct{ Addr, Reason string }

func (e NetDialFailed) Error() string {
	return "net.DialFailed{Addr:" + e.Addr + " Reason:" + e.Reason + "}"
}

// NetFailed is net.Failed: a read, write or accept of an open connection
// or listener failed, such as a connection the peer reset.
type NetFailed struct{ Reason string }

func (e NetFailed) Error() string { return "net.Failed{Reason:" + e.Reason + "}" }

// Conn is net.Conn: one end of a stream connection, on the host (host) or
// in memory (pipe).
type Conn struct {
	host gonet.Conn
	buf  []byte // host's read buffer, made at the first Read

	pipe          *PipeNet
	in, out       *half
	local, remote string
}

// runtimeOnly implements net.Conn's private operation (runtime/v1/sealed.go).
func (*Conn) runtimeOnly() {}

// Read is c.Read(): the next bytes the peer wrote, at most 64 KiB, waiting
// on the caller's task t until one is there; "" once the peer closed and
// every byte it wrote was read. A stop of t ends the wait with the
// interruption; bytes a read took are never dropped by a stop.
//
//ergo:wait caller
func (c *Conn) Read(t *rt.Task) (string, error) {
	t.Check()
	if c.host == nil {
		return c.pipeRead(t)
	}
	if c.buf == nil {
		c.buf = make([]byte, readMax)
	}
	disarm := onStop(t, c.host.SetReadDeadline)
	n, err := c.host.Read(c.buf)
	fired := disarm()
	if n > 0 {
		return string(c.buf[:n]), nil
	}
	if err == io.EOF {
		return "", nil
	}
	if fired {
		t.Check()
	}
	return "", NetFailed{Reason: err.Error()}
}

// Write is c.Write(data): it writes every byte of data, waiting on t
// while the peer has not read enough. A stop of t ends the wait with the
// interruption, after a part of data may have been written. A write to a
// peer that closed fails, at once in memory and, on the host, at the
// latest at the next write after the peer's reset arrived.
//
//ergo:wait caller
func (c *Conn) Write(t *rt.Task, data string) error {
	t.Check()
	if c.host == nil {
		return c.pipeWrite(t, data)
	}
	disarm := onStop(t, c.host.SetWriteDeadline)
	_, err := io.WriteString(c.host, data)
	fired := disarm()
	if err == nil {
		return nil
	}
	if fired {
		t.Check()
	}
	return NetFailed{Reason: err.Error()}
}

// LocalAddr is the address of c's own end.
//
//ergo:wait none
func (c *Conn) LocalAddr() string {
	if c.host != nil {
		return c.host.LocalAddr().String()
	}
	return c.local
}

// RemoteAddr is the address of the peer's end.
//
//ergo:wait none
func (c *Conn) RemoteAddr() string {
	if c.host != nil {
		return c.host.RemoteAddr().String()
	}
	return c.remote
}

// Close is the release of c (resources.md R5): the peer's reads give the
// bytes left and then the end, and its writes fail. As a TCP socket does,
// a close that leaves bytes unread resets the peer. It never waits, so it
// needs no mask.
//
//ergo:wait none
func (c *Conn) Close() {
	if c.host != nil {
		_ = c.host.Close()
		return
	}
	c.pipeClose()
}

// Listener is net.Listener: an address that accepts connections.
type Listener struct {
	host gonet.Listener

	pipe    *PipeNet
	addr    string
	backlog []*Conn // dialed, not accepted yet
	closed  bool
}

// runtimeOnly implements net.Listener's private operation.
func (*Listener) runtimeOnly() {}

// Addr is the address l is bound to, with the port the host chose for a
// port 0.
//
//ergo:wait none
func (l *Listener) Addr() string {
	if l.host != nil {
		return l.host.Addr().String()
	}
	return l.addr
}

// Accept is l.Accept(): the next connection dialed to l, in the order of
// the dials, waiting on t until there is one. A stop of t ends the wait.
//
//ergo:wait caller
func (l *Listener) Accept(t *rt.Task) (*Conn, error) {
	t.Check()
	if l.host == nil {
		return l.pipeAccept(t)
	}
	d, _ := l.host.(interface{ SetDeadline(time.Time) error })
	set := func(time.Time) error { return nil }
	if d != nil {
		set = d.SetDeadline
	}
	disarm := onStop(t, set)
	conn, err := l.host.Accept()
	fired := disarm()
	if err == nil {
		return &Conn{host: conn}, nil
	}
	if fired {
		t.Check()
	}
	return nil, NetFailed{Reason: err.Error()}
}

// Close is the release of l: the address is free again, a dial to it
// fails, and each connection dialed and not accepted is reset. It never
// waits, so it needs no mask. A second Close does nothing.
//
//ergo:wait none
func (l *Listener) Close() {
	if l.host != nil {
		_ = l.host.Close()
		return
	}
	l.pipeClose()
}

// ListenerOn is a Listener over ln, which Go code makes: http's Listen,
// and an in-process transport for a benchmark of http's Serve.
func ListenerOn(ln gonet.Listener) *Listener { return &Listener{host: ln} }

// Host is the host listener l holds, nil for a pipe's: http's Serve
// serves it.
func (l *Listener) Host() gonet.Listener { return l.host }

// NetListen binds the host's TCP address addr ("host:port"); port 0 has
// the host choose one, which Addr gives.
//
//ergo:host
//ergo:wait none
func NetListen(addr string) (*Listener, error) {
	ln, err := gonet.Listen("tcp", addr)
	if err != nil {
		return nil, NetListenFailed{Addr: addr, Reason: reason(err)}
	}
	return &Listener{host: ln}, nil
}

// NetDial connects to the host's TCP address addr, waiting on t. A stop of
// t ends the wait with the interruption.
//
//ergo:host
//ergo:wait caller
func NetDial(t *rt.Task, addr string) (*Conn, error) {
	t.Check()
	ctx := context.Background()
	if t.Done() != nil {
		ctx = rt.GoContext(t)
	}
	conn, err := (&gonet.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Check()
		return nil, NetDialFailed{Addr: addr, Reason: reason(err)}
	}
	return &Conn{host: conn}, nil
}

// reason is the host's cause of err without Go's operation and address
// prefix ("dial tcp 127.0.0.1:1: connect: "), which the failure names in
// its own fields.
func reason(err error) string {
	var op *gonet.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

// past is a deadline that has passed: setting it ends a blocked call.
var past = time.Unix(1, 0)

// stopWatch is the state of one onStop: whether the stop moved the
// deadline (fired) and whether the call is over, under mu, so a stop that
// comes after the call never moves the deadline of the next one.
type stopWatch struct {
	mu          sync.Mutex
	fired, over bool
	quit        chan struct{}
}

// onStop arms a move of the deadline set into the past for when a stop of
// t is requested during a host call, and returns the disarm, which reports
// whether the stop fired; a fired stop's deadline is cleared again, so the
// connection stays usable to whoever releases it. Inside a mask nothing is
// armed. It costs one goroutine per blocking call, which is what lets a
// stop end a system call that has no other way out.
func onStop(t *rt.Task, set func(time.Time) error) (disarm func() bool) {
	done := t.Done()
	if done == nil {
		return func() bool { return false }
	}
	w := &stopWatch{quit: make(chan struct{})}
	go func() {
		select {
		case <-done:
			w.mu.Lock()
			if !w.over {
				w.fired = true
				_ = set(past)
			}
			w.mu.Unlock()
		case <-w.quit:
		}
	}()
	return func() bool {
		w.mu.Lock()
		w.over = true
		fired := w.fired
		w.mu.Unlock()
		close(w.quit)
		if fired {
			_ = set(time.Time{})
		}
		return fired
	}
}

// PipeNet is the memory network of nettest.Pipe: listeners by address and
// connections whose bytes stay in this process. One lock guards all of
// it; a wait takes the changed channel under it, and every change closes
// that channel and makes a new one.
type PipeNet struct {
	mu        sync.Mutex
	listeners map[string]*Listener
	ports     int // the last port given for a port 0
	changed   chan struct{}
}

// half is one direction of a pipe connection: the bytes written and not
// read, whether the writer closed (the reader ends after the bytes),
// whether the writer reset it (the reader fails) and whether the reader
// closed (a write fails).
type half struct {
	buf                 []byte
	closed, reset, gone bool
}

// runtimeOnly implements nettest.PipeNet's private operation.
func (*PipeNet) runtimeOnly() {}

// NetNewPipe is an empty memory network.
//
//ergo:wait none
func NetNewPipe() *PipeNet {
	return &PipeNet{listeners: map[string]*Listener{}, changed: make(chan struct{})}
}

// signal wakes every wait on p. Call it with p.mu held.
func (p *PipeNet) signal() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// wait waits on t for the next change of p after ch was taken, or for a
// stop of t, which raises the interruption.
func wait(t *rt.Task, ch chan struct{}) {
	select {
	case <-ch:
	case <-t.Done():
		t.Check()
	}
}

// Listen binds addr in p ("host:port"); port 0 gives the next free port
// from 1. An address in use fails, as on the host.
//
//ergo:wait none
func (p *PipeNet) Listen(addr string) (*Listener, error) {
	i := strings.LastIndexByte(addr, ':') // the port follows the last colon, also after an IPv6 host
	if i < 0 {
		return nil, NetListenFailed{Addr: addr, Reason: "missing port in address"}
	}
	host, port := addr[:i], addr[i+1:]
	p.mu.Lock()
	defer p.mu.Unlock()
	if port == "0" {
		for {
			p.ports++
			addr = host + ":" + strconv.Itoa(p.ports)
			if p.listeners[addr] == nil {
				break
			}
		}
	}
	if p.listeners[addr] != nil {
		return nil, NetListenFailed{Addr: addr, Reason: "address already in use"}
	}
	l := &Listener{pipe: p, addr: addr}
	p.listeners[addr] = l
	return l, nil
}

// Dial connects to the listener at addr in p: the connection waits in its
// backlog until an Accept takes it, and the dialer may write at once, as
// the host's backlog allows. No listener at addr fails.
//
//ergo:wait none
func (p *PipeNet) Dial(addr string) (*Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l := p.listeners[addr]
	if l == nil {
		return nil, NetDialFailed{Addr: addr, Reason: "connection refused"}
	}
	p.ports++
	local := addr[:strings.LastIndexByte(addr, ':')+1] + strconv.Itoa(p.ports)
	up, down := &half{}, &half{}
	client := &Conn{pipe: p, in: down, out: up, local: local, remote: addr}
	server := &Conn{pipe: p, in: up, out: down, local: addr, remote: local}
	l.backlog = append(l.backlog, server)
	p.signal()
	return client, nil
}

func (l *Listener) pipeAccept(t *rt.Task) (*Conn, error) {
	p := l.pipe
	for {
		p.mu.Lock()
		if len(l.backlog) > 0 {
			c := l.backlog[0]
			l.backlog = l.backlog[1:]
			p.mu.Unlock()
			return c, nil
		}
		if l.closed {
			p.mu.Unlock()
			return nil, NetFailed{Reason: "use of closed listener"}
		}
		ch := p.changed
		p.mu.Unlock()
		wait(t, ch)
	}
}

func (l *Listener) pipeClose() {
	p := l.pipe
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	delete(p.listeners, l.addr)
	for _, c := range l.backlog {
		c.reset()
	}
	l.backlog = nil
	p.signal()
}

func (c *Conn) pipeRead(t *rt.Task) (string, error) {
	p := c.pipe
	for {
		p.mu.Lock()
		in := c.in
		if len(in.buf) > 0 {
			n := min(len(in.buf), readMax)
			s := string(in.buf[:n])
			in.buf = in.buf[n:]
			p.signal()
			p.mu.Unlock()
			return s, nil
		}
		if in.reset {
			p.mu.Unlock()
			return "", NetFailed{Reason: "connection reset by peer"}
		}
		if in.closed {
			p.mu.Unlock()
			return "", nil
		}
		ch := p.changed
		p.mu.Unlock()
		wait(t, ch)
	}
}

func (c *Conn) pipeWrite(t *rt.Task, data string) error {
	p := c.pipe
	for {
		p.mu.Lock()
		out := c.out
		if out.gone {
			p.mu.Unlock()
			return NetFailed{Reason: "broken pipe"}
		}
		if n := min(readMax-len(out.buf), len(data)); n > 0 {
			out.buf = append(out.buf, data[:n]...)
			data = data[n:]
			p.signal()
		}
		if data == "" {
			p.mu.Unlock()
			return nil
		}
		ch := p.changed
		p.mu.Unlock()
		wait(t, ch)
	}
}

// pipeClose closes both directions of c: the peer reads to the end, or
// fails when c left bytes unread (a TCP close with unread bytes resets).
func (c *Conn) pipeClose() {
	p := c.pipe
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(c.in.buf) > 0 {
		c.reset()
	} else {
		c.out.closed = true
		c.in.gone = true
		c.in.buf = nil
	}
	p.signal()
}

// reset ends c with a reset: its peer's reads fail and its writes too.
// Call it with the pipe's lock held.
func (c *Conn) reset() {
	c.out.reset = true
	c.in.gone = true
	c.in.buf = nil
}
