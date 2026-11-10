package lower

import (
	"strings"
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

// TestHTTPHasNoTestProviderDependency catches a law-only import retaining
// fstest in a production HTTP program. Functional HTTP tests import fstest
// on purpose and cannot catch this dependency regression.
func TestHTTPHasNoTestProviderDependency(t *testing.T) {
	pkg, err := check.Std.Import("http")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[*check.Package]bool{}
	var visit func(*check.Package)
	visit = func(p *check.Package) {
		if seen[p] {
			return
		}
		seen[p] = true
		if p.Path == "fstest" {
			t.Error("http imports fstest in a non-test build")
		}
		for _, obj := range p.Info.Uses {
			if dep, ok := obj.(*check.PkgName); ok {
				visit(dep.Pkg)
			}
		}
	}
	visit(pkg)
}

const stdHTTPLawsProbe = `import fs
import fstest
import http
import strings
import time
import slices
import strconv
import sync

func segment(step int) string {
	let segments = []string{"..", ".", "", "a.txt", "sub", "out", "in", "%2e%2e", "...", "..a", "b.txt"}
	let i = if step < 0 { -(step % len(segments)) } else { step % len(segments) }
	segments[i]
}

test HttpFilesAsksBeneath(steps []int, prefixed bool) {
	let mut rest = ""
	let mut dotdot = false
	for i, s in steps {
		let e = segment(s)
		dotdot = dotdot || e == ".."
		rest = if i == 0 { e } else { rest + "/" + e }
	}
	let path = if prefixed { "/static/" + rest } else { "/" + rest }
	let method = if len(steps) % 2 == 0 { "GET" } else { "HEAD" }
	let root = fstest.NewRecorder(fstest.NewTree([]fstest.Node{
		fstest.File{Path: "a.txt", Text: "A"},
		fstest.File{Path: "sub/b.txt", Text: "B"},
		fstest.Link{Path: "out", Target: "../outside"},
		fstest.Link{Path: "in", Target: "sub"},
	}))
	let response = http.Files(root, "/static/", http.Request{Method: method, Path: path, Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}})
	let asked = root.Paths()
	assert len(asked) <= 1
	if dotdot || !prefixed { assert len(asked) == 0 }
	for p in asked {
		assert !strings.HasPrefix(p, "/")
		for e in strings.Split(p, "/") {
			assert e != "" && e != "." && e != ".."
		}
	}
	assert response.Status == 200 || response.Status == 403 || response.Status == 404
}

// The reference of Files' rule: the status, the headers and the body
// it gives for a request, read from the tree through the Root itself.
func expected(root fstest.Tree, method string, path string) http.Response {
	let none = []http.Field{}
	if method != "GET" && method != "HEAD" {
		return http.Response{Status: 405, Header: []http.Field{http.Field{Name: "Allow", Value: "GET, HEAD"}}, Body: ""}
	}
	if !strings.HasPrefix(path, "/files/") { return http.Response{Status: 404, Header: none, Body: ""} }
	let mut names = []string{}
	for e in strings.Split(strings.TrimPrefix(path, "/files/"), "/") {
		if e == ".." { return http.Response{Status: 403, Header: none, Body: ""} }
		if e != "" && e != "." { names = names.Append(e) }
	}
	if len(names) == 0 { return http.Response{Status: 404, Header: none, Body: ""} }
	let name = strings.Join(names, "/")
	let text = root.ReadFile(name) catch {
	case fs.NotFound, fs.Failed: return http.Response{Status: 404, Header: none, Body: ""}
	case fs.Denied, fs.Escapes: return http.Response{Status: 403, Header: none, Body: ""}
	}
	if text == "" { return http.Response{Status: 200, Header: none, Body: ""} }
	let lower = strings.ToLower(name)
	let kind = if strings.HasSuffix(lower, ".html") { "text/html; charset=utf-8" } else if strings.HasSuffix(lower, ".txt") { "text/plain; charset=utf-8" } else { "application/octet-stream" }
	http.Response{Status: 200, Header: []http.Field{http.Field{Name: "Content-Type", Value: kind}}, Body: text}
}

test FilesAnswersByTheRule(steps []int, prefixed bool, verb int) {
	let names = []string{"..", ".", "", "a.txt", "PAGE.HTML", "empty.txt", "sub", "b.txt", "out", "in", "loop", "data", "missing"}
	let mut rest = ""
	for i, s in steps {
		let k = if s < 0 { -(s % len(names)) } else { s % len(names) }
		rest = if i == 0 { names[k] } else { rest + "/" + names[k] }
	}
	let path = if prefixed { "/files/" + rest } else { "/f/" + rest }
	let methods = []string{"GET", "HEAD", "GET", "POST", "DELETE"}
	let method = methods[if verb < 0 { -(verb % 5) } else { verb % 5 }]
	let root = fstest.NewTree([]fstest.Node{
		fstest.File{Path: "a.txt", Text: "alpha"},
		fstest.File{Path: "PAGE.HTML", Text: "<p>page</p>"},
		fstest.File{Path: "empty.txt", Text: ""},
		fstest.File{Path: "data", Text: "raw"},
		fstest.File{Path: "sub/b.txt", Text: "beta"},
		fstest.Link{Path: "out", Target: "../outside"},
		fstest.Link{Path: "in", Target: "sub"},
		fstest.Link{Path: "loop", Target: "loop"},
	})
	let got = http.Files(root, "/files/", http.Request{Method: method, Path: path, Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}})
	assert got == expected(root, method, path)
	// Each name alone, with the case's method, so every file is reached.
	for name in names {
		let one = "/files/" + name
		assert http.Files(root, "/files/", http.Request{Method: method, Path: one, Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}}) == expected(root, method, one)
	}
}

test HttpBuiltPathMatches(a string, b string) {
	if a == "" || b == "" || strings.Contains(a, "/") || strings.Contains(b, "/") { return }
	let route = http.Route{Value: "pair", Method: "GET", Path: "/:a/:b"}
	match http.Match([]http.Route[string]{route}, "GET", "/" + a + "/" + b) {
	case http.Found as f:
		assert f.Route == route && f.Params.Get("a") == Some{a} && f.Params.Get("b") == Some{b}
	case http.NotFound, http.MethodNotAllowed:
		assert false
	}
}

test HttpStreamIsItsWrites(status int, parts []string) {
	let header = []http.Field{http.Field{Name: "Content-Type", Value: "text/plain"}}
	let response = with context { http.Streaming: http.NewRecorder() } {
		http.Stream(status, header, func(w http.BodyWriter) fails http.WriteFailed {
			for p in parts {
				try w.Write(p)
			}
		})
	}
	let mut want = ""
	for p in parts {
		want = want + p
	}
	assert response == http.Response{Status: status, Header: header, Body: want}
}

test HttpWriteAfterStreamFails(text string) {
	let kept = sync.NewRef[[]http.BodyWriter]([]http.BodyWriter{})
	let response = with context { http.Streaming: http.NewRecorder() } {
		http.Stream(200, []http.Field{}, func(w http.BodyWriter) fails http.WriteFailed {
			kept.Set([]http.BodyWriter{w})
		})
	}
	assert len(kept.Get()) == 1
	for w in kept.Get() {
		w.Write(text) catch {
		case http.WriteFailed:
			assert response.Body == ""
			return
		}
	}
	assert false
}

test HttpAllowIsWhatMatches(picks []int, asked int) {
	let methods = []string{"GET", "HEAD", "POST", "PUT", "DELETE"}
	let pick = func(i int) string { methods[if i < 0 { -(i % len(methods)) } else { i % len(methods) }] }
	let mut routes = []http.Route[int]{}
	for i, p in picks {
		routes = routes.Append(http.Route{Value: i, Method: pick(p), Path: "/r/:id"})
	}
	match http.Match(routes, pick(asked), "/r/7") {
	case http.Found, http.NotFound:
	case http.MethodNotAllowed as m:
		for i, a in m.Allow {
			assert !slices.Contains(m.Allow[i+1:], a)
		}
		for name in methods {
			let found = match http.Match(routes, name, "/r/7") {
			case http.Found: true
			case http.NotFound, http.MethodNotAllowed: false
			}
			assert found == slices.Contains(m.Allow, name)
		}
	}
}

test RouterRefusalsAndHead(id string) {
	let path = "/jobs/j" + strings.ReplaceAll(id, "/", "_")
	let routes = []http.Route[string]{http.Route{Value: "get", Method: "GET", Path: "/jobs/:id"}, http.Route{Value: "head", Method: "HEAD", Path: "/jobs/:id"}}
	match http.Match(routes, "HEAD", path) {
	case http.Found as f: assert f.Route.Value == "head"
	case http.NotFound, http.MethodNotAllowed: assert false
	}
	match http.Match([]http.Route[string]{routes[0]}, "HEAD", path) {
	case http.Found as f: assert f.Route.Value == "get"
	case http.NotFound, http.MethodNotAllowed: assert false
	}
	match http.Match(routes, "POST", path) {
	case http.MethodNotAllowed:
	case http.Found, http.NotFound: assert false
	}
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

const httpBuiltPathStatement = `BuiltPathMatches(a string, b string) { if a == "" || b == "" || strings.Contains(a, "/") || strings.Contains(b, "/") { return true } let route = Route{Value: "pair", Method: "GET", Path: "/:a/:b"} match Match([]Route[string]{route}, "GET", "/"+a+"/"+b) { case Found as f: f.Route == route && f.Params.Get("a") == Some{a} && f.Params.Get("b") == Some{b} case NotFound, MethodNotAllowed: false } }`

const httpAllowStatement = `AllowIsWhatMatches(picks []int, asked int) { let methods = []string{"GET", "HEAD", "POST", "PUT", "DELETE"} let pick = func(i int) string { methods[if i < 0 { -(i % len(methods)) } else { i % len(methods) }] } let mut routes = []Route[int]{} for i, p in picks { routes = routes.Append(Route{Value: i, Method: pick(p), Path: "/r/:id"}) } match Match(routes, pick(asked), "/r/7") { case Found, NotFound: true case MethodNotAllowed as m: for i, a in m.Allow { if slices.Contains(m.Allow[i+1:], a) { return false } } for name in methods { let found = match Match(routes, name, "/r/7") { case Found: true case NotFound, MethodNotAllowed: false } if found != slices.Contains(m.Allow, name) { return false } } true } }`

// TestStdHTTPLawsRun runs the construction law and public pure handler
// checks. It builds Go because the native property runner executes the
// std router and bound clock; go/types alone cannot observe their values.
func TestStdHTTPLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdhttp"), "--seed", "1")
	if code != 0 {
		t.Fatalf("HTTP laws: exit %d\n%s\n%s", code, stdout, stderr)
	}
	statements := map[string]string{"http BuiltPathMatches": httpBuiltPathStatement, "http AllowIsWhatMatches": httpAllowStatement, "http FilesAsksBeneath": httpFilesStatement, "http StreamIsItsWrites": httpStreamStatement, "http WriteAfterStreamFails": httpLateWriteStatement}
	lawLabel := func(key string) string { return "Http" + strings.TrimPrefix(key, "http ") }
	lawledger.Run(t, lawsPassed(t, statements, lawLabel, stdHTTPLawsProbe, stdout, "RouterRefusalsAndHead", "HandlerUnderManual", "FilesAnswersByTheRule"))
}

const httpFilesStatement = `FilesAsksBeneath(steps []int, prefixed bool) { let mut rest = "" let mut dotdot = false for i, s in steps { let e = fileSegment(s) dotdot = dotdot || e == ".." rest = if i == 0 { e } else { rest + "/" + e } } let path = if prefixed { "/static/" + rest } else { "/" + rest } let method = if len(steps)%2 == 0 { "GET" } else { "HEAD" } let root = filesProbe{paths: sync.NewRef[[]string]([]string{})} let response = Files(root, "/static/", Request{Method: method, Path: path, Query: []Field{}, Header: []Field{}, Body: Buffered{Text: ""}}) let asked = root.paths.Get() if len(asked) > 1 || (dotdot || !prefixed) && len(asked) != 0 { return false } for p in asked { if strings.HasPrefix(p, "/") { return false } for e in strings.Split(p, "/") { if e == "" || e == "." || e == ".." { return false } } } response.Status == 200 || response.Status == 403 || response.Status == 404 }`

const httpStreamStatement = `StreamIsItsWrites(status int, parts []string) { let header = []Field{Field{Name: "Content-Type", Value: "text/plain"}} let response = with context { Streaming: NewRecorder() } { Stream(status, header, func(w BodyWriter) fails WriteFailed { for p in parts { try w.Write(p) } }) } let mut want = "" for p in parts { want = want + p } response == Response{Status: status, Header: header, Body: want} }`

const httpLateWriteStatement = `WriteAfterStreamFails(text string) { let kept = sync.NewRef[[]BodyWriter]([]BodyWriter{}) let response = with context { Streaming: NewRecorder() } { Stream(200, []Field{}, func(w BodyWriter) fails WriteFailed { kept.Set([]BodyWriter{w}) }) } for w in kept.Get() { w.Write(text) catch { case WriteFailed: return response.Body == "" } } false }`
