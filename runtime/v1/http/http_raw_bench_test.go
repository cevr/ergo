package http

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
	ergonet "ergo.local/prototype/runtime/v1/net"
)

// BenchmarkHTTPServeRaw counts the server's work per request: one
// kept-alive loopback connection, a raw POST of 256 bytes written and its
// echoed answer read whole per op, with no HTTP client, so what both modes
// add is the server's. ergo is Serve with a handler that echoes the body,
// reading with its engine; ergo-eager is the engine with the background
// read started before every handler, as net/http starts it; ergo-entrypoll
// is ergo with a handler that polls (Check) at entry, as every lowered
// Ergo handler does through std's http.Serve, and ergo-entrypoll-read the
// same with net/http's background read for the server's peer-close
// watcher; ergo-port is the engine with every answer
// through the port of net/http's writer (fastAnswer off); ergo-nethttp
// is the same Serve on its reference reader, net/http;
// go is hand net/http with the same guarantees (bounded header and body,
// the phase timeouts, cancelled in-flight work, a joined shutdown), as
// BenchmarkHTTPServer's control; floor is the least work the Go runtime
// allows for the exchange (rawFloor). Select one mode with an anchored
// pattern, -bench 'BenchmarkHTTPServeRaw/^go$': an unanchored "go$" also
// matches "ergo".
func BenchmarkHTTPServeRaw(b *testing.B) {
	for _, mode := range []string{"ergo", "ergo-eager", "ergo-entrypoll", "ergo-entrypoll-read", "ergo-port", "ergo-nethttp", "go", "floor"} {
		b.Run(mode, func(b *testing.B) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			if mode == "ergo-port" {
				// The engine with every answer through the port of
				// net/http's writer (HTTP-FAST-c1's writer).
				engineFastWriter = false
				b.Cleanup(func() { engineFastWriter = true })
			}
			if mode == "ergo-eager" {
				// The engine with net/http's background read before every
				// handler (HTTP-FAST-c2's watch).
				engineLazyWatch = false
				b.Cleanup(func() { engineLazyWatch = true })
			}
			// ergo-entrypoll's handler polls once at entry, as std's
			// http.Serve wrapper does before every lowered Ergo handler
			// (HTTP-FAST-c5's measurement).
			entryPoll := mode == "ergo-entrypoll" || mode == "ergo-entrypoll-read"
			if mode == "ergo-entrypoll-read" {
				// The background read in place of the server's
				// peer-close watcher (HTTP-FAST-c5's watch).
				engineWatcher = false
				b.Cleanup(func() { engineWatcher = true })
			}
			if mode != "go" && mode != "floor" {
				s := v1.NewScope(rt.Root("HTTP raw benchmark"))
				h := v1.Go(&s, "Serve", func(ct *rt.Task) (struct{}, error) {
					return struct{}{}, httpServe("request", ct, liveSleeper{}, ergonet.ListenerOn(ln), serveLimits(4096, 4096), func(ct *rt.Task, _ *Exchange, _, _, _ string, _, _ []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
						if entryPoll {
							ct.Check()
						}
						if tooLarge || readError != "" {
							return HTTPResponse{Status: 400}
						}
						return HTTPResponse{Status: 200, Body: body}
					}, mode != "ergo-nethttp")
				})
				b.Cleanup(h.Stop)
			} else if mode == "go" {
				b.Cleanup(rawControl(ln))
			} else {
				b.Cleanup(rawFloor(ln))
			}
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				b.Fatal(err)
			}
			defer c.Close()
			body := strings.Repeat("x", 256)
			req := []byte("POST /echo HTTP/1.1\r\nHost: bench\r\nContent-Type: text/plain\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
			// The answer's length, from one parsed exchange: the Date line
			// keeps one width, so every answer has it.
			if _, err := c.Write(req); err != nil {
				b.Fatal(err)
			}
			counted := &countingReader{r: c}
			res, err := http.ReadResponse(bufio.NewReaderSize(counted, 16), nil)
			if err != nil {
				b.Fatal(err)
			}
			got, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 || string(got) != body {
				b.Fatalf("answer %d %q", res.StatusCode, got)
			}
			buf := make([]byte, counted.n)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := c.Write(req); err != nil {
					b.Fatal(err)
				}
				if _, err := io.ReadFull(c, buf); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// rawFloor serves ln with the least work the Go runtime allows for this
// exchange (GO-RUNTIME FLOOR): a goroutine per connection over net.Conn,
// one read deadline per request, the head scanned in a reused buffer for
// its end and Content-Length (bounded at 4096 bytes, as the others), and
// the answer, with the Date line HTTP asks for, written whole in one
// Write. No http.Request, header map, context or bufio. Its shutdown
// closes the listener and every connection and joins their goroutines.
var (
	headEnd       = []byte("\r\n\r\n")
	crlf          = []byte("\r\n")
	colon         = []byte(":")
	contentLength = []byte("Content-Length")
)

func rawFloor(ln net.Listener) func() {
	var mu sync.Mutex
	var workers sync.WaitGroup
	conns := map[net.Conn]bool{}
	serve := func(c net.Conn) {
		defer workers.Done()
		defer c.Close()
		buf := make([]byte, 8192)
		out := make([]byte, 0, 1024)
		n := 0
		for {
			_ = c.SetReadDeadline(time.Now().Add(time.Hour))
			end := -1
			for end < 0 {
				if i := bytes.Index(buf[:n], headEnd); i >= 0 {
					end = i + 4
					break
				}
				if n >= 4096 {
					return
				}
				m, err := c.Read(buf[n:4096])
				if err != nil {
					return
				}
				n += m
			}
			cl := 0
			_, head, _ := bytes.Cut(buf[:end-2], crlf) // past the request line
			for len(head) > 0 {
				var line []byte
				line, head, _ = bytes.Cut(head, crlf)
				if k, v, ok := bytes.Cut(line, colon); ok && bytes.EqualFold(k, contentLength) {
					cl = 0
					for _, d := range bytes.TrimSpace(v) {
						cl = cl*10 + int(d-'0')
					}
				}
			}
			if cl > 4096 {
				return
			}
			for n < end+cl {
				m, err := c.Read(buf[n:])
				if err != nil {
					return
				}
				n += m
			}
			out = append(out[:0], "HTTP/1.1 200 OK\r\nContent-Length: "...)
			out = strconv.AppendInt(out, int64(cl), 10)
			out = append(out, "\r\nContent-Type: text/plain; charset=utf-8\r\nDate: "...)
			out = time.Now().UTC().AppendFormat(out, http.TimeFormat)
			out = append(out, "\r\n\r\n"...)
			out = append(out, buf[end:end+cl]...)
			if _, err := c.Write(out); err != nil {
				return
			}
			n = copy(buf, buf[end+cl:n])
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[c] = true
			workers.Add(1)
			mu.Unlock()
			go serve(c)
		}
	}()
	return func() {
		_ = ln.Close()
		<-done
		mu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		workers.Wait()
	}
}

// rawControl serves ln with hand net/http, BenchmarkHTTPServer's control,
// and returns its joined shutdown.
func rawControl(ln net.Listener) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var workers sync.WaitGroup
	closing := false
	srv := &http.Server{MaxHeaderBytes: 4096, ReadHeaderTimeout: time.Hour, ReadTimeout: time.Hour, IdleTimeout: time.Hour, BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if closing {
			mu.Unlock()
			panic(http.ErrAbortHandler)
		}
		workers.Add(1)
		mu.Unlock()
		defer workers.Done()
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(body)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	return func() {
		mu.Lock()
		closing = true
		mu.Unlock()
		cancel()
		_ = srv.Close()
		workers.Wait()
		<-done
	}
}
