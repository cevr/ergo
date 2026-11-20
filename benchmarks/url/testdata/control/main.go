// Command control is urlbench's hand Go twin (benchmarks/url/cmd/urlbench):
// the same URLs, run as `control bench KIND N`. KIND parse reads each with
// Go's url.Parse (the control); parsefloor reads each in one pass into a
// value whose parts are spans of the text, with no allocation (the floor:
// what a URL with no escape needs on the Go runtime). The floor hands a
// text with an escape to url.Parse; the workload has none.
package main

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
)

var urls = []string{
	"https://example.com/docs/a/b?x=1&y=two#top",
	"http://user:pw@host.example:8080/p/q",
	"/search?q=go&page=2",
	"http://[2001:db8::1]:443/index.html",
	"mailto:someone@example.com",
	"../up/./here?k=v",
	"https://a.b.c.example.org/very/long/path/with/many/segments/to/read",
	"ftp://files.example.net/pub/file.tar.gz",
}

func parsed(s string) int {
	u, err := url.Parse(s)
	if err != nil {
		return -1
	}
	n := len(u.Scheme) + len(u.Opaque) + len(u.Host) + len(u.Path) + len(u.RawQuery) + len(u.Fragment)
	if u.User != nil {
		n++
	}
	return n
}

// span is a parsed URL with no escape: each part a piece of the text.
type span struct {
	scheme, opaque, user, host, path, query, fragment string
	hasUser                                           bool
}

// floor parses s in one pass with no allocation, or is false when s needs
// more than spans (an escape, a control byte, an upper-case scheme) or is
// refused.
func floor(s string) (span, bool) {
	var u span
	schemeEnd := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < ' ' || c == 0x7f || c == '%' {
			return u, false
		}
		if schemeEnd < 0 && c == ':' && i > 0 {
			schemeEnd = i
		}
	}
	rest := s
	if schemeEnd > 0 {
		for i := 0; i < schemeEnd; i++ {
			c := s[i]
			if !('a' <= c && c <= 'z' || i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.')) {
				schemeEnd = -1
				break
			}
		}
	}
	if schemeEnd > 0 {
		u.scheme, rest = s[:schemeEnd], s[schemeEnd+1:]
	}
	if h := strings.IndexByte(rest, '#'); h >= 0 {
		u.fragment, rest = rest[h+1:], rest[:h]
	}
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		u.query, rest = rest[q+1:], rest[:q]
	}
	if len(rest) == 0 || rest[0] != '/' {
		if u.scheme != "" {
			u.opaque = rest
			return u, true
		}
		seg := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			seg = rest[:i]
		}
		if strings.IndexByte(seg, ':') >= 0 {
			return u, false
		}
	}
	if len(rest) >= 2 && rest[0] == '/' && rest[1] == '/' {
		auth := rest[2:]
		rest = ""
		if i := strings.IndexByte(auth, '/'); i >= 0 {
			auth, rest = auth[:i], auth[i:]
		}
		if at := strings.LastIndexByte(auth, '@'); at >= 0 {
			u.user, u.hasUser, auth = auth[:at], true, auth[at+1:]
		}
		host := auth
		if len(host) > 0 && host[0] == '[' {
			end := strings.LastIndexByte(host, ']')
			if end < 0 {
				return u, false
			}
			a, err := netip.ParseAddr(host[1:end])
			if err != nil || a.Is4() {
				return u, false
			}
			host = host[end+1:]
		} else if c := strings.IndexByte(host, ':'); c >= 0 {
			host = host[c:]
		} else {
			host = ""
		}
		if host != "" {
			if host[0] != ':' {
				return u, false
			}
			for i := 1; i < len(host); i++ {
				if host[i] < '0' || host[i] > '9' {
					return u, false
				}
			}
		}
		u.host = auth
	}
	u.path = rest
	return u, true
}

func floorParsed(s string) int {
	u, ok := floor(s)
	if !ok {
		return parsed(s)
	}
	n := len(u.scheme) + len(u.opaque) + len(u.host) + len(u.path) + len(u.query) + len(u.fragment)
	if u.hasUser {
		n++
	}
	return n
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	t := 0
	for i := 0; i < n; i++ {
		if os.Args[2] == "parsefloor" {
			t += floorParsed(urls[i&7])
		} else {
			t += parsed(urls[i&7])
		}
	}
	fmt.Println(t)
}
