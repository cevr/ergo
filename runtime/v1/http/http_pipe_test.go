package http

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// TestHTTPServePipeMatchesLoopback holds Serve over a nettest.Pipe
// listener equal to Serve over a loopback one: the same raw bytes, sent on
// one connection to each (one to three requests written at once, the last
// asking to close, sometimes a malformed one last), give the same response
// bytes, Date aside. The handler answers from what it was given. Seeded:
// a failure prints the requests, both responses and the replay command
// (ERGO_HTTP_PIPE_SEED=N).
func TestHTTPServePipeMatchesLoopback(t *testing.T) {
	limits := serveLimits(4096, 4096)
	handle := func(_ *rt.Task, _ *Exchange, method, path, rawPath string, query, header []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
		status := 200
		var echoed []HTTPField
		for _, f := range header {
			switch {
			case strings.EqualFold(f.Name, "X-Status"):
				status, _ = strconv.Atoi(f.Value)
			case strings.HasPrefix(strings.ToLower(f.Name), "x-"):
				echoed = append(echoed, HTTPField{Name: "Echo-" + f.Name, Value: f.Value})
			}
		}
		return HTTPResponse{Status: status, Header: echoed, Body: fmt.Sprintf("%s %s %s %v %v %q %v %q", method, path, rawPath, query, header, body, tooLarge, readError)}
	}
	root := rt.Root(t.Name())
	host, err := HTTPListen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, root, liveSleeper{}, host, limits, handle)
	pn := ergonet.NetNewPipe()
	pipe, err := pn.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, root, liveSleeper{}, pipe, limits, handle)
	client := rt.Root(t.Name() + " client")

	seeds := []uint64{}
	if s := os.Getenv("ERGO_HTTP_PIPE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for s := range uint64(64) {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		raw := pipeRequests(rand.New(rand.NewPCG(seed, 59)))
		hc, err := ergonet.NetDial(client, host.Addr())
		if err != nil {
			t.Fatal(err)
		}
		pc, err := pn.Dial(pipe.Addr())
		if err != nil {
			t.Fatal(err)
		}
		onHost, inPipe := exchangeRaw(client, hc, raw), exchangeRaw(client, pc, raw)
		if onHost != inPipe {
			t.Fatalf("seed %d (replay ERGO_HTTP_PIPE_SEED=%d go test -run TestHTTPServePipeMatchesLoopback ./runtime/v1/http):\nsent    %q\nloopback %q\npipe     %q", seed, seed, raw, onHost, inPipe)
		}
	}
}

// exchangeRaw writes raw on c and reads until the server closes; the
// response has its Date lines taken out, and a failed read ends it with
// its reason.
func exchangeRaw(t *rt.Task, c *ergonet.Conn, raw string) string {
	return exchangeRawAgainst(t, c, raw, nil)
}

// exchangeRawAgainst is exchangeRaw that, given the reference's response
// (want), stops at the first read after which the whole lines received
// are no prefix of want, and returns them with what came after: a server
// that answers otherwise than the reference is told by its bytes as they
// arrive, not by waiting for a close it may never make (the engine that
// misses a Connection: close keeps the connection open, c8-connection-unseen).
func exchangeRawAgainst(t *rt.Task, c *ergonet.Conn, raw string, want *string) string {
	defer c.Close()
	if err := c.Write(t, raw); err != nil {
		return "write failed: " + err.Error()
	}
	var b strings.Builder
	for {
		s, err := c.Read(t)
		if err != nil {
			b.WriteString("\nread failed: " + err.Error())
			break
		}
		if s == "" {
			break
		}
		b.WriteString(s)
		if want != nil {
			text := b.String()
			whole := ""
			if i := strings.LastIndex(text, "\r\n"); i >= 0 {
				whole = text[:i+len("\r\n")]
			}
			if !strings.HasPrefix(*want, withoutDate(whole)) {
				return withoutDate(whole) + text[len(whole):] + "\n(no longer net/http's answer; not read on)"
			}
		}
	}
	return withoutDate(b.String())
}

// withoutDate is s with its Date lines taken out.
func withoutDate(s string) string {
	lines := strings.SplitAfter(s, "\r\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(l, "Date: ") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "")
}

// pipeRequests is one to three HTTP/1.1 requests, the last with
// Connection: close; a fourth of the time the last is malformed instead
// (no Host, a header line with no colon, a bad request line, a bad
// Content-Length). Bodies stay under the limits, so no close leaves bytes
// unread (on the host that is a reset whose timing loses bytes).
func pipeRequests(r *rand.Rand) string {
	text := func(n int, alphabet string) string {
		var b strings.Builder
		for range n {
			b.WriteByte(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	const pathBytes = "abcXYZ019-_.~%2F"
	var out strings.Builder
	n := 1 + r.IntN(3)
	for i := range n {
		last := i == n-1
		if last && r.IntN(4) == 0 {
			out.WriteString([]string{
				"GET / HTTP/1.1\r\nConnection: close\r\n\r\n",
				"GET / HTTP/1.1\r\nHost: x\r\nno colon here\r\n\r\n",
				"GET\r\n\r\n",
				"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: x\r\n\r\n",
			}[r.IntN(4)])
			break
		}
		method := []string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"}[r.IntN(6)]
		path := "/" + text(r.IntN(8), pathBytes)
		if r.IntN(2) == 0 {
			path += "?" + text(r.IntN(4), "ab") + "=" + text(r.IntN(4), "xy+%20")
		}
		fmt.Fprintf(&out, "%s %s HTTP/1.1\r\nHost: x\r\n", method, path)
		fmt.Fprintf(&out, "X-Status: %d\r\n", []int{200, 201, 204, 301, 304, 404, 500}[r.IntN(7)])
		for range r.IntN(3) {
			fmt.Fprintf(&out, "X-%s: %s\r\n", text(1+r.IntN(3), "abc"), text(r.IntN(6), "pq r"))
		}
		body := text(r.IntN(40), "body ")
		switch {
		case body == "" || method == "GET" && r.IntN(2) == 0:
			body = ""
		case r.IntN(3) == 0:
			out.WriteString("Transfer-Encoding: chunked\r\n")
			if last {
				out.WriteString("Connection: close\r\n")
			}
			fmt.Fprintf(&out, "\r\n%x\r\n%s\r\n0\r\n\r\n", len(body), body)
			continue
		default:
			fmt.Fprintf(&out, "Content-Length: %d\r\n", len(body))
		}
		if last {
			out.WriteString("Connection: close\r\n")
		}
		out.WriteString("\r\n" + body)
	}
	return out.String()
}
