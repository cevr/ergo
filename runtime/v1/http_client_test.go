package v1

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// clientCase is one generated exchange: the request the client sends,
// the response the server answers with and the client's body budget.
type clientCase struct {
	method, path  string
	query, header []HTTPField
	body          string
	answer        HTTPResponse
	budget        int
}

func clientText(r *rand.Rand, alphabet string, max int) string {
	runes := []rune(alphabet)
	var b strings.Builder
	for n := r.IntN(max + 1); n > 0; n-- {
		b.WriteRune(runes[r.IntN(len(runes))])
	}
	return b.String()
}

func clientFields(r *rand.Rand, names []string, values string) []HTTPField {
	var fs []HTTPField
	for n := r.IntN(4); n > 0; n-- {
		fs = append(fs, HTTPField{names[r.IntN(len(names))], clientText(r, values, 6)})
	}
	return fs
}

func newClientCase(r *rand.Rand) clientCase {
	statuses := []int{200, 201, 204, 301, 302, 307, 400, 404, 418, 500, 503}
	c := clientCase{
		method: []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}[r.IntN(6)],
		path:   "/" + clientText(r, "ab/ %?#&=é+", 8),
		query:  clientFields(r, []string{"q", "a b", "é", "x&y"}, "az 0&=+%é/"),
		header: clientFields(r, []string{"X-A", "X-Bee", "X-C"}, "az 0,;=é"),
		answer: HTTPResponse{Status: statuses[r.IntN(len(statuses))], Header: clientFields(r, []string{"X-R", "X-Ess"}, "az 0,;=")},
		budget: 1 + r.IntN(24),
	}
	if c.method != "GET" && c.method != "OPTIONS" {
		c.body = clientText(r, "ab\n{}é", 16)
	}
	if c.answer.Status >= 300 && c.answer.Status < 400 {
		// A client that follows redirects would ask /moved instead.
		c.answer.Header = append(c.answer.Header, HTTPField{"Location", "/moved"})
	}
	if c.answer.Status != 204 {
		c.answer.Body = clientText(r, "xy\né", 28)
	}
	return c
}

// clientValues is fields grouped by canonical name, values in order: the
// form in which Go's header and query maps keep a field list.
func clientValues(fields []HTTPField, canonical bool) map[string][]string {
	m := map[string][]string{}
	for _, f := range fields {
		name := f.Name
		if canonical {
			name = http.CanonicalHeaderKey(name)
		}
		value := f.Value
		if canonical {
			// A header value has no leading or trailing whitespace
			// (RFC 9110 5.5): the peer drops it.
			value = strings.Trim(value, " \t")
		}
		m[name] = append(m[name], value)
	}
	return m
}

// clientSubset reports whether every name of want has its values, in
// order, in got: the peer's transport adds fields (Content-Length, Date,
// User-Agent) and keeps the ones sent.
func clientSubset(want, got []HTTPField) bool {
	g := clientValues(got, true)
	for name, values := range clientValues(want, true) {
		if !slices.Equal(g[name], values) {
			return false
		}
	}
	return true
}

// TestHTTPClientMatchesServer is G9's model property with G1's server on
// a loopback listener as the peer: each generated request arrives as sent
// (method, decoded path, query, header and body), and each generated
// response comes back as sent (status, header and body; a 3xx is a
// response, not a redirect), or TooLarge when its body is over the
// client's budget. A failing seed prints the case and its replay.
func TestHTTPClientMatchesServer(t *testing.T) {
	var mu sync.Mutex
	var next HTTPResponse
	var arrived clientCase
	l, _ := startHTTPLimits(t, serveLimits(1<<16, 1<<16), func(_ *rt.Task, _ *Exchange, method, path string, query, header []HTTPField, body string, _ bool, _ string) HTTPResponse {
		mu.Lock()
		defer mu.Unlock()
		arrived = clientCase{method: method, path: path, query: query, header: header, body: body}
		return next
	})
	origin := "http://" + l.Addr()
	caller := rt.Root("http client test")
	for seed := uint64(1); seed <= 64; seed++ {
		c := newClientCase(rand.New(rand.NewPCG(seed, 9)))
		mu.Lock()
		next = c.answer
		mu.Unlock()
		res, err := HTTPDo(caller, origin, HTTPClientLimits{MaxBodyBytes: c.budget, MaxHeaderBytes: 1 << 16}, c.method, c.path, c.query, c.header, c.body)
		mu.Lock()
		in := arrived
		mu.Unlock()
		fail := func(what string) {
			t.Fatalf("seed %d: %s\ncase %+v\narrived %+v\nresponse %+v, %v\nreplay: go test -run 'TestHTTPClientMatchesServer' ./runtime/v1", seed, what, c, in, res, err)
		}
		if in.method != c.method || in.path != c.path || in.body != c.body {
			fail("the request did not arrive as sent")
		}
		if !maps2Equal(clientValues(in.query, false), clientValues(c.query, false)) || !clientSubset(c.header, in.header) {
			fail("the request's query or header did not arrive as sent")
		}
		if len(c.answer.Body) > c.budget {
			var f HTTPSendFailed
			if !errors.As(err, &f) || f.Kind != httpTooLarge || f.Limit != c.budget {
				fail("a body over the budget is not TooLarge")
			}
			continue
		}
		if err != nil || res.Status != c.answer.Status || res.Body != c.answer.Body || !clientSubset(c.answer.Header, res.Header) {
			fail("the response did not come back as sent")
		}
	}
}

func maps2Equal(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(b[k], v) {
			return false
		}
	}
	return true
}

// TestHTTPClientRefusedAndStopped: a connection nothing accepts is the
// typed Refused, not a Transport failure; a stop of the caller ends the
// call with the stop, and the server's request task sees the hang-up.
// Both wait on events.
func TestHTTPClientRefusedAndStopped(t *testing.T) {
	closed, err := HTTPListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr()
	closed.Close()
	_, err = HTTPDo(rt.Root("refused"), "http://"+addr, HTTPClientLimits{MaxBodyBytes: 16, MaxHeaderBytes: 4096}, "GET", "/", nil, nil, "")
	var f HTTPSendFailed
	if !errors.As(err, &f) || f.Kind != httpRefused {
		t.Fatalf("a refused connection failed %v, want Refused", err)
	}

	entered, hungUp := make(chan struct{}), make(chan struct{})
	l, _ := startHTTP(t, func(ct *rt.Task, _ *Exchange, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		close(entered)
		<-ct.Done()
		close(hungUp)
		panic(rt.Interrupted)
	})
	caller := rt.Root("stopped caller")
	ended := make(chan any, 1)
	go func() {
		defer func() { ended <- recover() }()
		_, err := HTTPDo(caller, "http://"+l.Addr(), HTTPClientLimits{MaxBodyBytes: 16, MaxHeaderBytes: 4096}, "GET", "/hold", nil, nil, "")
		ended <- fmt.Sprintf("returned %v", err)
	}()
	select {
	case <-entered:
	case <-testwait.After(t):
		t.Fatal("the request did not reach the server")
	}
	caller.Interrupt()
	select {
	case p := <-ended:
		if p != rt.Interrupted {
			t.Fatalf("the stopped call ended with %v, want the stop", p)
		}
	case <-testwait.After(t):
		t.Fatal("the stopped call did not end")
	}
	select {
	case <-hungUp:
	case <-testwait.After(t):
		t.Fatal("the server's request task did not see the hang-up")
	}
}
