package v1

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"net/netip"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"

	"ergo.local/prototype/runtime/rt"
)

// HTTPSinkFailed is http.SinkFailed: a sink that refused a part of a body.
type HTTPSinkFailed struct{ Reason string }

func (e HTTPSinkFailed) Error() string { return "http.SinkFailed: " + e.Reason }

// BodyFile is http.BodyFile: the new file of one http.FetchFile. Only
// HTTPIntoFile makes one, and it is valid only while its fetch runs.
type BodyFile struct{ f *os.File }

// Put appends text to the new file. A failed write is SinkFailed, which
// ends the fetch.
//
//ergo:host
func (p *BodyFile) Put(_ *rt.Task, text string) error {
	if _, err := p.f.WriteString(text); err != nil {
		return HTTPSinkFailed{Reason: fsReason(err)}
	}
	return nil
}

// partOnly marks the runtime's type as the one http.BodyFile.
func (p *BodyFile) partOnly() {}

// HTTPIntoFile is the body of http.FetchFile. It makes a new file beside
// name below root (the caller's, borrowed for the call), runs fetch on the
// caller's task with that file, and, when fetch succeeds, syncs the file
// and renames it over name, so a reader sees the old file or the whole
// body, never a part. On every other exit (a failure of fetch, a body over
// its budget, a stop of t) the new file is closed and removed before the
// call returns, so no part of the body is left.
//
//ergo:host
func HTTPIntoFile(t *rt.Task, root *HostRoot, name string, fetch func(*rt.Task, *BodyFile) (HTTPResponse, error)) (HTTPResponse, error) {
	t.Check()
	if name == "" {
		return HTTPResponse{}, Failed{Path: name, Reason: "empty path"}
	}
	dir, base := path.Split(name)
	var (
		tmp string
		f   *os.File
		err error
	)
	for range 16 {
		tmp = dir + "." + base + ".part-" + strconv.FormatUint(rand.Uint64(), 36)
		f, err = root.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return HTTPResponse{}, rootErr(name, err)
	}
	kept := false
	defer func() {
		if !kept {
			_ = f.Close()
			_ = root.root.Remove(tmp)
		}
	}()
	res, err := fetch(t, &BodyFile{f: f})
	if err != nil {
		return HTTPResponse{}, err
	}
	if err := f.Sync(); err != nil {
		return HTTPResponse{}, rootErr(name, err)
	}
	if err := f.Close(); err != nil {
		return HTTPResponse{}, rootErr(name, err)
	}
	if err := root.root.Rename(tmp, name); err != nil {
		_ = root.root.Remove(tmp)
		kept = true // closed and removed here
		return HTTPResponse{}, rootErr(name, err)
	}
	kept = true
	return res, nil
}

type HTTPOriginRefused struct{ Origin string }

func (e HTTPOriginRefused) Error() string { return "http.OriginRefused: " + e.Origin }

type HTTPRedirectLimit struct {
	URL   string
	Limit int
}

func (e HTTPRedirectLimit) Error() string { return "http.RedirectLimit: " + e.URL }

// networkOrigin is an origin identity, not a hostname suffix: scheme,
// case-insensitive host and effective port must all agree. URLs with user
// information are refused instead of implicitly minting an Authorization.
func networkOrigin(u *url.URL) (string, error) {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", httpFailed(httpMalformed, "the URL must have an http(s) origin without user information")
	}
	host, port := u.Hostname(), u.Port()
	if strings.Contains(host, ":") {
		address, zone, zoned := strings.Cut(host, "%")
		ip, err := netip.ParseAddr(address)
		if err != nil || !ip.Is6() {
			return "", httpFailed(httpMalformed, "invalid IPv6 host")
		}
		host = ip.String()
		// Interface-zone identifiers are case sensitive, unlike DNS names.
		if zoned {
			host += "%" + zone
		}
	} else {
		host = strings.ToLower(host)
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", httpFailed(httpMalformed, "invalid URL port")
		}
		port = strconv.FormatUint(n, 10)
	}
	if port == "80" && u.Scheme == "http" || port == "443" && u.Scheme == "https" {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return (&url.URL{Scheme: u.Scheme, Host: host}).String(), nil
}

func HTTPCheckNetwork(origins []string, limits HTTPLimits) ([]string, error) {
	if limits.MaxBodyBytes <= 0 || limits.MaxHeaderBytes <= 0 {
		return nil, HTTPInvalidClient{"", "the response budget must be positive"}
	}
	out := make([]string, 0, len(origins))
	for _, origin := range origins {
		if err := HTTPCheckOrigin(origin, limits); err != nil {
			return nil, err
		}
		u, _ := url.Parse(origin)
		canonical, err := networkOrigin(u)
		if err != nil {
			return nil, HTTPInvalidClient{origin, "the origin is not valid"}
		}
		out = append(out, canonical)
	}
	return out, nil
}

func originAllowed(origin string, origins []string, anyOrigin bool) bool {
	if anyOrigin {
		return true
	}
	for _, allowed := range origins {
		if allowed == origin {
			return true
		}
	}
	return false
}

// HTTPFetch checks the initial and every resolved redirect origin before an
// exchange. Credentials are selected anew from their immutable owners on
// each hop, not copied from the preceding request (including an A-B-A chain).
// NoRedirects is -1; Follow(0) refuses the first hop. Limits apply per response.
//
//ergo:host
func HTTPFetch(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string) (HTTPResponse, error) {
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		return httpDoURL(t, u, limits, method, fields, body)
	})
}

// HTTPFetchInto is HTTPFetch with the final response's body given to put,
// a part at a time, in order, while it arrives (httpStreamURL); the
// returned response has an empty Body. The body of a redirect it follows
// is not read. A failure of put ends the call with that failure, after the
// exchange let go of its connection. limits.MaxBodyBytes bounds the bytes
// put is given, not memory: at most two parts are held at once.
//
//ergo:host
func HTTPFetchInto(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, put func(*rt.Task, string) error) (HTTPResponse, error) {
	follow := func(r HTTPResponse) bool { return hops != -1 && httpLocation(r) != "" }
	return httpFetch(t, origins, anyOrigin, hops, method, rawURL, header, owners, credentials, body, func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error) {
		return httpStreamURL(t, u, limits, method, fields, body, follow, put)
	})
}

// httpLocation is the Location of a redirect response, or "" when r is not
// one or has none.
func httpLocation(r HTTPResponse) string {
	if r.Status != 301 && r.Status != 302 && r.Status != 303 && r.Status != 307 && r.Status != 308 {
		return ""
	}
	for _, f := range r.Header {
		if strings.EqualFold(f.Name, "Location") {
			return f.Value
		}
	}
	return ""
}

// httpFetch is the policy loop of HTTPFetch and HTTPFetchInto; exchange
// sends one hop.
func httpFetch(t *rt.Task, origins []string, anyOrigin bool, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string, exchange func(u *url.URL, method string, fields []HTTPField, body string) (HTTPResponse, error)) (HTTPResponse, error) {
	t.Check()
	if !httpToken(method) || len(owners) != len(credentials) || hops < -1 {
		return HTTPResponse{}, httpFailed(httpMalformed, "invalid network request")
	}
	for _, f := range append(append([]HTTPField{}, header...), credentials...) {
		if !httpToken(f.Name) || strings.ContainsAny(f.Value, "\r\n\x00") {
			return HTTPResponse{}, httpFailed(httpMalformed, "invalid network header: "+f.Name)
		}
	}
	bound := make([]string, len(owners))
	for i, owner := range owners {
		u, err := url.Parse(owner)
		if err != nil {
			return HTTPResponse{}, httpFailed(httpMalformed, "invalid credential origin")
		}
		bound[i], err = networkOrigin(u)
		if err != nil {
			return HTTPResponse{}, err
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return HTTPResponse{}, httpFailed(httpMalformed, "invalid request URL")
	}
	for followed := 0; ; followed++ {
		t.Check()
		origin, err := networkOrigin(u)
		if err != nil {
			return HTTPResponse{}, err
		}
		if !originAllowed(origin, origins, anyOrigin) {
			return HTTPResponse{}, HTTPOriginRefused{origin}
		}
		fields := append([]HTTPField{}, header...)
		for i, f := range credentials {
			if bound[i] == origin {
				fields = append(fields, f)
			}
		}
		response, err := exchange(u, method, fields, body)
		if err != nil {
			return HTTPResponse{}, err
		}
		location := httpLocation(response)
		if location == "" || hops == -1 {
			return response, nil
		}
		if followed >= hops {
			return HTTPResponse{}, HTTPRedirectLimit{u.String(), hops}
		}
		next, err := url.Parse(location)
		if err != nil {
			return HTTPResponse{}, httpFailed(httpMalformed, "invalid redirect URL")
		}
		u = u.ResolveReference(next)
		// The usual Go/browser semantics: 303 switches to GET except HEAD;
		// 301/302 switch non-GET/HEAD requests. 307/308 keep method and body.
		if response.Status == 303 && method != "HEAD" || (response.Status == 301 || response.Status == 302) && method != "GET" && method != "HEAD" {
			method, body = "GET", ""
		}
	}
}
