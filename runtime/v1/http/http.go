package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// The listener is the net leaf's Listener, the transport shared by server
// protocols (std http's Listener and std net's name one Go type). HTTP
// adds no policy to it; Limits belongs to the serving call, not to the
// transport.

type HTTPListenFailed struct{ Addr, Reason string }

func (e HTTPListenFailed) Error() string { return "http.ListenFailed: " + e.Addr + ": " + e.Reason }

type HTTPInvalidLimits struct {
	MaxBodyBytes, MaxHeaderBytes                    int
	IdleTimeout, ReadHeaderTimeout, ReadBodyTimeout time.Duration
}

func (e HTTPInvalidLimits) Error() string {
	return fmt.Sprintf("http.InvalidLimits: body=%d header=%d idle=%v read-header=%v read-body=%v", e.MaxBodyBytes, e.MaxHeaderBytes, e.IdleTimeout, e.ReadHeaderTimeout, e.ReadBodyTimeout)
}

// HTTPLimits is http.Limits. The byte budgets bound what one
// request may send; the three timeouts bound how long a connection may
// stay in each read phase, measured on the program's Scheduler. Serve
// reads every field; the client uses only the byte budgets.
type HTTPLimits struct {
	MaxBodyBytes, MaxHeaderBytes                    int
	IdleTimeout, ReadHeaderTimeout, ReadBodyTimeout time.Duration
}

// valid reports whether every budget Serve reads is positive. Zero never
// means unbounded.
func (l HTTPLimits) valid() bool {
	return l.MaxBodyBytes > 0 && l.MaxHeaderBytes > 0 && l.IdleTimeout > 0 && l.ReadHeaderTimeout > 0 && l.ReadBodyTimeout > 0
}

type HTTPField struct{ Name, Value string }
type httpIncoming struct {
	Method, Path, RawPath string
	Query, Header         []HTTPField
	Body                  string
	TooLarge              bool
	ReadError             string
}
type HTTPResponse struct {
	Status int
	Header []HTTPField
	Body   string
}

// HTTPWriteFailed is http.WriteFailed: a streamed write the connection
// refused, or a write after its stream ended.
type HTTPWriteFailed struct{ Reason string }

func (e HTTPWriteFailed) Error() string { return "http.WriteFailed: " + e.Reason }

// Exchange is the response side of one served request: std http's live
// Streaming provider writes a streamed body through it. Its request task
// owns it: Serve's transport goroutine waits for that task's end before it
// touches the ResponseWriter again, and mu orders the task's writes with
// that end, so a writer kept past its stream reaches a closed Exchange.
type Exchange struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	started bool // Begin committed the status and the header
	ended   bool // End ran, or the request task ended
}

// runtimeOnly implements http.Exchange's private operation (runtime/v1/sealed.go).
func (*Exchange) runtimeOnly() {}

// Begin commits status and header and flushes them, so the client sees
// the response start before the first part. A second Begin is a defect:
// a response starts once.
//
//ergo:wait none
func (x *Exchange) Begin(status int, header []HTTPField) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.started || x.ended {
		panic("http.Stream: the response has already started")
	}
	x.started = true
	h := x.w.Header()
	for _, f := range header {
		h.Add(f.Name, f.Value)
	}
	if h.Get("Content-Type") == "" {
		// One rule with the buffered form: no content type is sniffed.
		h["Content-Type"] = nil
	}
	x.w.WriteHeader(status)
	_ = http.NewResponseController(x.w).Flush()
}

// Write sends text as the next part of the body and flushes it. A stop of
// t ends the write with the stop; a write after End, or one the
// connection refuses, fails WriteFailed.
//
//ergo:wait caller
func (x *Exchange) Write(t *rt.Task, text string) error {
	t.Check()
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ended || !x.started {
		return HTTPWriteFailed{Reason: "the stream has ended"}
	}
	if _, err := io.WriteString(x.w, text); err != nil {
		return HTTPWriteFailed{Reason: err.Error()}
	}
	if err := http.NewResponseController(x.w).Flush(); err != nil {
		return HTTPWriteFailed{Reason: err.Error()}
	}
	return nil
}

// Send writes status, header and body as the whole response, with its
// length, straight from body's buffer (std http SendBytes, GX9):
// no string of the body is made. The header rules are the buffered
// form's (an empty body has no content type, none is sniffed). A
// response that has started is a defect, as Begin's is. A write the
// connection refuses is dropped, as the buffered form's is: the client
// went away, and the request ends with it.
//
//ergo:wait none
func (x *Exchange) Send(status int, header []HTTPField, body []byte) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.started || x.ended {
		panic("http.SendBytes: the response has already started")
	}
	x.started, x.ended = true, true
	h := x.w.Header()
	for _, f := range header {
		h.Add(f.Name, f.Value)
	}
	if len(body) == 0 {
		h["Content-Type"] = nil
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	x.w.WriteHeader(status)
	if len(body) > 0 {
		_, _ = x.w.Write(body)
	}
}

// End closes the stream: every later Write fails.
//
//ergo:wait none
func (x *Exchange) End() {
	x.mu.Lock()
	x.ended = true
	x.mu.Unlock()
}

// finish closes x for good once the request task ended and reports
// whether the response started, so the transport writes no second one.
func (x *Exchange) finish() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ended = true
	return x.started
}

// HTTPListen binds TCP. Port zero selects an available port, reported by
// Addr. The caller either hands the transport to Serve or closes it.
//
//ergo:host
//ergo:wait none
func HTTPListen(addr string) (*ergonet.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, HTTPListenFailed{Addr: addr, Reason: err.Error()}
	}
	return ergonet.ListenerOn(ln), nil
}

type httpServer struct {
	limits HTTPLimits
	site   string
	handle func(*rt.Task, *Exchange, string, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse
	// requests is the parent of every request task (rt.Group): each runs
	// on the net/http goroutine that read its request.
	requests *rt.Group
	mu       sync.Mutex
	closing  bool
	workers  sync.WaitGroup

	// failed wakes the serving goroutine when a request ended in a defect;
	// fmu guards defects, every such exit, which the serving task raises.
	failed  chan struct{}
	fmu     sync.Mutex
	defects rt.Cause

	// timers is the parent of every phase sleep (rt.Group): each runs on
	// its connection's timer goroutine (timeouts), on sleeper, the
	// program's Scheduler. stopping ends those goroutines.
	timers    *rt.Group
	sleeper   v1.Sleeper
	timerSite string
	stopping  chan struct{}

	// alarms is the Scheduler's AlarmClock (v1.Alarmer): with it, each
	// connection keeps one deadline and at most one alarm (enterLocked),
	// and no timer goroutine, task or sleep runs. nil for a Go Sleeper with no
	// Alarms: the reference path above.
	alarms *v1.AlarmClock

	// conns are the connections the engine reads (serveConn), which a
	// stop closes; nil when net/http reads every one (serveEngine).
	conns map[*httpConn]struct{}

	// peerW is the engine's watcher of its connections' peers, made at
	// the first watch (peers); peerFailed: none can be made. Guarded by mu.
	peerW      *peerWatcher
	peerFailed bool
}

// phase is the read phase of one connection. Header, body and idle are
// bounded by the Limits timeout of that name; the others are not: a
// handler takes as long as it takes, under its own budgets.
type phase uint32

const (
	phaseFree   phase = iota // between the header and the body, and while the handler runs
	phaseHeader              // from the accept, or the first byte after idle, to the whole header
	phaseBody                // while Serve reads the request body
	phaseIdle                // a keep-alive connection waits for its next request
	phaseClosed              // the connection ended
)

// timeout is the bound of phase p, or 0 for an unbounded phase.
func (l HTTPLimits) timeout(p phase) time.Duration {
	switch p {
	case phaseHeader:
		return l.ReadHeaderTimeout
	case phaseBody:
		return l.ReadBodyTimeout
	case phaseIdle:
		return l.IdleTimeout
	}
	return 0
}

// httpTimeout is the read error of a connection whose phase outlived its
// bound. It is a net.Error with Timeout, so net/http closes an idle or
// header-phase connection quietly, as for its own deadlines.
type httpTimeout struct{ budget string }

func (e httpTimeout) Error() string { return "http: the connection passed its " + e.budget }
func (httpTimeout) Timeout() bool   { return true }
func (httpTimeout) Temporary() bool { return false }
func (p phase) budget() string {
	switch p {
	case phaseHeader:
		return "ReadHeaderTimeout"
	case phaseBody:
		return "ReadBodyTimeout"
	}
	return "IdleTimeout"
}

// aLongTimeAgo is a read deadline in the past: a pending Read returns at
// once (net/http's own abort uses the same).
var aLongTimeAgo = time.Unix(1, 0)

// httpConn is one accepted connection. net/http and the request worker
// move its phase (enter); its timer goroutine (timeouts) bounds the phase.
// An expiry pins a past read deadline, so every later read fails, whatever
// deadline net/http sets after it.
type httpConn struct {
	net.Conn
	s       *httpServer
	at      atomic.Uint32 // the phase, for Read's lock-free check
	kick    chan struct{} // a phase change wakes the timer goroutine
	mu      sync.Mutex
	phase   phase
	epoch   uint64   // counts phase changes; a sleep expires only its own
	expired phase    // the phase that expired, or phaseFree
	sleep   *rt.Task // the phase sleep in flight, cut by the next change
	request *rt.Task // the request task that a disconnect stops

	// The alarm path (alarms): the current bounded phase's deadline on
	// the AlarmClock (bounded), the armed alarm and the time it is armed
	// for, and wake, made once, which every alarm runs.
	deadline, armedAt time.Duration
	bounded           bool
	alarm             v1.Alarm
	wakeFn            func()
	gone              bool // a read failed other than by a deadline: the peer left

	// handed: the engine gave c to net/http (engineListener.handOver),
	// with replay, the bytes it had read, for net/http's first reads.
	handed bool
	replay []byte
}

// enter moves c to phase p: it cuts the sleep of the old phase and wakes
// c's timer goroutine, which sleeps the new phase's bound, if it has one.
// The timer's deadline moves in place: no task is started for the change.
func (c *httpConn) enter(p phase) {
	c.mu.Lock()
	c.enterLocked(p)
	c.mu.Unlock()
}

func (c *httpConn) enterLocked(p phase) {
	if c.phase == phaseClosed {
		return
	}
	c.phase = p
	c.at.Store(uint32(p))
	c.epoch++
	if a := c.s.alarms; a != nil {
		// The alarm path (HTTP-FAST-b2): a bounded phase sets c's one
		// deadline, and arms an alarm only when none is armed or the
		// armed one comes after the deadline; an unbounded phase clears
		// the deadline and leaves the alarm armed. An alarm that comes
		// early re-arms itself for the deadline (wake). So a phase change
		// is one clock read, and a kept-alive request whose phases end
		// before their bounds arms nothing. The close stops the alarm:
		// net/http closes every connection when Serve stops, so no alarm
		// outlives its connection.
		if p == phaseClosed {
			c.bounded = false
			if c.alarm != nil {
				c.alarm.Stop()
				c.alarm = nil
			}
			return
		}
		d := c.s.limits.timeout(p)
		if d <= 0 {
			c.bounded = false
			return
		}
		c.deadline, c.bounded = a.Deadline(d), true
		if c.alarm == nil || c.armedAt > c.deadline {
			if c.alarm != nil {
				c.alarm.Stop()
			}
			c.armedAt = c.deadline
			c.alarm = a.After(d, c.wakeFn)
		}
		a.Began(d)
		return
	}
	if c.sleep != nil {
		c.sleep.Interrupt()
		c.sleep = nil
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Read ends the idle phase at the first byte of the next request, and
// names an expiry in the error of every read after it.
func (c *httpConn) Read(b []byte) (int, error) {
	if len(c.replay) > 0 {
		n := copy(b, c.replay)
		c.replay = c.replay[n:]
		return n, nil
	}
	n, err := c.Conn.Read(b)
	if n > 0 && phase(c.at.Load()) == phaseIdle {
		c.mu.Lock()
		if c.phase == phaseIdle {
			c.enterLocked(phaseHeader)
		}
		c.mu.Unlock()
	}
	if err != nil {
		c.mu.Lock()
		expired := c.expired
		if !isTimeout(err) {
			// The peer closed or reset the connection: net/http's
			// background read sees it while a handler runs, and the
			// request stops, as net/http cancels its context. A deadline
			// net/http sets to end that read is no disconnect.
			c.gone = true
			if c.request != nil {
				c.request.Interrupt()
			}
		}
		c.mu.Unlock()
		if expired != phaseFree {
			return n, httpTimeout{expired.budget()}
		}
	}
	return n, err
}

// isTimeout reports whether err, a read error of the accepted connection
// itself (never wrapped), is a read deadline's. An assertion, not
// errors.As: net/http's own deadline ends a read after every request.
func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}

// running makes t the request that a disconnect of c stops, or stops t at
// once when the peer has left; done (t == nil) ends it.
func (c *httpConn) running(t *rt.Task) {
	c.mu.Lock()
	c.request = t
	if t != nil && c.gone {
		t.Interrupt()
	}
	c.mu.Unlock()
}

// SetReadDeadline passes net/http's deadlines through until an expiry,
// and keeps the expiry's past deadline after it.
func (c *httpConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expired != phaseFree {
		return nil
	}
	return c.Conn.SetReadDeadline(t)
}

func (c *httpConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.Conn.SetWriteDeadline(t)
}

// CloseWrite keeps net/http's graceful close of a refused request.
func (c *httpConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// expire ends the read side of c when phase epoch is still current: a
// read now fails, so net/http closes the connection, or, in the body
// phase, the handler receives the failed read and its answer is the last.
func (c *httpConn) expire(epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch != epoch {
		return
	}
	c.expireLocked()
}

func (c *httpConn) expireLocked() {
	if c.expired != phaseFree || c.phase == phaseClosed {
		return
	}
	c.expired = c.phase
	_ = c.Conn.SetReadDeadline(aLongTimeAgo)
}

// wake is the function of each of c's alarms. The armed alarm is done
// once the clock reaches its time (an alarm stopped too late to keep its
// function from running may wake c early, and changes nothing). The
// current bounded phase expires when the clock has reached its deadline;
// an earlier wake arms the alarm again for the deadline.
func (c *httpConn) wake() {
	a := c.s.alarms
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase == phaseClosed {
		return
	}
	now := a.Now()
	if c.alarm != nil && now >= c.armedAt {
		c.alarm = nil
	}
	if !c.bounded {
		return
	}
	if now >= c.deadline {
		c.expireLocked()
		return
	}
	if c.alarm == nil {
		c.armedAt = c.deadline
		c.alarm = a.After(c.deadline-now, c.wakeFn)
	}
}

// timeouts bounds c's read phases, on a goroutine of c's own from the
// accept until c closes or the server stops: for each bounded phase it
// sleeps the phase's Limits timeout on the Scheduler, as a member of the
// server's timers group, and expires the phase when the sleep ran its
// whole length with the phase still current. A phase change cuts the
// sleep in flight (enter), so a connection has at most one sleep, and a
// test Scheduler decides when a phase expires. A defect of a sleep (a
// Scheduler's) reaches the serving task, as a request's does.
func (s *httpServer) timeouts(c *httpConn) {
	defer s.workers.Done()
	for {
		select {
		case <-c.kick:
		case <-s.stopping:
			return
		}
		c.mu.Lock()
		p, epoch := c.phase, c.epoch
		c.mu.Unlock()
		if p == phaseClosed {
			return
		}
		d := s.limits.timeout(p)
		if d <= 0 {
			continue
		}
		slept, err := rt.RunIn(s.timers, s.timerSite, func(st *rt.Task) (bool, error) {
			c.mu.Lock()
			if c.epoch != epoch {
				c.mu.Unlock()
				return false, nil
			}
			c.sleep = st
			c.mu.Unlock()
			defer func() {
				c.mu.Lock()
				if c.sleep == st {
					c.sleep = nil
				}
				c.mu.Unlock()
			}()
			return v1.Guarded(st, func(st *rt.Task) (bool, error) {
				s.sleeper.Sleep(st, d)
				return true, nil
			})
		})
		if err != nil {
			if !rt.OnlyInterruption(err) {
				s.fail(err)
				return
			}
			continue
		}
		if slept {
			c.expire(epoch)
		}
	}
}

// watch starts c's timer goroutine, unless the server is stopping. The
// alarm path needs none: c's phases arm their alarms.
func (s *httpServer) watch(c *httpConn) {
	if s.alarms != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return
	}
	s.workers.Add(1)
	go s.timeouts(c)
}

// httpListener wraps each accepted connection, so Serve sees its reads.
type httpListener struct {
	net.Listener
	s *httpServer
}

func (l httpListener) Accept() (net.Conn, error) {
	nc, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.s.newConn(nc), nil
}

// newConn wraps an accepted connection, for net/http or the engine.
func (s *httpServer) newConn(nc net.Conn) *httpConn {
	c := &httpConn{Conn: nc, s: s}
	if s.alarms == nil {
		c.kick = make(chan struct{}, 1)
	} else {
		c.wakeFn = c.wake
	}
	return c
}

// connKey finds a request's connection in its context.
type connKey struct{}

// connState moves a connection's phase on net/http's state changes.
// StateActive comes once the whole header is read.
func connState(nc net.Conn, st http.ConnState) {
	c, ok := nc.(*httpConn)
	if !ok {
		return
	}
	switch st {
	case http.StateNew:
		if c.handed {
			// The engine watched c and is in its header phase.
			return
		}
		c.s.watch(c)
		c.enter(phaseHeader)
	case http.StateActive:
		c.enter(phaseFree)
	case http.StateIdle:
		c.enter(phaseIdle)
	case http.StateClosed, http.StateHijacked:
		c.enter(phaseClosed)
	}
}

// HTTPServe is the transport body of std http.Serve. Each request runs
// as a task on the net/http goroutine that read it, a member of one
// rt.Group under the serving task t, so handlers run on the same task
// tree as task.Go and RPC with no dispatcher goroutine between the
// connection and its handler (Cowboy, Bandit and mist run a request in
// its connection's own process the same way). A disconnect stops that
// request alone; a stop of t, or a defect of any request, stops them all. End means completed child
// cleanup, closed connections and joined transport workers, not merely a
// cancellation signal. Defects remain defects and reach the serving task.
// Each handler call gets its request's Exchange, through which std http's
// Stream sends a streamed body; once the call's task ends the Exchange is
// closed, and a started stream is the whole response.
//
// Each connection's read phases are bounded by the Limits timeouts: the
// header (from the accept, or from the first byte after idle, to the
// whole header), the body, and the idle wait between requests, on
// sleeper, the program's Scheduler, so a test Scheduler decides when a
// phase expires, as for timeout.After; Serve arms no host timer of its
// own. A program's Scheduler has Alarms (v1.Alarmer), which Serve calls
// once on t: each connection keeps one deadline on that AlarmClock, which
// a bounded phase moves, and one alarm, armed only when the deadline
// comes first (enterLocked, wake): no task, goroutine or sleep per phase.
// A Go Sleeper with no Alarms (a test's) takes the reference path: each
// connection has one phase timer, a goroutine whose sleeps are tasks
// under t; a phase that ends first cuts its sleep, and the next bounded
// phase sleeps its own bound on the same goroutine. Both expire the same
// phases (TestHTTPPhasesExpireOnTheScheduler runs each). An expired idle or header phase closes the connection; an
// expired body phase reaches the handler as a failed read, and its answer
// is the connection's last. A stop drains the timers with the requests.
//
//ergo:host
//ergo:wait child
func HTTPServe(site v1.Site, t *rt.Task, sleeper v1.Sleeper, l *ergonet.Listener, limits HTTPLimits, handle func(*rt.Task, *Exchange, string, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse) error {
	return httpServe(site, t, sleeper, l, limits, handle, serveEngine)
}

// httpServe is HTTPServe with its reader chosen: engine, Serve's own
// (serveConn), or net/http's for every connection, the reference.
func httpServe(site v1.Site, t *rt.Task, sleeper v1.Sleeper, l *ergonet.Listener, limits HTTPLimits, handle func(*rt.Task, *Exchange, string, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse, engine bool) error {
	if !limits.valid() {
		return HTTPInvalidLimits(limits)
	}
	defer l.Close()
	t.Check()
	s := &httpServer{limits: limits, site: string(site), handle: handle, requests: rt.NewGroup(t, string(site)+" (requests)"), failed: make(chan struct{}, 1),
		timers: rt.NewGroup(t, string(site)+" (timeouts)"), sleeper: sleeper, timerSite: string(site) + " (timeout)", stopping: make(chan struct{})}
	if a, ok := sleeper.(v1.Alarmer); ok {
		// The Scheduler's Alarms runs here, on the serving task, once:
		// a provider's Ergo code never runs on a connection's goroutine.
		s.alarms = a.Alarms(t)
	}
	srv := &http.Server{
		Handler:        s,
		MaxHeaderBytes: limits.MaxHeaderBytes,
		ConnState:      connState,
		ConnContext:    func(ctx context.Context, nc net.Conn) context.Context { return context.WithValue(ctx, connKey{}, nc) },
	}
	served := make(chan error, 1)
	var el *engineListener
	if engine {
		// The engine reads each connection; net/http serves the ones it
		// hands over.
		s.conns = map[*httpConn]struct{}{}
		el = newEngineListener(l.GoListener(), s)
		s.workers.Add(1)
		go el.accept()
		go func() { served <- srv.Serve(el) }()
	} else {
		go func() { served <- srv.Serve(httpListener{l.GoListener(), s}) }()
	}
	// shutdown stops the requests and the timers and joins them: every
	// request task and every sleep has returned once its goroutine has
	// (workers). It returns the defects of the requests and of the timers.
	shutdown := func() error {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.stopping)
		_ = srv.Close() // unblock even a partial header or body read
		if el != nil {
			_ = el.Close()
			s.mu.Lock()
			for c := range s.conns {
				_ = c.Conn.Close()
			}
			s.mu.Unlock()
		}
		// Closing a connection cuts its sleep (enter), so the timers end
		// with the connections.
		s.requests.Interrupt()
		var causes rt.Cause
		s.workers.Wait()
		s.mu.Lock()
		if s.peerW != nil {
			s.peerW.close() // every connection it watched has ended
		}
		s.mu.Unlock()
		s.requests.End()
		s.timers.End()
		s.fmu.Lock()
		causes.Add(s.defects.Err())
		s.fmu.Unlock()
		return causes.Err()
	}
	for {
		select {
		case <-s.failed:
			err := shutdown()
			<-served
			v1.Raise(err)
		case <-t.Done():
			var causes rt.Cause
			causes.AddInterruption()
			causes.Add(shutdown())
			<-served
			v1.Raise(causes.Err())
		case err := <-served:
			var causes rt.Cause
			causes.Add(v1.Capture(t, fmt.Errorf("http.Serve: %w", err)))
			causes.Add(shutdown())
			v1.Raise(causes.Err())
		}
	}
}

func (s *httpServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add and the closing transition share a lock: Wait cannot miss a
	// transport worker admitted concurrently with shutdown.
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		panic(http.ErrAbortHandler)
	}
	s.workers.Add(1)
	s.mu.Unlock()
	defer s.workers.Done()
	in := httpIncoming{Method: r.Method, Path: r.URL.Path, RawPath: r.URL.EscapedPath(), Header: httpFields(r.Header)}
	if r.URL.RawQuery != "" {
		in.Query = httpParseQuery(r.URL.RawQuery)
	}
	ctx := r.Context()
	conn, _ := ctx.Value(connKey{}).(*httpConn)
	// A request with no body reads none. The body phase is bounded only
	// while there is a body to read.
	if r.Body != http.NoBody {
		if conn != nil {
			conn.enter(phaseBody)
		}
		body, err := readBody(w, r, s.limits.MaxBodyBytes)
		if conn != nil {
			conn.enter(phaseFree)
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				in.TooLarge = true
			} else {
				in.ReadError = err.Error()
			}
		} else {
			in.Body = body
		}
	}
	// net/http cancels the request context on an input EOF, even when the
	// peer only closed its write half and can still read our response:
	// after a read refusal the program gets the typed refusal, and only a
	// stop of the server stops the request.
	watch := in.ReadError == "" && conn != nil
	if watch && ctx.Err() != nil {
		// The client left before its handler started.
		panic(http.ErrAbortHandler)
	}
	if !s.answer(w, &in, conn, watch) {
		panic(http.ErrAbortHandler)
	}
}

// answer runs the handler of in and writes its response on the same request
// task, so a panic while writing is also a request defect. A false result
// closes the connection without a further response.
func (s *httpServer) answer(w http.ResponseWriter, in *httpIncoming, conn *httpConn, watch bool) bool {
	x := &Exchange{w: w}
	_, err := rt.RunIn(s.requests, s.site, func(ct *rt.Task) (struct{}, error) {
		return v1.Guarded(ct, func(ct *rt.Task) (struct{}, error) {
			ew, onEngine := w.(*engineResponse)
			response := s.callHandler(ct, x, in, conn, watch, ew)
			if !x.finish() {
				ct.Check()
				fast := onEngine && engineFastWriter && ew.fastAnswer(response)
				if onEngine && engineFastHook != nil {
					engineFastHook(fast)
				}
				if !fast {
					httpWriteAnswer(w, response)
				}
			}
			return struct{}{}, nil
		})
	})
	if err != nil {
		// The handler may have stopped or panicked before finish closed
		// its Exchange. No writer it kept can write after the task ends.
		x.End()
		if !rt.OnlyInterruption(err) {
			s.fail(err)
		}
		return false
	}
	return true
}

// callHandler watches only the handler: a disconnect stops this request
// alone. The engine arms its watch at the handler's first interruption
// point, so the transport's own pre-write check does not start a watch.
func (s *httpServer) callHandler(ct *rt.Task, x *Exchange, in *httpIncoming, conn *httpConn, watch bool, ew *engineResponse) HTTPResponse {
	if watch {
		conn.running(ct)
		defer conn.running(nil)
	}
	ct.Check()
	if watch && ew != nil && engineLazyWatch {
		defer rt.WhenPolled(ct, &ew.e.poll, ew.e.watchFn).End()
	}
	return s.handle(ct, x, in.Method, in.Path, in.RawPath, in.Query, in.Header, in.Body, in.TooLarge, in.ReadError)
}

// httpWriteAnswer writes a handler's buffered answer: Serve's, and a local
// Network's (httpLocalServe), so both send the same bytes.
func httpWriteAnswer(w http.ResponseWriter, r HTTPResponse) {
	for _, f := range r.Header {
		w.Header().Add(f.Name, f.Value)
	}
	if r.Body == "" {
		// A nil value suppresses net/http's content sniffing without
		// writing an empty Content-Type header in the HTTP output.
		w.Header()["Content-Type"] = nil
	}
	w.WriteHeader(r.Status)
	// net/http suppresses HEAD's body and still computes the GET
	// content length and content type from this buffered representation.
	if r.Body != "" {
		_, _ = io.WriteString(w, r.Body)
	}
}

// bodyChunks holds the read buffers of readBody.
var bodyChunks = sync.Pool{New: func() any { return new([2048]byte) }}

// readBody is r's body, at most max bytes (a longer one is
// *http.MaxBytesError). The text is one allocation of its own size: a
// declared length sizes it first, and the reads go through a pooled
// chunk, not io.Copy's 32 KB buffer per request (neither a
// MaxBytesReader nor a strings.Builder takes io.Copy's shortcuts).
func readBody(w http.ResponseWriter, r *http.Request, max int) (string, error) {
	var body strings.Builder
	if n := r.ContentLength; n > 0 && n <= int64(max) {
		body.Grow(int(n))
	}
	chunk := bodyChunks.Get().(*[2048]byte)
	defer bodyChunks.Put(chunk)
	_, err := io.CopyBuffer(&body, http.MaxBytesReader(w, r.Body, int64(max)), chunk[:])
	return body.String(), err
}

// fail keeps a request's or a sleep's defect for the serving task, and wakes it to
// stop the server and raise every defect.
func (s *httpServer) fail(err error) {
	s.fmu.Lock()
	s.defects.Add(err)
	s.fmu.Unlock()
	select {
	case s.failed <- struct{}{}:
	default:
	}
}

// httpParseQuery is the Query of every request Serve gives a handler, as
// std http's ParseQuery (Ergo) reads it: url.ParseQuery's pairs, a pair with a malformed
// escape dropped as Request.URL.Query drops it.
func httpParseQuery(raw string) []HTTPField {
	values, _ := url.ParseQuery(raw)
	return httpFields(values)
}

// HTTPBadEscape is http.BadEscape: a raw path with a "%" that two hex
// digits do not follow.
type HTTPBadEscape struct{ Raw string }

func (e HTTPBadEscape) Error() string { return "http.BadEscape: " + e.Raw }

// HTTPDecodePath is std http.DecodePath: the path raw escapes, decoded
// as Serve decodes a request's Path (url.PathUnescape: "%XX" is the byte,
// "+" stays "+").
//
//ergo:wait none
func HTTPDecodePath(raw string) (string, error) {
	p, err := url.PathUnescape(raw)
	if err != nil {
		return "", HTTPBadEscape{Raw: raw}
	}
	return p, nil
}

// httpFields keeps repeated values in their host order; sorting names
// makes a buffered request independent of Go map iteration order.
func httpFields(values map[string][]string) []HTTPField {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := 0
	for _, vs := range values {
		n += len(vs)
	}
	fields := make([]HTTPField, 0, n)
	for _, k := range keys {
		for _, v := range values[k] {
			fields = append(fields, HTTPField{k, v})
		}
	}
	return fields
}
