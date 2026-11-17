package http

import (
	"bufio"
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
// add is the server's. ergo is Serve with a handler that echoes the body;
// go is hand net/http with the same guarantees (bounded header and body,
// the phase timeouts, cancelled in-flight work, a joined shutdown), as
// BenchmarkHTTPServer's control.
func BenchmarkHTTPServeRaw(b *testing.B) {
	for _, mode := range []string{"ergo", "go"} {
		b.Run(mode, func(b *testing.B) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			if mode == "ergo" {
				s := v1.NewScope(rt.Root("HTTP raw benchmark"))
				h := v1.Go(&s, "Serve", func(ct *rt.Task) (struct{}, error) {
					return struct{}{}, HTTPServe("request", ct, liveSleeper{}, ergonet.ListenerOn(ln), serveLimits(4096, 4096), func(_ *rt.Task, _ *Exchange, _, _, _ string, _, _ []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
						if tooLarge || readError != "" {
							return HTTPResponse{Status: 400}
						}
						return HTTPResponse{Status: 200, Body: body}
					})
				})
				b.Cleanup(h.Stop)
			} else {
				b.Cleanup(rawControl(ln))
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
