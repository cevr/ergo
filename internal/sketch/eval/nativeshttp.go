package eval

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std http's policy checks and the refusals a request meets before it
// reaches the network (ORACLE O23; std/http/network.ergo, http.ergo): a
// network policy's origins and limits, a URL whose origin the policy
// refuses, a malformed request, and an address Listen cannot bind. A
// bound listener and a Client's exchange with it run on the simulated
// loopback (nativesnet.go); a fetch from a network policy and a body
// file it fills are skips.

// sendMalformed is std http's SendFailed kind of a malformed request
// (client.ergo, causeOf).
const sendMalformed = 3

func init() {
	natives["http.checkNetwork"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		origins := args[0].(sliceV).elems()
		if ab := in.charge(fr, at, 1+argWork(args[:1])); ab != nil {
			return nil, ab
		}
		limits := args[1].(*record)
		if !limitsValid(limits) {
			return nil, httpFail(fn, fr, at, "InvalidLimits", limitsFields(limits))
		}
		out := make([]Value, 0, len(origins))
		for _, o := range origins {
			origin := o.(string)
			if reason := originReason(origin); reason != "" {
				return nil, httpFail(fn, fr, at, "InvalidClient", map[string]Value{"Origin": origin, "Reason": reason})
			}
			u, _ := url.Parse(origin)
			canonical, why := canonicalOrigin(u)
			if why != "" {
				return nil, httpFail(fn, fr, at, "InvalidClient", map[string]Value{"Origin": origin, "Reason": "the origin is not valid"})
			}
			out = append(out, canonical)
		}
		return newSlice(out), in.allocate(fr, at, valueSize(newSlice(out)))
	}
	hostNative("http.Listen", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		addr := args[0].(string)
		// Only a literal or empty host is resolved without a lookup,
		// which would read the host's resolver.
		if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
			if _, perr := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")); perr != nil {
				return nil, fr.unsupported(at, "a listen address with a host name (%s)", addr)
			}
		}
		if _, err := net.ResolveTCPAddr("tcp", addr); err != nil {
			reason := (&net.OpError{Op: "listen", Net: "tcp", Err: err}).Error()
			return nil, httpFail(fn, fr, at, "ListenFailed", map[string]Value{"Addr": addr, "Reason": reason})
		}
		l, reason, ok := h.listen(addr)
		switch {
		case !ok:
			return nil, fr.unsupported(at, "a listen address the simulated network does not bind (%s)", addr)
		case reason != "":
			return nil, httpFail(fn, fr, at, "ListenFailed", map[string]Value{"Addr": addr, "Reason": reason})
		}
		return l, nil
	})
	fetch := func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		origins := textsOf(args[0])
		anyOrigin := args[1].(bool)
		hops := args[3].(int64)
		method, raw := args[4].(string), args[5].(string)
		header, owners, credentials := args[6].(sliceV).elems(), textsOf(args[7]), args[8].(sliceV).elems()
		malformed := func(reason string) *abrupt {
			return httpFail(fn, fr, at, "SendFailed", map[string]Value{"Kind": int64(sendMalformed), "Reason": reason, "Limit": int64(0)})
		}
		if !httpToken(method) || len(owners) != len(credentials) || hops < -1 {
			return nil, malformed("invalid network request")
		}
		for _, f := range append(append([]Value{}, header...), credentials...) {
			name, value := fieldText(f, "Name"), fieldText(f, "Value")
			if !httpToken(name) || strings.ContainsAny(value, "\r\n\x00") {
				return nil, malformed("invalid network header: " + name)
			}
		}
		for _, owner := range owners {
			u, err := url.Parse(owner)
			if err != nil {
				return nil, malformed("invalid credential origin")
			}
			if _, why := canonicalOrigin(u); why != "" {
				return nil, malformed(why)
			}
		}
		u, why := urlOrigin(raw)
		if why != "" {
			return nil, malformed(why)
		}
		origin, why := canonicalOrigin(u)
		if why != "" {
			return nil, malformed(why)
		}
		allowed := anyOrigin
		for _, o := range origins {
			allowed = allowed || o == origin
		}
		if !allowed {
			return nil, httpFail(fn, fr, at, "OriginRefused", map[string]Value{"Origin": origin})
		}
		return nil, fr.unsupported(at, "an http exchange: the simulated process has no network")
	}
	hostNative("http.fetch", fetch)
	hostNative("http.fetchInto", fetch)
	// IntoFile runs the fetch with a file it fills; a fetch that fails
	// before its first part leaves no file. A filled file is not run yet.
	hostNative("http.IntoFile", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if args[1].(string) == "" {
			return nil, fr.unsupported(at, "an http.IntoFile of an empty path")
		}
		if _, ok := args[0].(*hostRootV); !ok {
			return nil, fr.unsupported(at, "an http.IntoFile below %s", describe(args[0]))
		}
		if _, ab := in.callValue(fr, at, args[2], []Value{&bodyFileV{}}); ab != nil {
			return nil, ab
		}
		return nil, fr.unsupported(at, "the file an http.IntoFile fetch filled")
	})
}

// bodyFileV is the BodyFile of one IntoFile: a part it is given is not
// written yet.
type bodyFileV struct{}

func (*bodyFileV) describe() string { return "an http.BodyFile" }

func (*bodyFileV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	return nil, fr.unsupported(at, "a part an http.BodyFile writes (%s) yet", name)
}

func httpFail(fn *check.Func, fr *frame, at syntax.Node, kind string, fields map[string]Value) *abrupt {
	return fr.abrupt(ctlFail, at, stdRecord(fn, kind, fields), "")
}

func textsOf(v Value) []string {
	var out []string
	for _, e := range v.(sliceV).elems() {
		out = append(out, e.(string))
	}
	return out
}

func fieldText(v Value, name string) string {
	r := v.(*record)
	return r.fields[fieldIndex(r.t, name)].(string)
}

var limitNames = []string{"MaxBodyBytes", "MaxHeaderBytes", "IdleTimeout", "ReadHeaderTimeout", "ReadBodyTimeout"}

// limitsValid: every limit is positive (http.ergo, Limits).
func limitsValid(r *record) bool {
	for _, n := range limitNames {
		if r.fields[fieldIndex(r.t, n)].(int64) <= 0 {
			return false
		}
	}
	return true
}

func limitsFields(r *record) map[string]Value {
	out := map[string]Value{}
	for _, n := range limitNames {
		out[n] = r.fields[fieldIndex(r.t, n)]
	}
	return out
}

// originReason is why origin is not scheme://authority of http or https,
// or "" (http.ergo, NewClient's origin rule).
func originReason(origin string) string {
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return err.Error()
	case u.Scheme != "http" && u.Scheme != "https":
		return "the scheme is not http or https"
	case u.Host == "" || u.User != nil:
		return "the authority is not a host and port"
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.ForceQuery:
		return "an origin has no path, query or fragment"
	}
	return ""
}

// canonicalOrigin is u's origin as the policy compares it: a lower-case
// name or a canonical IP, and the port only when it is not the scheme's
// default; why says what is malformed.
func canonicalOrigin(u *url.URL) (origin, why string) {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", "the URL must have an http(s) origin without user information"
	}
	host, port := u.Hostname(), u.Port()
	if strings.Contains(host, ":") {
		addr, zone, zoned := strings.Cut(host, "%")
		ip, err := netip.ParseAddr(addr)
		if err != nil || !ip.Is6() {
			return "", "invalid IPv6 host"
		}
		host = ip.String()
		if zoned {
			host += "%" + zone
		}
		host = "[" + host + "]"
	} else {
		host = strings.ToLower(host)
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", "invalid URL port"
		}
		port = strconv.FormatUint(n, 10)
	}
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return (&url.URL{Scheme: u.Scheme, Host: host}).String(), ""
}

// urlOrigin is the origin part of raw, an absolute http(s) URL as the
// WHATWG URL Standard reads it: its text trimmed of C0 controls and
// spaces, tabs and newlines dropped, the scheme followed by two slashes
// ("/" or "\"), and the authority up to the next "/", "\", "?" or "#".
func urlOrigin(raw string) (*url.URL, string) {
	if !utf8.ValidString(raw) {
		// Each byte of an invalid sequence is a U+FFFD of its own.
		var b strings.Builder
		for _, r := range raw {
			b.WriteRune(r)
		}
		raw = b.String()
	}
	raw = strings.TrimFunc(raw, func(r rune) bool { return r <= ' ' })
	raw = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(raw)
	scheme, after, ok := strings.Cut(raw, ":")
	scheme = strings.ToLower(scheme)
	slash := func(i int) bool { return len(after) > i && (after[i] == '/' || after[i] == '\\') }
	if !ok || (scheme != "http" && scheme != "https") || !slash(0) || !slash(1) {
		return nil, "the URL must have an http(s) origin without user information"
	}
	after = after[2:]
	end := strings.IndexAny(after, "/\\?#")
	if end < 0 {
		end = len(after)
	}
	u, err := url.Parse(scheme + "://" + after[:end])
	if err != nil {
		return nil, "invalid request URL"
	}
	return u, ""
}

// httpToken reports whether s is an HTTP token (RFC 9110, 5.6.2).
func httpToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}
