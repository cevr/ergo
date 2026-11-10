package v1

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

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

func HTTPCheckNetwork(origins []string, limits HTTPLimits) ([]string, error) {
	if !limits.valid() {
		return nil, HTTPInvalidLimits(limits)
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
		response, err := httpDoURL(t, u, limits, method, fields, body)
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
