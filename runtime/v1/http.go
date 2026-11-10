package v1

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"

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

type HTTPInvalidLimits struct{ MaxBodyBytes, MaxHeaderBytes int }

func (e HTTPInvalidLimits) Error() string {
	return fmt.Sprintf("http.InvalidLimits: body=%d header=%d", e.MaxBodyBytes, e.MaxHeaderBytes)
}

type HTTPLimits struct{ MaxBodyBytes, MaxHeaderBytes int }
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
	limits  HTTPLimits
	calls   chan *httpCall
	quit    chan struct{}
	mu      sync.Mutex
	closing bool
	workers sync.WaitGroup
}

// HTTPServe is the transport body of std http.Serve. Only its calling
// goroutine creates and consumes task handles. net/http workers hand it
// requests, so handlers run on the same child-task tree as task.Go and
// RPC. A disconnect stops that request alone. End means completed child
// cleanup, closed connections and joined transport workers, not merely a
// cancellation signal. Defects remain defects and reach the serving task.
//
//ergo:host
func HTTPServe(site Site, t *rt.Task, l *Listener, limits HTTPLimits, handle func(*rt.Task, string, string, []HTTPField, []HTTPField, string, bool, string) HTTPResponse) error {
	if limits.MaxBodyBytes <= 0 || limits.MaxHeaderBytes <= 0 {
		return HTTPInvalidLimits{limits.MaxBodyBytes, limits.MaxHeaderBytes}
	}
	defer l.Close()
	t.Check()
	s := &httpServer{limits: limits, calls: make(chan *httpCall), quit: make(chan struct{})}
	srv := &http.Server{Handler: s, MaxHeaderBytes: limits.MaxHeaderBytes}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(l.ln) }()
	ended := make(chan *httpCall)
	live := map[*httpCall]bool{}
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
		var causes rt.Cause
		for len(live) > 0 {
			causes.Add(finish(<-ended, true))
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
					c.response = handle(ct, v.Method, v.Path, v.Query, v.Header, v.Body, v.TooLarge, v.ReadError)
					return struct{}{}, nil
				})
			})
			live[c] = true
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(s.limits.MaxBodyBytes)))
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
	c := &httpCall{in: in, done: make(chan struct{})}
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
	if c.err != nil {
		panic(http.ErrAbortHandler)
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
