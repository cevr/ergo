package v1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Listener is the transport shared by server protocols. HTTP adds no
// policy to it; Limits belongs to the serving call, not to the transport.
type Listener struct {
	ln net.Listener
}

func (l *Listener) bound()       {}
func (l *Listener) Addr() string { return l.ln.Addr().String() }
func (l *Listener) Close()       { _ = l.ln.Close() }

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
	Method, Path  string
	Query, Header []HTTPField
	Body          string
	TooLarge      bool
	ReadError     string
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

// Begin commits status and header and flushes them, so the client sees
// the response start before the first part. A second Begin is a defect:
// a response starts once.
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

// End closes the stream: every later Write fails.
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

// ListenerOn is a Listener over ln, which Go code makes: an in-process
// transport for a benchmark of Serve. Ergo source has only Listen.
func ListenerOn(ln net.Listener) *Listener { return &Listener{ln: ln} }

// HTTPListen binds TCP. Port zero selects an available port, reported by
// Addr. The caller either hands the transport to Serve or closes it.
//
//ergo:host
func HTTPListen(addr string) (*Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, HTTPListenFailed{Addr: addr, Reason: err.Error()}
	}
	return &Listener{ln: ln}, nil
}

type httpServer struct {
	limits HTTPLimits
	site   string
	handle func(*rt.Task, *Exchange, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse
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
	sleeper   Sleeper
	timerSite string
	stopping  chan struct{}
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
	gone    bool     // a read failed other than by a deadline: the peer left
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
	if c.epoch != epoch || c.expired != phaseFree || c.phase == phaseClosed {
		return
	}
	c.expired = c.phase
	_ = c.Conn.SetReadDeadline(aLongTimeAgo)
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
			return guarded(st, func(st *rt.Task) (bool, error) {
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

// watch starts c's timer goroutine, unless the server is stopping.
func (s *httpServer) watch(c *httpConn) {
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
	return &httpConn{Conn: nc, s: l.s, kick: make(chan struct{}, 1)}, nil
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
// whole header), the body, and the idle wait between requests. Each
// connection has one phase timer, a goroutine whose sleeps are tasks under
// t that sleep on sleeper, the program's Scheduler, so a test Scheduler
// decides when a phase expires, as for timeout.After; Serve arms no host
// timer of its own. A phase that ends first cuts its sleep, and the next
// bounded phase sleeps its own bound on the same goroutine. An expired idle or header phase closes the connection; an
// expired body phase reaches the handler as a failed read, and its answer
// is the connection's last. A stop drains the timers with the requests.
//
//ergo:host
func HTTPServe(site Site, t *rt.Task, sleeper Sleeper, l *Listener, limits HTTPLimits, handle func(*rt.Task, *Exchange, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse) error {
	if !limits.valid() {
		return HTTPInvalidLimits(limits)
	}
	defer l.Close()
	t.Check()
	s := &httpServer{limits: limits, site: string(site), handle: handle, requests: rt.NewGroup(t, string(site)+" (requests)"), failed: make(chan struct{}, 1),
		timers: rt.NewGroup(t, string(site)+" (timeouts)"), sleeper: sleeper, timerSite: string(site) + " (timeout)", stopping: make(chan struct{})}
	srv := &http.Server{
		Handler:        s,
		MaxHeaderBytes: limits.MaxHeaderBytes,
		ConnState:      connState,
		ConnContext:    func(ctx context.Context, nc net.Conn) context.Context { return context.WithValue(ctx, connKey{}, nc) },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(httpListener{l.ln, s}) }()
	// shutdown stops the requests and the timers and joins them: every
	// request task and every sleep has returned once its goroutine has
	// (workers). It returns the defects of the requests and of the timers.
	shutdown := func() error {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(s.stopping)
		_ = srv.Close() // unblock even a partial header or body read
		s.requests.Interrupt()
		s.timers.Interrupt()
		var causes rt.Cause
		s.workers.Wait()
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
			raise(err)
		case <-t.Done():
			var causes rt.Cause
			causes.AddInterruption()
			causes.Add(shutdown())
			<-served
			raise(causes.Err())
		case err := <-served:
			var causes rt.Cause
			causes.Add(capture(t, fmt.Errorf("http.Serve: %w", err)))
			causes.Add(shutdown())
			raise(causes.Err())
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
	in := httpIncoming{Method: r.Method, Path: r.URL.Path, Header: httpFields(r.Header)}
	if r.URL.RawQuery != "" {
		in.Query = HTTPParseQuery(r.URL.RawQuery)
	}
	ctx := r.Context()
	conn, _ := ctx.Value(connKey{}).(*httpConn)
	// A request with no body reads none. The body phase is bounded only
	// while there is a body to read.
	if r.Body != http.NoBody {
		if conn != nil {
			conn.enter(phaseBody)
		}
		var body strings.Builder
		_, err := io.Copy(&body, http.MaxBytesReader(w, r.Body, int64(s.limits.MaxBodyBytes)))
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
			in.Body = body.String()
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
	x := &Exchange{w: w}
	response, err := rt.RunIn(s.requests, s.site, func(ct *rt.Task) (HTTPResponse, error) {
		if watch {
			// A disconnect stops this request alone.
			conn.running(ct)
			defer conn.running(nil)
		}
		return guarded(ct, func(ct *rt.Task) (HTTPResponse, error) {
			ct.Check()
			return s.handle(ct, x, in.Method, in.Path, in.Query, in.Header, in.Body, in.TooLarge, in.ReadError), nil
		})
	})
	// The request task has ended: a writer it kept now fails, and a
	// streamed response is complete (or, after a stop or a defect,
	// aborted with the connection, so the client sees a cut body).
	streamed := x.finish()
	if err != nil {
		if !rt.OnlyInterruption(err) {
			s.fail(err)
		}
		panic(http.ErrAbortHandler)
	}
	if streamed {
		return
	}
	for _, f := range response.Header {
		w.Header().Add(f.Name, f.Value)
	}
	if response.Body == "" {
		// A nil value suppresses net/http's content sniffing without
		// writing an empty Content-Type header in the HTTP output.
		w.Header()["Content-Type"] = nil
	}
	w.WriteHeader(response.Status)
	// net/http suppresses HEAD's body and still computes the GET
	// content length and content type from this buffered representation.
	if response.Body != "" {
		_, _ = io.WriteString(w, response.Body)
	}
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

// HTTPParseQuery is std http.ParseQuery and the Query of every request
// Serve gives a handler: url.ParseQuery's pairs, a pair with a malformed
// escape dropped as Request.URL.Query drops it.
func HTTPParseQuery(raw string) []HTTPField {
	values, _ := url.ParseQuery(raw)
	return httpFields(values)
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
