package v1

import (
	"errors"
	"fmt"
	"math/rand/v2"
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
	query                        []HTTPField // the fields rawQuery encodes (HTTPEncodeQuery), nil when it is written
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
// handler is given the same request, and the target's origin. Seeded over
// methods, statuses (refused ones too), bodies, paths, queries, headers
// around the header budget, redirects and their hop limits, body budgets,
// a fresh or a kept-alive connection (a closed kept-alive one is retried),
// and the streamed form with its accepted statuses and a failing sink.
// Only Date is left out. A failing seed prints the case and both
// outcomes; ERGO_HTTP_LOCAL_SEED=n replays it. An encoded query
// (HTTPEncodeQuery) arrives as its fields. Mutants (runtime/v1 ledger):
// local-key-kept, local-answer-unlike-serve, local-header-budget-lost,
// local-no-keepalive, local-origin-raw, encode-query-unescaped.
func TestHTTPLocalMatchesLoopback(t *testing.T) {
	var (
		mu      sync.Mutex
		current localCase
		arrived []localArrival
	)
	answer := func(method, path string, query, header []HTTPField, body string) HTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		c := current
		if path == "/warm" {
			return HTTPResponse{Status: 200, Body: "warm"}
		}
		arrived = append(arrived, localArrival{method, path, query, localWithout(header), body})
		if c.redirect && path != "/final" {
			return HTTPResponse{Status: 307, Header: []HTTPField{{"Location", "/final"}}, Body: "moved"}
		}
		return HTTPResponse{Status: c.status, Header: c.answerHeader, Body: c.answer}
	}
	l, _ := startHTTPLimits(t, serveLimits(1<<20, 1<<16), func(_ *rt.Task, _ *Exchange, method, path string, query, header []HTTPField, body string, _ bool, _ string) HTTPResponse {
		return answer(method, path, query, header, body)
	})
	origin := "http://" + l.Addr()
	// The local handler is given the target's origin, which the loopback
	// handler reads from its listener.
	local := func(_ *rt.Task, at string, method, path string, query, header []HTTPField, body string) HTTPResponse {
		if at != origin {
			t.Errorf("the local handler was given the origin %q for %q", at, origin)
		}
		return answer(method, path, query, header, body)
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
		run := func(remote bool) localOutcome {
			mu.Lock()
			current, arrived = c, nil
			mu.Unlock()
			// Which connection an exchange takes decides whether Go retries
			// one the server closed: each case starts with none kept, or
			// with one a warm exchange left.
			tr, fetch := httpTransportFor(c.limits.MaxHeaderBytes), HTTPFetch
			if !remote {
				tr = httpLocalTransportFor(c.limits.MaxHeaderBytes)
				fetch = func(t *rt.Task, origins []string, anyOrigin bool, limits HTTPLimits, hops int, method, rawURL string, header []HTTPField, owners []string, credentials []HTTPField, body string) (HTTPResponse, error) {
					return HTTPFetchLocal(t, origins, anyOrigin, limits, hops, method, rawURL, header, owners, credentials, body, local)
				}
			}
			tr.CloseIdleConnections()
			if c.warm {
				if _, err := fetch(task, nil, true, serveLimits(1<<16, c.limits.MaxHeaderBytes), -1, "GET", origin+"/warm", nil, nil, nil, ""); err != nil {
					t.Fatalf("seed %d: warm exchange: %v", seed, err)
				}
			}
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
			return o
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
		const alphabet = "abcXYZ019 -_.~%+&=/?#é\t"
		var b strings.Builder
		for range n {
			b.WriteString(string([]rune(alphabet)[r.IntN(len([]rune(alphabet)))]))
		}
		return b.String()
	}
	escape := func(s string) string {
		var b strings.Builder
		for _, c := range []byte(s) {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
				b.WriteByte(c)
			} else if r.IntN(4) == 0 && c == ' ' {
				b.WriteByte('+')
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
		c.rawQuery = HTTPEncodeQuery(c.query)
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
