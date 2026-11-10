package v1

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/runtime/rt"
)

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

// webURL is an absolute http(s) URL as the WHATWG URL Standard parses it
// (S-target). Go's url.Parse refuses a path that holds "%zz" and its
// EscapedPath re-escapes a path that holds "|" (and decodes "%2F"), so a
// request built from it is not the resource the URL names. Here only the
// authority goes through url.Parse, as before, so the origin, its policy,
// the credentials bound to it and the Host header do not change; the
// request target is the URL's path and query as the standard serializes
// them, and the client sends those bytes as they are.
type webURL struct {
	origin *url.URL // scheme and host only
	path   []string // segments, percent-encoded as the standard says
	query  *string  // nil when the URL has no "?"
}

// target is the request target: the serialized path and query.
func (w webURL) target() string {
	t := "/" + strings.Join(w.path, "/")
	if w.query != nil {
		t += "?" + *w.query
	}
	return t
}

func (w webURL) String() string { return w.origin.String() + w.target() }

// requestURL is the URL Go's client sends: the origin, and the target as
// Opaque, which RequestURI writes as it is. Go writes an Opaque that starts
// with "//" in absolute form, so such a path goes through RawPath, which
// Go keeps only when it is valid escaped text; a "//" path that is not is
// refused (Malformed), never sent re-escaped.
func (w webURL) requestURL() (*url.URL, error) {
	u := &url.URL{Scheme: w.origin.Scheme, Host: w.origin.Host}
	path := "/" + strings.Join(w.path, "/")
	if strings.HasPrefix(path, "//") {
		p, err := url.PathUnescape(path)
		u.Path, u.RawPath = p, path
		if err != nil || u.EscapedPath() != path {
			return nil, httpFailed(httpMalformed, "Go's client cannot send the request target "+w.target())
		}
	} else {
		u.Opaque = path
	}
	if w.query != nil {
		u.RawQuery, u.ForceQuery = *w.query, *w.query == ""
	}
	return u, nil
}

// webClean is the standard's input cleanup: invalid UTF-8 bytes read as
// U+FFFD each, leading and trailing C0 controls and spaces are removed,
// and every tab and newline is removed.
func webClean(s string) string {
	if !utf8.ValidString(s) {
		var b strings.Builder
		for i := 0; i < len(s); {
			r, n := utf8.DecodeRuneInString(s[i:])
			b.WriteRune(r)
			i += n
		}
		s = b.String()
	}
	s = strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
	return strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
}

// webPathSet and webQuerySet are the ASCII printable bytes the standard's
// path and special-query percent-encode sets add to the C0 control set.
const (
	webPathSet  = " \"#<>?^`{}"
	webQuerySet = " \"#<>'"
)

// webEncode percent-encodes s: a C0 control, DEL, a byte of a non-ASCII
// character and a byte of set become %XX; every other byte, "%" included,
// stays as it is, so an escape the URL holds (valid or not) is kept.
func webEncode(s, set string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || strings.IndexByte(set, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func webSingleDot(seg string) bool { return seg == "." || strings.EqualFold(seg, "%2e") }

func webDoubleDot(seg string) bool {
	switch strings.ToLower(seg) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

// webPath is the standard's path state for a special URL: path (after its
// first slash) is split at "/" and "\", each segment is encoded, "." and
// ".." (in any spelling with %2e) are removed with the segment before
// them, and a dot segment at the end leaves an empty last segment.
func webPath(segs []string, path string) []string {
	segs = slices.Clone(segs)
	start := 0
	for i := 0; i <= len(path); i++ {
		if i < len(path) && path[i] != '/' && path[i] != '\\' {
			continue
		}
		seg, slash := path[start:i], i < len(path)
		start = i + 1
		switch {
		case webDoubleDot(seg):
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
			if !slash {
				segs = append(segs, "")
			}
		case webSingleDot(seg):
			if !slash {
				segs = append(segs, "")
			}
		default:
			segs = append(segs, webEncode(seg, webPathSet))
		}
	}
	return segs
}

// webRest reads the path and query of rest, the part of a URL after its
// authority, onto base (nil: an absolute path). A fragment is dropped.
func webRest(origin *url.URL, base []string, rest string) webURL {
	rest, _, _ = strings.Cut(rest, "#")
	path, query, hasQuery := strings.Cut(rest, "?")
	w := webURL{origin: origin}
	if hasQuery {
		q := webEncode(query, webQuerySet)
		w.query = &q
	}
	switch {
	case base != nil && path == "":
		// A query alone keeps the base path.
		w.path = slices.Clone(base)
		return w
	case base == nil && (strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\")):
		path = path[1:]
	}
	w.path = webPath(base, path)
	return w
}

// webParse parses raw, an absolute http(s) URL whose scheme is followed by
// two slashes ("/" or "\"). The authority, up to the next "/", "\", "?" or
// "#", is parsed by url.Parse as before; networkOrigin checks it.
func webParse(raw string) (webURL, error) {
	raw = webClean(raw)
	scheme, after, ok := strings.Cut(raw, ":")
	scheme = strings.ToLower(scheme)
	if !ok || (scheme != "http" && scheme != "https") || len(after) < 2 || !strings.ContainsRune("/\\", rune(after[0])) || !strings.ContainsRune("/\\", rune(after[1])) {
		return webURL{}, httpFailed(httpMalformed, "the URL must have an http(s) origin without user information")
	}
	after = after[2:]
	end := strings.IndexAny(after, "/\\?#")
	if end < 0 {
		end = len(after)
	}
	origin, err := url.Parse(scheme + "://" + after[:end])
	if err != nil {
		return webURL{}, httpFailed(httpMalformed, "invalid request URL")
	}
	return webRest(origin, nil, after[end:]), nil
}

// resolve is the standard's parse of location against w, for a special
// URL: a scheme makes it absolute; two slashes keep only the scheme; one
// slash keeps the origin; "?" keeps the path; "#" keeps path and query;
// any other text replaces the last segment of the path.
func (w webURL) resolve(location string) (webURL, error) {
	location = webClean(location)
	if i := strings.IndexAny(location, ":/\\?#"); i > 0 && location[i] == ':' && webScheme(location[:i]) {
		rest := location[i+1:]
		// The base's own scheme without two slashes is relative
		// ("http:x"), as the standard's special relative state says.
		if !strings.EqualFold(location[:i], w.origin.Scheme) || strings.HasPrefix(rest, "//") || strings.HasPrefix(rest, "\\\\") || strings.HasPrefix(rest, "/\\") || strings.HasPrefix(rest, "\\/") {
			return webParse(location)
		}
		location = rest
	}
	slash := func(i int) bool { return len(location) > i && (location[i] == '/' || location[i] == '\\') }
	switch {
	case slash(0) && slash(1):
		return webParse(w.origin.Scheme + ":" + location)
	case slash(0):
		return webRest(w.origin, nil, location), nil
	case strings.HasPrefix(location, "?"):
		return webRest(w.origin, w.path, location), nil
	case location == "" || strings.HasPrefix(location, "#"):
		return w, nil
	}
	return webRest(w.origin, w.path[:len(w.path)-1], location), nil
}

// webScheme reports whether s is a URL scheme: a letter, then letters,
// digits, "+", "-" and ".".
func webScheme(s string) bool {
	for i, c := range s {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return s != ""
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
	u, err := webParse(rawURL)
	if err != nil {
		return HTTPResponse{}, err
	}
	for followed := 0; ; followed++ {
		t.Check()
		origin, err := networkOrigin(u.origin)
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
		target, err := u.requestURL()
		if err != nil {
			return HTTPResponse{}, err
		}
		response, err := httpDoURL(t, target, limits, method, fields, body)
		if err != nil {
			return HTTPResponse{}, err
		}
		redirect := response.Status == 301 || response.Status == 302 || response.Status == 303 || response.Status == 307 || response.Status == 308
		location := ""
		for _, f := range response.Header {
			if strings.EqualFold(f.Name, "Location") {
				location = f.Value
				break
			}
		}
		if !redirect || location == "" || hops == -1 {
			return response, nil
		}
		if followed >= hops {
			return HTTPResponse{}, HTTPRedirectLimit{u.String(), hops}
		}
		u, err = u.resolve(location)
		if err != nil {
			return HTTPResponse{}, err
		}
		// The usual Go/browser semantics: 303 switches to GET except HEAD;
		// 301/302 switch non-GET/HEAD requests. 307/308 keep method and body.
		if response.Status == 303 && method != "HEAD" || (response.Status == 301 || response.Status == 302) && method != "GET" && method != "HEAD" {
			method, body = "GET", ""
		}
	}
}
