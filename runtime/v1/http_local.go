package v1

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/runtime/rt"
)

// A local Network (std http.InMemoryNetwork) has no socket, but it is not
// a model of HTTP: each exchange is Go's own client and Go's own server,
// joined by an in-memory net.Pipe. The client is HTTPFetch's (the same
// policy loop, a transport per header budget as httpTransportFor keeps,
// the same reads); the server is net/http's, answering as Serve does
// (httpWriteAnswer). So what a caller sees is what a loopback Serve gives,
// by construction: no body for HEAD, 204 or 304, an informational status
// before the final one, the declared length, the header budget, and a
// handler status net/http refuses, which ends the connection, with the
// client's retry of an idempotent request on a kept-alive one.
// TestHTTPLocalMatchesLoopback holds it to that.
//
// The handler runs on the caller's task: the server hands each request it
// reads to the exchange's caller over a channel (httpLocal.calls), and the
// caller answers it while it waits for the response (httpDoVia,
// httpStreamVia). The request carries its exchange's key in a header the
// server takes off before the handler sees the request.

// httpLocalKey is the header that names a request's exchange.
const httpLocalKey = "Ergo-Local-Exchange"

// httpLocal is one exchange of a local Network: the handler and the
// target's origin, and the requests the server hands to the caller.
type httpLocal struct {
	key    string
	origin string
	handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse
	in     chan *httpLocalCall
	done   chan struct{}
}

// httpLocalCall is one request the server read, and its answer.
type httpLocalCall struct {
	method, path  string
	query, header []HTTPField
	body          string
	out           chan HTTPResponse // buffered: the caller never waits on it
}

var (
	httpLocals    sync.Map // key -> *httpLocal
	httpLocalNext atomic.Uint64
)

// calls is the channel of requests to answer; nil, which never delivers,
// for an exchange with a host peer.
func (l *httpLocal) calls() <-chan *httpLocalCall {
	if l == nil {
		return nil
	}
	return l.in
}

// answer runs the handler on the caller's task t.
func (l *httpLocal) answer(t *rt.Task, c *httpLocalCall) {
	c.out <- l.handle(t, l.origin, c.method, c.path, c.query, c.header, c.body)
}

// httpOpenLocal registers the exchange of target; close forgets it and
// releases a server request still waiting on it.
func httpOpenLocal(target *url.URL, handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse) (*httpLocal, error) {
	origin, err := networkOrigin(&url.URL{Scheme: target.Scheme, Host: target.Host})
	if err != nil {
		return nil, err
	}
	l := &httpLocal{key: strconv.FormatUint(httpLocalNext.Add(1), 10), origin: origin, handle: handle, in: make(chan *httpLocalCall), done: make(chan struct{})}
	httpLocals.Store(l.key, l)
	return l, nil
}

func (l *httpLocal) close() {
	httpLocals.Delete(l.key)
	close(l.done)
}

// httpLocalServe is the local server's handler: Serve's reading of a
// request (path, query, header, the whole body), handed to the exchange's
// caller, and its answer written as Serve writes one.
func httpLocalServe(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(httpLocalKey)
	r.Header.Del(httpLocalKey)
	v, ok := httpLocals.Load(key)
	if !ok {
		// The exchange ended (a stop) before its request was read.
		panic(http.ErrAbortHandler)
	}
	l := v.(*httpLocal)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
	c := &httpLocalCall{method: r.Method, path: r.URL.Path, query: HTTPParseQuery(r.URL.RawQuery), header: httpFields(r.Header), body: string(body), out: make(chan HTTPResponse, 1)}
	select {
	case l.in <- c:
	case <-l.done:
		panic(http.ErrAbortHandler)
	case <-r.Context().Done():
		panic(http.ErrAbortHandler)
	}
	select {
	case answer := <-c.out:
		httpWriteAnswer(w, answer)
	case <-l.done:
		panic(http.ErrAbortHandler)
	}
}

// httpPipeListener is the local server's listener: each dial of a local
// transport hands it the server end of a new pipe.
type httpPipeListener struct{ conns chan net.Conn }

func (p httpPipeListener) Accept() (net.Conn, error) { return <-p.conns, nil }
func (httpPipeListener) Close() error                { return nil }
func (httpPipeListener) Addr() net.Addr              { return httpPipeAddr{} }

type httpPipeAddr struct{}

func (httpPipeAddr) Network() string { return "pipe" }
func (httpPipeAddr) String() string  { return "ergo-local" }

// httpLocalServer is the one local server of the process, started at the
// first local exchange. It is net/http's server with Serve's settings
// that touch an answer, and no log of its own: a refused handler status
// is the client's failure.
var httpLocalServer = sync.OnceValue(func() httpPipeListener {
	l := httpPipeListener{conns: make(chan net.Conn)}
	srv := &http.Server{Handler: http.HandlerFunc(httpLocalServe), ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = srv.Serve(l) }()
	return l
})

var httpLocalTransports sync.Map // header budget -> *http.Transport

// httpLocalTransportFor is httpTransportFor's transport with a pipe to the
// local server for every origin, http and https alike (the pipe is past
// any TLS handshake, as DialTLSContext's conn is).
func httpLocalTransportFor(maxHeader int) *http.Transport {
	if tr, ok := httpLocalTransports.Load(maxHeader); ok {
		return tr.(*http.Transport)
	}
	server := httpLocalServer()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, peer := net.Pipe()
		select {
		case server.conns <- peer:
			return client, nil
		case <-ctx.Done():
			client.Close()
			peer.Close()
			return nil, ctx.Err()
		}
	}
	tr := httpTransportFor(maxHeader).Clone()
	tr.Proxy = nil
	tr.DialContext = dial
	tr.DialTLSContext = dial
	tr.ForceAttemptHTTP2 = false
	got, _ := httpLocalTransports.LoadOrStore(maxHeader, tr)
	return got.(*http.Transport)
}

// HTTPFetchLocal is HTTPFetch with each exchange served in memory by
// handle (std http.InMemoryNetwork): the same policy loop, Go's client and
// server over a pipe, and handle called on the caller's task with the
// target's origin and the request as Serve gives a handler.
//
//ergo:wait caller
func HTTPFetchLocal(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse) (HTTPResponse, error) {
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		l, err := httpOpenLocal(u, handle)
		if err != nil {
			return HTTPResponse{}, err
		}
		defer l.close()
		return httpDoVia(t, httpLocalTransportFor(limits.MaxHeaderBytes), l, u, limits, method, append(fields, HTTPField{httpLocalKey, l.key}), body)
	})
}

// HTTPFetchLocalInto is HTTPFetchInto served in memory, as HTTPFetchLocal
// is HTTPFetch.
//
//ergo:wait caller
func HTTPFetchLocalInto(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, accepts func(int) bool, put func(*rt.Task, string) error, handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse) (HTTPResponse, error) {
	follow := func(r HTTPResponse) bool { return hops != -1 && httpLocation(r) != "" }
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		l, err := httpOpenLocal(u, handle)
		if err != nil {
			return HTTPResponse{}, err
		}
		defer l.close()
		return httpStreamVia(t, httpLocalTransportFor(limits.MaxHeaderBytes), l, u, limits, method, append(fields, HTTPField{httpLocalKey, l.key}), body, follow, accepts, put)
	})
}
