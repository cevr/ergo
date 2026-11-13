package v1

import (
	"net/http"
	"net/url"
	"strconv"

	"ergo.local/prototype/runtime/rt"
)

// httpLocalHandle is a local Network's server: it answers one exchange
// with the request's origin and the request as HTTPServe gives a handler
// (method, decoded path, parsed query, header, body).
type httpLocalHandle = func(t *rt.Task, origin, method, path string, query, header []HTTPField, body string) HTTPResponse

// HTTPFetchLocal is HTTPFetch with handle in place of the host: the same
// policy loop (origins, credentials at their own origin, redirects and
// their hop limit), and each exchange is httpLocalExchange, a call of
// handle on the caller's task. No socket is opened.
//
//ergo:wait caller
func HTTPFetchLocal(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse) (HTTPResponse, error) {
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		r, _, err := httpLocalExchange(t, u, method, fields, body, handle)
		if err == nil && len(r.Body) > limits.MaxBodyBytes {
			return HTTPResponse{}, httpBodyTooLarge(limits)
		}
		return r, err
	})
}

// HTTPFetchLocalInto is HTTPFetchInto over handle: a redirect it follows
// is returned whole; a final status accepts refuses fails StatusRefused
// with the body's first httpPrefixBytes; a body over limits.MaxBodyBytes
// fails TooLarge before any part, as a declared length does on the host;
// otherwise put gets the body in one part (none when it is empty) and the
// response has an empty Body.
//
//ergo:wait caller
func HTTPFetchLocalInto(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, accepts func(int) bool, put func(*rt.Task, string) error, handle func(*rt.Task, string, string, string, []HTTPField, []HTTPField, string) HTTPResponse) (HTTPResponse, error) {
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		r, declared, err := httpLocalExchange(t, u, method, fields, body, handle)
		if err != nil || hops != -1 && httpLocation(r) != "" {
			return r, err
		}
		if !accepts(r.Status) {
			// The prefix is read from the parts within the budget, as
			// httpRefusedStatus reads it: the one part of a body over the
			// budget gives none.
			prefix := r.Body[:min(len(r.Body), httpPrefixBytes)]
			if len(r.Body) > limits.MaxBodyBytes {
				prefix = ""
			}
			return HTTPResponse{}, HTTPStatusRefused{Status: r.Status, Prefix: prefix}
		}
		if len(r.Body) > limits.MaxBodyBytes || declared > limits.MaxBodyBytes {
			return HTTPResponse{}, httpBodyTooLarge(limits)
		}
		if r.Body != "" {
			if err := put(t, r.Body); err != nil {
				return HTTPResponse{}, err
			}
		}
		r.Body = ""
		return r, nil
	})
}

// httpLocalExchange is one exchange of a local Network: what Go's client
// would send to target, read as HTTPServe's ServeHTTP reads it, given to
// handle, and its answer as the client would read it back. The request
// target is parsed from target.RequestURI() as Go's server parses a
// request line; header names take their canonical form and order
// (httpFields of an http.Header). The answer keeps HTTPServe's rules:
// no body for HEAD, 1xx, 204 and 304, and a status outside 100-999 ends
// the exchange (Transport), as a server whose handler writes one closes
// the connection. The headers Go's transports add (User-Agent,
// Accept-Encoding, Content-Length, Content-Type, Date) are not added.
// declared is the body length HTTPServe's net/http declares, -1 for none:
// a body it may send, written whole before the handler ends, declares its
// length when it fits the server's first buffer (bufferBeforeChunkingSize,
// 2048 bytes), HEAD's too, where the client reads no body.
func httpLocalExchange(t *rt.Task, target *url.URL, method string, fields []HTTPField, body string, handle httpLocalHandle) (out HTTPResponse, declared int, err error) {
	t.Check()
	origin, err := networkOrigin(&url.URL{Scheme: target.Scheme, Host: target.Host})
	if err != nil {
		return HTTPResponse{}, -1, err
	}
	in, err := url.ParseRequestURI(target.RequestURI())
	if err != nil {
		return HTTPResponse{}, -1, httpFailed(httpMalformed, err.Error())
	}
	sent := http.Header{}
	for _, f := range fields {
		sent.Add(f.Name, f.Value)
	}
	r := handle(t, origin, method, in.Path, HTTPParseQuery(in.RawQuery), httpFields(sent), body)
	if r.Status < 100 || r.Status > 999 {
		return HTTPResponse{}, -1, httpFailed(httpTransport, "the handler answered the invalid status "+strconv.Itoa(r.Status))
	}
	got := http.Header{}
	for _, f := range r.Header {
		got.Add(f.Name, f.Value)
	}
	out = HTTPResponse{Status: r.Status, Header: httpFields(got), Body: r.Body}
	if r.Status < 200 && r.Status != 101 {
		// net/http sends an informational status as an interim answer;
		// the answer the client reads is the 200 that follows, with the
		// body.
		out.Status = 200
	}
	declared = -1
	if out.Status < 200 || out.Status == 204 || out.Status == 304 {
		out.Body = ""
	} else if r.Body != "" && len(r.Body) <= 2048 {
		declared = len(r.Body)
	}
	if method == "HEAD" {
		out.Body = ""
	}
	return out, declared, nil
}
