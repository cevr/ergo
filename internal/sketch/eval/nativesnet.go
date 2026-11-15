package eval

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The simulated loopback network of a RunMain process (ORACLE O26): a
// Listen binds a port of the simulated host, Serve answers the requests
// sent to it, and a Client's send reaches a listener of the same process.
// Nothing touches the host's network. Each exchange runs Go's own
// net/http client and server over an in-memory pipe (net.Pipe), so the
// request the handler reads and the response the client reads are what
// Go's transport and server make of them: the canonical header names, the
// Content-Length and Content-Type net/http adds, a HEAD response with no
// body, and the server's own refusals (a header over MaxHeaderBytes).
// The handler itself runs on the evaluator, in the serving task, as
// runtime/v1's Serve runs it in a child of that task. The Date field
// reads the simulated host clock.
//
// Not run (counted skips): an origin that is not a literal loopback
// address, https, a streamed response (Exchange), a streamed client body
// (sendInto). The limits' timeouts never expire: every request is written
// at once, so no read phase waits.

// listenerV is an http.Listener bound on the simulated host.
type listenerV struct {
	h       *host
	ip      netip.Addr // the unspecified address binds every local one
	any     bool       // bound with no host: both families
	port    int
	addr    string
	closed  bool
	serving bool
	limits  *record
	pending []*netExchange
}

// netExchange is one request sent to a listener: the request as the
// server read it, and the handler's response once the serving task has
// answered it.
type netExchange struct {
	req  *netRequest
	resp *record
	done bool
}

// netRequest is a request as Go's server gave it to the handler.
type netRequest struct {
	method, path  string
	query, header []netField
	body          string
	tooLarge      bool
	readError     string
	reply         chan *netResponse
}

type netField struct{ name, value string }

type netResponse struct {
	status int
	header []netField
	body   string
}

func (l *listenerV) describe() string { return "an http.Listener" }

func (l *listenerV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	switch name {
	case "Addr":
		return l.addr, nil
	case "Close":
		l.close()
		return unit{}, nil
	}
	return nil, fr.unsupported(at, "the http.Listener operation %s", name)
}

// close unbinds the port; a request that waits for an answer is cut.
func (l *listenerV) close() {
	l.closed = true
	if l.h.listeners[l.port] != nil {
		ls := l.h.listeners[l.port]
		for i, x := range ls {
			if x == l {
				l.h.listeners[l.port] = append(ls[:i:i], ls[i+1:]...)
				break
			}
		}
	}
}

// accepts reports whether a connection to ip reaches l.
func (l *listenerV) accepts(ip netip.Addr) bool {
	switch {
	case l.any:
		return true
	case l.ip.IsUnspecified():
		return l.ip.Is4() == ip.Unmap().Is4()
	}
	return l.ip == ip.Unmap()
}

// exchangeV is the Exchange of a buffered request: a stream is not run.
type exchangeV struct{}

func (*exchangeV) describe() string { return "an http.Exchange" }

func (*exchangeV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	return nil, fr.unsupported(at, "a streamed http response (%s) over the simulated network yet", name)
}

func init() {
	natives["http.checkOrigin"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args[:1])); ab != nil {
			return nil, ab
		}
		origin, limits := args[0].(string), args[1].(*record)
		if !limitsValid(limits) {
			return nil, httpFail(fn, fr, at, "InvalidLimits", limitsFields(limits))
		}
		if reason := originReason(origin); reason != "" {
			return nil, httpFail(fn, fr, at, "InvalidClient", map[string]Value{"Origin": origin, "Reason": reason})
		}
		return unit{}, nil
	}
	natives["http.serve"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		l, ok := args[0].(*listenerV)
		if !ok {
			return nil, fr.unsupported(at, "http.Serve on %s", describe(args[0]))
		}
		limits := args[1].(*record)
		if !limitsValid(limits) {
			return nil, httpFail(fn, fr, at, "InvalidLimits", limitsFields(limits))
		}
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		// Serve owns l: it closes it when it ends, however it ends.
		defer l.close()
		l.serving, l.limits = true, limits
		for {
			ab := in.block(fr, at, "a request to an http.Listener", func() bool { return len(l.pending) > 0 })
			if ab != nil {
				return nil, ab
			}
			x := l.pending[0]
			l.pending = l.pending[1:]
			r := x.req
			v, ab := in.callValue(fr, at, args[2], []Value{&exchangeV{}, r.method, r.path, netFields(fn, r.query), netFields(fn, r.header), r.body, r.tooLarge, r.readError})
			if ab != nil {
				return nil, ab
			}
			x.resp, x.done = v.(*record), true
		}
	}
	natives["http.send"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if in.host == nil {
			return nil, fr.unsupported(at, "a Go function (%s), even a pure one", fn.Name)
		}
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		return in.netSend(fr, at, fn, args)
	}
}

// netFields is fields as a []http.Field.
func netFields(fn *check.Func, fields []netField) Value {
	out := make([]Value, len(fields))
	for i, f := range fields {
		out[i] = stdRecord(fn, "Field", map[string]Value{"Name": f.name, "Value": f.value})
	}
	return newSlice(out)
}

func fieldsOf(v Value) []netField {
	var out []netField
	for _, e := range v.(sliceV).elems() {
		out = append(out, netField{fieldText(e, "Name"), fieldText(e, "Value")})
	}
	return out
}

// sortedFields is a Go header as std http's fields: names sorted, a
// repeated name's values in their order (runtime/v1 httpFields).
func sortedFields(h map[string][]string) []netField {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []netField
	for _, k := range keys {
		for _, v := range h[k] {
			out = append(out, netField{k, v})
		}
	}
	return out
}

// listen binds addr on the simulated host (http.Listen): a literal or
// empty host, port 0 choosing a free port from 32768, as Linux's
// ephemeral range starts.
func (h *host) listen(addr string) (*listenerV, string, bool) {
	hostPart, portPart, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", false
	}
	l := &listenerV{h: h}
	switch hostPart {
	case "":
		l.any = true
	default:
		ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(hostPart, "["), "]"))
		if err != nil || !(ip.IsLoopback() || ip.IsUnspecified()) {
			return nil, "", false
		}
		l.ip = ip.Unmap()
	}
	port, err := strconv.Atoi(portPart)
	if err != nil {
		if p, perr := net.LookupPort("tcp", portPart); perr == nil {
			port = p
		} else {
			return nil, "", false
		}
	}
	if h.listeners == nil {
		h.listeners = map[int][]*listenerV{}
	}
	if port == 0 {
		for port = 32768; len(h.listeners[port]) > 0; port++ {
		}
	}
	for _, o := range h.listeners[port] {
		if o.any || l.any || o.ip.IsUnspecified() && o.ip.Is4() == l.ip.Is4() || l.ip.IsUnspecified() && l.ip.Is4() == o.ip.Is4() || o.ip == l.ip {
			return nil, "listen tcp " + addr + ": bind: address already in use", true
		}
	}
	l.port = port
	switch {
	case l.any:
		l.addr = "[::]:" + strconv.Itoa(port)
	default:
		l.addr = netip.AddrPortFrom(l.ip, uint16(port)).String()
	}
	h.listeners[port] = append(h.listeners[port], l)
	return l, "", true
}

// pipeListener hands Go's server the one server end of an exchange.
type pipeListener struct {
	conn chan net.Conn
	done chan struct{}
	once sync.Once
}

func (p *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-p.conn:
		return c, nil
	case <-p.done:
		return nil, net.ErrClosed
	}
}

func (p *pipeListener) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

type clientResult struct {
	resp netResponse
	kind int
	why  string
	max  int64
	ok   bool
}

// netSend is http.send: the request to a listener of this process, as
// runtime/v1's HTTPDo sends it.
func (in *interp) netSend(fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
	origin, limits := args[0].(string), args[1].(*record)
	method, p := args[2].(string), args[3].(string)
	query, header, body := fieldsOf(args[4]), fieldsOf(args[5]), args[6].(string)
	failed := func(kind int, reason string, limit int64) *abrupt {
		return httpFail(fn, fr, at, "SendFailed", map[string]Value{"Kind": int64(kind), "Reason": reason, "Limit": limit})
	}
	if !httpToken(method) {
		return nil, failed(sendMalformed, "the method is not a token: "+method, 0)
	}
	if !strings.HasPrefix(p, "/") {
		return nil, failed(sendMalformed, "the path does not start with /: "+p, 0)
	}
	for _, f := range header {
		if !httpToken(f.name) || strings.ContainsAny(f.value, "\r\n\x00") {
			return nil, failed(sendMalformed, "the header field is not valid: "+f.name, 0)
		}
	}
	u, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil {
		return nil, failed(sendMalformed, err.Error(), 0)
	}
	if u.Scheme != "http" {
		return nil, fr.unsupported(at, "an https exchange: the simulated network has no TLS")
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]"))
	if err != nil || !ip.IsLoopback() {
		return nil, fr.unsupported(at, "an http exchange with %s: the simulated network has only this process's listeners", u.Host)
	}
	port := 80
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	var vals []string
	for _, q := range query {
		vals = append(vals, url.QueryEscape(q.name)+"="+url.QueryEscape(q.value))
	}
	u.Path, u.RawQuery = p, strings.Join(vals, "&")
	maxBody := limits.fields[fieldIndex(limits.t, "MaxBodyBytes")].(int64)
	maxHeader := limits.fields[fieldIndex(limits.t, "MaxHeaderBytes")].(int64)
	h := in.host
	var l *listenerV
	for _, c := range h.listeners[port] {
		if c.accepts(ip) {
			l = c
		}
	}
	if l == nil {
		raddr := net.TCPAddrFromAddrPort(netip.AddrPortFrom(ip, uint16(port)))
		e := &net.OpError{Op: "dial", Net: "tcp", Addr: raddr, Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		return nil, failed(0, e.Error(), 0)
	}
	// A request waits in the listen queue until Serve takes the listener.
	if ab := in.block(fr, at, "an http.Listener that serves", func() bool { return l.serving || l.closed }); ab != nil {
		return nil, ab
	}
	srvBody := l.limits.fields[fieldIndex(l.limits.t, "MaxBodyBytes")].(int64)
	srvHeader := l.limits.fields[fieldIndex(l.limits.t, "MaxHeaderBytes")].(int64)

	quit := make(chan struct{})
	captured := make(chan *netRequest, 1)
	result := make(chan clientResult, 1)
	clientEnd, serverEnd := net.Pipe()
	pl := &pipeListener{conn: make(chan net.Conn, 1), done: make(chan struct{})}
	pl.conn <- serverEnd
	srv := &http.Server{
		MaxHeaderBytes: int(srvHeader),
		ErrorLog:       log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values, _ := url.ParseQuery(r.URL.RawQuery)
			nr := &netRequest{method: r.Method, path: r.URL.Path, header: sortedFields(r.Header), query: sortedFields(values), reply: make(chan *netResponse, 1)}
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, srvBody))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					nr.tooLarge = true
				} else {
					nr.readError = err.Error()
				}
			} else {
				nr.body = string(data)
			}
			select {
			case captured <- nr:
			case <-quit:
				panic(http.ErrAbortHandler)
			}
			var resp *netResponse
			select {
			case resp = <-nr.reply:
			case <-quit:
				panic(http.ErrAbortHandler)
			}
			for _, f := range resp.header {
				w.Header().Add(f.name, f.value)
			}
			if resp.body == "" {
				w.Header()["Content-Type"] = nil
			}
			w.WriteHeader(resp.status)
			if resp.body != "" {
				_, _ = io.WriteString(w, resp.body)
			}
		}),
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(pl)
	}()
	go func() {
		result <- exchange(clientEnd, u, method, header, body, maxBody, maxHeader)
	}()
	finish := func() clientResult {
		r := <-result
		_ = srv.Close()
		_ = pl.Close()
		<-served
		return r
	}
	var got clientResult
	select {
	case nr := <-captured:
		x := &netExchange{req: nr}
		l.pending = append(l.pending, x)
		ab := in.block(fr, at, "an http response", func() bool { return x.done || l.closed })
		if ab != nil || !x.done {
			close(quit)
			finish()
			if ab != nil {
				return nil, ab
			}
			for i, y := range l.pending {
				if y == x {
					l.pending = append(l.pending[:i:i], l.pending[i+1:]...)
				}
			}
			return nil, failed(1, "EOF", 0)
		}
		nr.reply <- &netResponse{status: int(fieldInt(x.resp, "Status")), header: fieldsOf(x.resp.fields[fieldIndex(x.resp.t, "Header")]), body: x.resp.fields[fieldIndex(x.resp.t, "Body")].(string)}
		got = finish()
	case got = <-result:
		close(quit)
		_ = srv.Close()
		_ = pl.Close()
		<-served
	}
	if !got.ok {
		return nil, failed(got.kind, got.why, got.max)
	}
	for i, f := range got.resp.header {
		if f.name == "Date" {
			got.resp.header[i].value = h.now().Format(http.TimeFormat)
		}
	}
	out := stdRecord(fn, "Response", map[string]Value{"Status": int64(got.resp.status), "Header": netFields(fn, got.resp.header), "Body": got.resp.body})
	return out, in.allocate(fr, at, valueSize(out))
}

func fieldInt(r *record, name string) int64 { return r.fields[fieldIndex(r.t, name)].(int64) }

// exchange is runtime/v1's httpDoURL and httpExchange over conn: Go's
// transport with compression off and the client's MaxHeaderBytes, the
// body read to MaxBodyBytes.
func exchange(conn net.Conn, u *url.URL, method string, header []netField, body string, maxBody, maxHeader int64) clientResult {
	tr := &http.Transport{
		DisableCompression:     true,
		MaxResponseHeaderBytes: maxHeader,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return conn, nil
		},
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequest(method, (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), strings.NewReader(body))
	if err != nil {
		return clientResult{kind: sendMalformed, why: err.Error()}
	}
	req.URL = u
	if body == "" {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	for _, f := range header {
		req.Header.Add(f.name, f.value)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if strings.Contains(err.Error(), "server response headers exceeded") {
			return clientResult{kind: 2, why: "the response is over its MaxHeaderBytes budget", max: maxHeader}
		}
		return clientResult{kind: 1, why: err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(bufio.NewReader(resp.Body), maxBody+1))
	if err != nil {
		return clientResult{kind: 1, why: err.Error()}
	}
	if int64(len(data)) > maxBody {
		return clientResult{kind: 2, why: "the response is over its MaxBodyBytes budget", max: maxBody}
	}
	return clientResult{ok: true, resp: netResponse{status: resp.StatusCode, header: sortedFields(resp.Header), body: string(data)}}
}
