package http

import (
	"bufio"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// TestHTTPServeOverLimitEndsConnection holds Serve's half of a request over
// a budget, on a nettest.Pipe listener, where no kernel timing decides what
// the client reads (on loopback the server's close with bytes unread is a
// reset that may drop the answer, so TestHTTPServePipeMatchesLoopback sends
// none). A body over MaxBodyBytes (by Content-Length or chunked) reaches the
// handler as too large; its answer arrives whole and is the connection's
// last: a request written after it on the same connection gets no answer.
// A header over MaxHeaderBytes gets net/http's 431 and the connection ends.
// A body at the budget is the control: both requests are answered.
// Seeded; a failure prints the bytes sent, the bytes read and the replay
// (ERGO_HTTP_OVERLIMIT_SEED=N).
func TestHTTPServeOverLimitEndsConnection(t *testing.T) {
	const maxBody, maxHeader = 32, 4096
	handle := func(_ *rt.Task, _ *Exchange, _, path, _ string, _, _ []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
		if tooLarge {
			return HTTPResponse{Status: 413, Body: "too large " + path}
		}
		return HTTPResponse{Status: 200, Body: fmt.Sprintf("%s %q %q", path, body, readError)}
	}
	pn := ergonet.NetNewPipe()
	l, err := pn.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, rt.Root(t.Name()), liveSleeper{}, l, serveLimits(maxBody, maxHeader), handle)
	client := rt.Root(t.Name() + " client")

	seeds := []uint64{}
	if s := os.Getenv("ERGO_HTTP_OVERLIMIT_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		// 16 seeds: each refused one costs net/http's rstAvoidanceDelay
		// (500 ms) before it closes, as on the host.
		for s := range uint64(16) {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 61))
		kind := []string{"body", "chunked", "header", "control"}[seed%4]
		// big: more than the server reads ahead or discards (net/http drains up
		// to 256 KiB of an unread body) and the pipe holds (64 KiB) is left
		// unsent when the server refuses.
		big := seed%8 >= 4
		var raw strings.Builder
		var want []string // the statuses and bodies of the answers, in order
		switch kind {
		case "body", "chunked":
			body := strings.Repeat("b", maxBody+1+r.IntN(3*maxBody))
			if big {
				body = strings.Repeat("b", 1<<20+r.IntN(maxBody)) // past the 256 KiB net/http discards after a handler
			}
			if kind == "body" {
				fmt.Fprintf(&raw, "POST /big HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			} else {
				raw.WriteString("POST /big HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n")
				for rest := body; rest != ""; {
					n := min(len(rest), 1+r.IntN(maxBody))
					fmt.Fprintf(&raw, "%x\r\n%s\r\n", n, rest[:n])
					rest = rest[n:]
				}
				raw.WriteString("0\r\n\r\n")
			}
			want = []string{"413 too large /big"}
		case "header":
			fmt.Fprintf(&raw, "GET /big HTTP/1.1\r\nHost: x\r\nX-Big: %s\r\n\r\n", strings.Repeat("h", map[bool]int{false: 2*maxHeader + 4096, true: 128 << 10}[big]+r.IntN(maxHeader)))
			want = []string{"431 431 Request Header Fields Too Large"}
		case "control":
			body := strings.Repeat("c", maxBody)
			fmt.Fprintf(&raw, "POST /ok HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			want = []string{fmt.Sprintf("200 /ok %q \"\"", body), "200 /after \"\" \"\""}
		}
		// A request after the first: answered after the control, and after
		// a refused one not at all, since that connection is over.
		raw.WriteString("GET /after HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
		c, err := pn.Dial(l.Addr())
		if err != nil {
			t.Fatal(err)
		}
		got, end := overLimitExchange(client, c, raw.String())
		answers := overLimitAnswers(got)
		// A refused connection the client has sent more to than the server
		// read ahead is closed with bytes unread, which resets it after the
		// answer; a small refused one, whose bytes the server took into its
		// buffer, and the control, whose last request asked to close, end
		// cleanly.
		wantEnd := "net.Failed{Reason:connection reset by peer}"
		if kind == "control" || !big && kind != "header" {
			wantEnd = "eof"
		}
		if strings.Join(answers, "\n") != strings.Join(want, "\n") || end != wantEnd {
			t.Fatalf("seed %d (%s; replay ERGO_HTTP_OVERLIMIT_SEED=%d go test -run TestHTTPServeOverLimitEndsConnection ./runtime/v1/http):\nwant answers %q\ngot answers  %q\nwant end %s, got %s\nsent %d bytes: %.300q\nread %q", seed, kind, seed, want, answers, wantEnd, end, raw.Len(), raw.String(), got)
		}
	}
}

// overLimitExchange writes raw on c from its own task while it reads c to
// the end, so a server that stops reading never blocks the write, and
// returns what it read and how the read ended ("eof" or the failure).
func overLimitExchange(t *rt.Task, c *ergonet.Conn, raw string) (string, string) {
	defer c.Close()
	go func() { _ = c.Write(rt.Root("over-limit writer"), raw) }()
	var b strings.Builder
	for {
		s, err := c.Read(t)
		if err != nil {
			return b.String(), err.Error()
		}
		if s == "" {
			return b.String(), "eof"
		}
		b.WriteString(s)
	}
}

// overLimitAnswers parses the responses in got: "status body" each.
func overLimitAnswers(got string) []string {
	br := bufio.NewReader(strings.NewReader(got))
	var out []string
	for {
		res, err := http.ReadResponse(br, nil)
		if err != nil {
			return out
		}
		body, _ := io.ReadAll(res.Body)
		out = append(out, strconv.Itoa(res.StatusCode)+" "+strings.TrimSpace(string(body)))
	}
}
