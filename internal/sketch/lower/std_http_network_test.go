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

const httpNetworkProbe = `import http
import os
import strconv

func main() fails http.InvalidClient | http.InvalidLimits needs Ambient | Console {
	let args = with context { os.Process: os.NewHost() } { os.Process.Args() }
	let hops = strconv.Atoi(args[3]) catch { case _: Unreachable("test hop input") }
	let policy http.OriginPolicy = if args[2] == "any" { http.AnyOrigin } else if args[2] == "both" { http.Origins{Values: []string{args[0], args[1]}} } else if args[2] == "first" { http.Origins{Values: []string{args[0]}} } else { http.Origins{Values: []string{}} }
	let net = try http.NewNet(policy, http.Limits{MaxBodyBytes: 128, MaxHeaderBytes: 8192, IdleTimeout: 5s, ReadHeaderTimeout: 10s, ReadBodyTimeout: 30s}, http.Follow{MaxHops: hops})
	let a = try http.ForOrigin(args[0], http.Field{Name: "X-Mark-A", Value: "mark-A"})
	let b = try http.ForOrigin(args[1], http.Field{Name: "X-Mark-B", Value: "mark-B"})
	let request = http.URLRequest{URL: args[0] + "/0/" + args[4], Method: "GET", Header: []http.Field{}, Credentials: []http.Credential{a, b}, Body: ""}
	let result = with context { http.Network: net } { http.Fetch(request) } catch {
	case http.OriginRefused as o: Console.Println("origin " + o.Origin); return
	case http.RedirectLimit as l: Console.Println("limit " + strconv.Itoa(l.Limit)); return
	case http.RequestFailed: Console.Println("request failed"); return
	}
	Console.Println("ok " + result.Body)
}
`

// TestHTTPNetworkRuns kills the std credential-owner and native failure-row
// mutants. The runtime model cannot see flattening of source Credentials,
// source context delegation or the error identities emitted at the boundary.
// It builds Go because those conversions and typed catches execute only in
// a built program; go/types would accept a catch of the wrong error identity.
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
			if e1 != nil || e2 != nil {
				t.Error("invalid generated path")
				return
			}
			if at < length {
				w.Header().Set("Location", fmt.Sprintf("%s/%d/%d", origins[1-host], at+1, length))
				w.WriteHeader(307)
				return
			}
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
		want := "ok mark-A/\n"
		if length%2 == 1 {
			want = "ok /mark-B\n"
		}
		switch {
		case mode == "none":
			want = "origin " + origins[0] + "\n"
		case length > 0 && hops > 0 && mode == "first":
			want = "origin " + origins[1] + "\n"
		case length > hops:
			want = fmt.Sprintf("limit %d\n", hops)
		}
		out, errout, code := runExeArgs(t, exes["httpnetwork"], origins[0], origins[1], mode, strconv.Itoa(hops), strconv.Itoa(length))
		if code != 0 || out != want {
			t.Fatalf("seed %d, mode %s length %d hops %d\nErgo exit %d: %q\nmodel: %q\nstderr: %s\nreplay: ERGO_HTTP_NETWORK_SOURCE_SEED=%d go test -run '^TestHTTPNetworkRuns$' ./internal/sketch/lower\n%s", seed, mode, length, hops, code, out, want, errout, seed, httpNetworkProbe)
		}
	}
}
