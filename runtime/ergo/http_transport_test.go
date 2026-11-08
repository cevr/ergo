package ergo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testport"
	"ergo.local/prototype/internal/testwait"
)

var testLimits = HTTPLimits{MaxBodyBytes: 4, ReadHeaderTimeout: time.Second, ReadBodyTimeout: 200 * time.Millisecond, IdleTimeout: time.Second, MaxActive: 4}

type transportServer struct {
	address string
	cancel  context.CancelFunc
	done    chan Exit[Unit]
	calls   atomic.Int64
}

func startTransport(t *testing.T, limits HTTPLimits, handler func(*FiberContext, HTTPRequest) Exit[HTTPResponse]) *transportServer {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := &transportServer{cancel: cancel, done: make(chan Exit[Unit], 1)}
	bound := make(chan string, 1)
	go func() {
		server.done <- RunContext(ctx, ServeHTTPRequests("127.0.0.1:0", limits, func(request HTTPRequest) Effect[HTTPResponse] {
			return func(fc *FiberContext) Exit[HTTPResponse] {
				server.calls.Add(1)
				return handler(fc, request)
			}
		}, func(address string) { bound <- address }))
	}()
	server.address = wait(t, bound)
	t.Cleanup(func() {
		cancel()
		wait(t, server.done)
	})
	return server
}

func TestHTTPTransportStartupFailureIsTyped(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	out := Run(ServeHTTPRequests(listener.Addr().String(), testLimits, func(HTTPRequest) Effect[HTTPResponse] {
		return func(*FiberContext) Exit[HTTPResponse] { return respond(200, "text/plain", "unreachable") }
	}, nil))
	if out.Failure == nil || out.Failure.Tag != "IoError" {
		t.Fatalf("startup failure: %+v", out)
	}
}

// exchange writes raw request bytes and reads one response, or reports that
// the server closed the connection without any response bytes.
func exchange(t *testing.T, address, raw string) (*http.Response, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(testEnd(t))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	return readResponse(t, bufio.NewReader(conn))
}

// testEnd is a connection deadline at the test's own (testwait.End); the
// zero time, no deadline, when the test has none.
func testEnd(t *testing.T) time.Time {
	t.Helper()
	end, _ := testwait.End(t)
	return end
}

func readResponse(t *testing.T, reader *bufio.Reader) (*http.Response, []byte) {
	t.Helper()
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return nil, nil
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, body
}

func requireStatusOnly(t *testing.T, response *http.Response, body []byte, status int) {
	t.Helper()
	if response == nil || response.StatusCode != status || len(body) != 0 || response.Header.Get("Content-Type") != "" {
		t.Fatalf("expected empty %d without content type, received %v %q", status, response, body)
	}
}

func respond(status int, contentType string, body string) Exit[HTTPResponse] {
	return Succeed(HTTPResponse{Status: status, ContentType: contentType, Body: []byte(body)})
}

func TestHTTPTransportDeliversExactRequestAndPublishesBufferedResponse(t *testing.T) {
	server := startTransport(t, testLimits, func(_ *FiberContext, request HTTPRequest) Exit[HTTPResponse] {
		switch request.Path {
		case "/echo":
			if request.Method != "PUT" || request.ContentType != "application/octet-stream" {
				return respond(400, "", "")
			}
			return Succeed(HTTPResponse{Status: 201, ContentType: "application/x-echo", Body: request.Body})
		case "/sniff":
			return respond(200, "", "<html>")
		case "/a%2Fb":
			return respond(200, "text/plain", "exact")
		default:
			return respond(404, "", "")
		}
	})
	response, body := exchange(t, server.address, "PUT /echo?ignored=1 HTTP/1.1\r\nHost: x\r\nContent-Type: application/octet-stream\r\nContent-Length: 3\r\n\r\n\xff\x00a")
	if response == nil || response.StatusCode != 201 || response.Header.Get("Content-Type") != "application/x-echo" || string(body) != "\xff\x00a" {
		t.Fatalf("echo: %v %q", response, body)
	}
	response, body = exchange(t, server.address, "GET /sniff HTTP/1.1\r\nHost: x\r\n\r\n")
	if response == nil || response.StatusCode != 200 || response.Header.Get("Content-Type") != "" || string(body) != "<html>" {
		t.Fatalf("an omitted content type was sniffed: %v %q", response, body)
	}
	response, body = exchange(t, server.address, "GET /a%2Fb HTTP/1.1\r\nHost: x\r\n\r\n")
	if response == nil || string(body) != "exact" {
		t.Fatalf("path was decoded or normalized: %v %q", response, body)
	}
	response, body = exchange(t, server.address, "GET /missing HTTP/1.1\r\nHost: x\r\n\r\n")
	requireStatusOnly(t, response, body, 404)
}

func TestHTTPTransportBoundsDeclaredAndChunkedBodiesBeforeHandling(t *testing.T) {
	server := startTransport(t, testLimits, func(_ *FiberContext, request HTTPRequest) Exit[HTTPResponse] {
		return Succeed(HTTPResponse{Status: 200, ContentType: "text/plain", Body: request.Body})
	})
	response, body := exchange(t, server.address, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\n12345")
	requireStatusOnly(t, response, body, 413)
	if !response.Close {
		t.Fatal("rejected body did not close the connection")
	}
	response, body = exchange(t, server.address, "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n3\r\n123\r\n2\r\n45\r\n0\r\n\r\n")
	requireStatusOnly(t, response, body, 413)
	if calls := server.calls.Load(); calls != 0 {
		t.Fatalf("oversized bodies reached the handler %d times", calls)
	}
	response, body = exchange(t, server.address, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\n1234")
	if response == nil || response.StatusCode != 200 || string(body) != "1234" {
		t.Fatalf("body at the declared limit: %v %q", response, body)
	}
	response, body = exchange(t, server.address, "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n2\r\n12\r\n2\r\n34\r\n0\r\n\r\n")
	if response == nil || response.StatusCode != 200 || string(body) != "1234" {
		t.Fatalf("chunked body at the limit: %v %q", response, body)
	}
}

func TestHTTPTransportRejectsMalformedAndStalledBodiesWithoutHandling(t *testing.T) {
	server := startTransport(t, testLimits, func(*FiberContext, HTTPRequest) Exit[HTTPResponse] { return respond(200, "", "") })
	response, body := exchange(t, server.address, "POST / HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n12\r\n0\r\n\r\n")
	requireStatusOnly(t, response, body, 400)
	// The server's body read timeout closes the connection: the read ends
	// at the server's close, an event, and not at this side's deadline
	// (fu-time-limits; it was a 2 s wall-clock bound).
	conn, err := net.Dial("tcp", server.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(testEnd(t))
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\n1"); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(conn); len(rest) != 0 || err != nil {
		t.Fatalf("stalled body: read %q, %v; want the server to close the connection with no response", rest, err)
	}
	if calls := server.calls.Load(); calls != 0 {
		t.Fatalf("rejected bodies reached the handler %d times", calls)
	}
}

// Go serves a connection's requests in order: a malformed body pipelined
// behind a request whose response is not yet written is answered after that
// response, never in its place, and the connection then closes.
func TestHTTPTransportAnswersAPipelinedMalformedBodyAfterTheEarlierResponse(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := startTransport(t, testLimits, func(*FiberContext, HTTPRequest) Exit[HTTPResponse] {
		close(started)
		<-release
		return respond(200, "text/plain", "slow")
	})
	conn, err := net.Dial("tcp", server.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(testEnd(t))
	if _, err := io.WriteString(conn, "GET /slow HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wait(t, started)
	if _, err := io.WriteString(conn, "POST /bad HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\nab\r\n0\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	close(release)
	reader := bufio.NewReader(conn)
	first, body := readResponse(t, reader)
	if first == nil || first.StatusCode != 200 || string(body) != "slow" {
		t.Fatalf("earlier response: %v %q", first, body)
	}
	second, body := readResponse(t, reader)
	requireStatusOnly(t, second, body, 400)
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatalf("connection stayed open after the 400: %v", err)
	}
	if calls := server.calls.Load(); calls != 1 {
		t.Fatalf("the malformed body reached the handler: %d calls", calls)
	}
}

func TestHTTPTransportFailsClosedWithGeneric500(t *testing.T) {
	server := startTransport(t, testLimits, func(fc *FiberContext, request HTTPRequest) Exit[HTTPResponse] {
		switch request.Path {
		case "/failure":
			return Fail[HTTPResponse]("Missing", "user")
		case "/defect":
			panic("handler defect")
		case "/status":
			return respond(99, "text/plain", "never")
		case "/header":
			return respond(200, "text/plain\r\nX-Injected: 1", "never")
		case "/unicode":
			return respond(200, "text/λ", "never")
		case "/control":
			return respond(200, "text/plain\x7f", "never")
		case "/cleanup":
			resource := Invoke(fc, AcquireRelease("request", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { return errors.New("cleanup failed") }))
			if resource.IsFailure() {
				return Propagate[HTTPResponse](resource)
			}
			return respond(200, "text/plain", "success before failed cleanup")
		}
		return respond(200, "", "")
	})
	for _, path := range []string{"/failure", "/defect", "/status", "/header", "/unicode", "/control", "/cleanup"} {
		response, body := exchange(t, server.address, "GET "+path+" HTTP/1.1\r\nHost: x\r\n\r\n")
		requireStatusOnly(t, response, body, 500)
	}
	if response, _ := exchange(t, server.address, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); response == nil || response.StatusCode != 200 {
		t.Fatalf("server did not serve after inadmissible responses: %v", response)
	}
}

func TestHTTPHeaderValuePolicyAdmitsVisibleASCIIAndBlanks(t *testing.T) {
	for value, want := range map[string]bool{"": true, "text/plain; charset=utf-8": true, "a\tb ~": true, "text/\u03bb": false, "caf\xe9": false, "a\r\nb": false, "a\x00": false, "a\x7f": false} {
		if got := admissibleHeaderValue(value); got != want {
			t.Fatalf("%q: admitted %v", value, got)
		}
	}
}

// The body exceeds every buffer between the handler and the socket, so an
// early publication would put its first bytes on the wire before cleanup.
func TestHTTPTransportPublishesOnlyAfterRequestCleanup(t *testing.T) {
	cleanupStarted, finish := make(chan struct{}), make(chan struct{})
	large := bytes.Repeat([]byte("d"), 1<<20)
	server := startTransport(t, testLimits, func(fc *FiberContext, _ HTTPRequest) Exit[HTTPResponse] {
		resource := Invoke(fc, AcquireRelease("request", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
			close(cleanupStarted)
			<-finish
			return nil
		}))
		if resource.IsFailure() {
			return Propagate[HTTPResponse](resource)
		}
		return Succeed(HTTPResponse{Status: 200, ContentType: "text/plain", Body: large})
	})
	conn, err := net.Dial("tcp", server.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(testEnd(t))
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	first := make(chan error, 1)
	go func() {
		_, err := reader.Peek(1)
		first <- err
	}()
	wait(t, cleanupStarted)
	select {
	case err := <-first:
		t.Fatalf("response bytes reached the wire before cleanup completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	if err := wait(t, first); err != nil {
		t.Fatal(err)
	}
	response, body := readResponse(t, reader)
	if response == nil || response.ContentLength != int64(len(large)) || !bytes.Equal(body, large) {
		t.Fatalf("response after cleanup: %v (%d bytes)", response, len(body))
	}
}

func TestHTTPTransportRejectsAdmissionBeyondActiveBound(t *testing.T) {
	limits := testLimits
	limits.MaxActive = 1
	started, finish := make(chan struct{}), make(chan struct{})
	server := startTransport(t, limits, func(_ *FiberContext, request HTTPRequest) Exit[HTTPResponse] {
		if request.Path == "/hold" {
			close(started)
			<-finish
		}
		return respond(200, "text/plain", request.Path)
	})
	held := make(chan []byte, 1)
	go func() {
		_, body := exchange(t, server.address, "GET /hold HTTP/1.1\r\nHost: x\r\n\r\n")
		held <- body
	}()
	wait(t, started)
	response, body := exchange(t, server.address, "GET /second HTTP/1.1\r\nHost: x\r\n\r\n")
	requireStatusOnly(t, response, body, 503)
	if calls := server.calls.Load(); calls != 1 {
		t.Fatalf("rejected admission reached the handler: %d calls", calls)
	}
	close(finish)
	if body := wait(t, held); string(body) != "/hold" {
		t.Fatalf("admitted request: %q", body)
	}
	// Reading the response does not join ServeHTTP: it releases admission
	// in a defer after writing the body. Wait for an admitted request, with
	// only empty 503 responses eligible for retry (#232).
	if !testwait.Until(t, func() bool {
		response, body := exchange(t, server.address, "GET /after HTTP/1.1\r\nHost: x\r\n\r\n")
		if response != nil && response.StatusCode == http.StatusServiceUnavailable {
			requireStatusOnly(t, response, body, http.StatusServiceUnavailable)
			return false
		}
		if response == nil || response.StatusCode != http.StatusOK || string(body) != "/after" {
			t.Fatalf("request after admission release: %v %q", response, body)
		}
		return true
	}) {
		t.Fatal("admission was not released before the test's deadline")
	}
	if calls := server.calls.Load(); calls != 2 {
		t.Fatalf("expected only the held and admitted requests to reach the handler: %d calls", calls)
	}
}

func TestHTTPTransportCancelsHandlerOnClientDisconnect(t *testing.T) {
	started, cleaned := make(chan struct{}), make(chan struct{})
	interrupted := make(chan bool, 1)
	server := startTransport(t, testLimits, func(fc *FiberContext, request HTTPRequest) Exit[HTTPResponse] {
		if request.Path != "/wait" {
			return respond(200, "text/plain", "alive")
		}
		resource := Invoke(fc, AcquireRelease("request", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { close(cleaned); return nil }))
		if resource.IsFailure() {
			return Propagate[HTTPResponse](resource)
		}
		close(started)
		<-fc.Context().Done()
		interrupted <- true
		return Interrupt[HTTPResponse](fc.Context().Err())
	})
	conn, err := net.Dial("tcp", server.address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /wait HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wait(t, started)
	conn.Close()
	if !wait(t, interrupted) {
		t.Fatal("handler was not cancelled")
	}
	wait(t, cleaned)
	if _, body := exchange(t, server.address, "GET /next HTTP/1.1\r\nHost: x\r\n\r\n"); string(body) != "alive" {
		t.Fatalf("server stopped after a client disconnect: %q", body)
	}
}

// requireReleased binds address again to prove the server released its
// listener. It runs only on a reserved address: elsewhere the released port
// could be assigned to another socket before the rebind (#112), so off Linux
// the step is skipped and the rest of the test still runs.
func requireReleased(t *testing.T, address string, reserved bool) {
	t.Helper()
	if !reserved {
		t.Log("release rebind skipped: no port reservation on this platform")
		return
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	listener.Close()
}

func TestHTTPTransportShutdownCancelsActiveWorkAndReports503AfterCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Where the port can be reserved, it stays reserved for the closing
	// rebind, which therefore fails only if the server kept its listener.
	served, reserved := testport.TryReserve(t)
	bound := make(chan string, 1)
	started, cleanupStarted, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContext(ctx, ServeHTTPRequests(served, testLimits, func(HTTPRequest) Effect[HTTPResponse] {
			return func(fc *FiberContext) Exit[HTTPResponse] {
				resource := Invoke(fc, AcquireRelease("request", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
					close(cleanupStarted)
					<-finish
					return nil
				}))
				if resource.IsFailure() {
					return Propagate[HTTPResponse](resource)
				}
				close(started)
				<-fc.Context().Done()
				return respond(200, "text/plain", "success after cancellation")
			}
		}, func(address string) { bound <- address }))
	}()
	address := wait(t, bound)
	if reserved && address != served {
		t.Fatalf("listening on %s, want %s", address, served)
	}
	received := make(chan *http.Response, 1)
	go func() {
		response, body := exchange(t, address, "GET /work HTTP/1.1\r\nHost: x\r\n\r\n")
		if response != nil && len(body) != 0 {
			response.StatusCode = -1
		}
		received <- response
	}()
	wait(t, started)
	cancel()
	wait(t, cleanupStarted)
	select {
	case response := <-received:
		t.Fatalf("response published before shutdown cleanup: %v", response)
	case <-done:
		t.Fatal("server completed before request cleanup")
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	response := wait(t, received)
	if response == nil || response.StatusCode != 503 || !response.Close {
		t.Fatalf("shutdown response: %v", response)
	}
	if !wait(t, done).Interrupted {
		t.Fatal("shutdown did not preserve cancellation")
	}
	requireReleased(t, address, reserved)
}

// A client that stops reading a large response must not hold shutdown open:
// the request scope has already closed, so only transport I/O remains and the
// server aborts it once its write grace (IdleTimeout) elapses.
func TestHTTPTransportShutdownAbortsAStalledResponseAfterCleanup(t *testing.T) {
	limits := testLimits
	limits.IdleTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Where the port can be reserved, it stays reserved for the closing
	// rebind, which therefore fails only if the server kept its listener.
	served, reserved := testport.TryReserve(t)
	bound, cleaned := make(chan string, 1), make(chan struct{})
	done := make(chan Exit[Unit], 1)
	large := bytes.Repeat([]byte("s"), 32<<20)
	go func() {
		done <- RunContext(ctx, ServeHTTPRequests(served, limits, func(HTTPRequest) Effect[HTTPResponse] {
			return func(fc *FiberContext) Exit[HTTPResponse] {
				resource := Invoke(fc, AcquireRelease("request", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { close(cleaned); return nil }))
				if resource.IsFailure() {
					return Propagate[HTTPResponse](resource)
				}
				return Succeed(HTTPResponse{Status: 200, ContentType: "application/octet-stream", Body: large})
			}
		}, func(address string) { bound <- address }))
	}()
	address := wait(t, bound)
	if reserved && address != served {
		t.Fatalf("listening on %s, want %s", address, served)
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wait(t, cleaned)
	cancel()
	select {
	case exit := <-done:
		if !exit.Interrupted {
			t.Fatalf("shutdown did not preserve cancellation: %+v", exit)
		}
	case <-testwait.After(t):
		t.Fatal("shutdown waited on a client that stopped reading")
	}
	requireReleased(t, address, reserved)
}

// The write grace is a transport-drain phase: it starts once every request
// scope has closed, not when the writer's own scope did. A stalled response
// whose client resumes inside the grace after another request's gated
// cleanup still arrives whole.
func TestHTTPTransportShutdownGraceStartsAfterTheLastScopeCloses(t *testing.T) {
	limits := testLimits
	limits.MaxActive = 2
	limits.IdleTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bound, cleaned, entered, gate := make(chan string, 1), make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan Exit[Unit], 1)
	large := bytes.Repeat([]byte("s"), 32<<20)
	go func() {
		done <- RunContext(ctx, ServeHTTPRequests("127.0.0.1:0", limits, func(request HTTPRequest) Effect[HTTPResponse] {
			return func(fc *FiberContext) Exit[HTTPResponse] {
				if request.Path == "/big" {
					close(cleaned)
					return Succeed(HTTPResponse{Status: 200, ContentType: "application/octet-stream", Body: large})
				}
				resource := Invoke(fc, AcquireRelease("gated", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { <-gate; return nil }))
				if resource.IsFailure() {
					return Propagate[HTTPResponse](resource)
				}
				close(entered)
				<-fc.Context().Done()
				return Interrupt[HTTPResponse](fc.Context().Err())
			}
		}, func(address string) { bound <- address }))
	}()
	address := wait(t, bound)
	stalled, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	if _, err := io.WriteString(stalled, "GET /big HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wait(t, cleaned)
	gated, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer gated.Close()
	if _, err := io.WriteString(gated, "GET /gated HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	wait(t, entered)
	cancel()
	// Twice the grace elapses while the other request's cleanup is gated.
	time.Sleep(2 * limits.IdleTimeout)
	close(gate)
	_ = stalled.SetReadDeadline(testEnd(t))
	response, err := http.ReadResponse(bufio.NewReader(stalled), nil)
	if err != nil {
		t.Fatalf("stalled response was aborted before the last scope closed: %v", err)
	}
	received, err := io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 || received != int64(len(large)) {
		t.Fatalf("stalled response was aborted before the last scope closed: %d, %d of %d bytes, %v", response.StatusCode, received, len(large), err)
	}
	response, body := readResponse(t, bufio.NewReader(gated))
	requireStatusOnly(t, response, body, http.StatusServiceUnavailable)
	select {
	case exit := <-done:
		if !exit.Interrupted {
			t.Fatalf("shutdown did not preserve cancellation: %+v", exit)
		}
	case <-testwait.After(t):
		t.Fatal("shutdown did not complete after the drain")
	}
}

func TestHTTPTransportRejectsImplicitLimits(t *testing.T) {
	for _, limits := range []HTTPLimits{{}, {MaxBodyBytes: -1, ReadHeaderTimeout: 1, ReadBodyTimeout: 1, IdleTimeout: 1, MaxActive: 1}, {MaxBodyBytes: 1, ReadHeaderTimeout: 1, ReadBodyTimeout: 1, IdleTimeout: 1}} {
		out := Run(ServeHTTPRequests("127.0.0.1:0", limits, nil, nil))
		if out.Defect == nil || !strings.Contains(out.Defect.Error(), "invalid HTTP limits") {
			t.Fatalf("limits %+v: %+v", limits, out)
		}
	}
}

func TestRequestPathKeepsTargetBytes(t *testing.T) {
	for target, want := range map[string]string{"/a?b": "/a", "/a%2F..//b": "/a%2F..//b", "http://host/x/y?z": "/x/y", "http://host": "/", "http://ergo?next=/health": "/", "http://ergo/a?next=/b": "/a", "http://ergo?": "/", "*": "*"} {
		if got := requestPath(target); got != want {
			t.Fatalf("%q: %q != %q", target, got, want)
		}
	}
}

func TestHTTPLimitsFromMillisRejectsUnrepresentableBounds(t *testing.T) {
	if limits, err := HTTPLimitsFromMillis(0, 1, 2, 3, 4); err != nil || limits.ReadBodyTimeout != 2*time.Millisecond || limits.MaxActive != 4 {
		t.Fatalf("valid limits: %+v %v", limits, err)
	}
	for _, values := range [][5]int64{{-1, 1, 1, 1, 1}, {1 << 53, 1, 1, 1, 1}, {1, 0, 1, 1, 1}, {1, 1, 1 << 31, 1, 1}, {1, 1, 1, 1, 0}, {1, 1, 1, 1, 1 << 31}} {
		if _, err := HTTPLimitsFromMillis(values[0], values[1], values[2], values[3], values[4]); err == nil {
			t.Fatalf("accepted %v", values)
		}
	}
}
