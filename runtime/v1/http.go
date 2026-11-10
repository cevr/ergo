package v1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Listener is the transport shared by server protocols. HTTP adds no
// policy to it; ServerLimits belongs to the serving call, not to the transport.
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

// HTTPServerLimits is http.ServerLimits. The byte budgets bound what one
// request may send; the three timeouts bound how long a connection may
// stay in each read phase, measured on the program's Scheduler. Serve
// reads every field.
type HTTPServerLimits struct {
	MaxBodyBytes, MaxHeaderBytes                    int
	IdleTimeout, ReadHeaderTimeout, ReadBodyTimeout time.Duration
}

// HTTPClientLimits is http.ClientLimits: the budgets of one response.
// The client reads every field.
type HTTPClientLimits struct {
	MaxBodyBytes, MaxHeaderBytes int
}

// valid reports whether every budget Serve reads is positive. Zero never
// means unbounded.
func (l HTTPServerLimits) valid() bool {
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

// httpCall crosses the net/http goroutine and the serving task. The task
// owns h; done publishes its completed exit, including all frame releases.
type httpCall struct {
	in        httpIncoming
	x         *Exchange
	response  HTTPResponse
	err       error
	done      chan struct{}
	h         *rt.Handle[struct{}]
	mu        sync.Mutex
	task      *rt.Task
	cancelled bool
}

func (c *httpCall) cancel() {
	c.mu.Lock()
	c.cancelled = true
	t := c.task
	c.mu.Unlock()
	if t != nil {
		t.Interrupt()
	}
}

func (c *httpCall) start(t *rt.Task) {
	c.mu.Lock()
	c.task = t
	cancelled := c.cancelled
	c.mu.Unlock()
	if cancelled {
		t.Interrupt()
	}
}

type httpServer struct {
	limits  HTTPServerLimits
	calls   chan *httpCall
	quit    chan struct{}
	mu      sync.Mutex
	closing bool
	workers sync.WaitGroup

	// wake tells the serving goroutine that dirty holds connections whose
	// phase changed. dmu guards dirty; a connection is in it at most once.
	wake  chan struct{}
	dmu   sync.Mutex
	dirty []*httpConn
}

// phase is the read phase of one connection. Header, body and idle are
// bounded by the ServerLimits timeout of that name; the others are not: a
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
func (l HTTPServerLimits) timeout(p phase) time.Duration {
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

// httpConn is one accepted connection. net/http, the request worker and
// the serving goroutine move its phase (enter); the serving goroutine
// alone owns timer. An expiry pins a past read deadline, so every later
// read fails, whatever deadline net/http sets after it.
type httpConn struct {
	net.Conn
	s       *httpServer
	at      atomic.Uint32 // the phase, for Read's lock-free check
	mu      sync.Mutex
	phase   phase
	epoch   uint64 // counts phase changes; a timer expires only its own
	queued  bool   // c is in s.dirty
	expired phase  // the phase that expired, or phaseFree
	timer   *phaseTimer
}

// enter moves c to phase p and queues c for the serving goroutine, which
// stops the timer of the old phase and arms the new one.
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
	c.queueLocked()
}

// queueLocked asks the serving goroutine to read c's latest phase. A
// stopped timer uses it again once joined: interruption does not vacate
// the timer slot, so a connection never has two live timer tasks.
func (c *httpConn) queueLocked() {
	if c.queued {
		return
	}
	c.queued = true
	s := c.s
	s.dmu.Lock()
	s.dirty = append(s.dirty, c)
	s.dmu.Unlock()
	select {
	case s.wake <- struct{}{}:
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
		c.mu.Unlock()
		if expired != phaseFree {
			return n, httpTimeout{expired.budget()}
		}
	}
	return n, err
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

// take returns c's phase and epoch for the serving goroutine and lets a
// later change queue c again.
func (c *httpConn) take() (phase, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queued = false
	return c.phase, c.epoch
}

// phaseTimer bounds one phase of one connection: a child of the serving
// task that sleeps on the program's Scheduler. Only the serving goroutine
// touches its fields.
type phaseTimer struct {
	c         *httpConn
	epoch     uint64
	h         *rt.Handle[struct{}]
	cancelled bool
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
	return &httpConn{Conn: nc, s: l.s}, nil
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
		c.enter(phaseHeader)
	case http.StateActive:
		c.enter(phaseFree)
	case http.StateIdle:
		c.enter(phaseIdle)
	case http.StateClosed, http.StateHijacked:
		c.enter(phaseClosed)
	}
}

// HTTPServe is the transport body of std http.Serve. Only its calling
// goroutine creates and consumes task handles. net/http workers hand it
// requests, so handlers run on the same child-task tree as task.Go and
// RPC. A disconnect stops that request alone. End means completed child
// cleanup, closed connections and joined transport workers, not merely a
// cancellation signal. Defects remain defects and reach the serving task.
// Each handler call gets its request's Exchange, through which std http's
// Stream sends a streamed body; once the call's task ends the Exchange is
// closed, and a started stream is the whole response.
//
// Each connection's read phases are bounded by the ServerLimits timeouts: the
// header (from the accept, or from the first byte after idle, to the
// whole header), the body, and the idle wait between requests. A phase
// timer is a child of t that sleeps on sleeper, the program's Scheduler,
// so a test Scheduler decides when a phase expires, as for timeout.After;
// Serve arms no host timer of its own. A phase that ends first stops its
// timer. An expired idle or header phase closes the connection; an
// expired body phase reaches the handler as a failed read, and its answer
// is the connection's last. A stop drains the timers with the requests.
//
//ergo:host
func HTTPServe(site Site, t *rt.Task, sleeper Sleeper, l *Listener, limits HTTPServerLimits, handle func(*rt.Task, *Exchange, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse) error {
	if !limits.valid() {
		return HTTPInvalidLimits(limits)
	}
	defer l.Close()
	t.Check()
	s := &httpServer{limits: limits, calls: make(chan *httpCall), quit: make(chan struct{}), wake: make(chan struct{}, 1)}
	srv := &http.Server{
		Handler:        s,
		MaxHeaderBytes: limits.MaxHeaderBytes,
		ConnState:      connState,
		ConnContext:    func(ctx context.Context, nc net.Conn) context.Context { return context.WithValue(ctx, connKey{}, nc) },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(httpListener{l.ln, s}) }()
	ended := make(chan *httpCall)
	live := map[*httpCall]bool{}
	fired := make(chan *phaseTimer)
	timers := map[*phaseTimer]bool{}
	timerSite := string(site) + " (timeout)"
	arm := func(c *httpConn, d time.Duration, epoch uint64) {
		pt := &phaseTimer{c: c, epoch: epoch}
		holder := rt.NewScope(t)
		pt.h = rt.Go(&holder, timerSite, func(ct *rt.Task) (struct{}, error) {
			defer func() { fired <- pt }()
			return guarded(ct, func(ct *rt.Task) (struct{}, error) {
				sleeper.Sleep(ct, d)
				return struct{}{}, nil
			})
		})
		c.timer = pt
		timers[pt] = true
	}
	cancel := func(pt *phaseTimer) {
		pt.cancelled = true
		pt.h.Task().Interrupt()
		// Keep c.timer until settle joins the child, including its cleanup.
	}
	// settle ends a timer whose task has ended: a sleep that ran its whole
	// length expires its phase, if that phase is still current.
	settle := func(pt *phaseTimer) error {
		delete(timers, pt)
		var err error
		if pt.cancelled {
			err = pt.h.Stop()
		} else {
			_, err = pt.h.Wait()
			if err == nil {
				pt.c.expire(pt.epoch)
			}
		}
		if pt.c.timer == pt {
			pt.c.timer = nil
		}
		if pt.cancelled {
			// A phase change may have been taken while the old child was
			// stopping. Revisit the latest phase now that its slot is free.
			pt.c.mu.Lock()
			pt.c.queueLocked()
			pt.c.mu.Unlock()
		}
		return err
	}
	// rearm brings each changed connection's timer to its current phase.
	rearm := func() {
		s.dmu.Lock()
		dirty := s.dirty
		s.dirty = nil
		s.dmu.Unlock()
		for _, c := range dirty {
			p, epoch := c.take()
			if pt := c.timer; pt != nil && pt.epoch != epoch {
				cancel(pt)
			}
			if d := limits.timeout(p); d > 0 && c.timer == nil {
				arm(c, d, epoch)
			}
		}
	}
	finish := func(c *httpCall, stop bool) error {
		var err error
		if stop {
			err = c.h.Stop()
			c.err = rt.Interrupted
		} else {
			_, err = c.h.Wait()
			c.err = err
		}
		delete(live, c)
		close(c.done)
		return err
	}
	shutdown := func() error {
		s.mu.Lock()
		s.closing = true
		close(s.quit)
		s.mu.Unlock()
		_ = srv.Close() // unblock even a partial header or body read
		for c := range live {
			c.cancel()
		}
		for pt := range timers {
			cancel(pt)
		}
		var causes rt.Cause
		for len(live) > 0 || len(timers) > 0 {
			select {
			case c := <-ended:
				causes.Add(finish(c, true))
			case pt := <-fired:
				pt.cancelled = true
				causes.Add(settle(pt))
			}
		}
		s.workers.Wait()
		return causes.Err()
	}
	for {
		select {
		case c := <-s.calls:
			holder := rt.NewScope(t)
			c.h = rt.Go(&holder, string(site), func(ct *rt.Task) (struct{}, error) {
				defer func() { ended <- c }()
				return guarded(ct, func(ct *rt.Task) (struct{}, error) {
					c.start(ct)
					ct.Check()
					v := c.in
					c.response = handle(ct, c.x, v.Method, v.Path, v.Query, v.Header, v.Body, v.TooLarge, v.ReadError)
					return struct{}{}, nil
				})
			})
			live[c] = true
		case <-s.wake:
			rearm()
		case pt := <-fired:
			if err := settle(pt); err != nil && !rt.OnlyInterruption(err) {
				var causes rt.Cause
				causes.Add(err)
				causes.Add(shutdown())
				<-served
				raise(causes.Err())
			}
		case c := <-ended:
			err := finish(c, false)
			if err != nil && !rt.OnlyInterruption(err) {
				var causes rt.Cause
				causes.Add(err)
				causes.Add(shutdown())
				<-served
				raise(causes.Err())
			}
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
	in := httpIncoming{Method: r.Method, Path: r.URL.Path, Header: httpFields(r.Header), Query: httpFields(r.URL.Query())}
	// The body phase is bounded only while there is a body to read.
	conn, _ := r.Context().Value(connKey{}).(*httpConn)
	if r.Body == http.NoBody {
		conn = nil
	}
	if conn != nil {
		conn.enter(phaseBody)
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(s.limits.MaxBodyBytes)))
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
		in.Body = string(body)
	}
	c := &httpCall{in: in, x: &Exchange{w: w}, done: make(chan struct{})}
	gone := r.Context().Done()
	if in.ReadError != "" {
		// net/http cancels the request context on an input EOF, even
		// when the peer only closed its write half and can still read
		// our response. Deliver the typed read refusal to the program;
		// its child remains owned and stoppable by the serving task.
		gone = nil
	}
	select {
	case s.calls <- c:
	case <-s.quit:
		panic(http.ErrAbortHandler)
	case <-gone:
		panic(http.ErrAbortHandler)
	}
	select {
	case <-c.done:
	case <-gone:
		c.cancel()
		<-c.done
	}
	// The request task has ended: a writer it kept now fails, and a
	// streamed response is complete (or, after a stop or a defect,
	// aborted with the connection, so the client sees a cut body).
	streamed := c.x.finish()
	if c.err != nil {
		panic(http.ErrAbortHandler)
	}
	if streamed {
		return
	}
	for _, f := range c.response.Header {
		w.Header().Add(f.Name, f.Value)
	}
	if c.response.Body == "" {
		// A nil value suppresses net/http's content sniffing without
		// writing an empty Content-Type header in the HTTP output.
		w.Header()["Content-Type"] = nil
	}
	w.WriteHeader(c.response.Status)
	// net/http suppresses HEAD's body and still computes the GET
	// content length and content type from this buffered representation.
	if c.response.Body != "" {
		_, _ = io.WriteString(w, c.response.Body)
	}
}

// httpFields keeps repeated values in their host order; sorting names
// makes a buffered request independent of Go map iteration order.
func httpFields(values map[string][]string) []HTTPField {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var fields []HTTPField
	for _, k := range keys {
		for _, v := range values[k] {
			fields = append(fields, HTTPField{k, v})
		}
	}
	return fields
}
