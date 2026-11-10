package v1

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// httpHandler is the trusted transport callback; the std wrapper makes
// its seven scalar arguments into a public Request with a closed Body.
type httpHandler = func(*rt.Task, *Exchange, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse

func startHTTP(t *testing.T, handle httpHandler) (*Listener, *Task[struct{}]) {
	t.Helper()
	return startHTTPLimits(t, serveLimits(16, 4096), handle)
}

// serveLimits are byte budgets with timeouts no test reaches on the host
// Scheduler.
func serveLimits(body, header int) HTTPServerLimits {
	return HTTPServerLimits{body, header, time.Hour, time.Hour, time.Hour}
}

// liveSleeper is the host Scheduler, as a program binds NewLiveScheduler.
type liveSleeper struct{}

func (liveSleeper) Sleep(t *rt.Task, d time.Duration) { Sleep(t, d) }

func startHTTPLimits(t *testing.T, limits HTTPServerLimits, handle httpHandler) (*Listener, *Task[struct{}]) {
	t.Helper()
	return startHTTPOn(t, liveSleeper{}, limits, handle)
}

func startHTTPOn(t *testing.T, sleeper Sleeper, limits HTTPServerLimits, handle httpHandler) (*Listener, *Task[struct{}]) {
	t.Helper()
	l, err := HTTPListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	root := rt.Root(t.Name())
	s := NewScope(root)
	h := Go(&s, "http test server", func(ct *rt.Task) (struct{}, error) {
		return struct{}{}, HTTPServe("http test request", ct, sleeper, l, limits, handle)
	})
	t.Cleanup(func() { h.Stop(); l.Close() })
	return l, h
}

func httpClient(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func httpRequest(t *testing.T, client *http.Client, method, url, body string) *http.Response {
	t.Helper()
	ctx, cancel := testwait.Context(t, context.Background())
	t.Cleanup(cancel)
	r, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Add("x-echo", "value")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func httpText(t *testing.T, r *http.Response) string {
	t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestHTTPServerMatchesModel sends seeded job-map request traces through a
// loopback socket. The independent map interpreter predicts each status,
// body and empty-body content type. It kills the buffering-limit mutant;
// pure route laws cannot see a transport truncating an input or a HEAD body.
func TestHTTPServerMatchesModel(t *testing.T) {
	// Invalid required budgets do not consume the transport. Draw every
	// non-positive limit from a seed, covering each budget independently.
	// Each budget is zero once (zero is never "unbounded"), then negative.
	budgetRandom := rand.New(rand.NewPCG(41, 87))
	for trial := 0; trial < 15; trial++ {
		l, err := HTTPListen("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		bad := -budgetRandom.IntN(16)
		if trial < 5 {
			bad = 0
		}
		limits := serveLimits(16, 4096)
		switch trial % 5 {
		case 0:
			limits.MaxBodyBytes = bad
		case 1:
			limits.MaxHeaderBytes = bad
		case 2:
			limits.IdleTimeout = time.Duration(bad)
		case 3:
			limits.ReadHeaderTimeout = time.Duration(bad)
		case 4:
			limits.ReadBodyTimeout = time.Duration(bad)
		}
		err = HTTPServe("invalid", rt.Root("invalid"), liveSleeper{}, l, limits, func(_ *rt.Task, _ *Exchange, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
			t.Error("invalid limits called handler")
			return HTTPResponse{Status: 200}
		})
		if err != HTTPInvalidLimits(limits) {
			t.Fatalf("limits %+v: %v", limits, err)
		}
		conn, err := net.Dial("tcp", l.Addr())
		l.Close()
		if err != nil {
			t.Fatalf("invalid limits closed listener: %v", err)
		}
		conn.Close()
	}
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			var mu sync.Mutex
			store := map[string]string{}
			l, server := startHTTP(t, func(_ *rt.Task, _ *Exchange, method, path string, query, header []HTTPField, body string, tooLarge bool, readError string) (response HTTPResponse) {
				defer func() {
					var queryText, echo string
					for _, field := range query {
						queryText += field.Name + "=" + field.Value + ";"
					}
					for _, field := range header {
						if strings.EqualFold(field.Name, "X-Echo") {
							echo = field.Value
						}
					}
					response.Header = []HTTPField{{"X-Query", queryText}, {"X-Request-Echo", echo}}
				}()
				mu.Lock()
				defer mu.Unlock()
				if tooLarge {
					return HTTPResponse{Status: 413, Body: "budget"}
				}
				if readError != "" {
					return HTTPResponse{Status: 400, Body: "read"}
				}
				switch method {
				case "POST":
					store[path] = body
					return HTTPResponse{Status: 201, Body: body}
				case "DELETE":
					delete(store, path)
					return HTTPResponse{Status: 204}
				case "GET", "HEAD":
					if v, ok := store[path]; ok {
						return HTTPResponse{Status: 200, Body: v}
					}
					return HTTPResponse{Status: 404, Body: "missing"}
				default:
					return HTTPResponse{Status: 405}
				}
			})
			client := httpClient(t)
			// Exceed Go's header budget, including its parser read-ahead
			// allowance. This protocol refusal happens before the handler.
			ctx, cancel := testwait.Context(t, context.Background())
			defer cancel()
			largeHeader, err := http.NewRequestWithContext(ctx, "GET", "http://"+l.Addr()+"/headers", nil)
			if err != nil {
				t.Fatal(err)
			}
			largeHeader.Header.Set("X-Padding", strings.Repeat("h", 12288+int(seed)))
			refused, err := client.Do(largeHeader)
			if err != nil {
				t.Fatal(err)
			}
			_ = httpText(t, refused)
			if refused.StatusCode != 431 {
				t.Fatalf("seed %d: header budget status %d, want 431", seed, refused.StatusCode)
			}
			// A half-closed, truncated request still gets the program's
			// read-failed response, rather than a successful partial body.
			conn, err := net.Dial("tcp", l.Addr())
			if err != nil {
				t.Fatal(err)
			}
			_, err = fmt.Fprintf(conn, "POST /broken HTTP/1.1\r\nHost: local\r\nContent-Length: %d\r\n\r\nx", 2+int(seed))
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			broken, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			brokenText := httpText(t, broken)
			conn.Close()
			if broken.StatusCode != 400 || brokenText != "read" {
				t.Fatalf("truncated body: %d %q", broken.StatusCode, brokenText)
			}
			model := map[string]string{}
			rng := rand.New(rand.NewPCG(seed, seed+91))
			var trace []string
			for step := 0; step < 40; step++ {
				method := []string{"POST", "GET", "HEAD", "DELETE", "PATCH"}[rng.IntN(5)]
				path := "/jobs/" + strconv.Itoa(rng.IntN(5))
				body := strings.Repeat(string(byte('a'+rng.IntN(26))), rng.IntN(24))
				trace = append(trace, fmt.Sprintf("%s %s %q", method, path, body))
				status, want := 405, ""
				if len(body) > 16 {
					status, want = 413, "budget"
				} else {
					switch method {
					case "POST":
						model[path] = body
						status, want = 201, body
					case "DELETE":
						delete(model, path)
						status = 204
					case "GET", "HEAD":
						if v, ok := model[path]; ok {
							status, want = 200, v
						} else {
							status, want = 404, "missing"
						}
					}
				}
				responseBody := want
				if method == "HEAD" {
					want = ""
				}
				query := "tag=" + strconv.FormatUint(seed, 10) + "&tag=" + strconv.Itoa(step)
				resp := httpRequest(t, client, method, "http://"+l.Addr()+path+"?"+query, body)
				got := httpText(t, resp)
				wantQuery := "tag=" + strconv.FormatUint(seed, 10) + ";tag=" + strconv.Itoa(step) + ";"
				if resp.StatusCode != status || got != want || responseBody == "" && resp.Header.Get("Content-Type") != "" || resp.Header.Get("X-Query") != wantQuery || resp.Header.Get("X-Request-Echo") != "value" {
					t.Fatalf("seed %d step %d\nrequests: %q\nserver: %d %q %v\nmodel: %d %q\nreplay: go test ./runtime/v1 -run TestHTTPServerMatchesModel/%d", seed, step, trace, resp.StatusCode, got, resp.Header, status, want, seed)
				}
			}
			server.Stop()
			conn, err = net.Dial("tcp", l.Addr())
			if err == nil {
				conn.Close()
				t.Fatal("Serve returned with an open listener")
			}
		})
	}
}

// stepSleeper is a test Scheduler whose time moves only by Advance. Each
// Sleep reports its duration on armed after its deadline is set, so a
// test that receives the report may Advance with no race. A stop of the
// sleeping task ends the sleep with the interruption.
type stepSleeper struct {
	mu      sync.Mutex
	now     time.Duration
	waiters map[*stepWait]bool
	armed   chan time.Duration
}

type stepWait struct {
	at   time.Duration
	done chan struct{}
}

func newStepSleeper() *stepSleeper {
	return &stepSleeper{waiters: map[*stepWait]bool{}, armed: make(chan time.Duration, 64)}
}

func (s *stepSleeper) Sleep(t *rt.Task, d time.Duration) {
	w := &stepWait{done: make(chan struct{})}
	s.mu.Lock()
	w.at = s.now + d
	s.waiters[w] = true
	s.mu.Unlock()
	s.armed <- d
	select {
	case <-w.done:
	case <-t.Done():
		s.mu.Lock()
		delete(s.waiters, w)
		s.mu.Unlock()
		raise(rt.Interrupted)
	}
}

// Advance moves the time by d and ends every sleep it reaches.
func (s *stepSleeper) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now += d
	for w := range s.waiters {
		if w.at <= s.now {
			close(w.done)
			delete(s.waiters, w)
		}
	}
}

// TestHTTPPhasesExpireOnTheScheduler drives one keep-alive connection
// through its header, body and idle phases on a test Scheduler, for
// seeded timeouts, with no host timing. Each phase arms its own budget; a
// phase that ends 1ns inside its budget lets the connection go on; the
// phase the seed picks expires: an idle or a header phase closes the
// connection, and a body phase reaches the handler as a failed read.
// Mutants it kills, which no other test sees: no idle timer (E4: an idle
// keep-alive connection was held open forever), a phase armed with another
// phase's budget, a header phase that does not start at the next
// request's first byte, and an expiry that closes the connection in the
// body phase instead of failing the read.
func TestHTTPPhasesExpireOnTheScheduler(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			budget := func() time.Duration { return time.Duration(1+rng.IntN(1000)) * time.Millisecond }
			limits := HTTPServerLimits{16, 4096, budget(), budget(), budget()}
			sl := newStepSleeper()
			l, _ := startHTTPOn(t, sl, limits, func(_ *rt.Task, _ *Exchange, _, _ string, _, _ []HTTPField, body string, _ bool, readError string) HTTPResponse {
				if readError != "" {
					return HTTPResponse{Status: 408, Body: readError}
				}
				return HTTPResponse{Status: 200, Body: body}
			})
			conn, err := net.Dial("tcp", l.Addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			// A watchdog only: a hung read fails the test with its step.
			stop := testwait.AfterFunc(t, func() { conn.Close() })
			defer stop()
			r := bufio.NewReader(conn)
			replay := fmt.Sprintf("seed %d, limits %+v; replay: go test ./runtime/v1 -run TestHTTPPhasesExpireOnTheScheduler/%d", seed, limits, seed)
			armed := func(want time.Duration, step string) {
				t.Helper()
				select {
				case d := <-sl.armed:
					if d != want {
						t.Fatalf("%s armed %v, want %v; %s", step, d, want, replay)
					}
				case <-testwait.After(t):
					t.Fatalf("%s armed no timer; %s", step, replay)
				}
			}
			send := func(text string) {
				t.Helper()
				if _, err := io.WriteString(conn, text); err != nil {
					t.Fatalf("send %q: %v; %s", text, err, replay)
				}
			}
			answer := func(step string) (int, string) {
				t.Helper()
				resp, err := http.ReadResponse(r, nil)
				if err != nil {
					t.Fatalf("%s: %v; %s", step, err, replay)
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("%s: %v; %s", step, err, replay)
				}
				return resp.StatusCode, string(b)
			}
			armed(limits.ReadHeaderTimeout, "the first header")
			send("POST /a HTTP/1.1\r\nHost: local\r\n")
			sl.Advance(limits.ReadHeaderTimeout - 1)
			send("Content-Length: 4\r\n\r\nab")
			armed(limits.ReadBodyTimeout, "the first body")
			sl.Advance(limits.ReadBodyTimeout - 1)
			send("cd")
			if status, body := answer("the first answer"); status != 200 || body != "abcd" {
				t.Fatalf("the first answer: %d %q; %s", status, body, replay)
			}
			armed(limits.IdleTimeout, "idle")
			sl.Advance(limits.IdleTimeout - 1)
			switch seed % 3 {
			case 0:
				sl.Advance(1)
			case 1:
				send("GET /b HTTP/1.1\r\n")
				armed(limits.ReadHeaderTimeout, "the second header")
				sl.Advance(limits.ReadHeaderTimeout)
			case 2:
				send("POST /c HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\na")
				armed(limits.ReadBodyTimeout, "the second body")
				sl.Advance(limits.ReadBodyTimeout)
				if status, body := answer("the expired body"); status != 408 || !strings.Contains(body, "ReadBodyTimeout") {
					t.Fatalf("the expired body: %d %q; %s", status, body, replay)
				}
			}
			// The server closes the connection: the next read ends at EOF.
			if b, err := r.ReadByte(); err != io.EOF {
				t.Fatalf("after the expiry: read %q, %v, want EOF; %s", b, err, replay)
			}
		})
	}
}

// TestHTTPConcurrentRequestsAndStop catches lost calls and premature
// shutdown. Started and released events, rather than time, order the stop.
// The server must stop children and wait for their protected releases.
func TestHTTPConcurrentRequestsAndStop(t *testing.T) {
	const n = 24
	var count, releaseCount atomic.Int64
	started, released := make(chan struct{}, n), make(chan struct{}, n)
	cleanup := make(chan struct{})
	l, server := startHTTP(t, func(ct *rt.Task, _ *Exchange, method, path string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		if path == "/count" {
			return HTTPResponse{Status: 200, Body: strconv.FormatInt(count.Add(1), 10)}
		}
		s := NewScope(ct)
		defer s.End(nil)
		OnExit(&s, func() { <-cleanup; releaseCount.Add(1); released <- struct{}{} })
		started <- struct{}{}
		<-ct.Done()
		ct.Check()
		return HTTPResponse{Status: 200}
	})
	client := httpClient(t)
	var calls sync.WaitGroup
	answers := make(chan string, n)
	for range n {
		calls.Add(1)
		go func() {
			defer calls.Done()
			answers <- httpText(t, httpRequest(t, client, "GET", "http://"+l.Addr()+"/count", ""))
		}()
	}
	calls.Wait()
	seen := map[string]bool{}
	for range n {
		seen[<-answers] = true
	}
	if count.Load() != n || len(seen) != n {
		t.Fatalf("lost or repeated calls: count=%d answers=%v", count.Load(), seen)
	}
	for range n {
		calls.Add(1)
		go func() {
			defer calls.Done()
			ctx, cancel := testwait.Context(t, context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+l.Addr()+"/wait", nil)
			resp, _ := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	for range n {
		<-started
	}
	// Interrupt from this task, and release cleanup only once every child
	// observed its stop. Stop itself stays on the owning goroutine.
	server.h.Task().Interrupt()
	close(cleanup)
	server.Stop()
	if releaseCount.Load() != n {
		t.Fatalf("Serve returned before cleanup: %d of %d", releaseCount.Load(), n)
	}
	for range n {
		<-released
	}
	calls.Wait()
}

// TestHTTPDisconnectAndDefects kills ignoring the client's abort, and
// dropping a request's cleanup defect. A disconnect must end that child
// without ending the server; a defect then ends Serve with its cause.
// A connection that never sends its header holds a phase timer on a
// Scheduler that never moves, so Serve ends only if its defect path stops
// that timer too (mutant http-stop-leaves-timers).
func TestHTTPDisconnectAndDefects(t *testing.T) {
	started, ended := make(chan struct{}), make(chan struct{})
	sl := newStepSleeper()
	l, server := startHTTPOn(t, sl, serveLimits(16, 4096), func(ct *rt.Task, _ *Exchange, _, path string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		s := NewScope(ct)
		defer s.End(nil)
		if path == "/defect" {
			OnExit(&s, func() { panic("cleanup-http") })
			panic("body-http")
		}
		OnExit(&s, func() { close(ended) })
		close(started)
		<-ct.Done()
		ct.Check()
		return HTTPResponse{Status: 200}
	})
	silent, err := net.Dial("tcp", l.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	<-sl.armed // the silent connection's header phase
	conn, err := net.Dial("tcp", l.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET /wait HTTP/1.1\r\nHost: local\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	<-started
	conn.Close()
	<-ended
	client := httpClient(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+l.Addr()+"/defect", nil)
	resp, _ := client.Do(req)
	if resp != nil {
		resp.Body.Close()
		t.Fatal("defect became a response")
	}
	_, cause := server.h.Wait()
	server.consume()
	text := fmt.Sprint(cause)
	if !strings.Contains(text, "body-http") || !strings.Contains(text, "cleanup-http") {
		t.Fatalf("lost cause: %v", cause)
	}
}
