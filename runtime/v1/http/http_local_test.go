package http

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// localCase is one seeded exchange of TestHTTPLocalMatchesLoopback.
type localCase struct {
	method, path, rawQuery, body string
	header                       []HTTPField
	query                        []HTTPField // the fields rawQuery encodes (httpEncodeQuery), nil when it is written
	status                       int
	answer                       string
	answerHeader                 []HTTPField
	redirect                     bool // the first answer is a redirect to /final
	hops                         int
	limits                       HTTPLimits
	into                         bool
	accepted                     []int // nil: the 2xx rule
	failPut                      bool
	warm                         bool // the transport keeps a connection from an exchange before, so a closed one is retried
}

// localArrival is what a handler was given, without the fields Go's
// transport adds.
type localArrival struct {
	method, path string
	rawPath      string
	query        []HTTPField
	header       []HTTPField
	body         string
}

// localOutcome is what a caller sees of one fetch.
type localOutcome struct {
	Status   int
	Header   []HTTPField
	Body     string
	Parts    string
	Failure  string
	Arrivals []localArrival
}

var localTransportFields = map[string]bool{"Date": true}

func localWithout(fields []HTTPField) []HTTPField {
	var out []HTTPField
	for _, f := range fields {
		if !localTransportFields[http.CanonicalHeaderKey(f.Name)] {
			out = append(out, f)
		}
	}
	return out
}

// localFailure names a failure by its kind and the fields a caller reads.
func localFailure(err error) string {
	var sf HTTPSendFailed
	var sr HTTPStatusRefused
	var rl HTTPRedirectLimit
	var or HTTPOriginRefused
	switch {
	case err == nil:
		return ""
	case errors.As(err, &sf):
		if sf.Kind == httpTooLarge {
			return fmt.Sprintf("TooLarge %d", sf.Limit)
		}
		return fmt.Sprintf("SendFailed kind %d", sf.Kind)
	case errors.As(err, &sr):
		return fmt.Sprintf("StatusRefused %d %q", sr.Status, sr.Prefix)
	case errors.As(err, &rl):
		return fmt.Sprintf("RedirectLimit %d", rl.Limit)
	case errors.As(err, &or):
		return "OriginRefused " + or.Origin
	}
	return "other " + err.Error()
}

var errLocalSink = errors.New("sink refused")

// TestHTTPLocalMatchesLoopback is the law of the local Network
// (http.InMemoryNetwork): a fetch answered in memory gives the caller what
// the same fetch gives through HTTPServe on a loopback socket, and the
// handler is given the same request, its raw (escaped) path as the
// client wrote it included, and the target's origin. Seeded over
// methods, statuses (refused ones too), bodies, paths (with reserved bytes
// kept and escapes in either case), queries, headers
// around the header budget, redirects and their hop limits, body budgets,
// a fresh or a kept-alive connection (a closed kept-alive one is retried),
// and the streamed form with its accepted statuses and a failing sink.
// Which connection a request takes is an input it waits on as an event
// (localDials), never on time. Only Date is left out. A failing seed prints the case and both
// outcomes; ERGO_HTTP_LOCAL_SEED=n replays it. An encoded query
// (httpEncodeQuery) arrives as its fields. Mutants (runtime/v1 ledger):
// local-key-kept, local-answer-unlike-serve, local-header-budget-lost,
// local-no-keepalive, local-origin-raw, encode-query-unescaped; runtime/v1/http
// ledger: rawpath-client-ignored, rawpath-serve-decoded, rawpath-local-decoded.
func TestHTTPLocalMatchesLoopback(t *testing.T) {
	var (
		mu      sync.Mutex
		current localCase
		arrived []localArrival
		dials   *localDials
	)
	answer := func(method, path, rawPath string, query, header []HTTPField, body string) HTTPResponse {
		if path == "/warm" {
			// The warm answer waits until the client's write of the
			// request has returned, so its transport has heard the
			// request was written before it reads the answer, and keeps
			// the connection (see run).
			mu.Lock()
			wrote := dials.wrote
			mu.Unlock()
			<-wrote
			return HTTPResponse{Status: 200, Body: "warm"}
		}
		mu.Lock()
		defer mu.Unlock()
		c := current
		// An empty query is one Ergo value, whether Go holds it as nil or
		// as an empty slice (the serve and the pipe parse it apart).
		if len(query) == 0 {
			query = nil
		}
		arrived = append(arrived, localArrival{method, path, rawPath, query, localWithout(header), body})
		if c.redirect && path != "/final" {
			return HTTPResponse{Status: 307, Header: []HTTPField{{"Location", "/final"}}, Body: "moved"}
		}
		return HTTPResponse{Status: c.status, Header: c.answerHeader, Body: c.answer}
	}
	l, _ := startHTTPLimits(t, serveLimits(1<<20, 1<<16), func(_ *rt.Task, _ *Exchange, method, path, rawPath string, query, header []HTTPField, body string, _ bool, _ string) HTTPResponse {
		return answer(method, path, rawPath, query, header, body)
	})
	origin := "http://" + l.Addr()
	// The local handler is given the target's origin, which the loopback
	// handler reads from its listener.
	local := func(_ *rt.Task, at string, method, path, rawPath string, query, header []HTTPField, body string) HTTPResponse {
		if at != origin {
			t.Errorf("the local handler was given the origin %q for %q", at, origin)
		}
		return answer(method, path, rawPath, query, header, body)
	}
	// The generator reaches the two answers net/http decides that a model
	// would most easily miss: a header over its budget, and a status the
	// server refuses on a kept-alive connection, which the client retries.
	sawHeaderBudget, sawRetry := false, false
	seeds := make([]uint64, 1000)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_HTTP_LOCAL_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	task := rt.Root(t.Name())
	for _, seed := range seeds {
		c := localCaseOf(rand.New(rand.NewPCG(seed, 31)))
		// runOnce is one fetch of c, and whether c is warm and its request
		// still went out on a new connection (cold).
		runOnce := func(remote bool) (localOutcome, bool) {
			d := localFreshTransports(c.limits.MaxHeaderBytes)
			mu.Lock()
			current, arrived, dials = c, nil, d
			mu.Unlock()
			// Which connection an exchange takes decides whether Go retries
			// one the server closed: each case starts with none kept, or
			// with one a warm exchange left. Each run gets new transports
			// (localFreshTransports), not only an empty pool: a dial of an
			// earlier case that lost its race to an idle connection joins
			// its transport's pool when it completes, which under load can
			// be after this case began, and the request it found there was
			// retried once more (seed 557: three arrivals on loopback, two
			// in memory).
			fetch := HTTPFetch
			if !remote {
				fetch = func(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string) (HTTPResponse, error) {
					return HTTPFetchLocal(t, origins, anyOrigin, limits, hops, method, rawURL, header, owners, credentials, body, local)
				}
			}
			if c.warm {
				if _, err := fetch(task, nil, true, serveLimits(1<<16, c.limits.MaxHeaderBytes), -1, "GET", origin+"/warm", nil, nil, nil, ""); err != nil {
					t.Fatalf("seed %d: warm exchange: %v", seed, err)
				}
			}
			d.begin()
			var parts strings.Builder
			put := func(_ *rt.Task, part string) error {
				if c.failPut {
					return errLocalSink
				}
				parts.WriteString(part)
				return nil
			}
			accepts := func(status int) bool {
				if c.accepted == nil {
					return status >= 200 && status < 300
				}
				for _, a := range c.accepted {
					if a == status {
						return true
					}
				}
				return false
			}
			u := origin + c.path
			if c.rawQuery != "" {
				u += "?" + c.rawQuery
			}
			var r HTTPResponse
			var err error
			switch {
			case remote && c.into:
				r, err = HTTPFetchInto(task, nil, true, c.limits, c.hops, c.method, u, c.header, nil, nil, c.body, accepts, put)
			case remote:
				r, err = HTTPFetch(task, nil, true, c.limits, c.hops, c.method, u, c.header, nil, nil, c.body)
			case c.into:
				r, err = HTTPFetchLocalInto(task, nil, true, c.limits, c.hops, c.method, u, c.header, nil, nil, c.body, accepts, put, local)
			default:
				r, err = HTTPFetchLocal(task, nil, true, c.limits, c.hops, c.method, u, c.header, nil, nil, c.body, local)
			}
			mu.Lock()
			defer mu.Unlock()
			o := localOutcome{Status: r.Status, Header: localWithout(r.Header), Body: r.Body, Failure: localFailure(err), Arrivals: arrived}
			// A sink sees the parts of a body that succeeds; the host may
			// have given some parts of a body that later failed.
			if err == nil {
				o.Parts = parts.String()
			}
			return o, c.warm && d.cold()
		}
		// A warm case holds only when its request goes out on the warm
		// exchange's connection. Go's transport keeps that connection only
		// if its writer reports the request written within 50 ms of the
		// answer read (maxWriteWaitBeforeConnReuse), which load can stretch,
		// and a request on a new connection is not retried (seed 12 under
		// load, or with that wait 0: two arrivals on one side, one on the
		// other). The test waits on the event itself, the connection the
		// request's first bytes were written to, and runs a cold one again.
		run := func(remote bool) localOutcome {
			for try := 1; ; try++ {
				o, cold := runOnce(remote)
				if !cold {
					return o
				}
				if try == localWarmTries {
					t.Fatalf("seed %d: in %d tries the %s transport never sent the request on the connection its warm exchange left", seed, try, map[bool]string{true: "loopback", false: "local"}[remote])
				}
			}
		}
		want, got := run(true), run(false)
		// An encoded query arrives as its fields, in key order
		// (http.EncodeQuery, read back by ParseQuery).
		if c.query != nil && len(want.Arrivals) > 0 {
			sorted := slices.Clone(c.query)
			slices.SortStableFunc(sorted, func(a, b HTTPField) int { return strings.Compare(a.Name, b.Name) })
			if got := want.Arrivals[0].query; !reflect.DeepEqual(got, sorted) && !(len(got) == 0 && len(sorted) == 0) {
				t.Fatalf("seed %d: EncodeQuery(%q) = %q arrives as %q", seed, c.query, c.rawQuery, got)
			}
		}
		// Whether a redirect's connection is back in the pool when the
		// client follows it is a race inside net/http, so whether a refused
		// /final is retried on it is too; after a redirect a retried
		// arrival counts once. The retry itself is held without a redirect
		// (sawRetry).
		if c.redirect {
			want.Arrivals, got.Arrivals = slices.CompactFunc(want.Arrivals, localSameArrival), slices.CompactFunc(got.Arrivals, localSameArrival)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("seed %d (replay ERGO_HTTP_LOCAL_SEED=%d go test -run TestHTTPLocalMatchesLoopback ./runtime/v1):\ncase     %+v\nloopback %+v\nlocal    %+v", seed, seed, c, want, got)
		}
		sawHeaderBudget = sawHeaderBudget || c.limits.MaxHeaderBytes != c.limits.MaxBodyBytes && want.Failure == fmt.Sprintf("TooLarge %d", c.limits.MaxHeaderBytes)
		sawRetry = sawRetry || !c.redirect && len(want.Arrivals) == 2
	}
	if len(seeds) > 1 && (!sawHeaderBudget || !sawRetry) {
		t.Fatalf("the cases never reached a header over its budget (%v) or a retried request (%v)", sawHeaderBudget, sawRetry)
	}
}

func localCaseOf(r *rand.Rand) localCase {
	text := func(n int) string {
		const alphabet = "abcXYZ019 -_.~%+&=/?#é\t!$'()*,;:@"
		var b strings.Builder
		for range n {
			b.WriteString(string([]rune(alphabet)[r.IntN(len([]rune(alphabet)))]))
		}
		return b.String()
	}
	// escape writes s for a URL. An unreserved byte is kept; a
	// sub-delimiter, ':' or '@', which a path may hold as it is, is kept
	// one time in two; every other byte is a percent escape, in upper or
	// lower case hex (HTTP-PATH-ESCAPE: a path already escaped, "%2f"
	// included, must reach the handler as written, never escaped again).
	escape := func(s string) string {
		var b strings.Builder
		for _, c := range []byte(s) {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
				b.WriteByte(c)
			} else if strings.IndexByte("!$'()*,;:@", c) >= 0 && r.IntN(2) == 0 {
				b.WriteByte(c)
			} else if r.IntN(4) == 0 && c == ' ' {
				b.WriteByte('+')
			} else if r.IntN(2) == 0 {
				fmt.Fprintf(&b, "%%%02x", c)
			} else {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
		return b.String()
	}
	c := localCase{
		method: []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}[r.IntN(7)],
		status: []int{200, 201, 202, 204, 206, 299, 301, 302, 303, 304, 307, 308, 400, 401, 404, 418, 500, 503, 599, 103, 100, 101, 0, 42, 1000}[r.IntN(25)],
		hops:   r.IntN(4) - 1,
		into:   r.IntN(2) == 0,
	}
	segments := r.IntN(4)
	c.path = "/"
	for i := range segments {
		if i > 0 {
			c.path += "/"
		}
		c.path += escape(text(r.IntN(6)))
	}
	if r.IntN(2) == 0 {
		var pairs []string
		for range r.IntN(4) {
			pairs = append(pairs, escape(text(1+r.IntN(4)))+"="+escape(text(r.IntN(5))))
		}
		c.rawQuery = strings.Join(pairs, "&")
	} else {
		for range r.IntN(4) {
			c.query = append(c.query, HTTPField{text(1 + r.IntN(4)), text(r.IntN(5))})
		}
		c.rawQuery = httpEncodeQuery(c.query)
	}
	if c.method == "POST" || c.method == "PUT" || c.method == "PATCH" || r.IntN(4) == 0 {
		c.body = text(r.IntN(64))
	}
	for i := range r.IntN(3) {
		c.header = append(c.header, HTTPField{[]string{"X-Case", "x-other", "X-CASE"}[i%3], escape(text(r.IntN(8)))})
		c.answerHeader = append(c.answerHeader, HTTPField{[]string{"X-Answer", "x-answer", "X-More"}[r.IntN(3)], escape(text(r.IntN(60)))})
	}
	size := []int{0, 1, 17, 1023, 1024, 1025, 3000}[r.IntN(7)]
	c.answer = strings.Repeat("ab", size/2+1)[:size]
	c.redirect = r.IntN(4) == 0
	budget := max(1, size+r.IntN(5)-2)
	if r.IntN(3) == 0 {
		budget = 1 << 16
	}
	c.limits = serveLimits(budget, []int{1 << 16, 1 << 16, 160, 220, 300, 450}[r.IntN(6)])
	if r.IntN(2) == 0 {
		c.accepted = []int{c.status, 418, 503}[:1+r.IntN(3)]
		if r.IntN(3) == 0 {
			c.accepted = []int{301}
		}
	}
	c.failPut = c.into && r.IntN(6) == 0
	c.warm = r.IntN(2) == 0
	return c
}

// localWarmTries bounds the runs of a warm case whose request went out on
// a new connection: a transport that never reuses one (local-no-keepalive)
// fails the case rather than looping.
const localWarmTries = 20

// localFreshTransports drops the cached loopback and local transports of
// maxHeader, closing their idle connections, and puts new ones in their
// place whose pools hold nothing an earlier case left. A late dial of a
// dropped transport lands in that transport's pool, which no fetch reads.
// The new transports number the connections they dial in the dials it
// returns.
func localFreshTransports(maxHeader int) *localDials {
	for _, m := range []*sync.Map{&httpTransports, &httpLocalTransports} {
		if old, ok := m.LoadAndDelete(maxHeader); ok {
			old.(*http.Transport).CloseIdleConnections()
		}
	}
	d := &localDials{first: -1, wrote: make(chan struct{})}
	// The local transport is a clone of the loopback one, so the loopback
	// one is made first and both are wrapped before any fetch uses them.
	for _, tr := range []*http.Transport{httpTransportFor(maxHeader), httpLocalTransportFor(maxHeader)} {
		dial := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			d.n++
			return &localConn{Conn: conn, dials: d, n: d.n - 1}, nil
		}
	}
	return d
}

// localDials numbers the connections a run's transports dial, and records
// which one the request after begin was first written to.
type localDials struct {
	mu          sync.Mutex
	n           int // connections dialed
	begun       bool
	mark, first int // n at begin; the connection written first after it, or -1
	// wrote is closed when the first write of the run has returned.
	wrote chan struct{}
	once  sync.Once
}

func (d *localDials) begin() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.begun, d.mark, d.first = true, d.n, -1
}

// cold reports whether the request after begin was first written to a
// connection dialed after begin, not one kept from before.
func (d *localDials) cold() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.first >= d.mark
}

type localConn struct {
	net.Conn
	dials *localDials
	n     int
}

func (c *localConn) Write(p []byte) (int, error) {
	d := c.dials
	d.mu.Lock()
	if d.begun && d.first < 0 {
		d.first = c.n
	}
	d.mu.Unlock()
	n, err := c.Conn.Write(p)
	d.once.Do(func() { close(d.wrote) })
	return n, err
}

// localSameArrival reports whether two arrivals are the same request.
func localSameArrival(a, b localArrival) bool { return reflect.DeepEqual(a, b) }
