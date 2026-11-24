package http

import (
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

// BenchmarkHTTPServer interleaves the buffered child-task server and an
// idiomatic net/http control, in alternating order. Both bind loopback,
// cap headers and bodies, echo the buffered bytes, cancel in-flight work
// and join shutdown. The Go control uses request contexts and a joined
// worker lifetime instead of Ergo tasks. Tracing is off. Run on a quiet
// host, not a gate; no parity claim rests on the unmeasured benchmark.
func BenchmarkHTTPServer(b *testing.B) {
	// The -pipe modes serve in memory: Ergo a nettest.Pipe listener, the
	// Go control a listener of Go's net.Pipe connections.
	modes := []string{"ergo", "go", "ergo-pipe", "go-pipe"}
	for pair := range 4 {
		order := append(modes[pair:len(modes):len(modes)], modes[:pair]...)
		for _, mode := range order {
			b.Run(strconv.Itoa(pair)+"/"+mode, func(b *testing.B) {
				var ln net.Listener
				var el *ergonet.Listener
				transport := &http.Transport{Proxy: nil}
				switch mode {
				case "ergo-pipe":
					pn := ergonet.NetNewPipe()
					var err error
					if el, err = pn.Listen("127.0.0.1:80"); err != nil {
						b.Fatal(err)
					}
					transport.DialContext = func(context.Context, string, string) (net.Conn, error) { return pn.GoDial("127.0.0.1:80") }
				case "go-pipe":
					cl := &chanListener{conns: make(chan net.Conn), done: make(chan struct{})}
					ln = cl
					transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
						client, server := net.Pipe()
						select {
						case cl.conns <- server:
							return client, nil
						case <-cl.done:
							return nil, net.ErrClosed
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
				default:
					var err error
					if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
						b.Fatal(err)
					}
					el = ergonet.ListenerOn(ln)
				}
				if mode == "ergo" || mode == "ergo-pipe" {
					root := rt.Root("HTTP benchmark")
					s := v1.NewScope(root)
					h := v1.Go(&s, "Serve", func(ct *rt.Task) (struct{}, error) {
						return struct{}{}, HTTPServe("request", ct, liveSleeper{}, el, serveLimits(4096, 4096), func(_ *rt.Task, _ *Exchange, _, _, _ string, _, _ []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
							if tooLarge || readError != "" {
								return HTTPResponse{Status: 400}
							}
							return HTTPResponse{Status: 200, Body: body}
						})
					})
					b.Cleanup(func() { h.Stop(root) })
				} else {
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
					b.Cleanup(func() {
						mu.Lock()
						closing = true
						mu.Unlock()
						cancel()
						_ = srv.Close()
						workers.Wait()
						<-done
					})
				}
				b.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport}
				url := "http://127.0.0.1:80/echo"
				if ln != nil && mode != "go-pipe" {
					url = "http://" + ln.Addr().String() + "/echo"
				}
				body := strings.Repeat("x", 256)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					r, err := client.Post(url, "text/plain", strings.NewReader(body))
					if err != nil {
						b.Fatal(err)
					}
					n, err := io.Copy(io.Discard, r.Body)
					r.Body.Close()
					if err != nil || r.StatusCode != 200 || n != int64(len(body)) {
						b.Fatalf("response %d: %v", r.StatusCode, err)
					}
				}
			})
		}
	}
}

// chanListener is the Go control's in-memory listener: Accept takes the
// server ends of net.Pipe pairs the client's dial makes.
type chanListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *chanListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80} }
