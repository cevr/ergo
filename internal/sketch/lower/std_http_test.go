package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
)

// TestHTTPHandlerRows protects the public function-handler seam: dropping
// Serve's open capability row or its runtime callback adapter breaks this
// in-process Go typecheck. Socket lifetime is tested in runtime/v1.
func TestHTTPHandlerRows(t *testing.T) {
	if _, err := check.Std.Import("http"); err != nil {
		t.Fatal(err)
	}
	const src = `import http
import time
func handle(r http.Request) http.Response needs time.Clock {
	let now = time.Clock.Now()
	http.Response{Status: 200, Header: []http.Field{}, Body: now.Format(time.DateOnly)}
}
func serve(l http.Listener) fails http.InvalidLimits needs time.Clock {
	return http.Serve(l, http.Limits{MaxBodyBytes: 1024, MaxHeaderBytes: 4096}, handle)
}
func direct() http.Response {
	let clock = time.NewManual(0)
	with context { time.Clock: clock } {
		handle(http.Request{Method: "GET", Path: "/", Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}})
	}
}
`
	o := lowerSource("http_rows", src, false)
	if o.err != nil || len(o.diags) != 0 || len(o.refused) != 0 || o.panicked != nil {
		t.Fatalf("HTTP source: err=%v diagnostics=%v refusals=%v panic=%v", o.err, o.diags, o.refused, o.panicked)
	}
	typecheckGo(t, []*lowered{o})
}

const stdHTTPLawsProbe = `import http
import strings
import time
import strconv

test HttpBuiltPathMatches(a string, b string) {
	if a == "" || b == "" || strings.Contains(a, "/") || strings.Contains(b, "/") { return }
	let route = http.Route{Name: "pair", Method: "GET", Path: "/:a/:b"}
	match http.Match([]http.Route{route}, "GET", "/" + a + "/" + b) {
	case http.Found as f:
		assert f.Route == route && f.Params.Get("a") == Some{a} && f.Params.Get("b") == Some{b}
	case http.NotFound, http.MethodNotAllowed:
		assert false
	}
}

test RouterRefusalsAndHead(id string) {
	let path = "/jobs/j" + strings.ReplaceAll(id, "/", "_")
	let routes = []http.Route{http.Route{Name: "get", Method: "GET", Path: "/jobs/:id"}, http.Route{Name: "head", Method: "HEAD", Path: "/jobs/:id"}}
	match http.Match(routes, "HEAD", path) {
	case http.Found as f: assert f.Route.Name == "head"
	case http.NotFound, http.MethodNotAllowed: assert false
	}
	match http.Match([]http.Route{routes[0]}, "HEAD", path) {
	case http.Found as f: assert f.Route.Name == "get"
	case http.NotFound, http.MethodNotAllowed: assert false
	}
	assert http.Match(routes, "POST", path) == http.MethodNotAllowed
	assert http.Match(routes, "GET", "/other" + path) == http.NotFound
	assert http.Match(routes, "GET", "/jobs/") == http.NotFound
}

func clockHandler(r http.Request) http.Response needs time.Clock {
	http.Response{Status: 200, Header: []http.Field{}, Body: strconv.FormatInt(time.Clock.Now().UnixSeconds(), 10)}
}

test HandlerUnderManual(sec int64, value string) {
	let second = sec % 10000
	let manual = time.NewManual(second)
	let response = with context { time.Clock: manual } {
		clockHandler(http.Request{Method: "GET", Path: "/", Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}})
	}
	assert response.Body == strconv.FormatInt(second, 10)
	let fields = []http.Field{http.Field{Name: "cOnTeNt-TyPe", Value: value}, http.Field{Name: "empty", Value: ""}}
	assert http.Header(fields, "CONTENT-TYPE") == Some{value}
	assert http.Header(fields, "EMPTY") == Some{""}
	assert http.Header(fields, "missing") == None
	assert http.Query(fields, "EMPTY") == None
}
`

const httpBuiltPathStatement = `BuiltPathMatches(a string, b string) { if a == "" || b == "" || strings.Contains(a, "/") || strings.Contains(b, "/") { return true } let route = Route{Name: "pair", Method: "GET", Path: "/:a/:b"} match Match([]Route{route}, "GET", "/"+a+"/"+b) { case Found as f: f.Route == route && f.Params.Get("a") == Some{a} && f.Params.Get("b") == Some{b} case NotFound, MethodNotAllowed: false } }`

// TestStdHTTPLawsRun runs the construction law and public pure handler
// checks. It builds Go because the native property runner executes the
// std router and bound clock; go/types alone cannot observe their values.
func TestStdHTTPLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdhttp"), "--seed", "1")
	if code != 0 {
		t.Fatalf("HTTP laws: exit %d\n%s\n%s", code, stdout, stderr)
	}
	lawledger.Run(t, lawsPassed(t, map[string]string{"http BuiltPathMatches": httpBuiltPathStatement}, func(string) string { return "HttpBuiltPathMatches" }, stdHTTPLawsProbe, stdout, "RouterRefusalsAndHead", "HandlerUnderManual"))
}
