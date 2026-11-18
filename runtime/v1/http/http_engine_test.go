package http

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// -http.engine=false runs the package's tests (the servers serveOn
// starts) with net/http reading every connection, the reference path;
// true serves through HTTPServe, so its default reader (serveEngine).
var engineFlag = flag.Bool("http.engine", true, "Serve reads requests with its own engine (false: net/http, the reference)")

// serveWith serves l as serveOn does, with the reader chosen.
func serveWith(t *testing.T, root *rt.Task, l *ergonet.Listener, limits HTTPLimits, handle httpHandler, engine bool) {
	t.Helper()
	holder := rt.NewScope(root)
	h := &serverTask{h: rt.Go(&holder, "http engine test server", func(ct *rt.Task) (struct{}, error) {
		return v1.Guarded(ct, func(ct *rt.Task) (struct{}, error) {
			return struct{}{}, httpServe("http engine test request", ct, liveSleeper{}, l, limits, handle, engine)
		})
	})}
	t.Cleanup(func() { h.Stop(); l.Close() })
}

// answerFields are the response fields a request asks for by index
// (X-Head): the ones net/http's writer reads (length, framing, the
// connection, trailers, the type and its encoding, the date) with right,
// wrong and invalid values, and fields it only writes.
var answerFields = []HTTPField{
	{"Content-Type", "text/plain"}, {"Content-Length", "5"}, {"Content-Length", "0"}, {"Content-Length", "x"},
	{"Transfer-Encoding", "chunked"}, {"Transfer-Encoding", "identity"}, {"Transfer-Encoding", "gzip"},
	{"Connection", "close"}, {"Connection", "keep-alive"}, {"Connection", "Upgrade"}, {"Upgrade", "websocket"},
	{"Trailer", "X-T, Content-Length, Cache-Control"}, {"X-T", "t"}, {"Trailer:X-P", "p"}, {"Date", "then"},
	{"Content-Encoding", "gzip"}, {"x-lower", "v"}, {"Bad Name", "v"}, {"X-Multi", "a\r\nb"}, {"Cache-Control", "no-store"},
	{"X-Spaced", " v\t"}, {"Content-Type", ""},
}

var answerStatuses = []int{100, 101, 103, 200, 201, 204, 206, 299, 304, 404, 500, 799}

// bodyStarts are what net/http's sniffing tells apart.
var bodyStarts = []string{"", "<html>", "{\"a\":1}", "%PDF-", "\x00\x01\x02"}

// engineHandler answers from what the request asks (X-Status, X-Head,
// X-Body: start,size, X-Stream: parts, X-Empty: no body) with a body that holds every
// argument Serve gave it, so the property compares the reader's
// arguments as well as the writer's bytes.
func engineHandler(t *rt.Task, x *Exchange, method, path, rawPath string, query, header []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
	status, start, size, parts := 200, 0, 0, -1
	empty := false
	var fields []HTTPField
	for _, f := range header {
		n, _ := strconv.Atoi(f.Value)
		switch f.Name {
		case "X-Status":
			status = n
		case "X-Head":
			fields = append(fields, answerFields[n%len(answerFields)])
		case "X-Body":
			a, b, _ := strings.Cut(f.Value, ",")
			start, _ = strconv.Atoi(a)
			size, _ = strconv.Atoi(b)
		case "X-Stream":
			parts = n
		case "X-Empty":
			empty = true
		}
	}
	text := bodyStarts[start%len(bodyStarts)] + fmt.Sprintf("%s %s %s %q %q %q %v %q|", method, path, rawPath, query, header, body, tooLarge, readError)
	text += strings.Repeat("0123456789abcdef", size/16+1)[:size]
	if empty {
		text = ""
	}
	if parts < 0 {
		return HTTPResponse{Status: status, Header: fields, Body: text}
	}
	x.Begin(status, fields)
	for i := range parts {
		_ = x.Write(t, text[i*len(text)/parts:(i+1)*len(text)/parts])
	}
	return HTTPResponse{}
}

// TestHTTPEngineMatchesNetHTTP holds Serve's engine (serveConn and
// engineResponse) equal to net/http, the reference: the same raw bytes,
// sent on one connection to a pipe server of each, give the same
// response bytes, Date aside, and the handler (engineHandler) the same
// arguments. The input is one to three requests, the last asking to
// close: ones the engine reads, with escapes, sub-delims and queries in
// the target, names in any case, spaces around values, repeated names,
// Connection tokens and bodies; and ones it hands to net/http (chunked,
// Expect, HTTP/1.0, other methods and targets, a missing or second Host,
// two or bad lengths, a body over the limit, Pragma, Trailer, bare LF,
// folded lines, non-ASCII, a head over its buffer, a CRLF after a POST).
// Each asks for an answer: any status (interim, protocol switch, no
// body, unknown), fields the writer reads, a sniffed body around the
// 2 KiB buffer, or a stream of parts. Seeded: a failure prints the
// requests, both responses and the replay command
// (ERGO_HTTP_ENGINE_SEED=N).
func TestHTTPEngineMatchesNetHTTP(t *testing.T) {
	limits := serveLimits(4096, 4096)
	// The engine's answers are counted by writer, so the property is known
	// to reach both fastAnswer and the port it falls back to.
	var fast, port atomic.Int64
	engineFastHook = func(f bool) {
		if f {
			fast.Add(1)
		} else {
			port.Add(1)
		}
	}
	t.Cleanup(func() { engineFastHook = nil })
	root := rt.Root(t.Name())
	pn := ergonet.NetNewPipe()
	listen := func(addr string) *ergonet.Listener {
		l, err := pn.Listen(addr)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	reference, engine := listen("127.0.0.1:1"), listen("127.0.0.1:2")
	serveWith(t, root, reference, limits, engineHandler, false)
	serveWith(t, root, engine, limits, engineHandler, true)
	client := rt.Root(t.Name() + " client")

	seeds := []uint64{}
	if s := os.Getenv("ERGO_HTTP_ENGINE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for s := range uint64(256) {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		raw := engineRequests(rand.New(rand.NewPCG(seed, 61)))
		rc, err := pn.Dial(reference.Addr())
		if err != nil {
			t.Fatal(err)
		}
		ec, err := pn.Dial(engine.Addr())
		if err != nil {
			t.Fatal(err)
		}
		want := exchangeRaw(client, rc, raw)
		got := exchangeRawAgainst(client, ec, raw, &want)
		if want != got {
			t.Fatalf("seed %d (replay ERGO_HTTP_ENGINE_SEED=%d go test -run TestHTTPEngineMatchesNetHTTP ./runtime/v1/http):\nsent     %q\nnet/http %q\nengine   %q", seed, seed, raw, want, got)
		}
	}
	if len(seeds) > 1 && *fastWriterFlag && (fast.Load() == 0 || port.Load() == 0) {
		t.Fatalf("the engine wrote %d answers through fastAnswer and %d through the port; the property must reach both", fast.Load(), port.Load())
	}
	t.Logf("engine answers: %d fast, %d through the port", fast.Load(), port.Load())
}

// engineRequests is the input of one seed of TestHTTPEngineMatchesNetHTTP.
func engineRequests(r *rand.Rand) string {
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	text := func(n int, alphabet string) string {
		var b strings.Builder
		for range n {
			b.WriteByte(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	var out strings.Builder
	n := 1 + r.IntN(3)
	for i := range n {
		last := i == n-1
		method := pick("GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS")
		var path strings.Builder
		path.WriteByte('/')
		for range r.IntN(6) {
			path.WriteString(pick("a", "Z", "0", "-", ".", "_", "~", "/", "%2F", "%41", "%7e", "!", "$", "&", "'", "(", ")", "*", "+", ",", ";", "=", ":", "@"))
		}
		target := path.String()
		if r.IntN(2) == 0 {
			target += "?" + text(r.IntN(4), "ab") + "=" + text(r.IntN(4), "xy+%20&=")
		}
		version := "HTTP/1.1"
		var lines []string
		lines = append(lines, pick("Host: x", "host: example.com:8080", "HOST:x", "Host:  [::1]:80  "))
		// Half the answers are common ones, which the engine writes in one
		// write (fastAnswer): a status that may have a body, fields its
		// writer only writes, and a body up to just past 2 KiB.
		common := r.IntN(2) == 0
		status := answerStatuses[r.IntN(len(answerStatuses))]
		if common {
			status = []int{200, 201, 206, 299, 404, 500, 799}[r.IntN(7)]
		}
		lines = append(lines, fmt.Sprintf("%s: %d", pick("X-Status", "x-status"), status))
		for range r.IntN(4) {
			head := r.IntN(len(answerFields))
			if common {
				head = []int{0, 10, 12, 15, 16, 18, 19, 20, 21}[r.IntN(9)]
			}
			lines = append(lines, fmt.Sprintf("X-Head: %d", head))
		}
		if common && r.IntN(4) == 0 {
			lines = append(lines, "X-Empty: 1")
		}
		if r.IntN(8) == 0 {
			// A Trailer naming a field the answer has, that no trailer may be.
			lines = append(lines, "X-Head: 11", "X-Head: 19")
		}
		lines = append(lines, fmt.Sprintf("X-Body: %d,%d", r.IntN(len(bodyStarts)), []int{0, 3, 100, 2000, 2047, 2048, 2100, 5000}[r.IntN(8)]))
		if r.IntN(4) == 0 {
			lines = append(lines, fmt.Sprintf("X-Stream: %d", r.IntN(4)))
		}
		for range r.IntN(4) {
			lines = append(lines, pick("X-", "x-", "X-A-", "accept-")+text(1+r.IntN(3), "abcAB")+":"+pick("", " ", "\t", "  ")+text(r.IntN(6), "pq r,\t")+pick("", " ", "\t"))
		}
		if r.IntN(5) == 0 {
			lines = append(lines, "Connection: "+pick("keep-alive", "foo, close", "Close", "upgrade", "x close"))
		}
		body := ""
		if method != "GET" || r.IntN(2) == 0 {
			body = text(r.IntN(40), "body ")
		}
		framing := "Content-Length: " + strconv.Itoa(len(body))
		if body == "" && r.IntN(2) == 0 {
			framing = ""
		}
		lf := "\r\n"
		// A fourth of the requests take a shape the engine hands over.
		if r.IntN(4) == 0 {
			switch r.IntN(17) {
			case 0:
				framing = "Transfer-Encoding: chunked"
				if body == "" {
					body = "0\r\n\r\n" // the last chunk alone
				} else {
					body = fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
				}
			case 1:
				lines = append(lines, "Expect: 100-continue")
			case 2:
				version = "HTTP/1.0"
			case 3:
				method = "get"
			case 4:
				target = "http://x" + target
			case 5:
				lines = lines[1:]
			case 6:
				lines = append(lines, "Host: y")
			case 7:
				framing = "Content-Length: 5000"
				body = strings.Repeat("o", 5000)
			case 8:
				lines = append(lines, "Pragma: no-cache")
			case 9:
				lines = append(lines, "Trailer: X-T")
			case 10:
				lf = "\n"
			case 11:
				lines = append(lines, "X-Fold: a", " b")
			case 12:
				lines = append(lines, "X-Name: caf\xc3\xa9")
			case 13:
				lines = append(lines, "X-Pad: "+strings.Repeat("p", 4100))
			case 14:
				if framing != "" {
					lines = append(lines, framing)
				}
			case 15:
				framing = "Content-Length: +" + strconv.Itoa(len(body))
			case 16:
				method, target = "OPTIONS", "*"
			}
		}
		if framing != "" {
			lines = append(lines, framing)
		}
		if last {
			lines = append(lines, "Connection: close")
		}
		fmt.Fprintf(&out, "%s %s %s%s%s%s%s", method, target, version, lf, strings.Join(lines, lf), lf, lf)
		out.WriteString(body)
		if method == "POST" && r.IntN(3) == 0 {
			out.WriteString(pick("\r\n", "\n", "\r\n\r\n"))
		}
	}
	return out.String()
}

// TestHTTPEngineWatchesWaitingHandlers counts the engine's background
// reads, the watch that sees a client leave (HTTP-FAST-c3, c4): a handler
// that answers without an interruption point starts none, since nothing
// could stop it, and a handler that reaches one (its task's Done, Check or
// Stopped) starts one, so a client that leaves still stops it
// (TestHTTPDisconnectAndDefects and TestHTTPCheckingHandlerStopsWhenClientLeaves
// hold the stop itself). Mutants: c3-lazy-never, c3-watch-never,
// c4-check-unpolled, c4-stopped-unpolled (runtime/v1/http ledger).
func TestHTTPEngineWatchesWaitingHandlers(t *testing.T) {
	if !*lazyWatchFlag {
		t.Skip("the reference watches every request, as net/http does")
	}
	var watches atomic.Int64
	engineWatchHook = func() { watches.Add(1) }
	t.Cleanup(func() { engineWatchHook = nil })
	root := rt.Root(t.Name())
	pn := ergonet.NetNewPipe()
	l, err := pn.Listen("127.0.0.1:3")
	if err != nil {
		t.Fatal(err)
	}
	serveWith(t, root, l, serveLimits(4096, 4096), func(ct *rt.Task, _ *Exchange, _, path, _ string, _, _ []HTTPField, _ string, _ bool, _ string) HTTPResponse {
		if path == "/wait" {
			select {
			case <-ct.Done():
			default:
			}
		}
		if path == "/check" {
			ct.Check()
		}
		if path == "/stopped" && ct.Stopped() {
			return HTTPResponse{Status: 500}
		}
		return HTTPResponse{Status: 200, Body: path}
	}, true)
	client := rt.Root(t.Name() + " client")
	for _, c := range []struct {
		paths   []string
		watches int64
	}{
		{[]string{"/a", "/b", "/c"}, 0},
		{[]string{"/a", "/wait", "/b"}, 1},
		{[]string{"/check", "/a", "/check"}, 2},
		{[]string{"/a", "/stopped"}, 1},
	} {
		watches.Store(0)
		var raw strings.Builder
		for i, p := range c.paths {
			raw.WriteString("GET " + p + " HTTP/1.1\r\nHost: x\r\n")
			if i == len(c.paths)-1 {
				raw.WriteString("Connection: close\r\n")
			}
			raw.WriteString("\r\n")
		}
		conn, err := pn.Dial(l.Addr())
		if err != nil {
			t.Fatal(err)
		}
		got := exchangeRaw(client, conn, raw.String())
		if n := strings.Count(got, "HTTP/1.1 200 OK"); n != len(c.paths) {
			t.Fatalf("%v: %d answers, want %d:\n%s", c.paths, n, len(c.paths), got)
		}
		if n := watches.Load(); n != c.watches {
			t.Errorf("%v: the engine started %d background reads, want %d", c.paths, n, c.watches)
		}
	}
}

// TestEngineDateIsNetHTTPs: the Date value fastAnswer caches per second is
// the one net/http formats for the same time (time.Now().UTC() in
// http.TimeFormat), over a seeded walk of times that stays in a second,
// steps to the next one, jumps, and comes in another zone. The property
// leaves Date aside, so this law holds it. Mutants: c2-date-stale,
// c2-date-local (runtime/v1/http ledger).
func TestEngineDateIsNetHTTPs(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 61))
	zone := time.FixedZone("X", -5*3600)
	now := time.Unix(1_700_000_000, 0)
	for i := range 2000 {
		switch r.IntN(4) {
		case 0:
			now = now.Add(time.Duration(r.IntN(int(time.Second))))
		case 1:
			now = now.Truncate(time.Second).Add(time.Second)
		case 2:
			now = now.Add(time.Duration(r.Int64N(int64(400 * 24 * time.Hour))))
		case 3:
			now = now.In(zone)
		}
		if got, want := string(appendEngineDate(nil, now)), now.UTC().Format(http.TimeFormat); got != want {
			t.Fatalf("step %d, %v: Date %q, net/http writes %q", i, now, got, want)
		}
	}
}

// sniffStarts are bodies' first bytes that net/http's sniff signatures
// look for, one or more per signature, so the property's bodies reach
// each of them ahead of text's.
var sniffStarts = []string{
	"<!DOCTYPE HTML ", "<!doctype html>", "<html>", "<HEAD ", "<script>", "<iframe>", "<h1>", "<div>",
	"<font>", "<table>", "<a ", "<style>", "<title>", "<b>", "<body>", "<br>", "<p>", "<!-->", "<pX",
	"<?xml", "%PDF-", "%!PS-Adobe-", "\xFE\xFF\x00\x00", "\xFF\xFE\x00\x00", "\xEF\xBB\xBF", "\x00\x00\x01\x00",
	"\x00\x00\x02\x00", "BM", "GIF87a", "GIF89a", "RIFF\x01\x02\x03\x04WEBPVP", "\x89PNG\x0D\x0A\x1A\x0A",
	"\xFF\xD8\xFF", "FORM\x00\x00\x00\x00AIFF", "ID3", "OggS\x00", "MThd\x00\x00\x00\x06",
	"RIFF\x00\x01\x00\x00AVI ", "RIFF\x00\x00\x00\x00WAVE", "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00isom",
	"\x00\x00\x00\x14ftypisom\x00\x00\x00\x00mp41", "\x1A\x45\xDF\xA3", strings.Repeat("e", 34) + "LP",
	"\x00\x01\x00\x00", "OTTO", "ttcf", "wOFF", "wOF2", "\x1F\x8B\x08", "PK\x03\x04", "Rar!\x1A\x07\x00",
	"Rar!\x1A\x07\x01\x00", "\x00\x61\x73\x6D",
}

// sniffBody is the body of one seed of TestSniffIsNetHTTPs: leading
// whitespace (sometimes past the 512 bytes sniffed), a signature's
// start or none, and a tail of text with now and then a byte text's
// signature refuses, anywhere in it.
func sniffBody(r *rand.Rand) string {
	var b strings.Builder
	switch r.IntN(8) {
	case 0, 1, 2:
	case 3:
		for range 440 + r.IntN(90) {
			b.WriteByte(" \t\n\x0c\r"[r.IntN(5)])
		}
	default:
		for range 1 + r.IntN(4) {
			b.WriteByte(" \t\n\x0c\r"[r.IntN(5)])
		}
	}
	if r.IntN(4) != 0 {
		b.WriteString(sniffStarts[r.IntN(len(sniffStarts))])
	}
	tail := make([]byte, r.IntN(600))
	for i := range tail {
		switch n := r.IntN(40); {
		case n == 0:
			tail[i] = "\t\n\x0c\r\x1b"[r.IntN(5)]
		case n == 1:
			tail[i] = byte(0x80 + r.IntN(0x80))
		default:
			tail[i] = byte(0x20 + r.IntN(0x5f))
		}
	}
	if len(tail) > 0 && r.IntN(3) == 0 {
		refused := "\x00\x01\x08\x0b\x0e\x1a\x1c\x1f"
		tail[r.IntN(len(tail))] = refused[r.IntN(len(refused))]
	}
	b.Write(tail)
	return b.String()
}

// TestSniffIsNetHTTPs: the type fastAnswer sniffs (sniffType) is
// net/http's DetectContentType of the same body, over seeded pairs of
// bodies on one connection's sniff that reach each signature,
// whitespace past the sniffed 512 bytes, a refused byte before, in and
// after the head sniffType reads first, and a second body whose head is
// the first's or differs from it. The property counts (engineSniffHook)
// kept heads, heads that decided and whole sniffs, and needs each.
// Seeded: a failure prints both bodies, both types and the replay
// command (ERGO_HTTP_SNIFF_SEED=N). Mutants: c7-sniff-path-never,
// c7-sniff-head-short, c7-sniff-rest-unread, c7-sniff-word-skips,
// c7-sniff-refused-high, c7-sniff-kept-stale, c7-sniff-kept-never
// (runtime/v1/http ledger).
func TestSniffIsNetHTTPs(t *testing.T) {
	seeds := []uint64{}
	if s := os.Getenv("ERGO_HTTP_SNIFF_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for s := range uint64(20000) {
			seeds = append(seeds, s)
		}
	}
	hows := map[string]int{}
	engineSniffHook = func(how string) { hows[how]++ }
	defer func() { engineSniffHook = nil }()
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 67))
		var s engineSniff
		body := sniffBody(r)
		// The second body starts with some of the first, often its whole
		// head, so the connection's kept head is met equal and unequal.
		next := body[:r.IntN(len(body)+1)] + sniffBody(r)
		for _, b := range []string{body, next} {
			want := http.DetectContentType([]byte(b))
			if got := s.sniffType(b); got != want {
				t.Fatalf("seed %d (replay ERGO_HTTP_SNIFF_SEED=%d go test -run TestSniffIsNetHTTPs ./runtime/v1/http):\nfirst    %q\nbody     %q\nnet/http %s\nengine   %s", seed, seed, body, b, want, got)
			}
		}
	}
	if len(seeds) > 1 && *sniffHeadFlag && (hows["kept"] == 0 || hows["head"] == 0 || hows["whole"] == 0) {
		t.Fatalf("bodies sniffed: %v; the property must reach a kept head, a head that decides and a whole sniff", hows)
	}
	t.Logf("bodies sniffed: %v", hows)
}
