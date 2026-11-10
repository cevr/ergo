package v1

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// BenchmarkHTTPServer interleaves the buffered child-task server and an
// idiomatic net/http control, in alternating order. Both bind loopback,
// cap headers and bodies, echo the buffered bytes and join shutdown.
// The control executes directly in net/http's worker, so the ratio also
// exposes the cost of Ergo's owned task boundary; it is not a claim of
// equal cancellation/trace contracts. Run on a quiet host, not a gate.
func BenchmarkHTTPServer(b *testing.B) {
	for pair := range 4 {
		order := []string{"ergo", "go"}
		if pair%2 != 0 {
			order[0], order[1] = order[1], order[0]
		}
		for _, mode := range order {
			b.Run(strconv.Itoa(pair)+"/"+mode, func(b *testing.B) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					b.Fatal(err)
				}
				if mode == "ergo" {
					s := NewScope(rt.Root("HTTP benchmark"))
					h := Go(&s, "Serve", func(ct *rt.Task) (struct{}, error) {
						return struct{}{}, HTTPServe("request", ct, &Listener{ln: ln}, HTTPLimits{4096, 4096}, func(_ *rt.Task, _, _ string, _, _ []HTTPField, body string, tooLarge bool, readError string) HTTPResponse {
							if tooLarge || readError != "" {
								return HTTPResponse{Status: 400}
							}
							return HTTPResponse{Status: 200, Body: body}
						})
					})
					b.Cleanup(h.Stop)
				} else {
					srv := &http.Server{MaxHeaderBytes: 4096, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					b.Cleanup(func() { _ = srv.Shutdown(context.Background()); <-done })
				}
				transport := &http.Transport{Proxy: nil}
				b.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport}
				url := "http://" + ln.Addr().String() + "/echo"
				body := strings.Repeat("x", 256)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					r, err := client.Post(url, "text/plain", strings.NewReader(body))
					if err != nil {
						b.Fatal(err)
					}
					_, err = io.Copy(io.Discard, r.Body)
					r.Body.Close()
					if err != nil || r.StatusCode != 200 {
						b.Fatalf("response %d: %v", r.StatusCode, err)
					}
				}
			})
		}
	}
}
