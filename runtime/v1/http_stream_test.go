package v1

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestHTTPStreamFlushesEachPart holds a streamed response between two
// parts: the client reads the first part while the handler still waits,
// so each Write reaches the client before it returns (kills
// stream-flush). The body is the parts alone, not the response the
// handler returns after streaming (stream-second-answer), and a writer
// kept past the request's end fails WriteFailed (stream-late-write). No
// buffered row or Ergo law sees a socket's flush or the transport's end.
func TestHTTPStreamFlushesEachPart(t *testing.T) {
	release := make(chan struct{})
	kept := make(chan *Exchange, 1)
	l, _ := startHTTP(t, func(ct *rt.Task, x *Exchange, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		x.Begin(200, []HTTPField{{"Content-Type", "application/x-ndjson"}})
		if err := x.Write(ct, "one\n"); err != nil {
			t.Errorf("first part: %v", err)
		}
		select {
		case <-release:
		case <-ct.Done():
			ct.Check()
		}
		if err := x.Write(ct, "two\n"); err != nil {
			t.Errorf("second part: %v", err)
		}
		kept <- x
		return HTTPResponse{Status: 500, Body: "not sent"}
	})
	resp := httpRequest(t, httpClient(t), "GET", "http://"+l.Addr()+"/stream", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("stream head: %d %v", resp.StatusCode, resp.Header)
	}
	r := bufio.NewReader(resp.Body)
	first, err := r.ReadString('\n')
	if err != nil || first != "one\n" {
		t.Fatalf("first part before release: %q %v", first, err)
	}
	close(release)
	rest, err := io.ReadAll(r)
	if err != nil || string(rest) != "two\n" {
		t.Fatalf("rest of the stream: %q %v", rest, err)
	}
	x := <-kept
	if err := x.Write(rt.Root("late"), "late"); err != (HTTPWriteFailed{Reason: "the stream has ended"}) {
		t.Fatalf("write after the request ended: %v", err)
	}
}

// TestHTTPStreamHangUpStopsTheWriter: a client that goes away mid-stream
// stops the request task, and the writer's next Write ends with the stop
// rather than returning (kills stream-stop-check); the task's release
// runs. The buffered disconnect test never writes after the stop.
func TestHTTPStreamHangUpStopsTheWriter(t *testing.T) {
	wrote, ended := make(chan struct{}), make(chan struct{})
	var returned atomic.Bool
	l, _ := startHTTP(t, func(ct *rt.Task, x *Exchange, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		s := NewScope(ct)
		defer s.End(nil)
		OnExit(&s, func() { close(ended) })
		x.Begin(200, nil)
		if err := x.Write(ct, "one\n"); err != nil {
			t.Errorf("first part: %v", err)
		}
		close(wrote)
		<-ct.Done()
		_ = x.Write(ct, "after the hang-up\n")
		returned.Store(true)
		return HTTPResponse{Status: 200}
	})
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", l.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /stream HTTP/1.1\r\nHost: local\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "one") {
		t.Fatalf("first part: %q %v", line, err)
	}
	<-wrote
	conn.Close()
	<-ended
	if returned.Load() {
		t.Fatal("a Write after the hang-up's stop returned")
	}
}
