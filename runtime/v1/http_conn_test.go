package v1

import (
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// TestHTTPHandlerRunsOnTheConnection: std http Serve runs a request's task
// on the net/http goroutine that read the request, with no goroutine
// between the connection and its handler (GX4). The handler reads its own
// stack: the connection's serve loop is below it. Each request of a few
// over one keep-alive connection checks it.
//
// Mutant it kills (testdata/mutants.json): gx4-member-on-own-goroutine
// (rt.RunIn runs the member on a goroutine of its own and waits for it,
// which no other test sees: the exits and the stops are the same).
func TestHTTPHandlerRunsOnTheConnection(t *testing.T) {
	l, _ := startHTTP(t, func(_ *rt.Task, _ *Exchange, _, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		buf := make([]byte, 64<<10)
		stack := string(buf[:runtime.Stack(buf, false)])
		if !strings.Contains(stack, "net/http.(*conn).serve") {
			return HTTPResponse{Status: 500, Body: stack}
		}
		return HTTPResponse{Status: 200}
	})
	client := httpClient(t)
	for range 3 {
		resp := httpRequest(t, client, "GET", "http://"+l.Addr()+"/", "")
		if body := httpText(t, resp); resp.StatusCode != 200 {
			t.Fatalf("the handler runs off the connection's goroutine:\n%s", body)
		}
	}
}
