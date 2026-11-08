package ergo

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// stallingListener wraps accepted connections so the first Write returns to
// the server only when the test resumes it, after the bytes are on the wire.
type stallingListener struct {
	net.Listener
	written chan struct{}
	resume  chan struct{}
	first   atomic.Bool
}

type stallingConn struct {
	net.Conn
	l *stallingListener
}

func (l *stallingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &stallingConn{c, l}, nil
}

func (c *stallingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if c.l.first.CompareAndSwap(false, true) {
		close(c.l.written)
		<-c.l.resume
	}
	return n, err
}

// docs/runtime.md: a request holds its admission until its response has been
// handed to the operating system. A sequential client that reconnects inside
// that hand-off window is at maxActive and receives 503; once the server's
// write returns the slot is free and the same request succeeds. This pins the
// documented policy (reject at the cap, no waiting); it is not a guarantee
// that a sequential client never sees 503.
func TestAdmissionIsHeldUntilTheResponseIsHandedToTheOperatingSystem(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	limits := HTTPLimits{MaxBodyBytes: 4, ReadHeaderTimeout: time.Second, ReadBodyTimeout: time.Second, IdleTimeout: time.Second, MaxActive: 1}
	transport := &httpTransport{server: ctx, scopes: newRequestScopes(ctx), limits: limits, active: make(chan struct{}, 1),
		handler: func(HTTPRequest) Effect[HTTPResponse] {
			return func(*FiberContext) Exit[HTTPResponse] { return respond(200, "text/plain", "ok") }
		}}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &stallingListener{Listener: inner, written: make(chan struct{}), resume: make(chan struct{})}
	server := &http.Server{Handler: transport}
	go server.Serve(listener)
	defer server.Close()
	address := inner.Addr().String()
	raw := "GET /a HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"
	first := make(chan int, 1)
	go func() {
		response, _ := exchange(t, address, raw)
		first <- response.StatusCode
	}()
	<-listener.written
	select {
	case status := <-first:
		if status != 200 {
			t.Fatalf("first response: %d", status)
		}
	case <-testwait.After(t):
		close(listener.resume)
		t.Fatal("client did not receive the first response")
	}
	during, _ := exchange(t, address, raw)
	close(listener.resume)
	if during == nil || during.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a request inside the hand-off window: %v", during)
	}
	var after *http.Response
	if !testwait.Until(t, func() bool {
		after, _ = exchange(t, address, raw)
		return after != nil && after.StatusCode == http.StatusOK
	}) {
		t.Fatalf("slot was not released after the write returned: %v", after)
	}
}
