package lower

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

const httpNetworkProbe = `import fs
import http
import os
import strconv
import sync

// body is the response body as args[5] asks for it: buffered by Fetch,
// given to a put by FetchInto (DoInto, through the fixed-origin Client),
// or written to out.txt below the root args[6] by FetchFile (DoFile) and
// read back.
func body(request http.URLRequest, fixed http.Request, args []string) string fails http.RequestFailed | http.OriginRefused | http.RedirectLimit | http.StatusRefused | http.SinkFailed | fs.NotFound | fs.Denied | fs.Failed | fs.Escapes needs http.Network | http.Net | Ambient {
	if args[5] == "into" || args[5] == "dointo" {
		let got = sync.NewRef[string]("")
		let put = func(part string) fails http.SinkFailed { got.Update(func(s string) string { s + part }) }
		let _ = if args[5] == "into" { try http.FetchInto(request, put) } else { try http.DoInto(fixed, put) }
		return got.Get()
	}
	if args[5] == "file" || args[5] == "dofile" {
		let root = try fs.OpenRoot(args[6])
		let _ = if args[5] == "file" { try http.FetchFile(root, "out.txt", request) } else { try http.DoFile(root, "out.txt", fixed) }
		return try root.ReadFile("out.txt")
	}
	let result = try http.Fetch(request)
	result.Body
}

func main() fails http.InvalidClient | http.InvalidLimits needs Ambient | Console {
	let args = with context { os.Process: os.NewHost() } { os.Process.Args() }
	let hops = strconv.Atoi(args[3]) catch { case _: Unreachable("test hop input") }
	let policy http.OriginPolicy = if args[2] == "any" { http.AnyOrigin } else if args[2] == "both" { http.Origins{Values: []string{args[0], args[1]}} } else if args[2] == "first" { http.Origins{Values: []string{args[0]}} } else { http.Origins{Values: []string{}} }
	let limits = http.Limits{MaxBodyBytes: 128, MaxHeaderBytes: 8192, IdleTimeout: 5s, ReadHeaderTimeout: 10s, ReadBodyTimeout: 30s}
	let net = try http.NewNet(policy, limits, http.Follow{MaxHops: hops})
	let client = try http.NewClient(args[0], limits)
	let a = try http.ForOrigin(args[0], http.Field{Name: "X-Mark-A", Value: "mark-A"})
	let b = try http.ForOrigin(args[1], http.Field{Name: "X-Mark-B", Value: "mark-B"})
	let path = "/0/" + args[4] + "/" + args[7]
	let request = http.URLRequest{URL: args[0] + path, Method: "GET", Header: []http.Field{}, Credentials: []http.Credential{a, b}, Body: ""}
	let fixed = http.Request{Method: "GET", Path: path, Query: []http.Field{}, Header: []http.Field{}, Body: http.Buffered{Text: ""}}
	let text = with context { http.Network: net, http.Net: client } { body(request, fixed, args) } catch {
	case http.OriginRefused as o: Console.Println("origin " + o.Origin); return
	case http.RedirectLimit as l: Console.Println("limit " + strconv.Itoa(l.Limit)); return
	case http.RequestFailed: Console.Println("request failed"); return
	case http.StatusRefused as s: Console.Println("refused " + strconv.Itoa(s.Status) + " [" + s.Prefix + "]"); return
	case http.SinkFailed, fs.NotFound, fs.Denied, fs.Failed, fs.Escapes: Console.Println("sink failed"); return
	}
	Console.Println("ok " + text)
}
`

// TestHTTPNetworkRuns kills the std credential-owner and native failure-row
// mutants. The runtime model cannot see flattening of source Credentials,
// source context delegation or the error identities emitted at the boundary.
// It builds Go because those conversions and typed catches execute only in
// a built program; go/types would accept a catch of the wrong error identity.
// Its sink modes (CU6) kill cu6-into-hops-lost and cu6-file-parts-dropped:
// the runtime property calls the Go bodies, not the std provider over them.
// Its final statuses and fixed-origin sinks (CU6b) kill the std mutants of
// the 2xx rule's callers: FetchInto, FetchFile, DoInto, DoFile, and the
// providers Remote and Client that hand the rule to the runtime.
func TestHTTPNetworkRuns(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{mustLower(t, "httpnetwork", httpNetworkProbe)}, true)
	if exes == nil {
		return
	}
	var origins [2]string
	handler := func(host int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			at, e1 := strconv.Atoi(parts[0])
			length, e2 := strconv.Atoi(parts[1])
			status, e3 := strconv.Atoi(parts[2])
			if e1 != nil || e2 != nil || e3 != nil {
				t.Error("invalid generated path")
				return
			}
			if at < length {
				w.Header().Set("Location", fmt.Sprintf("%s/%d/%d/%d", origins[1-host], at+1, length, status))
				w.WriteHeader(307)
				return
			}
			w.WriteHeader(status)
			fmt.Fprintf(w, "%s/%s", r.Header.Get("X-Mark-A"), r.Header.Get("X-Mark-B"))
		}
	}
	a, b := httptest.NewServer(handler(0)), httptest.NewServer(handler(1))
	defer a.Close()
	defer b.Close()
	origins = [2]string{a.URL, b.URL}
	seeds := make([]uint64, 32)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_HTTP_NETWORK_SOURCE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 27))
		length, hops := r.IntN(5), r.IntN(6)
		mode := []string{"any", "both", "first", "none"}[r.IntN(4)]
		status := []int{200, 201, 404}[r.IntN(3)]
		// Each case runs through every sink, so each sink's std caller meets
		// a 2xx and a refused status within the seeds.
		for _, sink := range []string{"fetch", "into", "file", "dointo", "dofile"} {
			// outcome is the line of a final response: a sink takes a 2xx body
			// (the default rule); Fetch buffers any.
			outcome := func(final int, text string) string {
				if sink != "fetch" && (final < 200 || final >= 300) {
					return fmt.Sprintf("refused %d [%s]\n", final, text)
				}
				return "ok " + text + "\n"
			}
			var want string
			switch {
			case strings.HasPrefix(sink, "do") && length > 0:
				// The fixed-origin client follows no redirect; the 307 has no body.
				want = outcome(307, "")
			case strings.HasPrefix(sink, "do"):
				want = outcome(status, "/")
			case mode == "none":
				want = "origin " + origins[0] + "\n"
			case length > 0 && hops > 0 && mode == "first":
				want = "origin " + origins[1] + "\n"
			case length > hops:
				want = fmt.Sprintf("limit %d\n", hops)
			case length%2 == 1:
				want = outcome(status, "/mark-B")
			default:
				want = outcome(status, "mark-A/")
			}
			dir := t.TempDir()
			out, errout, code := runExeArgs(t, exes["httpnetwork"], origins[0], origins[1], mode, strconv.Itoa(hops), strconv.Itoa(length), sink, dir, strconv.Itoa(status))
			if code != 0 || out != want {
				t.Fatalf("seed %d, mode %s length %d hops %d sink %s status %d\nErgo exit %d: %q\nmodel: %q\nstderr: %s\nreplay: ERGO_HTTP_NETWORK_SOURCE_SEED=%d go test -run '^TestHTTPNetworkRuns$' ./internal/sketch/lower\n%s", seed, mode, length, hops, sink, status, code, out, want, errout, seed, httpNetworkProbe)
			}
		}
	}
}
