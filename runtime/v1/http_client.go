package v1

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"ergo.local/prototype/runtime/rt"
)

// HTTPInvalidClient is http.InvalidClient: an origin that is not
// scheme://authority, or a budget that is not positive.
type HTTPInvalidClient struct{ Origin, Reason string }

func (e HTTPInvalidClient) Error() string {
	return "http.InvalidClient: " + e.Origin + ": " + e.Reason
}

// HTTPSendFailed is std http.SendFailed: the Kind of a failed
// exchange (httpRefused and the rest), which http.Send turns into the
// Cause of a RequestFailed. A Go type cannot be a variant of an Ergo
// union, so the causes cross as one record.
type HTTPSendFailed struct {
	Kind   int
	Reason string
	Limit  int
}

const (
	httpRefused = iota
	httpTransport
	httpTooLarge
	httpMalformed
)

func (e HTTPSendFailed) Error() string { return "http.RequestFailed: " + e.Reason }

func httpFailed(kind int, reason string) HTTPSendFailed {
	return HTTPSendFailed{Kind: kind, Reason: reason}
}

// HTTPCheckOrigin accepts scheme://authority with the scheme http or
// https and nothing after the authority (a lone "/" is allowed). It is
// pure: http.NewClient holds the authority to choose the host.
func HTTPCheckOrigin(origin string, limits HTTPLimits) error {
	if !limits.valid() {
		return HTTPInvalidLimits(limits)
	}
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return HTTPInvalidClient{origin, err.Error()}
	case u.Scheme != "http" && u.Scheme != "https":
		return HTTPInvalidClient{origin, "the scheme is not http or https"}
	case u.Host == "" || u.User != nil:
		return HTTPInvalidClient{origin, "the authority is not a host and port"}
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.ForceQuery:
		return HTTPInvalidClient{origin, "an origin has no path, query or fragment"}
	}
	return nil
}

// httpTransports holds one Go transport per response header budget, so
// clients with one budget share their idle connections.
var httpTransports sync.Map // int -> *http.Client

func httpClientFor(maxHeader int) *http.Client {
	if c, ok := httpTransports.Load(maxHeader); ok {
		return c.(*http.Client)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxResponseHeaderBytes = int64(maxHeader)
	// The body comes back as the peer sent it: no transparent gzip.
	tr.DisableCompression = true
	c := &http.Client{
		Transport: tr,
		// One request is one exchange: a redirect is a response.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	got, _ := httpTransports.LoadOrStore(maxHeader, c)
	return got.(*http.Client)
}

// httpToken reports whether s is an RFC 9110 token (a method or a field
// name).
func httpToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0 {
			continue
		}
		return false
	}
	return true
}

// httpQuery is fields in their order, each name=value escaped.
func httpQuery(fields []HTTPField) string {
	var b strings.Builder
	for i, f := range fields {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(f.Name))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(f.Value))
	}
	return b.String()
}

type httpDone struct {
	res HTTPResponse
	err error
}

// HTTPDo sends one request to origin and reads the whole response within
// limits. path is the decoded path, joined to origin; query and header
// keep their order. A refused connection is Refused; a response over a
// budget is TooLarge; a request Go cannot send is Malformed; any other
// failure of the exchange is Transport. A stop of t cancels the request,
// waits for its goroutine and ends the call with the stop. There is no
// timeout, retry or redirect of its own.
//
//ergo:host
func HTTPDo(t *rt.Task, origin string, limits HTTPLimits, method, path string, query, header []HTTPField, body string) (HTTPResponse, error) {
	u, err := httpRequestURL(t, origin, method, path, query, header)
	if err != nil {
		return HTTPResponse{}, err
	}
	return httpDoURL(t, u, limits, method, header, body)
}

// httpRequestURL checks a fixed-origin request (HTTPDo, HTTPDoInto) and
// joins path and query to origin.
func httpRequestURL(t *rt.Task, origin, method, path string, query, header []HTTPField) (*url.URL, error) {
	t.Check()
	if !httpToken(method) {
		return nil, httpFailed(httpMalformed, "the method is not a token: "+method)
	}
	if !strings.HasPrefix(path, "/") {
		return nil, httpFailed(httpMalformed, "the path does not start with /: "+path)
	}
	for _, f := range header {
		if !httpToken(f.Name) || strings.ContainsAny(f.Value, "\r\n\x00") {
			return nil, httpFailed(httpMalformed, "the header field is not valid: "+f.Name)
		}
	}
	u, err := url.Parse(strings.TrimSuffix(origin, "/"))
	if err != nil {
		return nil, httpFailed(httpMalformed, err.Error())
	}
	u.Path, u.RawQuery = path, httpQuery(query)
	return u, nil
}

// httpDoURL is the shared stopped-and-joined exchange for fixed and dynamic
// origins. Its client never follows redirects implicitly.
func httpDoURL(t *rt.Task, u *url.URL, limits HTTPLimits, method string, header []HTTPField, body string) (HTTPResponse, error) {
	t.Check()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The request is made for the origin and then takes u whole, so a
	// target u carries as Opaque (webURL.requestURL) is sent as it is.
	req, err := http.NewRequestWithContext(ctx, method, (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), strings.NewReader(body))
	if err != nil {
		return HTTPResponse{}, httpFailed(httpMalformed, err.Error())
	}
	req.URL = u
	if body == "" {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}
	got := make(chan httpDone, 1)
	go func() { got <- httpExchange(httpClientFor(limits.MaxHeaderBytes), req, limits) }()
	var d httpDone
	select {
	case d = <-got:
	case <-t.Done():
		cancel()
		<-got
		panic(rt.Interrupted)
	}
	return d.res, d.err
}

// httpExchange sends req and reads at most limits.MaxBodyBytes of body.
func httpExchange(c *http.Client, req *http.Request, limits HTTPLimits) httpDone {
	// The transport alone: Client.Do parses a 3xx's Location even when it
	// follows nothing, and fails the exchange on one url.Parse refuses
	// ("%zz"), where a redirect is a response the caller reads.
	resp, err := c.Transport.RoundTrip(req)
	if err != nil {
		return httpDone{err: httpDoErr(err, limits)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(limits.MaxBodyBytes)+1))
	if err != nil {
		return httpDone{err: httpFailed(httpTransport, err.Error())}
	}
	if len(body) > limits.MaxBodyBytes {
		return httpDone{err: httpBodyTooLarge(limits)}
	}
	return httpDone{res: HTTPResponse{Status: resp.StatusCode, Header: httpFields(resp.Header), Body: string(body)}}
}

// httpDoErr is the failure of a Go client Do that returned no response.
func httpDoErr(err error, limits HTTPLimits) error {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return httpFailed(httpRefused, err.Error())
	// net/http does not export this error; its text is stable.
	case strings.Contains(err.Error(), "server response headers exceeded"):
		return HTTPSendFailed{Kind: httpTooLarge, Reason: "the response is over its MaxHeaderBytes budget", Limit: limits.MaxHeaderBytes}
	}
	return httpFailed(httpTransport, err.Error())
}

func httpBodyTooLarge(limits HTTPLimits) error {
	return HTTPSendFailed{Kind: httpTooLarge, Reason: "the response is over its MaxBodyBytes budget", Limit: limits.MaxBodyBytes}
}

// httpPartBytes is the most bytes one part of a streamed body holds.
const httpPartBytes = 32 << 10

// httpEvent is one step of a streamed exchange: the head, a part of the
// body, the end of the body, or the failure that ends it.
type httpEvent struct {
	head *HTTPResponse
	// length is the head's declared body length, -1 when it has none.
	length int64
	part   string
	end    bool
	err    error
}

// HTTPStatusRefused is http.StatusRefused: a final status the caller does
// not accept. No part of its body reached the sink; Prefix is at most
// httpPrefixBytes of it.
type HTTPStatusRefused struct {
	Status int
	Prefix string
}

func (e HTTPStatusRefused) Error() string {
	return "http.StatusRefused: " + strconv.Itoa(e.Status)
}

// httpPrefixBytes bounds StatusRefused's Prefix (http.refusedPrefix
// states the same bound for the in-memory Net).
const httpPrefixBytes = 1024

// HTTPDoInto is the body of http.Client's SendInto: HTTPDo's request,
// with httpStreamURL's streamed body. A redirect is a response, so its
// status meets accepts like any other.
//
//ergo:host
func HTTPDoInto(t *rt.Task, origin string, limits HTTPLimits, method, path string, query, header []HTTPField, body string, accepts func(int) bool, put func(*rt.Task, string) error) (HTTPResponse, error) {
	u, err := httpRequestURL(t, origin, method, path, query, header)
	if err != nil {
		return HTTPResponse{}, err
	}
	return httpStreamURL(t, u, limits, method, header, body, func(HTTPResponse) bool { return false }, accepts, put)
}

// httpStreamURL is httpDoURL for a body that goes to put. A goroutine
// reads the body and hands each part to the caller's task over an
// unbuffered channel, so at most two parts are in memory, and put runs on
// the caller's task, in order. When follow(head) holds the body is not
// read: the response is a redirect the caller follows. When accepts does
// not take the head's status, put gets nothing: the call fails
// StatusRefused with at most httpPrefixBytes of the body. A Content-Length
// over the budget fails TooLarge before any part; a body that grows over
// it fails TooLarge before the part that crosses it. On every exit (the
// end, a failure, a failure of put, a stop of t) the request is cancelled
// and the goroutine joined before the call returns, so the connection is
// let go. The returned response has an empty Body.
func httpStreamURL(t *rt.Task, u *url.URL, limits HTTPLimits, method string, header []HTTPField, body string, follow func(HTTPResponse) bool, accepts func(int) bool, put func(*rt.Task, string) error) (HTTPResponse, error) {
	t.Check()
	ctx, cancel := context.WithCancel(context.Background())
	// As httpDoURL: made for the origin, then u whole, so an Opaque target
	// (webURL.requestURL) is sent as it is.
	req, err := http.NewRequestWithContext(ctx, method, (&url.URL{Scheme: u.Scheme, Host: u.Host}).String(), strings.NewReader(body))
	if err != nil {
		cancel()
		return HTTPResponse{}, httpFailed(httpMalformed, err.Error())
	}
	req.URL = u
	if body == "" {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	for _, f := range header {
		req.Header.Add(f.Name, f.Value)
	}
	events := make(chan httpEvent)
	go httpStreamRead(ctx, httpClientFor(limits.MaxHeaderBytes), req, limits, events)
	// The join: cancel, then take every event until the reader closes the
	// channel. It runs on every exit, a panic of put or of a stop included.
	defer func() {
		cancel()
		for range events {
		}
	}()
	var head HTTPResponse
	for {
		var e httpEvent
		select {
		case e = <-events:
		case <-t.Done():
			panic(rt.Interrupted)
		}
		switch {
		case e.err != nil:
			return HTTPResponse{}, e.err
		case e.head != nil:
			head = *e.head
			if follow(head) {
				return head, nil
			}
			if !accepts(head.Status) {
				return HTTPResponse{}, httpRefusedStatus(t, head.Status, events)
			}
			if e.length > int64(limits.MaxBodyBytes) {
				return HTTPResponse{}, httpBodyTooLarge(limits)
			}
		case e.end:
			return head, nil
		default:
			if err := put(t, e.part); err != nil {
				return HTTPResponse{}, err
			}
		}
	}
}

// httpRefusedStatus reads at most httpPrefixBytes of a refused body from
// events, for StatusRefused's message. The reader's own end, failure or
// budget ends the prefix early; the status is the failure either way.
func httpRefusedStatus(t *rt.Task, status int, events <-chan httpEvent) error {
	var prefix []byte
	for len(prefix) < httpPrefixBytes {
		var (
			e    httpEvent
			open bool
		)
		select {
		case e, open = <-events:
		case <-t.Done():
			panic(rt.Interrupted)
		}
		if !open || e.err != nil || e.end {
			break
		}
		prefix = append(prefix, e.part[:min(len(e.part), httpPrefixBytes-len(prefix))]...)
	}
	return HTTPStatusRefused{Status: status, Prefix: string(prefix)}
}

// httpStreamRead is the reader of httpStreamURL. It closes events when it
// ends; a cancel of ctx ends every send and the read under way.
func httpStreamRead(ctx context.Context, c *http.Client, req *http.Request, limits HTTPLimits, events chan<- httpEvent) {
	defer close(events)
	send := func(e httpEvent) bool {
		select {
		case events <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// The transport alone, as httpExchange: a 3xx's Location is the
	// caller's to read, not Client.Do's to parse.
	resp, err := c.Transport.RoundTrip(req)
	if err != nil {
		send(httpEvent{err: httpDoErr(err, limits)})
		return
	}
	defer resp.Body.Close()
	if !send(httpEvent{head: &HTTPResponse{Status: resp.StatusCode, Header: httpFields(resp.Header)}, length: resp.ContentLength}) {
		return
	}
	buf := make([]byte, httpPartBytes)
	total := 0
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			total += n
			if total > limits.MaxBodyBytes {
				send(httpEvent{err: httpBodyTooLarge(limits)})
				return
			}
			if !send(httpEvent{part: string(buf[:n])}) {
				return
			}
		}
		switch {
		case err == io.EOF:
			send(httpEvent{end: true})
			return
		case err != nil:
			send(httpEvent{err: httpFailed(httpTransport, err.Error())})
			return
		}
	}
}
