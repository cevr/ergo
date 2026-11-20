package http

import (
	"io"
	"net"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestHTTPWatcherSeesWhatTheReadSees holds the server's peer-close watcher
// (HTTP-FAST-c6) to the rules of net/http's background read, which
// -http.watcher=false runs the same test on, over loopback TCP, where the
// watcher serves:
//   - bytes ahead of a close: a handler that polls with the next request
//     and the client's close waiting is not stopped, since the read would
//     have read those bytes and stopped watching; the next request, read
//     from them, is stopped when it polls, since its read meets the close
//     (pending);
//   - a close before the first poll: a handler that polls only after the
//     client has left is stopped when it does, since the read started then
//     meets the end at once (eof).
//
// Each connection first answers /warm, whose poll registers it with the
// watcher, so the close comes to a registered connection with no watch
// armed. The watcher's event is awaited before the handler polls, so the
// order each rule is about is the one that runs. Mutants: c6-data-is-gone,
// c6-pending-unread, c6-eof-forgotten (runtime/v1/http ledger).
func TestHTTPWatcherSeesWhatTheReadSees(t *testing.T) {
	if !*engineFlag {
		t.Skip("net/http reads every connection: the watcher is the engine's")
	}
	events := make(chan struct{}, 16)
	if *watcherFlag {
		engineWatchEvent = func() {
			select {
			case events <- struct{}{}:
			default:
			}
		}
		t.Cleanup(func() { engineWatchEvent = nil })
	}
	// watcherSaw waits for the watcher's event, when the watcher serves.
	watcherSaw := func(t *testing.T) {
		t.Helper()
		if !*watcherFlag {
			return
		}
		select {
		case <-events:
		case <-testwait.After(t):
			t.Fatal("the watcher saw no event of the client's close")
		}
	}
	started, goOn, stopped := make(chan string, 4), make(chan struct{}), make(chan string, 4)
	// abort frees a handler held at goOn when the test fails, before the
	// server stops: its cleanup is registered after the server's, and
	// cleanups run last first.
	abort := make(chan struct{})
	next := func(t *testing.T) string {
		t.Helper()
		select {
		case p := <-started:
			return p
		case <-testwait.After(t):
			t.Fatal("no handler started")
			return ""
		}
	}
	release := func(t *testing.T) {
		t.Helper()
		select {
		case goOn <- struct{}{}:
		case <-testwait.After(t):
			t.Fatal("no handler waited to poll")
		}
	}
	l, _ := startHTTP(t, func(ct *rt.Task, _ *Exchange, _, path, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		defer func() {
			if ct.StopRequested() {
				stopped <- path
			}
		}()
		started <- path
		if path != "/next" && path != "/warm" {
			select { // Go channels: no interruption point
			case <-goOn:
			case <-abort:
				return HTTPResponse{Status: 500}
			}
		}
		ct.Check()
		if path == "/first" || path == "/warm" {
			return HTTPResponse{Status: 200, Body: path}
		}
		for {
			ct.Check() // until the client's leaving stops it
		}
	})
	t.Cleanup(func() { close(abort) })
	const warm = "GET /warm HTTP/1.1\r\nHost: x\r\n\r\n"
	// exchange sends /warm and then raw, waits for raw's handler, then
	// sends more and closes its side.
	exchange := func(t *testing.T, raw string, more string) net.Conn {
		conn, err := net.Dial("tcp", l.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, warm+raw); err != nil {
			t.Fatal(err)
		}
		next(t) // /warm
		next(t)
		if more != "" {
			if _, err := io.WriteString(conn, more); err != nil {
				t.Fatal(err)
			}
		}
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	t.Run("bytes ahead of the close", func(t *testing.T) {
		conn := exchange(t, "GET /first HTTP/1.1\r\nHost: x\r\n\r\n", "GET /next HTTP/1.1\r\nHost: x\r\n\r\n")
		defer conn.Close()
		watcherSaw(t)
		release(t)
		if p := next(t); p != "/next" {
			t.Fatalf("the second handler ran %s", p)
		}
		select {
		case p := <-stopped:
			if p != "/next" {
				t.Fatalf("%s was stopped, though bytes came ahead of the close", p)
			}
		case <-testwait.After(t):
			t.Fatal("/next, polling with the client gone behind its bytes, was never stopped")
		}
		got, _ := io.ReadAll(conn)
		if n := strings.Count(string(got), "HTTP/1.1 200 OK"); n != 2 || !strings.HasSuffix(string(got), "/first") {
			t.Fatalf("want the answers of /warm and /first alone, got:\n%s", got)
		}
	})
	t.Run("close before the first poll", func(t *testing.T) {
		conn := exchange(t, "GET /late HTTP/1.1\r\nHost: x\r\n\r\n", "")
		defer conn.Close()
		watcherSaw(t)
		release(t)
		select {
		case p := <-stopped:
			if p != "/late" {
				t.Fatalf("%s was stopped", p)
			}
		case <-testwait.After(t):
			t.Fatal("/late, polling after its client left, was never stopped")
		}
	})
}
