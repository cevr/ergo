package v1

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// webTargetRow is one row of testdata/webtarget/targets.jsonl: a URL in
// parts (its host is put in by the test), or a base path and a redirect
// Location, and the request target the reference URL parser gives.
type webTargetRow struct {
	Lead, Scheme, Sep, Rest, Trail string
	Base, BaseTarget, Location     string
	Target                         string
}

// webCarried reports whether Go's client can send target as it is: a
// path that starts with "//" goes through RawPath (webURL.requestURL),
// which Go keeps only when it is valid escaped text; such a URL is refused.
func webCarried(target string) bool {
	path, _, _ := strings.Cut(target, "?")
	if !strings.HasPrefix(path, "//") {
		return true
	}
	p, err := url.PathUnescape(path)
	u := url.URL{Path: p, RawPath: path}
	return err == nil && u.EscapedPath() == path
}

// TestHTTPFetchSendsTheWebTarget: for each recorded URL, HTTPFetch and
// HTTPFetchInto send the request line "GET <target> HTTP/1.1" with the
// target the WHATWG
// reference gives (no re-escaping of "%zz", "|", "%2F"; the standard's
// dot segments, backslashes and percent-encode sets), and the Host header
// of the origin. A redirect's Location is resolved as the reference
// resolves it. The rows are recorded from a reference parser (gen.mjs), so
// this checks the bytes on the wire against it, at a raw loopback
// listener, with no other network. A target that starts with "//" and that
// Go cannot carry is Malformed. The fixed-origin HTTPDo tests cannot see
// these bytes: Go's net/http server decodes the target before a handler.
func TestHTTPFetchSendsTheWebTarget(t *testing.T) {
	text, err := os.ReadFile("testdata/webtarget/targets.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var rows []webTargetRow
	for _, line := range strings.Split(strings.TrimSpace(string(text)), "\n") {
		var row webTargetRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type arrival struct{ line, host string }
	arrivals, replies := make(chan arrival), make(chan string)
	served, quit := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(served)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			r := bufio.NewReader(conn)
			line, _ := r.ReadString('\n')
			host := ""
			for {
				h, err := r.ReadString('\n')
				if err != nil || h == "\r\n" {
					break
				}
				if name, value, ok := strings.Cut(h, ":"); ok && strings.EqualFold(name, "Host") {
					host = strings.TrimSpace(value)
				}
			}
			if line == "" {
				// A connection the client opened and then refused to
				// write on (a request it could not send).
				conn.Close()
				continue
			}
			select {
			case arrivals <- arrival{strings.TrimSuffix(line, "\r\n"), host}:
			case <-quit:
				conn.Close()
				return
			}
			select {
			case reply := <-replies:
				_, _ = conn.Write([]byte(reply))
			case <-quit:
			}
			conn.Close()
		}
	}()
	defer func() { close(quit); ln.Close(); <-served }()
	host := ln.Addr().String()
	const ok = "HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	// Both network entry points send the same target: the buffered
	// HTTPFetch and the streamed HTTPFetchInto (its own exchange).
	fetches := []struct {
		name  string
		fetch func(raw string) error
	}{
		{"HTTPFetch", func(raw string) error {
			_, err := HTTPFetch(rt.Root("web target"), nil, true, serveLimits(128, 8192), 1, "GET", raw, nil, nil, nil, "")
			return err
		}},
		{"HTTPFetchInto", func(raw string) error {
			_, err := HTTPFetchInto(rt.Root("web target"), nil, true, serveLimits(128, 8192), 1, "GET", raw, nil, nil, nil, "", func(int) bool { return true }, func(*rt.Task, string) error { return nil })
			return err
		}},
	}
	for _, via := range fetches {
		for i, row := range rows {
			raw, first, location := row.Lead+row.Scheme+":"+row.Sep+host+row.Rest+row.Trail, row.Target, ""
			if row.Location != "" {
				raw, first, location = "http://"+host+row.Base, row.BaseTarget, row.Location
			}
			fail := func(format string, args ...any) {
				t.Helper()
				t.Fatalf("%s, row %d of testdata/webtarget/targets.jsonl %+v: "+format+"\nreplay: go test -run '^TestHTTPFetchSendsTheWebTarget$' ./runtime/v1", append([]any{via.name, i + 1, row}, args...)...)
			}
			done := make(chan error, 1)
			go func() { done <- via.fetch(raw) }()
			// want is each request the URL makes: the base, then the redirect.
			want := []string{first}
			if location != "" {
				want = append(want, row.Target)
			}
			for n, target := range want {
				if !webCarried(target) {
					select {
					case err := <-done:
						var e HTTPSendFailed
						if !errors.As(err, &e) || e.Kind != httpMalformed {
							fail("request %d: want Malformed for a // target Go cannot carry, got %v", n, err)
						}
					case a := <-arrivals:
						replies <- ok
						fail("request %d: sent %q, want Malformed", n, a.line)
					case <-testwait.After(t):
						fail("request %d: no refusal", n)
					}
					break
				}
				select {
				case a := <-arrivals:
					reply := ok
					if n == 0 && location != "" {
						reply = "HTTP/1.1 307 Temporary Redirect\r\nLocation: " + location + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
					}
					replies <- reply
					if a.line != "GET "+target+" HTTP/1.1" || a.host != host {
						fail("request %d: sent %q with Host %q, want %q with Host %q", n, a.line, a.host, "GET "+target+" HTTP/1.1", host)
					}
				case err := <-done:
					fail("request %d: ended before sending: %v", n, err)
				case <-testwait.After(t):
					fail("request %d: nothing arrived", n)
				}
				if n == len(want)-1 {
					select {
					case err := <-done:
						if err != nil {
							fail("the fetch failed: %v", err)
						}
					case <-testwait.After(t):
						fail("the fetch did not end")
					}
				}
			}
		}
	}
}
