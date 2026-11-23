package lower

import (
	"math/rand/v2"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// stdGoURLDecls print NewClient's outcome and a Network policy's: its
// failure, or each canonical origin as the policy's handler sees a
// request to it (refused when the policy holds another form), sent
// through a network of the same origins under roomy limits so a small
// generated MaxBodyBytes does not fail the reply.
const stdGoURLDecls = `func clientText(origin string, limits http.Limits) string needs Ambient {
	let _ = http.NewClient(origin, limits, []http.Credential{}) catch {
	case http.InvalidClient as e:
		return "client " + strconv.Quote(e.Origin) + " " + strconv.Quote(e.Reason)
	case http.InvalidLimits:
		return "limits"
	}
	"ok"
}

func policyText(origins []string, limits http.Limits, canonical []string) string {
	let _ = http.InMemoryNetwork(http.Origins{Values: origins}, limits, http.NoRedirects, func(origin string, r http.Request) http.Response {
		http.Response{Status: 200, Header: []http.Field{}, Body: ""}
	}) catch {
	case http.InvalidClient as e:
		return "client " + strconv.Quote(e.Origin) + " " + strconv.Quote(e.Reason)
	case http.InvalidLimits:
		return "limits"
	}
	let roomy = http.Limits{MaxBodyBytes: 4096, MaxHeaderBytes: 4096, IdleTimeout: 1s, ReadHeaderTimeout: 1s, ReadBodyTimeout: 1s}
	let n = http.InMemoryNetwork(http.Origins{Values: origins}, roomy, http.NoRedirects, func(origin string, r http.Request) http.Response {
		http.Response{Status: 200, Header: []http.Field{}, Body: origin}
	}) catch {
	case http.InvalidClient, http.InvalidLimits:
		return "roomy limits refused"
	}
	let mut out = "ok"
	for c in canonical {
		let r = n.SendURL(http.URLRequest{URL: c + "/", Method: "GET", Header: []http.Field{}, Credentials: []http.Credential{}, Body: ""}) catch {
		case http.OriginRefused:
			out = out + " refused"
			continue
		case http.RequestFailed, http.RedirectLimit:
			out = out + " failed"
			continue
		}
		out = out + " " + r.Body
	}
	out
}`

// urlPolicy is one generated policy: its origins, its byte budgets (a
// non-positive one is InvalidLimits) and whether ReadBodyTimeout is zero.
type urlPolicy struct {
	origins       []string
	body, header  int
	noBodyTimeout bool
}

// stdGoURLCases are NP4f-URL's moved http checks against the Go bodies
// they replaced, written out here: checkOrigin (through NewClient) and
// checkNetwork (through InMemoryNetwork's policy). Origins mix schemes in
// any case, user information, default, zero-padded and bad ports, IPv6
// with zones and IPv4-mapped, malformed escapes, paths, queries and
// fragments; budgets are positive, zero or negative. The policy's
// canonical origins are each sent a request, so a canonical form the
// Ergo body spells another way is refused. Mutants (std ledger):
// np4furl-*.
func stdGoURLCases(r *rand.Rand, n int) []bodyCase {
	text := np4fErgoString
	with := func(c bodyCase) bodyCase {
		c.decls = stdGoURLDecls
		return c
	}
	pick := func(r *rand.Rand, xs ...string) string { return xs[r.IntN(len(xs))] }
	origin := func(r *rand.Rand) string {
		host := pick(r, "example.com", "EXAMPLE.com", "Ex.Org", "127.0.0.1", "[::1]", "[0:0::1]", "[fe80::1%25eth0]", "[FE80::1%25En1]", "[::ffff:1.2.3.4]", "[zz::1]", "[1.2.3.4]", "a_b.test", "", "%zz", "h:")
		port := pick(r, "", "", ":80", ":443", ":8080", ":0080", ":0", ":65535", ":65536", ":x", ":")
		return pick(r, "http", "https", "HTTP", "ftp", "") + pick(r, "://", "://", ":/", "://user@", "://u:p@", ":") + host + port + pick(r, "", "", "/", "/p", "?q", "#f", "?")
	}
	limitsLit := func(body, header int, noBody bool) string {
		rb := "1s"
		if noBody {
			rb = "0s"
		}
		return "http.Limits{MaxBodyBytes: " + strconv.Itoa(body) + ", MaxHeaderBytes: " + strconv.Itoa(header) + ", IdleTimeout: 1s, ReadHeaderTimeout: 1s, ReadBodyTimeout: " + rb + "}"
	}
	genPolicy := func(r *rand.Rand) urlPolicy {
		p := urlPolicy{body: 1 + r.IntN(9), header: 1 + r.IntN(9)}
		for range r.IntN(4) {
			p.origins = append(p.origins, origin(r))
		}
		switch r.IntN(8) {
		case 0:
			p.body = -r.IntN(2)
		case 1:
			p.noBodyTimeout = true
		}
		return p
	}
	failText := func(err error) string {
		switch e := err.(type) {
		case oldInvalidClient:
			return "client " + strconv.Quote(e.origin) + " " + strconv.Quote(e.reason)
		case oldInvalidLimits:
			return "limits"
		}
		return "unexpected " + err.Error()
	}
	clientRef := func(p urlPolicy) string {
		o := ""
		if len(p.origins) > 0 {
			o = p.origins[0]
		}
		if err := oldCheckOrigin(o, p.limitsOK()); err != nil {
			return failText(err)
		}
		return "ok"
	}
	clientLit := func(p urlPolicy) string {
		o := ""
		if len(p.origins) > 0 {
			o = p.origins[0]
		}
		return text(o) + ", " + limitsLit(p.body, p.header, p.noBodyTimeout)
	}
	policyRef := func(p urlPolicy) string {
		cs, err := oldCheckNetwork(p.origins, p.limitsOK())
		if err != nil {
			return failText(err)
		}
		out := "ok"
		for _, c := range cs {
			out += " " + c
		}
		return out
	}
	policyLit := func(p urlPolicy) string {
		var os, cs []string
		for _, o := range p.origins {
			os = append(os, text(o))
		}
		if got, err := oldCheckNetwork(p.origins, p.limitsOK()); err == nil {
			for _, c := range got {
				cs = append(cs, text(c))
			}
		}
		return "[]string{" + strings.Join(os, ", ") + "}, " + limitsLit(p.body, p.header, p.noBodyTimeout) + ", []string{" + strings.Join(cs, ", ") + "}"
	}
	edges := []urlPolicy{
		{origins: []string{"http://example.com"}, body: 1, header: 1},
		{origins: []string{"HTTPS://EXAMPLE.com:0443/", "http://[FE80::1%25En1]:80", "https://[0:0::1]:0080"}, body: 1, header: 1},
		{origins: []string{"http://[::ffff:1.2.3.4]"}, body: 1, header: 1},
		{origins: []string{"http://%zz"}, body: 1, header: 1},
		{origins: []string{"http://u@x"}, body: 1, header: 1},
		{origins: []string{"http:x"}, body: 1, header: 1},
		{origins: []string{"http://x?"}, body: 1, header: 1},
		{origins: []string{"http://x:65536"}, body: 1, header: 1},
		{origins: []string{"http://:"}, body: 1, header: 1},
		{origins: []string{"http://x"}, body: 0, header: 1},
		{origins: []string{"http://x"}, body: 1, header: 1, noBodyTimeout: true},
		{origins: nil, body: 1, header: -1},
	}
	client := bodyRows("http.checkOrigin", []string{"http", "strconv"}, "origin string, limits http.Limits", "clientText(origin, limits)", edges, n, r, genPolicy, clientLit, clientRef)
	client.needs = "Ambient"
	return []bodyCase{
		with(client),
		with(bodyRows("http.checkNetwork", []string{"http", "strconv"}, "origins []string, limits http.Limits, canonical []string", "policyText(origins, limits, canonical)", edges, n, r, genPolicy, policyLit, policyRef)),
	}
}

// limitsOK is p's limits as the old bodies read them.
func (p urlPolicy) limitsOK() oldLimits {
	l := oldLimits{body: p.body, header: p.header, readBody: time.Second}
	if p.noBodyTimeout {
		l.readBody = 0
	}
	return l
}

// oldLimits are the five budgets of http.Limits that the old bodies read;
// the timeouts other than ReadBodyTimeout are 1s here.
type oldLimits struct {
	body, header int
	readBody     time.Duration
}

func (l oldLimits) valid() bool { return l.body > 0 && l.header > 0 && l.readBody > 0 }

type oldInvalidClient struct{ origin, reason string }

func (e oldInvalidClient) Error() string { return "http.InvalidClient: " + e.reason }

type oldInvalidLimits struct{}

func (oldInvalidLimits) Error() string { return "http.InvalidLimits" }

// oldCheckOrigin is runtime/v1/http's HTTPCheckOrigin before NP4f-URL.
func oldCheckOrigin(origin string, limits oldLimits) error {
	if !limits.valid() {
		return oldInvalidLimits{}
	}
	u, err := url.Parse(origin)
	switch {
	case err != nil:
		return oldInvalidClient{origin, err.Error()}
	case u.Scheme != "http" && u.Scheme != "https":
		return oldInvalidClient{origin, "the scheme is not http or https"}
	case u.Host == "" || u.User != nil:
		return oldInvalidClient{origin, "the authority is not a host and port"}
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.ForceQuery:
		return oldInvalidClient{origin, "an origin has no path, query or fragment"}
	}
	return nil
}

// oldCheckNetwork is runtime/v1/http's HTTPCheckNetwork before NP4f-URL.
func oldCheckNetwork(origins []string, limits oldLimits) ([]string, error) {
	if !limits.valid() {
		return nil, oldInvalidLimits{}
	}
	out := make([]string, 0, len(origins))
	for _, origin := range origins {
		if err := oldCheckOrigin(origin, limits); err != nil {
			return nil, err
		}
		u, _ := url.Parse(origin)
		canonical, ok := oldNetworkOrigin(u)
		if !ok {
			return nil, oldInvalidClient{origin, "the origin is not valid"}
		}
		out = append(out, canonical)
	}
	return out, nil
}

// oldNetworkOrigin is runtime/v1/http's networkOrigin, false where it
// fails.
func oldNetworkOrigin(u *url.URL) (string, bool) {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	host, port := u.Hostname(), u.Port()
	if strings.Contains(host, ":") {
		address, zone, zoned := strings.Cut(host, "%")
		ip, err := netip.ParseAddr(address)
		if err != nil || !ip.Is6() {
			return "", false
		}
		host = ip.String()
		if zoned {
			host += "%" + zone
		}
	} else {
		host = strings.ToLower(host)
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return "", false
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
	return (&url.URL{Scheme: u.Scheme, Host: host}).String(), true
}
