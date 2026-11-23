package http

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
	v1 "ergo.local/prototype/runtime/v1"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// httpHandler is the trusted transport callback; the std wrapper makes
// its seven scalar arguments into a public Request with a closed Body.
type httpHandler = func(*rt.Task, *Exchange, string, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse

func startHTTP(t *testing.T, handle httpHandler) (*ergonet.Listener, *serverTask) {
	t.Helper()
	return startHTTPLimits(t, serveLimits(16, 4096), handle)
}

// serveLimits are byte budgets with timeouts no test reaches on the host
// Scheduler.
func serveLimits(body, header int) HTTPLimits {
	return HTTPLimits{body, header, time.Hour, time.Hour, time.Hour}
}

// liveSleeper is the host Scheduler, as a program binds NewLiveScheduler.
type liveSleeper struct{}

func (liveSleeper) Sleep(t *rt.Task, d time.Duration) { v1.Sleep(t, d) }

// Alarms is the host's timers, as LiveScheduler's Alarms gives them.
func (liveSleeper) Alarms(*rt.Task) *v1.AlarmClock { return v1.HostAlarms() }

func startHTTPLimits(t *testing.T, limits HTTPLimits, handle httpHandler) (*ergonet.Listener, *serverTask) {
	t.Helper()
	return startHTTPOn(t, liveSleeper{}, limits, handle)
}

func startHTTPOn(t *testing.T, sleeper v1.Sleeper, limits HTTPLimits, handle httpHandler) (*ergonet.Listener, *serverTask) {
	t.Helper()
	return startHTTPRoot(t, rt.Root(t.Name()), sleeper, limits, handle)
}

func startHTTPRoot(t *testing.T, root *rt.Task, sleeper v1.Sleeper, limits HTTPLimits, handle httpHandler) (*ergonet.Listener, *serverTask) {
	t.Helper()
	l, err := HTTPListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l, serveOn(t, root, sleeper, l, limits, handle)
}

// serveOn serves l (a host listener or a pipe's) under root until the test
// ends.
func serveOn(t *testing.T, root *rt.Task, sleeper v1.Sleeper, l *ergonet.Listener, limits HTTPLimits, handle httpHandler) *serverTask {
	t.Helper()
	holder := rt.NewScope(root)
	h := &serverTask{h: rt.Go(&holder, "http test server", func(ct *rt.Task) (struct{}, error) {
		return v1.Guarded(ct, func(ct *rt.Task) (struct{}, error) {
			if !*engineFlag {
				return struct{}{}, httpServe("http test request", ct, sleeper, l, limits, handle, false)
			}
			return struct{}{}, HTTPServe("http test request", ct, sleeper, l, limits, handle)
		})
	})}
	t.Cleanup(func() { h.Stop(); l.Close() })
	return h
}

// serverTask is the test server's task, started as v1.Go starts one, with
// its rt handle at hand so a test can interrupt it or join its raw exit.
type serverTask struct {
	h    *rt.Handle[struct{}]
	used bool // Stop ran, or a test joined h
}

// Stop is v1.Task's Stop: interrupt, wait, raise the defects.
func (s *serverTask) Stop() {
	if s.used {
		return
	}
	s.used = true
	if err := s.h.Stop(); err != nil {
		v1.Raise(err)
	}
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
		err = HTTPServe("invalid", rt.Root("invalid"), liveSleeper{}, l, limits, func(_ *rt.Task, _ *Exchange, _, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
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
			l, server := startHTTP(t, func(_ *rt.Task, _ *Exchange, method, path, _ string, query, header []HTTPField, body string, tooLarge bool, readError string) (response HTTPResponse) {
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
					return HTTPResponse{Status: 400, Body: readError}
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
			if broken.StatusCode != 400 || brokenText != "unexpected EOF" {
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
					t.Fatalf("seed %d step %d\nrequests: %q\nserver: %d %q %v\nmodel: %d %q\nreplay: go test ./runtime/v1/http -run TestHTTPServerMatchesModel/%d", seed, step, trace, resp.StatusCode, got, resp.Header, status, want, seed)
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
	// alarms and peak count stepAlarms' alarms (armedAlarms).
	alarms, peak int
}

type stepWait struct {
	at   time.Duration
	done chan struct{}
	f    func() // an alarm's function (stepAlarms), nil for a Sleep
}

// stepAlarms is stepSleeper with Alarms, as every program's Scheduler
// has: Serve takes its alarm path on the same time. Each deadline read
// reports its span on armed, as a Sleep does; Advance starts the function of
// each alarm it reaches. alarms counts the armed ones, and peak the most
// armed at once.
type stepAlarms struct{ *stepSleeper }

func (s stepAlarms) Alarms(*rt.Task) *v1.AlarmClock {
	now := func() time.Duration {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.now
	}
	arm := func(d time.Duration, f func()) v1.Alarm {
		w := &stepWait{f: f}
		s.mu.Lock()
		w.at = s.now + d
		s.waiters[w] = true
		s.alarms++
		s.peak = max(s.peak, s.alarms)
		s.mu.Unlock()
		return stepAlarm{s.stepSleeper, w}
	}
	// A bounded phase arms an alarm only when its deadline comes first:
	// its Began, after any arm, is the report.
	return v1.NewAlarmClock(now, arm).Reporting(func(d time.Duration) { s.armed <- d })
}

type stepAlarm struct {
	s *stepSleeper
	w *stepWait
}

func (a stepAlarm) Stop() bool {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if !a.s.waiters[a.w] {
		return false
	}
	delete(a.s.waiters, a.w)
	a.s.alarms--
	return true
}

// armedAlarms is the number of alarms armed now, and the most armed at
// once so far.
func (s *stepSleeper) armedAlarms() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alarms, s.peak
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
		v1.Raise(rt.Interrupted)
	}
}

// Advance moves the time by d and ends every sleep it reaches. It runs
// the function of each alarm it reaches before it returns, outside its
// lock, so a step that follows sees what the alarms did.
func (s *stepSleeper) Advance(d time.Duration) {
	var fired []func()
	defer func() {
		for _, f := range fired {
			f()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now += d
	for w := range s.waiters {
		if w.at <= s.now {
			delete(s.waiters, w)
			if w.f != nil {
				s.alarms--
				fired = append(fired, w.f)
				continue
			}
			close(w.done)
		}
	}
}

// httpTimerCount counts started timer tasks until the owner observes their
// completed exits. An interruption alone does not vacate the timer slot.
// The phase property has one connection, so its peak is a per-connection
// bound, not a bound on how many clients a server can admit.
type httpTimerCount struct {
	mu           sync.Mutex
	live         map[uint64]bool
	starts, peak int
}

func (c *httpTimerCount) Event(e rt.Event) {
	if e.Def != "http test request (timeout)" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e.Kind {
	case rt.TraceStart:
		if c.live == nil {
			c.live = map[uint64]bool{}
		}
		c.live[e.Task] = true
		c.starts++
		c.peak = max(c.peak, len(c.live))
	case rt.TraceObserve:
		delete(c.live, e.Task)
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
// body phase instead of failing the read. Its task counter also kills
// http-timers-overlap: starting the next phase before joining the old
// timer, which the deadline results alone do not detect.
//
// It runs each seed on both of Serve's paths, on one test time: "tasks",
// the reference (a Scheduler with no Alarms: a timer goroutine whose
// sleeps are tasks), and "alarms", the path of every program (one
// deadline per connection, and an alarm armed only when the deadline
// comes before the armed one). On the alarm path its alarm count kills
// http-alarm-overlap (an earlier alarm armed with the old one left
// armed), and its task count http-alarm-path-never (a program's
// Scheduler served on the reference path). Its early seeds (a body bound
// shorter than the idle bound before it) kill lazy-arm-never-earlier,
// and every seed's later phases, whose deadlines come after the armed
// alarm, kill lazy-wake-no-rearm (an early alarm that does not arm
// again for the deadline).
func TestHTTPPhasesExpireOnTheScheduler(t *testing.T) {
	for _, path := range []string{"tasks", "alarms"} {
		for seed := uint64(1); seed <= 12; seed++ {
			t.Run(path+"/"+strconv.FormatUint(seed, 10), func(t *testing.T) {
				phasesExpire(t, path, seed)
			})
		}
	}
}

func phasesExpire(t *testing.T, path string, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 7))
	budget := func() time.Duration { return time.Duration(1+rng.IntN(1000)) * time.Millisecond }
	limits := HTTPLimits{16, 4096, budget(), budget(), budget()}
	// early: the first request is sent whole at once, its body bound
	// shorter than the header bound, so the body's deadline comes before
	// the header's alarm, and the body expires (lazy-arm-never-earlier).
	early := seed%3 == 2 && (seed/3)%2 == 1
	if early {
		limits.ReadHeaderTimeout = limits.ReadBodyTimeout + budget()
	}
	sl := newStepSleeper()
	var sched v1.Sleeper = sl
	if path == "alarms" {
		sched = stepAlarms{sl}
	}
	count := &httpTimerCount{}
	l, server := startHTTPRoot(t, rt.TracedRoot(t.Name(), count), sched, limits, func(_ *rt.Task, _ *Exchange, _, _, _ string, _, _ []HTTPField, body string, _ bool, readError string) HTTPResponse {
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
	replay := fmt.Sprintf("%s seed %d, limits %+v; replay: go test ./runtime/v1/http -run TestHTTPPhasesExpireOnTheScheduler/%s/%d", path, seed, limits, path, seed)
	// armed takes the next sleep the server armed, which must be want's.
	// A phase that one send enters and leaves at once (the header of
	// a request sent whole) may be armed before its timer sees the
	// change, or not at all: passing names its budget, skipped once.
	armed := func(want time.Duration, step string, passing ...time.Duration) {
		t.Helper()
		for {
			select {
			case d := <-sl.armed:
				if d != want && len(passing) > 0 && d == passing[0] {
					passing = passing[1:]
					continue
				}
				if d != want {
					t.Fatalf("%s armed %v, want %v; %s", step, d, want, replay)
				}
			case <-testwait.After(t):
				t.Fatalf("%s armed no timer; %s", step, replay)
			}
			return
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
	if early {
		send("POST /a HTTP/1.1\r\nHost: local\r\nContent-Length: 4\r\n\r\nab")
		armed(limits.ReadBodyTimeout, "the first body")
		sl.Advance(limits.ReadBodyTimeout)
		if status, body := answer("the expired first body"); status != 408 || !strings.Contains(body, "ReadBodyTimeout") {
			t.Fatalf("the expired first body: %d %q; %s", status, body, replay)
		}
	} else {
		phasesThroughIdle(t, seed, limits, sl, armed, send, answer, replay)
	}
	// The server closes the connection: the next read ends at EOF.
	if b, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("after the expiry: read %q, %v, want EOF; %s", b, err, replay)
	}
	server.Stop()
	count.mu.Lock()
	defer count.mu.Unlock()
	if count.peak > 1 || len(count.live) != 0 {
		t.Fatalf("timer tasks: %d starts, peak %d live, %d left after stop; want peak <= 1 and none left; %s", count.starts, count.peak, len(count.live), replay)
	}
	if _, peak := sl.armedAlarms(); peak > 1 {
		t.Fatalf("alarms: peak %d armed at once, want at most 1; %s", peak, replay)
	}
	if path == "alarms" && count.starts != 0 {
		t.Fatalf("the alarm path started %d timer tasks, want none; %s", count.starts, replay)
	}
}

// phasesThroughIdle is phasesExpire's flow for a seed that is not early:
// a request in parts, each phase ended 1ns inside its bound, then the
// idle phase, and the expiry the seed picks.
func phasesThroughIdle(t *testing.T, seed uint64, limits HTTPLimits, sl *stepSleeper, armed func(time.Duration, string, ...time.Duration), send func(string), answer func(string) (int, string), replay string) {
	t.Helper()
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
		armed(limits.ReadBodyTimeout, "the second body", limits.ReadHeaderTimeout)
		// In two steps: the first reaches the idle phase's alarm, which
		// comes before the body's deadline and must arm again for it
		// (lazy-wake-no-rearm); the second reaches the deadline.
		sl.Advance(1)
		sl.Advance(limits.ReadBodyTimeout - 1)
		if status, body := answer("the expired body"); status != 408 || !strings.Contains(body, "ReadBodyTimeout") {
			t.Fatalf("the expired body: %d %q; %s", status, body, replay)
		}
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
	l, server := startHTTP(t, func(ct *rt.Task, _ *Exchange, method, path, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		if path == "/count" {
			return HTTPResponse{Status: 200, Body: strconv.FormatInt(count.Add(1), 10)}
		}
		s := v1.NewScope(ct)
		defer s.End(nil)
		v1.OnExit(&s, func() { <-cleanup; releaseCount.Add(1); released <- struct{}{} })
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
	started, ended, refused := make(chan struct{}), make(chan struct{}), make(chan struct{})
	sl := newStepSleeper()
	l, server := startHTTPOn(t, sl, serveLimits(16, 4096), func(ct *rt.Task, _ *Exchange, _, path, _ string, _, _ []HTTPField, _ string, _ bool, readError string) HTTPResponse {
		s := v1.NewScope(ct)
		defer s.End(nil)
		if path == "/refused" && readError != "" {
			// A half-closed peer's request is no disconnect: it runs
			// until the server stops (GX4: gx4-shutdown-leaves-requests).
			close(refused)
			<-ct.Done()
			ct.Check()
			return HTTPResponse{Status: 400}
		}
		if path == "/defect" {
			v1.OnExit(&s, func() { panic("cleanup-http") })
			panic("body-http")
		}
		v1.OnExit(&s, func() { close(ended) })
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
	half, err := net.Dial("tcp", l.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer half.Close()
	if _, err := io.WriteString(half, "POST /refused HTTP/1.1\r\nHost: local\r\nContent-Length: 3\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	if err := half.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	<-refused
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
	server.used = true // The raw handle join above consumed the owned Task.
	text := fmt.Sprint(cause)
	if !strings.Contains(text, "body-http") || !strings.Contains(text, "cleanup-http") {
		t.Fatalf("lost cause: %v", cause)
	}
}

// A handler that returns an invalid buffered status causes WriteHeader to
// panic. Unlike a panic in the handler body, this used to be recovered by
// the transport and lost to the owner of the request.
func TestHTTPBufferedResponseDefect(t *testing.T) {
	l, server := startHTTP(t, func(*rt.Task, *Exchange, string, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse {
		return HTTPResponse{Status: 42}
	})
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+l.Addr()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpClient(t).Do(req)
	if resp != nil {
		resp.Body.Close()
		t.Fatalf("invalid buffered status became a response: %s", resp.Status)
	}
	if err == nil || ctx.Err() != nil {
		t.Fatalf("want the transport to close the connection, got %v (context: %v)", err, ctx.Err())
	}

	// Stop also joins a server that lost the defect, so that regression
	// fails here without waiting for a test timeout.
	server.used = true
	exit := server.h.Stop()
	var cause rt.Cause
	cause.Add(exit)
	defects := 0
	for _, reason := range cause.Reasons {
		if reason.Kind != rt.KindDefect {
			continue
		}
		defects++
		p, ok := reason.Value.(*v1.Panic)
		if !ok || p.Value != "invalid WriteHeader code 42" || !strings.Contains(string(p.Stack), "httpWriteAnswer") {
			t.Fatalf("want the recorded WriteHeader panic with its original stack, got %#v", reason.Value)
		}
		if len(reason.Chain) == 0 || reason.Chain[0].Span != "http test request" {
			t.Fatalf("want a defect of the request task, got %v", reason.Chain)
		}
	}
	if defects != 1 {
		t.Fatalf("want one buffered response defect from Serve, got %d: %v", defects, exit)
	}
}
